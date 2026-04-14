package xot

import (
	"fmt"
	"log"
)

const (
	GFIStandard = 0x01
	LCIControl  = 0
)

const (
	PktTypeCallRequest      = 0x0B
	PktTypeCallConnected    = 0x0F
	PktTypeClearRequest     = 0x13
	PktTypeClearConfirm     = 0x17
	PktTypeData             = 0x00 // Base type for data packets
	PktTypeRR               = 0x01
	PktTypeRNR              = 0x05
	PktTypeREJ              = 0x09
	PktTypeResetRequest     = 0x1B
	PktTypeResetConfirm     = 0x1F
	PktTypeRestartRequest   = 0xFB
	PktTypeRestartConfirm   = 0xFF
	PktTypeDiagnostic       = 0xF1
	PktTypeRegistrationReq  = 0xF3
	PktTypeRegistrationConf = 0xF7
)

const (
	CauseOutofOrder    = 0x01
)

type X25Packet struct {
	GFI     byte
	LCI     uint16
	Type    byte
	Payload []byte
}

func ParseX25(data []byte) (*X25Packet, error) {
	if len(data) < 3 {
		return nil, fmt.Errorf("X.25 packet too short: %d bytes", len(data))
	}

	gfi := (data[0] >> 4) & 0x0F
	lci := (uint16(data[0]&0x0F) << 8) | uint16(data[1])
	pktType := data[2]

	return &X25Packet{
		GFI:     gfi,
		LCI:     lci,
		Type:    pktType,
		Payload: data[3:],
	}, nil
}

func (p *X25Packet) IsData() bool {
	return (p.Type & 0x01) == 0
}

func (p *X25Packet) GetBaseType() byte {
	if p.IsData() {
		return PktTypeData
	}
	// For S-frames (RR, RNR, REJ), the type is in the lower 4 bits (excluding bit 0 which is 1)
	// Actually, bits 3-1 define the type: 000 (RR), 010 (RNR), 100 (REJ)
	if (p.Type & 0x0F) == 0x01 || (p.Type & 0x0F) == 0x05 || (p.Type & 0x0F) == 0x09 {
		return p.Type & 0x0F
	}
	return p.Type
}

func (p *X25Packet) Serialize() []byte {
	data := make([]byte, 3+len(p.Payload))
	data[0] = (p.GFI << 4) | byte((p.LCI>>8)&0x0F)
	data[1] = byte(p.LCI & 0xFF)
	data[2] = p.Type
	copy(data[3:], p.Payload)
	return data
}

func (p *X25Packet) TypeName() string {
	if p.IsData() {
		return "DATA"
	}
	switch p.GetBaseType() {
	case PktTypeCallRequest:
		return "CALL_REQ"
	case PktTypeCallConnected:
		return "CALL_CONN"
	case PktTypeClearRequest:
		return "CLR_REQ"
	case PktTypeClearConfirm:
		return "CLR_CONF"
	case PktTypeRR:
		return "RR"
	case PktTypeRNR:
		return "RNR"
	case PktTypeREJ:
		return "REJ"
	case PktTypeResetRequest:
		return "RESET_REQ"
	case PktTypeResetConfirm:
		return "RESET_CONF"
	case PktTypeRestartRequest:
		return "RESTART_REQ"
	case PktTypeRestartConfirm:
		return "RESTART_CONF"
	case PktTypeDiagnostic:
		return "DIAG"
	case PktTypeRegistrationReq:
		return "REG_REQ"
	case PktTypeRegistrationConf:
		return "REG_CONF"
	}
	return fmt.Sprintf("UNKNOWN(0x%02X)", p.Type)
}

func LogTrace(source, dest string, pkt *X25Packet) {
	log.Printf("%s>%s %s % X", source, dest, pkt.TypeName(), pkt.Serialize())
}

// ParseCallRequest extracts addresses from a Call Request packet
func (p *X25Packet) ParseCallRequest() (called, calling string, err error) {
	if p.Type != PktTypeCallRequest {
		return "", "", fmt.Errorf("not a call request")
	}
	if len(p.Payload) < 1 {
		return "", "", fmt.Errorf("call request payload too short: %d bytes", len(p.Payload))
	}

	addrLens := p.Payload[0]
	calledLen := int(addrLens >> 4)
	callingLen := int(addrLens & 0x0F)

	offset := 1
	totalAddrBytes := (calledLen + callingLen + 1) / 2
	if len(p.Payload) < offset+totalAddrBytes {
		return "", "", fmt.Errorf("payload too short for addresses: need %d, have %d", offset+totalAddrBytes, len(p.Payload))
	}

	addrData := p.Payload[offset : offset+totalAddrBytes]
	
	// Decode BCD addresses
	decode := func(data []byte, length int, startNibble int) string {
		res := ""
		nibble := startNibble
		for i := 0; i < length; i++ {
			byteIdx := nibble / 2
			if byteIdx >= len(data) {
				break
			}
			val := data[byteIdx]
			if nibble%2 == 0 {
				res += fmt.Sprintf("%x", val>>4)
			} else {
				res += fmt.Sprintf("%x", val&0x0F)
			}
			nibble++
		}
		return res
	}

	called = decode(addrData, calledLen, 0)
	calling = decode(addrData, callingLen, calledLen)

	return called, calling, nil
}

func CreateClearRequest(lci uint16, cause byte, diag byte) *X25Packet {
	return &X25Packet{
		GFI:     GFIStandard,
		LCI:     lci,
		Type:    PktTypeClearRequest,
		Payload: []byte{cause, diag},
	}
}
