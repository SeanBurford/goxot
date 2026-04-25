package xot

import (
	"net"
	"testing"
)

func TestSessionManager(t *testing.T) {
	sm := NewSessionManager(1, 10)

	lci, err := sm.AllocateTunLCI()
	if err != nil || lci != 1 {
		t.Errorf("Expected LCI 1, got %d (err: %v)", lci, err)
	}
	s1 := &Session{LciA: lci, LciB: 100, State: StateP1}
	sm.AddSession(s1)

	lci2, _ := sm.AllocateTunLCI()
	if lci2 != 2 {
		t.Errorf("Expected LCI 2, got %d", lci2)
	}
	s2 := &Session{LciA: lci2, LciB: 101, State: StateP1}
	sm.AddSession(s2)

	if sm.GetByALCI(lci) != s1 {
		t.Errorf("Failed to get session by A LCI")
	}

	sm.RemoveSession(s1)
	lci3, _ := sm.AllocateTunLCI()
	if lci3 != 1 {
		t.Errorf("Expected LCI 1 after release/remove, got %d", lci3)
	}
}

func TestAddRemoveSession(t *testing.T) {
	sm := NewSessionManager(1, 4095)
	conn := &net.TCPConn{}
	s := &Session{
		LciA:  10,
		LciB:  20,
		ConnB: conn,
	}

	sm.AddSession(s)
	if sm.GetByALCI(10) != s {
		t.Errorf("Failed to get session by side A LCI")
	}
	if sm.GetByBConnLCI(conn, 20) != s {
		t.Errorf("Failed to get session by connection and side B LCI")
	}

	sm.RemoveSession(s)
	if sm.GetByALCI(10) != nil {
		t.Errorf("Session still exists after removal")
	}
}

func TestRemoveAllSessions(t *testing.T) {
	sm := NewSessionManager(1, 10)
	s1 := &Session{LciA: 1, LciB: 101}
	s2 := &Session{LciA: 2, LciB: 102}
	sm.AddSession(s1)
	sm.AddSession(s2)

	sessions := sm.RemoveAllSessions()
	if len(sessions) != 2 {
		t.Errorf("Expected 2 sessions, got %d", len(sessions))
	}
	if sm.GetByALCI(1) != nil || sm.GetByALCI(2) != nil {
		t.Errorf("Sessions still exist in manager after RemoveAllSessions")
	}
}

func TestSetState(t *testing.T) {
	s := &Session{State: StateP1}

	s.SetState(StateP2)
	if s.State != StateP2 {
		t.Errorf("Expected StateP2, got %q", s.State)
	}

	// Idempotent
	s.SetState(StateP2)
	if s.State != StateP2 {
		t.Errorf("Expected StateP2 (idempotent), got %q", s.State)
	}

	s.SetState(StateP5)
	if s.State != StateP5 {
		t.Errorf("Expected StateP5, got %q", s.State)
	}
}

func TestGetSessionsForConn(t *testing.T) {
	sm := NewSessionManager(1, 100)

	conn1a, conn1b := net.Pipe()
	defer conn1a.Close()
	defer conn1b.Close()
	conn2a, conn2b := net.Pipe()
	defer conn2a.Close()
	defer conn2b.Close()

	// Use conn1b as ConnB for 3 sessions with different LciB
	for _, lci := range []uint16{1, 2, 3} {
		s := &Session{LciA: lci, LciB: lci + 100, ConnB: conn1b}
		sm.AddSession(s)
	}

	// Use conn2b as ConnB for 1 session
	s4 := &Session{LciA: 10, LciB: 10, ConnB: conn2b}
	sm.AddSession(s4)

	got1 := sm.GetSessionsForConn(conn1b)
	if len(got1) != 3 {
		t.Errorf("Expected 3 sessions for conn1, got %d", len(got1))
	}

	got2 := sm.GetSessionsForConn(conn2b)
	if len(got2) != 1 {
		t.Errorf("Expected 1 session for conn2, got %d", len(got2))
	}

	// nil conn → empty slice
	gotNil := sm.GetSessionsForConn(nil)
	if len(gotNil) != 0 {
		t.Errorf("Expected 0 sessions for nil conn, got %d", len(gotNil))
	}
}

func TestGetAllSessions(t *testing.T) {
	sm := NewSessionManager(1, 100)

	var conns []net.Conn
	for i := 0; i < 5; i++ {
		a, b := net.Pipe()
		conns = append(conns, a, b)
		s := &Session{LciA: uint16(i + 1), LciB: uint16(i + 100), ConnB: b}
		sm.AddSession(s)
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()

	all := sm.GetAllSessions()
	if len(all) != 5 {
		t.Errorf("Expected 5 sessions, got %d", len(all))
	}

	// Remove one and check
	sm.RemoveSession(all[0])
	remaining := sm.GetAllSessions()
	if len(remaining) != 4 {
		t.Errorf("Expected 4 sessions after remove, got %d", len(remaining))
	}
}

func TestAllocateTunLCIExhausted(t *testing.T) {
	sm := NewSessionManager(1, 3)

	// Allocate 3 LCIs successfully
	for i := uint16(1); i <= 3; i++ {
		lci, err := sm.AllocateTunLCI()
		if err != nil {
			t.Fatalf("Unexpected error allocating LCI %d: %v", i, err)
		}
		if lci != i {
			t.Errorf("Expected LCI %d, got %d", i, lci)
		}
		s := &Session{LciA: lci, LciB: lci + 100}
		sm.AddSession(s)
	}

	// 4th allocation should fail
	lci, err := sm.AllocateTunLCI()
	if err == nil {
		t.Errorf("Expected error on exhausted LCI range, got lci=%d", lci)
	}
	if lci != 0 {
		t.Errorf("Expected lci=0 on error, got %d", lci)
	}
}

func TestAllocateTunLCIReusesAfterRemove(t *testing.T) {
	sm := NewSessionManager(1, 2)

	lci1, _ := sm.AllocateTunLCI()
	s1 := &Session{LciA: lci1, LciB: 101}
	sm.AddSession(s1)

	lci2, _ := sm.AllocateTunLCI()
	s2 := &Session{LciA: lci2, LciB: 102}
	sm.AddSession(s2)

	// Free LCI 1
	sm.RemoveSession(s1)

	// Next allocation should return 1 (lowest free)
	lci3, err := sm.AllocateTunLCI()
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if lci3 != 1 {
		t.Errorf("Expected LCI 1 after remove, got %d", lci3)
	}

	_ = s2 // suppress unused warning
}

func TestRemoveSessionNilConnB(t *testing.T) {
	sm := NewSessionManager(1, 100)

	s := &Session{LciA: 5, LciB: 99, ConnB: nil}
	sm.AddSession(s)

	// Must not panic
	sm.RemoveSession(s)

	if sm.GetByALCI(5) != nil {
		t.Error("Session should no longer be in manager after remove")
	}
}

func TestSessionStateConstants(t *testing.T) {
	cases := map[string]string{
		"StateP1": StateP1,
		"StateP2": StateP2,
		"StateP3": StateP3,
		"StateP4": StateP4,
		"StateP5": StateP5,
	}
	expected := map[string]string{
		"StateP1": "p1",
		"StateP2": "p2",
		"StateP3": "p3",
		"StateP4": "p4",
		"StateP5": "p5",
	}
	for name, val := range cases {
		want := expected[name]
		if val != want {
			t.Errorf("%s: expected %q, got %q", name, want, val)
		}
	}
}

