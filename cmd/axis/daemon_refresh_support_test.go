package main

import (
	"context"
	"errors"
	"net/url"
	"syscall"
	"testing"
	"time"
)

func TestScheduleBestEffortDaemonRefreshReportsSignalError(t *testing.T) {
	errCh := make(chan struct {
		surface string
		trigger string
		err     error
	}, 1)

	prevReport := reportBackgroundRefreshError
	reportBackgroundRefreshError = func(surface, trigger string, err error) {
		errCh <- struct {
			surface string
			trigger string
			err     error
		}{surface: surface, trigger: trigger, err: err}
	}
	defer func() { reportBackgroundRefreshError = prevReport }()

	wantErr := errors.New("daemon unavailable")
	scheduleBestEffortDaemonRefresh("agent", "execution-reserved", func(context.Context, string) error {
		return wantErr
	})

	select {
	case got := <-errCh:
		if got.surface != "agent" {
			t.Fatalf("surface = %q, want agent", got.surface)
		}
		if got.trigger != "execution-reserved" {
			t.Fatalf("trigger = %q, want execution-reserved", got.trigger)
		}
		if !errors.Is(got.err, wantErr) {
			t.Fatalf("err = %v, want %v", got.err, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("expected background refresh error to be reported")
	}
}

func TestScheduleBestEffortDaemonRefreshSuppressesMissingDaemonSocket(t *testing.T) {
	errCh := make(chan struct{}, 1)

	prevReport := reportBackgroundRefreshError
	reportBackgroundRefreshError = func(surface, trigger string, err error) {
		errCh <- struct{}{}
	}
	defer func() { reportBackgroundRefreshError = prevReport }()

	scheduleBestEffortDaemonRefresh("task-run", "execution-finished", func(context.Context, string) error {
		return &url.Error{Op: "Post", URL: "http://localhost/refresh", Err: syscall.ENOENT}
	})

	select {
	case <-errCh:
		t.Fatal("expected missing daemon socket error to be suppressed")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestScheduleBestEffortDaemonRefreshSuppressesConnectionRefused(t *testing.T) {
	errCh := make(chan struct{}, 1)

	prevReport := reportBackgroundRefreshError
	reportBackgroundRefreshError = func(surface, trigger string, err error) {
		errCh <- struct{}{}
	}
	defer func() { reportBackgroundRefreshError = prevReport }()

	scheduleBestEffortDaemonRefresh("agent", "execution-reserved", func(context.Context, string) error {
		return &url.Error{Op: "Post", URL: "http://localhost/refresh", Err: syscall.ECONNREFUSED}
	})

	select {
	case <-errCh:
		t.Fatal("expected connection refused to be suppressed")
	case <-time.After(200 * time.Millisecond):
	}
}
