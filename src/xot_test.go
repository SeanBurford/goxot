package xot

import (
	"bytes"
	"net"
	"testing"
	"time"
	"io"
	"strings"
)

type mockAddr struct {
	net.Addr
	net string
}

func (a *mockAddr) Network() string { return a.net }
func (a *mockAddr) String() string  { return "mock" }

type mockConn struct {
	net.Conn
	readBuf  *bytes.Buffer
	writeBuf *bytes.Buffer
}

func (m *mockConn) Read(b []byte) (n int, err error) {
	return m.readBuf.Read(b)
}

func (m *mockConn) Write(b []byte) (n int, err error) {
	return m.writeBuf.Write(b)
}

func (m *mockConn) Close() error {
	return nil
}

func (m *mockConn) LocalAddr() net.Addr {
	return &mockAddr{net: "tcp"}
}

func (m *mockConn) RemoteAddr() net.Addr {
	return &mockAddr{net: "tcp"}
}

func (m *mockConn) SetDeadline(t time.Time) error      { return nil }
func (m *mockConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *mockConn) SetWriteDeadline(t time.Time) error { return nil }

func TestXotFraming(t *testing.T) {
	data := []byte{0x10, 0x01, 0x02}
	m := &mockConn{
		readBuf:  bytes.NewBuffer(nil),
		writeBuf: bytes.NewBuffer(nil),
	}

	// Test SendXot
	err := SendXot(m, data)
	if err != nil {
		t.Fatalf("SendXot failed: %v", err)
	}

	expected := []byte{0x00, 0x00, 0x00, 0x03, 0x10, 0x01, 0x02}
	if !bytes.Equal(m.writeBuf.Bytes(), expected) {
		t.Errorf("Expected %v, got %v", expected, m.writeBuf.Bytes())
	}

	// Test ReadXot
	m.readBuf.Write(expected)
	readData, err := ReadXot(m)
	if err != nil {
		t.Fatalf("ReadXot failed: %v", err)
	}
	if !bytes.Equal(readData, data) {
		t.Errorf("Expected %v, got %v", data, readData)
	}
}

func TestReadXotShortHeader(t *testing.T) {
	m := &mockConn{
		readBuf: bytes.NewBuffer([]byte{0x00, 0x00, 0x00}), // Only 3 bytes
	}
	_, err := ReadXot(m)
	if err == nil {
		t.Errorf("Expected error for short header")
	}
}

func TestReadXotOversized(t *testing.T) {
	m := &mockConn{
		readBuf: bytes.NewBuffer(nil),
	}
	
	// Header says 5000 bytes, which is > MaxX25PacketSize
	header := []byte{0x00, 0x00, 0x13, 0x88} // 0x1388 = 5000
	m.readBuf.Write(header)
	
	_, err := ReadXot(m)
	if err == nil {
		t.Errorf("Expected error for oversized packet")
	}
}

func TestReadXotVersionMismatch(t *testing.T) {
	// Use net.Pipe() (stream / TCP-like path)
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		// Write header with version=1 (bad), length=3, then 3 data bytes
		header := []byte{0x00, 0x01, 0x00, 0x03} // version=1, length=3
		data := []byte{0xAA, 0xBB, 0xCC}
		client.Write(header)
		client.Write(data)
	}()

	_, err := ReadXot(server)
	if err == nil {
		t.Error("Expected error for bad XOT version")
	}
	if !strings.Contains(err.Error(), "unsupported XOT version") {
		t.Errorf("Expected 'unsupported XOT version' in error, got: %v", err)
	}
}

func TestReadXotStreamLengthMismatch(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	go func() {
		// Valid version=0 header claiming length=10 but only 5 bytes of data
		header := []byte{0x00, 0x00, 0x00, 0x0A} // version=0, length=10
		data := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
		client.Write(header)
		client.Write(data)
		client.Close() // close to trigger EOF
	}()

	_, err := ReadXot(server)
	if err == nil {
		t.Error("Expected error for length mismatch")
	}
}

func TestSendXotThenReadLarge(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	// Build a 4095-byte payload (close to the limit)
	payload := make([]byte, 4095)
	for i := range payload {
		payload[i] = byte(i)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- SendXot(client, payload)
	}()

	got, err := ReadXot(server)
	if err != nil {
		t.Fatalf("ReadXot failed: %v", err)
	}
	if sendErr := <-errCh; sendErr != nil {
		t.Fatalf("SendXot failed: %v", sendErr)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("Payload mismatch: sent %d bytes, got %d bytes", len(payload), len(got))
	}
}

// nonSyscallConn implements net.Conn but NOT syscall.Conn.
type nonSyscallConn struct {
	net.Conn
}

func (n *nonSyscallConn) Read(b []byte) (int, error)  { return 0, io.EOF }
func (n *nonSyscallConn) Write(b []byte) (int, error) { return 0, io.EOF }
func (n *nonSyscallConn) Close() error                { return nil }
func (n *nonSyscallConn) LocalAddr() net.Addr         { return &mockAddr{net: "tcp"} }
func (n *nonSyscallConn) RemoteAddr() net.Addr        { return &mockAddr{net: "tcp"} }
func (n *nonSyscallConn) SetDeadline(t time.Time) error      { return nil }
func (n *nonSyscallConn) SetReadDeadline(t time.Time) error  { return nil }
func (n *nonSyscallConn) SetWriteDeadline(t time.Time) error { return nil }

func TestGetFdFromTCPConn(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer ln.Close()

	connCh := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		connCh <- c
	}()

	dial, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Failed to dial: %v", err)
	}
	defer dial.Close()

	server := <-connCh
	defer server.Close()

	fd := GetFd(server)
	if fd <= 0 {
		t.Errorf("Expected fd > 0 for TCP conn, got %d", fd)
	}
}

func TestGetFdNonSyscallConn(t *testing.T) {
	c := &nonSyscallConn{}
	fd := GetFd(c)
	if fd != 0 {
		t.Errorf("Expected fd=0 for non-syscall conn, got %d", fd)
	}
}
