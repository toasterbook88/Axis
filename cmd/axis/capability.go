package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/capability"
	"github.com/toasterbook88/axis/internal/events"
	"github.com/toasterbook88/axis/internal/ui"
)

// capabilityCmd is the operator surface for the capability record: what each
// capability has been observed to do, and how far that has earned trust.
func capabilityCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capability",
		Short: "Show and update the evidence-backed capability record",
		Long: `The capability record tracks how far each capability has earned trust.

  axis capability check <id>   run one of Axis's own checkers and record the result
  axis capability record       report an observation from outside Axis
  axis capability verify       replay the journal and check the state against it

Only a pass that one of Axis's own checkers produced, from a probe it ran
itself, counts toward promotion. A pass reported with "record" is kept and
counted as inconclusive. Every failure counts, whoever reports it.

Levels: unproven, advisory, supervised, trusted. Counted passes raise a level
one step at a time, up to a ceiling the operator sets (advisory by default),
once the streak is long enough, covers enough time (6 hours by default) and no
machine has an open failure. A counted failure lowers it at once; a failure
from Axis's checker on a machine blocks promotion until a check on that same
machine passes.

The record is advisory. No AXIS surface reads a level to allow or refuse an
action.`,
		RunE: func(cmd *cobra.Command, args []string) error { return runCapabilityList(cmd) },
	}
	cmd.PersistentFlags().String("format", "text", "Output format: text, json, or yaml")
	cmd.AddCommand(capabilityListCmd(), capabilityShowCmd(), capabilityCheckCmd(), capabilityRecordCmd(),
		capabilityCeilingCmd(), capabilityResolveCmd(), capabilityVerifyCmd(), capabilityExportCmd())
	cmd.PreRunE = validateCapabilityFormat
	for _, sub := range cmd.Commands() {
		sub.PreRunE = validateCapabilityFormat
	}
	return cmd
}

// validateCapabilityFormat refuses a --format the capability commands do not
// write, so a script's typo is an error rather than a table.
func validateCapabilityFormat(cmd *cobra.Command, _ []string) error {
	switch capabilityFormat(cmd) {
	case "text", "json", "yaml":
		return nil
	}
	raw, _ := cmd.Flags().GetString("format")
	return fmt.Errorf("invalid --format %q (expected one of: text, json, yaml)", raw)
}

func capabilityFormat(cmd *cobra.Command) string {
	format, _ := cmd.Flags().GetString("format")
	if format == "" {
		format, _ = cmd.InheritedFlags().GetString("format")
	}
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "text"
	}
	return format
}

func capabilityStructured(cmd *cobra.Command) (string, bool) {
	format := capabilityFormat(cmd)
	return format, format == "json" || format == "yaml"
}

func loadCapabilityReport() (capability.Report, error) {
	policies, err := capability.LoadPolicies(capability.PolicyPath())
	if err != nil {
		return capability.Report{}, err
	}
	st, err := capability.Open(capability.Dir()).Load()
	if err != nil {
		return capability.Report{}, err
	}
	return capability.BuildReport(st, policies, time.Now().UTC()), nil
}

func capabilityListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every capability with its level, ceiling and pass rate",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return runCapabilityList(cmd) },
	}
}

func runCapabilityList(cmd *cobra.Command) error {
	rep, err := loadCapabilityReport()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if format, ok := capabilityStructured(cmd); ok {
		return printOutput(out, rep, format)
	}
	if len(rep.Capabilities) == 0 {
		fmt.Fprintln(out, "No capability has been observed yet. Run one of Axis's checkers: axis capability check facts.hardware")
		return nil
	}
	table := ui.NewTable("CAPABILITY", "LEVEL", "CEILING", "STREAK", "RATE", "PASS/FAIL/INC", "NEXT")
	for _, e := range rep.Capabilities {
		table.AddRow(e.Capability, string(e.Level), string(e.Ceiling), fmt.Sprintf("%d", e.Streak), capabilityRate(e),
			fmt.Sprintf("%d/%d/%d", e.Totals.Pass, e.Totals.Fail, e.Totals.Inconclusive), capabilityNext(e))
	}
	table.Render(out)
	return nil
}

