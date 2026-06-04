package axismcp

import (
	"context"
	"sync"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

type mcpCacheEntry struct {
	snapshot *models.ClusterSnapshot
	state    *state.ClusterState
	hasState bool
	cachedAt time.Time
}

type SessionCache struct {
	mu        sync.Mutex
	entries   map[string]*mcpCacheEntry
	ttl       time.Duration
	useCache  bool
	cacheAddr string
}

func NewSessionCache(ttl time.Duration, useCache bool, cacheAddr string) *SessionCache {
	return &SessionCache{
		entries:   make(map[string]*mcpCacheEntry),
		ttl:       ttl,
		useCache:  useCache,
		cacheAddr: cacheAddr,
	}
}

func (c *SessionCache) GetSnapshot(ctx context.Context, sessionID string) (*models.ClusterSnapshot, error) {
	c.mu.Lock()
	entry, ok := c.entries[sessionID]
	c.mu.Unlock()

	if ok && time.Since(entry.cachedAt) < c.ttl && entry.snapshot != nil {
		return daemon.CloneSnapshot(entry.snapshot), nil
	}

	snap, err := currentSnapshot(ctx, c.useCache, c.cacheAddr)
	if err != nil {
		return nil, err
	}

	// Clone snapshot to ensure each session gets a private copy
	snapCopy := daemon.CloneSnapshot(snap)

	c.mu.Lock()
	if c.entries[sessionID] == nil {
		c.entries[sessionID] = &mcpCacheEntry{}
	}
	c.entries[sessionID].snapshot = snapCopy
	c.entries[sessionID].state = nil
	c.entries[sessionID].hasState = false
	c.entries[sessionID].cachedAt = time.Now()
	c.mu.Unlock()

	return snapCopy, nil
}

func (c *SessionCache) GetPlacementInputs(ctx context.Context, sessionID string) (*models.ClusterSnapshot, *state.ClusterState, error) {
	c.mu.Lock()
	entry, ok := c.entries[sessionID]
	c.mu.Unlock()

	if ok && time.Since(entry.cachedAt) < c.ttl && entry.snapshot != nil && entry.hasState {
		return daemon.CloneSnapshot(entry.snapshot), entry.state, nil
	}

	snap, st, err := currentPlacementInputs(ctx, c.useCache, c.cacheAddr)
	if err != nil {
		return nil, nil, err
	}

	// Clone snapshot and state to ensure each session gets private copies
	snapCopy := daemon.CloneSnapshot(snap)
	var stateCopy *state.ClusterState
	if st != nil {
		// ClusterState is treated as a shared reference since it is read-only metadata.
		stateCopy = st
	}

	c.mu.Lock()
	if c.entries[sessionID] == nil {
		c.entries[sessionID] = &mcpCacheEntry{}
	}
	c.entries[sessionID].snapshot = snapCopy
	c.entries[sessionID].state = stateCopy
	c.entries[sessionID].hasState = true
	c.entries[sessionID].cachedAt = time.Now()
	c.mu.Unlock()

	return snapCopy, stateCopy, nil
}

func (c *SessionCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*mcpCacheEntry)
}

func (c *SessionCache) Invalidate(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, sessionID)
}

func GetSessionID(ctx context.Context) string {
	session := mcpserver.ClientSessionFromContext(ctx)
	if session != nil {
		return session.SessionID()
	}
	return "global"
}
