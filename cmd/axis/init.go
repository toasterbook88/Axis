package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/chzyer/readline"
	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/transport"
	"github.com/toasterbook88/axis/internal/ui"
)

var errInitCanceled = errors.New("axis init cancelled")

func initCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Configure AXIS interactively",
		Long: "Create a new AXIS cluster configuration or safely update the existing one. " +
			"All input is validated before an atomic write.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInitWizard(cmd)
		},
	}
	cmd.Flags().String("config", "", "configuration path (default: ~/.axis/nodes.yaml)")
	return cmd
}

type initDependencies struct {
	hostname          func() (string, error)
	defaultUser       func() string
	loadConfig        func(string) (*config.Config, error)
	saveConfig        func(string, *config.Config) (config.SaveResult, error)
	verifySSH         func(context.Context, string, int, string, int, io.Writer) bool
	discoverTailscale func(context.Context) ([]config.NodeConfig, error)
	discoverMesh      func(context.Context) ([]config.NodeConfig, error)
}

func defaultInitDependencies() initDependencies {
	return initDependencies{
		hostname:          os.Hostname,
		defaultUser:       currentSSHUser,
		loadConfig:        config.Load,
		saveConfig:        config.SaveAtomic,
		verifySSH:         verifySSHConnectionFn,
		discoverTailscale: discoverTailscalePeersFn,
		discoverMesh:      discoverMeshPeersFn,
	}
}

func runInitWizard(cmd *cobra.Command) error {
	return runInitWizardWithDeps(cmd, defaultInitDependencies())
}

func runInitWizardWithDeps(cmd *cobra.Command, deps initDependencies) error {
	out := cmd.OutOrStdout()
	cfgPath, err := cmd.Flags().GetString("config")
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfgPath) == "" {
		cfgPath = config.DefaultConfigPath()
	}

	prompt, err := newInitPrompter(cmd)
	if err != nil {
		return fmt.Errorf("initialize interactive prompt: %w", err)
	}
	defer prompt.Close()

	ui.PrintLogo(out, Version)
	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.Bold("Configure AXIS"))
	fmt.Fprintf(out, "Cluster seed: %s\n\n", cfgPath)

	cfg, err := prepareInitConfig(cmd.Context(), prompt, cfgPath, deps)
	if errors.Is(err, errInitCanceled) {
		fmt.Fprintln(out, "No changes written.")
		return nil
	}
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("generated configuration is invalid: %w", err)
	}

	printConfigReview(out, cfg, cfgPath)
	confirmed, err := prompt.Confirm("Write this configuration?", true)
	if err != nil {
		if errors.Is(err, errInitCanceled) {
			fmt.Fprintln(out, "No changes written.")
			return nil
		}
		return err
	}
	if !confirmed {
		fmt.Fprintln(out, "No changes written.")
		return nil
	}

	result, err := deps.saveConfig(cfgPath, cfg)
	if err != nil {
		return fmt.Errorf("save configuration: %w", err)
	}
	if !result.Changed {
		fmt.Fprintf(out, "\n%s Configuration already matches %s\n", ui.Green("✓"), cfgPath)
		return nil
	}

	fmt.Fprintf(out, "\n%s Configuration saved to %s\n", ui.Green("✓"), cfgPath)
	if result.BackupPath != "" {
		fmt.Fprintf(out, "  Previous configuration: %s\n", result.BackupPath)
	}
	fmt.Fprintf(out, "  Next: %s\n", ui.Bold("axis doctor"))
	fmt.Fprintf(out, "  Then: %s\n\n", ui.Bold("axis summary"))
	return nil
}

