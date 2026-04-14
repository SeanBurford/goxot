package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	xot "github.com/SeanBurford/goxot"
)

var (
	configPath  = flag.String("config", "config.json", "Path to config file")
	trace       = flag.Bool("trace", false, "Enable trace logging")
	gracePeriod = flag.Int("graceperiod", 5, "Grace period in seconds for SIGHUP shutdown")

	shuttingDown atomic.Bool
	activeConns  sync.Map // net.Conn -> chan struct{} (stop channel)
	wg           sync.WaitGroup
)

func main() {
	flag.Parse()

	cm, err := xot.NewConfigManager(*configPath)
	if err != nil {
		log.Printf("Warning: Failed to initialize config manager: %v", err)
	}
	if cm != nil {
		if _, err := cm.Reload(); err != nil {
			log.Printf("Warning: Failed to load config: %v", err)
		}
	}

	sockPath := "/tmp/xot_gwy.sock"
	os.Remove(sockPath)
	ln, err := net.Listen("unixpacket", sockPath)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", sockPath, err)
	}
	log.Printf("xot-gateway listening on %s", sockPath)

	// Signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Printf("Received signal %v, starting shutdown...", sig)
		shuttingDown.Store(true)
		ln.Close() // Stop accepting new connections

		if sig == syscall.SIGHUP {
			log.Printf("Graceful shutdown: waiting up to %d seconds...", *gracePeriod)
			
			// Wait for grace period or all connections to finish
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()

			select {
			case <-done:
				log.Printf("All connections finished.")
			case <-time.After(time.Duration(*gracePeriod) * time.Second):
				log.Printf("Grace period expired, closing remaining connections.")
			}
		} else {
			log.Printf("Immediate shutdown.")
		}

		// Close all active connections with Clear Request
		activeConns.Range(func(key, value interface{}) bool {
			conn := key.(net.Conn)
			stop := value.(chan struct{})
			
			// Signal the relay loop to stop
			select {
			case <-stop:
			default:
				close(stop)
			}
			
			conn.Close()
			return true
		})
		
		os.Remove(sockPath)
		os.Exit(0)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if shuttingDown.Load() {
				return
			}
			continue
		}
		wg.Add(1)
		stop := make(chan struct{})
		activeConns.Store(conn, stop)
		go func() {
			handleGatewayConn(conn, cm, stop)
			activeConns.Delete(conn)
			wg.Done()
		}()
	}
}

