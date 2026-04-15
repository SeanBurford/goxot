package xot

import (
	"net"
	"sync"
)

type Session struct {
	LocalLCI  uint16
	RemoteLCI uint16
	Conn      net.Conn
	Server    *XotServerConfig
	Quit      chan struct{}
	ToXot     chan *X25Packet
	FromXot   chan *X25Packet
}

type SessionManager struct {
	mu               sync.RWMutex
	sessionsByLocal  map[uint16]*Session
	sessionsByRemote map[string]map[uint16]*Session // serverIP -> remoteLCI -> session
	usedRemoteLCIs   map[string]map[uint16]bool     // serverIP -> LCI -> used
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessionsByLocal:  make(map[uint16]*Session),
		sessionsByRemote: make(map[string]map[uint16]*Session),
		usedRemoteLCIs:   make(map[string]map[uint16]bool),
	}
}

func (sm *SessionManager) AllocateRemoteLCI(serverIP string, lciStart, lciEnd int) (uint16, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.usedRemoteLCIs[serverIP] == nil {
		sm.usedRemoteLCIs[serverIP] = make(map[uint16]bool)
	}

	for lci := uint16(lciStart); lci <= uint16(lciEnd); lci++ {
		if !sm.usedRemoteLCIs[serverIP][lci] {
			sm.usedRemoteLCIs[serverIP][lci] = true
			return lci, true
		}
	}
	return 0, false
}

func (sm *SessionManager) ReleaseRemoteLCI(srvIP string, lci uint16) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.releaseRemoteLCI(srvIP, lci)
}

func (sm *SessionManager) releaseRemoteLCI(srvIP string, lci uint16) {
	if sm.usedRemoteLCIs[srvIP] != nil {
		delete(sm.usedRemoteLCIs[srvIP], lci)
	}
}

func (sm *SessionManager) AddSession(s *Session) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.sessionsByLocal[s.LocalLCI] = s
	if sm.sessionsByRemote[s.Server.IP] == nil {
		sm.sessionsByRemote[s.Server.IP] = make(map[uint16]*Session)
	}
	sm.sessionsByRemote[s.Server.IP][s.RemoteLCI] = s
}

func (sm *SessionManager) RemoveSession(s *Session) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	delete(sm.sessionsByLocal, s.LocalLCI)
	if sm.sessionsByRemote[s.Server.IP] != nil {
		delete(sm.sessionsByRemote[s.Server.IP], s.RemoteLCI)
	}
	sm.releaseRemoteLCI(s.Server.IP, s.RemoteLCI)
}

func (sm *SessionManager) GetByLocalLCI(lci uint16) *Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.sessionsByLocal[lci]
}

func (sm *SessionManager) GetByRemoteLCI(srvIP string, lci uint16) *Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.sessionsByRemote[srvIP] == nil {
		return nil
	}
	return sm.sessionsByRemote[srvIP][lci]
}