func prepareInitConfig(ctx context.Context, prompt *initPrompter, cfgPath string, deps initDependencies) (*config.Config, error) {
	_, statErr := os.Stat(cfgPath)
	if errors.Is(statErr, os.ErrNotExist) {
		fmt.Fprintln(prompt.out, ui.Cyan("First-time setup"))
		fmt.Fprintln(prompt.out, "We will identify this machine, add any remote nodes, then review before writing.")
		fmt.Fprintln(prompt.out)
		return firstTimeConfig(ctx, prompt, deps)
	}
	if statErr != nil {
		return nil, fmt.Errorf("inspect existing configuration: %w", statErr)
	}

	existing, loadErr := deps.loadConfig(cfgPath)
	if loadErr != nil {
		fmt.Fprintf(prompt.out, "%s Existing configuration is invalid:\n  %v\n\n", ui.Red("!"), loadErr)
		replace, err := prompt.Confirm("Replace it with a new validated configuration?", false)
		if err != nil || !replace {
			return nil, errInitCanceled
		}
		return firstTimeConfig(ctx, prompt, deps)
	}

	fmt.Fprintln(prompt.out, ui.Cyan("Existing configuration detected"))
	fmt.Fprintf(prompt.out, "%d node(s), UDP discovery %s. Optional chat, provider, MCP, and webhook settings will be preserved.\n\n",
		len(existing.Nodes), enabledLabel(existing.Discovery != nil && existing.Discovery.Enabled))

	choice, err := prompt.Choose(ctx, "What would you like to do?", []ui.SelectOption{
		{ID: "update", Label: "Review and update", Detail: "edit the existing configuration in place"},
		{ID: "replace", Label: "Start over", Detail: "build a replacement configuration"},
		{ID: "cancel", Label: "Exit", Detail: "leave the file unchanged"},
	}, "update")
	if err != nil {
		return nil, err
	}
	switch choice {
	case "update":
		return updateConfig(ctx, prompt, cloneConfig(existing), deps)
	case "replace":
		ok, err := prompt.Confirm("Replace the current node and discovery settings?", false)
		if err != nil || !ok {
			return nil, errInitCanceled
		}
		fresh, err := firstTimeConfig(ctx, prompt, deps)
		if err != nil {
			return nil, err
		}
		preserveOptionalConfig(fresh, existing)
		return fresh, nil
	default:
		return nil, errInitCanceled
	}
}

func firstTimeConfig(ctx context.Context, prompt *initPrompter, deps initDependencies) (*config.Config, error) {
	fmt.Fprintln(prompt.out, ui.Cyan("1/3  This machine"))
	host, _ := deps.hostname()
	host = normalizeSuggestedName(host)
	if host == "" {
		host = "local"
	}
	name, err := prompt.Text("Node name", host, validateNodeName)
	if err != nil {
		return nil, err
	}
	user, err := prompt.Text("SSH user", deps.defaultUser(), validateSSHUser)
	if err != nil {
		return nil, err
	}

	cfg := &config.Config{Nodes: []config.NodeConfig{{
		Name:       name,
		Hostname:   "localhost",
		SSHUser:    user,
		Role:       "primary",
		TimeoutSec: 10,
	}}}
	fmt.Fprintf(prompt.out, "%s Local node configured.\n\n", ui.Green("✓"))

	fmt.Fprintln(prompt.out, ui.Cyan("2/3  Remote nodes"))
	if err := remoteNodeMenu(ctx, prompt, cfg, deps, true); err != nil {
		return nil, err
	}

	fmt.Fprintln(prompt.out, ui.Cyan("3/3  Discovery"))
	if err := configureDiscovery(prompt, cfg, len(cfg.Nodes) > 1); err != nil {
		return nil, err
	}
	return cfg, nil
}