func handleGatewayConn(conn net.Conn, cm *xot.ConfigManager, stop chan struct{}) {
	defer conn.Close()
	fd := xot.GetFd(conn)
	log.Printf("LOCAL: New session on FD %d", fd)
	defer log.Printf("LOCAL: Session on FD %d closed", fd)
	source := fmt.Sprintf("LOCAL(%d)", fd)

	for {
		data, err := xot.ReadXot(conn)
		if err != nil {
			if errors.Is(err, xot.ErrPacketTooLong) {
				log.Printf("%s: %v", source, err)
				pkt, _ := xot.ParseX25(data)
				lci_err := uint16(0)
				if pkt != nil {
					lci_err = pkt.LCI
				}
				clr := xot.CreateClearRequest(lci_err, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
				xot.SendXot(conn, clr.Serialize())
			} else if err != io.EOF {
				log.Printf("%s: Error reading XOT: %v", source, err)
			}
			return
		}

		pkt, err := xot.ParseX25(data)
		if err != nil {
			log.Printf("%s: Error parsing X.25: %v", source, err)
			continue
		}

		if err := pkt.ValidateSize(); err != nil {
			log.Printf("%s: %v", source, err)
			clr := xot.CreateClearRequest(pkt.LCI, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
			xot.SendXot(conn, clr.Serialize())
			return
		}

		if pkt.GetBaseType() != xot.PktTypeCallRequest {
			continue
		}

		lci := pkt.LCI
		called, calling, err := pkt.ParseCallRequest()
		if err != nil {
			continue
		}
		log.Printf("%s: CALL_REQ from %s to %s", source, calling, called)

		srv := cm.GetServer(called)
		if srv == nil {
			log.Printf("No route for %s", called)
			// Send Clear Request back to source
			clr := xot.CreateClearRequest(lci, xot.CauseOutofOrder, 0)
			xot.SendXot(conn, clr.Serialize())
			return
		}

		// Resolve destination
		ips, err := xot.ResolveXotDestination(called, srv)
		if err != nil {
			log.Printf("%s: Destination resolution failed for %s: %v", source, called, err)
			clr := xot.CreateClearRequest(lci, xot.CauseOutofOrder, 0)
			xot.SendXot(conn, clr.Serialize())
			return
		}

		var remoteConn net.Conn
		var connectedIP string

		for _, ip := range ips {
			addr := fmt.Sprintf("%s:%d", ip, srv.Port)
			log.Printf("%s: Attempting connection to %s (for %s)", source, addr, called)
			c, err := net.DialTimeout("tcp", addr, 5*time.Second)
			if err == nil {
				remoteConn = c
				connectedIP = ip
				break
			}
			log.Printf("%s: Connection to %s failed: %v", source, addr, err)
		}

		if remoteConn == nil {
			log.Printf("%s: All connection attempts failed for %s", source, called)
			clr := xot.CreateClearRequest(lci, xot.CauseOutofOrder, 0)
			xot.SendXot(conn, clr.Serialize())
			return
		}

		func() {
			defer remoteConn.Close()
			dest := fmt.Sprintf("XOT(%s)", connectedIP)
			log.Printf("%s: Connected to %s", source, dest)
			defer log.Printf("%s: Connection to %s closed", source, dest)

			if *trace {
				xot.LogTrace(source, dest, pkt)
			}

			// Forward initial packet
			xot.SendXot(remoteConn, data)

			// Bidirectional relay
			relayQuit := make(chan struct{})
			
			// Relay from remote to local
			go func() {
				for {
					d, err := xot.ReadXot(remoteConn)
					if err != nil {
						if errors.Is(err, xot.ErrPacketTooLong) {
							log.Printf("%s: %v from remote", source, err)
							pkt, _ := xot.ParseX25(d)
							lci_err := uint16(0)
							if pkt != nil {
								lci_err = pkt.LCI
							}
							clr := xot.CreateClearRequest(lci_err, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
							xot.SendXot(conn, clr.Serialize())
						} else if err != io.EOF {
							log.Printf("%s: Error reading from remote: %v", source, err)
						}
						select {
						case <-relayQuit:
						default:
							close(relayQuit)
						}
						return
					}
					if *trace {
						p, _ := xot.ParseX25(d)
						if p != nil {
							xot.LogTrace(dest, source, p)
						} else {
							log.Printf("%s>%s UNKNOWN % X", dest, source, d)
						}
					}

					p, _ := xot.ParseX25(d)
					if p != nil {
						if err := p.ValidateSize(); err != nil {
							log.Printf("%s: %v from remote", source, err)
							clr := xot.CreateClearRequest(p.LCI, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
							xot.SendXot(conn, clr.Serialize())
							return
						}
						if p.GetBaseType() == xot.PktTypeCallConnected {
							log.Printf("%s: Call connected on LCI %d", source, lci)
						} else if p.GetBaseType() == xot.PktTypeClearRequest {
							log.Printf("%s: Call cleared on LCI %d", source, lci)
						}
					}

					xot.SendXot(conn, d)
				}
			}()

			// Relay from local to remote
			go func() {
				for {
					d, err := xot.ReadXot(conn)
					if err != nil {
						if errors.Is(err, xot.ErrPacketTooLong) {
							log.Printf("%s: %v from local", source, err)
							pkt, _ := xot.ParseX25(d)
							lci_err := uint16(0)
							if pkt != nil {
								lci_err = pkt.LCI
							}
							clr := xot.CreateClearRequest(lci_err, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
							xot.SendXot(conn, clr.Serialize())
						} else if err != io.EOF {
							log.Printf("%s: Error reading from local: %v", source, err)
						}
						select {
						case <-relayQuit:
						default:
							close(relayQuit)
						}
						return
					}
					p, _ := xot.ParseX25(d)
					if p != nil {
						if err := p.ValidateSize(); err != nil {
							log.Printf("%s: %v from local", source, err)
							clr := xot.CreateClearRequest(p.LCI, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
							xot.SendXot(conn, clr.Serialize())
							return
						}
						if p.GetBaseType() == xot.PktTypeCallRequest {
							c, _, _ := p.ParseCallRequest()
							if c != called {
								log.Printf("Rejecting subsequent call to different destination: %s", c)
								continue
							}
						}
						if *trace {
							xot.LogTrace(source, dest, p)
						}
						if p.GetBaseType() == xot.PktTypeClearRequest {
							log.Printf("%s: Call cleared on LCI %d", source, lci)
						}
					} else if *trace {
						log.Printf("%s>%s UNKNOWN % X", source, dest, d)
					}

					xot.SendXot(remoteConn, d)
				}
			}()

			select {
			case <-relayQuit:
				// One side closed naturally
			case <-stop:
				// Shutdown triggered
				clr := xot.CreateClearRequest(lci, xot.CauseOutofOrder, 0)
				if *trace {
					xot.LogTrace("SHUTDOWN", source, clr)
				}
				xot.SendXot(conn, clr.Serialize())
			}
		}()
		
		if shuttingDown.Load() {
			return
		}
	}
}
