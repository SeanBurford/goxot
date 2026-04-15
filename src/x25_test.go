package xot

import (
	"bytes"
	"testing"
)

func TestParseX25(t *testing.T) {
	// Call Request packet
	data := []byte{0x10, 0x01, 0x0B, 0x22, 0x12, 0x34, 0x56}
	pkt, err := ParseX25(data)
	if err != nil {
		t.Fatalf("ParseX25 failed: %v", err)
	}

	if pkt.GFI != 0x01 {
		t.Errorf("Expected GFI 1, got %d", pkt.GFI)
	}
	if pkt.LCI != 1 {
		t.Errorf("Expected LCI 1, got %d", pkt.LCI)
	}
	if pkt.Type != PktTypeCallRequest {
		t.Errorf("Expected Call Request type, got 0x%02X", pkt.Type)
	}
}

func TestSerializeX25(t *testing.T) {
	pkt := &X25Packet{
		GFI:     0x01,
		LCI:     0x123,
		Type:    PktTypeCallConnected,
		Payload: []byte{0xAA, 0xBB},
	}

	data := pkt.Serialize()
	expected := []byte{0x11, 0x23, 0x0F, 0xAA, 0xBB}
	if !bytes.Equal(data, expected) {
		t.Errorf("Expected %v, got %v", expected, data)
	}
}

func TestParseCallRequest(t *testing.T) {
	// GFI=1, LCI=1, Type=CallRequest, AddrLens=0x21 (Called=2, Calling=1), Addrs=0x12, 0x30
	data := []byte{0x10, 0x01, 0x0B, 0x21, 0x12, 0x30}
	pkt, _ := ParseX25(data)

	called, calling, err := pkt.ParseCallRequest()
	if err != nil {
		t.Fatalf("ParseCallRequest failed: %v", err)
	}

	if called != "12" {
		t.Errorf("Expected called '12', got '%s'", called)
	}
	if calling != "3" {
		t.Errorf("Expected calling '3', got '%s'", calling)
	}

	// Test with odd number of nibbles total
	// AddrLens=0x32 (Called=3, Calling=2), Addrs=0x12, 0x34, 0x50
	data2 := []byte{0x10, 0x01, 0x0B, 0x32, 0x12, 0x34, 0x50}
	pkt2, _ := ParseX25(data2)
	called2, calling2, err2 := pkt2.ParseCallRequest()
	if err2 != nil {
		t.Fatalf("ParseCallRequest failed: %v", err2)
	}
	if called2 != "123" {
		t.Errorf("Expected called '123', got '%s'", called2)
	}
	if calling2 != "45" {
		t.Errorf("Expected calling '45', got '%s'", calling2)
	}
}

func TestParseX25Short(t *testing.T) {
	data := []byte{0x10, 0x01}
	_, err := ParseX25(data)
	if err == nil {
		t.Errorf("Expected error for short packet")
	}
}

func TestParseCallRequestShort(t *testing.T) {
	data := []byte{0x10, 0x01, 0x0B} // No payload
	pkt, _ := ParseX25(data)
	_, _, err := pkt.ParseCallRequest()
	if err == nil {
		t.Errorf("Expected error for short call request payload")
	}
}

func TestTypeName(t *testing.T) {
	pkt := &X25Packet{Type: PktTypeCallRequest}
	if pkt.TypeName() != "CALL_REQ" {
		t.Errorf("Expected CALL_REQ, got %s", pkt.TypeName())
	}
	pkt.Type = 0xFF // Unknown
	if pkt.TypeName() == "" {
		t.Errorf("Expected non-empty TypeName for unknown type")
	}
}

func TestCreateClearRequest(t *testing.T) {
	pkt := CreateClearRequest(0x123, 0x01, 0x02)
	if pkt.Type != PktTypeClearRequest {
		t.Errorf("Expected Clear Request type")
	}
	if pkt.LCI != 0x123 {
		t.Errorf("Expected LCI 0x123")
	}
	if pkt.Payload[0] != 0x01 || pkt.Payload[1] != 0x02 {
		t.Errorf("Expected cause 1 and diag 2")
	}
}

func TestIsData(t *testing.T) {
	pkt := &X25Packet{Type: 0x00} // Data bit 0 is 0
	if !pkt.IsData() {
		t.Errorf("Expected 0x00 to be data")
	}
	pkt.Type = 0x01 // Control bit 0 is 1
	if pkt.IsData() {
		t.Errorf("Expected 0x01 to be control")
	}
}

func TestGetBaseType(t *testing.T) {
	pkt := &X25Packet{Type: 0x01} // RR
	if pkt.GetBaseType() != PktTypeRR {
		t.Errorf("Expected RR, got 0x%02X", pkt.GetBaseType())
	}
	pkt.Type = 0x11 // RR with P(R)=1
	if pkt.GetBaseType() != PktTypeRR {
		t.Errorf("Expected RR, got 0x%02X", pkt.GetBaseType())
	}
}

func TestValidateSize(t *testing.T) {
	// Normal data packet
	pkt := &X25Packet{
		Type:    PktTypeData,
		Payload: make([]byte, MaxUserData),
	}
	if err := pkt.ValidateSize(); err != nil {
		t.Errorf("Valid data packet rejected: %v", err)
	}

	// Oversized data packet
	pkt.Payload = make([]byte, MaxUserData+1)
	if err := pkt.ValidateSize(); err == nil {
		t.Errorf("Oversized data packet accepted")
	}

	// Normal call request
	pkt = &X25Packet{
		Type:    PktTypeCallRequest,
		Payload: make([]byte, MaxCallRequestSize-3),
	}
	if err := pkt.ValidateSize(); err != nil {
		t.Errorf("Valid call request rejected: %v", err)
	}

	// Oversized call request
	pkt.Payload = make([]byte, MaxCallRequestSize-2)
	if err := pkt.ValidateSize(); err == nil {
		t.Errorf("Oversized call request accepted")
	}
}
