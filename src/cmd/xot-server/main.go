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
	statsPort   = flag.Int("stats-port", 0, "Port for /varz stats (0 to disable)")

	shuttingDown atomic.Bool
	activeConns  sync.Map // net.Conn -> chan struct{} (stop channel)
	wg           sync.WaitGroup
)

func main() {
	flag.Parse()

	if *statsPort > 0 {
		xot.StartStatsServer(*statsPort)
	}

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
		xot.ThreadStarts.Add("shutdown_handler", 1)
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
		xot.SessionsOpened.Add(1)
		wg.Add(1)
		stop := make(chan struct{})
		activeConns.Store(conn, stop)
		go func() {
			xot.ThreadStarts.Add("incoming_xot_handler", 1)
			xot.ThreadsActive.Add("incoming_xot_handler", 1)
			defer xot.ThreadsActive.Add("incoming_xot_handler", -1)
			handleIncomingXot(conn, cm, stop)
			activeConns.Delete(conn)
			xot.SessionsClosed.Add(1)
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
				xot.CausesGenerated.Add("packet_too_long", 1)
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
		xot.BytesReceived.Add("XOT", int64(len(data)))

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
			xot.SendXot(conn, clr.Serialize())
			return
		}

		if pkt.GetBaseType() == xot.PktTypeClearRequest && len(pkt.Payload) >= 1 {
			xot.CausesReceived.Add(fmt.Sprintf("0x%02x", pkt.Payload[0]), 1)
		}

		if pkt.GetBaseType() != xot.PktTypeCallRequest {
			log.Printf("Received non-CallRequest from %s - ignoring", source)
			continue
		}
		xot.CallsReceived.Add("XOT", 1)

		lci := pkt.LCI
		called, calling, fac, _, err := pkt.ParseCallRequest()
		if err != nil {
			continue
		}
		log.Printf("%s: CALL_REQ from %s to %s (fac: %s)", source, calling, called, xot.FormatFacilities(fac))

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
			xot.CallsSent.Add(destName, 1)
			xot.BytesSent.Add(destName, int64(len(data)))

			// Bidirectional relay
			relayQuit := make(chan struct{})
			var relayWg sync.WaitGroup
			relayWg.Add(2)
			
			// Relay from destination to source
			go func() {
				xot.ThreadStarts.Add("relay_dest_to_source", 1)
				xot.ThreadsActive.Add("relay_dest_to_source", 1)
				defer xot.ThreadsActive.Add("relay_dest_to_source", -1)
				defer relayWg.Done()
				for {
					d, err := xot.ReadXot(destConn)
					if err != nil {
						if errors.Is(err, xot.ErrPacketTooLong) {
							log.Printf("%s: %v from %s", source, err, destName)
							xot.CausesGenerated.Add("packet_too_long", 1)
							pkt, _ := xot.ParseX25(d)
							lci_err := uint16(0)
							if pkt != nil {
								lci_err = pkt.LCI
							}
							clr := xot.CreateClearRequest(lci_err, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
							xot.SendXot(conn, clr.Serialize())
						} else if err != io.EOF && !errors.Is(err, net.ErrClosed) {
							log.Printf("%s: Error reading from %s: %v", source, destName, err)
						}
						select {
						case <-relayQuit:
						default:
							close(relayQuit)
						}
						return
					}
					xot.BytesReceived.Add(destName, int64(len(d)))

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
						xot.PacketsHandled.Add(p.TypeName(), 1)
						if p.LCI != lci {
							log.Printf("%s: Mismatched LCI %d from %s (expected %d) - ignoring", source, p.LCI, destName, lci)
							continue
						}
						if err := p.ValidateSize(); err != nil {
							log.Printf("%s: %v from %s", source, err, destName)
							xot.CausesGenerated.Add("packet_too_long", 1)
							clr := xot.CreateClearRequest(p.LCI, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
							xot.SendXot(conn, clr.Serialize())
							return
						}
						if p.GetBaseType() == xot.PktTypeCallConnected {
							log.Printf("%s: Call connected on LCI %d", source, lci)
							xot.CallsReceived.Add(destName, 1)
						} else if p.GetBaseType() == xot.PktTypeClearRequest || p.GetBaseType() == xot.PktTypeClearConfirm {
							log.Printf("%s: Call cleared on LCI %d (type: %s)", source, lci, p.TypeName())
							if p.GetBaseType() == xot.PktTypeClearRequest && len(p.Payload) >= 1 {
								xot.CausesReceived.Add(fmt.Sprintf("0x%02x", p.Payload[0]), 1)
							}
							// Forward the clear packet before exiting
							xot.SendXot(conn, d)
							xot.BytesSent.Add("XOT", int64(len(d)))
							select {
							case <-relayQuit:
							default:
								close(relayQuit)
							}
							return
						}
						if *trace {
							xot.LogTrace(dest, source, p)
						}
					}

					xot.SendXot(conn, d)
					xot.BytesSent.Add("XOT", int64(len(d)))
				}
			}()

			// Relay from source to destination
			go func() {
				xot.ThreadStarts.Add("relay_source_to_dest", 1)
				xot.ThreadsActive.Add("relay_source_to_dest", 1)
				defer xot.ThreadsActive.Add("relay_source_to_dest", -1)
				defer relayWg.Done()
				for {
					d, err := xot.ReadXot(conn)
					if err != nil {
						if errors.Is(err, xot.ErrPacketTooLong) {
							log.Printf("%s: %v from source", source, err)
							xot.CausesGenerated.Add("packet_too_long", 1)
							pkt, _ := xot.ParseX25(d)
							lci_err := uint16(0)
							if pkt != nil {
								lci_err = pkt.LCI
							}
							clr := xot.CreateClearRequest(lci_err, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
							xot.SendXot(conn, clr.Serialize())
						} else if err != io.EOF && !errors.Is(err, net.ErrClosed) {
							log.Printf("%s: Error reading from source: %v", source, err)
						}
						select {
						case <-relayQuit:
						default:
							close(relayQuit)
						}
						return
					}
					xot.BytesReceived.Add("XOT", int64(len(d)))

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
						xot.PacketsHandled.Add(p.TypeName(), 1)
						if p.LCI != lci {
							log.Printf("%s: Mismatched LCI %d from source (expected %d) - ignoring", source, p.LCI, lci)
							continue
						}
						if err := p.ValidateSize(); err != nil {
							log.Printf("%s: %v from source", source, err)
							xot.CausesGenerated.Add("packet_too_long", 1)
							clr := xot.CreateClearRequest(p.LCI, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
							xot.SendXot(conn, clr.Serialize())
							return
						}
						if p.GetBaseType() == xot.PktTypeClearRequest || p.GetBaseType() == xot.PktTypeClearConfirm {
							log.Printf("%s: Call cleared on LCI %d (type: %s)", source, lci, p.TypeName())
							if p.GetBaseType() == xot.PktTypeClearRequest && len(p.Payload) >= 1 {
								xot.CausesReceived.Add(fmt.Sprintf("0x%02x", p.Payload[0]), 1)
							}
							// Forward the clear packet before exiting
							xot.SendXot(destConn, d)
							xot.BytesSent.Add(destName, int64(len(d)))
							select {
							case <-relayQuit:
							default:
								close(relayQuit)
							}
							return
						}
						if *trace {
							xot.LogTrace(source, dest, p)
						}
					}

					xot.SendXot(destConn, d)
					xot.BytesSent.Add(destName, int64(len(d)))
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
				xot.SendXot(conn, clr.Serialize())
			}
			
			// Ensure both goroutines exit by closing connections
			destConn.Close()
			conn.Close()
			relayWg.Wait()
		}()
		
		if shuttingDown.Load() {
			return
		}
	}
}
