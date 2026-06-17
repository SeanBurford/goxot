// tun-loopback implements a multi-TUN userland local-to-local X.25 loopback relay.
//
// Each address in the "routes" config list gets its own ARPHRD_X25 TUN interface.
// The relay copies X.25 frames between TUNs so that an AF_X25 socket calling
// connect() to one address can reach a listening socket bound to another address,
// without kernel changes. See docs/tech/linux_x25_routing.md for the design rationale.
//
// Requires root (CAP_NET_ADMIN) to create TUN devices and manage X.25 routes.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	xot "github.com/SeanBurford/goxot/src"
	"github.com/SeanBurford/goxot/src/tun"
)

var (
	configPath = flag.String("config", "config.json", "Path to config file")
	tunPrefix  = flag.String("tun-prefix", "tunlb", "Prefix for TUN interface names (e.g. tunlb → tunlb0, tunlb1, ...)")
	trace      = flag.Bool("trace", false, "Enable packet trace logging")
	statsPort  = flag.String("stats-port", "", "Address for /varz stats (port or host:port; empty to disable)")
)

// Link state values (atomic int32).
const (
	linkDown        = int32(0)
	linkConnecting  = int32(1)
	linkOperational = int32(3)
)

// tunNode represents one TUN interface in the loopback relay.
type tunNode struct {
	ifce    *tun.Interface
	name    string // e.g. "tunlb0"
	address string // X.121 address whose route points here
	idx     int    // index into relay.nodes

	linkState int32 // atomic

	wmu  sync.Mutex // serialises writes to ifce
	wbuf []byte     // pre-allocated write buffer (len=tun.MaxPacketSize), guarded by wmu

	// Relay-LCI allocator for this node as the B-side (destination) of a forwarded call.
	lciMu    sync.Mutex
	nextLCI  uint16
	lciStart uint16
	lciEnd   uint16
}

// allocRelayLCI returns an unused relay LCI for this node (used when forwarding a
// CALL_REQ to this node as destination). The caller holds sm.mu for reading so that
// the "in use" check is consistent.
func (n *tunNode) allocRelayLCI(inUse func(uint16) bool) (uint16, error) {
	n.lciMu.Lock()
	defer n.lciMu.Unlock()

	rangeSize := n.lciEnd - n.lciStart + 1
	for i := uint16(0); i < rangeSize; i++ {
		lci := n.lciStart + (n.nextLCI-n.lciStart+i)%rangeSize
		if !inUse(lci) {
			n.nextLCI = n.lciStart + (lci-n.lciStart+1)%rangeSize
			return lci, nil
		}
	}
	return 0, fmt.Errorf("relay LCI exhausted on %s (range %d-%d)", n.name, n.lciStart, n.lciEnd)
}

// writeFrame writes a PI-framed X.25 packet to this node's TUN, using the
// pre-allocated write buffer to avoid hot-path allocation.
func (n *tunNode) writeFrame(header byte, data []byte) error {
	n.wmu.Lock()
	defer n.wmu.Unlock()
	return tun.WriteFrameBuf(n.ifce, n.name, header, data, n.wbuf)
}

// loopSession records a forwarded X.25 call between two TUN nodes.
type loopSession struct {
	GFI  byte   // GFI for the session
	tunA int    // source node index
	lciA uint16 // LCI on tunA (kernel-assigned, from CALL_REQ)
	tunB int    // destination node index
	lciB uint16 // LCI on tunB (relay-assigned, in forwarded packet)
}

// sessionManager tracks active loopback sessions.
//
// slots[nodeIdx][lci] maps directly to the session for that (node, LCI) pair.
// Both the A-side key (kernel LCI on tunA) and the B-side key (relay LCI on tunB)
// point to the same *loopSession. Reads are lock-free atomic loads; writes are
// serialised by mu. Using direct array indexing instead of a map eliminates hash
// overhead and allows the data-packet hot path to avoid any mutex.
type sessionManager struct {
	mu      sync.Mutex
	slots   [][]atomic.Pointer[loopSession] // [nodeIdx][lci]; LCI 0..xot.LCIMax
	usedLCI []map[uint16]bool               // B-side LCI reservations; guarded by mu
}

