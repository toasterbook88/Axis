package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/persist"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

func contextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Show or edit placement memory state",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show the current cluster placement state",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := state.Load()
			if err != nil {
				if st == nil {
					return err
				}
				if writeErr := printWarning(cmd.ErrOrStderr(), err); writeErr != nil {
					return writeErr
				}
			}
			if st != nil {
				state.Maintain(st)
			}
			return printOutput(cmd.OutOrStdout(), st, "json")
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Clear the cluster placement memory",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContextClear(cmd.OutOrStdout())
		},
	})

	cmd.AddCommand(contextPruneCmd())

	return cmd
}

func runContextClear(w io.Writer) error {
	release, err := persist.LockFile(state.Path())
	if err != nil {
		return err
	}
	defer release()

	if err := os.Remove(state.Path()); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(w, "Cluster state is already clear.")
			return nil
		}
		return fmt.Errorf("clear cluster state: %w", err)
	}
	fmt.Fprintln(w, "Cleared cluster state.")
	return nil
}

// storeSnapshot is the exact on-disk content of one store at the moment the
// prune read it, under lock. missing distinguishes "file absent" from "empty
// file" so rollback can restore either faithfully.
type storeSnapshot struct {
	path    string
	data    []byte
	missing bool
}

func readStoreSnapshot(path string) (storeSnapshot, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return storeSnapshot{path: path, missing: true}, nil
	}
	if err != nil {
		return storeSnapshot{}, err
	}
	return storeSnapshot{path: path, data: data}, nil
}

// restore puts a store back exactly as it was read.
func (s storeSnapshot) restore() error {
	if s.missing {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return persist.WritePrivateFileAtomic(s.path, s.data)
}

// backupSnapshots writes the already-read store contents into a fresh
// timestamped directory. It takes snapshots rather than re-reading the files,
// so the backup is guaranteed to be the exact version being pruned — a
// re-read could pick up a writer that landed after the prune parsed the store.
//
// MkdirTemp, not MkdirAll: the timestamp has one-second resolution and
// MkdirAll succeeds on an existing directory, so two applies within the same
// second would silently overwrite the first backup — losing exactly the copy
// needed to undo the first prune.
func backupSnapshots(snaps ...storeSnapshot) (string, error) {
	parent := persist.AxisDir()
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	// MkdirTemp creates the directory with 0o700 and a unique suffix.
	dir, err := os.MkdirTemp(parent, fmt.Sprintf("prune-backup-%s-", time.Now().UTC().Format("20060102T150405Z")))
	if err != nil {
		return "", err
	}
	for _, s := range snaps {
		if s.missing {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(s.path)), s.data, 0o600); err != nil {
			if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
				return "", fmt.Errorf(
					"write backup snapshot: %w; remove partial backup %s: %v",
					err, dir, cleanupErr)
			}
			return "", err
		}
	}
	return dir, nil
}

// Write seams, so tests can force a failure at EITHER write position and
// assert that both stores are rolled back.
var (
	saveStateStore  = func(s *state.ClusterState) error { return s.Save() }
	saveSkillsStore = func(s *skills.Store) error { return s.Save() }
)

// rollbackPrune restores every store the transaction attempted to write.
//
// It restores ATTEMPTED writes, not failed ones. persist.WriteFileAtomic
// (internal/persist/recovery.go:60) renames the temp file into place and only
// then fsyncs the parent directory — so a sync failure returns an error after
// the new contents are already live. An error from Save therefore does not
// mean the store is unchanged, and rolling back only "the other" store would
// leave a half-applied prune behind.
//
// Both locks are still held here, so no other writer has observed the
// intermediate state.
func rollbackPrune(cause error, backupDir string, attempted ...storeSnapshot) error {
	var failed []string
	for _, s := range attempted {
		if err := s.restore(); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", filepath.Base(s.path), err))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf(
			"prune failed (%v) AND rollback failed [%s]; restore both stores by hand from %s",
			cause, strings.Join(failed, "; "), backupDir)
	}
	return fmt.Errorf("prune failed, all stores rolled back: %w", cause)
}

type ledgerPruneBlock struct {
	node       string
	entries    int
	reservedMB int64
}

