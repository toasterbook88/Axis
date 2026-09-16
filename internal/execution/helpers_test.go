package execution

import (
	"context"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/events"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

type stubRemoteExecutor struct {
	runFunc func(context.Context, string) (string, error)
}

func (s *stubRemoteExecutor) Run(ctx context.Context, cmd string) (string, error) {
	return s.runFunc(ctx, cmd)
}
func (s *stubRemoteExecutor) Close() error { return nil }

func testGuardedRuntime(t *testing.T, nodes []models.NodeFacts) *runtimectx.Context {
	t.Cleanup(func() {
		if err := events.FlushEvents(5 * time.Second); err != nil {
			t.Errorf("FlushEvents cleanup: %v", err)
		}
	})
	cfgNodes := make([]config.NodeConfig, 0, len(nodes))
	for _, node := range nodes {
		cfgNodes = append(cfgNodes, config.NodeConfig{
			Name:     node.Name,
			Hostname: node.Hostname,
			SSHUser:  "me",
		})
	}

	return &runtimectx.Context{
		Config: &config.Config{Nodes: cfgNodes},
		Snapshot: &models.ClusterSnapshot{
			Status:  models.SnapshotHealthy,
			Nodes:   nodes,
			Summary: models.ClusterSummary{TotalNodes: len(nodes)},
		},
		State:  &state.ClusterState{Nodes: map[string]state.NodeState{}},
		Skills: &skills.Store{},
		Ledger: func() *reservation.Ledger {
			l := reservation.NewLedger(reservation.DefaultLimits(), nil)
			for _, n := range nodes {
				if n.Resources != nil {
					l.SetNodeCapacity(n.Name, n.Resources.RAMTotalMB)
				}
			}
			return l
		}(),
	}
}
