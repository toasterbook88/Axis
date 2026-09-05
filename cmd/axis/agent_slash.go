package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/ui"
	"golang.org/x/term"
)

// slashVerbHandlers is the dispatch table for the agent REPL slash commands.
// It maps every accepted verb (including aliases) to its named handler.
// The verb set is locked by TestCharAllVerbsAreHandled — any change here must
// keep that inventory test green and update the characterization rows.
var slashVerbHandlers = map[string]func(*agentREPLSession, []string) (bool, bool, error){
	"/exit":         slashExit,
	"/quit":         slashExit,
	"/help":         slashHelp,
	"/plan":         slashPlan,
	"/todo":         slashPlan,
	"/diff":         slashDiff,
	"/undo":         slashUndo,
	"/compact":      slashCompact,
	"/autonomy":     slashAutonomy,
	"/fleet":        slashFleet,
	"/export":       slashExport,
	"/facts":        slashFacts,
	"/cluster":      slashCluster,
	"/nodes":        slashNodes,
	"/reservations": slashReservations,
	"/skills":       slashSkills,
	"/models":       slashModels,
	"/model":        slashModels,
	"/mcp":          slashMCP,
	"/clear":        slashClear,
	"/context":      slashContext,
	"/history":      slashHistory,
	"/tools":        slashTools,
}

// handleREPLSlashCommand parses a REPL line and dispatches slash verbs.
// Returns (handled, shouldExit, error): handled=false means the line is not a
// slash command and falls through to the agent loop.
func handleREPLSlashCommand(session *agentREPLSession, line string) (bool, bool, error) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return false, false, nil
	}
	cmd := strings.ToLower(parts[0])

	handler, ok := slashVerbHandlers[cmd]
	if !ok {
		return false, false, nil
	}
	return handler(session, parts)
}

func slashExit(*agentREPLSession, []string) (bool, bool, error) {
	return true, true, nil
}

func slashHelp(session *agentREPLSession, _ []string) (bool, bool, error) {
	errW := session.ErrOut
	fmt.Fprintln(errW, "Available commands:")
	fmt.Fprintln(errW, "  /help          Show this help message")
	fmt.Fprintln(errW, "  /plan, /todo   Show active plan and tasks")
	fmt.Fprintln(errW, "  /diff          Show git working tree changes")
	fmt.Fprintln(errW, "  /undo          Undo the most recent file edit")
	fmt.Fprintln(errW, "  /compact       Compress older conversation history")
	fmt.Fprintln(errW, "  /autonomy [m]  View or set autonomy mode (default, edit, full)")
	fmt.Fprintln(errW, "  /export [path] Export session worklog to Markdown")
	fmt.Fprintln(errW, "  /fleet         Show cluster fleet status table")
	fmt.Fprintln(errW, "  /facts         Show local node facts and resident models")
	fmt.Fprintln(errW, "  /cluster       Show cluster snapshot (no live collect)")
	fmt.Fprintln(errW, "  /clear         Clear conversation history (keep system prompt)")
	fmt.Fprintln(errW, "  /context       Show conversation token usage and limit")
	fmt.Fprintln(errW, "  /history       Show conversation turn summary")
	fmt.Fprintln(errW, "  /tools         List available tools")
	fmt.Fprintln(errW, "  /model <name>  Switch LLM model mid-session")
	fmt.Fprintln(errW, "  /models        List available models and switch interactively")
	fmt.Fprintln(errW, "  /mcp           Manage and view connected MCP servers")
	fmt.Fprintln(errW, "  /reservations  Show active ledger reservations")
	fmt.Fprintln(errW, "  /skills        Show learned skills from history")
	fmt.Fprintln(errW, "  /exit, /quit   Quit the session")
	return true, false, nil
}

func slashPlan(session *agentREPLSession, _ []string) (bool, bool, error) {
	res, err := session.Agent.ExecuteToolDirect(context.Background(), "todo", []byte(`{"op":"view"}`))
	if err != nil {
		fmt.Fprintf(session.ErrOut, "%s failed to view todo: %v\n", ui.Red("Error:"), err)
	} else {
		fmt.Fprintln(session.Out, res)
	}
	return true, false, nil
}