func capabilityRate(e capability.Entry) string {
	if e.RateOver == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%% of %d", e.Rate*100, e.RateOver)
}

func capabilityNext(e capability.Entry) string {
	th := e.Policy.Thresholds()
	switch {
	case e.AtCeiling:
		return "at ceiling"
	case len(e.OpenFailures) > 0:
		return "blocked: failing on " + strings.Join(e.OpenFailures, ", ")
	case e.PassesToGo > 0 && e.RateOver > 0 && e.Rate < th.MinRate:
		return fmt.Sprintf("%s after %d passes and a %.0f%% rate", e.NextLevel, e.PassesToGo, th.MinRate*100)
	case e.PassesToGo > 0 && !e.EarliestPromotion.IsZero():
		return fmt.Sprintf("%s in %d passes, not before %s", e.NextLevel, e.PassesToGo, e.EarliestPromotion.Local().Format("Jan 2 15:04"))
	case e.PassesToGo > 0:
		return fmt.Sprintf("%s in %d passes", e.NextLevel, e.PassesToGo)
	case !e.EarliestPromotion.IsZero() && time.Now().Before(e.EarliestPromotion):
		return fmt.Sprintf("%s after %s", e.NextLevel, e.EarliestPromotion.Local().Format("Jan 2 15:04"))
	case e.Rate < th.MinRate:
		return fmt.Sprintf("%s when the rate reaches %.0f%%", e.NextLevel, th.MinRate*100)
	}
	return string(e.NextLevel) + " on the next pass"
}

func capabilityShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <capability>",
		Short: "Show one capability's record in full",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rep, err := loadCapabilityReport()
			if err != nil {
				return err
			}
			for _, e := range rep.Capabilities {
				if e.Capability != args[0] {
					continue
				}
				out := cmd.OutOrStdout()
				if format, ok := capabilityStructured(cmd); ok {
					return printOutput(out, e, format)
				}
				printCapabilityEntry(out, e)
				return nil
			}
			return fmt.Errorf("no record for capability %q", args[0])
		},
	}
}

func printCapabilityEntry(out io.Writer, e capability.Entry) {
	th := e.Policy.Thresholds()
	fmt.Fprintf(out, "%s\n", ui.Bold(e.Capability))
	if e.Description != "" {
		fmt.Fprintf(out, "  %s\n", e.Description)
	}
	fmt.Fprintf(out, "  level      %s (ceiling %s)\n", e.Level, e.Ceiling)
	if !e.Since.IsZero() {
		fmt.Fprintf(out, "  since      %s\n", e.Since.Format(time.RFC3339))
	}
	fmt.Fprintf(out, "  streak     %d consecutive counted passes (a promotion needs %d, over at least %s)\n", e.Streak, th.PromoteAfter, th.MinSpan())
	if !e.StreakSince.IsZero() {
		fmt.Fprintf(out, "  streak since %s\n", e.StreakSince.Format(time.RFC3339))
	}
	if len(e.OpenFailures) > 0 {
		fmt.Fprintf(out, "  failing    %s (a promotion waits for a passing check on each)\n", strings.Join(e.OpenFailures, ", "))
	}
	fmt.Fprintf(out, "  rate       %s (a promotion needs %.0f%% over %d)\n", capabilityRate(e), th.MinRate*100, th.Window)
	fmt.Fprintf(out, "  totals     %d pass, %d fail, %d inconclusive\n", e.Totals.Pass, e.Totals.Fail, e.Totals.Inconclusive)
	if e.Recent != "" {
		fmt.Fprintf(out, "  recent     %s\n", e.Recent)
	}
	fmt.Fprintf(out, "  next       %s\n", capabilityNext(e))
	if t := e.LastTransition; t != nil {
		fmt.Fprintf(out, "  last move  %s %s -> %s at %s (entry %d): %s\n", t.Kind, t.From, t.To, t.At.Format(time.RFC3339), t.Seq, t.Reason)
	}
}

