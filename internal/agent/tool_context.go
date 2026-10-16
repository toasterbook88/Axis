package agent

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

type RuntimeView struct {
	Config    *config.Config
	Snapshot  *models.ClusterSnapshot
	State     *state.ClusterState
	Ledger    *reservation.Ledger
	Skills    *skills.Store
	Knowledge *knowledge.ClusterKnowledge
}

// ToolContext holds runtime state available to tool executors. The current
// view is accessed atomically and reloaded on demand via the Reload callback.
type ToolContext struct {
	current atomic.Pointer[RuntimeView]
	Reload  func(context.Context) (*RuntimeView, error)
}

// NewToolContext creates a new ToolContext with an initial view.
func NewToolContext(initial *RuntimeView, reload func(context.Context) (*RuntimeView, error)) *ToolContext {
	if initial == nil {
		return nil
	}
	tc := &ToolContext{
		Reload: reload,
	}
	tc.current.Store(initial)
	return tc
}

// Current returns the current atomically replaceable RuntimeView.
func (tc *ToolContext) Current() *RuntimeView {
	if tc == nil {
		return nil
	}
	return tc.current.Load()
}

// ReloadCurrent reloads the current view using the Reload callback.
func (tc *ToolContext) ReloadCurrent(ctx context.Context) error {
	if tc == nil {
		return fmt.Errorf("nil tool context")
	}
	if tc.Reload == nil {
		return fmt.Errorf("no reload function defined")
	}
	view, err := tc.Reload(ctx)
	if err != nil {
		return err
	}
	if view == nil {
		return fmt.Errorf("reload returned nil view")
	}
	if view.Config == nil || view.Snapshot == nil || view.State == nil {
		return fmt.Errorf("reload returned incomplete view (Config, Snapshot, or State is nil)")
	}
	tc.current.Store(view)
	return nil
}

// GetView returns the current atomically replaceable RuntimeView.
func (tc *ToolContext) GetView() *RuntimeView {
	return tc.Current()
}
