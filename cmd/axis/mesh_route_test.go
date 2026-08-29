package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/mesh"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
)

// meshOnlyCache satisfies the api package's snapshot cache interface. It is a
// real cache handed to a real server, not a fake route.
type meshOnlyCache struct {
	snap *models.ClusterSnapshot
	mesh *mesh.Mesh
}

func (c *meshOnlyCache) Snapshot() (*models.ClusterSnapshot, bool) { return c.snap, c.snap != nil }
func (c *meshOnlyCache) Meta() daemon.Metadata                     { return daemon.Metadata{} }
func (c *meshOnlyCache) Ledger() *reservation.Ledger               { return nil }
func (c *meshOnlyCache) Mesh() *mesh.Mesh                          { return c.mesh }
func (c *meshOnlyCache) Invalidate()                               {}
func (c *meshOnlyCache) RefreshNow(context.Context) error          { return nil }

// TestFetchDaemonMeshAgainstProductionServer exercises the CLI's mesh path
// against api.ServeWithContext -- the same mux the daemon runs. The previous
// test served /mesh from an httptest handler the daemon never registered, so it
// passed while the live daemon answered 404.
func TestFetchDaemonMeshAgainstProductionServer(t *testing.T) {
	// macOS caps unix socket paths at 104 chars.
	dir, err := os.MkdirTemp("", "ax")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "s.sock")

	const token = "test-token"
	t.Setenv("AXIS_HOME", dir)
	t.Setenv("AXIS_API_TOKEN", token)

	m := mesh.New(mesh.Peer{Name: "self"}, mesh.Config{}, nil)
	m.AddSeed(mesh.Peer{Name: "samson", Hostname: "samson.local", StableID: "samson-1"})

	cache := &meshOnlyCache{snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy}, mesh: m}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- api.ServeWithContext(ctx, socketPath, cache, token, false) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.Dial("unix", socketPath); err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	peers, err := fetchDaemonMesh(context.Background(), socketPath)
	if err != nil {
		t.Fatalf("fetchDaemonMesh against production server: %v", err)
	}
	if len(peers) != 1 || peers[0].Name != "samson" {
		t.Fatalf("peers = %+v, want one peer named samson", peers)
	}

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("server returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
}
