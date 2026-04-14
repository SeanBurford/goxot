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
	listenAddr  = flag.String("listen", "0.0.0.0:1998", "XOT listen address")
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

	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", *listenAddr, err)
	}
	log.Printf("xot-server listening on %s", *listenAddr)

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
			
			// We don't know the LCI here easily without more tracking, 
			// but we can at least close the connection.
			// Actually, we should try to send a Clear Request if we can.
			// For now, just close the connection as handleIncomingXot will handle it.
			conn.Close()
			return true
		})
		
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
			handleIncomingXot(conn, cm, stop)
			activeConns.Delete(conn)
			wg.Done()
		}()
	}
}

func handleIncomingXot(conn net.Conn, cm *xot.ConfigManager, stop chan struct{}) {
	defer conn.Close()
	remoteAddr := conn.RemoteAddr().String()
	log.Printf("XOT: New session from %s", remoteAddr)
	defer log.Printf("XOT: Session from %s closed", remoteAddr)

	host, _, _ := net.SplitHostPort(remoteAddr)
	source := fmt.Sprintf("XOT(%s)", host)

	for {
		data, err := xot.ReadXot(conn)
		if err != nil {
			if errors.Is(err, xot.ErrPacketTooLong) {
				log.Printf("%s: %v", source, err)
				pkt, _ := xot.ParseX25(data)
				lci := uint16(0)
				if pkt != nil {
					lci = pkt.LCI
				}
				clr := xot.CreateClearRequest(lci, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
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
			log.Printf("Received non-CallRequest from %s - ignoring", source)
			continue
		}

		lci := pkt.LCI
		called, calling, err := pkt.ParseCallRequest()
		if err != nil {
			continue
		}
		log.Printf("%s: CALL_REQ from %s to %s", source, calling, called)

		var destConn net.Conn
		var destName string

		if cm.GetServer(called) != nil {
			// Route to xot-gateway
			destConn, err = net.Dial("unixpacket", "/tmp/xot_gwy.sock")
			destName = "GWY"
		} else {
			// Route to tun-gateway
			destConn, err = net.Dial("unixpacket", "/tmp/xot_tun.sock")
			destName = "TUN"
		}

		if err != nil {
			log.Printf("Failed to connect to %s gateway: %v", destName, err)
			// Send Clear Request back to source
			clr := xot.CreateClearRequest(lci, xot.CauseOutofOrder, 0)
			xot.SendXot(conn, clr.Serialize())
			return
		}
		
		// Inner function to handle the relay so we can defer destConn.Close() properly
		func() {
			defer destConn.Close()
			dest := fmt.Sprintf("%s(%d)", destName, xot.GetFd(destConn))

			if *trace {
				xot.LogTrace(source, dest, pkt)
			}

			// Forward initial packet
			xot.SendXot(destConn, data)

			// Bidirectional relay
			relayQuit := make(chan struct{})
			
			// Relay from destination to source
			go func() {
				for {
					d, err := xot.ReadXot(destConn)
					if err != nil {
						if errors.Is(err, xot.ErrPacketTooLong) {
							log.Printf("%s: %v from %s", source, err, destName)
							pkt, _ := xot.ParseX25(d)
							lci_err := uint16(0)
							if pkt != nil {
								lci_err = pkt.LCI
							}
							clr := xot.CreateClearRequest(lci_err, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
							xot.SendXot(conn, clr.Serialize())
						} else if err != io.EOF {
							log.Printf("%s: Error reading from %s: %v", source, destName, err)
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
							log.Printf("%s: %v from %s", source, err, destName)
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

			// Relay from source to destination
			go func() {
				for {
					d, err := xot.ReadXot(conn)
					if err != nil {
						if errors.Is(err, xot.ErrPacketTooLong) {
							log.Printf("%s: %v from source", source, err)
							pkt, _ := xot.ParseX25(d)
							lci_err := uint16(0)
							if pkt != nil {
								lci_err = pkt.LCI
							}
							clr := xot.CreateClearRequest(lci_err, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
							xot.SendXot(conn, clr.Serialize())
						} else if err != io.EOF {
							log.Printf("%s: Error reading from source: %v", source, err)
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
							xot.LogTrace(source, dest, p)
						} else {
							log.Printf("%s>%s UNKNOWN % X", source, dest, d)
						}
					}

					p, _ := xot.ParseX25(d)
					if p != nil {
						if err := p.ValidateSize(); err != nil {
							log.Printf("%s: %v from source", source, err)
							clr := xot.CreateClearRequest(p.LCI, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
							xot.SendXot(conn, clr.Serialize())
							return
						}
						if p.GetBaseType() == xot.PktTypeClearRequest {
							log.Printf("%s: Call cleared on LCI %d", source, lci)
						}
					}

					xot.SendXot(destConn, d)
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
