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
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	xot "github.com/SeanBurford/goxot"
	"github.com/SeanBurford/goxot/tun"
)

var (
	tunName    = flag.String("tun", "tun0", "TUN interface name")
	configPath = flag.String("config", "config.json", "Path to config file")
	trace      = flag.Bool("trace", false, "Enable trace logging")
	statsPort  = flag.Int("stats-port", 0, "Port for /varz stats (0 to disable)")
)

const (
	LinkStateDown        = int32(0)
	LinkStateConnecting  = int32(1)
	LinkStateOperational = int32(3)
)

type TunGateway struct {
	ifce *tun.Interface
	cm   *xot.ConfigManager
	sm   *xot.SessionManager

	routeMu       sync.Mutex
	currentRoutes map[string]int // prefix -> digits

	linkState    int32 // atomic: 0=Down, 1=Connecting, 3=Operational
	shuttingDown int32 // atomic: 1 = shutting down
}

func (tg *TunGateway) getTunLCI(conn net.Conn, incomingLCI uint16) uint16 {
	s, err := tg.sm.AllocateAndAddTunSession(conn, incomingLCI)
	if err != nil {
		log.Printf("TUN: %v", err)
		return 0
	}
	return s.LciA
}

func (tg *TunGateway) cleanupConn(conn net.Conn) {
	sessions := tg.sm.GetSessionsForConn(conn)
	for _, s := range sessions {
		// SESS004: Only send CLEAR if there is kernel-side state (not StateP1)
		// AND only if this session is still mapped to the LCI (ABA protection).
		if s.State != xot.StateP1 && tg.sm.GetByALCI(s.LciA) == s {
			if *trace {
				log.Printf("TUN: Cleaning up LCI %d - sending CLEAR_REQ to kernel", s.LciA)
			}
			clr := xot.CreateClearRequest(s.LciA, xot.CauseOutofOrder, 0)
			tun.WriteFrame(tg.ifce, *tunName, tun.HeaderData, clr.Serialize())
		}
		tg.sm.RemoveSession(s)
	}
}

func (tg *TunGateway) closeAllSessions() {
	// SESS005: Atomically remove all sessions to prevent races.
	sessions := tg.sm.RemoveAllSessions()
	for _, s := range sessions {
		if s.ConnB != nil {
			clr := xot.CreateClearRequest(s.LciB, xot.CauseNetworkCongestion, 0)
			xot.SendXot("xot", s.ConnB, clr.Serialize())
		}
	}
	log.Printf("TUN: All %d sessions cleared", len(sessions))
}

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
		actualStatsPort = cm.GetTunGatewayConfig().StatsPort
	}
	if actualStatsPort > 0 {
		xot.StartStatsServer(actualStatsPort)
	}

	var tunCfg xot.TunConfig
	if cm != nil {
		tunCfg = cm.GetTunGatewayConfig().TunConfig
	} else {
		tunCfg = xot.TunConfig{LciStart: 1024, LciEnd: 4095}
	}

	ifce, err := tun.Setup(*tunName)
	if err != nil {
		log.Fatalf("Failed to setup TUN: %v", err)
	}

	if err := tun.SetSubscription(*tunName, tunCfg.LciStart, tunCfg.LciEnd); err != nil {
		log.Printf("Warning: failed to set X.25 subscription: %v", err)
	}

	tg := &TunGateway{
		ifce:          ifce,
		cm:            cm,
		sm:            xot.NewSessionManager(uint16(tunCfg.LciStart), uint16(tunCfg.LciEnd)),
		currentRoutes: make(map[string]int),
		linkState:     LinkStateDown,
	}

	tg.SyncRoutes()

	// Proactively establish link layer (COMPAT003).
	log.Printf("TUN: Proactively establishing link layer")
	tun.WriteFrame(ifce, *tunName, tun.HeaderConnect, nil)
	atomic.StoreInt32(&tg.linkState, LinkStateConnecting)

	go func() {
		xot.ThreadsActive.Add("watch_config", 1)
		defer xot.ThreadsActive.Add("watch_config", -1)
		tg.watchConfig()
	}()

	sockPath := "/tmp/xot_tun.sock"
	os.Remove(sockPath)
	ln, err := net.Listen("unixpacket", sockPath)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", sockPath, err)
	}
	log.Printf("tun-gateway listening on %s", sockPath)

	go func() {
		xot.ThreadsActive.Add("tun_read_handler", 1)
		defer xot.ThreadsActive.Add("tun_read_handler", -1)
		tg.handleTunRead()
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		xot.ThreadsActive.Add("signal_handler", 1)
		defer xot.ThreadsActive.Add("signal_handler", -1)
		<-sigChan
		log.Printf("TUN: Shutting down - cleaning up sessions")
		atomic.StoreInt32(&tg.shuttingDown, 1)
		ln.Close()
		tg.closeAllSessions()
		tun.WriteFrame(ifce, *tunName, tun.HeaderDisconnect, nil)
		ifce.Close()
		os.Remove(sockPath)
		os.Exit(0)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		xot.InterfaceSessionsOpened.Add("unix", 1)
		go func() {
			xot.ThreadsActive.Add("server_conn_handler", 1)
			defer xot.ThreadsActive.Add("server_conn_handler", -1)
			tg.handleServerConn(conn)
			xot.InterfaceSessionsClosed.Add("unix", 1)
		}()
	}
}

