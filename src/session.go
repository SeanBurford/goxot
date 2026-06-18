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

	GFI byte // negotiated GFI

	// Side A (e.g. TUN)
	LciA  uint16
	ConnA net.Conn // nil if side A is the TUN physical interface

	// Side B (e.g. TCP XOT)
	LciB  uint16
	ConnB net.Conn

	CreatedAt int64
}

// SetState updates the session's X.25 call state.
func (s *Session) SetState(newState string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.State != newState {
		s.State = newState
	}
}

// SessionManager tracks active X.25 virtual circuits indexed by LCI and connection.
type SessionManager struct {
	mu         sync.RWMutex
	sessions   map[string]*Session
	byALCI     map[uint16]*Session
	byBConnLCI map[net.Conn]map[uint16]*Session

	tunLciStart uint16
	tunLciEnd   uint16
	nextLCI     uint16 // round-robin cursor; never reuse the most-recently freed LCI immediately
}

// NewSessionManager creates a SessionManager using the given LCI range.
func NewSessionManager(lciStart, lciEnd uint16) *SessionManager {
	// Defence-in-depth: clamp to the valid X.25 LCI range even if the caller
	// passes unchecked config values.  Primary clamping happens in config.Reload.
	if lciStart < LCIMin {
		lciStart = LCIMin
	}
	if lciEnd > LCIMax {
		lciEnd = LCIMax
	}
	if lciStart > lciEnd {
		lciStart = LCIMin
		lciEnd = LCIMax
	}
	return &SessionManager{
		sessions:    make(map[string]*Session),
		byALCI:      make(map[uint16]*Session),
		byBConnLCI:  make(map[net.Conn]map[uint16]*Session),
		tunLciStart: lciStart,
		tunLciEnd:   lciEnd,
		nextLCI:     lciStart,
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

// AddSession registers a session and indexes it by LCI A and (ConnB, LCI B).
func (sm *SessionManager) AddSession(s *Session) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if s.GFI == 0 {
		return fmt.Errorf("GFI cannot be zero")
	}
	if s.LciA < LCIMin {
		return fmt.Errorf("LCI A cannot be < %d", LCIMin)
	}
	if s.LciA > LCIMax {
		return fmt.Errorf("LCI A cannot be > %d", LCIMax)
	}
	if s.LciB < LCIMin {
		return fmt.Errorf("LCI B cannot be < %d", LCIMin)
	}
	if s.LciB > LCIMax {
		return fmt.Errorf("LCI B cannot be > %d", LCIMax)
	}

	// Unique ID including connection pointers to distinguish recycled LCIs
	id := fmt.Sprintf("A:%p:%d-B:%p:%d", s.ConnA, s.LciA, s.ConnB, s.LciB)
	s.ID = id
	sm.sessions[id] = s

	// Index by A
	sm.byALCI[s.LciA] = s

	// Index by B
	if s.ConnB != nil {
		if sm.byBConnLCI[s.ConnB] == nil {
			sm.byBConnLCI[s.ConnB] = make(map[uint16]*Session)
		}
		sm.byBConnLCI[s.ConnB][s.LciB] = s
	}
	return nil
}

// AllocateAndAddTunSession atomizes LCI allocation and session creation for TUN-side LCIs
func (sm *SessionManager) AllocateAndAddTunSession(incomingConn net.Conn, incomingGFI byte, incomingLCI uint16) (*Session, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Double check if already exists under lock
	if lcis, ok := sm.byBConnLCI[incomingConn]; ok {
		if s, ok := lcis[incomingLCI]; ok {
			return s, nil
		}
	}

	// Round-robin: start from nextLCI so that a just-freed LCI is not immediately
	// reused. This is defence-in-depth against any residual cleanupConn races where
	// a late goroutine still holds a stale session pointer for the old LCI value.
	rangeSize := sm.tunLciEnd - sm.tunLciStart + 1
	for i := uint16(0); i < rangeSize; i++ {
		lci := sm.tunLciStart + (sm.nextLCI-sm.tunLciStart+i)%rangeSize
		if _, ok := sm.byALCI[lci]; !ok {
			// Advance cursor past this LCI for the next allocation.
			sm.nextLCI = sm.tunLciStart + (lci-sm.tunLciStart+1)%rangeSize
			s := &Session{
				GFI:   NegotiateGFI(incomingGFI),
				LciA:  lci,
				LciB:  incomingLCI,
				ConnB: incomingConn,
				State: StateP1,
			}
			id := fmt.Sprintf("A:%p:%d-B:%p:%d", s.ConnA, s.LciA, s.ConnB, s.LciB)
			s.ID = id
			sm.sessions[id] = s
			sm.byALCI[lci] = s
			if sm.byBConnLCI[incomingConn] == nil {
				sm.byBConnLCI[incomingConn] = make(map[uint16]*Session)
			}
			sm.byBConnLCI[incomingConn][incomingLCI] = s
			return s, nil
		}
	}
	return nil, fmt.Errorf("LCI exhaustion in range %d-%d", sm.tunLciStart, sm.tunLciEnd)
}

// RemoveByBConnLCI removes the session identified by the B-side connection and LCI.
func (sm *SessionManager) RemoveByBConnLCI(conn net.Conn, lci uint16) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	lcis, ok := sm.byBConnLCI[conn]
	if !ok {
		return
	}
	s, ok := lcis[lci]
	if !ok {
		return
	}

	delete(sm.sessions, s.ID)
	delete(sm.byALCI, s.LciA)
	delete(lcis, lci)
	if len(lcis) == 0 {
		delete(sm.byBConnLCI, conn)
	}
}

// RemoveSession removes a session from all indexes.
func (sm *SessionManager) RemoveSession(s *Session) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.sessions[s.ID] == s {
		delete(sm.sessions, s.ID)
	}
	if sm.byALCI[s.LciA] == s {
		delete(sm.byALCI, s.LciA)
	}
	if s.ConnB != nil && sm.byBConnLCI[s.ConnB] != nil {
		if sm.byBConnLCI[s.ConnB][s.LciB] == s {
			delete(sm.byBConnLCI[s.ConnB], s.LciB)
			if len(sm.byBConnLCI[s.ConnB]) == 0 {
				delete(sm.byBConnLCI, s.ConnB)
			}
		}
	}
}

// GetByALCI returns the session with the given A-side LCI, or nil.
func (sm *SessionManager) GetByALCI(lci uint16) *Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.byALCI[lci]
}

// GetByBConnLCI returns the session for the given B-side connection and LCI, or nil.
func (sm *SessionManager) GetByBConnLCI(conn net.Conn, lci uint16) *Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.byBConnLCI[conn] == nil {
		return nil
	}
	return sm.byBConnLCI[conn][lci]
}

// GetSessionsForConn returns all sessions associated with a B-side connection.
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

// GetAllSessions returns all active sessions.
func (sm *SessionManager) GetAllSessions() []*Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var res []*Session
	for _, s := range sm.sessions {
		res = append(res, s)
	}
	return res
}

// RemoveAllSessions removes and returns all active sessions.
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
