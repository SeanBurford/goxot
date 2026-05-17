package tun

import (
	"bytes"
	"io"
	"testing"
)

// perFrameReader simulates TUN read semantics: one complete frame per Read call.
type perFrameReader struct {
	frames [][]byte
	idx    int
}

func (r *perFrameReader) Read(p []byte) (int, error) {
	if r.idx >= len(r.frames) {
		return 0, io.EOF
	}
	n := copy(p, r.frames[r.idx])
	r.idx++
	return n, nil
}

func TestWriteFrameData(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte{0x10, 0x00, 0x0B, 0xAA, 0xBB}
	if err := WriteFrame(&buf, "test", HeaderData, payload); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	want := append([]byte{0x00, 0x00, 0x08, 0x05, HeaderData}, payload...)
	if !bytes.Equal(got, want) {
		t.Errorf("WriteFrame = %x, want %x", got, want)
	}
}

func TestWriteFrameConnect(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, "test", HeaderConnect, nil); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	want := []byte{0x00, 0x00, 0x08, 0x05, HeaderConnect}
	if !bytes.Equal(got, want) {
		t.Errorf("WriteFrame Connect = %x, want %x", got, want)
	}
}

func TestWriteFrameDisconnect(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, "test", HeaderDisconnect, nil); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	want := []byte{0x00, 0x00, 0x08, 0x05, HeaderDisconnect}
	if !bytes.Equal(got, want) {
		t.Errorf("WriteFrame Disconnect = %x, want %x", got, want)
	}
}

func TestWriteFrameBuf(t *testing.T) {
	var w bytes.Buffer
	payload := []byte{0x10, 0x00, 0x0F}
	buf := make([]byte, MaxPacketSize)
	if err := WriteFrameBuf(&w, "test", HeaderData, payload, buf); err != nil {
		t.Fatal(err)
	}
	got := w.Bytes()
	want := []byte{0x00, 0x00, 0x08, 0x05, HeaderData, 0x10, 0x00, 0x0F}
	if !bytes.Equal(got, want) {
		t.Errorf("WriteFrameBuf = %x, want %x", got, want)
	}
}

func TestReadFrameData(t *testing.T) {
	payload := []byte{0x10, 0x00, 0x0B}
	frame := append([]byte{0x00, 0x00, 0x08, 0x05, HeaderData}, payload...)
	r := &perFrameReader{frames: [][]byte{frame}}
	buf := make([]byte, MaxPacketSize)

	hdr, got, err := ReadFrame(r, "test", buf)
	if err != nil {
		t.Fatal(err)
	}
	if hdr != HeaderData {
		t.Errorf("header = 0x%02x, want HeaderData (0x00)", hdr)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload = %x, want %x", got, payload)
	}
}

func TestReadFrameConnect(t *testing.T) {
	frame := []byte{0x00, 0x00, 0x08, 0x05, HeaderConnect}
	r := &perFrameReader{frames: [][]byte{frame}}
	buf := make([]byte, MaxPacketSize)

	hdr, got, err := ReadFrame(r, "test", buf)
	if err != nil {
		t.Fatal(err)
	}
	if hdr != HeaderConnect {
		t.Errorf("header = 0x%02x, want HeaderConnect (0x01)", hdr)
	}
	if len(got) != 0 {
		t.Errorf("payload len = %d, want 0", len(got))
	}
}

func TestReadFrameSkipsNonX25(t *testing.T) {
	// First frame: IPv4 EtherType (0x0800) — must be skipped.
	nonX25 := []byte{0x00, 0x00, 0x08, 0x00, 0x00, 0xAA, 0xBB}
	// Second frame: valid X.25 EtherType (0x0805).
	payload := []byte{0x10, 0x00, 0x0B}
	valid := append([]byte{0x00, 0x00, 0x08, 0x05, HeaderData}, payload...)

	r := &perFrameReader{frames: [][]byte{nonX25, valid}}
	buf := make([]byte, MaxPacketSize)

	hdr, got, err := ReadFrame(r, "test", buf)
	if err != nil {
		t.Fatal(err)
	}
	if hdr != HeaderData {
		t.Errorf("header = 0x%02x, want HeaderData", hdr)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload = %x, want %x", got, payload)
	}
}

func TestReadFrameSkipsShort(t *testing.T) {
	// First frame: too short to contain a header byte.
	short := []byte{0x00, 0x00, 0x08, 0x05} // only 4 bytes, no control byte
	payload := []byte{0x10, 0x00, 0x0B}
	valid := append([]byte{0x00, 0x00, 0x08, 0x05, HeaderData}, payload...)

	r := &perFrameReader{frames: [][]byte{short, valid}}
	buf := make([]byte, MaxPacketSize)

	hdr, got, err := ReadFrame(r, "test", buf)
	if err != nil {
		t.Fatal(err)
	}
	if hdr != HeaderData {
		t.Errorf("header = 0x%02x, want HeaderData", hdr)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload = %x, want %x", got, payload)
	}
}

func TestReadFrameEOF(t *testing.T) {
	r := &perFrameReader{frames: nil}
	buf := make([]byte, MaxPacketSize)
	_, _, err := ReadFrame(r, "test", buf)
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	payload := []byte{0x10, 0x04, 0x0B, 0x34, 0x12, 0x00, 0x00, 0x00}
	var w bytes.Buffer
	if err := WriteFrame(&w, "test", HeaderData, payload); err != nil {
		t.Fatal(err)
	}
	frame := w.Bytes()
	r := &perFrameReader{frames: [][]byte{frame}}
	buf := make([]byte, MaxPacketSize)
	hdr, got, err := ReadFrame(r, "test", buf)
	if err != nil {
		t.Fatal(err)
	}
	if hdr != HeaderData {
		t.Errorf("header = 0x%02x, want HeaderData", hdr)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload = %x, want %x", got, payload)
	}
}

func TestMaxPacketSize(t *testing.T) {
	if MaxPacketSize <= 5 {
		t.Errorf("MaxPacketSize = %d, too small", MaxPacketSize)
	}
}

func TestHeaderConstants(t *testing.T) {
	if HeaderData != 0x00 {
		t.Errorf("HeaderData = 0x%02x, want 0x00", HeaderData)
	}
	if HeaderConnect != 0x01 {
		t.Errorf("HeaderConnect = 0x%02x, want 0x01", HeaderConnect)
	}
	if HeaderDisconnect != 0x02 {
		t.Errorf("HeaderDisconnect = 0x%02x, want 0x02", HeaderDisconnect)
	}
	if HeaderParam != 0x03 {
		t.Errorf("HeaderParam = 0x%02x, want 0x03", HeaderParam)
	}
}
