package main

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	xot "github.com/SeanBurford/goxot/src"
)

var (
	localAddr = flag.String("a", "127001", "Local X.25 address")
	destAddr  = flag.String("d", "127100", "Destination X.25 address")
	bufSize   = flag.Int("l", 64, "Data payload size in bytes (data method)")
	runTime   = flag.Int("T", 0, "Run time in seconds (0 = indefinite)")
	gateway   = flag.String("g", "localhost:1998", "XOT server address[:port]")
	lciFlag   = flag.Uint("L", 1, "LCI for call request and packet headers")
	method    = flag.String("m", "data", "Probe method: data, moredata, irq, reset, restart")
	cudFlag   = flag.String("C", "", "Call user data: multi|pad|mail|ip or hex bytes (e.g. cc0100)")
)

// cudBytes holds the parsed call user data bytes, populated in main after flag.Parse.
var cudBytes []byte

var cudAliases = map[string]string{
	"multi": "00000000",
	"pad":   "01000000",
	"mail":  "c0f70000",
	"ip":    "cc",
}

func parseCUD(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	if h, ok := cudAliases[strings.ToLower(s)]; ok {
		s = h
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid CUD hex %q: %w", s, err)
	}
	return b, nil
}

// negotiatedGFI is set from the GFI field of the CALL_CONNECTED packet.
// GFI=1 → mod-8 (3-byte data header); GFI=2 → mod-128 (4-byte data header).
var negotiatedGFI byte = xot.GFIMod8

// callFacilities negotiates window=1, packet_size=128 (2^7).
var callFacilities = []byte{
	0x42, 0x07, 0x07, // packet size 128/128
	0x43, 0x01, 0x01, // window size 1/1
}

// gfiByte returns the first byte of an X.25 header: GFI in upper nibble, LCI high in lower.
func gfiByte(lci uint16) byte {
	return (negotiatedGFI << 4) | byte((lci>>8)&0x0F)
}

func buildCallRequest(lci uint16, called, calling string) []byte {
	calledLen := len(called)
	callingLen := len(calling)
	addrLens := byte((callingLen << 4) | (calledLen & 0x0F))

	totalAddrBytes := (calledLen + callingLen + 1) / 2
	addrData := make([]byte, totalAddrBytes)
	nibbleIdx := 0
	writeNibble := func(ch byte) {
		var nib byte
		if ch >= '0' && ch <= '9' {
			nib = ch - '0'
		} else {
			nib = ch - 'a' + 10
		}
		if nibbleIdx%2 == 0 {
			addrData[nibbleIdx/2] = nib << 4
		} else {
			addrData[nibbleIdx/2] |= nib
		}
		nibbleIdx++
	}
	for i := 0; i < len(called); i++ {
		writeNibble(called[i])
	}
	for i := 0; i < len(calling); i++ {
		writeNibble(calling[i])
	}

	payload := []byte{addrLens}
	payload = append(payload, addrData...)
	payload = append(payload, byte(len(callFacilities)))
	payload = append(payload, callFacilities...)
	payload = append(payload, cudBytes...)

	pkt := &xot.X25Packet{
		GFI:     xot.GFIMod128,
		LCI:     lci,
		Type:    xot.PktTypeCallRequest,
		Payload: payload,
	}
	return pkt.Serialize()
}

func createData(lci uint16, pS byte, mbit bool, data []byte) []byte {
	return xot.CreateData(negotiatedGFI, lci, pS, 0, mbit, data)
}

func createResetRequest(lci uint16) []byte {
	return []byte{
		gfiByte(lci),
		byte(lci & 0xFF),
		xot.PktTypeResetRequest,
		xot.CauseDTEOriginated, 0x00,
	}
}

func createRestartRequest() []byte {
	return []byte{
		xot.GFIMod8 << 4, // GFI=1, LCI=0
		0x00,
		xot.PktTypeRestartRequest,
		xot.CauseDTEOriginated, 0x00,
	}
}

func resolveGateway(addr string) string {
	if strings.Contains(addr, ":") {
		return addr
	}
	return addr + ":1998"
}

func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func parseCauseDiag(payload []byte) (cause, diag byte) {
	if len(payload) >= 1 {
		cause = payload[0]
	}
	if len(payload) >= 2 {
		diag = payload[1]
	}
	return
}