func slashDiff(session *agentREPLSession, _ []string) (bool, bool, error) {
	out, err := gitDiffHEAD()
	if err != nil {
		fmt.Fprintf(session.ErrOut, "%s git diff: %v\n", ui.Red("Error:"), err)
		return true, false, nil
	}
	if len(strings.TrimSpace(out)) == 0 {
		fmt.Fprintln(session.Out, "Working tree is clean — no uncommitted changes.")
	} else {
		fmt.Fprintln(session.Out, out)
	}
	return true, false, nil
}

func slashUndo(session *agentREPLSession, _ []string) (bool, bool, error) {
	res, err := session.Agent.ExecuteToolDirect(context.Background(), "undo_last", []byte(`{}`))
	if err != nil {
		fmt.Fprintf(session.ErrOut, "%s %v\n", ui.Red("Error:"), err)
	} else {
		fmt.Fprintf(session.Out, "%s %s\n", ui.Green("✓"), res)
	}
	return true, false, nil
}

func slashCompact(session *agentREPLSession, _ []string) (bool, bool, error) {
	ctx2, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	before, after, count, err := session.Agent.CompactManually(ctx2)
	if err != nil {
		fmt.Fprintf(session.ErrOut, "%s %v\n", ui.Yellow("Compaction note:"), err)
	} else {
		fmt.Fprintf(session.Out, "%s Compacted %d older messages into summary (%d → %d tokens)\n",
			ui.Green("✓"), count, before, after)
	}
	return true, false, nil
}

func slashAutonomy(session *agentREPLSession, parts []string) (bool, bool, error) {
	a := session.Agent
	w, errW := session.Out, session.ErrOut
	if len(parts) >= 2 {
		mode, err := agent.ParseAutonomyMode(parts[1])
		if err != nil {
			fmt.Fprintf(errW, "%s %v\n", ui.Red("Error:"), err)
			return true, false, nil
		}
		a.SetAutonomy(mode)
		fmt.Fprintf(w, "Switched autonomy mode to %s\n", ui.Bold(string(mode)))
	} else {
		fmt.Fprintf(w, "Current autonomy mode: %s (available: default, edit, full)\n", ui.Bold(string(a.Autonomy())))
	}
	return true, false, nil
}

func slashFleet(session *agentREPLSession, _ []string) (bool, bool, error) {
	rt, _ := session.Runtime(context.Background())
	printAgentCluster(session.Out, session.ErrOut, rt)
	return true, false, nil
}

func slashExport(session *agentREPLSession, parts []string) (bool, bool, error) {
	exportPath := ""
	if len(parts) >= 2 {
		exportPath = strings.Join(parts[1:], " ")
	}
	saved, err := exportAgentWorklog(session.Agent, exportPath)
	if err != nil {
		fmt.Fprintf(session.ErrOut, "%s %v\n", ui.Red("Error exporting worklog:"), err)
	} else {
		fmt.Fprintf(session.Out, "%s Session worklog exported to: %s\n", ui.Green("✓"), ui.Bold(saved))
	}
	return true, false, nil
}

func slashFacts(session *agentREPLSession, _ []string) (bool, bool, error) {
	rt, _ := session.Runtime(context.Background())
	printAgentFacts(session.Out, session.ErrOut, rt)
	return true, false, nil
}

func slashCluster(session *agentREPLSession, _ []string) (bool, bool, error) {
	rt, _ := session.Runtime(context.Background())
	printAgentCluster(session.Out, session.ErrOut, rt)
	return true, false, nil
}

func slashNodes(session *agentREPLSession, _ []string) (bool, bool, error) {
	fmt.Fprintln(session.ErrOut, "/nodes was cached-first with a live fallback.")
	fmt.Fprintln(session.ErrOut, "/cluster shows the session snapshot (source and age are printed).")
	fmt.Fprintln(session.ErrOut, "For a live collect: axis cluster status")
	return true, false, nil
}

