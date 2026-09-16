package runtimectx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/persist"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

func stubCacheDeps(
	t *testing.T,
	cfgFn func(string) (*config.Config, error),
	daemonFn func(context.Context, string) (*models.ClusterSnapshot, string, error),
	diskFn func() (*models.ClusterSnapshot, error),
	stateFn func() (*state.ClusterState, error),
	skillsFn func() (*skills.Store, error),
) func() {
	t.Helper()
	prevCfg := loadConfig
	prevDaemon := fetchDaemonSnapshot
	prevDisk := readDiskSnapshot
	prevStat := loadState
	prevSkills := loadSkills
	prevLedger := loadLedger

	if cfgFn != nil {
		loadConfig = cfgFn
	}
	if daemonFn != nil {
		fetchDaemonSnapshot = daemonFn
	}
	if diskFn != nil {
		readDiskSnapshot = diskFn
	}
	if stateFn != nil {
		loadState = stateFn
	}
	if skillsFn != nil {
		loadSkills = skillsFn
	}
	loadLedger = func() (*reservation.Ledger, error) {
		return reservation.NewLedger(reservation.DefaultLimits(), nil), nil
	}

	return func() {
		loadConfig = prevCfg
		fetchDaemonSnapshot = prevDaemon
		readDiskSnapshot = prevDisk
		loadState = prevStat
		loadSkills = prevSkills
		loadLedger = prevLedger
	}
}

func TestLoadCachedFromDaemonHTTP(t *testing.T) {
	cfg := &config.Config{
		Nodes: []config.NodeConfig{{Name: "node-a", Hostname: "node-a.example.com"}},
	}
	observedAt := time.Now().UTC().Add(-30 * time.Second)
	daemonSnap := &models.ClusterSnapshot{
		Timestamp: observedAt,
		Status:    models.SnapshotHealthy,
		Nodes: []models.NodeFacts{
			{
				Name:   "node-a",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMTotalMB: 16384,
					RAMFreeMB:  8192,
				},
			},
		},
		Publication: &models.PublicationEnvelope{
			ID:     "pub-daemon-123",
			Source: "daemon-publication",
		},
		Vantage: &models.VantageInfo{
			NodeName:   "foundry",
			ObservedAt: observedAt,
		},
	}

	restore := stubCacheDeps(t,
		func(string) (*config.Config, error) { return cfg, nil },
		func(context.Context, string) (*models.ClusterSnapshot, string, error) {
			return daemonSnap, "daemon-cache", nil
		},
		func() (*models.ClusterSnapshot, error) {
			t.Fatal("readDiskSnapshot must not be called when daemon succeeds")
			return nil, nil
		},
		func() (*state.ClusterState, error) {
			return &state.ClusterState{Version: 1, Nodes: map[string]state.NodeState{}}, nil
		},
		func() (*skills.Store, error) {
			return &skills.Store{}, nil
		},
	)
	defer restore()

	rt, err := LoadCached(context.Background())
	if err != nil {
		t.Fatalf("LoadCached: %v", err)
	}
	if rt == nil || rt.Snapshot == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if rt.Snapshot.Publication == nil || rt.Snapshot.Publication.ID != "pub-daemon-123" {
		t.Fatalf("expected publication ID pub-daemon-123, got %+v", rt.Snapshot.Publication)
	}
	if rt.Snapshot.Vantage == nil || rt.Snapshot.Vantage.NodeName != "foundry" {
		t.Fatalf("expected vantage foundry, got %+v", rt.Snapshot.Vantage)
	}
	if len(rt.Snapshot.Nodes) != 1 || rt.Snapshot.Nodes[0].Name != "node-a" {
		t.Fatalf("unexpected nodes: %+v", rt.Snapshot.Nodes)
	}
}

