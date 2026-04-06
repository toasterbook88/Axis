package main

import (
	"context"
	"fmt"
	"os"
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
			reportBackgroundRefreshError(surface, trigger, err)
		}
	}()
}