func ledgerPruneBlockers(entries []reservation.Entry, targets map[string]bool) []ledgerPruneBlock {
	norm := make(map[string]bool, len(targets))
	for name := range targets {
		norm[strings.ToLower(strings.TrimSpace(name))] = true
	}
	byNode := map[string]ledgerPruneBlock{}
	for _, entry := range entries {
		node := strings.TrimSpace(entry.Node)
		key := strings.ToLower(node)
		if node == "" || !norm[key] {
			continue
		}
		block := byNode[key]
		if block.node == "" {
			block.node = node
		}
		block.entries++
		block.reservedMB += entry.RAMMB
		byNode[key] = block
	}
	blocked := make([]ledgerPruneBlock, 0, len(byNode))
	for _, block := range byNode {
		blocked = append(blocked, block)
	}
	sort.Slice(blocked, func(i, j int) bool {
		return strings.ToLower(blocked[i].node) < strings.ToLower(blocked[j].node)
	})
	return blocked
}

func printPruneBlockers(w io.Writer, stateBlocks []state.PruneBlock, ledgerBlocks []ledgerPruneBlock) {
	fmt.Fprintln(w, "\nPrune blocked by execution or reservation state:")
	for _, block := range stateBlocks {
		fmt.Fprintf(
			w,
			"  - %s: state reserved_mb=%d active_tasks=%d active_execs=%d tracking_records=%d\n",
			block.Node,
			block.ReservedMB,
			block.ActiveTasks,
			block.ActiveExecs,
			block.TrackingRecords,
		)
	}
	for _, block := range ledgerBlocks {
		fmt.Fprintf(
			w,
			"  - %s: ledger entries=%d reserved_mb=%d\n",
			block.node,
			block.entries,
			block.reservedMB,
		)
	}
}