func newSessionManager(numNodes int) *sessionManager {
	slots := make([][]atomic.Pointer[loopSession], numNodes)
	used := make([]map[uint16]bool, numNodes)
	for i := range slots {
		slots[i] = make([]atomic.Pointer[loopSession], xot.LCIMax+1)
		used[i] = make(map[uint16]bool)
	}
	return &sessionManager{slots: slots, usedLCI: used}
}

// get returns the session for (nodeIdx, lci) with a single atomic load — no lock.
func (sm *sessionManager) get(nodeIdx int, lci uint16) *loopSession {
	return sm.slots[nodeIdx][lci].Load()
}

// add records s under both its A-side and B-side LCI slots. Must hold mu.
func (sm *sessionManager) add(s *loopSession) {
	sm.slots[s.tunA][s.lciA].Store(s)
	sm.slots[s.tunB][s.lciB].Store(s)
	sm.usedLCI[s.tunB][s.lciB] = true
}

// remove clears both LCI slots for s and releases the B-side LCI. Must hold mu.
func (sm *sessionManager) remove(s *loopSession) {
	sm.slots[s.tunA][s.lciA].Store(nil)
	sm.slots[s.tunB][s.lciB].Store(nil)
	delete(sm.usedLCI[s.tunB], s.lciB)
}

// isBsideLCIUsed reports whether lci is reserved on the B side of nodeIdx. Must hold mu.
func (sm *sessionManager) isBsideLCIUsed(nodeIdx int, lci uint16) bool {
	return sm.usedLCI[nodeIdx][lci]
}

// removeAllForNode removes all sessions involving nodeIdx (A or B side) and returns them.
func (sm *sessionManager) removeAllForNode(nodeIdx int) []*loopSession {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	seen := make(map[*loopSession]bool)
	for _, nodeSlots := range sm.slots {
		for i := range nodeSlots {
			if s := nodeSlots[i].Load(); s != nil && (s.tunA == nodeIdx || s.tunB == nodeIdx) {
				seen[s] = true
			}
		}
	}
	dead := make([]*loopSession, 0, len(seen))
	for s := range seen {
		dead = append(dead, s)
	}
	for _, s := range dead {
		sm.remove(s)
	}
	return dead
}

// relay is the top-level loopback relay.
type relay struct {
	nodes []*tunNode
	sm    *sessionManager
}

func main() {
	flag.Parse()

	cm, err := xot.NewConfigManager(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	cfg := cm.GetTunLoopbackConfig()

	if len(cfg.Routes) == 0 {
		log.Fatal("tun-loopback: no routes configured in tun-loopback.routes")
	}

	statsAddr := *statsPort
	if statsAddr == "" {
		statsAddr = string(cfg.StatsPort)
	}
	if statsAddr != "" {
		xot.StartStatsServer(statsAddr)
	}

	lciStart := uint16(cfg.LciStart)
	lciEnd := uint16(cfg.LciEnd)

	nodes := make([]*tunNode, 0, len(cfg.Routes))
	for i, addr := range cfg.Routes {
		name := fmt.Sprintf("%s%d", *tunPrefix, i)
		ifce, err := tun.Setup(name)
		if err != nil {
			log.Fatalf("Failed to setup TUN %s: %v", name, err)
		}
		if err := tun.SetSubscription(name, cfg.Modulo); err != nil {
			log.Printf("Warning: SetSubscription %s: %v", name, err)
		}
		// Route for this address points to this TUN.
		if err := tun.AddRoute(name, addr, len(addr)); err != nil {
			log.Printf("Warning: AddRoute %s → %s/%d: %v", name, addr, len(addr), err)
		}
		log.Printf("tun-loopback: %s → address %s (LCI range %d-%d)", name, addr, lciStart, lciEnd)
		nodes = append(nodes, &tunNode{
			ifce:     ifce,
			name:     name,
			address:  addr,
			idx:      i,
			linkState: linkDown,
			wbuf:     make([]byte, tun.MaxPacketSize),
			nextLCI:  lciStart,
			lciStart: lciStart,
			lciEnd:   lciEnd,
		})
	}

	r := &relay{
		nodes: nodes,
		sm:    newSessionManager(len(nodes)),
	}

	// Proactively send Connect on every TUN (COMPAT003).
	for _, n := range nodes {
		tun.WriteFrame(n.ifce, n.name, tun.HeaderConnect, nil)
		atomic.StoreInt32(&n.linkState, linkConnecting)
	}

	// Start one reader goroutine per TUN.
	for _, n := range nodes {
		n := n
		go func() {
			xot.ThreadsActive.Add("tun_read_handler", 1)
			defer xot.ThreadsActive.Add("tun_read_handler", -1)
			r.handleTunRead(n)
		}()
	}

	// Signal handler: clean up all sessions and TUN interfaces.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Printf("tun-loopback: shutting down")
		for _, n := range nodes {
			tun.AddRoute(n.name, n.address, len(n.address)) // best-effort cleanup, ignore error
			tun.DeleteRoute(n.name, n.address, len(n.address))
			tun.WriteFrame(n.ifce, n.name, tun.HeaderDisconnect, nil)
			n.ifce.Close()
		}
		os.Exit(0)
	}()

	// Block forever; reader goroutines run the relay.
	select {}
}

