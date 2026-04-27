package main

import (
	"encoding/binary"
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
	"unsafe"

	xot "github.com/SeanBurford/goxot"
)

var (
	tunName    = flag.String("tun", "tun0", "TUN interface name")
	configPath = flag.String("config", "config.json", "Path to config file")
	trace      = flag.Bool("trace", false, "Enable trace logging")
	statsPort  = flag.Int("stats-port", 0, "Port for /varz stats (0 to disable)")
)

const (
	MaxTunPacketSize = xot.MaxX25PacketSize + 5
)

const (
	ARPHRD_X25       = 271
	TUNSETLINK       = 0x400454cd
	TUNSETIFF        = 0x400454ca
	SIOCSIFFLAGS     = 0x8914
	SIOCGIFFLAGS     = 0x8913
	SIOCADDRT         = 0x890B
	SIOCDELRT         = 0x890C
	SIOCX25GSUBSCRIP  = 0x89E0
	SIOCX25SSUBSCRIP  = 0x89E1
	IFF_UP            = 0x1
	IFF_RUNNING      = 0x40
	IFF_TUN          = 0x0001
	IFF_TAP          = 0x0002
	IFF_NO_PI        = 0x1000
)

const (
	TunHeaderData       = 0x00
	TunHeaderConnect    = 0x01
	TunHeaderDisconnect = 0x02
	TunHeaderParam      = 0x03
)

type x25_address struct {
	X25Addr [16]byte
}

type x25_route_struct struct {
	Address   x25_address
	SigDigits uint32
	Device    [192]byte
}

type x25_subscrip_struct struct {
	Device          [192]byte
	GlobalFacilMask uint64
	Extended        uint32
}

type x25_causediag struct {
	Cause      byte
	Diagnostic byte
}

type TunInterface struct {
	io.ReadWriteCloser
	name string
	fd   int
}

func (t *TunInterface) Name() string {
	return t.name
}

func (t *TunInterface) Fd() int {
	return t.fd
}

type sessionInfo struct {
	conn net.Conn
	lci  uint16
}

type TunGateway struct {
	ifce *TunInterface
	cm   *xot.ConfigManager
	sm   *xot.SessionManager
	
	// Routing state
	routeMu       sync.Mutex
	currentRoutes map[string]int // prefix -> digits

	// Link state
	linkState int32 // 0: Down, 1: Connecting, 3: Operational

	// Shutdown flag
	shuttingDown int32
}

const (
	LinkStateDown        = 0
	LinkStateConnecting  = 1
	LinkStateOperational = 3
)

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
		// SESS004: Only send CLEAR if we have a kernel-side state (not StateP1)
		// AND only if the session is still the one mapped to this LCI (ABA protection)
		if s.State != xot.StateP1 && tg.sm.GetByALCI(s.LciA) == s {
			if *trace {
				log.Printf("TUN: Cleaning up LCI %d - sending CLEAR_REQ to kernel", s.LciA)
			}
			// Send CLEAR back to kernel to move state machine
			clr := xot.CreateClearRequest(s.LciA, xot.CauseOutofOrder, 0)
			WriteTun(tg.ifce, TunHeaderData, clr.Serialize())
		}
		
		tg.sm.RemoveSession(s)
	}
}

func (tg *TunGateway) removeTunLCI(tunLCI uint16) {
	if s := tg.sm.GetByALCI(tunLCI); s != nil {
		tg.sm.RemoveSession(s)
	}
}

func (tg *TunGateway) closeAllSessions() {
	// SESS005: Atomically remove all sessions to prevent races
	sessions := tg.sm.RemoveAllSessions()
	for _, s := range sessions {
		// Forward CLEAR to gateway side
		if s.ConnB != nil {
			clr := xot.CreateClearRequest(s.LciB, xot.CauseNetworkCongestion, 0)
			xot.SendXot("xot", s.ConnB, clr.Serialize())
			// Note: SESS005: We don't remove session again here as it's already removed
		}
	}
	log.Printf("TUN: All %d sessions cleared", len(sessions))
}