// clearCall sends CLEAR_REQUEST and waits briefly for CLEAR_CONFIRM.
func clearCall(conn net.Conn, gfi byte, lci uint16) {
	log.Printf("CLEAR_REQ GFI=%d LCI=%d", gfi, lci)
	pkt := xot.CreateClearRequest(gfi, lci, xot.CauseDTEOriginated, 0x00).Serialize()
	if err := xot.SendXot("xot", conn, pkt); err != nil {
		log.Printf("CLEAR_REQ send failed: %v", err)
		return
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer conn.SetReadDeadline(time.Time{})
	for {
		d, err := xot.ReadXot("xot", conn)
		if err != nil {
			if !isTimeout(err) && !errors.Is(err, io.EOF) {
				log.Printf("CLEAR_REQ read error: %v", err)
			}
			return
		}
		if len(d) >= 3 && xot.GetPacketType(d) == xot.PktTypeClearConfirm {
			log.Printf("CLEAR_CONFIRM LCI=%d", lci)
			return
		}
	}
}

// establish sends CALL_REQUEST and waits for CALL_CONNECTED.
// Returns an error string if the call fails (cause/diag from CLEAR_REQUEST).
func establish(conn net.Conn, lci uint16, deadline time.Duration) error {
	if err := xot.SendXot("xot", conn, buildCallRequest(lci, *destAddr, *localAddr)); err != nil {
		return fmt.Errorf("send CALL_REQ: %w", err)
	}
	conn.SetReadDeadline(time.Now().Add(deadline))
	defer conn.SetReadDeadline(time.Time{})
	for {
		d, err := xot.ReadXot("xot", conn)
		if err != nil {
			return fmt.Errorf("reading after CALL_REQ: %w", err)
		}
		if len(d) < 3 {
			continue
		}
		switch xot.GetPacketType(d) {
		case xot.PktTypeCallConnected:
			negotiatedGFI = xot.GetGFI(d)
			return nil
		case xot.PktTypeClearRequest:
			cause, diag := parseCauseDiag(d[3:])
			return fmt.Errorf("CLEAR_REQUEST: cause=0x%02x diag=%d", cause, diag)
		}
	}
}

func run(conn net.Conn) {
	lci := uint16(*lciFlag)
	connectStart := time.Now()

	var deadline time.Time
	if *runTime > 0 {
		deadline = time.Now().Add(time.Duration(*runTime) * time.Second)
	}

	// All methods need a call in place: goxot drops non-CALL_REQUEST before a
	// call is established.  For restart, the call is cleared after each probe
	// so callEstablished tracks whether we need to re-establish.
	callEstablished := false
	defer func() {
		if callEstablished {
			clearCall(conn, negotiatedGFI, lci)
		}
	}()

	if *method != "restart" {
		log.Printf("CALL_REQ %s -> %s LCI=%d", *localAddr, *destAddr, lci)
		if err := establish(conn, lci, 30*time.Second); err != nil {
			log.Fatalf("call setup failed (%.3fs): %v", time.Since(connectStart).Seconds(), err)
		}
		callEstablished = true
		log.Printf("CALL_CONNECTED LCI=%d GFI=%d (%.3fms setup)",
			lci, negotiatedGFI, float64(time.Since(connectStart).Microseconds())/1000)
	}

	probeNum := 0
	seq := byte(0) // modulo-8 P(S) for data method

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-sigChan:
			fmt.Printf("\nInterrupted after %d probes.\n", probeNum)
			return
		case t := <-ticker.C:
			if !deadline.IsZero() && !t.Before(deadline) {
				fmt.Printf("Completed %d probes.\n", probeNum)
				return
			}

			probeNum++
			elapsed := time.Since(connectStart)

			// restart: re-establish the call if it was cleared by the last probe.
			if *method == "restart" && !callEstablished {
				if err := establish(conn, lci, 5*time.Second); err != nil {
					fmt.Printf("probe %d [RESTART_REQ] FAILED re-establishing call (%.3fs): %v\n",
						probeNum, elapsed.Seconds(), err)
					return
				}
				callEstablished = true
			}

			var probeBytes []byte
			var probeLabel string

			switch *method {
			case "data", "moredata":
				buf := make([]byte, *bufSize)
				for i := range buf {
					buf[i] = 'A'
				}
				mbit := *method == "moredata"
				probeBytes = createData(lci, seq, mbit, buf)
				if mbit {
					probeLabel = fmt.Sprintf("DATA(M,P(S)=%d)", seq)
				} else {
					probeLabel = fmt.Sprintf("DATA(P(S)=%d)", seq)
				}
				seqMask := byte(0x07) // mod-8
				if negotiatedGFI == 2 {
					seqMask = 0x7F // mod-128
				}
				seq = (seq + 1) & seqMask
			case "irq":
				probeBytes = xot.CreateInterrupt(negotiatedGFI, lci, 0x01)
				probeLabel = "INTERRUPT"
			case "reset":
				probeBytes = createResetRequest(lci)
				probeLabel = "RESET_REQ"
			case "restart":
				probeBytes = createRestartRequest()
				probeLabel = "RESTART_REQ"
			}

			if err := xot.SendXot("xot", conn, probeBytes); err != nil {
				fmt.Printf("probe %d [%s] FAILED after %.3fs: send: %v\n",
					probeNum, probeLabel, elapsed.Seconds(), err)
				return
			}

			// Read until we receive the expected response.
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			rtt, err := waitForResponse(conn, *method, probeLabel, lci)
			conn.SetReadDeadline(time.Time{})

			if err != nil {
				elapsed = time.Since(connectStart)
				fmt.Printf("probe %d [%s] FAILED after %.3fs: %v\n",
					probeNum, probeLabel, elapsed.Seconds(), err)
				return
			}

			fmt.Printf("probe %d: %s rtt=%dµs\n", probeNum, probeLabel, rtt.Microseconds())

			if *method == "restart" {
				// RESTART cleared our circuit; drain any CLEAR_REQUEST for the
				// call LCI that the server may send after RESTART_CONFIRM.
				conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				drainClear(conn, lci)
				conn.SetReadDeadline(time.Time{})
				callEstablished = false
			}
		}
	}
}