func updateConfig(ctx context.Context, prompt *initPrompter, cfg *config.Config, deps initDependencies) (*config.Config, error) {
	for {
		fmt.Fprintf(prompt.out, "\n%s  %d node(s) · discovery %s\n", ui.Cyan("Configuration"), len(cfg.Nodes), enabledLabel(cfg.Discovery != nil && cfg.Discovery.Enabled))
		choice, err := prompt.Choose(ctx, "Choose an action", []ui.SelectOption{
			{ID: "add", Label: "Add nodes", Detail: "manual, Tailscale, or AXIS beacon discovery"},
			{ID: "edit", Label: "Edit a node", Detail: "change identity or SSH settings", Disabled: len(cfg.Nodes) == 0},
			{ID: "remove", Label: "Remove a node", Detail: "delete a seed entry", Disabled: len(cfg.Nodes) <= 1},
			{ID: "discovery", Label: "Discovery settings", Detail: "enable or disable authenticated UDP discovery"},
			{ID: "review", Label: "Review and save", Detail: "validate the complete configuration"},
			{ID: "cancel", Label: "Exit without saving"},
		}, "review")
		if err != nil {
			return nil, err
		}
		switch choice {
		case "add":
			if err := remoteNodeMenu(ctx, prompt, cfg, deps, false); err != nil {
				return nil, err
			}
		case "edit":
			if err := editNode(ctx, prompt, cfg, deps); err != nil {
				return nil, err
			}
		case "remove":
			if err := removeNode(ctx, prompt, cfg); err != nil {
				return nil, err
			}
		case "discovery":
			if err := configureDiscovery(prompt, cfg, cfg.Discovery != nil && cfg.Discovery.Enabled); err != nil {
				return nil, err
			}
		case "review":
			if err := cfg.Validate(); err != nil {
				fmt.Fprintf(prompt.out, "%s %v\n", ui.Red("Invalid configuration:"), err)
				continue
			}
			return cfg, nil
		default:
			return nil, errInitCanceled
		}
	}
}

