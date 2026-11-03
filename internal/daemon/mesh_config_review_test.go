package daemon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

func TestMeshReloadRejectsDefaultBeaconPortCollision(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := config.DefaultConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	nodes := "nodes:\n  - {name: local, hostname: localhost, ssh_user: test}\n  - {name: alpha, hostname: old.example, ssh_user: test}\n"
	if err := os.WriteFile(path, []byte(nodes+"discovery: {enabled: true, gossip_port: 42424, udp_port: 12345}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	d := NewDefault(time.Hour)
	before := d.Mesh().Peers()
	updated := strings.Replace(nodes, "old.example", "new.example", 1) + "discovery: {enabled: true, gossip_port: 42424}\n"
	if err := os.WriteFile(path, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	if err := d.applyMeshConfig([]byte(updated)); err == nil || !strings.Contains(err.Error(), "ports conflict") {
		t.Errorf("error=%v, want listener collision", err)
	}
	if got := d.Mesh().Peers(); !reflect.DeepEqual(got, before) {
		t.Errorf("conflicting configuration changed peers: %+v", got)
	}
}

func TestMeshReloadInvalidatesSnapshotForRejectedVersion(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid", true: "missing"}[missing], func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			path := config.DefaultConfigPath()
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			nodes := "nodes:\n  - {name: local, hostname: localhost, ssh_user: test}\n  - {name: alpha, hostname: alpha.example, ssh_user: test}\n"
			if err := os.WriteFile(path, []byte(nodes), 0600); err != nil {
				t.Fatal(err)
			}
			d := NewDefault(time.Hour)
			d.SetSnapshotPath("")
			var calls atomic.Int32
			observed := make(chan error, 8)
			d.collector = func(context.Context) (*models.ClusterSnapshot, error) {
				calls.Add(1)
				_, err := config.Load(path)
				select {
				case observed <- err:
				default:
				}
				if err != nil {
					return nil, err
				}
				return &models.ClusterSnapshot{Status: models.SnapshotHealthy}, nil
			}
			if err := d.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			<-observed
			peers := d.Mesh().Peers()
			retries := make(chan error, 16)
			previousReporter := reportWatchFingerprintError
			reportWatchFingerprintError = func(_, _ string, err error) {
				select {
				case retries <- err:
				default:
				}
			}
			t.Cleanup(func() { reportWatchFingerprintError = previousReporter })
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(func() { cancel(); d.WaitStopped(context.Background()) })
			d.WatchConfig(ctx, path)
			// Observe startup collection before replacing the file.
			select {
			case <-observed:
			case <-time.After(3 * time.Second):
				t.Fatal("watcher did not collect startup config")
			}
			if missing {
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, []byte("nodes: ["), 0600); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-observed:
				if err == nil {
					t.Error("collector did not observe rejected version")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("rejected version did not invalidate/refresh snapshot")
			}
			deadline := time.Now().Add(3 * time.Second)
			for d.Meta().Ready && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if _, ok := d.Snapshot(); ok {
				t.Error("rejected config left snapshot available")
			}
			if d.Meta().Ready {
				t.Error("rejected config left cache ready")
			}
			if got := d.Mesh().Peers(); !reflect.DeepEqual(got, peers) {
				t.Error("rejected config changed mesh peers")
			}
			beforeRetry := calls.Load()
			for i := 0; i < 3; i++ {
				select {
				case <-retries:
				case <-time.After(3 * time.Second):
					t.Fatal("rejected mesh version was not retried")
				}
			}
			if n := calls.Load(); n != beforeRetry {
				t.Errorf("unchanged rejected version refreshed %d more times", n-beforeRetry)
			}
		})
	}
}
