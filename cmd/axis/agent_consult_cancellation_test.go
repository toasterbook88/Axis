package main

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func TestConsultCloudResolverHonorsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		entered := make(chan struct{})
		release := make(chan struct{})
		defer close(release)
		loads := 0
		resolver := consultResolver(func(context.Context) []ModelChoice {
			return []ModelChoice{{ID: "cloud:example:seat-a", Model: "seat-a", Protocol: agent.ProtocolCloud, ProviderName: "example"}}
		}, func(ctx context.Context) (*runtimectx.Context, error) {
			loads++
			if loads == 1 {
				return &runtimectx.Context{}, nil
			}
			close(entered)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
				return nil, errors.New("test fixture released")
			}
		}, nil)
		done := make(chan error, 1)
		go func() {
			_, _, cleanup, err := resolver(ctx, "seat-a")
			if cleanup != nil {
				cleanup()
			}
			done <- err
		}()
		<-entered
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("resolver error = %v, want context.Canceled", err)
			}
		default:
			t.Fatal("cloud credential resolution remained blocked after cancellation")
		}
	})
}
