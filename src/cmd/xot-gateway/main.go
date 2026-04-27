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
	statsPort   = flag.Int("stats-port", 0, "Port for /varz stats (0 to disable)")

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

        actualStatsPort := *statsPort
        if actualStatsPort == 0 && cm != nil {
                actualStatsPort = cm.GetXotGatewayConfig().StatsPort
        }
        if actualStatsPort > 0 {
                xot.StartStatsServer(actualStatsPort)
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
		xot.ThreadsActive.Add("shutdown_handler", 1)
		defer xot.ThreadsActive.Add("shutdown_handler", -1)
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
			xot.InterfaceSessionsOpened.Add("unix", 1)
			defer xot.InterfaceSessionsClosed.Add("unix", 1)
			xot.ThreadsActive.Add("gateway_conn_handler", 1)
			defer xot.ThreadsActive.Add("gateway_conn_handler", -1)
			handleGatewayConn(conn, cm, stop)
			activeConns.Delete(conn)
			wg.Done()
		}()
	}
}

func handleGatewayConn(conn net.Conn, cm *xot.ConfigManager, stop chan struct{}) {
	defer conn.Close()
	fd := xot.GetFd(conn)
	source := fmt.Sprintf("LOCAL(%d)", fd)

	for {
		data, err := xot.ReadXot("unix", conn)
		if err != nil {
			if errors.Is(err, xot.ErrPacketTooLong) {
				log.Printf("%s: %v", source, err)
				xot.CausesGenerated.Add("packet_too_long", 1)
				pkt, _ := xot.ParseX25(data)
				lci_err := uint16(0)
				if pkt != nil {
					lci_err = pkt.LCI
				}
				clr := xot.CreateClearRequest(lci_err, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
				xot.SendXot("unix", conn, clr.Serialize())
			} else if err != io.EOF && !errors.Is(err, net.ErrClosed) {
				log.Printf("%s: Error reading XOT: %v", source, err)
			}
			return
		}

		pkt, err := xot.ParseX25(data)
		if err != nil {
			log.Printf("%s: Error parsing X.25: %v", source, err)
			continue
		}
		xot.PacketsHandled.Add(pkt.TypeName(), 1)

		if err := pkt.ValidateSize(); err != nil {
			log.Printf("%s: %v", source, err)
			xot.CausesGenerated.Add("packet_too_long", 1)
			clr := xot.CreateClearRequest(pkt.LCI, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
			xot.SendXot("unix", conn, clr.Serialize())
			return
		}

		if pkt.GetBaseType() == xot.PktTypeClearRequest && len(pkt.Payload) >= 1 {
			xot.CausesReceived.Add(fmt.Sprintf("0x%02x", pkt.Payload[0]), 1)
		}

		if pkt.GetBaseType() != xot.PktTypeCallRequest {
			continue
		}

		lci := pkt.LCI
		called, calling, fac, _, err := pkt.ParseCallRequest()
		if err != nil {
			log.Printf("%s: Malformed CALL_REQ from source: %v", source, err)
			clr := xot.CreateClearRequest(lci, xot.CauseInvalidFacility, 0)
			xot.SendXot("unix", conn, clr.Serialize())
			return
		}
		log.Printf("%s: CALL_REQ from %s to %s (fac: %s)", source, calling, called, xot.FormatFacilities(fac))

		srv := cm.GetServer(called)
		if srv == nil {
			log.Printf("No route for %s", called)
			// Send Clear Request back to source - Use CauseNumberBusy (0x01) as per best practices
			clr := xot.CreateClearRequest(lci, xot.CauseNumberBusy, 0)
			xot.SendXot("unix", conn, clr.Serialize())
			return
		}

		// Resolve destination
		ips, err := xot.ResolveXotDestination(called, srv)
		if err != nil {
			log.Printf("%s: Destination resolution failed for %s: %v", source, called, err)
			clr := xot.CreateClearRequest(lci, xot.CauseNumberBusy, 0)
			xot.SendXot("unix", conn, clr.Serialize())
			return
		}

		var remoteConn net.Conn
		var connectedIP string

		for _, ip := range ips {
			addr := fmt.Sprintf("%s:%d", ip, srv.Port)
				c, err := net.DialTimeout("tcp", addr, 5*time.Second)
			if err == nil {
				xot.SetNoDelay(c)
				remoteConn = c
				connectedIP = ip
				break
			}
			log.Printf("%s: Connection to %s failed: %v", source, addr, err)
		}

		if remoteConn == nil {
			log.Printf("%s: All connection attempts failed for %s", source, called)
			clr := xot.CreateClearRequest(lci, xot.CauseOutofOrder, 0)
			xot.SendXot("unix", conn, clr.Serialize())
			return
		}

		func() {
			xot.InterfaceSessionsOpened.Add("xot", 1)
			defer xot.InterfaceSessionsClosed.Add("xot", 1)
			defer remoteConn.Close()
			dest := fmt.Sprintf("XOT(%s)", connectedIP)
			log.Printf("%s: Connected to %s", source, dest)
			defer log.Printf("%s: Connection to %s closed", source, dest)

			if *trace {
				xot.LogTrace(source, dest, pkt)
			}

			// Forward initial packet
			xot.SendXot("xot", remoteConn, data)

			// Bidirectional relay
			relayQuit := make(chan struct{})
			var closeOnce sync.Once
			closeRelay := func() {
				closeOnce.Do(func() {
					close(relayQuit)
				})
			}
			var relayWg sync.WaitGroup
			relayWg.Add(2)
			
			// Relay from remote to local
			go func() {
				xot.ThreadsActive.Add("relay_remote_to_local", 1)
				defer xot.ThreadsActive.Add("relay_remote_to_local", -1)
				defer relayWg.Done()
				buf := xot.GetBuffer()
				defer xot.PutBuffer(buf)
				for {
					d, err := xot.ReadXotInto("xot", remoteConn, buf)
					if err != nil {
						if errors.Is(err, xot.ErrPacketTooLong) {
							log.Printf("%s: %v from remote", source, err)
							xot.CausesGenerated.Add("packet_too_long", 1)
							lci_err := xot.GetLCI(d)
							clr := xot.CreateClearRequest(lci_err, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
							xot.SendXot("unix", conn, clr.Serialize())
						} else if err != io.EOF && !errors.Is(err, net.ErrClosed) {
							log.Printf("%s: Error reading from remote: %v", source, err)
						}
						closeRelay()
						return
					}

					pktType := xot.GetPacketType(d)
					pktTypeName := xot.GetPacketTypeName(pktType)
					if *trace {
						xot.LogTraceRaw(dest, source, d)
					}

					xot.PacketsHandled.Add(pktTypeName, 1)
					pLCI := xot.GetLCI(d)
					if pLCI != lci {
						log.Printf("%s: Mismatched LCI %d from remote (expected %d) - ignoring", source, pLCI, lci)
						continue
					}

					if pktType == xot.PktTypeCallConnected {
						log.Printf("%s: Call connected on LCI %d", source, lci)
					} else if pktType == xot.PktTypeClearRequest || pktType == xot.PktTypeClearConfirm {
						log.Printf("%s: Call cleared on LCI %d (type: %s)", source, lci, pktTypeName)
						if pktType == xot.PktTypeClearRequest && len(d) >= 4 {
							xot.CausesReceived.Add(fmt.Sprintf("0x%02x", d[3]), 1)
						}
						// Forward the clear packet before exiting
						xot.SendXot("unix", conn, d)
						closeRelay()
						return
					}

					xot.SendXot("unix", conn, d)
				}
			}()

			// Relay from local to remote
			go func() {
				xot.ThreadsActive.Add("relay_local_to_remote", 1)
				defer xot.ThreadsActive.Add("relay_local_to_remote", -1)
				defer relayWg.Done()
				buf := xot.GetBuffer()
				defer xot.PutBuffer(buf)
				for {
					d, err := xot.ReadXotInto("unix", conn, buf)
					if err != nil {
						if errors.Is(err, xot.ErrPacketTooLong) {
							log.Printf("%s: %v from local", source, err)
							xot.CausesGenerated.Add("packet_too_long", 1)
							lci_err := xot.GetLCI(d)
							clr := xot.CreateClearRequest(lci_err, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
							xot.SendXot("unix", conn, clr.Serialize())
						} else if err != io.EOF && !errors.Is(err, net.ErrClosed) {
							log.Printf("%s: Error reading from local: %v", source, err)
						}
						closeRelay()
						return
					}

					pktType := xot.GetPacketType(d)
					pktTypeName := xot.GetPacketTypeName(pktType)
					if *trace {
						xot.LogTraceRaw(source, dest, d)
					}

					xot.PacketsHandled.Add(pktTypeName, 1)
					pLCI := xot.GetLCI(d)
					if pLCI != lci {
						log.Printf("%s: Mismatched LCI %d from local (expected %d) - ignoring", source, pLCI, lci)
						continue
					}

					if pktType == xot.PktTypeClearRequest || pktType == xot.PktTypeClearConfirm {
						log.Printf("%s: Call cleared on LCI %d (type: %s)", source, lci, pktTypeName)
						if pktType == xot.PktTypeClearRequest && len(d) >= 4 {
							xot.CausesReceived.Add(fmt.Sprintf("0x%02x", d[3]), 1)
						}
						// Forward the clear packet before exiting
						xot.SendXot("xot", remoteConn, d)
						closeRelay()
						return
					}

					xot.SendXot("xot", remoteConn, d)
				}
			}()

			select {
			case <-relayQuit:
				// One side closed naturally
			case <-stop:
				// Shutdown triggered
				xot.CausesGenerated.Add(fmt.Sprintf("0x%02x", xot.CauseOutofOrder), 1)
				clr := xot.CreateClearRequest(lci, xot.CauseOutofOrder, 0)
				if *trace {
					xot.LogTrace("SHUTDOWN", source, clr)
				}
				xot.SendXot("unix", conn, clr.Serialize())
			}
			
			// Ensure both goroutines exit by closing connections
			remoteConn.Close()
			conn.Close()
			relayWg.Wait()
		}()
		
		if shuttingDown.Load() {
			return
		}
	}
}
