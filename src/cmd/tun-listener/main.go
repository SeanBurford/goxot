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
)

const (
	AF_X25               = 9
	SOCK_SEQPACKET       = 5
	SIOCX25GFACILITIES   = 0x89E2
	SIOCX25GCALLUSERDATA = 0x89E3
)

type x25_address struct {
	X25Addr [16]byte
}

type sockaddr_x25 struct {
	Family  uint16
	Address x25_address
}

type x25_facilities struct {
	Winsize_in  uint32
	Winsize_out uint32
	Psize_in    uint32
	Psize_out   uint32
	Throughput  uint32
	Reverse     uint32
}

type x25_calluserdata struct {
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

	fd, err := syscall.Socket(AF_X25, SOCK_SEQPACKET, 0)
	if err != nil {
		log.Fatalf("Failed to create AF_X25 socket: %v", err)
	}
	defer syscall.Close(fd)

	var sa sockaddr_x25
	sa.Family = AF_X25
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
		var rsa sockaddr_x25
		rsaLen := uint32(unsafe.Sizeof(rsa))
		nfd, _, errno := syscall.Syscall(syscall.SYS_ACCEPT, uintptr(fd), uintptr(unsafe.Pointer(&rsa)), uintptr(unsafe.Pointer(&rsaLen)))
		if errno != 0 {
			log.Printf("Accept failed: %v", errno)
			continue
		}

		go handleConn(int(nfd), rsa)
	}
}

func handleConn(fd int, sa sockaddr_x25) {
	f := os.NewFile(uintptr(fd), "")
	defer f.Close()

	remoteAddr := strings.TrimRight(string(sa.Address.X25Addr[:]), "\x00")
	log.Printf("Accepted connection from %s", remoteAddr)
	fmt.Fprintf(f, "Welcome to tun-listener. Your address: %s\r\n", remoteAddr)

	// Query LCI
	var lci uint16
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), 0x89E5, uintptr(unsafe.Pointer(&lci)))
	if errno == 0 {
		fmt.Fprintf(f, "LCI: %d\r\n", lci)
	}

	// Query facilities
	var fac x25_facilities
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), SIOCX25GFACILITIES, uintptr(unsafe.Pointer(&fac)))
	if errno == 0 {
		fmt.Fprintf(f, "Facilities: WinIn=%d, WinOut=%d, PktIn=%d, PktOut=%d\r\n", fac.Winsize_in, fac.Winsize_out, fac.Psize_in, fac.Psize_out)
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
