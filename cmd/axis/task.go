package main

import (
"context"
"fmt"
"os"
"os/exec"
"strings"
"time"

"al.essio.dev/pkg/shellescape"
"github.com/spf13/cobra"
"github.com/toasterbook88/axis/internal/config"
"github.com/toasterbook88/axis/internal/discovery"
"github.com/toasterbook88/axis/internal/knowledge"
"github.com/toasterbook88/axis/internal/models"
"github.com/toasterbook88/axis/internal/placement"
"github.com/toasterbook88/axis/internal/safety"
"github.com/toasterbook88/axis/internal/scripts"
"github.com/toasterbook88/axis/internal/skills"
"github.com/toasterbook88/axis/internal/snapshot"
"github.com/toasterbook88/axis/internal/state"
"github.com/toasterbook88/axis/internal/transport"
)

func taskCmd() *cobra.Command {
cmd := &cobra.Command{
Use:   "task",
Short: "Task placement, context emission, and execution",
}
cmd.AddCommand(taskPlaceCmd())
cmd.AddCommand(taskContextCmd())
cmd.AddCommand(taskRunCmd())
return cmd
}

func taskPlaceCmd() *cobra.Command {
cmd := &cobra.Command{
Use:   "place [description]",
Short: "Select the best node to run a task (advisory only)",
Args:  cobra.ExactArgs(1),
RunE:  runTaskPlace,
}

cmd.Flags().String("format", "", "Output format: json")
return cmd
}

func runTaskPlace(cmd *cobra.Command, args []string) error {
format, _ := cmd.Flags().GetString("format")
desc := args[0]

// Load config → discover → snapshot (reuse Phase 1 flow)
cfgPath := config.DefaultConfigPath()
cfg, err := config.Load(cfgPath)
if err != nil {
fmt.Fprintf(os.Stderr, "error: %v\n", err)
return err
}

ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
defer cancel()

nodes := discovery.Discover(ctx, cfg)
snap := snapshot.Build(nodes)

// Infer requirements from description
reqs := placement.InferRequirements(desc)

// Run placement
decision := placement.SelectBestNode(reqs, snap.Nodes, nil)

if format == "json" {
return printOutput(decision, "json")
}

// Human-readable output
if !decision.OK {
fmt.Println("No suitable node found.")
for _, r := range decision.Reasoning {
fmt.Printf("  - %s\n", r)
}
os.Exit(ExitErrNoNodesFit)
}

locality := "remote"
if decision.IsLocal {
locality = "local"
}
fmt.Printf("Selected node: %s (%s, fit %d/100)\n", decision.Node, locality, decision.FitScore)
if decision.Tool != "" {
fmt.Printf("Tool: %s\n", decision.Tool)
}
fmt.Println("Reason:")
for _, r := range decision.Reasoning {
fmt.Printf("  - %s\n", r)
}
return nil
}

type taskRunIntent struct {
command              string
label                string
matchedScript        *scripts.Script
matchedSkill         *skills.LearnedSkill
requiresConfirmation bool
}

func resolveTaskRunIntent(input string, execFlag, scriptFlag bool, skillStore *skills.Store) (taskRunIntent, error) {
if execFlag && scriptFlag {
return taskRunIntent{}, fmt.Errorf("use either --exec for a raw command or --script for a known script/skill, not both")
}

var intent taskRunIntent
if skillStore != nil {
if skill, ok := skillStore.BestMatch(input); ok {
skillCopy := skill
intent.matchedSkill = &skillCopy
}
}
if script, ok := scripts.GetBestScript(input); ok {
scriptCopy := script
intent.matchedScript = &scriptCopy
}

if execFlag {
intent.command = input
intent.label = "raw command"
return intent, nil
}

if scriptFlag {
if intent.matchedScript != nil {
intent.command = intent.matchedScript.Command
intent.label = fmt.Sprintf("fallback script %q", intent.matchedScript.Name)
return intent, nil
}
if intent.matchedSkill != nil {
intent.command = intent.matchedSkill.Command
intent.label = fmt.Sprintf("learned skill %q", intent.matchedSkill.ID)
return intent, nil
}
return taskRunIntent{}, fmt.Errorf("no known script or learned skill matches %q", input)
}

if intent.matchedScript != nil {
intent.command = intent.matchedScript.Command
intent.label = fmt.Sprintf("fallback script %q", intent.matchedScript.Name)
intent.requiresConfirmation = true
return intent, nil
}
if intent.matchedSkill != nil {
intent.command = intent.matchedSkill.Command
intent.label = fmt.Sprintf("learned skill %q", intent.matchedSkill.ID)
intent.requiresConfirmation = true
return intent, nil
}

return taskRunIntent{}, fmt.Errorf("refusing to execute implicitly: use --exec for a raw command or --script for a known script/skill")
}