// collectReservationListItems gathers ledger reservations, daemon-first with a
// runtime-context fallback (extracted verbatim from the /reservations verb).
func collectReservationListItems() ([]ReservationListItem, error) {
	freshRt, err := runtimectx.Load(context.Background())
	var items []ReservationListItem
	daemonFetched := false
	cacheAddr := api.DefaultAddr()
	client, baseURLAddr := daemon.HttpClientForAddr(cacheAddr)
	baseURL := daemon.NormalizeAddr(baseURLAddr)
	if token, tokenErr := auth.LoadOrGenerateToken(); tokenErr == nil {
		ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, reqErr := http.NewRequestWithContext(ctx2, http.MethodGet, baseURL+"/v2/reservations", nil)
		if reqErr == nil {
			req.Header.Set("Authorization", "Bearer "+token)
			resp, respErr := client.Do(req)
			if respErr == nil {
				defer resp.Body.Close()
				if resp.StatusCode == 200 {
					var result struct {
						Entries []reservation.Entry `json:"reservations"`
					}
					if json.NewDecoder(resp.Body).Decode(&result) == nil {
						daemonFetched = true
						now := time.Now()
						limits := reservation.DefaultLimits()
						for _, e := range result.Entries {
							items = append(items, ReservationListItem{
								ID:      e.ID,
								Node:    e.Node,
								RAMMB:   e.RAMMB,
								Owner:   e.OwnerSurface,
								Age:     now.Sub(e.CreatedAt),
								IsStale: e.ClassifyLiveness(now, limits) != reservation.LivenessActive,
							})
						}
					}
				}
			}
		}
	}

	if !daemonFetched {
		if err != nil {
			return nil, fmt.Errorf("failed to load cluster status fallback: %w", err)
		}
		if freshRt == nil {
			return nil, fmt.Errorf("failed to load cluster status fallback: runtime context is nil")
		}

		if freshRt.Ledger != nil {
			now := time.Now()
			limits := reservation.DefaultLimits()
			for _, e := range freshRt.Ledger.Entries() {
				items = append(items, ReservationListItem{
					ID:      e.ID,
					Node:    e.Node,
					RAMMB:   e.RAMMB,
					Owner:   e.OwnerSurface,
					Age:     now.Sub(e.CreatedAt),
					IsStale: e.ClassifyLiveness(now, limits) != reservation.LivenessActive,
				})
			}
		}
	}
	return items, nil
}

// preferredSkillNode picks the node with the highest success count for a skill.
func preferredSkillNode(s skills.LearnedSkill) string {
	bestNode := s.PreferredNode
	if bestNode == "" && len(s.NodeCount) > 0 {
		maxVal := 0
		for n, val := range s.NodeCount {
			if val > maxVal {
				maxVal = val
				bestNode = n
			} else if val == maxVal {
				if bestNode == "" || n < bestNode {
					bestNode = n
				}
			}
		}
	}
	if bestNode == "" {
		bestNode = "-"
	}
	return bestNode
}

// interactiveStdin reports whether standard input is a terminal.
func interactiveStdin() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// gitDiffHEAD runs git diff HEAD and returns combined output (extracted verbatim).
func gitDiffHEAD() (string, error) {
	cmdDiff := exec.Command("git", "diff", "HEAD")
	out, err := cmdDiff.CombinedOutput()
	return string(out), err
}

func slashReservations(session *agentREPLSession, _ []string) (bool, bool, error) {
	items, err := collectReservationListItems()
	if err != nil {
		return true, false, err
	}
	fmt.Fprint(session.Out, RenderReservationTable(items))
	return true, false, nil
}

