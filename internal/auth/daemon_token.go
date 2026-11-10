package auth

import (
	"context"
	"fmt"
	"os"
	"time"
)

// DaemonTokenSource is the API token source `axis serve` authenticates
// against. It owns the whole decision of where the daemon's token comes from:
//
//   - AXIS_API_TOKEN, when set, is fixed for the daemon's lifetime.
//   - Otherwise ~/.axis/token is loaded (created on first start only) and
//     hot-reloaded by a FileToken poller until ctx ends.
//
// WaitStopped lets the caller drain the poller alongside the daemon's other
// background goroutines.
type DaemonTokenSource struct {
	TokenSource
	stopped chan struct{}
}

// NewDaemonTokenSource builds the daemon's token source and, for the file
// case, starts its poller. Call it before the API starts listening so a
// failure is reported before the daemon says it is up. The poller stops when
// ctx is cancelled.
func NewDaemonTokenSource(ctx context.Context) (*DaemonTokenSource, error) {
	return newDaemonTokenSource(ctx, tokenPollInterval)
}

func newDaemonTokenSource(ctx context.Context, interval time.Duration) (*DaemonTokenSource, error) {
	stopped := make(chan struct{})
	if v := os.Getenv(TokenEnvVar); v != "" {
		close(stopped) // nothing to run
		return &DaemonTokenSource{TokenSource: StaticToken(v), stopped: stopped}, nil
	}
	token, err := loadOrGenerateTokenFile()
	if err != nil {
		return nil, fmt.Errorf("loading API token: %w", err)
	}
	ft, err := NewFileToken(TokenPath(), token)
	if err != nil {
		return nil, err
	}
	ft.interval = interval
	go func() {
		defer close(stopped)
		ft.Run(ctx)
	}()
	return &DaemonTokenSource{TokenSource: ft, stopped: stopped}, nil
}

// WaitStopped blocks until the poller has exited (immediately for the
// env-var case) or ctx is done.
func (s *DaemonTokenSource) WaitStopped(ctx context.Context) {
	select {
	case <-s.stopped:
	case <-ctx.Done():
	}
}
