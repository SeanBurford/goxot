package xot

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
)

var (
	headerPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 4)
		},
	}
	bufferPool = sync.Pool{
		New: func() interface{} {
			// Pre-allocate the maximum possible XOT packet size to avoid growth
			return make([]byte, MaxXOTPacketSize)
		},
	}
)

// GetBuffer returns a buffer from the pool. The caller MUST call PutBuffer when done.
func GetBuffer() []byte {
	return bufferPool.Get().([]byte)
}

// PutBuffer returns a buffer to the pool.
func PutBuffer(buf []byte) {
	if cap(buf) < MaxXOTPacketSize {
		return
	}
	bufferPool.Put(buf[:MaxXOTPacketSize])
}

const (
	XotVersion = 0
)

func SetNoDelay(conn net.Conn) error {
	if tcp, ok := conn.(*net.TCPConn); ok {
		return tcp.SetNoDelay(true)
	}
	return nil
}

func isPacketConn(conn net.Conn) bool {
	network := conn.LocalAddr().Network()
	return network == "unixpacket"
}

func updateCallRequestCount(ifname string, data []byte) {
	if len(data) >= 3 {
		switch data[2] {
		case PktTypeCallRequest:
			InterfaceCallRequest.Add(ifname, 1)
		case PktTypeCallConnected:
			InterfaceCallConnected.Add(ifname, 1)
		case PktTypeClearRequest:
			InterfaceClearRequest.Add(ifname, 1)
		case PktTypeClearConfirm:
			InterfaceClearConfirm.Add(ifname, 1)
		}
	}
}

// SendXot sends an X.25 packet over a TCP connection with RFC 1613 framing
func SendXot(ifname string, conn net.Conn, data []byte) error {
	length := uint16(len(data))
	updateCallRequestCount(ifname, data)
	// For packet-oriented sockets, we MUST send in a single Write.
	// We also use a single write for small packets on stream sockets to reduce syscalls.
	if isPacketConn(conn) || length < 4096 {
		buf := bufferPool.Get().([]byte)
		defer bufferPool.Put(buf)

		binary.BigEndian.PutUint16(buf[0:2], XotVersion)
		binary.BigEndian.PutUint16(buf[2:4], length)
		copy(buf[4:], data)
		n, err := conn.Write(buf[0 : 4+length])
		if err == nil {
			InterfacePacketsSent.Add(ifname, 1)
			InterfaceBytesSent.Add(ifname, int64(n))
		}
		return err
	}

	header := headerPool.Get().([]byte)
	defer headerPool.Put(header)

	binary.BigEndian.PutUint16(header[0:2], XotVersion)
	binary.BigEndian.PutUint16(header[2:4], length)

	_, err := conn.Write(header)
	if err != nil {
		return err
	}
	n, err := conn.Write(data)
	if err == nil {
		InterfacePacketsSent.Add(ifname, 1)
		InterfaceBytesSent.Add(ifname, int64(n))
	}
	return err
}

// ReadXotInto reads an X.25 packet into the provided buffer.
// It returns the sub-slice of buf containing the data, or an error.
func ReadXotInto(ifname string, conn net.Conn, buf []byte) ([]byte, error) {
	if isPacketConn(conn) {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		if n < 4 {
			return nil, io.ErrUnexpectedEOF
		}
		InterfacePacketsReceived.Add(ifname, 1)
		InterfaceBytesReceived.Add(ifname, int64(n))

		version := binary.BigEndian.Uint16(buf[0:2])
		if version != XotVersion {
			return nil, fmt.Errorf("unsupported XOT version: %d", version)
		}

		length := binary.BigEndian.Uint16(buf[2:4])
		if int(length) > MaxX25PacketSize {
			return buf[4:n], fmt.Errorf("%w: XOT packet too large: %d > %d", ErrPacketTooLong, length, MaxX25PacketSize)
		}
		if int(length) != n-4 {
			return nil, fmt.Errorf("XOT length mismatch: header says %d, read %d", length, n-4)
		}

		res := buf[4:n]
		updateCallRequestCount(ifname, res)
		return res, nil
	}

	header := headerPool.Get().([]byte)
	defer headerPool.Put(header)

	n, err := io.ReadFull(conn, header)
	if err != nil {
		return nil, err
	}

	if n < 4 {
		return nil, io.ErrUnexpectedEOF
	}
	InterfacePacketsReceived.Add(ifname, 1)
	InterfaceBytesReceived.Add(ifname, int64(n))

	version := binary.BigEndian.Uint16(header[0:2])
	if version != XotVersion {
		return nil, fmt.Errorf("unsupported XOT version: %d", version)
	}

	length := binary.BigEndian.Uint16(header[2:4])
	if int(length) > MaxX25PacketSize {
		// Read at least the first 3 bytes to try and get the LCI
		io.ReadFull(conn, buf[:3])
		return buf[:3], fmt.Errorf("%w: XOT packet too large: %d > %d", ErrPacketTooLong, length, MaxX25PacketSize)
	}

	if int(length) > len(buf) {
		return nil, fmt.Errorf("buffer too small: need %d, have %d", length, len(buf))
	}

	res := buf[:length]
	_, err = io.ReadFull(conn, res)
	if err != nil {
		return nil, err
	}
	updateCallRequestCount(ifname, res)

	return res, nil
}

// ReadXot reads an X.25 packet from a TCP connection with RFC 1613 framing
func ReadXot(ifname string, conn net.Conn) ([]byte, error) {
	buf := bufferPool.Get().([]byte)
	defer bufferPool.Put(buf)

	data, err := ReadXotInto(ifname, conn, buf)
	if err != nil {
		return nil, err
	}
	// We MUST copy here because the buf is returned to the pool
	res := make([]byte, len(data))
	copy(res, data)
	return res, nil
}

func GetFd(conn net.Conn) int {
	if sc, ok := conn.(syscall.Conn); ok {
		raw, err := sc.SyscallConn()
		if err == nil {
			var fd uintptr
			raw.Control(func(f uintptr) {
				fd = f
			})
			return int(fd)
		}
	}
	return 0
}