func slashSkills(session *agentREPLSession, _ []string) (bool, bool, error) {
	freshRt, err := runtimectx.Load(context.Background())
	if err != nil {
		return true, false, fmt.Errorf("failed to load skills: %w", err)
	}
	if freshRt == nil {
		return true, false, fmt.Errorf("failed to load skills: runtime context is nil")
	}
	w, errW := session.Out, session.ErrOut
	if freshRt.Skills == nil || len(freshRt.Skills.Skills) == 0 {
		fmt.Fprintln(w, "\nLearned skills:")
		fmt.Fprintln(w, ui.DimColor.Sprint("  No learned skills yet\n"))
		return true, false, nil
	}
	tbl := ui.NewTable("ID", "DESCRIPTION", "COMMAND", "SUCCESS", "BEST NODE", "LAST USED")
	for _, s := range freshRt.Skills.Skills {
		tbl.AddRow(
			s.ID,
			s.Description,
			s.Command,
			fmt.Sprintf("%d", s.SuccessCount),
			preferredSkillNode(s),
			s.LastUsed.Format(time.RFC3339),
		)
	}
	fmt.Fprintln(w, "\nLearned skills:")
	tbl.Render(w)
	fmt.Fprintln(w)

	if !interactiveStdin() {
		return true, false, nil
	}

	var skillOptions []ui.SelectOption
	skillOptions = append(skillOptions, ui.SelectOption{
		ID:    "none",
		Label: "Cancel (do not run any skill)",
	})
	for _, s := range freshRt.Skills.Skills {
		skillOptions = append(skillOptions, ui.SelectOption{
			ID:     s.ID,
			Label:  s.Description,
			Detail: fmt.Sprintf("Command: %s", s.Command),
		})
	}

	res, err := session.Selector.Select(context.Background(), "Execute a learned skill:", skillOptions)
	if err != nil {
		return true, false, err
	}
	if !res.Selected || res.ID == "none" {
		return true, false, nil
	}

	var chosenCommand string
	for _, s := range freshRt.Skills.Skills {
		if s.ID == res.ID {
			chosenCommand = s.Command
			break
		}
	}

	if chosenCommand != "" {
		fmt.Fprintf(w, "\nRunning skill command: %s\n", ui.Bold(chosenCommand))
		ctx2, cancel := agentRequestContext(context.Background(), 5*time.Minute)
		defer cancel()
		if err := session.Agent.Run(ctx2, chosenCommand); err != nil {
			fmt.Fprintf(errW, "\n%s %v\n", ui.Red("Error:"), err)
		}
		fmt.Fprintln(session.Out)
	}
	return true, false, nil
}

func slashModels(session *agentREPLSession, parts []string) (bool, bool, error) {
	cmd := strings.ToLower(strings.TrimSpace(parts[0]))
	rt, _ := session.Runtime(context.Background())
	choices := collectModelChoices(rt)
	w := session.Out

	if cmd == "/model" && len(parts) >= 2 {
		ref := strings.Join(parts[1:], " ")
		chosen, err := findModelTargetByRef(choices, ref)
		if err != nil {
			return true, false, err
		}
		if err := switchAgentToModelChoice(session, chosen); err != nil {
			return true, false, err
		}
		return true, false, nil
	}

	if len(choices) == 0 {
		fmt.Fprintln(w, "No models found (neither local Ollama models nor enabled cloud providers).")
		return true, false, nil
	}

	var selectOptions []ui.SelectOption
	for _, choice := range choices {
		detail := fmt.Sprintf("%s - %s", choice.ProviderName, choice.ProviderKind)
		if choice.ProviderKind == "local" {
			if choice.Node != "" {
				detail = fmt.Sprintf("Remote node %s [%s] (%s)", choice.Node, choice.ProviderName, choice.Endpoint)
			} else {
				detail = fmt.Sprintf("Local node [%s] (%s)", choice.ProviderName, choice.Endpoint)
			}
		}

		disabled := choice.Disabled
		if choice.ProviderKind == "local" && choice.Node != "" && choice.Endpoint == "" {
			disabled = true
			detail += " (unsupported: no valid IP/hostname)"
		} else if choice.ProviderKind == "local" && disabled {
			detail += " (unreachable)"
		}

		selectOptions = append(selectOptions, ui.SelectOption{
			ID:       choice.ID,
			Label:    choice.Model,
			Detail:   detail,
			Disabled: disabled,
		})
	}

	res, err := session.Selector.Select(context.Background(), "Select active model for task routing:", selectOptions)
	if err != nil {
		return true, false, err
	}
	if !res.Selected {
		return true, false, nil
	}

	var chosen ModelChoice
	for _, c := range choices {
		if c.ID == res.ID {
			chosen = c
			break
		}
	}

	if err := switchAgentToModelChoice(session, chosen); err != nil {
		return true, false, err
	}
	return true, false, nil
}