func SetupTun(name string, create bool) (*TunInterface, error) {
	fd, err := syscall.Open("/dev/net/tun", syscall.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open /dev/net/tun: %v", err)
	}

	var ifr [40]byte
	copy(ifr[:], name)
	*(*uint16)(unsafe.Pointer(&ifr[16])) = IFF_TUN

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), TUNSETIFF, uintptr(unsafe.Pointer(&ifr)))
	if errno != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("failed to set TUN interface flags (IFF_TUN): %v", errno)
	}

	// Always set link type and bring UP
	linkType := ARPHRD_X25
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(TUNSETLINK), uintptr(linkType)); errno != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("failed to set TUN link type to ARPHRD_X25: %v", errno)
	}

	if err := BringUpInterface(name); err != nil {
		log.Printf("Warning: failed to bring up interface %s: %v", name, err)
	}

	log.Printf("TUN interface %s attached (direct)", name)

	return &TunInterface{
		ReadWriteCloser: os.NewFile(uintptr(fd), name),
		name:            name,
		fd:              fd,
	}, nil
}

func BringUpInterface(name string) error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, syscall.IPPROTO_IP)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	var ifr [40]byte
	copy(ifr[:], name)

	// Get current flags
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), SIOCGIFFLAGS, uintptr(unsafe.Pointer(&ifr)))
	if errno != 0 {
		return errno
	}

	// Set UP and RUNNING
	flags := *(*uint16)(unsafe.Pointer(&ifr[16]))
	flags |= IFF_UP | IFF_RUNNING
	*(*uint16)(unsafe.Pointer(&ifr[16])) = flags

	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), SIOCSIFFLAGS, uintptr(unsafe.Pointer(&ifr)))
	if errno != 0 {
		return errno
	}
	return nil
}

func AddX25Route(name string, prefix string, digits int) error {
	fd, err := syscall.Socket(syscall.AF_X25, syscall.SOCK_SEQPACKET, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	route := x25_route_struct{
		SigDigits: uint32(digits),
	}
	copy(route.Address.X25Addr[:], prefix)
	copy(route.Device[:], name)

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), SIOCADDRT, uintptr(unsafe.Pointer(&route)))
	if errno != 0 {
		return errno
	}
	return nil
}

func DeleteX25Route(name string, prefix string, digits int) error {
	fd, err := syscall.Socket(syscall.AF_X25, syscall.SOCK_SEQPACKET, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	route := x25_route_struct{
		SigDigits: uint32(digits),
	}
	copy(route.Address.X25Addr[:], prefix)
	copy(route.Device[:], name)

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), SIOCDELRT, uintptr(unsafe.Pointer(&route)))
	if errno != 0 {
		return errno
	}
	return nil
}

func SetX25Subscription(name string, lciStart, lciEnd int) error {
	fd, err := syscall.Socket(syscall.AF_X25, syscall.SOCK_SEQPACKET, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	extended := uint32(0)
	if lciEnd > 255 {
		extended = 1
	}

	sub := x25_subscrip_struct{
		GlobalFacilMask: 0x0F, // Enable standard facilities (Reverse, Throughput, Packet, Window)
		Extended:        extended,
	}
	copy(sub.Device[:], name)

	// Note: Standard Linux x25_subscrip_struct does not support setting explicit min/max LCI ranges.
	// Partitioning is achieved by setting Extended=1 and relying on the kernel's LCI allocator
	// starting at 1, while the gateway uses a higher range (configured via LciStart).
	log.Printf("TUN: Configuring X.25 subscription on %s (Extended=%d, Mask=0x0F, ExpectedRange=%d-%d)", 
		name, extended, lciStart, lciEnd)
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), SIOCX25SSUBSCRIP, uintptr(unsafe.Pointer(&sub)))
	if errno != 0 {
		return fmt.Errorf("SIOCX25SSUBSCRIP failed: %v", errno)
	}
	return nil
}

func ReadTun(ifce *TunInterface, packet []byte) (byte, []byte, error) {
	for {
		n, err := ifce.Read(packet)
		if err != nil {
			return 0, nil, err
		}
		if n < 4 {
			continue
		}
		proto := binary.BigEndian.Uint16(packet[2:4])
		if proto != 0x0805 {
			continue
		}
		if n < 5 {
			continue
		}
		xot.InterfacePacketsReceived.Add("tun", 1)
		xot.InterfaceBytesReceived.Add("tun", int64(n - 5))
		return packet[4], packet[5:n], nil
	}
}

