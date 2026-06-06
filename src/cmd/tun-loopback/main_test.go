package main

import (
	"os"
	"os/exec"
	"testing"

	xot "github.com/SeanBurford/goxot"
	"github.com/SeanBurford/goxot/tun"
)

func TestMain(t *testing.T) {
	if os.Getenv("BE_MAIN") == "1" {
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "--help")
	cmd.Env = append(os.Environ(), "BE_MAIN=1")
	err := cmd.Run()
	if err != nil {
		t.Fatalf("process ran with err %v, want exit status 0", err)
	}
}

// --- sessionManager ---

func TestSessionManagerGetReturnsNilForEmpty(t *testing.T) {
	sm := newSessionManager(2)
	if sm.get(0, 1) != nil {
		t.Fatal("expected nil for unset slot on node 0")
	}
	if sm.get(1, 100) != nil {
		t.Fatal("expected nil for unset slot on node 1")
	}
}

func TestSessionManagerAddAndGet(t *testing.T) {
	sm := newSessionManager(2)
	s := &loopSession{GFI: xot.GFIMod8, tunA: 0, lciA: 10, tunB: 1, lciB: 20}
	sm.mu.Lock()
	sm.add(s)
	sm.mu.Unlock()

	if sm.get(0, 10) != s {
		t.Error("A-side lookup did not return the session")
	}
	if sm.get(1, 20) != s {
		t.Error("B-side lookup did not return the session")
	}
	// Slots not reserved by s must still be nil.
	if sm.get(0, 20) != nil {
		t.Error("expected nil for unreserved slot (0, 20)")
	}
	if sm.get(1, 10) != nil {
		t.Error("expected nil for unreserved slot (1, 10)")
	}
}

func TestSessionManagerRemoveClearsBothSides(t *testing.T) {
	sm := newSessionManager(2)
	s := &loopSession{GFI: xot.GFIMod8, tunA: 0, lciA: 3, tunB: 1, lciB: 4}
	sm.mu.Lock()
	sm.add(s)
	sm.remove(s)
	sm.mu.Unlock()

	if sm.get(0, 3) != nil {
		t.Error("A-side slot not nil after remove")
	}
	if sm.get(1, 4) != nil {
		t.Error("B-side slot not nil after remove")
	}
}

func TestSessionManagerRemoveAllForNode(t *testing.T) {
	sm := newSessionManager(3)
	// s1 and s3 involve node 0; s2 does not.
	s1 := &loopSession{GFI: xot.GFIMod8, tunA: 0, lciA: 1, tunB: 1, lciB: 2}
	s2 := &loopSession{GFI: xot.GFIMod8, tunA: 1, lciA: 3, tunB: 2, lciB: 4}
	s3 := &loopSession{GFI: xot.GFIMod8, tunA: 0, lciA: 5, tunB: 2, lciB: 6}
	sm.mu.Lock()
	sm.add(s1)
	sm.add(s2)
	sm.add(s3)
	sm.mu.Unlock()

	dead := sm.removeAllForNode(0)
	if len(dead) != 2 {
		t.Fatalf("expected 2 sessions removed for node 0, got %d", len(dead))
	}
	// Both sides of s1 and s3 must be nil after the teardown.
	if sm.get(0, 1) != nil || sm.get(1, 2) != nil {
		t.Error("s1 not fully cleared from both sides")
	}
	if sm.get(0, 5) != nil || sm.get(2, 6) != nil {
		t.Error("s3 not fully cleared from both sides")
	}
	// s2 does not touch node 0 and must survive.
	if sm.get(1, 3) != s2 || sm.get(2, 4) != s2 {
		t.Error("s2 should not be removed by removeAllForNode(0)")
	}
}

