package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/state"
	"github.com/toasterbook88/axis/internal/ui"
)

func reservationsCmd() *cobra.Command {
	var cacheAddr string

	cmd := &cobra.Command{
		Use:   "reservations",
		Short: "Show active resource reservations in the cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReservationsTable(cmd, cacheAddr)
		},
	}
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS API daemon cache")
	cmd.AddCommand(reservationsListCmd())
	cmd.AddCommand(reservationsInspectCmd())
	cmd.AddCommand(reservationsReleaseCmd())
	cmd.AddCommand(reservationsDoctorCmd())
	return cmd
}

func runReservationsTable(cmd *cobra.Command, cacheAddr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, baseURLAddr := daemon.HttpClientForAddr(cacheAddr)
	baseURL := daemon.NormalizeAddr(baseURLAddr)

	token, err := auth.LoadOrGenerateToken()
	if err != nil {
		return err
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v2/reservations", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	var entries []reservation.Entry
	var isFallback bool

	if err != nil {
		isFallback = true
		ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
		if loadErr := ledger.Load(); loadErr == nil {
			entries = ledger.Entries()
		} else if !os.IsNotExist(loadErr) {
			return fmt.Errorf("daemon not reachable and failed to load local ledger: %w", loadErr)
		}
	} else {
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("api error (%d): %s", resp.StatusCode, string(body))
		}
		var result struct {
			Entries []reservation.Entry `json:"reservations"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return err
		}
		entries = result.Entries
	}

	items := make([]ReservationListItem, 0, len(entries))
	now := time.Now()
	limits := reservation.DefaultLimits()
	for _, e := range entries {
		items = append(items, ReservationListItem{
			ID:      e.ID,
			Node:    e.Node,
			RAMMB:   e.RAMMB,
			Owner:   e.OwnerSurface,
			Age:     now.Sub(e.CreatedAt),
			IsStale: e.ClassifyLiveness(now, limits) != reservation.LivenessActive,
		})
	}

	if isFallback {
		ui.FprintWarning(cmd.ErrOrStderr(), "using local ledger (daemon cache unavailable)")
	}

	_, err = io.WriteString(cmd.OutOrStdout(), RenderReservationTable(items))
	return err
}

type ReservationListItem struct {
	ID      string
	Node    string
	RAMMB   int64
	Owner   string
	Age     time.Duration
	IsStale bool
}

func RenderReservationTable(items []ReservationListItem) string {
	var b strings.Builder
	sep := strings.Repeat("─", 75)
	b.WriteString("\n")
	ui.WhiteColor.Fprintf(&b, "  ACTIVE RESERVATIONS\n")
	b.WriteString("  ")
	b.WriteString(sep)
	b.WriteString("\n")

	if len(items) == 0 {
		ui.DimColor.Fprintf(&b, "  No active reservations\n\n")
		return b.String()
	}

	ui.WhiteColor.Fprintf(&b, "  %-20s %-15s %10s %-15s %10s\n",
		"ID", "NODE", "RAM (MB)", "OWNER", "AGE")
	b.WriteString("  ")
	b.WriteString(sep)
	b.WriteString("\n")

	displayItems := items
	truncated := 0
	if len(items) > 50 {
		displayItems = items[:50]
		truncated = len(items) - 50
	}

	for _, r := range displayItems {
		ageStr := formatDuration(r.Age)
		if r.IsStale {
			ageStr = ui.RedColor.Sprintf("%s (STALE)", ageStr)
		}
		fmt.Fprintf(&b, "  %-20s %-15s %10d %-15s %s\n",
			truncateID(r.ID, 20),
			r.Node,
			r.RAMMB,
			truncateID(r.Owner, 15),
			ageStr,
		)
	}

	if truncated > 0 {
		ui.DimColor.Fprintf(&b, "\n  ... and %d more reservations.\n", truncated)
	}

	b.WriteString("\n")
	return b.String()
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

func truncateID(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func reservationsListCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List active reservations from the local ledger",
		PreRunE: validateOutputFormat(&format, "text", "json", "ndjson"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
			if err := ledger.Load(); err != nil {
				return fmt.Errorf("loading ledger: %w", err)
			}

			entries := ledger.Entries()

			switch format {
			case "json":
				return json.NewEncoder(cmd.OutOrStdout()).Encode(entries)
			case "ndjson":
				out := cmd.OutOrStdout()
				for _, e := range entries {
					b, err := json.Marshal(e)
					if err != nil {
						return err
					}
					if _, err := fmt.Fprintln(out, string(b)); err != nil {
						return err
					}
				}
				return nil
			default:
				if len(entries) == 0 {
					_, err := fmt.Fprintln(cmd.OutOrStdout(), "No active reservations")
					return err
				}
				tbl := ui.NewTable("ID", "NODE", "RAM MB", "OWNER", "CREATED AT", "LAST HEARTBEAT")
				for _, e := range entries {
					tbl.AddRow(
						truncateID(e.ID, 20),
						e.Node,
						fmt.Sprintf("%d", e.RAMMB),
						truncateID(e.OwnerSurface, 15),
						e.CreatedAt.Format(time.RFC3339),
						e.LastHeartbeat.Format(time.RFC3339),
					)
				}
				var b strings.Builder
				tbl.Render(&b)
				_, err := io.WriteString(cmd.OutOrStdout(), b.String())
				return err
			}
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or ndjson")
	return cmd
}

func reservationsInspectCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:     "inspect <id>",
		Short:   "Show full details of a reservation",
		Args:    cobra.ExactArgs(1),
		PreRunE: validateOutputFormat(&format, "text", "json"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
			if err := ledger.LoadReadOnly(); err != nil {
				return fmt.Errorf("loading ledger: %w", err)
			}

			entries := ledger.Entries()
			var found *reservation.Entry
			for i := range entries {
				if entries[i].ID == id {
					found = &entries[i]
					break
				}
			}

			if found == nil {
				return ExitCodeError{Code: ExitErrGeneric, Message: fmt.Sprintf("reservation %q not found", id)}
			}

			switch format {
			case "json":
				return json.NewEncoder(cmd.OutOrStdout()).Encode(found)
			default:
				var b strings.Builder
				fmt.Fprintf(&b, "ID:            %s\n", found.ID)
				fmt.Fprintf(&b, "Node:          %s\n", found.Node)
				fmt.Fprintf(&b, "RAM MB:        %d\n", found.RAMMB)
				if found.VRAMMB > 0 {
					fmt.Fprintf(&b, "VRAM MB:       %d\n", found.VRAMMB)
				}
				fmt.Fprintf(&b, "Owner Exec ID: %s\n", found.OwnerExecID)
				fmt.Fprintf(&b, "Owner Surface: %s\n", found.OwnerSurface)
				if found.OwnerPID > 0 {
					fmt.Fprintf(&b, "Owner PID:     %d\n", found.OwnerPID)
				}
				fmt.Fprintf(&b, "Created At:    %s\n", found.CreatedAt.Format(time.RFC3339))
				fmt.Fprintf(&b, "Last Heartbeat:%s\n", found.LastHeartbeat.Format(time.RFC3339))
				if !found.ExpiresAt.IsZero() {
					fmt.Fprintf(&b, "Expires At:    %s\n", found.ExpiresAt.Format(time.RFC3339))
				}
				if found.Description != "" {
					fmt.Fprintf(&b, "Description:   %s\n", found.Description)
				}
				_, err := io.WriteString(cmd.OutOrStdout(), b.String())
				return err
			}
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	return cmd
}

func reservationsReleaseCmd() *cobra.Command {
	var force bool
	var format string

	cmd := &cobra.Command{
		Use:     "release <id>",
		Short:   "Release a reservation from the local ledger",
		Args:    cobra.ExactArgs(1),
		PreRunE: validateOutputFormat(&format, "text", "json"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if err := ledger.LockFile(ctx); err != nil {
				return fmt.Errorf("locking ledger for release: %w", err)
			}
			defer ledger.UnlockFile()
			if err := ledger.LoadReadOnly(); err != nil {
				return fmt.Errorf("loading ledger: %w", err)
			}

			entries := ledger.Entries()
			var found *reservation.Entry
			for i := range entries {
				if entries[i].ID == id {
					found = &entries[i]
					break
				}
			}

			if found == nil {
				result := map[string]interface{}{
					"success": false,
					"error":   fmt.Sprintf("reservation %q not found", id),
				}
				primary := ExitCodeError{Code: ExitErrGeneric, Message: fmt.Sprintf("reservation %q not found", id)}
				return writeReservationReleaseFailure(cmd, format, result, primary)
			}

			if !force && found.OwnerPID > 0 && found.OwnerPID != os.Getpid() {
				result := map[string]interface{}{
					"success": false,
					"error":   fmt.Sprintf("reservation %q is owned by PID %d (current PID %d); use --force to override", id, found.OwnerPID, os.Getpid()),
				}
				primary := ExitCodeError{Code: ExitErrGeneric, Message: fmt.Sprintf("reservation %q is owned by PID %d (current PID %d); use --force to override", id, found.OwnerPID, os.Getpid())}
				return writeReservationReleaseFailure(cmd, format, result, primary)
			}

			if err := ledger.Release(id); err != nil {
				result := map[string]interface{}{
					"success": false,
					"error":   err.Error(),
				}
				primary := ExitCodeError{Code: ExitErrGeneric, Message: err.Error()}
				return writeReservationReleaseFailure(cmd, format, result, primary)
			}

			result := map[string]interface{}{
				"success": true,
				"id":      id,
			}
			if format == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Released reservation %s\n", id)
			return err
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Release even if not owned by the current process")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	return cmd
}

func writeReservationReleaseFailure(cmd *cobra.Command, format string, result map[string]interface{}, primary error) error {
	if format != "json" {
		return primary
	}
	if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
		return errors.Join(primary, fmt.Errorf("writing reservation release result: %w", err))
	}
	return primary
}

type DoctorFinding struct {
	Type     string `json:"type"` // "stale", "expired", "orphaned", "drift", "leak"
	Node     string `json:"node"`
	EntryID  string `json:"entry_id,omitempty"`
	Severity string `json:"severity"` // "warning", "error"
	Message  string `json:"message"`
}

var reservationsDoctorProcessAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		proc, err := os.FindProcess(pid)
		if err != nil {
			return false
		}
		proc.Release()
		return true
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

var reservationsDoctorBeforeFix = func() {}

func isNodeLocal(nodeName string, snap *models.ClusterSnapshot, cfg *config.Config) bool {
	if snap != nil {
		for _, n := range snap.Nodes {
			if n.Name == nodeName {
				return models.IsLocalNode(n)
			}
		}
	}
	if cfg != nil {
		for _, n := range cfg.Nodes {
			if n.Name == nodeName {
				return n.IsLocal()
			}
		}
	}
	h, err := os.Hostname()
	if err == nil {
		return models.IsLocalTarget(nodeName, h)
	}
	return false
}

func doctorFindingStillApplies(f DoctorFinding, entry reservation.Entry, now time.Time, limits reservation.Limits, snap *models.ClusterSnapshot, cfg *config.Config) bool {
	switch f.Type {
	case "expired":
		return entry.ClassifyLiveness(now, limits) == reservation.LivenessExpired
	case "stale":
		return entry.ClassifyLiveness(now, limits) == reservation.LivenessStale
	case "orphaned":
		return entry.ClassifyLiveness(now, limits) == reservation.LivenessActive &&
			isNodeLocal(entry.Node, snap, cfg) && entry.OwnerPID > 0 &&
			!reservationsDoctorProcessAlive(entry.OwnerPID)
	default:
		return false
	}
}

func getBestSnapshot(ctx context.Context, cacheAddr string) (*models.ClusterSnapshot, error) {
	snap, _, err := daemon.FetchSnapshot(ctx, cacheAddr)
	if err == nil {
		return snap, nil
	}

	if data, err := os.ReadFile(daemon.DefaultSnapshotPath()); err == nil {
		var snap models.ClusterSnapshot
		if err := json.Unmarshal(data, &snap); err == nil {
			return &snap, nil
		}
	}

	rt, err := runtimectx.Load(ctx)
	if err == nil && rt.Snapshot != nil {
		return rt.Snapshot, nil
	}

	return &models.ClusterSnapshot{}, nil
}

func reservationsDoctorCmd() *cobra.Command {
	var fix bool
	var format string
	var staleWindow time.Duration
	var cacheAddr string
	validateFormat := validateOutputFormat(&format, "text", "json")
	validateStaleWindow := validatePositiveDuration("stale-window", &staleWindow)

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose reservation inconsistencies, stale leases, and memory leaks",
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := validateFormat(cmd, args); err != nil {
				return err
			}
			return validateStaleWindow(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReservationsDoctor(cmd, fix, format, staleWindow, cacheAddr)
		},
	}

	cmd.Flags().BoolVar(&fix, "fix", false, "Automatically release stale and orphaned reservations")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	cmd.Flags().DurationVar(&staleWindow, "stale-window", 2*time.Minute, "Stale heartbeat threshold (e.g. 2m)")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS API daemon cache")

	return cmd
}

func runReservationsDoctor(cmd *cobra.Command, fix bool, format string, staleWindow time.Duration, cacheAddr string) error {
	if staleWindow <= 0 {
		return fmt.Errorf("--stale-window must be greater than zero")
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)

	// 1. Read ledger file directly to inspect raw entries before Load cleans them
	var diskEntries []reservation.Entry
	ledgerPath := reservation.Path()
	if data, err := os.ReadFile(ledgerPath); err == nil {
		var df struct {
			Entries []reservation.Entry `json:"entries"`
		}
		if err := json.Unmarshal(data, &df); err == nil {
			diskEntries = df.Entries
		}
	}

	st, err := state.Load()
	if err != nil {
		if st == nil {
			st = &state.ClusterState{Nodes: make(map[string]state.NodeState)}
		}
	}

	cfg, cfgErr := config.Load(config.DefaultConfigPath())
	if cfgErr != nil {
		cfg = &config.Config{}
	}

	snap, err := getBestSnapshot(ctx, cacheAddr)
	if err != nil {
		snap = &models.ClusterSnapshot{}
	}

	now := time.Now()
	var findings []DoctorFinding
	var fixed []DoctorFinding

	nodesSet := make(map[string]bool)
	for _, e := range diskEntries {
		nodesSet[e.Node] = true
	}
	for nodeName := range st.Nodes {
		nodesSet[nodeName] = true
	}

	// Check for stale, expired, and orphaned reservations
	limits := reservation.DefaultLimits()
	limits.HeartbeatStaleWindow = staleWindow
	for _, e := range diskEntries {
		liveness := e.ClassifyLiveness(now, limits)
		isExpired := (liveness == reservation.LivenessExpired)
		isStale := (liveness == reservation.LivenessStale)

		if isExpired {
			finding := DoctorFinding{
				Type:     "expired",
				Node:     e.Node,
				EntryID:  e.ID,
				Severity: "warning",
				Message:  fmt.Sprintf("Reservation expired at %s", e.ExpiresAt.Format(time.RFC3339)),
			}
			findings = append(findings, finding)
		} else if isStale {
			finding := DoctorFinding{
				Type:     "stale",
				Node:     e.Node,
				EntryID:  e.ID,
				Severity: "warning",
				Message:  fmt.Sprintf("Heartbeat stale (last seen %s)", e.LastHeartbeat.Format(time.RFC3339)),
			}
			findings = append(findings, finding)
		} else {
			isLocal := isNodeLocal(e.Node, snap, cfg)
			if isLocal && e.OwnerPID > 0 && !reservationsDoctorProcessAlive(e.OwnerPID) {
				finding := DoctorFinding{
					Type:     "orphaned",
					Node:     e.Node,
					EntryID:  e.ID,
					Severity: "warning",
					Message:  fmt.Sprintf("Owner PID %d is no longer alive", e.OwnerPID),
				}
				findings = append(findings, finding)
			}
		}
	}

	// If fix is requested, perform remediation
	if fix && len(findings) > 0 {
		reservationsDoctorBeforeFix()
		if err := ledger.LockFile(ctx); err != nil {
			return fmt.Errorf("acquiring write lock on ledger: %w", err)
		}
		defer ledger.UnlockFile()

		// Load the exact ledger state without default-threshold reconciliation.
		// Findings above use the operator's --stale-window, so remediation must
		// release precisely those entries rather than applying ledger defaults.
		if err := ledger.LoadReadOnly(); err != nil {
			return fmt.Errorf("loading reservation ledger for repair: %w", err)
		}

		// Re-fetch remaining entries to see what is left to release manually
		remainingMap := make(map[string]reservation.Entry)
		for _, re := range ledger.Entries() {
			remainingMap[re.ID] = re
		}

		// Go through our findings and perform release
		for _, f := range findings {
			switch f.Type {
			case "expired", "stale", "orphaned":
				entry, ok := remainingMap[f.EntryID]
				if !ok || !doctorFindingStillApplies(f, entry, time.Now(), limits, snap, cfg) {
					continue
				}
				if err := ledger.Release(f.EntryID); err != nil {
					return fmt.Errorf("releasing %s reservation %q: %w", f.Type, f.EntryID, err)
				}
				delete(remainingMap, f.EntryID)
				fixed = append(fixed, f)
			}
		}
	}

	// Calculate current entries after possible fixes
	var currentEntries []reservation.Entry
	if fix {
		currentEntries = ledger.Entries()
	} else {
		currentEntries = diskEntries
	}

	// Calculate drift per node
	for nodeName := range nodesSet {
		var ledgerReservedMB int64
		for _, e := range currentEntries {
			if e.Node == nodeName {
				ledgerReservedMB += e.RAMMB
			}
		}

		stateReservedMB := int64(0)
		if nodeState, ok := st.Nodes[nodeName]; ok {
			stateReservedMB = nodeState.ReservedMB
		}

		if ledgerReservedMB != stateReservedMB {
			findings = append(findings, DoctorFinding{
				Type:     "drift",
				Node:     nodeName,
				Severity: "warning",
				Message:  fmt.Sprintf("Ledger total reserved RAM (%d MB) drifts from State total reserved RAM (%d MB)", ledgerReservedMB, stateReservedMB),
			})
		}
	}

	// Calculate memory leaks per node
	for nodeName := range nodesSet {
		var ledgerReservedMB int64
		for _, e := range currentEntries {
			if e.Node == nodeName {
				ledgerReservedMB += e.RAMMB
			}
		}

		var capacityMB int64
		foundNode := false
		for _, n := range snap.Nodes {
			if n.Name == nodeName {
				foundNode = true
				if n.Resources != nil {
					capacityMB = n.Resources.RAMTotalMB
				}
				break
			}
		}

		if foundNode && capacityMB > 0 && ledgerReservedMB > capacityMB {
			findings = append(findings, DoctorFinding{
				Type:     "leak",
				Node:     nodeName,
				Severity: "error",
				Message:  fmt.Sprintf("Total reserved RAM (%d MB) exceeds physical capacity (%d MB)", ledgerReservedMB, capacityMB),
			})
		}
	}

	healthy := len(findings) == 0

	switch format {
	case "json":
		type JSONResult struct {
			Healthy  bool            `json:"healthy"`
			Findings []DoctorFinding `json:"findings"`
			Fixed    []DoctorFinding `json:"fixed,omitempty"`
		}
		res := JSONResult{
			Healthy:  healthy,
			Findings: findings,
			Fixed:    fixed,
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
	default:
		out := cmd.OutOrStdout()
		if healthy {
			fmt.Fprintln(out, "No issues found. Cluster reservations are healthy.")
			return nil
		}

		fmt.Fprintf(out, "Reservations Doctor report:\n\n")
		tbl := ui.NewTable("SEVERITY", "CATEGORY", "NODE", "RESERVATION ID", "DETAILS")
		for _, f := range findings {
			sevStr := f.Severity
			switch f.Severity {
			case "error":
				sevStr = ui.Red("ERROR")
			case "warning":
				sevStr = ui.Yellow("WARNING")
			}

			entryID := f.EntryID
			if entryID == "" {
				entryID = "—"
			}

			tbl.AddRow(
				sevStr,
				strings.ToUpper(f.Type),
				f.Node,
				truncateID(entryID, 20),
				f.Message,
			)
		}
		tbl.Render(out)

		if fix && len(fixed) > 0 {
			fmt.Fprintf(out, "\nSuccessfully fixed %d issue(s):\n", len(fixed))
			for _, f := range fixed {
				fmt.Fprintf(out, "  - Released %s reservation %s on node %s\n", f.Type, f.EntryID, f.Node)
			}
		}

		return nil
	}
}
