package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseMatchesLoadValidation(t *testing.T) {
	for i, data := range []string{
		"nodes:\n  - {name: alpha, hostname: alpha.example, ssh_user: test, stable_id: 'ABC'}\n",
		"nodes: [",
		"nodes: []\n",
		"nodes:\n  - {name: alpha, hostname: alpha.example, ssh_user: test, unknown: true}\n",
		"nodes:\n  - {name: alpha, hostname: alpha.example, ssh_user: test}\n---\n{}\n",
	} {
		path := filepath.Join(t.TempDir(), "nodes.yaml")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		loaded, loadErr := Load(path)
		parsed, parseErr := Parse([]byte(data))
		// The first fixture is valid; each remaining fixture violates a
		// distinct parser rule. Check these independently of Load's delegation.
		if wantErr := i != 0; (parseErr != nil) != wantErr {
			t.Errorf("fixture %d: Parse error=%v, want error=%v", i, parseErr, wantErr)
		}
		if i == 0 && (parsed == nil || len(parsed.Nodes) != 1 || parsed.Nodes[0].Name != "alpha" || parsed.Nodes[0].Hostname != "alpha.example" || parsed.Nodes[0].SSHUser != "test") {
			t.Errorf("valid node not preserved: %+v", parsed)
		}
		if (loadErr == nil) != (parseErr == nil) {
			t.Errorf("Load error=%v, Parse error=%v", loadErr, parseErr)
		}
		if !reflect.DeepEqual(loaded, parsed) {
			t.Errorf("Parse differs from Load: %+v / %+v", parsed, loaded)
		}
	}
}