func TestSessionManagerIsBsideLCIUsed(t *testing.T) {
	sm := newSessionManager(2)
	s := &loopSession{GFI: xot.GFIMod8, tunA: 0, lciA: 7, tunB: 1, lciB: 8}
	sm.mu.Lock()
	sm.add(s)

	if !sm.isBsideLCIUsed(1, 8) {
		t.Error("expected B-side LCI 8 to be marked used after add")
	}
	if sm.isBsideLCIUsed(1, 9) {
		t.Error("expected B-side LCI 9 to be unused")
	}

	sm.remove(s)
	if sm.isBsideLCIUsed(1, 8) {
		t.Error("expected B-side LCI 8 to be released after remove")
	}
	sm.mu.Unlock()
}

// --- findNode ---

func TestFindNodeReturnsNilForNoMatch(t *testing.T) {
	r := &relay{
		nodes: []*tunNode{{name: "tunlb0", address: "1234", idx: 0}},
		sm:    newSessionManager(1),
	}
	if r.findNode("9999") != nil {
		t.Error("findNode should return nil for an unroutable address")
	}
}

func TestFindNodeEmptyRelay(t *testing.T) {
	r := &relay{nodes: []*tunNode{}, sm: newSessionManager(0)}
	if r.findNode("1234") != nil {
		t.Error("findNode on empty relay should return nil")
	}
}

func TestFindNodeExactMatch(t *testing.T) {
	n := &tunNode{name: "tunlb0", address: "1234", idx: 0}
	r := &relay{nodes: []*tunNode{n}, sm: newSessionManager(1)}
	if got := r.findNode("1234"); got != n {
		t.Errorf("expected exact match node, got %v", got)
	}
}

func TestFindNodeLongestPrefixMatch(t *testing.T) {
	n0 := &tunNode{name: "tunlb0", address: "12", idx: 0}
	n1 := &tunNode{name: "tunlb1", address: "123", idx: 1}
	r := &relay{nodes: []*tunNode{n0, n1}, sm: newSessionManager(2)}

	// "12345" has both "12" and "123" as prefixes; longest wins.
	if got := r.findNode("12345"); got != n1 {
		t.Errorf("expected longest-prefix node (tunlb1), got %v", got)
	}
	// "12999" only has "12" as a prefix.
	if got := r.findNode("12999"); got != n0 {
		t.Errorf("expected shorter-prefix node (tunlb0), got %v", got)
	}
}

// --- forwardPacket nil-session paths ---
//
// When forwardPacket finds no session it may call sendClear, which writes to
// src.ifce.  The tests below cover the three paths where sendClear is skipped,
// so src.ifce may safely be nil — a nil dereference would otherwise occur.

func newTestRelay(numNodes int) (*relay, []*tunNode) {
	nodes := make([]*tunNode, numNodes)
	for i := range nodes {
		nodes[i] = &tunNode{
			name: "tunlb0",
			idx:  i,
			wbuf: make([]byte, tun.MaxPacketSize),
			// ifce intentionally nil: valid only for code paths that do not write.
		}
	}
	return &relay{nodes: nodes, sm: newSessionManager(numNodes)}, nodes
}

func TestForwardPacketNilSessionClearRequest(t *testing.T) {
	// CLEAR_REQUEST with no session: the sendClear guard is skipped, so src.ifce
	// (nil) is never dereferenced.
	r, nodes := newTestRelay(1)
	payload := []byte{0x10, 0x01, xot.PktTypeClearRequest}
	r.forwardPacket(nodes[0], 1, payload, xot.PktTypeClearRequest)
}

func TestForwardPacketNilSessionClearConfirm(t *testing.T) {
	// CLEAR_CONFIRM with no session follows the same safe guard.
	r, nodes := newTestRelay(1)
	payload := []byte{0x10, 0x01, xot.PktTypeClearConfirm}
	r.forwardPacket(nodes[0], 1, payload, xot.PktTypeClearConfirm)
}

func TestForwardPacketNilSessionLCIZero(t *testing.T) {
	// LCI 0 with no session: sendClear is skipped because the guard checks lci != 0.
	r, nodes := newTestRelay(1)
	payload := []byte{0x00, 0x00, xot.PktTypeCallConnected}
	r.forwardPacket(nodes[0], 0, payload, xot.PktTypeCallConnected)
}