func slashMCP(session *agentREPLSession, _ []string) (bool, bool, error) {
	mcpReg := session.MCPRegistry
	w, errW := session.Out, session.ErrOut
	if mcpReg == nil || len(mcpReg.Names()) == 0 {
		fmt.Fprintln(errW, "No MCP servers configured or connected.")
		return true, false, nil
	}

	for {
		names := mcpReg.Names()
		var serverOptions []ui.SelectOption
		for _, name := range names {
			s := mcpReg.Get(name)
			status := "[not initialized]"
			if s.Err != nil {
				status = "[failed]"
			} else if s.InitResult != nil {
				status = "[ready]"
			}
			serverOptions = append(serverOptions, ui.SelectOption{
				ID:     name,
				Label:  name,
				Detail: fmt.Sprintf("Transport: %s %s", s.Transport, status),
			})
		}

		serverIdx, err := session.Selector.Select(context.Background(), "Select an MCP Server:", serverOptions)
		if err != nil {
			return true, false, err
		}
		if !serverIdx.Selected {
			return true, false, nil
		}

		sc := mcpReg.Get(names[serverIdx.Index])

		for {
			actions := []ui.SelectOption{
				{ID: "tools", Label: "List Tools", Detail: "Show all tools exposed by this server"},
				{ID: "resources", Label: "List Resources", Detail: "Show all data resources exposed by this server"},
				{ID: "diagnostics", Label: "Show Server Status & Diagnostics", Detail: "Run a live ping and show connection details"},
				{ID: "back", Label: "Back", Detail: "Return to the server menu"},
			}

			actionIdx, err := session.Selector.Select(context.Background(), fmt.Sprintf("MCP Server %q Actions:", sc.Name), actions)
			if err != nil {
				return true, false, err
			}
			if !actionIdx.Selected || actionIdx.ID == "back" {
				break
			}

			switch actionIdx.ID {
			case "tools":
				slashMCPListTools(w, sc)
			case "resources":
				slashMCPListResources(w, sc)
			case "diagnostics":
				slashMCPDiagnostics(w, sc)
			}
		}
	}
}

func slashMCPListTools(w io.Writer, sc *mcpclient.ServerConnection) {
	tools := sc.CachedTools()
	if len(tools) == 0 {
		fmt.Fprintln(w, "\nNo tools exposed by this server.")
		return
	}
	fmt.Fprintf(w, "\nTools exposed by %s:\n", sc.Name)
	tbl := ui.NewTable("TOOL NAME", "SAFETY", "DESCRIPTION")
	for _, t := range tools {
		name := sanitizeDiagnosticsText(t.Name)
		desc := sanitizeDiagnosticsText(t.Description)

		safety := ui.YellowColor.Sprint("Execute")
		if agent.IsReadOnlyTool(name) || agent.IsReadOnlyTool("mcp_"+sc.Name+"_"+name) {
			safety = ui.GreenColor.Sprint("Read-Only")
		}

		tbl.AddRow(name, safety, desc)
	}
	tbl.Render(w)
	fmt.Fprintln(w)
}

func slashMCPListResources(w io.Writer, sc *mcpclient.ServerConnection) {
	resources := sc.CachedResources()
	if len(resources) == 0 {
		fmt.Fprintln(w, "\nNo resources exposed by this server.")
		return
	}
	fmt.Fprintf(w, "\nResources exposed by %s:\n", sc.Name)
	tbl := ui.NewTable("RESOURCE NAME", "URI", "DESCRIPTION")
	for _, r := range resources {
		tbl.AddRow(
			sanitizeDiagnosticsText(r.Name),
			sanitizeDiagnosticsText(r.URI),
			sanitizeDiagnosticsText(r.Description),
		)
	}
	tbl.Render(w)
	fmt.Fprintln(w)
}

func slashMCPDiagnostics(w io.Writer, sc *mcpclient.ServerConnection) {
	fmt.Fprintf(w, "\nMCP Server Details: %s\n", sanitizeDiagnosticsText(sc.Name))
	fmt.Fprintf(w, "  Transport: %s\n", sanitizeDiagnosticsText(sc.Transport))
	if sc.InitResult != nil {
		fmt.Fprintf(w, "  Initial Handshake: \033[32msuccess\033[0m\n")
		fmt.Fprintf(w, "    Protocol Version: %s\n", sanitizeDiagnosticsText(sc.InitResult.ProtocolVersion))
		if sc.InitResult.ServerInfo.Name != "" {
			sName := sanitizeDiagnosticsText(sc.InitResult.ServerInfo.Name)
			sVer := sanitizeDiagnosticsText(sc.InitResult.ServerInfo.Version)
			fmt.Fprintf(w, "    Server Name:      %s (%s)\n", sName, sVer)
		}
	} else {
		fmt.Fprintf(w, "  Initial Handshake: \033[31mfailed / not initialized\033[0m\n")
	}

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 2*time.Second)
	var pingErr error
	var pingDur time.Duration
	if sc.Client != nil {
		start := time.Now()
		pingErr = sc.Client.Ping(pingCtx)
		pingDur = time.Since(start)
	} else {
		pingErr = fmt.Errorf("client is nil")
	}
	pingCancel()

	fmt.Fprintf(w, "  Live Probe (Ping):\n")
	if pingErr == nil {
		fmt.Fprintf(w, "    Status:   \033[32mconnected\033[0m (latency: %v)\n", pingDur)
	} else {
		pErrStr := sanitizeDiagnosticsText(pingErr.Error())
		fmt.Fprintf(w, "    Status:   \033[31mfailed / unreachable\033[0m (%s)\n", pErrStr)
	}

	if !sc.ConnectedAt().IsZero() {
		fmt.Fprintf(w, "    Handshake Time:   %s\n", sc.ConnectedAt().Format(time.RFC3339))
	}
	fmt.Fprintf(w, "    Probe Time:       %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintln(w)
}