func WriteTun(ifce *TunInterface, header byte, data []byte) error {
	buf := make([]byte, len(data)+5)
	buf[0] = 0x00
	buf[1] = 0x00
	buf[2] = 0x08
	buf[3] = 0x05
	buf[4] = header
	copy(buf[5:], data)
	n, err := ifce.Write(buf)
	if err != nil {
		log.Printf("Error writing to TUN (Header: 0x%02X, Data Len: %d): %v", header, len(data), err)
		return err
	}
	xot.InterfacePacketsSent.Add("tun", 1)
	xot.InterfaceBytesSent.Add("tun", int64(n - 5))
	if n != len(buf) {
		return fmt.Errorf("short write to TUN: wrote %d of %d bytes", n, len(buf))
	}
	return nil
}

func main() {
	flag.Parse()
	
	// Load config
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

	// Open TUN
	ifce, err := SetupTun(*tunName, true)
	if err != nil {
		log.Fatalf("Failed to setup TUN: %v", err)
	}

	// Configure LCI partitioning (COMPAT006 part 2)
	if err := SetX25Subscription(*tunName, tunCfg.LciStart, tunCfg.LciEnd); err != nil {
		log.Printf("Warning: failed to set X.25 subscription: %v", err)
	}
	
	tg := &TunGateway{
		ifce: ifce,
		cm:   cm,
		sm:   xot.NewSessionManager(uint16(tunCfg.LciStart), uint16(tunCfg.LciEnd)),
		currentRoutes: make(map[string]int),
		linkState:     LinkStateDown,
	}
	
	// Initial route sync
	tg.SyncRoutes()
	
	// Proactively establish link (COMPAT003)
	log.Printf("TUN: Proactively establishing link layer")
	WriteTun(ifce, TunHeaderConnect, nil)
	atomic.StoreInt32(&tg.linkState, LinkStateConnecting)
	
	// Watch config for changes
	go func() {
		xot.ThreadsActive.Add("watch_config", 1)
		defer xot.ThreadsActive.Add("watch_config", -1)
		tg.watchConfig()
	}()

	// Listen for xot-server
	sockPath := "/tmp/xot_tun.sock"
	os.Remove(sockPath)
	ln, err := net.Listen("unixpacket", sockPath)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", sockPath, err)
	}
	log.Printf("tun-gateway listening on %s", sockPath)
	
	// Handle TUN reads
	go func() {
		xot.ThreadsActive.Add("tun_read_handler", 1)
		defer xot.ThreadsActive.Add("tun_read_handler", -1)
		tg.handleTunRead()
	}()
	
	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		xot.ThreadsActive.Add("signal_handler", 1)
		defer xot.ThreadsActive.Add("signal_handler", -1)
		<-sigChan
		log.Printf("TUN: Shutting down - cleaning up sessions")
		atomic.StoreInt32(&tg.shuttingDown, 1) // COMPAT009
		ln.Close()                            // COMPAT009 - stop accepting new connections
		tg.closeAllSessions()
		WriteTun(ifce, TunHeaderDisconnect, nil) // COMPAT010 / SOCK006
		ifce.Close() // Explicit close to trigger NETDEV_DOWN promptly
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
				lci_err := xot.GetLCI(data)
				clr := xot.CreateClearRequest(lci_err, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
				xot.SendXot("unix", conn, clr.Serialize())
			} else if err != io.EOF {
				log.Printf("%s: Error reading XOT: %v", source, err)
			}
			return
		}
		
		pktType := xot.GetPacketType(data)
		pktTypeName := xot.GetPacketTypeName(pktType)
		xot.PacketsHandled.Add(pktTypeName, 1)

		// Remap LCI
		incomingLCI := xot.GetLCI(data)

		if pktType == xot.PktTypeClearRequest || pktType == xot.PktTypeClearConfirm {
			log.Printf("%s: Call cleared on LCI %d (type: %s)", source, incomingLCI, pktTypeName)
			if pktType == xot.PktTypeClearRequest && len(data) >= 4 {
				xot.CausesReceived.Add(fmt.Sprintf("0x%02x", data[3]), 1)
			}
			
			// Find and update session state
			s := tg.sm.GetByBConnLCI(conn, incomingLCI)
			if s != nil {
				s.SetState(xot.StateP5)
				// Create a copy of the packet for remapping if we were going to use the original
				// But here we can just update the LCI in place in the buffer
				data[0] = (data[0] & 0xF0) | byte((s.LciA>>8)&0x0F)
				data[1] = byte(s.LciA & 0xFF)
				WriteTun(tg.ifce, TunHeaderData, data)
				
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
			log.Printf("%s: Session for LCI %d lost mid-flight (likely disconnect)", source, tunLCI)
			return
		}

		// Update LCI in place
		data[0] = (data[0] & 0xF0) | byte((tunLCI>>8)&0x0F)
		data[1] = byte(tunLCI & 0xFF)
		
		WriteTun(tg.ifce, TunHeaderData, data)
	}
}

