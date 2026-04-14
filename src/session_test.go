package xot

import (
	"testing"
)

func TestSessionManager(t *testing.T) {
	sm := NewSessionManager()
	serverIP := "1.2.3.4"

	lci, ok := sm.AllocateRemoteLCI(serverIP, 1, 10)
	if !ok || lci != 1 {
		t.Errorf("Expected LCI 1, got %d", lci)
	}

	lci2, _ := sm.AllocateRemoteLCI(serverIP, 1, 10)
	if lci2 != 2 {
		t.Errorf("Expected LCI 2, got %d", lci2)
	}

	sm.ReleaseRemoteLCI(serverIP, lci)
	lci3, _ := sm.AllocateRemoteLCI(serverIP, 1, 10)
	if lci3 != 1 {
		t.Errorf("Expected LCI 1 after release, got %d", lci3)
	}
}

func TestAddRemoveSession(t *testing.T) {
	sm := NewSessionManager()
	s := &Session{
		LocalLCI:  10,
		RemoteLCI: 20,
		Server:    &XotServerConfig{IP: "1.2.3.4"},
	}

	sm.AddSession(s)
	if sm.GetByLocalLCI(10) != s {
		t.Errorf("Failed to get session by local LCI")
	}
	if sm.GetByRemoteLCI("1.2.3.4", 20) != s {
		t.Errorf("Failed to get session by remote LCI")
	}

	sm.RemoveSession(s)
	if sm.GetByLocalLCI(10) != nil {
		t.Errorf("Session still exists after removal")
	}
}