func remoteNodeMenu(ctx context.Context, prompt *initPrompter, cfg *config.Config, deps initDependencies, firstTime bool) error {
	for {
		options := []ui.SelectOption{
			{ID: "manual", Label: "Add manually", Detail: "hostname or IP plus SSH settings"},
			{ID: "tailscale", Label: "Scan Tailscale", Detail: "review online peers before adding"},
			{ID: "mesh", Label: "Scan AXIS beacons", Detail: "listen for three seconds"},
		}
		if firstTime {
			options = append(options, ui.SelectOption{ID: "done", Label: "Continue", Detail: "finish adding remote nodes"})
		} else {
			options = append(options, ui.SelectOption{ID: "done", Label: "Back"})
		}
		choice, err := prompt.Choose(ctx, "Add remote nodes", options, "done")
		if err != nil {
			return err
		}
		switch choice {
		case "manual":
			node, keep, err := promptManualNode(ctx, prompt, cfg, deps)
			if err != nil {
				return err
			}
			if keep {
				cfg.Nodes = append(cfg.Nodes, node)
				fmt.Fprintf(prompt.out, "%s Added %s.\n", ui.Green("✓"), node.Name)
			}
		case "tailscale":
			fmt.Fprintln(prompt.out, "Scanning Tailscale peers...")
			candidates, err := deps.discoverTailscale(ctx)
			if err != nil {
				fmt.Fprintf(prompt.out, "%s %v\n", ui.Red("Tailscale scan unavailable:"), err)
				continue
			}
			if err := reviewCandidates(ctx, prompt, cfg, candidates, deps); err != nil {
				return err
			}
		case "mesh":
			fmt.Fprintln(prompt.out, "Listening for AXIS beacons for 3 seconds...")
			candidates, err := deps.discoverMesh(ctx)
			if err != nil {
				fmt.Fprintf(prompt.out, "%s %v\n", ui.Red("Beacon scan unavailable:"), err)
				continue
			}
			if err := reviewCandidates(ctx, prompt, cfg, candidates, deps); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

func promptManualNode(ctx context.Context, prompt *initPrompter, cfg *config.Config, deps initDependencies) (config.NodeConfig, bool, error) {
	name, err := prompt.Text("Node name", "", func(value string) error {
		if err := validateNodeName(value); err != nil {
			return err
		}
		if _, found := findNodeIndex(cfg, value); found {
			return fmt.Errorf("a node named %q already exists", value)
		}
		return nil
	})
	if err != nil {
		return config.NodeConfig{}, false, err
	}
	host, err := prompt.Text("Hostname or IP", "", validateHostname)
	if err != nil {
		return config.NodeConfig{}, false, err
	}
	user, err := prompt.Text("SSH user", defaultNodeUser(cfg, deps.defaultUser()), validateSSHUser)
	if err != nil {
		return config.NodeConfig{}, false, err
	}
	port, err := prompt.Int("SSH port", 22, 1, 65535)
	if err != nil {
		return config.NodeConfig{}, false, err
	}
	if duplicateHostPort(cfg, host, port, -1) {
		fmt.Fprintf(prompt.out, "%s %s:%d is already configured.\n", ui.Red("Error:"), host, port)
		return config.NodeConfig{}, false, nil
	}
	timeout, err := prompt.Int("Connection timeout (seconds)", 10, 1, 300)
	if err != nil {
		return config.NodeConfig{}, false, err
	}
	node := config.NodeConfig{Name: name, Hostname: host, SSHUser: user, Role: "worker", SSHPort: port, TimeoutSec: timeout}
	return verifyCandidate(ctx, prompt, node, deps)
}

func reviewCandidates(ctx context.Context, prompt *initPrompter, cfg *config.Config, candidates []config.NodeConfig, deps initDependencies) error {
	if len(candidates) == 0 {
		fmt.Fprintln(prompt.out, "No eligible nodes found.")
		return nil
	}
	fmt.Fprintf(prompt.out, "Found %d candidate(s).\n", len(candidates))
	for _, candidate := range candidates {
		if candidate.Hostname == "localhost" || candidate.Hostname == "127.0.0.1" {
			continue
		}
		if _, found := findNodeIndex(cfg, candidate.Name); found || duplicateHostPort(cfg, candidate.Hostname, candidate.EffectiveSSHPort(), -1) {
			fmt.Fprintf(prompt.out, "  - %s (%s): already configured\n", candidate.Name, candidate.Hostname)
			continue
		}
		candidate.SSHUser = defaultNodeUser(cfg, deps.defaultUser())
		candidate.Role = "worker"
		if candidate.SSHPort <= 0 {
			candidate.SSHPort = 22
		}
		if candidate.TimeoutSec <= 0 {
			candidate.TimeoutSec = 10
		}
		add, err := prompt.Confirm(fmt.Sprintf("Add %s (%s)?", candidate.Name, candidate.Hostname), true)
		if err != nil {
			return err
		}
		if !add {
			continue
		}
		node, keep, err := verifyCandidate(ctx, prompt, candidate, deps)
		if err != nil {
			return err
		}
		if keep {
			cfg.Nodes = append(cfg.Nodes, node)
			fmt.Fprintf(prompt.out, "%s Added %s.\n", ui.Green("✓"), node.Name)
		}
	}
	return nil
}

func verifyCandidate(ctx context.Context, prompt *initPrompter, node config.NodeConfig, deps initDependencies) (config.NodeConfig, bool, error) {
	check, err := prompt.Confirm("Verify SSH before adding?", true)
	if err != nil {
		return config.NodeConfig{}, false, err
	}
	if !check || deps.verifySSH(ctx, node.Hostname, node.EffectiveSSHPort(), node.SSHUser, node.EffectiveTimeout(), prompt.out) {
		return node, true, nil
	}
	keep, err := prompt.Confirm("SSH verification failed. Keep this node anyway?", false)
	return node, keep, err
}

func editNode(ctx context.Context, prompt *initPrompter, cfg *config.Config, deps initDependencies) error {
	idx, err := chooseNode(ctx, prompt, cfg, "Edit which node?")
	if err != nil {
		return err
	}
	current := cfg.Nodes[idx]
	name, err := prompt.Text("Node name", current.Name, func(value string) error {
		if err := validateNodeName(value); err != nil {
			return err
		}
		if other, found := findNodeIndex(cfg, value); found && other != idx {
			return fmt.Errorf("a node named %q already exists", value)
		}
		return nil
	})
	if err != nil {
		return err
	}
	host, err := prompt.Text("Hostname or IP", current.Hostname, validateHostname)
	if err != nil {
		return err
	}
	user, err := prompt.Text("SSH user", current.SSHUser, validateSSHUser)
	if err != nil {
		return err
	}
	role, err := prompt.Choose(ctx, "Node role", []ui.SelectOption{
		{ID: "primary", Label: "Primary"},
		{ID: "worker", Label: "Worker"},
	}, normalizedRole(current.Role))
	if err != nil {
		return err
	}
	port, err := prompt.Int("SSH port", current.EffectiveSSHPort(), 1, 65535)
	if err != nil {
		return err
	}
	if duplicateHostPort(cfg, host, port, idx) {
		fmt.Fprintf(prompt.out, "%s %s:%d is already configured.\n", ui.Red("Error:"), host, port)
		return nil
	}
	timeout, err := prompt.Int("Connection timeout (seconds)", current.EffectiveTimeout(), 1, 300)
	if err != nil {
		return err
	}
	updated := current
	updated.Name, updated.Hostname, updated.SSHUser, updated.Role = name, host, user, role
	updated.SSHPort, updated.TimeoutSec = port, timeout
	if host != "localhost" && host != "127.0.0.1" {
		verify, err := prompt.Confirm("Verify the updated SSH settings?", true)
		if err != nil {
			return err
		}
		if verify && !deps.verifySSH(ctx, host, port, user, timeout, prompt.out) {
			keep, err := prompt.Confirm("Verification failed. Keep these edits?", false)
			if err != nil {
				return err
			}
			if !keep {
				return nil
			}
		}
	}
	cfg.Nodes[idx] = updated
	fmt.Fprintf(prompt.out, "%s Updated %s.\n", ui.Green("✓"), updated.Name)
	return nil
}

func removeNode(ctx context.Context, prompt *initPrompter, cfg *config.Config) error {
	idx, err := chooseNode(ctx, prompt, cfg, "Remove which node?")
	if err != nil {
		return err
	}
	node := cfg.Nodes[idx]
	ok, err := prompt.Confirm(fmt.Sprintf("Remove %s (%s)?", node.Name, node.Hostname), false)
	if err != nil || !ok {
		return err
	}
	cfg.Nodes = append(cfg.Nodes[:idx], cfg.Nodes[idx+1:]...)
	fmt.Fprintf(prompt.out, "%s Removed %s.\n", ui.Green("✓"), node.Name)
	return nil
}

func configureDiscovery(prompt *initPrompter, cfg *config.Config, defaultEnabled bool) error {
	enabled, err := prompt.Confirm("Enable authenticated UDP discovery?", defaultEnabled)
	if err != nil {
		return err
	}
	if !enabled {
		cfg.Discovery = &config.DiscoveryConfig{Enabled: false}
		fmt.Fprintln(prompt.out, "UDP discovery disabled.")
		return nil
	}
	port := 42424
	interval := 3
	secret := ""
	if cfg.Discovery != nil {
		if cfg.Discovery.UDPPort > 0 {
			port = cfg.Discovery.UDPPort
		}
		if cfg.Discovery.BeaconInterval > 0 {
			interval = cfg.Discovery.BeaconInterval
		}
		secret = cfg.Discovery.Secret
	}
	port, err = prompt.Int("UDP port", port, 1, 65535)
	if err != nil {
		return err
	}
	interval, err = prompt.Int("Beacon interval (seconds)", interval, 1, 300)
	if err != nil {
		return err
	}
	if secret == "" {
		secret, err = generateRandomSecret()
		if err != nil {
			return fmt.Errorf("generate discovery secret: %w", err)
		}
	}
	cfg.Discovery = &config.DiscoveryConfig{Enabled: true, UDPPort: port, BeaconInterval: interval, Secret: secret}
	fmt.Fprintln(prompt.out, "Discovery secret generated and stored with the configuration; it will not be printed.")
	return nil
}

func chooseNode(ctx context.Context, prompt *initPrompter, cfg *config.Config, title string) (int, error) {
	options := make([]ui.SelectOption, 0, len(cfg.Nodes))
	for i, node := range cfg.Nodes {
		options = append(options, ui.SelectOption{ID: strconv.Itoa(i), Label: node.Name, Detail: node.Hostname})
	}
	choice, err := prompt.Choose(ctx, title, options, "0")
	if err != nil {
		return 0, err
	}
	idx, err := strconv.Atoi(choice)
	if err != nil || idx < 0 || idx >= len(cfg.Nodes) {
		return 0, fmt.Errorf("invalid node selection %q", choice)
	}
	return idx, nil
}

func printConfigReview(out io.Writer, cfg *config.Config, path string) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.Cyan("Review"))
	fmt.Fprintf(out, "  Path:      %s\n", path)
	fmt.Fprintf(out, "  Nodes:     %d\n", len(cfg.Nodes))
	fmt.Fprintf(out, "  Discovery: %s\n", enabledLabel(cfg.Discovery != nil && cfg.Discovery.Enabled))
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %-18s %-9s %-24s %s\n", "NAME", "ROLE", "HOST", "SSH")
	for _, node := range cfg.Nodes {
		fmt.Fprintf(out, "  %-18s %-9s %-24s %s@%s:%d\n",
			node.Name, normalizedRole(node.Role), node.Hostname, node.SSHUser, node.Hostname, node.EffectiveSSHPort())
	}
	if cfg.Discovery != nil && cfg.Discovery.Enabled {
		fmt.Fprintf(out, "\n  UDP discovery uses port %d every %ds; secret configured.\n", cfg.Discovery.UDPPort, cfg.Discovery.BeaconInterval)
	}
	fmt.Fprintln(out)
}

type initPrompter struct {
	rl       *readline.Instance
	out      io.Writer
	terminal *ui.StdTerminal
}

func newInitPrompter(cmd *cobra.Command) (*initPrompter, error) {
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "",
		Stdin:           io.NopCloser(cmd.InOrStdin()),
		Stdout:          cmd.OutOrStdout(),
		InterruptPrompt: "^C",
		EOFPrompt:       "",
	})
	if err != nil {
		return nil, err
	}
	return &initPrompter{
		rl:       rl,
		out:      cmd.OutOrStdout(),
		terminal: ui.NewStdTerminal(cmd.InOrStdin(), cmd.OutOrStdout()),
	}, nil
}

