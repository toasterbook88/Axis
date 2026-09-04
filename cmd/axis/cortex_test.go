package main

import (
	"os"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
)

func TestResolveCortexNode(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		envVal   string
		wantName string
		wantOK   bool
	}{
		{
			name:   "nil config",
			cfg:    nil,
			wantOK: false,
		},
		{
			name: "no nodes",
			cfg: &config.Config{
				Nodes: []config.NodeConfig{},
			},
			wantOK: false,
		},
		{
			name: "resolved via role cortex",
			cfg: &config.Config{
				Nodes: []config.NodeConfig{
					{Name: "worker-1", Hostname: "192.168.1.10"},
					{Name: "brain", Hostname: "192.168.1.50", Role: "cortex"},
				},
			},
			wantName: "brain",
			wantOK:   true,
		},
		{
			name: "resolved via name cortex",
			cfg: &config.Config{
				Nodes: []config.NodeConfig{
					{Name: "worker-1", Hostname: "192.168.1.10"},
					{Name: "cortex", Hostname: "192.168.1.50"},
				},
			},
			wantName: "cortex",
			wantOK:   true,
		},
		{
			name: "fallback to foundry for legacy compatibility",
			cfg: &config.Config{
				Nodes: []config.NodeConfig{
					{Name: "worker-1", Hostname: "192.168.1.10"},
					{Name: "foundry", Hostname: "192.168.1.50"},
				},
			},
			wantName: "foundry",
			wantOK:   true,
		},
		{
			name:   "override via AXIS_CORTEX_NODE env var",
			envVal: "custom-node",
			cfg: &config.Config{
				Nodes: []config.NodeConfig{
					{Name: "foundry", Hostname: "192.168.1.50"},
					{Name: "custom-node", Hostname: "192.0.2.60"},
				},
			},
			wantName: "custom-node",
			wantOK:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envVal != "" {
				t.Setenv("AXIS_CORTEX_NODE", tc.envVal)
			} else {
				os.Unsetenv("AXIS_CORTEX_NODE")
			}

			got, ok := resolveCortexNode(tc.cfg)
			if ok != tc.wantOK {
				t.Fatalf("resolveCortexNode() ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got.Name != tc.wantName {
				t.Errorf("resolveCortexNode() name = %q, want %q", got.Name, tc.wantName)
			}
		})
	}
}