func capabilityCheckCmd() *cobra.Command {
	var nodes, except []string
	var allNodes bool
	cmd := &cobra.Command{
		Use:   "check <capability>",
		Short: "Run one of Axis's own checkers and record what it found",
		Long: `Run a capability's built-in checker on this machine and record the result.
These are the only observations whose passes count toward promotion.

Checkers:
  facts.hardware  compares the OS, architecture, CPU count, RAM total and root
                  disk total AXIS reports with a fresh reading of the machine
  facts.gpu       compares the NVIDIA GPUs AXIS lists with what the NVIDIA
                  driver shows, and fails any GPU entry that is an error message

With no --node, the check runs the AXIS fact collector on this machine and
reads the hardware through system calls. With --node or --all-nodes, it reads
AXIS's published snapshot for each machine and runs a read-only shell probe on
it through AXIS's own SSH transport.

A disagreement is a counted failure, and it stays open on that machine until a
check of the same machine passes; while it is open the capability cannot be
promoted. A value AXIS did not observe, a machine AXIS cannot reach, or a probe
that could not run makes the result inconclusive.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			run, ok := capabilityCheckers[args[0]]
			if !ok {
				return fmt.Errorf("no built-in checker for %q; checkers: %s", args[0], strings.Join(capabilityCheckerNames(), ", "))
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			targets, err := capabilityTargets(ctx, nodes, allNodes, except)
			if err != nil {
				return err
			}
			policies, err := capability.LoadPolicies(capability.PolicyPath())
			if err != nil {
				return err
			}
			store := capability.Open(capability.Dir())
			out := cmd.OutOrStdout()
			format, structured := capabilityStructured(cmd)
			var results []map[string]any
			for _, target := range targets {
				nodeCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
				rec, obs, transition, err := store.RecordCheck(run(nodeCtx, target), policies)
				cancel()
				if err != nil {
					return err
				}
				if transition != nil {
					emitCapabilityTransition(*transition)
				}
				if structured {
					results = append(results, map[string]any{"node": target, "observation": obs, "record": rec, "transition": transition})
					continue
				}
				subject := obs.Subject
				if subject == "" {
					subject = "this machine"
				}
				fmt.Fprintf(out, "%s on %s: %s\n  %s\n", obs.Capability, subject, obs.Outcome, obs.Claim)
				fmt.Fprintf(out, "level %s, streak %d\n", rec.Level, rec.Streak)
				if transition != nil {
					fmt.Fprintf(out, "%s: %s -> %s (%s)\n", transition.Kind, transition.From, transition.To, transition.Reason)
				}
			}
			if structured {
				if len(results) == 1 && len(nodes) == 0 && !allNodes {
					return printOutput(out, results[0], format)
				}
				return printOutput(out, results, format)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&nodes, "node", nil, "Check these machines from AXIS's published snapshot (repeat or comma-separate)")
	cmd.Flags().BoolVar(&allNodes, "all-nodes", false, "Check every machine in AXIS's published snapshot")
	cmd.Flags().StringSliceVar(&except, "except", nil, "Machines to leave out of --all-nodes")
	return cmd
}

func capabilityRecordCmd() *cobra.Command {
	var obs capability.Observation
	var outcome, severity, evidenceKind string
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Report an observation of a capability from outside Axis",
		Long: `Report one checked claim about a capability from outside Axis.

A failure counts, whatever evidence it cites, and lowers the level one step.
It is never critical: only Axis's own checkers can fail critically, because a
caller can name a probe it never ran or a hash it never computed. To drop a
capability further at once, lower its ceiling.

A pass is kept in the journal and counted as inconclusive: Axis did not run
the probe. Passes that count come from Axis's own checkers
(axis capability check).`,
		Example: `  axis capability record --capability facts.hardware --subject node:alpha \
    --outcome fail --claim "a GPU is listed that the driver does not report" \
    --checker manual-audit --evidence-kind live-probe --evidence-ref "nvidia-smi -L"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			obs.Outcome = capability.Outcome(strings.ToLower(strings.TrimSpace(outcome)))
			obs.Severity = capability.Severity(strings.ToLower(strings.TrimSpace(severity)))
			obs.Evidence.Kind = capability.EvidenceKind(strings.ToLower(strings.TrimSpace(evidenceKind)))
			policies, err := capability.LoadPolicies(capability.PolicyPath())
			if err != nil {
				return err
			}
			rec, _, transition, err := capability.Open(capability.Dir()).Record(obs, policies)
			if err != nil {
				return err
			}
			if transition != nil {
				emitCapabilityTransition(*transition)
			}
			out := cmd.OutOrStdout()
			if format, ok := capabilityStructured(cmd); ok {
				return printOutput(out, map[string]any{"record": rec, "transition": transition}, format)
			}
			reported := obs
			reported.Origin = capability.OriginExternal
			fmt.Fprintf(out, "recorded %s for %s, counted as %s\n", obs.Outcome, rec.Capability, capability.Counted(reported))
			switch {
			case obs.Outcome == capability.OutcomePass:
				fmt.Fprintln(out, "  a pass reported from outside Axis never counts toward promotion; counted passes come from axis capability check")
			case obs.Severity == capability.SeverityCritical:
				fmt.Fprintln(out, "  reported as critical, but only Axis's own checkers can fail critically, so it counts as one level; lower the ceiling to drop it further")
			}
			fmt.Fprintf(out, "level %s, streak %d\n", rec.Level, rec.Streak)
			if transition != nil {
				fmt.Fprintf(out, "%s: %s -> %s (%s)\n", transition.Kind, transition.From, transition.To, transition.Reason)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&obs.Capability, "capability", "", "Capability observed, for example facts.hardware (required)")
	f.StringVar(&obs.Subject, "subject", "", "What it was performed on, for example node:alpha")
	f.StringVar(&outcome, "outcome", "", "pass, fail or inconclusive (required)")
	f.StringVar(&severity, "severity", "normal", "normal or critical; from outside Axis a critical failure still counts as one level")
	f.StringVar(&obs.Claim, "claim", "", "The claim that was checked")
	f.StringVar(&obs.Checker, "checker", "", "Who or what made this observation")
	f.StringVar(&evidenceKind, "evidence-kind", "none", "live-probe, file, peer or none")
	f.StringVar(&obs.Evidence.Ref, "evidence-ref", "", "The probe that was run, or the file that was read")
	f.StringVar(&obs.Evidence.SHA256, "evidence-sha256", "", "Content hash of the evidence file")
	_ = cmd.MarkFlagRequired("capability")
	_ = cmd.MarkFlagRequired("outcome")
	return cmd
}

func capabilityCeilingCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "ceiling <capability> <level>",
		Short: "Set the highest level a capability may reach",
		Long: `Set the highest level a capability may be promoted to.

A new capability starts with a ceiling of advisory, the highest level that
cannot act. Raising the ceiling does not raise the level: the capability still
has to earn it. Lowering the ceiling below the current level lowers the level.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			level, err := capability.ParseLevel(args[1])
			if err != nil {
				return err
			}
			rec, transitions, err := capability.Open(capability.Dir()).SetCeiling(args[0], level, reason)
			if err != nil {
				return err
			}
			for _, t := range transitions {
				emitCapabilityTransition(t)
			}
			out := cmd.OutOrStdout()
			if format, ok := capabilityStructured(cmd); ok {
				return printOutput(out, map[string]any{"record": rec, "transitions": transitions}, format)
			}
			fmt.Fprintf(out, "%s: level %s, ceiling %s\n", rec.Capability, rec.Level, rec.Ceiling)
			for _, t := range transitions {
				fmt.Fprintf(out, "%s: %s -> %s (%s)\n", t.Kind, t.From, t.To, t.Reason)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Why, kept in the journal")
	return cmd
}

func capabilityResolveCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "resolve <capability> <subject>",
		Short: "Close one machine's open failure on a capability",
		Long: `Close one subject's open failure, for a machine that was retired, renamed or
will not be checked again. While a failure is open the capability cannot be
promoted; normally only a passing check of that same machine closes it.

The reason is required and kept in the journal. Resolving does not raise a
level: the capability still has to earn it.`,
		Example: "  axis capability resolve facts.gpu node:alpha --reason \"machine retired\"",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			subject := strings.TrimSpace(args[1])
			if strings.HasPrefix(strings.ToLower(subject), "node:") {
				subject = nodeSubject(subject[len("node:"):]) // the one spelling the checkers write
			}
			rec, transitions, err := capability.Open(capability.Dir()).Resolve(args[0], subject, reason)
			if err != nil {
				return err
			}
			for _, t := range transitions {
				emitCapabilityTransition(t)
			}
			out := cmd.OutOrStdout()
			if format, ok := capabilityStructured(cmd); ok {
				return printOutput(out, map[string]any{"record": rec, "transitions": transitions}, format)
			}
			fmt.Fprintf(out, "%s: closed the open failure on %s; level %s\n", rec.Capability, subject, rec.Level)
			if len(rec.OpenFailures) > 0 {
				fmt.Fprintf(out, "still failing on %s\n", strings.Join(rec.OpenFailures, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Why the failure no longer applies (required, kept in the journal)")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

var errCapabilityVerify = errors.New("the capability record does not match its journal")

func capabilityVerifyCmd() *cobra.Command {
	var repair bool
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Replay the journal and check that the state matches it",
		Long: `Replay the capability journal from its first entry, checking that every
line is in order and chained to the one before it, and compare the result with
the state file. It writes nothing.

It catches an edited state, an edited or removed journal line, a journal
shorter than the state, and a transitions log that no longer matches the
journal. It cannot catch a consistent rewrite of journal and state, which
anything running as this user can make.

--repair-transitions first rebuilds the derived transitions.jsonl from the
journal (for when a write of it failed after the state was saved). It never
changes the journal or the state.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store := capability.Open(capability.Dir())
			if repair {
				n, err := store.RepairTransitions()
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "rebuilt transitions.jsonl from the journal: %d transitions\n", n)
			}
			v, err := store.Verify()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if format, ok := capabilityStructured(cmd); ok {
				if err := printOutput(out, v, format); err != nil {
					return err
				}
			} else {
				head := v.Head
				if len(head) > 12 {
					head = head[:12]
				}
				if v.OK {
					fmt.Fprintf(out, "journal verified: %d entries, the state matches (head %s)\n", v.Entries, head)
				} else {
					fmt.Fprintf(out, "journal check FAILED: %d valid entries, the state counts %d\n", v.Entries, v.Counted)
				}
				for _, p := range v.Problems {
					fmt.Fprintf(out, "  problem: %s\n", p)
				}
				for _, n := range v.Notes {
					fmt.Fprintf(out, "  note: %s\n", n)
				}
			}
			if !v.OK {
				return errCapabilityVerify
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&repair, "repair-transitions", false, "Rebuild the derived transitions.jsonl from the journal before verifying")
	return cmd
}

func capabilityExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export",
		Short: "Print the whole record as JSON for a dashboard or another tool",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rep, err := loadCapabilityReport()
			if err != nil {
				return err
			}
			format := capabilityFormat(cmd)
			if format != "yaml" {
				format = "json"
			}
			return printOutput(cmd.OutOrStdout(), rep, format)
		},
	}
}

// emitCapabilityTransition puts a level or ceiling change on the AXIS event
// log. Observations are not emitted: they would outnumber every other event.
func emitCapabilityTransition(t capability.Transition) {
	name := events.EventCapabilityCeilingChanged
	switch t.Kind {
	case capability.TransitionPromoted:
		name = events.EventCapabilityPromoted
	case capability.TransitionDemoted:
		name = events.EventCapabilityDemoted
	case capability.TransitionResolved:
		name = events.EventCapabilityFailureResolved
	}
	events.EmitToBuffer(nil, name, map[string]any{
		"capability": t.Capability,
		"actor":      t.Actor,
		"from":       string(t.From),
		"to":         string(t.To),
		"reason":     t.Reason,
		"seq":        t.Seq,
	})
}