func (tg *TunGateway) handleServerConn(conn net.Conn) {
	defer conn.Close()
	defer tg.cleanupConn(conn)
	fd := xot.GetFd(conn)
	source := fmt.Sprintf("SVR(%d)", fd)
	tunDest := fmt.Sprintf("TUN(%d)", tg.ifce.Fd())

	buf := xot.GetBuffer()
	defer xot.PutBuffer(buf)
	for {
		data, err := xot.ReadXotInto("unix", conn, buf)
		if err != nil {
			if errors.Is(err, xot.ErrPacketTooLong) {
				log.Printf("%s: %v", source, err)
				xot.CausesGenerated.Add("packet_too_long", 1)
				lciErr := xot.GetLCI(data)
				clr := xot.CreateClearRequest(lciErr, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
				xot.SendXot("unix", conn, clr.Serialize())
			} else if err != io.EOF {
				log.Printf("%s: Error reading XOT: %v", source, err)
			}
			return
		}

		pktType := xot.GetPacketType(data)
		pktTypeName := xot.GetPacketTypeName(pktType)
		xot.PacketsHandled.Add(pktTypeName, 1)

		incomingLCI := xot.GetLCI(data)

		// ABA: if a new CALL_REQ arrives on an LCI we think is still active, force-remove
		// the old session so a fresh one is allocated.
		if pktType == xot.PktTypeCallRequest {
			if tg.sm.GetByBConnLCI(conn, incomingLCI) != nil {
				log.Printf("%s: Forced removal of old session for LCI %d - new CALL_REQ arrived", source, incomingLCI)
				tg.sm.RemoveByBConnLCI(conn, incomingLCI)
			}
		}

		if pktType == xot.PktTypeClearRequest || pktType == xot.PktTypeClearConfirm {
			log.Printf("%s: Call cleared on LCI %d (type: %s)", source, incomingLCI, pktTypeName)
			if pktType == xot.PktTypeClearRequest && len(data) >= 4 {
				xot.CausesReceived.Add(fmt.Sprintf("0x%02x", data[3]), 1)
			}
			s := tg.sm.GetByBConnLCI(conn, incomingLCI)
			if s != nil {
				s.SetState(xot.StateP5)
				data[0] = (data[0] & 0xF0) | byte((s.LciA>>8)&0x0F)
				data[1] = byte(s.LciA & 0xFF)
				tun.WriteFrame(tg.ifce, *tunName, tun.HeaderData, data)
				if pktType == xot.PktTypeClearConfirm {
					tg.sm.RemoveSession(s)
				}
			} else {
				log.Printf("%s: Received CLEAR for unknown LCI %d", source, incomingLCI)
			}
			continue
		}

		if *trace {
			xot.LogTraceRaw(source, tunDest, data)
		}

		if atomic.LoadInt32(&tg.linkState) != LinkStateOperational {
			log.Printf("%s: Dropping packet for LCI %d - link not operational", source, incomingLCI)
			clr := xot.CreateClearRequest(incomingLCI, xot.CauseNetworkCongestion, 0)
			xot.SendXot("unix", conn, clr.Serialize())
			return
		}

		if atomic.LoadInt32(&tg.shuttingDown) == 1 {
			log.Printf("%s: Dropping packet for LCI %d - shutting down", source, incomingLCI)
			return
		}

		tunLCI := tg.getTunLCI(conn, incomingLCI)
		if tunLCI == 0 {
			log.Printf("%s: Failed to allocate tunLCI for incoming LCI %d", source, incomingLCI)
			clr := xot.CreateClearRequest(incomingLCI, xot.CauseNetworkCongestion, 0)
			xot.SendXot("unix", conn, clr.Serialize())
			return
		}

		s := tg.sm.GetByALCI(tunLCI)
		if s == nil {
			log.Printf("%s: Session for LCI %d lost mid-flight", source, tunLCI)
			return
		}

		data[0] = (data[0] & 0xF0) | byte((tunLCI>>8)&0x0F)
		data[1] = byte(tunLCI & 0xFF)
		tun.WriteFrame(tg.ifce, *tunName, tun.HeaderData, data)
	}
}

func (tg *TunGateway) handleTunRead() {
	tunFd := tg.ifce.Fd()
	tunSource := fmt.Sprintf("TUN(%d)", tunFd)
	packet := make([]byte, tun.MaxPacketSize)
	for {
		hdr, payload, err := tun.ReadFrame(tg.ifce, *tunName, packet)
		if err != nil {
			if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "closed") || strings.Contains(err.Error(), "bad file descriptor") {
				log.Printf("%s: Connection closed, exiting reader", tunSource)
				return
			}
			log.Printf("Error reading from TUN: %v", err)
			return
		}

		if len(payload) == 0 {
			if *trace {
				log.Printf("%s> Control Header (hdr=0x%02X, empty payload)", tunSource, hdr)
			}
			if hdr == tun.HeaderConnect {
				if atomic.LoadInt32(&tg.linkState) != LinkStateOperational {
					xot.InterfaceSessionsOpened.Add(*tunName, 1)
					if *trace {
						log.Printf("%s< Responding with Connect (0x01)", tunSource)
					}
					tun.WriteFrame(tg.ifce, *tunName, tun.HeaderConnect, nil)
					atomic.CompareAndSwapInt32(&tg.linkState, LinkStateDown, LinkStateConnecting)
				}
			} else if hdr == tun.HeaderDisconnect {
				log.Printf("%s: Received Disconnect from kernel - cleaning up all sessions", tunSource)
				xot.InterfaceSessionsClosed.Add(*tunName, 1)
				atomic.StoreInt32(&tg.linkState, LinkStateDown)
				tg.closeAllSessions()
			}
			continue
		}

		pktType := xot.GetPacketType(payload)
		pktTypeName := xot.GetPacketTypeName(pktType)
		xot.PacketsHandled.Add(pktTypeName, 1)

		if pktType == xot.PktTypeRestartRequest {
			currentState := atomic.LoadInt32(&tg.linkState)
			hasSessions := len(tg.sm.GetAllSessions()) > 0

			if currentState == LinkStateOperational {
				if hasSessions {
					// COMPAT005: genuine mid-session restart.
					log.Printf("%s> Genuine RESTART_REQ in STATE_3 - clearing sessions", tunSource)
					tg.closeAllSessions()
				} else {
					// COMPAT004: buffered duplicate from startup.
					if *trace {
						log.Printf("%s> Ignoring buffered RESTART_REQ duplicate", tunSource)
					}
					continue
				}
			}

			if *trace {
				log.Printf("%s> Sending RESTART_CONF", tunSource)
			}
			buf := make([]byte, 3)
			buf[0] = xot.GFIStandard << 4
			buf[1] = 0
			buf[2] = xot.PktTypeRestartConfirm
			tun.WriteFrame(tg.ifce, *tunName, tun.HeaderData, buf)

			if atomic.CompareAndSwapInt32(&tg.linkState, LinkStateConnecting, LinkStateOperational) {
				log.Printf("%s: Link Layer Operational (STATE_3)", tunSource)
			} else {
				atomic.StoreInt32(&tg.linkState, LinkStateOperational)
			}
			continue
		}

		pLCI := xot.GetLCI(payload)

		if pktType == xot.PktTypeCallRequest {
			if s := tg.sm.GetByALCI(pLCI); s != nil {
				if *trace {
					log.Printf("TUN: New CALL_REQ on busy LCI %d - clearing old session", pLCI)
				}
				tg.sm.RemoveSession(s)
			}

			xot.InterfaceCallRequest.Add(*tunName, 1)
			pkt, err := xot.ParseX25(payload)
			if err == nil {
				called, calling, fac, _, err := pkt.ParseCallRequest()
				if err == nil && tg.cm.GetServer(called) != nil {
					log.Printf("TUN: Intercepting CALL_REQ from %s to %s (fac: %s)", calling, called, xot.FormatFacilities(fac))
					// RACE-A: payload aliases the TUN read buffer; copy before spawning.
					pktData := make([]byte, len(payload))
					copy(pktData, payload)
					pktSafe, _ := xot.ParseX25(pktData)
					go tg.forwardToGateway(pktSafe)
					continue
				}
			}
		}

		s := tg.sm.GetByALCI(pLCI)

		if s != nil {
			remapped := make([]byte, len(payload))
			copy(remapped, payload)
			remapped[0] = (remapped[0] & 0xF0) | byte((s.LciB>>8)&0x0F)
			remapped[1] = byte(s.LciB & 0xFF)

			dest := fmt.Sprintf("SVR(%d)", xot.GetFd(s.ConnB))
			if *trace {
				xot.LogTraceRaw(tunSource, dest, remapped)
			}

			if pktType == xot.PktTypeCallConnected {
				log.Printf("TUN: Call connected on LCI %d", s.LciB)
				s.SetState(xot.StateP4)
				xot.InterfaceCallConnected.Add(*tunName, 1)
			} else if pktType == xot.PktTypeClearRequest {
				log.Printf("TUN: Clear Request from kernel on LCI %d", s.LciB)
				s.SetState(xot.StateP5)
				confBuf := []byte{payload[0], payload[1], xot.PktTypeClearConfirm}
				tun.WriteFrame(tg.ifce, *tunName, tun.HeaderData, confBuf)
				// Remove before forwarding — see SESS004 comment in CLAUDE.md.
				tg.sm.RemoveSession(s)
				xot.SendXot("unix", s.ConnB, remapped)
				xot.InterfaceClearRequest.Add(*tunName, 1)
				continue
			} else if pktType == xot.PktTypeClearConfirm {
				log.Printf("TUN: Clear Confirmation from kernel on LCI %d", s.LciB)
				tg.sm.RemoveSession(s)
				xot.SendXot("unix", s.ConnB, remapped)
				xot.InterfaceClearConfirm.Add(*tunName, 1)
				continue
			}

			xot.SendXot("unix", s.ConnB, remapped)
		} else {
			// RACE-B: send CLR_REQ to prevent stale kernel socket lingering.
			if *trace {
				log.Printf("%s>??? NO_SESSION (hdr=0x%02X) %s LCI=%d", tunSource, hdr, pktTypeName, pLCI)
			}
			if pktType != xot.PktTypeClearRequest && pktType != xot.PktTypeClearConfirm && pLCI != 0 {
				if *trace {
					log.Printf("%s< NO_SESSION - Sending CLEAR to prevent kernel hang on LCI %d", tunSource, pLCI)
				}
				clr := xot.CreateClearRequest(pLCI, xot.CauseNetworkCongestion, 0)
				tun.WriteFrame(tg.ifce, *tunName, tun.HeaderData, clr.Serialize())
			}
		}

		if hdr == tun.HeaderDisconnect {
			log.Printf("%s: Received Disconnect from kernel - cleaning up all sessions", tunSource)
			atomic.StoreInt32(&tg.linkState, LinkStateDown)
			tg.closeAllSessions()
		}
	}
}