func (p *initPrompter) Close() error { return p.rl.Close() }

func (p *initPrompter) Text(label, defaultValue string, validate func(string) error) (string, error) {
	for {
		prompt := label
		if defaultValue != "" {
			prompt += fmt.Sprintf(" [%s]", defaultValue)
		}
		p.rl.SetPrompt(ui.Cyan("› ") + prompt + ": ")
		line, err := p.rl.Readline()
		if errors.Is(err, readline.ErrInterrupt) || errors.Is(err, io.EOF) {
			return "", errInitCanceled
		}
		if err != nil {
			return "", err
		}
		value := strings.TrimSpace(line)
		if value == "" {
			value = defaultValue
		}
		if validate != nil {
			if err := validate(value); err != nil {
				fmt.Fprintf(p.out, "  %s %v\n", ui.Red("!"), err)
				continue
			}
		}
		return value, nil
	}
}

func (p *initPrompter) Confirm(label string, defaultYes bool) (bool, error) {
	hint := "y/N"
	if defaultYes {
		hint = "Y/n"
	}
	for {
		value, err := p.Text(label+" ["+hint+"]", "", nil)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(value) {
		case "":
			return defaultYes, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(p.out, "  Enter yes or no.")
		}
	}
}

func (p *initPrompter) Int(label string, defaultValue, minValue, maxValue int) (int, error) {
	value, err := p.Text(label, strconv.Itoa(defaultValue), func(raw string) error {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("enter a whole number")
		}
		if n < minValue || n > maxValue {
			return fmt.Errorf("enter a value from %d to %d", minValue, maxValue)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(value)
}

func (p *initPrompter) Choose(ctx context.Context, title string, options []ui.SelectOption, defaultID string) (string, error) {
	if p.terminal.IsTTY() {
		result, err := ui.Select(ctx, p.terminal, title, options)
		if err != nil {
			return "", err
		}
		if !result.Selected {
			return "", errInitCanceled
		}
		return result.ID, nil
	}

	fmt.Fprintln(p.out, title)
	defaultNumber := 0
	for i, option := range options {
		status := ""
		if option.Disabled {
			status = " (unavailable)"
		}
		fmt.Fprintf(p.out, "  %d) %s", i+1, option.Label)
		if option.Detail != "" {
			fmt.Fprintf(p.out, " — %s", option.Detail)
		}
		fmt.Fprintln(p.out, status)
		if option.ID == defaultID {
			defaultNumber = i + 1
		}
	}
	for {
		defaultText := ""
		if defaultNumber > 0 {
			defaultText = strconv.Itoa(defaultNumber)
		}
		raw, err := p.Text("Selection", defaultText, nil)
		if err != nil {
			return "", err
		}
		n, err := strconv.Atoi(raw)
		if err == nil && n >= 1 && n <= len(options) && !options[n-1].Disabled {
			return options[n-1].ID, nil
		}
		for _, option := range options {
			if !option.Disabled && strings.EqualFold(raw, option.ID) {
				return option.ID, nil
			}
		}
		fmt.Fprintln(p.out, "  Choose one of the available options.")
	}
}

func validateNodeName(value string) error {
	if value == "" {
		return errors.New("node name is required")
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			continue
		}
		return errors.New("use only letters, numbers, '.', '_' or '-'")
	}
	return nil
}

func validateHostname(value string) error {
	if value == "" {
		return errors.New("hostname or IP is required")
	}
	if strings.Contains(value, "://") {
		return errors.New("enter a hostname or IP, not a URL")
	}
	if strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return errors.New("hostname or IP cannot contain whitespace")
	}
	return nil
}

