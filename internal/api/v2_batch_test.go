package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/failures"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/state"
)

func batchNode(name string, freeMB int64) models.NodeFacts {
	return models.NodeFacts{Name: name, Status: models.StatusComplete,
		Resources: &models.Resources{RAMTotalMB: 16384, RAMAllocatableMB: freeMB, RAMFreeMB: freeMB}}
}

func runBatchPlace(t *testing.T, snap *models.ClusterSnapshot, body string) []V2BatchPlaceResult {
	t.Helper()
	return runBatchPlaceWithState(t, snap, &state.ClusterState{}, body)
}

func runBatchPlaceWithState(t *testing.T, snap *models.ClusterSnapshot, st *state.ClusterState, body string) []V2BatchPlaceResult {
	t.Helper()
	prev := loadLiveRuntime
	loadLiveRuntime = func(context.Context) (*runtimectx.Context, error) {
		return &runtimectx.Context{State: st}, nil
	}
	t.Cleanup(func() { loadLiveRuntime = prev })
	mux := http.NewServeMux()
	registerRoutes(mux, &fakeCache{meta: daemon.Metadata{Ready: true}, snap: snap}, "test-token")
	req := httptest.NewRequest(http.MethodPost, "/v2/batch/place", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Results []V2BatchPlaceResult `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.Results
}

func TestV2BatchPlaceSpreadSplitsAcrossIdenticalNodes(t *testing.T) {
	snap := &models.ClusterSnapshot{Status: models.SnapshotHealthy,
		Nodes: []models.NodeFacts{batchNode("node-a", 8000), batchNode("node-b", 8000)}}
	got := runBatchPlace(t, snap, `{"objective":"spread","tasks":[{"id":"1","description":"small task"},{"id":"2","description":"another task"},{"id":"3","description":"third task"}]}`)
	want := []string{"node-a", "node-b", "node-a"}
	if len(got) != len(want) {
		t.Fatalf("got %d results, want %d", len(got), len(want))
	}
	for i, r := range got {
		if !r.Decision.OK || r.Decision.Node != want[i] {
			t.Fatalf("task %s: got node=%q ok=%v, want %q", r.ID, r.Decision.Node, r.Decision.OK, want[i])
		}
	}
}

func TestV2BatchPlaceCapacityDoesNotOvercommitNode(t *testing.T) {
	reserve := placement.ReservationMBForRequirements(placement.InferRequirements("small task"))
	snap := &models.ClusterSnapshot{Status: models.SnapshotHealthy,
		Nodes: []models.NodeFacts{batchNode("node-a", reserve+reserve/2)}}
	got := runBatchPlace(t, snap, `{"tasks":[{"id":"1","description":"small task"},{"id":"2","description":"small task"}]}`)
	if len(got) != 2 || !got[0].Decision.OK || got[0].Decision.Node != "node-a" {
		t.Fatalf("first task should fit node-a: %+v", got)
	}
	if got[1].Decision.OK {
		t.Fatalf("second task over-commits node-a (reservable %dMB, %dMB per task): %+v", reserve+reserve/2, reserve, got[1].Decision)
	}
}

func TestV2BatchPlaceDoesNotMutateCachedSnapshot(t *testing.T) {
	snap := &models.ClusterSnapshot{Status: models.SnapshotHealthy,
		Nodes: []models.NodeFacts{batchNode("node-a", 8000), batchNode("node-b", 8000)}}
	before := make([]models.NodeFacts, len(snap.Nodes))
	for i, n := range snap.Nodes {
		res := *n.Resources
		n.Resources = &res
		before[i] = n
	}
	runBatchPlace(t, snap, `{"objective":"spread","tasks":[{"id":"1","description":"small task"},{"id":"2","description":"another task"}]}`)
	if !reflect.DeepEqual(before, snap.Nodes) {
		t.Fatalf("cached snapshot mutated:\nbefore %+v\nafter  %+v", before, snap.Nodes)
	}
}

// Overlay-shaped nodes: snapshotview sets top-level RAMAllocatableMB (reservable
// minus reserved), which ranking reads; Resources.RAMAllocatableMB is capacity.
func TestV2BatchPlaceCapacitySeesOverlayAllocatable(t *testing.T) {
	a, b := batchNode("node-a", 8000), batchNode("node-b", 8000)
	a.RAMAllocatableMB, b.RAMAllocatableMB = 8000, 7500
	snap := &models.ClusterSnapshot{Status: models.SnapshotHealthy, Nodes: []models.NodeFacts{a, b}}
	got := runBatchPlace(t, snap, `{"tasks":[{"id":"1","description":"small task"},{"id":"2","description":"small task"}]}`)
	if len(got) != 2 || got[0].Decision.Node != "node-a" || got[1].Decision.Node != "node-b" {
		t.Fatalf("want node-a then node-b (node-a allocatable drops below node-b), got %+v", got)
	}
}

func TestV2BatchPlaceHonorsRecordedFailures(t *testing.T) {
	snap := &models.ClusterSnapshot{Status: models.SnapshotHealthy,
		Nodes: []models.NodeFacts{batchNode("node-a", 8000), batchNode("node-b", 8000)}}
	st := &state.ClusterState{Nodes: map[string]state.NodeState{}, Failures: failures.NewStore()}
	st.Failures.Record(models.FailureExecCrash, models.FailureScope{Node: "node-a"}, "crashed repeatedly", nil)
	got := runBatchPlaceWithState(t, snap, st, `{"tasks":[{"id":"1","description":"small task"}]}`)
	if len(got) != 1 || !got[0].Decision.OK || got[0].Decision.Node != "node-b" {
		t.Fatalf("batch ignored node-a's recorded failure: %+v", got)
	}
}