func taskRunCmd() *cobra.Command {
cmd := &cobra.Command{
Use:   "run [description-or-command]",
Short: "Run task on best node (explicit only — advisory placement first)",
Args:  cobra.ExactArgs(1),
RunE:  runTaskRun,
}
cmd.Flags().Bool("exec", false, "run raw command (required for safety)")
cmd.Flags().Bool("script", false, "run multi-line script")
return cmd
}

func runTaskRun(cmd *cobra.Command, args []string) error {
execFlag, _ := cmd.Flags().GetBool("exec")
scriptFlag, _ := cmd.Flags().GetBool("script")
input := args[0]
skillStore := skills.Load()
intent, err := resolveTaskRunIntent(input, execFlag, scriptFlag, skillStore)
if err != nil {
return err
}

// 1. placement (reuse existing)
cfgPath := config.DefaultConfigPath()
cfg, err := config.Load(cfgPath)
if err != nil {
Fatal(ExitErrConfigLoad, "Failed to load config: %v", err)
}
ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
defer cancel()

nodes := discovery.Discover(ctx, cfg)
snap := snapshot.Build(nodes)

st, _ := state.Load()
reqs := placement.InferRequirements(input)

if intent.matchedScript != nil {
reqs.RequiredTools = append([]string(nil), intent.matchedScript.RequiredTools...)
if intent.matchedScript.EstRAMMB > reqs.MinFreeRAMMB {
reqs.MinFreeRAMMB = intent.matchedScript.EstRAMMB
}
}

// Always bypass strict requirement for purely explicit runs if user just says "df -h"
if execFlag && len(reqs.RequiredTools) > 0 {
filtered := reqs.RequiredTools[:0]
for _, tool := range reqs.RequiredTools {
if !strings.EqualFold(tool, "ollama") {
filtered = append(filtered, tool)
}
}
reqs.RequiredTools = append([]string(nil), filtered...)
}

decision := placement.SelectBestNode(reqs, snap.Nodes, st)

if !decision.OK {
for _, r := range decision.Reasoning {
fmt.Printf("  - %s\n", r)
}
Fatal(ExitErrNoNodesFit, "no suitable node found")
}

fmt.Printf("Selected node: %s (fit %d/100)\n", decision.Node, decision.FitScore)
for _, r := range decision.Reasoning {
fmt.Printf("  - %s\n", r)
}

// 2. require an explicit execution opt-in for any script/skill suggestion.
if intent.requiresConfirmation {
fmt.Printf("\nSuggested %s for %q:\n%s\n", intent.label, input, intent.command)
return fmt.Errorf("refusing to execute implicitly; re-run with --script to execute the suggestion or --exec to run a raw command")
}

commandToRun := intent.command
if intent.matchedSkill != nil && scriptFlag {
fmt.Printf("\n=== AXIS LEARNED SKILL: %s ===\n%s\n", intent.matchedSkill.ID, intent.matchedSkill.Description)
} else if intent.matchedScript != nil && scriptFlag {
fmt.Printf("\n=== MOLE FALLBACK SCRIPT: %s ===\n%s\n", intent.matchedScript.Name, intent.matchedScript.Description)
}

fmt.Printf("\n=== EXECUTING ON %s ===\n%s\n\n", decision.Node, commandToRun)

// 3. execute with stream
// Match the node explicitly
var targetNode models.NodeFacts
for _, n := range snap.Nodes {
if n.Name == decision.Node {
targetNode = n
break
}
}

// knowledge injection
k := knowledge.Build(snap, st, decision.Node)

// Safety blocker applies only once the user has explicitly opted into execution.
if block := safety.Check(k, commandToRun, skillStore.IsKnownBad); block.Blocked {
if out, err := exec.Command("toilet", "-f", "mono12", "-F", "metal", "BULLSHIT BLOCKED").Output(); err == nil {
fmt.Print("\n", string(out), "\n")
} else {
fmt.Printf("\n=== BULLSHIT BLOCKED ===\n")
}
fmt.Printf("Reason: %s\n", block.Reason)
fmt.Printf("Dumb score: %d/100\n", block.Score)
fmt.Println("Nothing was executed. Fix your request.")
return nil
}

contextJSON, err := knowledge.ExecutionContextJSON(snap, st, decision, input, intent.matchedScript, intent.matchedSkill)
if err != nil {
Fatal(ExitErrContextWrite, "failed to encode execution context: %v", err)
}

if models.IsLocalNode(targetNode) {
contextFile, err := os.CreateTemp("", "axis-knows-*.json")
if err != nil {
Fatal(ExitErrContextWrite, "failed to create context file: %v", err)
}
defer os.Remove(contextFile.Name())
if _, err := contextFile.Write(contextJSON); err != nil {
contextFile.Close()
Fatal(ExitErrContextWrite, "failed to write context: %v", err)
}
if err := contextFile.Close(); err != nil {
Fatal(ExitErrContextWrite, "failed to finalize context file: %v", err)
}

c := exec.CommandContext(ctx, "bash", "-c", commandToRun)
c.Env = append(os.Environ(),
"AXIS_CONTEXT_FILE="+contextFile.Name(),
"BEST_NODE="+decision.Node,
)
if st != nil {
reservationMB := reqs.MinFreeRAMMB + 1024
execID, err := st.AcquireTask(decision.Node, input, reservationMB)
if err != nil {
return fmt.Errorf("failed to persist task reservation: %w", err)
}
defer func() {
if err := st.ReleaseTask(decision.Node, execID, reservationMB); err != nil {
fmt.Fprintf(os.Stderr, "warning: failed to release task reservation: %v\n", err)
}
}()
}
c.Stdout = os.Stdout
c.Stderr = os.Stderr
runErr := c.Run()
if runErr == nil {
skillStore.RecordSuccess(input, commandToRun, decision.Node)
skillStore.Save()
} else {
exitCode := 1
if err, ok := runErr.(*exec.ExitError); ok {
exitCode = err.ExitCode()
}
skillStore.RecordFailure(input, "failed with code "+fmt.Sprint(exitCode))
skillStore.Save()
}
return runErr
}

// Find config for SSH transport
var targetConfig config.NodeConfig
for _, nc := range cfg.Nodes {
if nc.Name == decision.Node {
targetConfig = nc
break
}
}

executor := transport.NewSSHExecutor(targetConfig.Hostname, targetConfig.EffectiveSSHPort(), targetConfig.SSHUser, targetConfig.EffectiveTimeout())
defer executor.Close()

remoteContextPath := fmt.Sprintf("/tmp/axis-knows-%d.json", time.Now().UTC().UnixNano())
writeJSONCmd := fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF\n", shellescape.Quote(remoteContextPath), string(contextJSON))
if _, err := executor.Run(ctx, writeJSONCmd); err != nil {
Fatal(ExitErrContextWrite, "failed to upload context: %v", err)
}

if st != nil {
reservationMB := reqs.MinFreeRAMMB + 1024
execID, err := st.AcquireTask(decision.Node, input, reservationMB)
if err != nil {
return fmt.Errorf("failed to persist task reservation: %w", err)
}
defer func() {
if err := st.ReleaseTask(decision.Node, execID, reservationMB); err != nil {
fmt.Fprintf(os.Stderr, "warning: failed to release task reservation: %v\n", err)
}
}()
}

quotedCmd := fmt.Sprintf(
"export BEST_NODE=%s AXIS_CONTEXT_FILE=%s; trap 'rm -f %s' EXIT; bash -c %s",
shellescape.Quote(decision.Node),
shellescape.Quote(remoteContextPath),
shellescape.Quote(remoteContextPath),
shellescape.Quote(commandToRun),
)
runErr := executor.Stream(ctx, quotedCmd, os.Stdout, os.Stderr)
if runErr == nil {
skillStore.RecordSuccess(input, commandToRun, decision.Node)
skillStore.Save()
}
if runErr != nil {
exitCode := 1
if err, ok := runErr.(*exec.ExitError); ok {
exitCode = err.ExitCode()
}
skillStore.RecordFailure(input, "failed with code "+fmt.Sprint(exitCode))
skillStore.Save()
return fmt.Errorf("remote execution failed: %w", runErr)
}
return nil
}