func slashClear(session *agentREPLSession, _ []string) (bool, bool, error) {
	session.Agent.Conversation().Clear()
	fmt.Fprintln(session.ErrOut, "Conversation history cleared (system prompt kept).")
	return true, false, nil
}

func slashContext(session *agentREPLSession, _ []string) (bool, bool, error) {
	a := session.Agent
	errW := session.ErrOut
	tokens := a.ContextTokens()
	limit := a.MaxTokens()
	pct := 0.0
	if limit > 0 {
		pct = float64(tokens) / float64(limit)
	}

	barWidth := 20
	filled := int(pct * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}
	if filled < 0 {
		filled = 0
	}

	barStr := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

	var coloredBar string
	switch {
	case pct <= 0.60:
		coloredBar = ui.GreenColor.Sprint(barStr)
	case pct <= 0.85:
		coloredBar = ui.YellowColor.Sprint(barStr)
	default:
		coloredBar = ui.RedColor.Sprint(barStr)
	}

	fmt.Fprintf(errW, "\nConversation Context Budget:\n")
	fmt.Fprintf(errW, "  [%s] %.1f%% (Tokens used: %d / %d budget)\n\n", coloredBar, pct*100, tokens, limit)
	return true, false, nil
}

func slashHistory(session *agentREPLSession, _ []string) (bool, bool, error) {
	msgs := session.Agent.Conversation().Messages()
	errW := session.ErrOut
	fmt.Fprintf(errW, "\nConversation History (%d message(s)):\n", len(msgs))
	for i, m := range msgs {
		short := truncateMessagePreview(m.Content, 60)
		roleLabel := historyRoleLabel(m.Role)

		fmt.Fprintf(errW, "  [%d] %s: %s\n", i, roleLabel, short)
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(errW, "      %s %s\n", ui.Dim("→ Tool call:"), ui.Bold(tc.Function.Name))
		}
	}
	fmt.Fprintln(errW)
	return true, false, nil
}

func truncateMessagePreview(content string, max int) string {
	runes := []rune(content)
	if len(runes) > max {
		return string(runes[:max-3]) + "..."
	}
	return strings.ReplaceAll(string(runes), "\n", " ")
}

func historyRoleLabel(role string) string {
	switch strings.ToLower(role) {
	case "user":
		return ui.CyanColor.Sprint("user")
	case "assistant":
		return ui.GreenColor.Sprint("assistant")
	case "system":
		return ui.WhiteColor.Sprint("system")
	case "tool":
		return ui.YellowColor.Sprint("tool")
	default:
		return role
	}
}

func slashTools(session *agentREPLSession, _ []string) (bool, bool, error) {
	defs := session.Agent.ToolDefs()
	w := session.Out
	if len(defs) == 0 {
		fmt.Fprintln(w, "\nNo tools registered.")
		return true, false, nil
	}
	tbl := ui.NewTable("TOOL NAME", "SAFETY", "DESCRIPTION")
	for _, d := range defs {
		safety := ui.YellowColor.Sprint("Execute")
		if agent.IsReadOnlyTool(d.Function.Name) {
			safety = ui.GreenColor.Sprint("Read-Only")
		}
		tbl.AddRow(d.Function.Name, safety, d.Function.Description)
	}
	fmt.Fprintln(w, "\nAvailable Tools:")
	tbl.Render(w)
	fmt.Fprintln(w)
	return true, false, nil
}