func validateSSHUser(value string) error {
	if value == "" {
		return errors.New("SSH user is required")
	}
	if strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return errors.New("SSH user cannot contain whitespace")
	}
	return nil
}

func cloneConfig(source *config.Config) *config.Config {
	clone := *source
	clone.Nodes = append([]config.NodeConfig(nil), source.Nodes...)
	clone.Webhooks = append([]string(nil), source.Webhooks...)
	clone.AllowedInternalHosts = append([]string(nil), source.AllowedInternalHosts...)
	if source.Discovery != nil {
		discoveryClone := *source.Discovery
		clone.Discovery = &discoveryClone
	}
	if source.Chat != nil {
		chatClone := *source.Chat
		clone.Chat = &chatClone
	}
	if source.Inference != nil {
		infClone := *source.Inference
		clone.Inference = &infClone
	}
	if source.AIProviders != nil {
		clone.AIProviders = make(map[string]config.AIProviderConfig, len(source.AIProviders))
		for k, v := range source.AIProviders {
			if v.Models != nil {
				v.Models = append([]config.AIModelConfig(nil), v.Models...)
			}
			clone.AIProviders[k] = v
		}
	}
	if source.MCPServers != nil {
		clone.MCPServers = make(map[string]config.MCPServerConfig, len(source.MCPServers))
		for k, v := range source.MCPServers {
			if v.Command != nil {
				v.Command = append([]string(nil), v.Command...)
			}
			if v.Headers != nil {
				headersClone := make(map[string]string, len(v.Headers))
				for hk, hv := range v.Headers {
					headersClone[hk] = hv
				}
				v.Headers = headersClone
			}
			clone.MCPServers[k] = v
		}
	}
	return &clone
}

