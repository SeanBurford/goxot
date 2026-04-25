package xot

import (
	"fmt"
	"net"
	"sync"
)

// X.25 States (ITU-T X.25 Section 4)
const (
	StateP1 = "p1" // Ready
	StateP2 = "p2" // DTE Waiting
	StateP3 = "p3" // DCE Waiting
	StateP4 = "p4" // Data Transfer
	StateP5 = "p5" // Call Clearing
)

// Session represents an active X.25 virtual circuit / logical channel.
type Session struct {
	ID    string
	State string
	mu    sync.Mutex

	// Side A (e.g. TUN)
	LciA  uint16
	ConnA net.Conn // nil if side A is the TUN physical interface

	// Side B (e.g. TCP XOT)
	LciB  uint16
	ConnB net.Conn

	CreatedAt int64
}

func (s *Session) SetState(newState string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.State != newState {
		s.State = newState
	}
}

type SessionManager struct {
	mu           sync.RWMutex
	sessions     map[string]*Session
	byALCI       map[uint16]*Session
	byBConnLCI   map[net.Conn]map[uint16]*Session
	
	tunLciStart  uint16
	tunLciEnd    uint16
}

func NewSessionManager(lciStart, lciEnd uint16) *SessionManager {
	return &SessionManager{
		sessions:    make(map[string]*Session),
		byALCI:      make(map[uint16]*Session),
		byBConnLCI:  make(map[net.Conn]map[uint16]*Session),
		tunLciStart: lciStart,
		tunLciEnd:   lciEnd,
	}
}

// AllocateTunLCI finds the lowest available LCI for the TUN side
func (sm *SessionManager) AllocateTunLCI() (uint16, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for lci := sm.tunLciStart; lci <= sm.tunLciEnd; lci++ {
		if _, ok := sm.byALCI[lci]; !ok {
			return lci, nil
		}
	}
	return 0, fmt.Errorf("LCI exhaustion in range %d-%d", sm.tunLciStart, sm.tunLciEnd)
}

func (sm *SessionManager) AddSession(s *Session) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	id := fmt.Sprintf("%v:%d-%v:%d", s.ConnA, s.LciA, s.ConnB, s.LciB)
	s.ID = id
	sm.sessions[id] = s
	
	// Index by A
	sm.byALCI[s.LciA] = s
	
	// Index by B
	if sm.byBConnLCI[s.ConnB] == nil {
		sm.byBConnLCI[s.ConnB] = make(map[uint16]*Session)
	}
	sm.byBConnLCI[s.ConnB][s.LciB] = s
}

func (sm *SessionManager) RemoveSession(s *Session) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	delete(sm.sessions, s.ID)
	delete(sm.byALCI, s.LciA)
	if s.ConnB != nil && sm.byBConnLCI[s.ConnB] != nil {
		delete(sm.byBConnLCI[s.ConnB], s.LciB)
		if len(sm.byBConnLCI[s.ConnB]) == 0 {
			delete(sm.byBConnLCI, s.ConnB)
		}
	}
}

func (sm *SessionManager) GetByALCI(lci uint16) *Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.byALCI[lci]
}

func (sm *SessionManager) GetByBConnLCI(conn net.Conn, lci uint16) *Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.byBConnLCI[conn] == nil {
		return nil
	}
	return sm.byBConnLCI[conn][lci]
}

func (sm *SessionManager) GetSessionsForConn(conn net.Conn) []*Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	var res []*Session
	if lcis, ok := sm.byBConnLCI[conn]; ok {
		for _, s := range lcis {
			res = append(res, s)
		}
	}
	return res
}

func (sm *SessionManager) GetAllSessions() []*Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var res []*Session
	for _, s := range sm.sessions {
		res = append(res, s)
	}
	return res
}

func (sm *SessionManager) RemoveAllSessions() []*Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	var res []*Session
	for _, s := range sm.sessions {
		res = append(res, s)
	}
	sm.sessions = make(map[string]*Session)
	sm.byALCI = make(map[uint16]*Session)
	sm.byBConnLCI = make(map[net.Conn]map[uint16]*Session)
	return res
}
