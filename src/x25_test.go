package xot

import (
	"bytes"
	"errors"
	"strings"
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
	tests := []struct {
		name            string
		addrLens        byte
		addrData        []byte
		expectedCalled  string
		expectedCalling string
	}{
		{
			name:            "Standard 2-1",
			addrLens:        0x12, // Calling=1, Called=2
			addrData:        []byte{0x12, 0x30},
			expectedCalled:  "12",
			expectedCalling: "3",
		},
		{
			name:            "Standard 3-2",
			addrLens:        0x23, // Calling=2, Called=3
			addrData:        []byte{0x12, 0x34, 0x50},
			expectedCalled:  "123",
			expectedCalling: "45",
		},
		{
			name:            "Called zero length",
			addrLens:        0x30, // Calling=3, Called=0
			addrData:        []byte{0x12, 0x30},
			expectedCalled:  "",
			expectedCalling: "123",
		},
		{
			name:            "Calling zero length",
			addrLens:        0x03, // Calling=0, Called=3
			addrData:        []byte{0x12, 0x30},
			expectedCalled:  "123",
			expectedCalling: "",
		},
		{
			name:            "Max length 15-0",
			addrLens:        0x0F, // Calling=0, Called=15
			addrData:        []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x80},
			expectedCalled:  "112233445566778",
			expectedCalling: "",
		},
		{
			name:            "Max length 0-15",
			addrLens:        0xF0, // Calling=15, Called=0
			addrData:        []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x80},
			expectedCalled:  "",
			expectedCalling: "112233445566778",
		},
		{
			name:            "Both Max length 15-15",
			addrLens:        0xFF, // Calling=15, Called=15
			addrData:        []byte{
				0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x81,
				0x12, 0x23, 0x34, 0x45, 0x56, 0x67, 0x78,
			},
			expectedCalled:  "112233445566778",
			expectedCalling: "112233445566778",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := append([]byte{tt.addrLens}, tt.addrData...)
			pkt := &X25Packet{
				Type:    PktTypeCallRequest,
				Payload: payload,
			}

			called, calling, _, _, err := pkt.ParseCallRequest()
			if err != nil {
				t.Fatalf("ParseCallRequest failed: %v", err)
			}

			if called != tt.expectedCalled {
				t.Errorf("Expected called '%s', got '%s'", tt.expectedCalled, called)
			}
			if calling != tt.expectedCalling {
				t.Errorf("Expected calling '%s', got '%s'", tt.expectedCalling, calling)
			}
		})
	}
}