// waitForResponse reads packets until it sees the response expected for the
// given method.  Returns the RTT from when the probe was sent (probeTime is
// taken by the caller; we receive the matched packet here).
func waitForResponse(conn net.Conn, method, probeLabel string, lci uint16) (_ time.Duration, err error) {
	// We need the probe send time captured before reading; the caller already
	// sent the probe and set a read deadline, so we measure from now.
	t0 := time.Now()
	for {
		d, rerr := xot.ReadXot("xot", conn)
		if rerr != nil {
			if isTimeout(rerr) {
				return 0, fmt.Errorf("timeout waiting for response to %s", probeLabel)
			}
			if errors.Is(rerr, io.EOF) {
				return 0, fmt.Errorf("link closed after %s", probeLabel)
			}
			return 0, fmt.Errorf("read error after %s: %w", probeLabel, rerr)
		}
		if len(d) < 3 {
			continue
		}
		pktType := xot.GetPacketType(d)

		switch method {
		case "data", "moredata":
			// RR: lower nibble == 0x01, any P(R) value in upper nibble.
			if (d[2] & 0x0F) == xot.PktTypeRR {
				return time.Since(t0), nil
			}
			if pktType == xot.PktTypeClearRequest {
				cause, diag := parseCauseDiag(d[3:])
				return 0, fmt.Errorf("CLEAR_REQUEST: cause=0x%02x diag=%d", cause, diag)
			}
		case "irq":
			if pktType == xot.PktTypeInterruptConfirm {
				return time.Since(t0), nil
			}
			if pktType == xot.PktTypeClearRequest {
				cause, diag := parseCauseDiag(d[3:])
				return 0, fmt.Errorf("CLEAR_REQUEST: cause=0x%02x diag=%d", cause, diag)
			}
		case "reset":
			if pktType == xot.PktTypeResetConfirm {
				return time.Since(t0), nil
			}
			if pktType == xot.PktTypeClearRequest {
				cause, diag := parseCauseDiag(d[3:])
				return 0, fmt.Errorf("CLEAR_REQUEST: cause=0x%02x diag=%d", cause, diag)
			}
		case "restart":
			// CLEAR_REQUEST for our call is normal during a restart; skip it.
			if pktType == xot.PktTypeRestartConfirm {
				return time.Since(t0), nil
			}
		}
	}
}

// drainClear reads and discards pending CLEAR_REQUEST/CLEAR_CONFIRM packets
// for the given LCI, stopping at the read deadline.
func drainClear(conn net.Conn, lci uint16) {
	for {
		d, err := xot.ReadXot("xot", conn)
		if err != nil {
			return
		}
		if len(d) < 3 {
			continue
		}
		t := xot.GetPacketType(d)
		if t == xot.PktTypeClearRequest || t == xot.PktTypeClearConfirm {
			continue
		}
		// Unexpected packet — stop draining.
		return
	}
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options]\n\nOptions:\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Methods:
  data     send DATA packet, time the RR response
  moredata send DATA packet with M bit set, time the RR response
  irq      send INTERRUPT packet, time the INTERRUPT_CONFIRM response
  reset    send RESET_REQ packet, time the RESET_CONFIRM response
  restart  send RESTART_REQ (LCI=0), time the RESTART_CONFIRM response
           (call is re-established between probes as restart clears the circuit)

Call user data aliases (-C):
  multi    00000000
  pad      01000000
  mail     c0f70000
  ip       cc
`)
	}
	flag.Parse()

	var err error
	if cudBytes, err = parseCUD(*cudFlag); err != nil {
		log.Fatalf("%v", err)
	}

	switch *method {
	case "data", "moredata", "irq", "reset", "restart":
	default:
		log.Fatalf("unknown method %q; choose data, moredata, irq, reset, or restart", *method)
	}
	if *destAddr == "" {
		log.Fatalf("-d destination address is required")
	}

	gw := resolveGateway(*gateway)
	if len(cudBytes) > 0 {
		log.Printf("x25_ping: method=%s gateway=%s LCI=%d %s->%s CUD=%X",
			*method, gw, *lciFlag, *localAddr, *destAddr, cudBytes)
	} else {
		log.Printf("x25_ping: method=%s gateway=%s LCI=%d %s->%s",
			*method, gw, *lciFlag, *localAddr, *destAddr)
	}

	conn, err := net.DialTimeout("tcp", gw, 10*time.Second)
	if err != nil {
		log.Fatalf("connect to %s: %v", gw, err)
	}
	defer conn.Close()
	xot.SetNoDelay(conn)
	log.Printf("TCP connected to %s", gw)

	run(conn)
}
