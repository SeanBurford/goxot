// Package tun provides Linux X.25 TUN interface operations.
package tun

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"syscall"
	"unsafe"

	xot "github.com/SeanBurford/goxot"
)

// PI control header bytes exchanged with the kernel (x25device.h).
const (
	HeaderData       = byte(0x00) // X.25 PLP packet follows
	HeaderConnect    = byte(0x01) // L2 connect request / acknowledgement
	HeaderDisconnect = byte(0x02) // L2 disconnection
	HeaderParam      = byte(0x03) // Link parameter exchange (unused in practice)
)

// MaxPacketSize is the maximum size of a single PI-framed TUN read:
// 5-byte PI header + largest X.25 packet.
const MaxPacketSize = xot.MaxX25PacketSize + 5

// ioctl constants for Linux TUN/X.25 operations (unexported; callers use the functions).
const (
	arphrdX25        = uintptr(271)
	tunSetLink       = uintptr(0x400454cd)
	tunSetIff        = uintptr(0x400454ca)
	siocSIFFlags     = uintptr(0x8914)
	siocGIFFlags     = uintptr(0x8913)
	siocAddRT        = uintptr(0x890B)
	siocDelRT        = uintptr(0x890C)
	siocX25SSubscrip = uintptr(0x89E1)
	iffUp            = uint16(0x1)
	iffRunning       = uint16(0x40)
	iffTUN           = uint16(0x0001)
)

// x25Address mirrors the kernel struct x25_address.
type x25Address struct {
	X25Addr [16]byte
}

// x25RouteStruct mirrors the kernel struct x25_route_struct.
type x25RouteStruct struct {
	Address   x25Address
	SigDigits uint32
	Device    [192]byte
}

// x25SubscripStruct mirrors the kernel struct x25_subscrip_struct.
type x25SubscripStruct struct {
	Device          [192]byte
	GlobalFacilMask uint64
	Extended        uint32
}

// Interface wraps a TUN file descriptor configured as ARPHRD_X25.
type Interface struct {
	f    *os.File
	name string
	fd   int
}

// Name returns the interface name (e.g. "tun0").
func (t *Interface) Name() string { return t.name }

// Fd returns the raw file descriptor number.
func (t *Interface) Fd() int { return t.fd }

// Read implements io.Reader — returns one complete TUN frame per call.
func (t *Interface) Read(b []byte) (int, error) { return t.f.Read(b) }

// Write implements io.Writer.
func (t *Interface) Write(b []byte) (int, error) { return t.f.Write(b) }

// Close closes the TUN file descriptor.
func (t *Interface) Close() error { return t.f.Close() }

// Setup opens /dev/net/tun, attaches to (or creates) the named interface,
// sets the link type to ARPHRD_X25 (triggering kernel neighbor registration),
// and brings the interface up.
func Setup(name string) (*Interface, error) {
	fd, err := syscall.Open("/dev/net/tun", syscall.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/net/tun: %w", err)
	}

	var ifr [40]byte
	copy(ifr[:], name)
	*(*uint16)(unsafe.Pointer(&ifr[16])) = iffTUN

	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tunSetIff, uintptr(unsafe.Pointer(&ifr))); errno != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("TUNSETIFF %s: %w", name, errno)
	}

	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tunSetLink, arphrdX25); errno != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("TUNSETLINK %s: %w", name, errno)
	}

	if err := BringUp(name); err != nil {
		log.Printf("Warning: failed to bring up %s: %v", name, err)
	}

	log.Printf("TUN interface %s ready", name)
	return &Interface{
		f:    os.NewFile(uintptr(fd), name),
		name: name,
		fd:   fd,
	}, nil
}

// BringUp brings a named network interface up (IFF_UP | IFF_RUNNING).
// Uses a temporary AF_INET SOCK_DGRAM socket for the ioctl.
func BringUp(name string) error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, syscall.IPPROTO_IP)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	var ifr [40]byte
	copy(ifr[:], name)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), siocGIFFlags, uintptr(unsafe.Pointer(&ifr))); errno != 0 {
		return errno
	}
	flags := *(*uint16)(unsafe.Pointer(&ifr[16]))
	flags |= iffUp | iffRunning
	*(*uint16)(unsafe.Pointer(&ifr[16])) = flags
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), siocSIFFlags, uintptr(unsafe.Pointer(&ifr))); errno != 0 {
		return errno
	}
	return nil
}