func TestLoadCachedFallbackToDiskSnapshot(t *testing.T) {
	cfg := &config.Config{
		Nodes: []config.NodeConfig{{Name: "node-b", Hostname: "node-b.example.com"}},
	}
	observedAt := time.Now().UTC().Add(-1 * time.Minute)
	diskSnap := &models.ClusterSnapshot{
		Timestamp: observedAt,
		Status:    models.SnapshotHealthy,
		Nodes: []models.NodeFacts{
			{
				Name:   "node-b",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMTotalMB: 32768,
					RAMFreeMB:  16384,
				},
			},
		},
		Publication: &models.PublicationEnvelope{
			ID:     "pub-disk-456",
			Source: "daemon-publication",
		},
		Vantage: &models.VantageInfo{
			NodeName:   "cranium",
			ObservedAt: observedAt,
		},
	}

	restore := stubCacheDeps(t,
		func(string) (*config.Config, error) { return cfg, nil },
		func(context.Context, string) (*models.ClusterSnapshot, string, error) {
			return nil, "", errors.New("connection refused")
		},
		func() (*models.ClusterSnapshot, error) {
			return diskSnap, nil
		},
		func() (*state.ClusterState, error) {
			return &state.ClusterState{Version: 1, Nodes: map[string]state.NodeState{}}, nil
		},
		func() (*skills.Store, error) {
			return &skills.Store{}, nil
		},
	)
	defer restore()

	rt, err := LoadCached(context.Background())
	if err != nil {
		t.Fatalf("LoadCached: %v", err)
	}
	if rt.Snapshot == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if rt.Snapshot.Vantage == nil || rt.Snapshot.Vantage.NodeName != "cranium" {
		t.Fatalf("expected vantage cranium, got %+v", rt.Snapshot.Vantage)
	}

	foundWarning := false
	for _, w := range rt.Snapshot.Warnings {
		if w.Kind == "cache" && w.Message == "daemon unreachable; snapshot loaded from disk cache" {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatalf("expected daemon unreachable warning, got: %+v", rt.Snapshot.Warnings)
	}
}

func TestLoadCachedFallbackToBootstrapSkeleton(t *testing.T) {
	cfg := &config.Config{
		Nodes: []config.NodeConfig{
			{Name: "node-1", Hostname: "node-1.example.com", Role: "compute"},
			{Name: "node-2", Hostname: "node-2.example.com", Role: "storage"},
		},
	}

	restore := stubCacheDeps(t,
		func(string) (*config.Config, error) { return cfg, nil },
		func(context.Context, string) (*models.ClusterSnapshot, string, error) {
			return nil, "", errors.New("connection refused")
		},
		func() (*models.ClusterSnapshot, error) {
			return nil, os.ErrNotExist
		},
		func() (*state.ClusterState, error) {
			return &state.ClusterState{Version: 1, Nodes: map[string]state.NodeState{}}, nil
		},
		func() (*skills.Store, error) {
			return &skills.Store{}, nil
		},
	)
	defer restore()

	rt, err := LoadCached(context.Background())
	if err != nil {
		t.Fatalf("LoadCached should not fail on cold bootstrap, got: %v", err)
	}
	if rt.Snapshot == nil {
		t.Fatal("expected bootstrap skeleton snapshot")
	}
	if len(rt.Snapshot.Nodes) != 2 {
		t.Fatalf("expected 2 bootstrap nodes from config, got %d", len(rt.Snapshot.Nodes))
	}
	if rt.Snapshot.Nodes[0].Name != "node-1" || rt.Snapshot.Nodes[1].Name != "node-2" {
		t.Fatalf("unexpected node names: %+v", rt.Snapshot.Nodes)
	}
	// Per C1 contract: Vantage must be nil (unknown), never inferred
	if rt.Snapshot.Vantage != nil {
		t.Fatalf("bootstrap snapshot must not synthesize a fake vantage, got: %+v", rt.Snapshot.Vantage)
	}
	if rt.Snapshot.Publication == nil {
		t.Fatal("expected fallback publication envelope")
	}
	if rt.Snapshot.Publication.Source == "live-runtime" {
		t.Fatal("bootstrap skeleton publication must not be labeled live-runtime")
	}
	if rt.Snapshot.Publication.Source != "bootstrap" {
		t.Fatalf("expected publication source 'bootstrap', got %q", rt.Snapshot.Publication.Source)
	}
	if !rt.Snapshot.Publication.AssembledAt.Equal(rt.Snapshot.Timestamp.UTC()) {
		t.Fatalf("bootstrap AssembledAt = %v, want snapshot timestamp %v", rt.Snapshot.Publication.AssembledAt, rt.Snapshot.Timestamp)
	}
}