// handleTunRead is the per-TUN reader goroutine. It manages the L2 handshake
// and dispatches X.25 packets to the relay logic.
func (r *relay) handleTunRead(n *tunNode) {
	src := n.name
	buf := make([]byte, tun.MaxPacketSize)

	for {
		hdr, payload, err := tun.ReadFrame(n.ifce, n.name, buf)
		if err != nil {
			if errors.Is(err, io.EOF) ||
				strings.Contains(err.Error(), "closed") ||
				strings.Contains(err.Error(), "bad file descriptor") {
				log.Printf("%s: TUN closed, exiting reader", src)
				return
			}
			log.Printf("%s: read error: %v", src, err)
			return
		}

		// Control frames (empty payload).
		if len(payload) == 0 {
			switch hdr {
			case tun.HeaderConnect:
				if atomic.LoadInt32(&n.linkState) != linkOperational {
					if *trace {
						log.Printf("%s< Connect echo", src)
					}
					n.writeFrame(tun.HeaderConnect, nil)
					atomic.CompareAndSwapInt32(&n.linkState, linkDown, linkConnecting)
					xot.InterfaceSessionsOpened.Add(n.name, 1)
				}
			case tun.HeaderDisconnect:
				log.Printf("%s: kernel disconnect — clearing sessions", src)
				atomic.StoreInt32(&n.linkState, linkDown)
				r.clearAllForNode(n)
				xot.InterfaceSessionsClosed.Add(n.name, 1)
			}
			continue
		}

		pktType := xot.GetPacketType(payload)

		// RESTART handshake.
		if pktType == xot.PktTypeRestartRequest {
			r.handleRestart(n, payload)
			continue
		}

		if atomic.LoadInt32(&n.linkState) != linkOperational {
			if *trace {
				log.Printf("%s: dropping packet — link not operational", src)
			}
			continue
		}

		gfi := xot.GetGFI(payload)
		lci := xot.GetLCI(payload)

		if pktType == xot.PktTypeCallRequest {
			// RACE-A: payload aliases buf; copy before any goroutine or async op.
			pktData := make([]byte, len(payload))
			copy(pktData, payload)
			r.handleCallRequest(n, gfi, lci, pktData)
			continue
		}

		r.forwardPacket(n, lci, payload, pktType)
	}
}

