package config

import (
	"testing"
	"time"
)

func testSaveConfig(name string) *Config {
	return &Config{Nodes: []NodeConfig{{
		Name:       name,
		Hostname:   "localhost",
		SSHUser:    "operator",
		Role:       "primary",
		TimeoutSec: 10,
	}}}
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return ts.UTC()
}