func runContextPrune(w io.Writer, targetNames []string, apply bool) error {
	if len(targetNames) == 0 {
		return fmt.Errorf("no nodes selected; pass --node NAME or --unknown-nodes")
	}

	targets := make(map[string]bool, len(targetNames))
	for _, n := range targetNames {
		targets[n] = true
	}

	sort.Strings(targetNames)
	fmt.Fprintf(w, "Nodes selected for removal (%d):\n", len(targetNames))
	for _, n := range targetNames {
		fmt.Fprintf(w, "  - %s\n", n)
	}

	// ---- Begin the transaction ----------------------------------------
	//
	// All three locks are held across the liveness preflight, read, backup,
	// and both writes. The ledger is first so no new reservation can land
	// between the preflight and the prune; the persisted-store order remains
	// state, then skills. Release is deferred and therefore happens in reverse.
	//
	// LoadUnlocked, not Load: state.Load persists pending migrations through
	// state.Update, which would deadlock against the lock we now hold.
	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	ledgerCtx, cancelLedgerLock := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelLedgerLock()
	if err := ledger.LockFile(ledgerCtx); err != nil {
		return fmt.Errorf("lock reservation ledger for prune preflight: %w", err)
	}
	defer ledger.UnlockFile()
	if err := ledger.LoadReadOnly(); err != nil {
		return fmt.Errorf("load reservation ledger for prune preflight: %w", err)
	}

	releaseState, err := persist.LockFile(state.Path())
	if err != nil {
		return err
	}
	defer releaseState()

	releaseSkills, err := persist.LockFile(skills.Path())
	if err != nil {
		return err
	}
	defer releaseSkills()

	// Exact on-disk bytes, read under lock. These are what gets backed up
	// and what rollback restores — never a re-read, which could pick up a
	// writer that landed after we parsed.
	stSnap, err := readStoreSnapshot(state.Path())
	if err != nil {
		return err
	}
	skSnap, err := readStoreSnapshot(skills.Path())
	if err != nil {
		return err
	}

	st, err := state.LoadUnlocked()
	if err != nil {
		return err
	}
	sk, err := skills.LoadUnlocked()
	if err != nil {
		return err
	}

	stateBlocks := st.PruneBlockers(targets)
	ledgerBlocks := ledgerPruneBlockers(ledger.Entries(), targets)
	if len(stateBlocks) > 0 || len(ledgerBlocks) > 0 {
		printPruneBlockers(w, stateBlocks, ledgerBlocks)
		if apply {
			return fmt.Errorf(
				"refusing to prune nodes with execution or reservation state; release active work and repair stale reservations first")
		}
		fmt.Fprintln(
			w,
			"\ndry run — nothing was pruned; --apply would be refused until the execution and reservation state is cleared.",
		)
		return nil
	}

	// LoadUnlocked skips migrations, but ClusterState.Save stamps the current
	// schema version unconditionally. Pruning a version-0 state and saving it
	// would therefore mark the file current while leaving surviving legacy
	// tombstones unconverted — and because every later Load treats a current
	// version as needing no migration, those records would be stranded for
	// good. Migrate in memory, under the lock we already hold; state.Load
	// would deadlock, since it persists migrations through state.Update.
	//
	// This runs before PruneNodes so migrated records are pruned as the
	// failure records they have become, and it mutates memory only: the
	// backup snapshot was taken above and still holds the exact original
	// bytes, and a dry run returns below without writing anything.
	state.MigratePending(st)

	stRep := st.PruneNodes(targets)
	if len(stRep.Blocked) > 0 {
		return fmt.Errorf("refusing to prune nodes with execution state")
	}
	skRep := sk.PruneNodes(targets)

	// Every field of both reports is printed. An operator cannot judge blast
	// radius from a subset, and the fields most easily omitted (failure
	// records, tombstones, whole skills) are the destructive ones.
	fmt.Fprintf(w, "\nRecords affected:\n")
	fmt.Fprintf(w, "  state.json\n")
	fmt.Fprintf(w, "    node entries        %d\n", stRep.Nodes)
	fmt.Fprintf(w, "    observations        %d\n", stRep.Observations)
	fmt.Fprintf(w, "    task history rows   %d\n", stRep.TaskHistory)
	fmt.Fprintf(w, "    failure records     %d\n", stRep.Failures)
	fmt.Fprintf(w, "    legacy tombstones   %d\n", stRep.Tombstones)
	fmt.Fprintf(w, "    recent decisions    %d\n", stRep.Decisions)
	fmt.Fprintf(w, "  skills.json\n")
	fmt.Fprintf(w, "    node counts         %d\n", skRep.NodeCounts)
	fmt.Fprintf(w, "    preferred-node refs %d\n", skRep.PreferredNodes)
	fmt.Fprintf(w, "    success evidence    %d\n", skRep.SuccessCount)
	fmt.Fprintf(w, "    SKILLS DELETED      %d", skRep.SkillsDeleted())
	if n := skRep.AutoTemplatesDeleted(); n > 0 {
		fmt.Fprintf(w, " (%d auto-discovered templates)", n)
	}
	fmt.Fprintln(w)

	// Whole-skill deletion is the least recoverable effect. Names come from
	// the prune calculation itself — reloading and diffing by ID would
	// mis-attribute deletions, because skill IDs are not unique.
	if len(skRep.Deleted) > 0 {
		fmt.Fprintln(w, "\nLearned skills that will be removed entirely:")
		for _, d := range skRep.Deleted {
			label := ""
			if d.Auto {
				label = "  [auto-discovered]"
			}
			fmt.Fprintf(w, "  - %s  %q%s\n", d.ID, d.Description, label)
		}
	}

	if stRep.Empty() && skRep.Empty() {
		fmt.Fprintln(w, "\nNothing to prune.")
		return nil
	}

	if !apply {
		fmt.Fprintln(w, "\ndry run — nothing above was pruned. Re-run with --apply to prune.")
		return nil
	}

	// Back up the exact snapshots being pruned, and print the path BEFORE
	// touching either file, so the recovery path is on screen even if the
	// process dies mid-write.
	dir, err := backupSnapshots(stSnap, skSnap)
	if err != nil {
		return fmt.Errorf("backup failed, refusing to prune: %w", err)
	}
	fmt.Fprintf(w, "\nBackup written to %s\n", dir)

	// Neither Update is used here: we already hold both locks, and calling
	// state.Update or skills.Update would deadlock. Save takes no lock.
	//
	// Each store is written ONLY if its own report is non-empty. Writing
	// both unconditionally would create a previously absent store, or
	// reserialize an untouched one and lose an operator's hand formatting.
	//
	// A pending migration alone therefore does not trigger a write: if this
	// prune removes nothing from state.json, the file is left exactly as it
	// was, still unmigrated, and the next ordinary state.Load migrates it
	// properly. Prune is not a migration command and does not become one.
	var attempted []storeSnapshot

	if !stRep.Empty() {
		attempted = append(attempted, stSnap)
		if err := saveStateStore(st); err != nil {
			return rollbackPrune(err, dir, attempted...)
		}
	}

	if !skRep.Empty() {
		attempted = append(attempted, skSnap)
		if err := saveSkillsStore(sk); err != nil {
			return rollbackPrune(err, dir, attempted...)
		}
	}

	fmt.Fprintln(w, "Pruned.")
	return nil
}