func TestLoadCachedMintsPublicationAssembledAtFromSnapshotTimestamp(t *testing.T) {
	observedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	diskSnap := &models.ClusterSnapshot{
		Timestamp: observedAt,
		Status:    models.SnapshotHealthy,
		Nodes:     []models.NodeFacts{{Name: "node-a", Status: models.StatusComplete}},
	}
	restore := stubCacheDeps(t,
		func(string) (*config.Config, error) { return &config.Config{}, nil },
		func(context.Context, string) (*models.ClusterSnapshot, string, error) {
			return nil, "", errors.New("connection refused")
		},
		func() (*models.ClusterSnapshot, error) { return diskSnap, nil },
		func() (*state.ClusterState, error) { return &state.ClusterState{}, nil },
		func() (*skills.Store, error) { return &skills.Store{}, nil },
	)
	defer restore()

	before := time.Now().UTC()
	rt, err := LoadCached(context.Background())
	if err != nil {
		t.Fatalf("LoadCached: %v", err)
	}
	if rt.Snapshot.Publication == nil {
		t.Fatal("expected minted publication envelope")
	}
	if rt.Snapshot.Publication.Source != "daemon-cache" {
		t.Fatalf("source = %q, want daemon-cache", rt.Snapshot.Publication.Source)
	}
	if !rt.Snapshot.Publication.AssembledAt.Equal(observedAt) {
		t.Fatalf("AssembledAt = %v, want snapshot timestamp %v (not read time after %v)", rt.Snapshot.Publication.AssembledAt, observedAt, before)
	}
}

func TestDefaultCacheFetchAddressIsSocketNotPort8080(t *testing.T) {
	addr := DefaultDaemonAddr()
	want := persist.AxisPath("axis.sock")
	if addr != want {
		t.Fatalf("DefaultDaemonAddr() = %q, want %q", addr, want)
	}
	if addr == "127.0.0.1:8080" || strings.Contains(addr, "8080") {
		t.Fatalf("DefaultDaemonAddr() should be unix socket, got port 8080 addr: %q", addr)
	}

	var fetchedAddr string
	restore := stubCacheDeps(t,
		func(string) (*config.Config, error) { return &config.Config{}, nil },
		func(_ context.Context, a string) (*models.ClusterSnapshot, string, error) {
			fetchedAddr = a
			return &models.ClusterSnapshot{Timestamp: time.Now().UTC(), Status: models.SnapshotHealthy}, "daemon-cache", nil
		},
		func() (*models.ClusterSnapshot, error) { return nil, os.ErrNotExist },
		func() (*state.ClusterState, error) { return &state.ClusterState{}, nil },
		func() (*skills.Store, error) { return &skills.Store{}, nil },
	)
	defer restore()

	_, err := LoadCachedWithAddr(context.Background(), "")
	if err != nil {
		t.Fatalf("LoadCachedWithAddr: %v", err)
	}
	if fetchedAddr != want {
		t.Fatalf("LoadCachedWithAddr with empty addr called fetchDaemonSnapshot with %q, want %q", fetchedAddr, want)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestLoadCachedCallsStubbedSocketClientAndDoesNotOpenPort8080(t *testing.T) {
	var clientCalledAddr string
	var reqURL string

	prevClient := httpClientForAddr
	httpClientForAddr = func(addr string, timeout time.Duration) (*http.Client, string) {
		clientCalledAddr = addr
		return &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				reqURL = r.URL.String()
				snap := &models.ClusterSnapshot{
					Timestamp: time.Now().UTC(),
					Status:    models.SnapshotHealthy,
				}
				data, _ := json.Marshal(snap)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(data)),
					Header:     make(http.Header),
				}, nil
			}),
		}, "http://localhost"
	}
	defer func() { httpClientForAddr = prevClient }()

	snap, source, err := fetchDaemonHTTP(context.Background(), "")
	if err != nil {
		t.Fatalf("fetchDaemonHTTP: %v", err)
	}
	if snap == nil || source != "daemon-cache" {
		t.Fatalf("unexpected result: snap=%+v source=%q", snap, source)
	}

	wantSocket := persist.AxisPath("axis.sock")
	if clientCalledAddr != wantSocket {
		t.Fatalf("client called with %q, want socket %q", clientCalledAddr, wantSocket)
	}
	if strings.Contains(clientCalledAddr, "8080") || strings.Contains(reqURL, "8080") {
		t.Fatalf("socket client called with port 8080: addr=%q url=%q", clientCalledAddr, reqURL)
	}
}

