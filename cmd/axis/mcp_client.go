package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/fatih/color"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/ui"
)

var loadMCPClientConfig = config.Load

func mcpClientCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "client",
		Short: "Connect to and query external MCP servers",
		Long:  "Discover, inspect, and call tools across configured MCP servers. Servers are defined in ~/.axis/nodes.yaml under mcp_servers.",
	}
	cmd.AddCommand(mcpClientListCmd())
	cmd.AddCommand(mcpClientToolsCmd())
	cmd.AddCommand(mcpClientCallCmd())
	cmd.AddCommand(mcpClientResourcesCmd())
	cmd.AddCommand(mcpClientReadCmd())
	cmd.AddCommand(mcpClientPromptsCmd())
	cmd.AddCommand(mcpClientGetPromptCmd())
	cmd.AddCommand(mcpClientSearchCmd())
	cmd.AddCommand(mcpClientBatchCmd())
	cmd.AddCommand(mcpClientInteractiveCmd())
	return cmd
}

func mcpClientListCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured MCP servers and connection status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMCPClientList(cmd.Context(), cmd.OutOrStdout(), format)
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	return cmd
}

func runMCPClientList(ctx context.Context, out io.Writer, format string) error {
	cfg, err := loadMCPClientConfig(config.DefaultConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if len(cfg.MCPServers) == 0 {
		fmt.Fprintln(out, "No MCP servers configured.")
		fmt.Fprintf(out, "Add them to %s under mcp_servers:\n", config.DefaultConfigPath())
		return nil
	}

	reg := mcpclient.NewRegistry()

	reg.ConnectAll(ctx, cfg)
	defer reg.Close()

	if format == "json" {
		return printMCPClientListJSON(out, reg)
	}
	return printMCPClientListText(out, reg)
}

func printMCPClientListText(out io.Writer, reg *mcpclient.Registry) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", "NAME", "TRANSPORT", "STATUS", "TOOLS", "RESOURCES")
	for _, name := range reg.Names() {
		sc := reg.Get(name)
		status := color.GreenString("connected")
		if !sc.Connected() {
			status = color.RedString("error")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\n", name, sc.Transport, status, sc.ToolCount(), sc.ResourceCount())
	}
	return tw.Flush()
}

func printMCPClientListJSON(out io.Writer, reg *mcpclient.Registry) error {
	type serverRow struct {
		Name         string `json:"name"`
		Transport    string `json:"transport"`
		Connected    bool   `json:"connected"`
		Error        string `json:"error,omitempty"`
		Tools        int    `json:"tools"`
		Resources    int    `json:"resources"`
		Prompts      int    `json:"prompts"`
		Calls        int64  `json:"calls,omitempty"`
		Errors       int64  `json:"errors,omitempty"`
		AvgLatencyMs int64  `json:"avg_latency_ms,omitempty"`
		UptimeSec    int64  `json:"uptime_sec,omitempty"`
	}
	var rows []serverRow
	for _, name := range reg.Names() {
		sc := reg.Get(name)
		r := serverRow{
			Name:      name,
			Transport: sc.Transport,
			Connected: sc.Connected(),
			Tools:     sc.ToolCount(),
			Resources: sc.ResourceCount(),
			Prompts:   len(sc.CachedPrompts()),
		}
		if sc.Err != nil {
			r.Error = sc.Err.Error()
		}
		calls, errs, avgLat, uptime := sc.Metrics()
		r.Calls = calls
		r.Errors = errs
		if avgLat > 0 {
			r.AvgLatencyMs = avgLat.Milliseconds()
		}
		if uptime > 0 {
			r.UptimeSec = int64(uptime.Seconds())
		}
		rows = append(rows, r)
	}
	return printOutput(out, rows, "json")
}

func mcpClientToolsCmd() *cobra.Command {
	var server string
	var format string
	cmd := &cobra.Command{
		Use:   "tools",
		Short: "List tools from connected MCP servers",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMCPClientTools(cmd.Context(), cmd.OutOrStdout(), server, format)
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "Filter to a specific server")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	return cmd
}

func runMCPClientTools(ctx context.Context, out io.Writer, server, format string) error {
	cfg, err := loadMCPClientConfig(config.DefaultConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	reg := mcpclient.NewRegistry()

	reg.ConnectAll(ctx, cfg)
	defer reg.Close()

	if server != "" {
		sc := reg.Get(server)
		if sc == nil {
			return fmt.Errorf("server %q not configured", server)
		}
		if !sc.Connected() {
			return fmt.Errorf("server %q not connected: %v", server, sc.Err)
		}
		if format == "json" {
			return printOutput(out, sc.Tools, "json")
		}
		for _, t := range sc.Tools {
			fmt.Fprintf(out, "%s\t%s\n", t.Name, t.Description)
		}
		return nil
	}

	tools := reg.ListAllTools()
	if format == "json" {
		type entry struct {
			Server      string `json:"server"`
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
		}
		var entries []entry
		for _, te := range tools {
			entries = append(entries, entry{Server: te.Server, Name: te.Tool.Name, Description: te.Tool.Description})
		}
		return printOutput(out, entries, "json")
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\t%s\t%s\n", "SERVER", "NAME", "DESCRIPTION")
	for _, te := range tools {
		desc := te.Tool.Description
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", te.Server, te.Tool.Name, desc)
	}
	return tw.Flush()
}

func mcpClientCallCmd() *cobra.Command {
	var pretty bool
	var autoRoute bool
	cmd := &cobra.Command{
		Use:   "call [<server>] <tool> [json-args]",
		Short: "Call a tool on a specific MCP server (or auto-route with --auto-route)",
		Args: func(cmd *cobra.Command, args []string) error {
			if autoRoute {
				if len(args) < 1 || len(args) > 2 {
					return fmt.Errorf("with --auto-route, expected 1-2 args: <tool> [json-args]")
				}
				return nil
			}
			if len(args) < 2 || len(args) > 3 {
				return fmt.Errorf("expected 2-3 args: <server> <tool> [json-args]")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if autoRoute {
				toolName := args[0]
				var rawArgs string
				if len(args) > 1 {
					rawArgs = args[1]
				}
				return runMCPClientCallAutoRoute(cmd.Context(), cmd.OutOrStdout(), toolName, rawArgs, pretty)
			}
			serverName := args[0]
			toolName := args[1]
			var rawArgs string
			if len(args) > 2 {
				rawArgs = args[2]
			}
			return runMCPClientCall(cmd.Context(), cmd.OutOrStdout(), serverName, toolName, rawArgs, pretty)
		},
	}
	cmd.Flags().BoolVar(&pretty, "pretty", false, "Pretty-print JSON output")
	cmd.Flags().BoolVar(&autoRoute, "auto-route", false, "Auto-route tool call to best available server")
	return cmd
}

func runMCPClientCallAutoRoute(ctx context.Context, out io.Writer, toolName, rawArgs string, pretty bool) error {
	cfg, err := loadMCPClientConfig(config.DefaultConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	reg := mcpclient.NewRegistry()

	reg.ConnectAll(ctx, cfg)
	defer reg.Close()

	args, err := mcpclient.ParseArgs(rawArgs)
	if err != nil {
		return err
	}

	result := reg.CallToolAutoRoute(ctx, toolName, args)
	if result.Err != nil {
		return fmt.Errorf("tool call failed: %w", result.Err)
	}

	for _, content := range result.Result.Content {
		if tc, ok := content.(mcp.TextContent); ok {
			fmt.Fprintln(out, tc.Text)
		} else if ic, ok := content.(mcp.ImageContent); ok {
			fmt.Fprintf(out, "[image: %s %d bytes]\n", ic.MIMEType, len(ic.Data))
		} else {
			b, _ := json.MarshalIndent(content, "", "  ")
			fmt.Fprintln(out, string(b))
		}
	}
	return nil
}

func runMCPClientCall(ctx context.Context, out io.Writer, serverName, toolName, rawArgs string, pretty bool) error {
	cfg, err := loadMCPClientConfig(config.DefaultConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	reg := mcpclient.NewRegistry()

	reg.ConnectAll(ctx, cfg)
	defer reg.Close()

	args, err := mcpclient.ParseArgs(rawArgs)
	if err != nil {
		return err
	}

	result := reg.CallTool(ctx, serverName, toolName, args)
	if result.Err != nil {
		return fmt.Errorf("tool call failed: %w", result.Err)
	}

	for _, content := range result.Result.Content {
		if tc, ok := content.(mcp.TextContent); ok {
			fmt.Fprintln(out, tc.Text)
		} else if ic, ok := content.(mcp.ImageContent); ok {
			fmt.Fprintf(out, "[image: %s %d bytes]\n", ic.MIMEType, len(ic.Data))
		} else {
			b, _ := json.MarshalIndent(content, "", "  ")
			fmt.Fprintln(out, string(b))
		}
	}
	return nil
}

func mcpClientResourcesCmd() *cobra.Command {
	var server string
	var format string
	cmd := &cobra.Command{
		Use:   "resources",
		Short: "List resources from connected MCP servers",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMCPClientResources(cmd.Context(), cmd.OutOrStdout(), server, format)
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "Filter to a specific server")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	return cmd
}

func runMCPClientResources(ctx context.Context, out io.Writer, server, format string) error {
	cfg, err := loadMCPClientConfig(config.DefaultConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	reg := mcpclient.NewRegistry()

	reg.ConnectAll(ctx, cfg)
	defer reg.Close()

	if server != "" {
		sc := reg.Get(server)
		if sc == nil {
			return fmt.Errorf("server %q not configured", server)
		}
		if !sc.Connected() {
			return fmt.Errorf("server %q not connected: %v", server, sc.Err)
		}
		if format == "json" {
			return printOutput(out, sc.Resources, "json")
		}
		for _, r := range sc.Resources {
			fmt.Fprintf(out, "%s\t%s\n", r.URI, r.Name)
		}
		return nil
	}

	resources := reg.ListAllResources()
	if format == "json" {
		type entry struct {
			Server string `json:"server"`
			URI    string `json:"uri"`
			Name   string `json:"name,omitempty"`
		}
		var entries []entry
		for _, re := range resources {
			entries = append(entries, entry{Server: re.Server, URI: re.Resource.URI, Name: re.Resource.Name})
		}
		return printOutput(out, entries, "json")
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\t%s\t%s\n", "SERVER", "URI", "NAME")
	for _, re := range resources {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", re.Server, re.Resource.URI, re.Resource.Name)
	}
	return tw.Flush()
}

func mcpClientReadCmd() *cobra.Command {
	var pretty bool
	cmd := &cobra.Command{
		Use:   "read <server> <uri>",
		Short: "Read a resource from a specific MCP server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMCPClientRead(cmd.Context(), cmd.OutOrStdout(), args[0], args[1], pretty)
		},
	}
	cmd.Flags().BoolVar(&pretty, "pretty", false, "Pretty-print JSON output")
	return cmd
}

func runMCPClientRead(ctx context.Context, out io.Writer, serverName, uri string, pretty bool) error {
	cfg, err := loadMCPClientConfig(config.DefaultConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	reg := mcpclient.NewRegistry()

	reg.ConnectAll(ctx, cfg)
	defer reg.Close()

	res, err := reg.ReadResource(ctx, serverName, uri)
	if err != nil {
		return err
	}

	for _, content := range res.Contents {
		if tc, ok := content.(mcp.TextResourceContents); ok {
			fmt.Fprintln(out, tc.Text)
		} else if bc, ok := content.(mcp.BlobResourceContents); ok {
			fmt.Fprintf(out, "[blob: %s %d bytes]\n", bc.MIMEType, len(bc.Blob))
		} else {
			b, _ := json.MarshalIndent(content, "", "  ")
			fmt.Fprintln(out, string(b))
		}
	}
	return nil
}

func mcpClientPromptsCmd() *cobra.Command {
	var server string
	var format string
	cmd := &cobra.Command{
		Use:   "prompts",
		Short: "List prompts from connected MCP servers",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMCPClientPrompts(cmd.Context(), cmd.OutOrStdout(), server, format)
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "Filter to a specific server")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	return cmd
}

func runMCPClientPrompts(ctx context.Context, out io.Writer, server, format string) error {
	cfg, err := loadMCPClientConfig(config.DefaultConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	reg := mcpclient.NewRegistry()

	reg.ConnectAll(ctx, cfg)
	defer reg.Close()

	if server != "" {
		sc := reg.Get(server)
		if sc == nil {
			return fmt.Errorf("server %q not configured", server)
		}
		if !sc.Connected() {
			return fmt.Errorf("server %q not connected: %v", server, sc.Err)
		}
		if format == "json" {
			return printOutput(out, sc.Prompts, "json")
		}
		for _, p := range sc.Prompts {
			fmt.Fprintf(out, "%s\t%s\n", p.Name, p.Description)
		}
		return nil
	}

	prompts := reg.ListAllPrompts()
	if format == "json" {
		type entry struct {
			Server      string `json:"server"`
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
		}
		var entries []entry
		for _, pe := range prompts {
			entries = append(entries, entry{Server: pe.Server, Name: pe.Prompt.Name, Description: pe.Prompt.Description})
		}
		return printOutput(out, entries, "json")
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\t%s\t%s\n", "SERVER", "NAME", "DESCRIPTION")
	for _, pe := range prompts {
		desc := pe.Prompt.Description
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", pe.Server, pe.Prompt.Name, desc)
	}
	return tw.Flush()
}

func mcpClientGetPromptCmd() *cobra.Command {
	var pretty bool
	cmd := &cobra.Command{
		Use:   "get-prompt <server> <name> [json-args]",
		Short: "Fetch a prompt from a specific MCP server",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			serverName := args[0]
			promptName := args[1]
			var rawArgs string
			if len(args) > 2 {
				rawArgs = args[2]
			}
			return runMCPClientGetPrompt(cmd.Context(), cmd.OutOrStdout(), serverName, promptName, rawArgs, pretty)
		},
	}
	cmd.Flags().BoolVar(&pretty, "pretty", false, "Pretty-print JSON output")
	return cmd
}

func runMCPClientGetPrompt(ctx context.Context, out io.Writer, serverName, promptName, rawArgs string, pretty bool) error {
	cfg, err := loadMCPClientConfig(config.DefaultConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	reg := mcpclient.NewRegistry()

	reg.ConnectAll(ctx, cfg)
	defer reg.Close()

	args, err := mcpclient.ParseArgs(rawArgs)
	if err != nil {
		return err
	}

	res, err := reg.GetPrompt(ctx, serverName, promptName, args)
	if err != nil {
		return fmt.Errorf("get prompt failed: %w", err)
	}

	return printOutput(out, res, outputFormat(pretty))
}

func mcpClientSearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search <keyword>",
		Short: "Search tools by name or description across all connected MCP servers",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMCPClientSearch(cmd.Context(), cmd.OutOrStdout(), args[0])
		},
	}
	return cmd
}

func runMCPClientSearch(ctx context.Context, out io.Writer, keyword string) error {
	cfg, err := loadMCPClientConfig(config.DefaultConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	reg := mcpclient.NewRegistry()

	reg.ConnectAll(ctx, cfg)
	defer reg.Close()

	keywordLower := strings.ToLower(keyword)
	matches := reg.ListAllTools()
	var filtered []mcpclient.ToolEntry
	for _, te := range matches {
		if strings.Contains(strings.ToLower(te.Tool.Name), keywordLower) ||
			strings.Contains(strings.ToLower(te.Tool.Description), keywordLower) {
			filtered = append(filtered, te)
		}
	}

	if len(filtered) == 0 {
		fmt.Fprintf(out, "No tools match %q\n", keyword)
		return nil
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\t%s\t%s\n", "SERVER", "NAME", "DESCRIPTION")
	for _, te := range filtered {
		desc := te.Tool.Description
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", te.Server, te.Tool.Name, desc)
	}
	return tw.Flush()
}

func mcpClientBatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "batch <file.json>",
		Short: "Execute multiple tool calls from a JSON file",
		Long: `Read an array of tool calls from a JSON file and execute them sequentially.

Each entry must have: server, tool, and optional args (map).
Example file:
[
  {"server":"axis-local","tool":"axis_health"},
  {"server":"axis-local","tool":"placement_decision","args":{"description":"ollama run llama3"}}
]`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMCPClientBatch(cmd.Context(), cmd.OutOrStdout(), args[0])
		},
	}
	return cmd
}

type batchEntry struct {
	Server string         `json:"server"`
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args,omitempty"`
}

func runMCPClientBatch(ctx context.Context, out io.Writer, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read batch file: %w", err)
	}
	var entries []batchEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("parse batch file: %w", err)
	}

	cfg, err := loadMCPClientConfig(config.DefaultConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	reg := mcpclient.NewRegistry()

	reg.ConnectAll(ctx, cfg)
	defer reg.Close()

	type batchResult struct {
		Index  int    `json:"index"`
		Server string `json:"server"`
		Tool   string `json:"tool"`
		OK     bool   `json:"ok"`
		Error  string `json:"error,omitempty"`
		Output string `json:"output,omitempty"`
	}
	var results []batchResult

	for i, entry := range entries {
		res := reg.CallTool(ctx, entry.Server, entry.Tool, entry.Args)
		br := batchResult{
			Index:  i,
			Server: entry.Server,
			Tool:   entry.Tool,
		}
		if res.Err != nil {
			br.OK = false
			br.Error = res.Err.Error()
		} else {
			br.OK = true
			var parts []string
			for _, content := range res.Result.Content {
				if tc, ok := content.(mcp.TextContent); ok {
					parts = append(parts, tc.Text)
				} else {
					b, _ := json.Marshal(content)
					parts = append(parts, string(b))
				}
			}
			br.Output = strings.Join(parts, "\n")
		}
		results = append(results, br)
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

func mcpClientInteractiveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "interactive",
		Short: "Interactive REPL for exploring and calling MCP servers",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMCPClientInteractive(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	return cmd
}

func runMCPClientInteractive(ctx context.Context, in io.Reader, out io.Writer) error {
	cfg, err := loadMCPClientConfig(config.DefaultConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	reg := mcpclient.NewRegistry()

	reg.ConnectAll(ctx, cfg)
	defer reg.Close()

	fmt.Fprintln(out, "AXIS MCP Client Interactive REPL")
	fmt.Fprintln(out, "Commands: tools, resources, prompts, call <server> <tool> [args], read <server> <uri>, get-prompt <server> <prompt> [args], search <keyword>, list, help, quit")
	fmt.Fprintln(out)

	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprint(out, "mcp> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		cmd := parts[0]
		args := parts[1:]

		switch cmd {
		case "quit", "exit", "q":
			fmt.Fprintln(out, "Bye.")
			return nil
		case "help", "h":
			fmt.Fprintln(out, "Commands:")
			fmt.Fprintln(out, "  list                          List connected servers")
			fmt.Fprintln(out, "  tools [--server <name>]      List tools")
			fmt.Fprintln(out, "  resources [--server <name>]   List resources")
			fmt.Fprintln(out, "  prompts [--server <name>]     List prompts")
			fmt.Fprintln(out, "  call <server> <tool> [args]   Call a tool")
			fmt.Fprintln(out, "  read <server> <uri>          Read a resource")
			fmt.Fprintln(out, "  get-prompt <server> <prompt> [args]")
			fmt.Fprintln(out, "  search <keyword>             Search tools")
			fmt.Fprintln(out, "  help                          Show this help")
			fmt.Fprintln(out, "  quit                          Exit REPL")
		case "list":
			for _, name := range reg.Names() {
				sc := reg.Get(name)
				status := "connected"
				if !sc.Connected() {
					status = fmt.Sprintf("error: %v", sc.Err)
				}
				fmt.Fprintf(out, "  %s (%s) — %s, %d tools, %d resources\n", name, sc.Transport, status, sc.ToolCount(), sc.ResourceCount())
			}
		case "tools":
			server := ""
			if len(args) >= 2 && args[0] == "--server" {
				server = args[1]
				args = args[2:]
			}
			tools := reg.ListAllTools()
			for _, te := range tools {
				if server != "" && te.Server != server {
					continue
				}
				fmt.Fprintf(out, "  %s / %s — %s\n", te.Server, te.Tool.Name, te.Tool.Description)
			}
		case "resources":
			server := ""
			if len(args) >= 2 && args[0] == "--server" {
				server = args[1]
				args = args[2:]
			}
			resources := reg.ListAllResources()
			for _, re := range resources {
				if server != "" && re.Server != server {
					continue
				}
				fmt.Fprintf(out, "  %s / %s — %s\n", re.Server, re.Resource.URI, re.Resource.Name)
			}
		case "prompts":
			server := ""
			if len(args) >= 2 && args[0] == "--server" {
				server = args[1]
				args = args[2:]
			}
			prompts := reg.ListAllPrompts()
			for _, pe := range prompts {
				if server != "" && pe.Server != server {
					continue
				}
				fmt.Fprintf(out, "  %s / %s — %s\n", pe.Server, pe.Prompt.Name, pe.Prompt.Description)
			}
		case "call":
			if len(args) < 2 {
				fmt.Fprintln(out, "Usage: call <server> <tool> [json-args]")
				continue
			}
			rawArgs := ""
			if len(args) > 2 {
				rawArgs = strings.Join(args[2:], " ")
			}
			parsedArgs, parseErr := mcpclient.ParseArgs(rawArgs)
			if parseErr != nil {
				fmt.Fprintf(out, "Error: %v\n", parseErr)
				continue
			}
			res := reg.CallTool(ctx, args[0], args[1], parsedArgs)
			if res.Err != nil {
				fmt.Fprintf(out, "Error: %v\n", res.Err)
				continue
			}
			for _, content := range res.Result.Content {
				if tc, ok := content.(mcp.TextContent); ok {
					fmt.Fprintln(out, tc.Text)
				} else {
					b, _ := json.MarshalIndent(content, "", "  ")
					fmt.Fprintln(out, string(b))
				}
			}
		case "read":
			if len(args) < 2 {
				fmt.Fprintln(out, "Usage: read <server> <uri>")
				continue
			}
			result, readErr := reg.ReadResource(ctx, args[0], args[1])
			if readErr != nil {
				fmt.Fprintf(out, "Error: %v\n", readErr)
				continue
			}
			for _, content := range result.Contents {
				if tc, ok := content.(mcp.TextResourceContents); ok {
					fmt.Fprintln(out, tc.Text)
				} else {
					b, _ := json.MarshalIndent(content, "", "  ")
					fmt.Fprintln(out, string(b))
				}
			}
		case "get-prompt":
			if len(args) < 2 {
				fmt.Fprintln(out, "Usage: get-prompt <server> <prompt> [json-args]")
				continue
			}
			rawArgs := ""
			if len(args) > 2 {
				rawArgs = strings.Join(args[2:], " ")
			}
			parsedArgs, parseErr := mcpclient.ParseArgs(rawArgs)
			if parseErr != nil {
				fmt.Fprintf(out, "Error: %v\n", parseErr)
				continue
			}
			res, gpErr := reg.GetPrompt(ctx, args[0], args[1], parsedArgs)
			if gpErr != nil {
				fmt.Fprintf(out, "Error: %v\n", gpErr)
				continue
			}
			b, _ := json.MarshalIndent(res, "", "  ")
			fmt.Fprintln(out, string(b))
		case "search":
			if len(args) < 1 {
				fmt.Fprintln(out, "Usage: search <keyword>")
				continue
			}
			keywordLower := strings.ToLower(strings.Join(args, " "))
			for _, te := range reg.ListAllTools() {
				if strings.Contains(strings.ToLower(te.Tool.Name), keywordLower) ||
					strings.Contains(strings.ToLower(te.Tool.Description), keywordLower) {
					fmt.Fprintf(out, "  %s / %s — %s\n", te.Server, te.Tool.Name, te.Tool.Description)
				}
			}
		default:
			fmt.Fprintf(out, "Unknown command: %s. Type 'help' for available commands.\n", cmd)
		}
	}
	return scanner.Err()
}

func outputFormat(pretty bool) string {
	if pretty {
		return "json-pretty"
	}
	return "json"
}

func init() {
	// Ensure color is available for status indicators
	ui.Init(false)
}
