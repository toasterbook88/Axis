package axismcp

import (
	"context"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/state"
)

func TestSessionCacheSnapshotCachingAndTTL(t *testing.T) {
	fetchCount := 0
	restore := stubMCPRuntime(t, nil, nil)
	defer restore()

	loadMCPRuntime = func(ctx context.Context) (*runtimectx.Context, error) {
		fetchCount++
		return &runtimectx.Context{
			Snapshot: &models.ClusterSnapshot{
				Status: models.SnapshotHealthy,
				Nodes: []models.NodeFacts{
					{Name: "node-1"},
				},
			},
		}, nil
	}

	cache := NewSessionCache(100*time.Millisecond, false, "")

	// First fetch - should call loadMCPRuntime
	ctx := context.Background()
	snap1, err := cache.GetSnapshot(ctx, "session-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetchCount != 1 {
		t.Fatalf("expected 1 fetch, got %d", fetchCount)
	}
	if len(snap1.Nodes) != 1 || snap1.Nodes[0].Name != "node-1" {
		t.Fatalf("unexpected snapshot contents: %+v", snap1)
	}

	// Second fetch - within TTL, should hit cache
	snap2, err := cache.GetSnapshot(ctx, "session-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetchCount != 1 {
		t.Fatalf("expected cached hit, got fetch count %d", fetchCount)
	}
	// Verify cloning
	if snap1 == snap2 {
		t.Fatal("expected cached snapshot to be a clone, got identical pointer")
	}

	// Fetch with a different session - should fetch again
	snapOther, err := cache.GetSnapshot(ctx, "session-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetchCount != 2 {
		t.Fatalf("expected separate fetch for new session, got fetch count %d", fetchCount)
	}
	if snapOther.Nodes[0].Name != "node-1" {
		t.Fatal("unexpected snapshot content for session-2")
	}

	// Wait for TTL expiration
	time.Sleep(150 * time.Millisecond)

	// Fetch again - TTL expired, should fetch
	snap3, err := cache.GetSnapshot(ctx, "session-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetchCount != 3 {
		t.Fatalf("expected fetch after TTL expiration, got fetch count %d", fetchCount)
	}
	if snap3.Nodes[0].Name != "node-1" {
		t.Fatal("unexpected snapshot content after TTL expiration")
	}
}

func TestSessionCachePlacementInputsCachingUncached(t *testing.T) {
	fetchCount := 0

	restore := stubMCPRuntime(t, nil, nil)
	defer restore()
	loadMCPRuntime = func(ctx context.Context) (*runtimectx.Context, error) {
		fetchCount++
		return &runtimectx.Context{
			Snapshot: &models.ClusterSnapshot{
				Status: models.SnapshotHealthy,
			},
			State: &state.ClusterState{
				Nodes: map[string]state.NodeState{
					"node-1": {ReservedMB: 1024},
				},
			},
		}, nil
	}

	cache := NewSessionCache(100*time.Millisecond, false, "")

	ctx := context.Background()
	snap1, st1, err := cache.GetPlacementInputs(ctx, "session-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetchCount != 1 {
		t.Fatalf("expected 1 fetch, got %d", fetchCount)
	}
	if st1 == nil || st1.Nodes["node-1"].ReservedMB != 1024 {
		t.Fatalf("unexpected state: %+v", st1)
	}

	// Fetch again - should hit cache
	snap2, st2, err := cache.GetPlacementInputs(ctx, "session-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetchCount != 1 {
		t.Fatalf("expected cache hit, got %d", fetchCount)
	}
	if snap1 == snap2 {
		t.Fatal("expected snap2 to be cloned")
	}
	if st1 != st2 {
		t.Fatal("expected state reference to match")
	}
}

