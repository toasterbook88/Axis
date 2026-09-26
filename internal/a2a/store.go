// Copyright (c) 2026 Smith Software Solutions
package a2a

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const defaultTaskTTL = 30 * time.Minute

// Store is an in-memory, principal-bound task store with TTL expiry (F6, F7).
// Principal binding is enforced on Get; with a single shared API token all
// authenticated callers share one principal hash (defense-in-depth today).
type Store struct {
	mu   sync.RWMutex
	byID map[string]*Task
	ttl  time.Duration
	now  func() time.Time
}

// NewStore returns a task store with the given TTL (default 30m when <= 0).
func NewStore(ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = defaultTaskTTL
	}
	return &Store{
		byID: make(map[string]*Task),
		ttl:  ttl,
		now:  time.Now,
	}
}

// Put inserts or replaces a task. Sets CreatedAt/ExpiresAt when zero.
func (s *Store) Put(t *Task) {
	if t == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.ExpiresAt.IsZero() {
		t.ExpiresAt = now.Add(s.ttl)
	}
	cp := *t
	s.byID[t.ID] = &cp
}

// Get returns a copy of the task when present, unexpired, and owned by
// principalHash. ok=false covers missing, expired, and foreign principal
// alike so callers cannot distinguish leak cases on the wire (F6).
func (s *Store) Get(id, principalHash string) (*Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	t, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	if t.PrincipalHash == "" || t.PrincipalHash != principalHash {
		return nil, false
	}
	cp := *t
	return &cp, true
}

// NewID returns a random hex task id.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *Store) expireLocked() {
	now := s.now()
	for id, t := range s.byID {
		if !t.ExpiresAt.IsZero() && now.After(t.ExpiresAt) {
			delete(s.byID, id)
		}
	}
}