// unknownNodeNames returns every node name referenced by either store that is
// absent from nodes.yaml.
//
// It must scan every record type PruneNodes can affect. A node referenced only
// by an observation, a failure record, a legacy tombstone, a recent decision,
// or a learned skill's NodeCount would otherwise be reported as nothing to
// prune while its records stayed behind — a selection that silently
// under-reports what it missed is worse than one that offers nothing.
func unknownNodeNames(errOut io.Writer) ([]string, error) {
	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(cfg.Nodes))
	for _, n := range cfg.Nodes {
		known[strings.ToLower(strings.TrimSpace(n.Name))] = true
	}

	seen := map[string]bool{}
	note := func(name string) {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || known[strings.ToLower(trimmed)] {
			return
		}
		seen[trimmed] = true
	}

	// Selection is read-only with respect to healthy stores. In particular,
	// LoadUnlocked does not persist pending state migrations, so the default
	// dry run cannot rewrite state.json before runContextPrune starts.
	st, err := state.LoadUnlocked()
	if err != nil {
		if st == nil {
			return nil, err
		}
		if writeErr := printWarning(errOut, err); writeErr != nil {
			return nil, writeErr
		}
	}
	for name := range st.Nodes {
		note(name)
	}
	for _, r := range st.TaskHistory {
		note(r.Node)
	}
	for _, obs := range st.Observations {
		note(obs.Scope.Node)
	}
	for _, f := range st.Failures {
		note(f.Scope.Node)
	}
	for _, tomb := range st.Tombstones {
		note(tomb.NodeName)
	}
	for _, decision := range st.Decisions {
		if node, ok := state.DecisionNodeName(decision); ok {
			note(node)
		}
	}

	sk, err := skills.LoadUnlocked()
	if err != nil {
		if sk == nil {
			return nil, err
		}
		if writeErr := printWarning(errOut, err); writeErr != nil {
			return nil, writeErr
		}
	}
	for _, s := range sk.Skills {
		note(s.PreferredNode)
		for name := range s.NodeCount {
			note(name)
		}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func contextPruneCmd() *cobra.Command {
	var nodeNames []string
	var unknownNodes bool
	var apply bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove placement memory and learned-skill references for named nodes",
		Long: "Prune is destructive and defaults to a dry run.\n\n" +
			"Absence from nodes.yaml does not prove a record is stale — retired or " +
			"temporarily removed nodes may hold legitimate history. Nodes are always " +
			"listed before any write, and both stores are backed up before --apply. " +
			"Prune refuses any node that still carries execution state or reservation " +
			"ledger entries.\n\n" +
			"A dry run never rewrites a healthy state.json, skills.json, or ledger.json, " +
			"and it does not persist schema migrations. Acquiring the transaction locks " +
			"may create sibling .lock files. A state or skills store that fails to parse " +
			"may still be renamed aside for recovery, which is the normal AXIS corrupt-file " +
			"load behaviour rather than a prune.",
		RunE: func(cmd *cobra.Command, args []string) error {
			targets := append([]string(nil), nodeNames...)

			if unknownNodes {
				found, err := unknownNodeNames(cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				targets = append(targets, found...)
			}

			return runContextPrune(cmd.OutOrStdout(), targets, apply)
		},
	}

	cmd.Flags().StringArrayVar(&nodeNames, "node", nil, "node to prune (repeatable)")
	cmd.Flags().BoolVar(&unknownNodes, "unknown-nodes", false, "select every node absent from nodes.yaml")
	cmd.Flags().BoolVar(&apply, "apply", false, "write changes (default is a dry run)")
	return cmd
}
