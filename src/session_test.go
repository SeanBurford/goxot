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
