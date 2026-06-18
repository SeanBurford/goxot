package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"

	xot "github.com/SeanBurford/goxot/src"
)

const (
	afX25              = 9
	sockSeqpacket      = 5
	siocX25GFacilities = 0x89E2
)

type x25Address struct {
	X25Addr [16]byte
}

type sockaddrX25 struct {
	Family  uint16
	Address x25Address
}

type x25Facilities struct {
	WinsizeIn  uint32
	WinsizeOut uint32
	PsizeIn    uint32
	PsizeOut   uint32
	Throughput uint32
	Reverse    uint32
}

type x25Calluserdata struct {
	CudLen  uint32
	CudData [128]byte
}

var (
	address = flag.String("address", "", "X.25 address to bind to")
)

func main() {
	flag.Parse()

	if *address == "" {
		log.Fatal("--address is required")
	}

	fd, err := syscall.Socket(afX25, sockSeqpacket, 0)
	if err != nil {
		log.Fatalf("Failed to create AF_X25 socket: %v", err)
	}
	defer syscall.Close(fd)

	var sa sockaddrX25
	sa.Family = afX25
	copy(sa.Address.X25Addr[:], *address)

	// Bind
	_, _, errno := syscall.Syscall(syscall.SYS_BIND, uintptr(fd), uintptr(unsafe.Pointer(&sa)), uintptr(unsafe.Sizeof(sa)))
	if errno != 0 {
		log.Fatalf("Failed to bind to %s: %v", *address, errno)
	}

	// Listen
	if err := syscall.Listen(fd, 5); err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	log.Printf("tun-listener listening on X.25 address %s", *address)

	for {
		var rsa sockaddrX25
		rsaLen := uint32(unsafe.Sizeof(rsa))
		nfd, _, errno := syscall.Syscall(syscall.SYS_ACCEPT, uintptr(fd), uintptr(unsafe.Pointer(&rsa)), uintptr(unsafe.Pointer(&rsaLen)))
		if errno != 0 {
			log.Printf("Accept failed: %v", errno)
			continue
		}

		go handleConn(int(nfd), rsa)
	}
}

func handleConn(fd int, sa sockaddrX25) {
	f := os.NewFile(uintptr(fd), "")
	defer f.Close()

	remoteAddr := xot.X25AddrFromBytes(sa.Address.X25Addr[:])
	log.Printf("Accepted connection from %s", remoteAddr)
	fmt.Fprintf(f, "Welcome to tun-listener. Your address: %s\r\n", remoteAddr)

	// Query facilities
	var fac x25Facilities
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), siocX25GFacilities, uintptr(unsafe.Pointer(&fac)))
	if errno == 0 {
		fmt.Fprintf(f, "Facilities: %s\r\n", xot.FormatX25FacilitiesRaw(fac.WinsizeIn, fac.WinsizeOut, fac.PsizeIn, fac.PsizeOut))
	}

	// Set read timeout for idle disconnection
	idleTimeout := 5 * time.Second

	// Just read and discard for now
	buf := make([]byte, 4096)
	for {
		// We use syscall.Setoptsockopt to set timeout if we were using net.Conn,
		// but since we are using os.File, we can use SetReadDeadline if we wrap it back or just use a timer.
		// Actually, since it's a raw FD, we should use syscall.Select or set SO_RCVTIMEO.

		tv := syscall.NsecToTimeval(idleTimeout.Nanoseconds())
		syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

		n, err := f.Read(buf)
		if err != nil {
			if err == os.ErrDeadlineExceeded || strings.Contains(err.Error(), "resource temporarily unavailable") {
				log.Printf("Connection from %s timed out", remoteAddr)
				fmt.Fprintf(f, "Goodbye (Idle Timeout)\r\n")
			}
			break
		}
		if n == 0 {
			break
		}
	}
	log.Printf("Connection from %s closed", remoteAddr)
}