// handleRestart processes a RESTART_REQUEST from the kernel, sends RESTART_CONF,
// and transitions the link to operational. Active sessions on this node are cleared
// if the link was already operational (COMPAT005).
func (r *relay) handleRestart(n *tunNode, payload []byte) {
	src := n.name
	if atomic.LoadInt32(&n.linkState) == linkOperational {
		log.Printf("%s: RESTART_REQ in operational state — clearing sessions", src)
		r.clearAllForNode(n)
	}

	conf := []byte{payload[0] & 0xF0, 0x00, xot.PktTypeRestartConfirm}
	n.writeFrame(tun.HeaderData, conf)

	if atomic.CompareAndSwapInt32(&n.linkState, linkConnecting, linkOperational) {
		log.Printf("%s: link operational", src)
	} else {
		atomic.StoreInt32(&n.linkState, linkOperational)
	}
}

// handleCallRequest processes an incoming CALL_REQUEST on node src with the given
// LCI. It resolves the destination node by X.121 address, allocates a relay LCI on
// the destination, records the session, and forwards the packet.
func (r *relay) handleCallRequest(src *tunNode, srcGFI byte, srcLCI uint16, payload []byte) {
	pkt, err := xot.ParseX25(payload)
	if err != nil {
		log.Printf("%s: CALL_REQ parse error: %v", src.name, err)
		r.sendClear(src, srcGFI, srcLCI, xot.CauseLocalProcedureError, 0)
		return
	}

	called, calling, _, _, err := pkt.ParseCallRequest()
	if err != nil {
		log.Printf("%s: CALL_REQ address parse error: %v", src.name, err)
		r.sendClear(src, srcGFI, srcLCI, xot.CauseLocalProcedureError, 0)
		return
	}

	dst := r.findNode(called)
	if dst == nil {
		log.Printf("%s: no route for called address %s (calling %s)", src.name, called, calling)
		r.sendClear(src, srcGFI, srcLCI, xot.CauseNetworkCongestion, 0)
		return
	}

	if atomic.LoadInt32(&dst.linkState) != linkOperational {
		log.Printf("%s: destination %s link not operational", src.name, dst.name)
		r.sendClear(src, srcGFI, srcLCI, xot.CauseNetworkCongestion, 0)
		return
	}

	// Allocate a relay LCI on the destination TUN. We check usedLCI under the
	// write lock to guarantee the allocated LCI is not already taken.
	r.sm.mu.Lock()
	// Clean up any stale session on this (src, srcLCI) before creating a new one.
	if stale := r.sm.get(src.idx, srcLCI); stale != nil {
		r.sm.remove(stale)
	}
	dstLCI, err := dst.allocRelayLCI(func(lci uint16) bool {
		return r.sm.isBsideLCIUsed(dst.idx, lci)
	})
	if err != nil {
		r.sm.mu.Unlock()
		log.Printf("%s: %v", src.name, err)
		r.sendClear(src, srcGFI, srcLCI, xot.CauseNetworkCongestion, 0)
		return
	}
	s := &loopSession{GFI: xot.NegotiateGFI(srcGFI), tunA: src.idx, lciA: srcLCI, tunB: dst.idx, lciB: dstLCI}
	r.sm.add(s)
	r.sm.mu.Unlock()

	if *trace {
		log.Printf("%s(%d)→%s(%d) CALL_REQ calling=%s called=%s GFI=%d",
			src.name, srcLCI, dst.name, dstLCI, calling, called, srcGFI)
	}
	xot.InterfaceCallRequest.Add(src.name, 1)

	// Remap LCI in payload and forward to destination TUN.
	payload[0] = (srcGFI << 4) | byte((dstLCI>>8)&0x0F)
	payload[1] = byte(dstLCI & 0xFF)
	if err := dst.writeFrame(tun.HeaderData, payload); err != nil {
		log.Printf("%s: write CALL_REQ to %s: %v", src.name, dst.name, err)
		r.sm.mu.Lock()
		r.sm.remove(s)
		r.sm.mu.Unlock()
		r.sendClear(src, srcGFI, srcLCI, xot.CauseNetworkCongestion, 0)
	}
}