func preserveOptionalConfig(target, source *config.Config) {
	target.Chat = source.Chat
	target.AIProviders = source.AIProviders
	target.Inference = source.Inference
	target.MCPServers = source.MCPServers
	target.Webhooks = append([]string(nil), source.Webhooks...)
	target.AllowedInternalHosts = append([]string(nil), source.AllowedInternalHosts...)
}

func findNodeIndex(cfg *config.Config, name string) (int, bool) {
	for i, node := range cfg.Nodes {
		if strings.EqualFold(node.Name, name) {
			return i, true
		}
	}
	return 0, false
}

// duplicateHostPort reports whether another node already uses the same
// hostname and SSH port. Same host with different ports is allowed (containers,
// port-forwarded VMs, multi-instance hosts).
func duplicateHostPort(cfg *config.Config, hostname string, port int, except int) bool {
	if port <= 0 {
		port = 22
	}
	for i, node := range cfg.Nodes {
		if i == except {
			continue
		}
		if !strings.EqualFold(node.Hostname, hostname) {
			continue
		}
		if node.EffectiveSSHPort() == port {
			return true
		}
	}
	return false
}

func defaultNodeUser(cfg *config.Config, fallback string) string {
	for _, node := range cfg.Nodes {
		if node.SSHUser != "" {
			return node.SSHUser
		}
	}
	return fallback
}

