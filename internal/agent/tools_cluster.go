package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/transport"
)

// findNode looks up a node by name in the cluster config. Returns nil if the
// config is unavailable or the node is not found.
func findNode(nodes []config.NodeConfig, name string) *config.NodeConfig {
	for i := range nodes {
		if nodes[i].Name == name {
			return &nodes[i]
		}
	}
	return nil
}

// runRemote executes a single shell command on the named cluster node via SSH
// and returns its stdout. The SSHExecutor handles host-key verification and
// reuses the operator's ~/.ssh config/keys.
func runRemote(ctx context.Context, tc *ToolContext, nodeName, command string) (string, error) {
	if tc == nil {
		return "", fmt.Errorf("no tool context (cluster config unavailable)")
	}
	view := tc.Current()
	if view == nil || view.Config == nil {
		return "", fmt.Errorf("cluster config not loaded; run `axis status` first to refresh")
	}
	node := findNode(view.Config.Nodes, nodeName)
	if node == nil {
		available := nodeNames(view.Config.Nodes)
		return "", fmt.Errorf("node %q not found in cluster config (available: %s)", nodeName, available)
	}
	exec := transport.NewSSHExecutor(node.Hostname, node.EffectiveSSHPort(), node.SSHUser, node.EffectiveTimeout())
	if err := exec.Connect(ctx); err != nil {
		return "", fmt.Errorf("connect to %s (%s@%s): %w", nodeName, node.SSHUser, node.Hostname, err)
	}
	defer exec.Close()
	out, err := exec.Run(ctx, command)
	if err != nil {
		return "", fmt.Errorf("run on %s: %w", nodeName, err)
	}
	return out, nil
}

func nodeNames(nodes []config.NodeConfig) string {
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.Name)
	}
	return strings.Join(names, ", ")
}

// --- Tool: run_on_node ---

type runOnNodeArgs struct {
	Node    string `json:"node"`
	Command string `json:"command"`
}

func (r *ToolRegistry) registerRunOnNode(tc *ToolContext) {
	r.add("run_on_node",
		"Run a shell command on a named cluster node via SSH and return stdout. "+
			"Use this to run something on a specific remote computer (e.g. tests on nixos, a build on foundry). "+
			"Requires confirmation since it executes on a remote host. The node must be configured in nodes.yaml.",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"node":{"type":"string","description":"Cluster node name (from `+"`axis status`"+`)"},
				"command":{"type":"string","description":"Shell command to run on the node"}
			},
			"required":["node","command"]
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a runOnNodeArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for run_on_node: %w", err)
			}
			if a.Node == "" {
				return "", fmt.Errorf("run_on_node requires a non-empty \"node\" argument")
			}
			if a.Command == "" {
				return "", fmt.Errorf("run_on_node requires a non-empty \"command\" argument")
			}
			out, err := runRemote(ctx, tc, a.Node, a.Command)
			if err != nil {
				return "", err
			}
			if out == "" {
				return fmt.Sprintf("(no output from %s)", a.Node), nil
			}
			return out, nil
		},
	)
}

// --- Tool: remote_read_file ---

type remoteReadArgs struct {
	Node string `json:"node"`
	Path string `json:"path"`
}

func (r *ToolRegistry) registerRemoteReadFile(tc *ToolContext) {
	r.add("remote_read_file",
		"Read a file's contents on a remote cluster node via SSH (cat). Read-only, no confirmation. "+
			"Use this to inspect files that live on another computer (e.g. logs on foundry, configs on nixos).",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"node":{"type":"string","description":"Cluster node name"},
				"path":{"type":"string","description":"Absolute or relative path on the remote node"}
			},
			"required":["node","path"]
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a remoteReadArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for remote_read_file: %w", err)
			}
			if a.Node == "" || a.Path == "" {
				return "", fmt.Errorf("remote_read_file requires \"node\" and \"path\"")
			}
			// Truncate large remote reads.
			cmd := fmt.Sprintf("head -c 32000 %s", shellQuote(a.Path))
			out, err := runRemote(ctx, tc, a.Node, cmd)
			if err != nil {
				return "", err
			}
			return out, nil
		},
	)
}

// --- Tool: remote_grep ---

type remoteGrepArgs struct {
	Node  string `json:"node"`
	Query string `json:"query"`
	Path  string `json:"path,omitempty"`
}

func (r *ToolRegistry) registerRemoteGrep(tc *ToolContext) {
	r.add("remote_grep",
		"Search recursively for a pattern in text files on a remote cluster node via SSH (grep -rn). "+
			"Read-only, no confirmation. Skips binary files and hidden directories.",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"node":{"type":"string","description":"Cluster node name"},
				"query":{"type":"string","description":"Search term or pattern"},
				"path":{"type":"string","description":"Directory or file to search (defaults to the node's home)"}
			},
			"required":["node","query"]
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a remoteGrepArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for remote_grep: %w", err)
			}
			if a.Node == "" || a.Query == "" {
				return "", fmt.Errorf("remote_grep requires \"node\" and \"query\"")
			}
			searchPath := a.Path
			if searchPath == "" {
				searchPath = "."
			}
			// -I skips binary files; --include limits to text-like files; head caps output.
			cmd := fmt.Sprintf("grep -rnI --include='*.go' --include='*.py' --include='*.js' --include='*.ts' --include='*.md' --include='*.txt' --include='*.yaml' --include='*.yml' --include='*.json' --include='*.sh' -- %s %s 2>/dev/null | head -50",
				shellQuote(a.Query), shellQuote(searchPath))
			out, err := runRemote(ctx, tc, a.Node, cmd)
			if err != nil {
				return "", err
			}
			if out == "" {
				return fmt.Sprintf("No matches for %q on %s.", a.Query, a.Node), nil
			}
			return out, nil
		},
	)
}

// --- Tool: remote_list ---

type remoteListArgs struct {
	Node string `json:"node"`
	Path string `json:"path,omitempty"`
}

func (r *ToolRegistry) registerRemoteList(tc *ToolContext) {
	r.add("remote_list",
		"List the contents of a directory on a remote cluster node via SSH (ls). Read-only, no confirmation.",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"node":{"type":"string","description":"Cluster node name"},
				"path":{"type":"string","description":"Directory path on the remote node (defaults to home)"}
			},
			"required":["node"]
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a remoteListArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for remote_list: %w", err)
			}
			if a.Node == "" {
				return "", fmt.Errorf("remote_list requires \"node\"")
			}
			path := a.Path
			if path == "" {
				path = "."
			}
			cmd := fmt.Sprintf("ls -1F %s 2>/dev/null | head -100", shellQuote(path))
			out, err := runRemote(ctx, tc, a.Node, cmd)
			if err != nil {
				return "", err
			}
			if out == "" {
				return fmt.Sprintf("(empty or missing directory %s on %s)", path, a.Node), nil
			}
			return out, nil
		},
	)
}

// shellQuote single-quotes a string for safe interpolation into a remote shell
// command. Single quotes inside the string are escaped via the standard
// '\” idiom.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
