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
			return make([]byte, 65536+4) // Max XOT packet size
		},
	}
)

const (
	XotVersion = 0
)

func isPacketConn(conn net.Conn) bool {
	network := conn.LocalAddr().Network()
	return network == "unixpacket"
}

// SendXot sends an X.25 packet over a TCP connection with RFC 1613 framing
func SendXot(conn net.Conn, data []byte) error {
	length := uint16(len(data))

	// For packet-oriented sockets, we MUST send in a single Write.
	// We also use a single write for small packets on stream sockets to reduce syscalls.
	if isPacketConn(conn) || length < 4096 {
		buf := bufferPool.Get().([]byte)
		defer bufferPool.Put(buf)

		binary.BigEndian.PutUint16(buf[0:2], XotVersion)
		binary.BigEndian.PutUint16(buf[2:4], length)
		copy(buf[4:], data)
		_, err := conn.Write(buf[0 : 4+length])
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
	_, err = conn.Write(data)
	return err
}

// ReadXot reads an X.25 packet from a TCP connection with RFC 1613 framing
func ReadXot(conn net.Conn) ([]byte, error) {
	if isPacketConn(conn) {
		buf := bufferPool.Get().([]byte)
		defer bufferPool.Put(buf)

		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		if n < 4 {
			return nil, io.ErrUnexpectedEOF
		}

		version := binary.BigEndian.Uint16(buf[0:2])
		if version != XotVersion {
			return nil, fmt.Errorf("unsupported XOT version: %d", version)
		}

		length := binary.BigEndian.Uint16(buf[2:4])
		if int(length) != n-4 {
			return nil, fmt.Errorf("XOT length mismatch: header says %d, read %d", length, n-4)
		}

		res := make([]byte, length)
		copy(res, buf[4:n])
		return res, nil
	}

	header := headerPool.Get().([]byte)
	defer headerPool.Put(header)

	_, err := io.ReadFull(conn, header)
	if err != nil {
		return nil, err
	}

	version := binary.BigEndian.Uint16(header[0:2])
	if version != XotVersion {
		return nil, fmt.Errorf("unsupported XOT version: %d", version)
	}

	length := binary.BigEndian.Uint16(header[2:4])
	data := make([]byte, length)
	_, err = io.ReadFull(conn, data)
	if err != nil {
		return nil, err
	}

	return data, nil
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