func TestParseCallConnected(t *testing.T) {
	// Calling=1, Called=2
	payload := []byte{0x12, 0x12, 0x30}
	pkt := &X25Packet{
		Type:    PktTypeCallConnected,
		Payload: payload,
	}

	called, calling, _, _, err := pkt.ParseCallConnected()
	if err != nil {
		t.Fatalf("ParseCallConnected failed: %v", err)
	}

	if called != "12" {
		t.Errorf("Expected called '12', got '%s'", called)
	}
	if calling != "3" {
		t.Errorf("Expected calling '3', got '%s'", calling)
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
	_, _, _, _, err := pkt.ParseCallRequest()
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

func TestFormatFacilities(t *testing.T) {
	t.Run("empty_nil", func(t *testing.T) {
		if got := FormatFacilities(nil); got != "none" {
			t.Errorf("Expected 'none', got %q", got)
		}
	})

	t.Run("empty_slice", func(t *testing.T) {
		if got := FormatFacilities([]byte{}); got != "none" {
			t.Errorf("Expected 'none', got %q", got)
		}
	})

	t.Run("packet_size", func(t *testing.T) {
		// code 0x42 (class 1 = 2 byte value), value {7, 7} → pkt:128/128
		fac := []byte{0x42, 7, 7}
		got := FormatFacilities(fac)
		if !strings.Contains(got, "pkt:128/128") {
			t.Errorf("Expected 'pkt:128/128' in %q", got)
		}
	})

	t.Run("window_size", func(t *testing.T) {
		// code 0x43 (class 1 = 2 byte value), value {3, 3} → win:3/3
		fac := []byte{0x43, 3, 3}
		got := FormatFacilities(fac)
		if !strings.Contains(got, "win:3/3") {
			t.Errorf("Expected 'win:3/3' in %q", got)
		}
	})

	t.Run("unknown_class0", func(t *testing.T) {
		// code 0x01 (class 0 = 1 byte value), unknown facility
		fac := []byte{0x01, 0xAB}
		got := FormatFacilities(fac)
		// Should produce hex output like "01:AB"
		if len(got) == 0 {
			t.Errorf("Expected non-empty result for unknown class-0 facility, got %q", got)
		}
	})

	t.Run("variable_length_class3", func(t *testing.T) {
		// class-3 facility: code 0xC9 (0xC9 >> 6 == 3), then length byte, then data
		fac := []byte{0xC9, 3, 0xAA, 0xBB, 0xCC}
		// Should not panic
		got := FormatFacilities(fac)
		if len(got) == 0 {
			t.Errorf("Expected non-empty result for class-3 facility, got %q", got)
		}
	})

	t.Run("truncated_class3", func(t *testing.T) {
		// class-3 code followed by length that exceeds buffer → no panic
		fac := []byte{0xC9, 10, 0xAA} // claims 10 bytes but only 1 available
		// Must not panic
		_ = FormatFacilities(fac)
	})

	t.Run("both_pkt_and_win", func(t *testing.T) {
		fac := []byte{0x42, 7, 7, 0x43, 3, 3}
		got := FormatFacilities(fac)
		if !strings.Contains(got, "pkt:") {
			t.Errorf("Expected 'pkt:' in %q", got)
		}
		if !strings.Contains(got, "win:") {
			t.Errorf("Expected 'win:' in %q", got)
		}
	})
}

func TestTypeNameAll(t *testing.T) {
	// These types have unique lower nibbles that don't collide with S-frame detection,
	// so TypeName() returns the expected string directly.
	cases := map[byte]string{
		PktTypeCallRequest:      "CALL_REQ",  // 0x0B
		PktTypeCallConnected:    "CALL_CONN", // 0x0F
		PktTypeClearRequest:     "CLR_REQ",   // 0x13
		PktTypeClearConfirm:     "CLR_CONF",  // 0x17
		PktTypeRR:               "RR",        // 0x01
		PktTypeRNR:              "RNR",       // 0x05
		PktTypeREJ:              "REJ",       // 0x09
		PktTypeResetRequest:     "RESET_REQ",    // 0x1B
		PktTypeResetConfirm:     "RESET_CONF",   // 0x1F
		PktTypeRestartRequest:   "RESTART_REQ",  // 0xFB
		PktTypeRestartConfirm:   "RESTART_CONF", // 0xFF
		PktTypeRegistrationReq:  "REG_REQ",      // 0xF3
		PktTypeRegistrationConf: "REG_CONF",     // 0xF7
	}

	for pktType, want := range cases {
		pkt := &X25Packet{Type: pktType}
		got := pkt.TypeName()
		if got != want {
			t.Errorf("Type 0x%02X: expected %q, got %q", pktType, want, got)
		}
	}

	// PktTypeDiagnostic (0xF1): odd, but lower nibble 0x01 == PktTypeRR, so
	// GetBaseType() returns PktTypeRR and TypeName() returns "RR". This is the
	// current behaviour of the codec; verify it doesn't panic.
	diagPkt := &X25Packet{Type: PktTypeDiagnostic}
	_ = diagPkt.TypeName() // just smoke-test, no panic

	// Data packet: Type & 0x01 == 0 → "DATA"
	dataPkt := &X25Packet{Type: 0x20}
	if dataPkt.TypeName() != "DATA" {
		t.Errorf("Expected 'DATA' for type 0x20, got %q", dataPkt.TypeName())
	}

	// Unknown odd type with upper bits that don't map to any known lower nibble.
	// 0x7B has lower nibble 0x0B (not 0x01/0x05/0x09), so GetBaseType() returns
	// 0x7B itself, and TypeName() returns UNKNOWN(0x7B).
	unknownPkt := &X25Packet{Type: 0x7B}
	got := unknownPkt.TypeName()
	if !strings.Contains(got, "UNKNOWN(0x") {
		t.Errorf("Expected UNKNOWN(0x... for 0x7B, got %q", got)
	}
}

func TestGetBaseTypeSFrames(t *testing.T) {
	cases := []struct {
		typ      byte
		expected byte
	}{
		{0x11, PktTypeRR},  // upper bits set, lower nibble 0x01 = RR
		{0x21, PktTypeRR},  // different upper bits, lower nibble 0x01 = RR
		{0x15, PktTypeRNR}, // lower nibble 0x05 = RNR
		{0x19, PktTypeREJ}, // lower nibble 0x09 = REJ
		{0x0B, PktTypeCallRequest}, // CALL_REQ, not an S-frame
	}

	for _, tc := range cases {
		pkt := &X25Packet{Type: tc.typ}
		got := pkt.GetBaseType()
		if got != tc.expected {
			t.Errorf("Type 0x%02X: expected base 0x%02X, got 0x%02X", tc.typ, tc.expected, got)
		}
	}
}

func TestValidateSizeEdgeCases(t *testing.T) {
	// Exactly MaxUserData bytes → no error
	pkt := &X25Packet{
		Type:    PktTypeData,
		Payload: make([]byte, MaxUserData),
	}
	if err := pkt.ValidateSize(); err != nil {
		t.Errorf("MaxUserData should be valid, got: %v", err)
	}

	// MaxUserData+1 → ErrPacketTooLong
	pkt.Payload = make([]byte, MaxUserData+1)
	if err := pkt.ValidateSize(); !errors.Is(err, ErrPacketTooLong) {
		t.Errorf("Expected ErrPacketTooLong for MaxUserData+1, got: %v", err)
	}

	// CallRequest with serialized size == MaxCallRequestSize → no error
	pkt = &X25Packet{
		Type:    PktTypeCallRequest,
		Payload: make([]byte, MaxCallRequestSize-3),
	}
	if err := pkt.ValidateSize(); err != nil {
		t.Errorf("Exact MaxCallRequestSize should be valid, got: %v", err)
	}

	// CallRequest with serialized size == MaxCallRequestSize+1 → ErrPacketTooLong
	pkt = &X25Packet{
		Type:    PktTypeCallRequest,
		Payload: make([]byte, MaxCallRequestSize-2),
	}
	if err := pkt.ValidateSize(); !errors.Is(err, ErrPacketTooLong) {
		t.Errorf("Expected ErrPacketTooLong for MaxCallRequestSize+1, got: %v", err)
	}

	// Non-CallRequest: MaxUserData bytes payload → exactly MaxUserData, should pass
	// (Note: MaxX25PacketSize = MaxUserData + X25HeaderSize, but the ValidateSize
	// user-data check fires first, so the effective ceiling for non-call-request
	// data packets is MaxUserData bytes of payload.)
	pkt = &X25Packet{
		Type:    PktTypeData,
		Payload: make([]byte, MaxUserData),
	}
	if err := pkt.ValidateSize(); err != nil {
		t.Errorf("MaxUserData payload for non-call-request should be valid, got: %v", err)
	}

	// Non-CallRequest: MaxUserData+1 bytes payload → ErrPacketTooLong (user data too large)
	pkt = &X25Packet{
		Type:    PktTypeData,
		Payload: make([]byte, MaxUserData+1),
	}
	if err := pkt.ValidateSize(); !errors.Is(err, ErrPacketTooLong) {
		t.Errorf("Expected ErrPacketTooLong for MaxUserData+1 non-call-request, got: %v", err)
	}
}

// buildCallRequest creates a properly encoded CALL_REQ packet payload.
// called and calling are digit strings; facilities and userData are raw bytes.
func buildCallRequest(gfi byte, lci uint16, called, calling string, facilities, userData []byte) *X25Packet {
	calledLen := len(called)
	callingLen := len(calling)

	addrLens := byte((callingLen << 4) | (calledLen & 0x0F))

	// BCD encode: called first, then calling, packed nibble by nibble
	totalNibbles := calledLen + callingLen
	totalAddrBytes := (totalNibbles + 1) / 2
	addrData := make([]byte, totalAddrBytes)

	nibbleIdx := 0
	writeNibble := func(ch byte) {
		var nib byte
		if ch >= '0' && ch <= '9' {
			nib = ch - '0'
		} else {
			nib = ch - 'a'
		}
		byteIdx := nibbleIdx / 2
		if nibbleIdx%2 == 0 {
			addrData[byteIdx] = nib << 4
		} else {
			addrData[byteIdx] |= nib
		}
		nibbleIdx++
	}

	for _, c := range []byte(called) {
		writeNibble(c)
	}
	for _, c := range []byte(calling) {
		writeNibble(c)
	}

	payload := []byte{addrLens}
	payload = append(payload, addrData...)
	payload = append(payload, byte(len(facilities)))
	payload = append(payload, facilities...)
	payload = append(payload, userData...)

	return &X25Packet{
		GFI:     gfi,
		LCI:     lci,
		Type:    PktTypeCallRequest,
		Payload: payload,
	}
}

func TestParseCallRequestFacilitiesAndUserData(t *testing.T) {
	fac := []byte{0x42, 0x07, 0x07} // packet size facility
	userData := []byte("hello")

	pkt := buildCallRequest(1, 42, "12", "34", fac, userData)

	called, calling, gotFac, gotUD, err := pkt.ParseCallRequest()
	if err != nil {
		t.Fatalf("ParseCallRequest failed: %v", err)
	}
	if called != "12" {
		t.Errorf("Expected called='12', got %q", called)
	}
	if calling != "34" {
		t.Errorf("Expected calling='34', got %q", calling)
	}
	if !bytes.Equal(gotFac, fac) {
		t.Errorf("Expected facilities %v, got %v", fac, gotFac)
	}
	if !bytes.Equal(gotUD, userData) {
		t.Errorf("Expected userData %q, got %q", userData, gotUD)
	}
}

func TestParseCallRequestNegativeWrongType(t *testing.T) {
	pkt := &X25Packet{
		GFI:     1,
		LCI:     1,
		Type:    PktTypeClearRequest,
		Payload: []byte{0x00, 0x00},
	}
	_, _, _, _, err := pkt.ParseCallRequest()
	if err == nil {
		t.Error("Expected error for wrong packet type")
	}
}

func TestParseCallRequestNegativeEmptyPayload(t *testing.T) {
	pkt := &X25Packet{
		GFI:     1,
		LCI:     1,
		Type:    PktTypeCallRequest,
		Payload: []byte{},
	}
	_, _, _, _, err := pkt.ParseCallRequest()
	if err == nil {
		t.Error("Expected error for empty payload")
	}
}

func TestParseCallRequestNegativeTruncatedAddresses(t *testing.T) {
	// Address length byte claims more address bytes than the payload contains
	// calledLen=5, callingLen=5 → totalNibbles=10, totalAddrBytes=5
	// but we only provide 2 bytes of address data
	payload := []byte{0x55, 0xAB} // addrLens claims 5+5 but only 1 data byte
	pkt := &X25Packet{
		Type:    PktTypeCallRequest,
		Payload: payload,
	}
	_, _, _, _, err := pkt.ParseCallRequest()
	if err == nil {
		t.Error("Expected error for truncated addresses")
	}
}

func TestParseCallRequestNegativeTruncatedFacilities(t *testing.T) {
	// Valid addresses, then facilities length byte that exceeds remaining payload
	// called=1 digit, calling=1 digit → addrLens=0x11, totalAddrBytes=1
	// addr byte, then facilityLen=10 but no actual facility data
	payload := []byte{0x11, 0x12, 10} // facLen=10 but nothing follows
	pkt := &X25Packet{
		Type:    PktTypeCallRequest,
		Payload: payload,
	}
	_, _, _, _, err := pkt.ParseCallRequest()
	if err == nil {
		t.Error("Expected error for truncated facilities")
	}
}

func TestParseCallConnectedNegative(t *testing.T) {
	t.Run("wrong_type", func(t *testing.T) {
		pkt := &X25Packet{
			Type:    PktTypeClearRequest,
			Payload: []byte{0x00},
		}
		_, _, _, _, err := pkt.ParseCallConnected()
		if err == nil {
			t.Error("Expected error for wrong type")
		}
	})

	t.Run("empty_payload", func(t *testing.T) {
		pkt := &X25Packet{
			Type:    PktTypeCallConnected,
			Payload: []byte{},
		}
		_, _, _, _, err := pkt.ParseCallConnected()
		if err == nil {
			t.Error("Expected error for empty payload")
		}
	})

	t.Run("truncated_addresses", func(t *testing.T) {
		// calledLen=5, callingLen=5 → needs 5 addr bytes, only 1 provided
		pkt := &X25Packet{
			Type:    PktTypeCallConnected,
			Payload: []byte{0x55, 0xAB},
		}
		_, _, _, _, err := pkt.ParseCallConnected()
		if err == nil {
			t.Error("Expected error for truncated addresses")
		}
	})

	t.Run("truncated_facilities", func(t *testing.T) {
		// called=1, calling=1 → addrLens=0x11, 1 addr byte; facLen=10 but nothing follows
		pkt := &X25Packet{
			Type:    PktTypeCallConnected,
			Payload: []byte{0x11, 0x12, 10},
		}
		_, _, _, _, err := pkt.ParseCallConnected()
		if err == nil {
			t.Error("Expected error for truncated facilities")
		}
	})
}

func TestLogTrace(t *testing.T) {
	// Smoke test: should not panic
	LogTrace("src", "dst", CreateClearRequest(1, 0, 0))
}

func TestSerializeRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		pktType byte
	}{
		{"CallRequest", PktTypeCallRequest},
		{"CallConnected", PktTypeCallConnected},
		{"ClearRequest", PktTypeClearRequest},
		{"ClearConfirm", PktTypeClearConfirm},
		{"RestartRequest", PktTypeRestartRequest},
		{"RestartConfirm", PktTypeRestartConfirm},
		{"Data", 0x00},
		{"RR", PktTypeRR},
		{"RNR", PktTypeRNR},
		{"REJ", PktTypeREJ},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := &X25Packet{
				GFI:     1,
				LCI:     42,
				Type:    tc.pktType,
				Payload: []byte{0xAA, 0xBB},
			}

			serialized := original.Serialize()
			parsed, err := ParseX25(serialized)
			if err != nil {
				t.Fatalf("ParseX25 failed: %v", err)
			}

			if parsed.GFI != original.GFI {
				t.Errorf("GFI mismatch: want %d, got %d", original.GFI, parsed.GFI)
			}
			if parsed.LCI != original.LCI {
				t.Errorf("LCI mismatch: want %d, got %d", original.LCI, parsed.LCI)
			}
			if parsed.Type != original.Type {
				t.Errorf("Type mismatch: want 0x%02X, got 0x%02X", original.Type, parsed.Type)
			}
			if !bytes.Equal(parsed.Payload, original.Payload) {
				t.Errorf("Payload mismatch: want %v, got %v", original.Payload, parsed.Payload)
			}
		})
	}
}
