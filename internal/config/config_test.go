package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	os.WriteFile(path, []byte(`
nodes:
  - name: m3
    hostname: m3.local
    ssh_user: smithanator
    role: cortex
  - name: m1
    hostname: m1.local
    ssh_user: smithanator
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(cfg.Nodes))
	}
	if cfg.Nodes[0].Name != "m3" {
		t.Errorf("node[0].name: got %q, want m3", cfg.Nodes[0].Name)
	}
	if cfg.Nodes[1].Hostname != "m1.local" {
		t.Errorf("node[1].hostname: got %q, want m1.local", cfg.Nodes[1].Hostname)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/nodes.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	os.WriteFile(path, []byte(`{{{not yaml`), 0644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestValidate_EmptyNodes(t *testing.T) {
	cfg := &Config{Nodes: []NodeConfig{}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty nodes")
	}
}

func TestValidate_MissingName(t *testing.T) {
	cfg := &Config{Nodes: []NodeConfig{
		{Hostname: "x.local", SSHUser: "u"},
	}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestValidate_MissingHostname(t *testing.T) {
	cfg := &Config{Nodes: []NodeConfig{
		{Name: "n", SSHUser: "u"},
	}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing hostname")
	}
}

func TestValidate_MissingSSHUser(t *testing.T) {
	cfg := &Config{Nodes: []NodeConfig{
		{Name: "n", Hostname: "x.local"},
	}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing ssh_user")
	}
}

func TestEffectiveSSHPort_Default(t *testing.T) {
	n := &NodeConfig{}
	if p := n.EffectiveSSHPort(); p != 22 {
		t.Errorf("expected 22, got %d", p)
	}
}

func TestEffectiveSSHPort_Custom(t *testing.T) {
	n := &NodeConfig{SSHPort: 2222}
	if p := n.EffectiveSSHPort(); p != 2222 {
		t.Errorf("expected 2222, got %d", p)
	}
}

func TestEffectiveTimeout_Default(t *testing.T) {
	n := &NodeConfig{}
	if t2 := n.EffectiveTimeout(); t2 != 10 {
		t.Errorf("expected 10, got %d", t2)
	}
}

func TestEffectiveTimeout_Custom(t *testing.T) {
	n := &NodeConfig{TimeoutSec: 30}
	if t2 := n.EffectiveTimeout(); t2 != 30 {
		t.Errorf("expected 30, got %d", t2)
	}
}

func TestDefaultConfigPath(t *testing.T) {
	p := DefaultConfigPath()
	if p == "" {
		t.Fatal("expected non-empty path")
	}
	if filepath.Base(p) != "nodes.yaml" {
		t.Errorf("expected nodes.yaml, got %s", filepath.Base(p))
	}
}
