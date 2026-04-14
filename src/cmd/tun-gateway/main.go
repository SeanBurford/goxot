package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"unsafe"
	xot "github.com/SeanBurford/goxot"
)

var (
	tunName    = flag.String("tun", "tun0", "TUN interface name")
	configPath = flag.String("config", "config.json", "Path to config file")
	trace      = flag.Bool("trace", false, "Enable trace logging")
)

const (
	MaxTunPacketSize = 4096
	ARPHRD_X25       = 271
	TUNSETLINK       = 0x400454cd
	TUNSETIFF        = 0x400454ca
	SIOCSIFFLAGS     = 0x8914
	SIOCGIFFLAGS     = 0x8913
	SIOCADDRT         = 0x890B
	SIOCDELRT         = 0x890C
	SIOCX25GCAUSEDIAG = 0x89E4
	IFF_UP            = 0x1
	IFF_RUNNING      = 0x40
	IFF_TUN          = 0x0001
	IFF_TAP          = 0x0002
	IFF_NO_PI        = 0x1000

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
	Device    [16]byte
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
	mu   sync.Mutex
	// Map (conn, incomingLCI) -> tunLCI
	incomingToTun map[string]uint16
	// Map tunLCI -> (conn, incomingLCI)
	tunToIncoming map[uint16]sessionInfo
	nextTunLCI    uint16
	tunLciStart   uint16
	tunLciEnd     uint16
	
	// xot-gateway connection for intercepted calls
	gwyConn net.Conn

	// Routing state
	routeMu       sync.Mutex
	currentRoutes map[string]int // prefix -> digits
}

func (tg *TunGateway) getTunLCI(conn net.Conn, incomingLCI uint16) uint16 {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	
	key := fmt.Sprintf("%p:%d", conn, incomingLCI)
	if lci, ok := tg.incomingToTun[key]; ok {
		return lci
	}
	
	lci := tg.nextTunLCI
	tg.nextTunLCI++
	if tg.nextTunLCI > tg.tunLciEnd {
		tg.nextTunLCI = tg.tunLciStart
	}
	
	tg.incomingToTun[key] = lci
	tg.tunToIncoming[lci] = sessionInfo{conn, incomingLCI}
	return lci
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

func ReadTun(ifce *TunInterface) (byte, []byte, error) {
	packet := make([]byte, MaxTunPacketSize)
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
	
	var tunCfg xot.TunConfig
	if cm != nil {
		tunCfg = cm.GetTunConfig()
	} else {
		tunCfg = xot.TunConfig{LciStart: 1, LciEnd: 255}
	}

	// Open TUN
	ifce, err := SetupTun(*tunName, true)
	if err != nil {
		log.Fatalf("Failed to setup TUN: %v", err)
	}
	
	tg := &TunGateway{
		ifce: ifce,
		cm: cm,
		incomingToTun: make(map[string]uint16),
		tunToIncoming: make(map[uint16]sessionInfo),
		tunLciStart: uint16(tunCfg.LciStart),
		tunLciEnd: uint16(tunCfg.LciEnd),
		nextTunLCI: uint16(tunCfg.LciStart),
		currentRoutes: make(map[string]int),
	}
	
	// Initial route sync
	tg.SyncRoutes()
	
	// Watch config for changes
	go tg.watchConfig()

	// Listen for xot-server
	sockPath := "/tmp/xot_tun.sock"
	os.Remove(sockPath)
	ln, err := net.Listen("unixpacket", sockPath)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", sockPath, err)
	}
	log.Printf("tun-gateway listening on %s", sockPath)
	
	// Handle TUN reads
	go tg.handleTunRead()
	
	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		os.Remove(sockPath)
		os.Exit(0)
	}()
	
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go tg.handleServerConn(conn)
	}
}

func (tg *TunGateway) handleServerConn(conn net.Conn) {
	defer conn.Close()
	fd := xot.GetFd(conn)
	source := fmt.Sprintf("SVR(%d)", fd)
	tunDest := fmt.Sprintf("TUN(%d)", tg.ifce.Fd())
	
	for {
		data, err := xot.ReadXot(conn)
		if err != nil {
			if err != io.EOF {
				log.Printf("%s: Error reading XOT: %v", source, err)
			}
			return
		}
		
		pkt, err := xot.ParseX25(data)
		if err != nil {
			log.Printf("%s: Error parsing X.25: %v", source, err)
			continue
		}

		if *trace {
			xot.LogTrace(source, tunDest, pkt)
		}
		
		// Remap LCI
		incomingLCI := pkt.LCI
		tunLCI := tg.getTunLCI(conn, incomingLCI)
		pkt.LCI = tunLCI
		
		// Always use TunHeaderData (0x00) for sending to TUN as per user feedback
		WriteTun(tg.ifce, TunHeaderData, pkt.Serialize())
	}
}