// === NEW: axis task context <description> — zero-risk token saver ===
func taskContextCmd() *cobra.Command {
cmd := &cobra.Command{
Use:   "context [description]",
Short: "Emit 200-token context block for Gemini/Codex/Copilot/OpenCode",
Args:  cobra.ExactArgs(1),
RunE:  runTaskContext,
}
return cmd
}

func runTaskContext(cmd *cobra.Command, args []string) error {
desc := args[0]

cfgPath := config.DefaultConfigPath()
cfg, err := config.Load(cfgPath)
if err != nil {
Fatal(ExitErrConfigLoad, "Failed to load config: %v", err)
}
ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
defer cancel()

snap := snapshot.Build(discovery.Discover(ctx, cfg))
reqs := placement.InferRequirements(desc)
printContextBlock(snap, reqs, desc)
return nil
}

func printContextBlock(snap *models.ClusterSnapshot, reqs models.TaskRequirements, task string) {
fmt.Println(buildContextBlock(snap, reqs, task))
}

func buildContextBlock(snap *models.ClusterSnapshot, reqs models.TaskRequirements, task string) string {
if snap == nil || len(snap.Nodes) == 0 {
return "No nodes found in cluster."
}

best, ok := selectContextNode(snap.Nodes, reqs)
if !ok {
return "No nodes found in cluster."
}

freeRAM := "unknown"
pressure := "unknown"
if best.Resources != nil {
freeRAM = fmt.Sprintf("%dMB", best.Resources.RAMFreeMB)
pressure = best.Resources.Pressure
}

return fmt.Sprintf(`AXIS CLUSTER CONTEXT (paste as system prompt):

- Best node: %s (%s free, %s pressure)
- Tools: %v
- Summary: %d nodes, %dMB total free RAM
- Task: %s
- Live tools: start read-only MCP with: axis mcp serve

Be precise. Use real node names and tools above.`, best.Name, freeRAM, pressure,
toolsList(best), len(snap.Nodes), snap.Summary.TotalFreeRAMMB, task)
}

func selectContextNode(nodes []models.NodeFacts, reqs models.TaskRequirements) (models.NodeFacts, bool) {
if len(nodes) == 0 {
return models.NodeFacts{}, false
}

// Keep the context block broad: prefer a capable node even if the exact tool is absent.
reqs.RequiredTools = nil
ranked := placement.RankCandidates(placement.FilterCandidates(reqs, nodes, nil), reqs, nil)
if len(ranked) > 0 {
return ranked[0], true
}

for _, n := range nodes {
if n.Resources != nil {
return n, true
}
}

return nodes[0], true
}

func toolsList(n models.NodeFacts) []string {
var t []string
for _, tool := range n.Tools {
t = append(t, tool.Name)
}
return t
}
