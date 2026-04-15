package xot

import (
	"bytes"
	"net"
	"testing"
	"time"
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