// forwardPacket relays a non-CALL_REQ packet from src TUN with the given LCI to
// the peer TUN, remapping the LCI. The hot path (data packets) is a single atomic
// load + two byte writes + a TUN write with no mutex.
func (r *relay) forwardPacket(src *tunNode, lci uint16, payload []byte, pktType byte) {
	s := r.sm.get(src.idx, lci)

	if s == nil {
		// RACE-B: unknown LCI — send CLEAR to prevent kernel socket from lingering.
		if pktType != xot.PktTypeClearRequest && pktType != xot.PktTypeClearConfirm && lci != 0 {
			if *trace {
				log.Printf("%s: no session for LCI %d, sending CLEAR", src.name, lci)
			}
			r.sendClear(src, xot.GetGFI(payload), lci, xot.CauseNetworkCongestion, 0)
		}
		return
	}

	// Determine peer side.
	var peerNode *tunNode
	var peerLCI uint16
	if s.tunA == src.idx && s.lciA == lci {
		peerNode = r.nodes[s.tunB]
		peerLCI = s.lciB
	} else {
		peerNode = r.nodes[s.tunA]
		peerLCI = s.lciA
	}

	// Remap LCI in-place (payload aliases buf so we work in-place; the write
	// completes before the next ReadFrame call overwrites buf).
	payload[0] = (payload[0] & 0xF0) | byte((peerLCI>>8)&0x0F)
	payload[1] = byte(peerLCI & 0xFF)

	if *trace {
		log.Printf("%s(%d)→%s(%d) %s", src.name, lci, peerNode.name, peerLCI,
			xot.GetPacketTypeName(pktType))
	}

	// Data packets (bit 0 = 0) dominate in steady state — skip the switch entirely.
	if (pktType & 0x01) == 0 {
		peerNode.writeFrame(tun.HeaderData, payload)
		return
	}

	switch pktType {
	case xot.PktTypeCallConnected:
		xot.InterfaceCallConnected.Add(src.name, 1)
	case xot.PktTypeClearRequest:
		xot.InterfaceClearRequest.Add(src.name, 1)
		// Keep session alive: peer kernel auto-generates CLR_CONF in response,
		// and we need the session to forward that CLR_CONF back to src.
		peerNode.writeFrame(tun.HeaderData, payload)
		return
	case xot.PktTypeClearConfirm:
		xot.InterfaceClearConfirm.Add(src.name, 1)
		r.sm.mu.Lock()
		if r.sm.get(src.idx, lci) == s { // ABA: only remove if not already replaced
			r.sm.remove(s)
		}
		r.sm.mu.Unlock()
		peerNode.writeFrame(tun.HeaderData, payload)
		return
	}

	peerNode.writeFrame(tun.HeaderData, payload)
}

// clearAllForNode tears down all sessions involving node n, sending CLEAR_REQ to
// the peer TUN for each active call.
func (r *relay) clearAllForNode(n *tunNode) {
	dead := r.sm.removeAllForNode(n.idx)
	for _, s := range dead {
		// Send CLEAR to whichever side is *not* n.
		if s.tunA == n.idx {
			peer := r.nodes[s.tunB]
			r.sendClear(peer, s.GFI, s.lciB, xot.CauseNetworkCongestion, 0)
		} else {
			peer := r.nodes[s.tunA]
			r.sendClear(peer, s.GFI, s.lciA, xot.CauseNetworkCongestion, 0)
		}
	}
	log.Printf("%s: cleared %d sessions", n.name, len(dead))
}

// sendClear writes a CLEAR_REQUEST to node n for the given LCI.
func (r *relay) sendClear(n *tunNode, gfi byte, lci uint16, cause, diag byte) {
	clr := xot.CreateClearRequest(gfi, lci, cause, diag)
	n.writeFrame(tun.HeaderData, clr.Serialize())
}

// findNode returns the tunNode whose address is a prefix of called, or nil.
func (r *relay) findNode(called string) *tunNode {
	var best *tunNode
	bestLen := -1
	for _, n := range r.nodes {
		if strings.HasPrefix(called, n.address) && len(n.address) > bestLen {
			best = n
			bestLen = len(n.address)
		}
	}
	return best
}