func TestCachedSnapshotVantagePreservedAndSurfaced(t *testing.T) {
	cfg := &config.Config{
		Nodes: []config.NodeConfig{{Name: "node-a", Hostname: "node-a.example.com"}},
	}
	now := time.Now().UTC()

	// Case A: cached snapshot WITH vantage keeps it and surfaces it in badge
	snapWithVantage := &models.ClusterSnapshot{
		Timestamp: now,
		Status:    models.SnapshotHealthy,
		Vantage: &models.VantageInfo{
			NodeName:   "foundry",
			ObservedAt: now,
		},
	}

	restoreA := stubCacheDeps(t,
		func(string) (*config.Config, error) { return cfg, nil },
		func(context.Context, string) (*models.ClusterSnapshot, string, error) {
			return snapWithVantage, "daemon-cache", nil
		},
		func() (*models.ClusterSnapshot, error) { return nil, os.ErrNotExist },
		func() (*state.ClusterState, error) { return &state.ClusterState{}, nil },
		func() (*skills.Store, error) { return &skills.Store{}, nil },
	)

	rtA, err := LoadCached(context.Background())
	restoreA()
	if err != nil {
		t.Fatalf("LoadCached: %v", err)
	}
	if rtA.Snapshot.Vantage == nil || rtA.Snapshot.Vantage.NodeName != "foundry" {
		t.Fatalf("expected vantage foundry kept, got: %+v", rtA.Snapshot.Vantage)
	}
	var foundBadgeA bool
	for _, w := range rtA.Snapshot.Warnings {
		if w.Kind == "cache" && strings.Contains(w.Message, "vantage: foundry") {
			foundBadgeA = true
			break
		}
	}
	if !foundBadgeA {
		t.Fatalf("expected cache badge surfacing vantage foundry, got: %+v", rtA.Snapshot.Warnings)
	}

	// Case B: cached snapshot WITHOUT vantage keeps it nil and surfaces "unknown", not a guessed host
	snapWithoutVantage := &models.ClusterSnapshot{
		Timestamp: now,
		Status:    models.SnapshotHealthy,
		Vantage:   nil,
	}

	restoreB := stubCacheDeps(t,
		func(string) (*config.Config, error) { return cfg, nil },
		func(context.Context, string) (*models.ClusterSnapshot, string, error) {
			return snapWithoutVantage, "daemon-cache", nil
		},
		func() (*models.ClusterSnapshot, error) { return nil, os.ErrNotExist },
		func() (*state.ClusterState, error) { return &state.ClusterState{}, nil },
		func() (*skills.Store, error) { return &skills.Store{}, nil },
	)

	rtB, err := LoadCached(context.Background())
	restoreB()
	if err != nil {
		t.Fatalf("LoadCached: %v", err)
	}
	if rtB.Snapshot.Vantage != nil {
		t.Fatalf("expected nil vantage to remain nil (not guessed), got: %+v", rtB.Snapshot.Vantage)
	}
	var foundBadgeB bool
	for _, w := range rtB.Snapshot.Warnings {
		if w.Kind == "cache" && strings.Contains(w.Message, "vantage: unknown") {
			foundBadgeB = true
			break
		}
	}
	if !foundBadgeB {
		t.Fatalf("expected cache badge surfacing vantage: unknown, got: %+v", rtB.Snapshot.Warnings)
	}
}

