package daemon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/mesh"
	"github.com/toasterbook88/axis/internal/models"
)

func TestWatchConfigReconcilesMesh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(os.Getenv("HOME"), ".axis", "nodes.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	initial := "nodes:\n  - {name: local, hostname: localhost, ssh_user: test}\n  - {name: alpha, hostname: old.example, ssh_user: test}\n  - {name: removed, hostname: removed.example, ssh_user: test}\n"
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	d := NewDefault(time.Hour)
	d.collector = func(context.Context) (*models.ClusterSnapshot, error) { return &models.ClusterSnapshot{}, nil }
	d.SetSnapshotPath("")
	errorsSeen := make(chan error, 1)
	previousReporter := reportWatchFingerprintError
	reportWatchFingerprintError = func(_, _ string, err error) {
		select {
		case errorsSeen <- err:
		default:
		}
	}
	t.Cleanup(func() { reportWatchFingerprintError = previousReporter })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); d.WaitStopped(context.Background()) })
	d.WatchConfig(ctx, path)
	writeVersion := func(contents string) {
		t.Helper()
		if err := os.WriteFile(path+".next", []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".next", path); err != nil {
			t.Fatal(err)
		}
	}
	// The watcher also inspects the file immediately, so replacement before its
	// first scheduled poll must not hide a configuration change.
	updated := "nodes:\n  - {name: local, hostname: localhost, ssh_user: test}\n  - {name: alpha, hostname: new.example, ssh_user: test}\n  - {name: added, hostname: added.example, ssh_user: test}\n"
	writeVersion(updated)
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		peers := d.Mesh().Peers()
		if len(peers) == 2 && peers[0].Name == "added" && peers[1].Name == "alpha" && peers[1].Hostname == "new.example" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("mesh did not apply added/updated/removed peers: %+v", peers)
		case <-tick.C:
		}
	}
	writeVersion("nodes: [")
	select {
	case <-errorsSeen:
	case <-time.After(3 * time.Second):
		t.Fatal("watcher did not inspect invalid configuration")
	}
	peers := d.Mesh().Peers()
	if len(peers) != 2 || peers[1].Hostname != "new.example" || peers[1].State != mesh.PeerTrusted {
		t.Fatalf("invalid config changed mesh: %+v", peers)
	}
	writeVersion(initial)
	deadline = time.After(3 * time.Second)
	for {
		peers = d.Mesh().Peers()
		if len(peers) == 2 && peers[0].Name == "alpha" && peers[0].Hostname == "old.example" && peers[1].Name == "removed" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("valid config after invalid version not applied: %+v", peers)
		case <-tick.C:
		}
	}
}

func TestApplyMeshConfigKeepsPeersWhenRestartIsRequired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(os.Getenv("HOME"), ".axis", "nodes.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	nodes := "nodes:\n  - {name: local, hostname: localhost, ssh_user: test}\n  - {name: alpha, hostname: alpha.example, ssh_user: test}\n"
	if err := os.WriteFile(path, []byte(nodes), 0600); err != nil {
		t.Fatal(err)
	}
	d := NewDefault(time.Hour)
	before := d.Mesh().Peers()
	for _, version := range []string{
		nodes + "discovery: {enabled: false}\n",
		nodes + "discovery: {enabled: true, gossip_port: 12345}\n",
		nodes + "discovery: {enabled: true, beacon_interval_sec: 10}\n",
		nodes + "discovery: {enabled: true, secret: test-only}\n",
		strings.Replace(nodes, "name: local", "name: renamed", 1),
	} {
		if err := os.WriteFile(path, []byte(version), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := d.applyMeshConfig([]byte(version)); err == nil || !strings.Contains(err.Error(), "restart required") {
			t.Errorf("runtime change error=%v, want restart required", err)
		}
		if got := d.Mesh().Peers(); !reflect.DeepEqual(before, got) {
			t.Errorf("restart-required config changed peers: %+v", got)
		}
	}
}
