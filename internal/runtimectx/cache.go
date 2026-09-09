package runtimectx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/persist"
	"github.com/toasterbook88/axis/internal/publication"
	"github.com/toasterbook88/axis/internal/reservation"
)

// DefaultDaemonAddr returns the default address for the local daemon (the unix socket).
func DefaultDaemonAddr() string {
	return persist.AxisPath("axis.sock")
}

// StaleCacheThreshold defines the age at which a cached snapshot is considered stale.
const StaleCacheThreshold = 5 * time.Minute

var (
	defaultDaemonAddr   = DefaultDaemonAddr
	httpClientForAddr   = auth.HttpClientForAddrWithTimeout
	fetchDaemonSnapshot = fetchDaemonHTTP
	readDiskSnapshot    = loadDiskSnapshot
)

// LoadCached loads cluster context from the daemon snapshot publication,
// falling back to disk cache, and finally a bootstrap skeleton if no cache exists.
// It never performs a synchronous SSH discovery sweep.
func LoadCached(ctx context.Context) (*Context, error) {
	return LoadCachedWithAddr(ctx, "")
}

// LoadCachedWithAddr loads cluster context using a specific daemon address, or
// the default address if empty.
func LoadCachedWithAddr(ctx context.Context, daemonAddr string) (*Context, error) {
	if daemonAddr == "" {
		daemonAddr = defaultDaemonAddr()
	}

	cfg, err := loadConfig(config.DefaultConfigPath())
	if err != nil {
		return nil, err
	}

	snap, source, snapErr := resolveCachedSnapshot(ctx, daemonAddr, cfg)
	if snap == nil {
		snap = bootstrapSkeletonSnapshot(cfg)
		source = "bootstrap"
	}

	st, err := loadState()
	if err != nil && st == nil {
		return nil, err
	}

	ledger, ledgerErr := loadLedger()
	if ledgerErr != nil {
		return nil, fmt.Errorf("load reservation ledger: %w", ledgerErr)
	}
	if ledger != nil {
		for _, n := range snap.Nodes {
			if n.Resources != nil {
				ledger.SetNodeCapacity(n.Name, n.Resources.RAMTotalMB)
			}
		}
	}

	var ledgerEntries []reservation.Entry
	ledgerAvailable := ledger != nil
	if ledgerAvailable {
		ledgerEntries = ledger.Entries()
	}

	// If the snapshot has no publication envelope (e.g. older disk snapshot or bootstrap),
	// build a fallback envelope so consumers expecting Publication won't panic.
	// A bootstrap skeleton is NOT live runtime.
	if snap.Publication == nil {
		pubSource := source
		if pubSource == "" {
			pubSource = "bootstrap"
		} else if pubSource == "disk-cache" || pubSource == "daemon-cache" {
			pubSource = publication.SourceDaemonCache
		}
		assembledAt := publicationAssemblyTime(snap)
		if snap.Timestamp.IsZero() {
			models.AppendWarningIfMissing(snap, models.Warning{
				Kind:    "cache",
				Message: "publication assembly time fell back to read time because snapshot timestamp is zero",
			})
		}
		publicationEnvelope, publicationErr := publication.Build(
			pubSource,
			assembledAt,
			snap,
			ledgerEntries,
			ledgerAvailable,
			st,
			err,
		)
		if publicationErr != nil {
			return nil, publicationErr
		}
		snap.Publication = publicationEnvelope
	}

	applyReservationEntries(snap, st, ledgerEntries, ledgerAvailable)
	if err != nil {
		models.AppendWarningIfMissing(snap, models.Warning{
			Kind:    "state",
			Message: err.Error(),
		})
	}

	skillStore, skillErr := loadSkills()
	if skillErr != nil && skillStore == nil {
		return nil, skillErr
	}
	if skillErr != nil {
		models.AppendWarningIfMissing(snap, models.Warning{
			Kind:    "skills",
			Message: skillErr.Error(),
		})
	}

	if snapErr != nil {
		models.AppendWarningIfMissing(snap, models.Warning{
			Kind:    "cache",
			Message: snapErr.Error(),
		})
	}

	return &Context{
		Config:   cfg,
		Snapshot: snap,
		State:    st,
		Ledger:   ledger,
		Skills:   skillStore,
	}, nil
}