func TestSessionCachePlacementInputsCachingCached(t *testing.T) {
	fetchCount := 0
	stateCount := 0

	restoreCached := stubCachedSnapshotFetcher(t, func(ctx context.Context, addr string) (*models.ClusterSnapshot, string, error) {
		fetchCount++
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
		}, "cache", nil
	})
	defer restoreCached()

	restoreState := stubMCPStateLoader(t, func() (*state.ClusterState, error) {
		stateCount++
		return &state.ClusterState{
			Nodes: map[string]state.NodeState{
				"node-1": {ReservedMB: 2048},
			},
		}, nil
	})
	defer restoreState()

	cache := NewSessionCache(100*time.Millisecond, true, "http://localhost")

	ctx := context.Background()
	snap1, st1, err := cache.GetPlacementInputs(ctx, "session-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetchCount != 1 || stateCount != 1 {
		t.Fatalf("expected 1 fetch/state read, got %d/%d", fetchCount, stateCount)
	}
	if st1 == nil || st1.Nodes["node-1"].ReservedMB != 2048 {
		t.Fatalf("unexpected state: %+v", st1)
	}

	// Fetch again - should hit cache
	snap2, st2, err := cache.GetPlacementInputs(ctx, "session-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetchCount != 1 || stateCount != 1 {
		t.Fatalf("expected cache hit, got %d/%d", fetchCount, stateCount)
	}
	if snap1 == snap2 {
		t.Fatal("expected snap2 to be cloned")
	}
	if st1 != st2 {
		t.Fatal("expected state reference to match")
	}
}

func TestSessionCacheInvalidation(t *testing.T) {
	fetchCount := 0
	restore := stubMCPRuntime(t, nil, nil)
	defer restore()

	loadMCPRuntime = func(ctx context.Context) (*runtimectx.Context, error) {
		fetchCount++
		return &runtimectx.Context{
			Snapshot: &models.ClusterSnapshot{
				Status: models.SnapshotHealthy,
			},
		}, nil
	}

	cache := NewSessionCache(10*time.Second, false, "")
	ctx := context.Background()

	_, _ = cache.GetSnapshot(ctx, "session-1")
	_, _ = cache.GetSnapshot(ctx, "session-2")
	if fetchCount != 2 {
		t.Fatalf("expected 2 fetches, got %d", fetchCount)
	}

	// Invalidate session-1
	cache.Invalidate("session-1")

	// session-1 should fetch again
	_, _ = cache.GetSnapshot(ctx, "session-1")
	if fetchCount != 3 {
		t.Fatalf("expected session-1 to fetch again, got %d", fetchCount)
	}

	// session-2 should still be cached
	_, _ = cache.GetSnapshot(ctx, "session-2")
	if fetchCount != 3 {
		t.Fatalf("expected session-2 to hit cache, got %d", fetchCount)
	}

	// InvalidateAll
	cache.InvalidateAll()

	// session-2 should now fetch again
	_, _ = cache.GetSnapshot(ctx, "session-2")
	if fetchCount != 4 {
		t.Fatalf("expected session-2 to fetch again after InvalidateAll, got %d", fetchCount)
	}
}

func TestSessionCachePlacementInputsCachingNilState(t *testing.T) {
	fetchCount := 0

	restore := stubMCPRuntime(t, nil, nil)
	defer restore()
	loadMCPRuntime = func(ctx context.Context) (*runtimectx.Context, error) {
		fetchCount++
		return &runtimectx.Context{
			Snapshot: &models.ClusterSnapshot{
				Status: models.SnapshotHealthy,
			},
			State: nil, // Legitimately nil state
		}, nil
	}

	cache := NewSessionCache(100*time.Millisecond, false, "")

	ctx := context.Background()
	_, st1, err := cache.GetPlacementInputs(ctx, "session-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetchCount != 1 {
		t.Fatalf("expected 1 fetch, got %d", fetchCount)
	}
	if st1 != nil {
		t.Fatalf("expected state to be nil, got %+v", st1)
	}

	// Fetch again - should hit cache (and not call loadMCPRuntime) despite state being nil
	_, st2, err := cache.GetPlacementInputs(ctx, "session-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetchCount != 1 {
		t.Fatalf("expected cache hit (no new fetch), got fetch count %d", fetchCount)
	}
	if st2 != nil {
		t.Fatalf("expected cached state to be nil, got %+v", st2)
	}

	// Wait for TTL expiration to force a GetSnapshot cache miss/update
	time.Sleep(150 * time.Millisecond)

	// Now call GetSnapshot - should invalidate/clear the cached state
	_, err = cache.GetSnapshot(ctx, "session-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetchCount != 2 {
		t.Fatalf("expected new fetch on GetSnapshot, got %d", fetchCount)
	}

	// Now calling GetPlacementInputs should trigger a fetch again because GetSnapshot cleared hasState
	_, _, err = cache.GetPlacementInputs(ctx, "session-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetchCount != 3 {
		t.Fatalf("expected fetch since state was invalidated, got %d", fetchCount)
	}
}
