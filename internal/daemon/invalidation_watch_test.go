package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

func TestRejectedConfigDuringActiveRefresh(t *testing.T) {
	for _, meshWatcher := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged_generic_watcher", true: "mesh_watcher"}[meshWatcher], func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			path := config.DefaultConfigPath()
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("nodes:\n  - {name: local, hostname: localhost, ssh_user: test}\n  - {name: alpha, hostname: alpha.example, ssh_user: test}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			started, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			d := NewDefault(time.Hour)
			d.SetSnapshotPath("")
			d.collector = func(ctx context.Context) (*models.ClusterSnapshot, error) {
				n := calls.Add(1)
				cfg, err := config.Load(path)
				if err != nil {
					return nil, err
				}
				if n == 1 {
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return &models.ClusterSnapshot{Timestamp: time.Now(), Status: models.SnapshotHealthy, Summary: models.ClusterSummary{TotalNodes: len(cfg.Nodes)}}, nil
			}
			prevPoll, prevReporter := watchConfigPollInterval, reportWatchFingerprintError
			watchConfigPollInterval = 10 * time.Millisecond
			rejected := make(chan struct{}, 1)
			reportWatchFingerprintError = func(_, _ string, _ error) {
				select {
				case rejected <- struct{}{}:
				default:
				}
			}
			t.Cleanup(func() { watchConfigPollInterval, reportWatchFingerprintError = prevPoll, prevReporter })
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(func() { cancel(); d.WaitStopped(context.Background()) })
			d.Start(ctx)
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("initial collection did not start")
			}
			if meshWatcher {
				if err := os.WriteFile(path, []byte("nodes: ["), 0600); err != nil {
					t.Fatal(err)
				}
				d.WatchConfig(ctx, path)
				select {
				case <-rejected:
				case <-time.After(3 * time.Second):
					t.Fatal("mesh watcher did not reject invalid config")
				}
			} else {
				initialized := make(chan struct{})
				var seen atomic.Bool
				d.watchFileWithFingerprint(ctx, path, RefreshTriggerConfigChange, func(path string) (configFingerprint, error) {
					fp, err := fingerprintFile(path)
					if !seen.Swap(true) {
						close(initialized)
					}
					return fp, err
				})
				select {
				case <-initialized:
				case <-time.After(3 * time.Second):
					t.Fatal("generic watcher not initialized")
				}
				if err := os.WriteFile(path, []byte("nodes: ["), 0600); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(3 * time.Second)
				for {
					d.pendingMu.Lock()
					queued := d.pendingTriggers[RefreshTriggerConfigChange]
					d.pendingMu.Unlock()
					if queued {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("invalid-config refresh not queued")
					}
					time.Sleep(time.Millisecond)
				}
			}
			close(release)
			deadline := time.Now().Add(3 * time.Second)
			for d.Meta().RefreshCount < 2 || d.Meta().LastError == "" {
				if time.Now().After(deadline) {
					t.Fatalf("invalid-config follow-up did not finish: %+v", d.Meta())
				}
				time.Sleep(time.Millisecond)
			}
			if snap, ok := d.Snapshot(); ok {
				t.Errorf("stale snapshot remains published after invalid config: nodes=%d ready=%v error=%s", snap.Summary.TotalNodes, d.Meta().Ready, d.Meta().LastError)
			}
		})
	}
}