func (tg *TunGateway) forwardToGateway(pkt *xot.X25Packet) {
	if atomic.LoadInt32(&tg.shuttingDown) == 1 {
		return
	}
	conn, err := net.Dial("unixpacket", "/tmp/xot_gwy.sock")
	if err != nil {
		log.Printf("Failed to connect to xot-gateway: %v", err)
		clr := xot.CreateClearRequest(pkt.LCI, xot.CauseNetworkCongestion, 0)
		tun.WriteFrame(tg.ifce, *tunName, tun.HeaderData, clr.Serialize())
		return
	}

	s := &xot.Session{
		LciA:  pkt.LCI,
		LciB:  pkt.LCI,
		ConnB: conn,
		State: xot.StateP2,
	}
	tg.sm.AddSession(s)

	go func() {
		xot.InterfaceSessionsOpened.Add("xot", 1)
		defer xot.InterfaceSessionsClosed.Add("xot", 1)
		xot.ThreadsActive.Add("gateway_read_handler", 1)
		defer xot.ThreadsActive.Add("gateway_read_handler", -1)
		tg.handleGatewayRead(conn)
	}()

	if *trace {
		xot.LogTrace(fmt.Sprintf("TUN(%d)", tg.ifce.Fd()), fmt.Sprintf("GWY(%d)", xot.GetFd(conn)), pkt)
	}
	if err := xot.SendXot("xot", conn, pkt.Serialize()); err != nil {
		log.Printf("Failed to send CALL_REQ to gateway: %v", err)
		conn.Close()
	}
}

