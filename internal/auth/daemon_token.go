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
//
// Build it with NewDaemonTokenSource. The zero value has no source: Current
// and Refresh panic (failing closed; returning "" would make withAuthSource a
// pass-through), and WaitStopped returns at once because there is no poller.
type DaemonTokenSource struct {
	src     TokenSource
	stopped chan struct{} // nil: no poller was started
}

// Current returns the token in effect now.
func (s *DaemonTokenSource) Current() string { return s.src.Current() }

// Refresh re-checks the backing store and returns the token in effect now.
func (s *DaemonTokenSource) Refresh() string { return s.src.Refresh() }

// NewDaemonTokenSource builds the daemon's token source and, for the file
// case, starts its poller. Call it before the API starts listening so a
// failure is reported before the daemon says it is up. The poller stops when
// ctx is cancelled.
func NewDaemonTokenSource(ctx context.Context) (*DaemonTokenSource, error) {
	return newDaemonTokenSource(ctx, tokenPollInterval)
}

func newDaemonTokenSource(ctx context.Context, interval time.Duration) (*DaemonTokenSource, error) {
	if v := os.Getenv(TokenEnvVar); v != "" {
		// Used as given, exactly like LoadOrGenerateToken; not run through
		// parseToken, so existing non-hex AXIS_API_TOKEN values keep working.
		return &DaemonTokenSource{src: StaticToken(v)}, nil
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
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ft.Run(ctx)
	}()
	return &DaemonTokenSource{src: ft, stopped: stopped}, nil
}

// WaitStopped blocks until the poller has exited or ctx is done. It returns
// at once when no poller was started (the env-var case, or the zero value).
func (s *DaemonTokenSource) WaitStopped(ctx context.Context) {
	if s.stopped == nil {
		return
	}
	select {
	case <-s.stopped:
	case <-ctx.Done():
	}
}