func (tg *TunGateway) handleTunRead() {
	tunFd := tg.ifce.Fd()
	tunSource := fmt.Sprintf("TUN(%d)", tunFd)
	for {
		hdr, payload, err := ReadTun(tg.ifce)
		if err != nil {
			log.Fatalf("Error reading from TUN: %v", err)
		}

		if len(payload) == 0 {
			if *trace {
				log.Printf("%s> Control Header (hdr=0x%02X, empty payload)", tunSource, hdr)
			}
			if hdr == TunHeaderConnect {
				if *trace {
					log.Printf("%s< Responding with Connect (0x01)", tunSource)
				}
				WriteTun(tg.ifce, TunHeaderConnect, nil)
			}
			continue
		}

		pkt, err := xot.ParseX25(payload)
		if err != nil {
			if *trace {
				log.Printf("%s>??? UNKNOWN (hdr=0x%02X) % X", tunSource, hdr, payload)
			}
			continue
		}

		// Handle RESTART_REQ from kernel - usually means interface reset or peer reset
		if pkt.GetBaseType() == xot.PktTypeRestartRequest {
			if *trace {
				log.Printf("%s> RESTART_REQ (hdr=0x%02X) - sending RESTART_CONF", tunSource, hdr)
			}
			conf := &xot.X25Packet{
				GFI:  xot.GFIStandard,
				LCI:  0,
				Type: xot.PktTypeRestartConfirm,
			}
			WriteTun(tg.ifce, TunHeaderData, conf.Serialize())
			continue
		}

		// Check for intercepted call
		if pkt.GetBaseType() == xot.PktTypeCallRequest {
			called, calling, err := pkt.ParseCallRequest()
			if err == nil && tg.cm.GetServer(called) != nil {
				log.Printf("TUN: Intercepting CALL_REQ from %s to %s", calling, called)
				tg.forwardToGateway(pkt)
				continue
			}
		}

		// Find session
		tg.mu.Lock()
		info, ok := tg.tunToIncoming[pkt.LCI]
		tg.mu.Unlock()

		if ok {
			pkt.LCI = info.lci
			dest := fmt.Sprintf("SVR(%d)", xot.GetFd(info.conn))
			if *trace {
				xot.LogTrace(tunSource, dest, pkt)
			}
			
			if pkt.GetBaseType() == xot.PktTypeCallConnected {
				log.Printf("TUN: Call connected on LCI %d", pkt.LCI)
			} else if pkt.GetBaseType() == xot.PktTypeClearRequest {
				log.Printf("TUN: Call cleared on LCI %d", pkt.LCI)
			}

			xot.SendXot(info.conn, pkt.Serialize())
		} else if *trace {
			log.Printf("%s>??? NO_SESSION (hdr=0x%02X) %s LCI=%d", tunSource, hdr, pkt.TypeName(), pkt.LCI)
		}
		
		// Handle disconnect header from TUN
		if hdr == TunHeaderDisconnect {
			// We could proactively clean up the session here if we wanted
		}
	}
}

func (tg *TunGateway) forwardToGateway(pkt *xot.X25Packet) {
	tg.mu.Lock()
	if tg.gwyConn == nil {
		conn, err := net.Dial("unixpacket", "/tmp/xot_gwy.sock")
		if err != nil {
			tg.mu.Unlock()
			log.Printf("Failed to connect to xot-gateway: %v", err)
			return
		}
		tg.gwyConn = conn
		go tg.handleGatewayRead(conn)
	}
	conn := tg.gwyConn
	tg.mu.Unlock()
	
	if *trace {
		xot.LogTrace(fmt.Sprintf("TUN(%d)", tg.ifce.Fd()), fmt.Sprintf("GWY(%d)", xot.GetFd(conn)), pkt)
	}
	xot.SendXot(conn, pkt.Serialize())
}

func (tg *TunGateway) handleGatewayRead(conn net.Conn) {
	defer func() {
		tg.mu.Lock()
		tg.gwyConn = nil
		tg.mu.Unlock()
		conn.Close()
	}()
	
	fd := xot.GetFd(conn)
	source := fmt.Sprintf("GWY(%d)", fd)
	tunDest := fmt.Sprintf("TUN(%d)", tg.ifce.Fd())
	
	for {
		data, err := xot.ReadXot(conn)
		if err != nil {
			if err != io.EOF {
				log.Printf("%s: Error reading XOT: %v", source, err)
			}
			return
		}
		
		if *trace {
			pkt, err := xot.ParseX25(data)
			if err == nil {
				xot.LogTrace(source, tunDest, pkt)
			} else {
				log.Printf("%s>%s UNKNOWN % X", source, tunDest, data)
			}
		}

		pkt, err := xot.ParseX25(data)
		if err != nil {
			log.Printf("%s: Error parsing X.25: %v", source, err)
			continue
		}
		
		WriteTun(tg.ifce, TunHeaderData, pkt.Serialize())
	}
}

func (tg *TunGateway) SyncRoutes() {
	tg.routeMu.Lock()
	defer tg.routeMu.Unlock()

	servers := tg.cm.GetServers()
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
