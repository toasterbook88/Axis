// Package facts is STABLE — local and remote cluster fact collection.
// It is part of the stable operator path.
package facts

import (
	"context"

	"github.com/toasterbook88/axis/internal/models"
)

// Collector collects facts from a node.
// Implementations: LocalCollector (direct), RemoteCollector (SSH-based, temporary).
type Collector interface {
	Collect(ctx context.Context) (*models.NodeFacts, error)
}