// AddRoute adds an X.25 prefix route pointing to ifaceName.
// prefix is the X.121 address; digits is the number of significant digits.
// Requires CAP_NET_ADMIN and an open AF_X25 socket (created internally).
func AddRoute(ifaceName, prefix string, digits int) error {
	fd, err := syscall.Socket(syscall.AF_X25, syscall.SOCK_SEQPACKET, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	r := x25RouteStruct{SigDigits: uint32(digits)}
	copy(r.Address.X25Addr[:], prefix)
	copy(r.Device[:], ifaceName)

	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), siocAddRT, uintptr(unsafe.Pointer(&r))); errno != 0 {
		return errno
	}
	return nil
}

// DeleteRoute removes an X.25 prefix route from ifaceName.
func DeleteRoute(ifaceName, prefix string, digits int) error {
	fd, err := syscall.Socket(syscall.AF_X25, syscall.SOCK_SEQPACKET, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	r := x25RouteStruct{SigDigits: uint32(digits)}
	copy(r.Address.X25Addr[:], prefix)
	copy(r.Device[:], ifaceName)

	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), siocDelRT, uintptr(unsafe.Pointer(&r))); errno != 0 {
		return errno
	}
	return nil
}

// SetSubscription configures the X.25 subscription on ifaceName:
// enables standard facility negotiation and sets extended mode when lciEnd > 255.
// Call after Setup to configure LCI range partitioning.
func SetSubscription(ifaceName string, lciStart, lciEnd int) error {
	fd, err := syscall.Socket(syscall.AF_X25, syscall.SOCK_SEQPACKET, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	extended := uint32(0)
	if lciEnd > 255 {
		extended = 1
	}
	sub := x25SubscripStruct{
		GlobalFacilMask: 0x0F, // Reverse | Throughput | PacketSize | WindowSize
		Extended:        extended,
	}
	copy(sub.Device[:], ifaceName)

	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), siocX25SSubscrip, uintptr(unsafe.Pointer(&sub))); errno != 0 {
		return fmt.Errorf("SIOCX25SSUBSCRIP %s: %w", ifaceName, errno)
	}
	return nil
}

// ReadFrame reads one PI-framed X.25 TUN packet from r.
// Returns (controlHeader, x25Payload, error).
// Frames with a non-X25 EtherType are silently skipped.
// buf must be at least MaxPacketSize bytes; it is used as a scratch buffer and the
// returned payload slice aliases it — copy before the next ReadFrame call if needed.
func ReadFrame(r io.Reader, ifname string, buf []byte) (byte, []byte, error) {
	for {
		n, err := r.Read(buf)
		if err != nil {
			return 0, nil, err
		}
		if n < 4 {
			continue
		}
		if binary.BigEndian.Uint16(buf[2:4]) != 0x0805 { // ETH_P_X25
			continue
		}
		if n < 5 {
			continue
		}
		xot.InterfacePacketsReceived.Add(ifname, 1)
		xot.InterfaceBytesReceived.Add(ifname, int64(n-5))
		return buf[4], buf[5:n], nil
	}
}

// WriteFrame writes one PI-framed X.25 TUN packet to w.
// Allocates a small buffer internally; use WriteFrameBuf in the hot path.
func WriteFrame(w io.Writer, ifname string, header byte, data []byte) error {
	buf := make([]byte, len(data)+5)
	return writeFrameInto(w, ifname, header, data, buf)
}

// WriteFrameBuf writes one PI-framed X.25 TUN packet using caller-supplied buf,
// avoiding allocation in the packet relay hot path.
// buf capacity must be >= len(data)+5.
func WriteFrameBuf(w io.Writer, ifname string, header byte, data []byte, buf []byte) error {
	return writeFrameInto(w, ifname, header, data, buf[:len(data)+5])
}

func writeFrameInto(w io.Writer, ifname string, header byte, data []byte, buf []byte) error {
	buf[0] = 0x00
	buf[1] = 0x00
	buf[2] = 0x08
	buf[3] = 0x05
	buf[4] = header
	copy(buf[5:], data)
	n, err := w.Write(buf)
	if err != nil {
		return err
	}
	xot.InterfacePacketsSent.Add(ifname, 1)
	xot.InterfaceBytesSent.Add(ifname, int64(n-5))
	if n != len(buf) {
		return fmt.Errorf("short write: %d/%d", n, len(buf))
	}
	return nil
}