func normalizedRole(role string) string {
	if strings.EqualFold(role, "primary") {
		return "primary"
	}
	return "worker"
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func normalizeSuggestedName(host string) string {
	host = strings.TrimSpace(host)
	if dot := strings.IndexByte(host, '.'); dot > 0 {
		host = host[:dot]
	}
	var b strings.Builder
	for _, r := range host {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-._")
}

func currentSSHUser() string {
	for _, key := range []string{"USER", "LOGNAME", "USERNAME"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return "root"
}

var verifySSHConnectionFn = verifySSHConnection

func verifySSHConnection(ctx context.Context, host string, port int, user string, timeoutSec int, out io.Writer) bool {
	fmt.Fprintf(out, "Checking SSH to %s@%s:%d... ", user, host, port)
	sshExec := transport.NewSSHExecutor(host, port, user, timeoutSec)
	defer sshExec.Close()
	dialCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	if err := sshExec.Connect(dialCtx); err != nil {
		fmt.Fprintf(out, "%s\n  %v\n", ui.Red("failed"), err)
		return false
	}
	fmt.Fprintln(out, ui.Green("connected"))
	return true
}

var discoverTailscalePeersFn = discoverTailscalePeers

func discoverTailscalePeers(ctx context.Context) ([]config.NodeConfig, error) {
	output, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return nil, err
	}
	var status struct {
		Peer map[string]struct {
			HostName     string   `json:"HostName"`
			TailscaleIPs []string `json:"TailscaleIPs"`
			Online       bool     `json:"Online"`
		} `json:"Peer"`
	}
	if err := json.Unmarshal(output, &status); err != nil {
		return nil, fmt.Errorf("parse tailscale status: %w", err)
	}
	peers := make([]config.NodeConfig, 0, len(status.Peer))
	for _, peer := range status.Peer {
		if !peer.Online || len(peer.TailscaleIPs) == 0 {
			continue
		}
		name := normalizeSuggestedName(peer.HostName)
		if name == "" {
			name = normalizeSuggestedName(peer.TailscaleIPs[0])
		}
		peers = append(peers, config.NodeConfig{Name: name, Hostname: peer.TailscaleIPs[0], Role: "worker", SSHPort: 22, TimeoutSec: 10})
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })
	return peers, nil
}

var discoverMeshPeersFn = discoverMeshPeers

func discoverMeshPeers(ctx context.Context) ([]config.NodeConfig, error) {
	scanCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	registry := discovery.NewBeaconRegistry()
	discovery.WatchBeaconChanges(scanCtx, &config.Config{Discovery: &config.DiscoveryConfig{Enabled: true, UDPPort: 42424}}, registry, nil)
	<-scanCtx.Done()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	peers := registry.Snapshot()
	sort.Slice(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })
	return peers, nil
}

func generateRandomSecret() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return hex.EncodeToString(secret), nil
}