func resolveCachedSnapshot(ctx context.Context, daemonAddr string, cfg *config.Config) (*models.ClusterSnapshot, string, error) {
	// Priority 1: Daemon HTTP API
	snap, source, err := fetchDaemonSnapshot(ctx, daemonAddr)
	if err == nil && snap != nil {
		badgeCacheRead(snap, source)
		return snap, source, nil
	}

	// Priority 2: Disk snapshot
	diskSnap, diskErr := readDiskSnapshot()
	if diskErr == nil && diskSnap != nil {
		if diskSnap.Timestamp.IsZero() {
			cachePath := persist.AxisPath("snapshot.json")
			if info, statErr := os.Stat(cachePath); statErr == nil {
				diskSnap.Timestamp = info.ModTime().UTC()
			}
		}
		badgeCacheRead(diskSnap, "disk-cache")
		models.AppendWarningIfMissing(diskSnap, models.Warning{
			Kind:    "cache",
			Message: "daemon unreachable; snapshot loaded from disk cache",
		})
		return diskSnap, "disk-cache", nil
	}

	// Priority 3: Bootstrap skeleton
	return bootstrapSkeletonSnapshot(cfg), "bootstrap", fmt.Errorf("no snapshot cache available: daemon unreachable (%v) and no disk snapshot (%v)", err, diskErr)
}

func badgeCacheRead(snap *models.ClusterSnapshot, source string) {
	if snap == nil {
		return
	}
	var age time.Duration
	if !snap.Timestamp.IsZero() {
		age = time.Since(snap.Timestamp).Round(time.Second)
		if age < 0 {
			age = 0
		}
	}
	isStale := snap.Timestamp.IsZero() || time.Since(snap.Timestamp) > StaleCacheThreshold

	vantageName := "unknown"
	if snap.Vantage != nil && strings.TrimSpace(snap.Vantage.NodeName) != "" {
		vantageName = snap.Vantage.NodeName
	}

	badgeMsg := fmt.Sprintf("cache read (%s): age %s, stale: %t, vantage: %s", source, age, isStale, vantageName)
	models.AppendWarningIfMissing(snap, models.Warning{
		Kind:    "cache",
		Message: badgeMsg,
	})

	if isStale && !snap.Timestamp.IsZero() {
		models.AppendWarningIfMissing(snap, models.Warning{
			Kind:    "cache",
			Message: fmt.Sprintf("%s snapshot cache is stale (age: %s, collected: %s)", source, age, snap.Timestamp.Format(time.RFC3339)),
		})
	}
}

func fetchDaemonHTTP(ctx context.Context, addr string) (*models.ClusterSnapshot, string, error) {
	if addr == "" {
		addr = defaultDaemonAddr()
	}

	token, _ := auth.LoadOrGenerateToken()

	client, baseURLAddr := httpClientForAddr(addr, 3*time.Second)
	baseURL := strings.TrimRight(baseURLAddr, "/")
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/snapshot", nil)
	if err != nil {
		return nil, "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("daemon returned status %d", resp.StatusCode)
	}

	var snap models.ClusterSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return nil, "", fmt.Errorf("decoding snapshot: %w", err)
	}
	return &snap, "daemon-cache", nil
}

func loadDiskSnapshot() (*models.ClusterSnapshot, error) {
	cachePath := persist.AxisPath("snapshot.json")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, err
	}
	var snap models.ClusterSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("parsing disk snapshot: %w", err)
	}
	return &snap, nil
}

func publicationAssemblyTime(snap *models.ClusterSnapshot) time.Time {
	if snap != nil && !snap.Timestamp.IsZero() {
		return snap.Timestamp.UTC()
	}
	return time.Now().UTC()
}

func bootstrapSkeletonSnapshot(cfg *config.Config) *models.ClusterSnapshot {
	snap := &models.ClusterSnapshot{
		Timestamp: time.Now().UTC(),
		Status:    models.SnapshotDegraded,
	}
	if cfg != nil {
		for _, n := range cfg.Nodes {
			snap.Nodes = append(snap.Nodes, models.NodeFacts{
				Name:     n.Name,
				Hostname: n.Hostname,
				Role:     n.Role,
				Status:   models.StatusUnreachable,
			})
		}
	}
	models.AppendWarningIfMissing(snap, models.Warning{
		Kind:    "cache",
		Message: "no snapshot cache available (daemon unreachable and no disk snapshot; run 'axis daemon' or use --live)",
	})
	return snap
}
