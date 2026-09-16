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