func TestLoadCachedStaleWarningAndVantageBadge(t *testing.T) {
	cfg := &config.Config{
		Nodes: []config.NodeConfig{{Name: "node-a", Hostname: "node-a.example.com"}},
	}

	// Case A: Fresh snapshot (1 minute ago) has vantage badge with stale: false, and NO separate stale warning
	freshTime := time.Now().UTC().Add(-1 * time.Minute)
	freshSnap := &models.ClusterSnapshot{
		Timestamp: freshTime,
		Status:    models.SnapshotHealthy,
		Vantage:   &models.VantageInfo{NodeName: "node-a", ObservedAt: freshTime},
	}
	restoreFresh := stubCacheDeps(t,
		func(string) (*config.Config, error) { return cfg, nil },
		func(context.Context, string) (*models.ClusterSnapshot, string, error) {
			return freshSnap, "daemon-cache", nil
		},
		func() (*models.ClusterSnapshot, error) { return nil, os.ErrNotExist },
		func() (*state.ClusterState, error) { return &state.ClusterState{}, nil },
		func() (*skills.Store, error) { return &skills.Store{}, nil },
	)
	rtFresh, err := LoadCached(context.Background())
	restoreFresh()
	if err != nil {
		t.Fatalf("LoadCached: %v", err)
	}
	var foundFreshBadge bool
	var foundFreshStaleWarning bool
	for _, w := range rtFresh.Snapshot.Warnings {
		if w.Kind == "cache" && strings.Contains(w.Message, "stale: false") && strings.Contains(w.Message, "vantage: node-a") {
			foundFreshBadge = true
		}
		if w.Kind == "cache" && strings.Contains(w.Message, "is stale") {
			foundFreshStaleWarning = true
		}
	}
	if !foundFreshBadge {
		t.Fatalf("expected fresh vantage badge, got warnings: %+v", rtFresh.Snapshot.Warnings)
	}
	if foundFreshStaleWarning {
		t.Fatalf("fresh snapshot should not have stale warning, got: %+v", rtFresh.Snapshot.Warnings)
	}

	// Case B: Stale snapshot (15 minutes ago) has BOTH the vantage badge (stale: true) AND the stale warning
	staleTime := time.Now().UTC().Add(-15 * time.Minute)
	diskSnap := &models.ClusterSnapshot{
		Timestamp: staleTime,
		Status:    models.SnapshotHealthy,
		Nodes: []models.NodeFacts{
			{Name: "node-a", Status: models.StatusComplete},
		},
		Vantage: &models.VantageInfo{NodeName: "node-a", ObservedAt: staleTime},
	}

	restoreStale := stubCacheDeps(t,
		func(string) (*config.Config, error) { return cfg, nil },
		func(context.Context, string) (*models.ClusterSnapshot, string, error) {
			return nil, "", errors.New("connection refused")
		},
		func() (*models.ClusterSnapshot, error) {
			return diskSnap, nil
		},
		func() (*state.ClusterState, error) {
			return &state.ClusterState{}, nil
		},
		func() (*skills.Store, error) {
			return &skills.Store{}, nil
		},
	)
	defer restoreStale()

	rt, err := LoadCached(context.Background())
	if err != nil {
		t.Fatalf("LoadCached: %v", err)
	}

	foundVantageBadge := false
	foundStaleWarning := false
	for _, w := range rt.Snapshot.Warnings {
		if w.Kind == "cache" && strings.Contains(w.Message, "vantage: node-a") && strings.Contains(w.Message, "stale: true") {
			foundVantageBadge = true
		}
		if w.Kind == "cache" && strings.Contains(w.Message, "is stale") {
			foundStaleWarning = true
		}
	}
	if !foundVantageBadge {
		t.Fatalf("expected vantage badge with stale: true, got warnings: %+v", rt.Snapshot.Warnings)
	}
	if !foundStaleWarning {
		t.Fatalf("expected stale warning alongside vantage badge, got warnings: %+v", rt.Snapshot.Warnings)
	}
}

func TestLoadCachedReadsSocketDaemonWithUnixScheme(t *testing.T) {
	var clientCalledAddr string
	var reqURL string

	prevClient := httpClientForAddr
	httpClientForAddr = func(addr string, timeout time.Duration) (*http.Client, string) {
		clientCalledAddr = addr
		return &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				reqURL = r.URL.String()
				snap := &models.ClusterSnapshot{
					Timestamp: time.Now().UTC(),
					Status:    models.SnapshotHealthy,
				}
				data, _ := json.Marshal(snap)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(data)),
					Header:     make(http.Header),
				}, nil
			}),
		}, "http://localhost"
	}
	defer func() { httpClientForAddr = prevClient }()

	testAddr := "unix:///var/run/axis.sock"
	snap, source, err := fetchDaemonHTTP(context.Background(), testAddr)
	if err != nil {
		t.Fatalf("fetchDaemonHTTP: %v", err)
	}
	if snap == nil || source != "daemon-cache" {
		t.Fatalf("unexpected result: snap=%+v source=%q", snap, source)
	}
	if clientCalledAddr != testAddr {
		t.Fatalf("httpClientForAddr called with %q, want %q", clientCalledAddr, testAddr)
	}
	if reqURL != "http://localhost/snapshot" {
		t.Fatalf("request URL = %q, want http://localhost/snapshot", reqURL)
	}
}
