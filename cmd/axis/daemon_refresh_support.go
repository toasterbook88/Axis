package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

var reportBackgroundRefreshError = func(surface, trigger string, err error) {
	fmt.Fprintf(os.Stderr, "axis: %s daemon refresh signal (%s) failed: %v\n", surface, trigger, err)
}

func scheduleBestEffortDaemonRefresh(surface, trigger string, signal func(context.Context, string) error) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := signal(ctx, trigger); err != nil {
			if suppressBackgroundRefreshError(err) {
				return
			}
			reportBackgroundRefreshError(surface, trigger, err)
		}
	}()
}

func suppressBackgroundRefreshError(err error) bool {
	// Missing daemon socket or a refused connection is expected when axis serve
	// is not running; avoid noisy stderr output for that case.
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return suppressBackgroundRefreshError(urlErr.Err)
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such file or directory") || strings.Contains(msg, "connection refused")
}