func (tg *TunGateway) handleGatewayRead(conn net.Conn) {
	defer conn.Close()
	defer tg.cleanupConn(conn)

	fd := xot.GetFd(conn)
	source := fmt.Sprintf("GWY(%d)", fd)
	tunDest := fmt.Sprintf("TUN(%d)", tg.ifce.Fd())

	buf := xot.GetBuffer()
	defer xot.PutBuffer(buf)
	for {
		data, err := xot.ReadXotInto("xot", conn, buf)
		if err != nil {
			if errors.Is(err, xot.ErrPacketTooLong) {
				log.Printf("%s: %v from gateway", source, err)
				xot.CausesGenerated.Add("packet_too_long", 1)
				lciErr := xot.GetLCI(data)
				clr := xot.CreateClearRequest(lciErr, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
				xot.SendXot("xot", conn, clr.Serialize())
			} else if err != io.EOF {
				log.Printf("%s: Error reading XOT: %v", source, err)
			}
			return
		}

		pktType := xot.GetPacketType(data)
		pktTypeName := xot.GetPacketTypeName(pktType)
		xot.PacketsHandled.Add(pktTypeName, 1)

		incomingLCI := xot.GetLCI(data)
		s := tg.sm.GetByBConnLCI(conn, incomingLCI)

		if s == nil {
			if *trace {
				log.Printf("%s: No session for gateway LCI %d (likely closed by peer)", source, incomingLCI)
			}
			continue
		}

		if pktType == xot.PktTypeCallConnected {
			s.SetState(xot.StateP4)
		} else if pktType == xot.PktTypeClearRequest || pktType == xot.PktTypeClearConfirm {
			log.Printf("%s: Call cleared on LCI %d (type: %s)", source, s.LciA, pktTypeName)
			if pktType == xot.PktTypeClearRequest && len(data) >= 4 {
				xot.CausesReceived.Add(fmt.Sprintf("0x%02x", data[3]), 1)
			}
			data[0] = (data[0] & 0xF0) | byte((s.LciA>>8)&0x0F)
			data[1] = byte(s.LciA & 0xFF)
			tun.WriteFrame(tg.ifce, *tunName, tun.HeaderData, data)
			if pktType == xot.PktTypeClearConfirm {
				tg.sm.RemoveSession(s)
				return
			}
			s.SetState(xot.StateP5)
			continue
		}

		if *trace {
			xot.LogTraceRaw(source, tunDest, data)
		}

		data[0] = (data[0] & 0xF0) | byte((s.LciA>>8)&0x0F)
		data[1] = byte(s.LciA & 0xFF)
		tun.WriteFrame(tg.ifce, *tunName, tun.HeaderData, data)
	}
}

func (tg *TunGateway) SyncRoutes() {
	tg.routeMu.Lock()
	defer tg.routeMu.Unlock()

	servers := tg.cm.GetServers()
	if servers == nil {
		log.Printf("Warning: No servers configured, skipping route sync")
		return
	}
	newRoutes := make(map[string]int)
	for _, srv := range servers {
		parts := strings.Split(srv.Prefix, "/")
		if len(parts) == 2 {
			prefix := parts[0]
			digits := 0
			fmt.Sscanf(parts[1], "%d", &digits)
			newRoutes[prefix] = digits
		}
	}

	for prefix, digits := range tg.currentRoutes {
		if _, ok := newRoutes[prefix]; !ok {
			if err := tun.DeleteRoute(*tunName, prefix, digits); err != nil {
				log.Printf("Warning: failed to delete X.25 route %s/%d: %v", prefix, digits, err)
			} else {
				log.Printf("Removed X.25 route %s/%d from %s", prefix, digits, *tunName)
			}
			delete(tg.currentRoutes, prefix)
		}
	}

	for prefix, digits := range newRoutes {
		if _, ok := tg.currentRoutes[prefix]; !ok {
			if err := tun.AddRoute(*tunName, prefix, digits); err != nil {
				log.Printf("Warning: failed to add X.25 route %s/%d: %v", prefix, digits, err)
			} else {
				log.Printf("Added X.25 route %s/%d to %s", prefix, digits, *tunName)
				tg.currentRoutes[prefix] = digits
			}
		}
	}
}

func (tg *TunGateway) watchConfig() {
	fd, err := syscall.InotifyInit()
	if err != nil {
		log.Printf("Error initializing inotify: %v", err)
		return
	}
	defer syscall.Close(fd)

	_, err = syscall.InotifyAddWatch(fd, *configPath, syscall.IN_MODIFY|syscall.IN_CLOSE_WRITE)
	if err != nil {
		log.Printf("Error adding inotify watch for %s: %v", *configPath, err)
		return
	}

	buf := make([]byte, syscall.SizeofInotifyEvent*10)
	for {
		n, err := syscall.Read(fd, buf)
		if err != nil {
			log.Printf("Error reading inotify event: %v", err)
			return
		}
		if n > 0 {
			log.Printf("Config file %s changed, syncing routes", *configPath)
			if _, err := tg.cm.Reload(); err == nil {
				tg.SyncRoutes()
			}
		}
	}
}