func (tg *TunGateway) handleTunRead() {
	tunFd := tg.ifce.Fd()
	tunSource := fmt.Sprintf("TUN(%d)", tunFd)
	packet := make([]byte, MaxTunPacketSize)
	for {
		hdr, payload, err := ReadTun(tg.ifce, packet)
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
			if hdr == TunHeaderConnect {
				if atomic.LoadInt32(&tg.linkState) != LinkStateOperational {
					xot.InterfaceSessionsOpened.Add("tun", 1)
					if *trace {
						log.Printf("%s< Responding with Connect (0x01)", tunSource)
					}
					WriteTun(tg.ifce, TunHeaderConnect, nil)
					atomic.CompareAndSwapInt32(&tg.linkState, LinkStateDown, LinkStateConnecting)
				}
			} else if hdr == TunHeaderDisconnect {
				log.Printf("%s: Received Disconnect from kernel - cleaning up all sessions", tunSource)
				xot.InterfaceSessionsClosed.Add("tun", 1)
				atomic.StoreInt32(&tg.linkState, LinkStateDown)
				tg.closeAllSessions()
			}
			continue
		}

		pktType := xot.GetPacketType(payload)
		pktTypeName := xot.GetPacketTypeName(pktType)
		xot.PacketsHandled.Add(pktTypeName, 1)

		// Handle RESTART_REQ from kernel - usually means interface reset or peer reset
		if pktType == xot.PktTypeRestartRequest {
			currentState := atomic.LoadInt32(&tg.linkState)
			hasSessions := len(tg.sm.GetAllSessions()) > 0

			if currentState == LinkStateOperational {
				if hasSessions {
					// Genuine mid-session restart (COMPAT005)
					log.Printf("%s> Genuine RESTART_REQ in STATE_3 - clearing sessions", tunSource)
					tg.closeAllSessions()
				} else {
					// Likely a buffered duplicate from startup (COMPAT004)
					if *trace {
						log.Printf("%s> Ignoring buffered RESTART_REQ duplicate", tunSource)
					}
					continue
				}
			}

			if *trace {
				log.Printf("%s> Sending RESTART_CONF (hdr=0x%02X)", tunSource, hdr)
			}

			// Respond with RESTART_CONF
			buf := make([]byte, 3)
			buf[0] = (xot.GFIStandard << 4)
			buf[1] = 0 // LCI 0
			buf[2] = xot.PktTypeRestartConfirm
			WriteTun(tg.ifce, TunHeaderData, buf)
			
			if atomic.CompareAndSwapInt32(&tg.linkState, LinkStateConnecting, LinkStateOperational) {
				log.Printf("%s: Link Layer Operational (STATE_3)", tunSource)
			} else {
				atomic.StoreInt32(&tg.linkState, LinkStateOperational)
			}
			continue
		}

		pLCI := xot.GetLCI(payload)

		// Check for intercepted call
		if pktType == xot.PktTypeCallRequest {
			// If we have an existing session for this LCI, remove it.
			// The kernel is initiating a new call on this LCI.
			if s := tg.sm.GetByALCI(pLCI); s != nil {
				if *trace {
					log.Printf("TUN: New CALL_REQ on busy LCI %d - clearing old session", pLCI)
				}
				tg.sm.RemoveSession(s)
			}

			xot.InterfaceCallRequest.Add("tun", 1)
			pkt, err := xot.ParseX25(payload)
			if err == nil {
				called, calling, fac, _, err := pkt.ParseCallRequest()
				if err == nil && tg.cm.GetServer(called) != nil {
					log.Printf("TUN: Intercepting CALL_REQ from %s to %s (fac: %s)", calling, called, xot.FormatFacilities(fac))
					go tg.forwardToGateway(pkt)
					continue
				}
			}
		}

		// Find session
		s := tg.sm.GetByALCI(pLCI)

		if s != nil {
			// Update LCI in place
			oldData := make([]byte, len(payload))
			copy(oldData, payload)
			oldData[0] = (oldData[0] & 0xF0) | byte((s.LciB>>8)&0x0F)
			oldData[1] = byte(s.LciB & 0xFF)

			dest := fmt.Sprintf("SVR(%d)", xot.GetFd(s.ConnB))
			if *trace {
				xot.LogTraceRaw(tunSource, dest, oldData)
			}
			
			if pktType == xot.PktTypeCallConnected {
				log.Printf("TUN: Call connected on LCI %d", s.LciB)
				s.SetState(xot.StateP4)
				xot.InterfaceCallConnected.Add("tun", 1)
			} else if pktType == xot.PktTypeClearRequest {
				log.Printf("TUN: Clear Request from kernel on LCI %d", s.LciB)
				s.SetState(xot.StateP5)
				
				// Respond with Clear Confirmation to kernel immediately
				confBuf := make([]byte, 3)
				confBuf[0] = payload[0]
				confBuf[1] = payload[1]
				confBuf[2] = xot.PktTypeClearConfirm
				WriteTun(tg.ifce, TunHeaderData, confBuf)
				
				// Forward CLEAR to gateway and cleanup
				xot.SendXot("unix", s.ConnB, oldData)
				tg.sm.RemoveSession(s)
				xot.InterfaceClearRequest.Add("tun", 1)
				continue
			} else if pktType == xot.PktTypeClearConfirm {
				log.Printf("TUN: Clear Confirmation from kernel on LCI %d", s.LciB)
				xot.SendXot("unix", s.ConnB, oldData)
				tg.sm.RemoveSession(s)
				xot.InterfaceClearConfirm.Add("tun", 1)
				continue
			}

			xot.SendXot("unix", s.ConnB, oldData)
		} else if *trace {
			log.Printf("%s>??? NO_SESSION (hdr=0x%02X) %s LCI=%d", tunSource, hdr, pktTypeName, pLCI)
			
			if pktType != xot.PktTypeClearRequest && pktType != xot.PktTypeClearConfirm && pLCI != 0 {
				log.Printf("%s< NO_SESSION - Sending CLEAR to prevent kernel hang on LCI %d", tunSource, pLCI)
				clr := xot.CreateClearRequest(pLCI, xot.CauseNetworkCongestion, 0)
				WriteTun(tg.ifce, TunHeaderData, clr.Serialize())
			}
		}
		
		if hdr == TunHeaderDisconnect {
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
		// Send CLEAR back to TUN
		clr := xot.CreateClearRequest(pkt.LCI, xot.CauseNetworkCongestion, 0)
		WriteTun(tg.ifce, TunHeaderData, clr.Serialize())
		return
	}

	// Record session mapping
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
		// cleanupConn will be called by the handleGatewayRead goroutine 
		// when it sees the error or connection close, which will send CLEAR to TUN.
		// But let's be explicit here too in case the goroutine hasn't started.
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
				lci_err := xot.GetLCI(data)
				clr := xot.CreateClearRequest(lci_err, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
				xot.SendXot("xot", conn, clr.Serialize())
			} else if err != io.EOF {
				log.Printf("%s: Error reading XOT: %v", source, err)
			}
			return
		}
		
		pktType := xot.GetPacketType(data)
		pktTypeName := xot.GetPacketTypeName(pktType)
		xot.PacketsHandled.Add(pktTypeName, 1)

		// Remap LCI
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
			
			// Remap LCI in place
			data[0] = (data[0] & 0xF0) | byte((s.LciA>>8)&0x0F)
			data[1] = byte(s.LciA & 0xFF)
			
			// Forward to TUN
			WriteTun(tg.ifce, TunHeaderData, data)
			
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

		// Remap LCI in place
		data[0] = (data[0] & 0xF0) | byte((s.LciA>>8)&0x0F)
		data[1] = byte(s.LciA & 0xFF)

		WriteTun(tg.ifce, TunHeaderData, data)
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

	// Remove old routes
	for prefix, digits := range tg.currentRoutes {
		if _, ok := newRoutes[prefix]; !ok {
			if err := DeleteX25Route(*tunName, prefix, digits); err != nil {
				log.Printf("Warning: failed to delete X.25 route %s/%d: %v", prefix, digits, err)
			} else {
				log.Printf("Removed X.25 route %s/%d from %s", prefix, digits, *tunName)
			}
			delete(tg.currentRoutes, prefix)
		}
	}

	// Add new routes
	for prefix, digits := range newRoutes {
		if _, ok := tg.currentRoutes[prefix]; !ok {
			if err := AddX25Route(*tunName, prefix, digits); err != nil {
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
