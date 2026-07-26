# Truth-Integrity Containment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop AXIS surfaces from asserting things the machine contradicts — fix the self-defeating Ollama probe, stop the test suite writing into operator state, stop reporting certainty on non-classifications, and stop drawing network links that were never observed.

**Architecture:** Four independent defect fixes from `docs/evaluations/2026-07-25-truth-integrity-audit.md`. Each is self-contained and separately revertable. No new packages, no schema migrations, no new dependencies. Task 1 is ordered first because the defect worsens on every test run — including the test runs this plan itself performs.

**Tech Stack:** Go 1.26.1+, standard library, `gopkg.in/yaml.v3`. Tests use the stdlib `testing` package. Shell probes are POSIX `bash` executed locally and over SSH.

## Global Constraints

- Go 1.26.1+ required; `go.mod` is authoritative for the minimum.
- Dependency budget is fixed. **Do not add any dependency** for this work.
- Public repository: no real hostnames, private/Tailscale IPs, operator home paths, API keys, or model catalogs in code, tests, fixtures, or commit messages. Use `node-a`/`node-b`, `127.0.0.1`, and RFC 5737 (`192.0.2.0/24`) only.
- Every test that touches persistent state MUST call `t.Setenv("HOME", t.TempDir())` as its first statement.
- `make lint` runs `gofmt -l` (fails if dirty) and `go vet`. Both must pass before every commit.
- Full verification command: `make test` → `go test ./... -count=1 -timeout 180s`.
- Do not run `make test` on this host until Task 1 Step 5 is committed. See Task 1.
- **Never run the Go test suite against the real `~/.axis/`** — not to reproduce a defect, and not to verify one is fixed. Use `HOME=$(mktemp -d)`; `persist.AxisDir()` resolves via `os.UserHomeDir()`, which honours `$HOME`. Verifying under the operator's own store means a surviving leak *writes* before you detect it, and restoring from a copy then discards whatever a concurrent daemon refresh or execution legitimately wrote in the meantime. A disposable `HOME` makes the check "the sandbox stays empty", which is both stronger and non-destructive.
- Any command that deletes operator state must default to a dry run, name what it will remove, and back up before writing.
- Both persistent stores are mutated by more than one process. Every read-modify-write of `state.json` or `skills.json` must go through a lock-backed `Update`, never `Load` → mutate → `Save`.
- Advisory surfaces must not present themselves as cluster truth (`docs/doctrine.md` Truth Rule).

---

## File Structure

| File | Responsibility | Task |
| --- | --- | --- |
| `internal/execution/guarded_test.go` | Add `HOME` isolation to the leaking test | 1 |
| `cmd/axis/context.go` | New `axis context prune` subcommand (dry-run default, backup, explicit node selection) | 2 |
| `cmd/axis/context_test.go` | **Create.** Tests for prune | 2 |
| `internal/state/state.go` | `PruneNodes` removing records for named target nodes, including failures and tombstones | 2 |
| `internal/state/state_test.go` | Tests for state `PruneNodes` | 2 |
| `internal/skills/skills.go` | `PruneNodes` removing node evidence from learned skills; lock-backed `Update` | 2 |
| `internal/skills/autodiscover.go` | Migrate its `Save` onto `Update` | 2 |
| `internal/execution/guarded.go` | Migrate `recordSuccess`/`recordFailure` onto `skills.Update` | 2 |
| `internal/skills/skills_test.go` | Tests for skills `PruneNodes` | 2 |
| `internal/facts/tools.go` | Fix `OllamaDiscoveryScript` self-match and quoting | 3 |
| `internal/facts/collectors_test.go` | Hermetic tests executing the real discovery script | 3 |
| `internal/llmrouter/engine.go` | Reflex confidence + error sanitization | 4 |
| `internal/llmrouter/engine_internal_test.go` | **Create** (`package llmrouter`). Tests for reflex signal | 4 |
| `cmd/axis/dashboard.go` | Delete pairwise topology, `speedPriority`, and `sort` import | 5 |
| `cmd/axis/summary_test.go` | Assert no topology and no unlabeled route data | 5 |

---

### Task 1: Stop the test suite writing into operator state

**Why first:** `TestRunRemoteUsesVariableBasedTrap` has no `HOME` isolation, so it persists a fixture execution record into the real `~/.axis/state.json`. Measured contamination at audit time: 70 of 71 `task_history` rows and a 70-sample observation. Every `make test` deepens it, including the ones in this plan.

**Files:**
- Modify: `internal/execution/guarded_test.go:687`
- Test: same file (the test itself is the test)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: a safe `make test`. Every later task depends on this being committed first.

- [ ] **Step 1: Record the current contamination for comparison**

Run:

```bash
python3 -c "
import json,os
p=os.path.expanduser('~/.axis/state.json')
d=json.load(open(p))
th=d.get('task_history') or []
print('task_history rows:', len(th))
print('testnode rows:', sum(1 for r in th if 'testnode' in json.dumps(r)))
print('observations:', len(d.get('observations') or {}))
"
```

Write the three numbers down. Step 4 compares against them.

- [ ] **Step 2: Confirm the leak reproduces — under a disposable HOME**

`persist.AxisDir()` calls `os.UserHomeDir()`, which reads `$HOME` on Unix, so overriding `HOME` for the test process redirects every write into a throwaway directory.

**Never reproduce this defect against your real home.** The test writes an execution record *and* a skills entry (`recordSuccess` at `internal/execution/guarded.go:1194`), and a copied state file is not a restore.

Run:

```bash
export SANDBOX=$(mktemp -d)
mkdir -p "$SANDBOX/.axis"
HOME="$SANDBOX" go test ./internal/execution/ -run TestRunRemoteUsesVariableBasedTrap -count=1
echo "--- files created in the sandbox:"
ls -la "$SANDBOX/.axis/" 2>/dev/null || echo "(none)"
python3 -c "
import json,os,sys
p=os.path.join(os.environ['SANDBOX'],'.axis','state.json')
if not os.path.exists(p):
    print('no state written'); sys.exit()
d=json.load(open(p))
print('task_history rows written:', len(d.get('task_history') or []))
print('observations written:', len(d.get('observations') or {}))
"
```

`SANDBOX` must be **exported**, not merely assigned. A trailing `SANDBOX="$SANDBOX"` after `python3 -c '...'` becomes `sys.argv[1]`, not an environment entry, and `os.environ['SANDBOX']` then raises `KeyError`.

Expected: `state.json` (and likely `skills.json`) appear in the sandbox with at least one `task_history` row. That is the leak — it landed in your real `~/.axis/` on every prior run.

Keep `$SANDBOX` for Step 4.

- [ ] **Step 3: Add HOME isolation**

In `internal/execution/guarded_test.go`, make the first statement of `TestRunRemoteUsesVariableBasedTrap` match the ten sibling tests in the same file:

```go
func TestRunRemoteUsesVariableBasedTrap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var capturedCmds []string
	prev := NewRemoteExecutor
```

- [ ] **Step 4: Verify the leak is closed**

Run against a *fresh* sandbox so the check cannot be satisfied by leftovers:

```bash
SANDBOX2=$(mktemp -d)
mkdir -p "$SANDBOX2/.axis"
HOME="$SANDBOX2" go test ./internal/execution/ -run TestRunRemoteUsesVariableBasedTrap -count=1
echo "--- files created:"
ls -A "$SANDBOX2/.axis/" 2>/dev/null | wc -l
```

Expected: `0` — the isolated test now writes into its own `t.TempDir()`, so even the sandbox home stays empty.

Then confirm your real state was untouched throughout:

```bash
python3 -c "
import json,os
d=json.load(open(os.path.expanduser('~/.axis/state.json')))
print('task_history rows:', len(d.get('task_history') or []))
"
```

Expected: identical to the count recorded in Step 1.

- [ ] **Step 5: Sweep for other unisolated tests and commit**

Run:

```bash
for f in $(grep -rl "func Test" --include=*_test.go internal/ cmd/); do
  awk -v F="$f" '/^func Test/{name=$2; iso=0} /t\.Setenv\("HOME"/{iso=1} /^}/{if(name!=""&&iso==0) print F": "name; name=""}' "$f"
done | head -40
```

This lists candidates only — a test that never touches persistent state does not need isolation. For each hit, check whether it reaches `state.Load`, `state.Save`, `recordExecutionOutcome`, or a skills/ledger write. Add `t.Setenv("HOME", t.TempDir())` to any that do.

Then:

```bash
make lint
go test ./internal/execution/ -count=1
git add internal/execution/guarded_test.go
git commit -m "test: isolate HOME in guarded remote-trap test

TestRunRemoteUsesVariableBasedTrap reached recordExecutionOutcome without
HOME isolation, persisting fixture execution records into the operator's
real ~/.axis/state.json on every run. Ten sibling tests in the same file
already isolate HOME; this one did not.

Refs docs/evaluations/2026-07-25-truth-integrity-audit.md C3"
```

- [ ] **Step 6: Confirm the full suite is now safe**

Run the whole suite under a **disposable** `HOME`, with the Go toolchain caches pinned to their real locations, and assert that no `.axis` store was created:

```bash
SAFE=$(mktemp -d)
# The Go toolchain derives GOCACHE, GOPATH and GOMODCACHE from HOME. Without
# pinning them, the suite repopulates a module cache from scratch inside the
# sandbox — slow, and it guarantees the sandbox is never empty.
export GOCACHE=$(go env GOCACHE) GOPATH=$(go env GOPATH) GOMODCACHE=$(go env GOMODCACHE)

HOME="$SAFE" make test

echo "--- AXIS files written under a disposable HOME:"
find "$SAFE/.axis" -type f 2>/dev/null | sed "s|^$SAFE|\$HOME|"

echo "--- the two stores this task is about:"
for f in state.json skills.json; do
  test -e "$SAFE/.axis/$f" && echo "LEAK: $f" || echo "clean: $f"
done
```

Expected: `clean: state.json` and `clean: skills.json`.

**The sandbox `.axis` will NOT be empty, and that is not this task's failure.** Measured on a passing run, the suite also writes `snapshot.json`, `events.jsonl`, `event-sequence`, `ledger.json`, `chat-history.json` and `token` (plus their lock files). Those are a separate, wider instance of the same defect class — see "Unfinished: the other six stores" below. Task 1 closes the two stores the audit measured; asserting an empty `.axis` here would make this step permanently red for reasons Task 1 does not fix.

The assertion is scoped to `$SAFE/.axis` deliberately. The sandbox as a whole is *not* expected to stay empty — measured, even a single `go build` under a disposable `HOME` with caches pinned still creates `$HOME/.config/go`.

**Export on its own line, before `HOME` is overridden.** Bash applies assignment prefixes left to right, so the tempting one-liner

```bash
HOME="$SAFE" GOCACHE=$(go env GOCACHE) make test   # WRONG
```

expands `$(go env GOCACHE)` *after* `HOME` has already been reassigned, and pins the cache to the sandbox — the exact outcome the pinning exists to avoid.

Do **not** run this against your real `~/.axis/`. A snapshot-and-diff there detects a surviving leak only *after* it has written, and restoring from the copy would discard anything a concurrent daemon refresh or execution legitimately wrote during the run. Under a disposable `HOME` a surviving leak is caught with zero blast radius, and the listing names the file — which usually identifies the test.

If files appear, return to Step 5 with the paths as the clue.

If some unrelated test fails only under a clean `HOME`, that is itself a finding: it means the test depends on the operator's real configuration. Record it; do not "fix" it by restoring the real `HOME`.

#### Unfinished: the other six stores

Measured while executing Task 1 Step 6 — the suite, run under a disposable `HOME` with `state.json` and `skills.json` verified clean, still wrote:

| File | Sandbox evidence | Present in the operator's real store |
| --- | --- | --- |
| `chat-history.json` | messages `"hi"` / `"hello world"` | **Yes — byte-identical fixture, 150 bytes.** Confirmed contamination |
| `ledger.json` | reservation `node-a`, 1024 MB | Yes, currently 0 entries |
| `events.jsonl`, `event-sequence` | fixture events | Yes, also daemon-maintained |
| `snapshot.json` | fixture snapshot | Yes, also daemon-maintained |
| `token` | 64-byte token generated | Yes; the real file predates these runs |

`chat-history.json` is a confirmed second instance of audit finding C3: an operator-visible store carrying test fixtures.

**What the `token` evidence does and does not show.** The suite reaches production auth state: `auth.LoadOrGenerateToken` (`internal/auth/auth.go:26`) resolves `~/.axis/token` and, when the file is absent or fails its 64-hex-character validation, **generates and writes a new one**. A valid existing token is read and returned unchanged (`auth.go:53-55`), which is why the operator's token still carries a much older mtime despite many suite runs. So the accurate claim is: tests access production auth state and will create or regenerate a token when none is valid — not that they overwrite a good one.

**Out of scope for this plan, but not backlog.** C3 as audited covers `state.json` and `skills.json`, and those are what Tasks 1 and 2 fix and verify. Extending containment to six more stores mid-execution would widen the change well past what was reviewed, and each store needs its own provenance work: which tests write it, whether it is also daemon-maintained, and what a clean fixture looks like. `axis context prune` should **not** grow to cover them — it is a node-scoped remediation, and these are test-isolation defects.

This is an **immediate containment follow-up, not a backlog item**: the repository's standard `make test` currently writes operator state. It needs its own audit finding and its own plan.

Until then, the standing constraint in this plan — never run the suite against a real `HOME` — is doing more work than it first appeared to.

---

### Task 2: Remediate the existing state contamination

**Why:** Task 1 stops new writes. The historical fixture records remain, skewing `axis observations`, `axis task history`, and `axis skills`.

**Two stores, not one.** `ClusterState` (`~/.axis/state.json`) holds observations, task history, node entries, `Failures` (`failures.Store`, keyed by a scope hash that includes `node:`), and legacy `Tombstones` (each carrying `NodeName`). The ghost `preferred_node` lives in a *separate* store, `~/.axis/skills.json` (`internal/skills/skills.go`, `persist.AxisPath("skills.json")`), which `axis skills` reads independently. Pruning only `ClusterState` cannot repair it.

**Clearing node references is not enough.** Verified against this host's `skills.json`, the entire learned-skill store is one fixture-derived record:

```json
{"id":"20260724-173817","description":"test","success_count":1,
 "preferred_node":"testnode","node_count":{"testnode":1}}
```

Removing only `node_count` and `preferred_node` leaves `{"description":"test","success_count":1}` — `axis skills` still advertises a learned skill called `test` that no operator ever ran. The evidence must be removed with the node.

`RecordSuccess` (`internal/skills/skills.go:77`) increments `SuccessCount` and `NodeCount[node]` together on every call, so **`SuccessCount` equals the sum of `NodeCount`** for any skill learned at runtime. Subtracting the pruned nodes' counts is therefore exact arithmetic, not an estimate.

**But "zero evidence" cannot be the deletion test on its own.** `AutoDiscoverSkills` (`internal/skills/autodiscover.go:12`) builds a second, structurally different kind of skill — one per discovered CLI tool per node:

```go
skill := LearnedSkill{
    ID:            "auto-" + name + "-" + time.Now().Format("20060102"),
    Description:   "use " + name + " (auto-discovered on " + n.Name + ")",
    SuccessCount:  0,
    PreferredNode: n.Name,
    // NodeCount is never set — it stays nil
}
```

`SuccessCount: 0` and a nil `NodeCount`. A rule that deletes every zero-evidence skill would destroy **every** auto-discovered template on any non-empty prune, including templates for nodes that were never targeted.

So deletion is conditioned on the prune having actually **touched** the skill:

> Delete a skill only when this prune removed a reference to it *and* nothing survives — no `SuccessCount`, no `NodeCount` entry. A skill the prune never touched is never deleted, whatever its counts.

That handles both kinds without a provenance field: an auto-discovered template is touched exactly when its `PreferredNode` was targeted, which is the node it was discovered on and the only node it refers to.

*(`AutoDiscoverSkills` currently has no production caller — only its own test — which is why this host's `skills.json` contains no `auto-*` entries. That is not a reason to prune unsafely: the writer exists, older stores may hold its output, and a prune must be correct for every record the store can contain.)*

**Safety posture.** Absence from the current `nodes.yaml` does **not** prove contamination — retired, renamed, or temporarily removed nodes have legitimate history. This command is therefore destructive by nature and is built accordingly: **dry-run is the default**, node identities are always listed before any write, applying requires an explicit flag, and a timestamped backup of both stores is written before mutation.

**Failure contract — what is and is not guaranteed.** Prune spans two files, so it cannot be a single atomic write. State precisely what it does provide:

| Failure | Behaviour |
| --- | --- |
| Either store fails to read or parse | Nothing is written. The transaction aborts before backup |
| Backup cannot be written | Nothing is pruned. Backup failure is fatal by design |
| A store's report is empty | That store is **not written at all** — not created, not reserialized |
| `state.json` write fails | Every attempted write is rolled back from its under-lock snapshot |
| `skills.json` write fails | Every attempted write is rolled back — **including `state.json`**, which by then has already been written |
| Rollback itself fails | All errors are reported together, with the backup path, and the operator restores by hand |
| **Process is killed mid-write** | **Not covered.** `state.json` may be pruned while `skills.json` is not |

**Rollback restores attempted writes, not failed ones.** `persist.WriteFileAtomic` (`internal/persist/recovery.go:60`) renames the temp file into place at line 92 and fsyncs the parent directory at line 95 — so it can return an error *after* the new contents are already live. A `Save` error therefore proves nothing about whether the store changed, and treating the failing store as untouched would leave a half-applied prune.

The last row is the honest limit: there is no crash atomicity across two files, and this plan does not implement any. What it implements is (a) both locks held for the whole transaction, so no concurrent writer ever observes or overwrites an intermediate state, and (b) the backup directory path printed to the operator *before* the first write, so the recovery path is on screen even if the process dies immediately afterwards.

**Recovery from an interrupted prune:**

```bash
# The path was printed by the interrupted run.
cp <backup-dir>/state.json  ~/.axis/state.json
cp <backup-dir>/skills.json ~/.axis/skills.json
```

Both stores are restored to the exact bytes read at the start of the transaction. Any writes by other processes during the interrupted run were blocked by the locks, so nothing legitimate is lost.

**Lock discipline.** Both locks are acquired in one fixed order — `state.Path()`, then `skills.Path()` — held for the entire transaction, and released in reverse. Nothing inside the transaction calls `state.Update` or `skills.Update`: `flock` is tied to the open file description, so a second acquire in the same process blocks forever. `state.Load` is likewise forbidden inside it, because it persists pending migrations *through* `Update`.

**Files:**
- Modify: `internal/state/state.go` (add `PruneNodes`)
- Modify: `internal/skills/skills.go` (add `PruneNodes` and `Update`)
- Modify: `cmd/axis/context.go` (add `prune` subcommand)
- Test: `internal/state/state_test.go` (modify), `internal/skills/skills_test.go` (modify), `cmd/axis/context_test.go` (**create** — does not exist yet)

**Interfaces:**
- Consumes: Task 1 committed (so the suite does not re-contaminate during these tests).
- Produces:
  - `func (s *ClusterState) PruneNodes(targets map[string]bool) PruneReport`
  - `func (s *Store) PruneNodes(targets map[string]bool) SkillPruneReport` (package `skills`)
  - `func Update(mutator func(*Store) error) error` (package `skills`)
  - `axis context prune [--node NAME]... [--unknown-nodes] [--apply]`

**Why `skills.Update` is in scope.** `state.Update` (`internal/state/state.go:114`) serializes read-modify-write behind a `flock`; the skills store has no equivalent, and it has concurrent writers — `recordSuccess` at `internal/execution/guarded.go:1194` does `RecordSuccess` then `Save` on every successful execution. Two concurrent executions lose one update. Worse, `guarded.go:381` substitutes an **empty** `&skills.Store{}` when the runtime's store is nil, and the subsequent `Save` then truncates `skills.json` to whatever that one execution learned. Prune must not add a third unlocked writer to that file, and the lock is ~25 lines mirroring `state.Update`.

**Note on `PruneNodes` semantics:** the parameter is `targets` — nodes to **remove** — not `known` nodes to keep. Naming it for what it deletes makes the destructive direction explicit at every call site.

- [ ] **Step 1: Write the failing test for PruneNodes**

Add to `internal/state/state_test.go`:

```go
func TestPruneNodesRemovesUnknownNodeRecords(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := &ClusterState{
		Nodes: map[string]NodeState{
			"node-a":  {},
			"ghost":   {},
		},
		Observations: map[string]models.ExecutionObservation{
			"keep": {Scope: models.ObservationScope{Node: "node-a"}, SampleCount: 1},
			"drop": {Scope: models.ObservationScope{Node: "ghost"}, SampleCount: 70},
		},
		TaskHistory: []TaskExecutionRecord{
			{Node: "node-a"},
			{Node: "ghost"},
			{Node: "ghost"},
		},
		Failures: func() failures.Store {
			f := failures.NewStore()
			f.Record(models.FailureExecCrash, models.FailureScope{Node: "ghost"}, "fixture", nil)
			f.Record(models.FailureExecCrash, models.FailureScope{Node: "node-a"}, "real", nil)
			return f
		}(),
		Tombstones: map[string]TombstoneEntry{
			"t1": {TaskPattern: "x", NodeName: "ghost"},
			"t2": {TaskPattern: "y", NodeName: "node-a"},
		},
	}

	// targets = nodes to REMOVE
	rep := s.PruneNodes(map[string]bool{"ghost": true})

	if rep.Nodes != 1 || rep.Observations != 1 || rep.TaskHistory != 2 {
		t.Fatalf("unexpected prune report: %+v", rep)
	}
	if rep.Failures != 1 || rep.Tombstones != 1 {
		t.Fatalf("node-scoped failure records not pruned: %+v", rep)
	}
	if _, ok := s.Nodes["ghost"]; ok {
		t.Error("ghost node survived prune")
	}
	if _, ok := s.Observations["drop"]; ok {
		t.Error("ghost observation survived prune")
	}
	if len(s.TaskHistory) != 1 || s.TaskHistory[0].Node != "node-a" {
		t.Errorf("task history not pruned: %+v", s.TaskHistory)
	}
	if len(s.Failures) != 1 {
		t.Errorf("ghost failure record survived: %+v", s.Failures)
	}
	if _, ok := s.Tombstones["t1"]; ok {
		t.Error("ghost tombstone survived prune")
	}
}

func TestPruneNodesKeepsEverythingWhenAllKnown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := &ClusterState{
		Nodes:        map[string]NodeState{"node-a": {}},
		Observations: map[string]models.ExecutionObservation{
			"keep": {Scope: models.ObservationScope{Node: "node-a"}, SampleCount: 1},
		},
		TaskHistory: []TaskExecutionRecord{{Node: "node-a"}},
	}

	// No targets named: nothing may be removed.
	rep := s.PruneNodes(map[string]bool{})

	if rep.Nodes != 0 || rep.Observations != 0 || rep.TaskHistory != 0 {
		t.Fatalf("expected no-op prune, got %+v", rep)
	}
	if len(s.TaskHistory) != 1 {
		t.Error("no-op prune removed history")
	}
}

func TestSkillsPruneNodesRemovesEvidenceNotJustReferences(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := &Store{
		Skills: []LearnedSkill{
			{
				ID:            "s1",
				Description:   "mixed evidence",
				SuccessCount:  4, // == 3 + 1, the RecordSuccess invariant
				PreferredNode: "ghost",
				NodeCount:     map[string]int{"ghost": 3, "node-a": 1},
			},
			{
				ID:            "s2",
				Description:   "test", // the shape of this host's real contamination
				SuccessCount:  5,
				PreferredNode: "ghost",
				NodeCount:     map[string]int{"ghost": 5},
			},
		},
	}

	rep := s.PruneNodes(map[string]bool{"ghost": true})

	if rep.SkillsDeleted != 1 {
		t.Errorf("skill with no surviving evidence must be deleted, got %d", rep.SkillsDeleted)
	}
	if len(s.Skills) != 1 || s.Skills[0].ID != "s1" {
		t.Fatalf("expected only s1 to survive, got %+v", s.Skills)
	}

	surv := s.Skills[0]
	// Evidence is subtracted, not merely dereferenced.
	if surv.SuccessCount != 1 {
		t.Errorf("SuccessCount must drop by the pruned node counts: got %d, want 1", surv.SuccessCount)
	}
	if _, ok := surv.NodeCount["ghost"]; ok {
		t.Error("pruned node survived in NodeCount")
	}
	// Preference falls back to the surviving node.
	if surv.PreferredNode != "node-a" {
		t.Errorf("expected preferred to fall back to node-a, got %q", surv.PreferredNode)
	}
	// The invariant still holds after pruning.
	sum := 0
	for _, c := range surv.NodeCount {
		sum += c
	}
	if sum != surv.SuccessCount {
		t.Errorf("SuccessCount %d != sum(NodeCount) %d", surv.SuccessCount, sum)
	}
}

func TestSkillsPruneNodesSparesUntargetedAutoDiscoveredTemplates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// AutoDiscoverSkills shape: SuccessCount 0, nil NodeCount, PreferredNode
	// set to the node the tool was discovered on. A zero-evidence deletion
	// rule would destroy both of these on any prune.
	s := &Store{
		Skills: []LearnedSkill{
			{
				ID:            "auto-jq-20260724",
				Description:   "use jq (auto-discovered on node-a)",
				SuccessCount:  0,
				PreferredNode: "node-a",
			},
			{
				ID:            "auto-uv-20260724",
				Description:   "use uv (auto-discovered on ghost)",
				SuccessCount:  0,
				PreferredNode: "ghost",
			},
		},
	}

	rep := s.PruneNodes(map[string]bool{"ghost": true})

	if len(s.Skills) != 1 {
		t.Fatalf("expected exactly the node-a template to survive, got %+v", s.Skills)
	}
	if s.Skills[0].ID != "auto-jq-20260724" {
		t.Errorf("wrong template survived: %q", s.Skills[0].ID)
	}
	if s.Skills[0].PreferredNode != "node-a" {
		t.Errorf("untargeted template was modified: %q", s.Skills[0].PreferredNode)
	}
	if rep.SkillsDeleted != 1 || rep.AutoTemplatesDeleted != 1 {
		t.Errorf("expected 1 auto template deleted, got %+v", rep)
	}
}

func TestSkillsPruneNodesLeavesUntouchedSkillsAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// No reference to the pruned node anywhere: the skill must be bit-identical
	// afterwards, and must not count toward the report.
	s := &Store{
		Skills: []LearnedSkill{{
			ID: "s1", Description: "unrelated",
			SuccessCount: 3, PreferredNode: "node-a",
			NodeCount: map[string]int{"node-a": 3},
		}},
	}
	before := s.Skills[0]

	rep := s.PruneNodes(map[string]bool{"ghost": true})

	if !rep.Empty() {
		t.Errorf("prune touching nothing must report nothing, got %+v", rep)
	}
	if len(s.Skills) != 1 || s.Skills[0].SuccessCount != before.SuccessCount ||
		s.Skills[0].PreferredNode != before.PreferredNode {
		t.Errorf("untouched skill was modified: %+v", s.Skills)
	}
}

func TestSkillsPruneNodesKeepsSkillWithUnattributedEvidence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// A hand-edited or migrated store may carry SuccessCount that exceeds
	// the sum of NodeCount. That excess is not attributable to any node and
	// must not be destroyed, so the skill survives.
	s := &Store{
		Skills: []LearnedSkill{{
			ID: "s1", Description: "partly unattributed",
			SuccessCount: 9,
			NodeCount:    map[string]int{"ghost": 2},
		}},
	}

	s.PruneNodes(map[string]bool{"ghost": true})

	if len(s.Skills) != 1 {
		t.Fatal("skill with unattributed evidence must survive")
	}
	if s.Skills[0].SuccessCount != 7 {
		t.Errorf("SuccessCount should drop to 7, got %d", s.Skills[0].SuccessCount)
	}
}
```

And the transactional contract for `Update` itself — the API is the point of the change, so it needs direct coverage rather than being exercised only through prune:

```go
func TestSkillsUpdateReloadsFromDiskInsideLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// A stale in-memory store must not be able to clobber newer disk state:
	// this is the guarded.go:381 nil-store truncation path.
	seed := &Store{Skills: []LearnedSkill{{
		ID: "existing", Description: "already learned",
		SuccessCount: 5, NodeCount: map[string]int{"node-a": 5},
	}}}
	if err := seed.Save(); err != nil {
		t.Fatal(err)
	}

	if err := Update(func(s *Store) error {
		s.RecordSuccess("new thing", "echo hi", "node-a")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 2 {
		t.Fatalf("Update must build on the on-disk store, got %d skills: %+v",
			len(got.Skills), got.Skills)
	}
}

func TestSkillsUpdateSerializesConcurrentWriters(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := (&Store{}).Save(); err != nil {
		t.Fatal(err)
	}

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- Update(func(s *Store) error {
				s.RecordSuccess(fmt.Sprintf("skill-%d", i), "cmd", "node-a")
				return nil
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// Without serialization, concurrent read-modify-write loses updates and
	// this count comes back below writers.
	if len(got.Skills) != writers {
		t.Errorf("lost concurrent updates: got %d skills, want %d", len(got.Skills), writers)
	}
}
```

Additional imports for `internal/skills/skills_test.go`: `fmt`, `sync`.

Put all skills tests in `internal/skills/skills_test.go` (package `skills`), not in the state test file.

- [ ] **Step 2: Run the tests to verify they fail**

Run **both** packages — the skills tests live in `internal/skills`, and running only `internal/state` would leave every skills assertion unverified in the red stage:

```bash
go test ./internal/state/  -run TestPruneNodes    -v
go test ./internal/skills/ -run 'TestSkillsPrune|TestSkillsUpdate' -v
```

Expected: both FAIL to build — `s.PruneNodes undefined`, `skills.Update undefined`.

- [ ] **Step 3: Implement PruneNodes**

Add to `internal/state/state.go`:

```go
// PruneReport counts records removed by PruneNodes.
type PruneReport struct {
	Nodes        int
	Observations int
	TaskHistory  int
	Failures     int
	Tombstones   int
}

// Empty reports whether the prune removed nothing.
func (r PruneReport) Empty() bool {
	return r.Nodes == 0 && r.Observations == 0 && r.TaskHistory == 0 &&
		r.Failures == 0 && r.Tombstones == 0
}

// PruneNodes removes state records belonging to the named target nodes.
// targets holds nodes to REMOVE. An empty map removes nothing.
// It does not save; callers persist via Update.
//
// Node names are matched case-insensitively because failures.HashScope
// normalizes case, so an exact-match prune would leave failure records that
// the failure system still considers live for the pruned node.
func (s *ClusterState) PruneNodes(targets map[string]bool) PruneReport {
	var rep PruneReport
	if len(targets) == 0 {
		return rep
	}

	norm := make(map[string]bool, len(targets))
	for name := range targets {
		norm[strings.ToLower(strings.TrimSpace(name))] = true
	}
	hit := func(name string) bool {
		return name != "" && norm[strings.ToLower(strings.TrimSpace(name))]
	}

	for name := range s.Nodes {
		if hit(name) {
			delete(s.Nodes, name)
			rep.Nodes++
		}
	}

	for key, obs := range s.Observations {
		if hit(obs.Scope.Node) {
			delete(s.Observations, key)
			rep.Observations++
		}
	}

	kept := s.TaskHistory[:0]
	for _, r := range s.TaskHistory {
		if hit(r.Node) {
			rep.TaskHistory++
			continue
		}
		kept = append(kept, r)
	}
	s.TaskHistory = kept

	// Node-scoped failure records outlive the node they blame. Leaving them
	// keeps a pruned node's tombstones influencing placement exclusions.
	for key, f := range s.Failures {
		if hit(f.Scope.Node) {
			delete(s.Failures, key)
			rep.Failures++
		}
	}

	for key, t := range s.Tombstones {
		if hit(t.NodeName) {
			delete(s.Tombstones, key)
			rep.Tombstones++
		}
	}

	return rep
}
```

`strings` is already imported by `internal/state/state.go`.

Failure records are keyed by `failures.HashScope`, which includes `node:`, but the hash is not reversible — the node must be read from `record.Scope.Node`, which is what the loop above does. Records with an empty `Scope.Node` are cluster-wide and are never pruned.

This mutates `ClusterState` only. The skills store is separate and is handled next.

Add to `internal/skills/skills.go`:

```go
// DeletedSkill identifies a learned skill removed in its entirety.
//
// Identity is captured here, at the moment of deletion, because it cannot be
// recovered afterwards: skill IDs are NOT unique. RecordSuccess derives them
// from time.Now().Format("20060102-150405") — one-second resolution — and
// AutoDiscoverSkills from a date alone. Reloading the store and diffing by ID
// would mis-attribute deletions whenever two skills collide.
type DeletedSkill struct {
	ID          string
	Description string
	Auto        bool // ID carries the auto-discovery prefix; reporting only
	SuccessLost int  // evidence this skill contributed to SuccessCount
}

// SkillPruneReport records what PruneNodes removed.
//
// Field semantics — all counts include references removed as part of a whole
// skill deletion, so the totals describe everything the prune destroyed:
//
//	NodeCounts     NodeCount map entries removed, across all skills
//	PreferredNodes PreferredNode references removed, whether reassigned to a
//	               surviving node or destroyed with the skill
//	SuccessCount   success evidence subtracted, including from deleted skills
//	Deleted        skills removed entirely, with identity captured in place
type SkillPruneReport struct {
	NodeCounts     int
	PreferredNodes int
	SuccessCount   int
	Deleted        []DeletedSkill
}

// SkillsDeleted returns the number of skills removed entirely.
func (r SkillPruneReport) SkillsDeleted() int { return len(r.Deleted) }

// AutoTemplatesDeleted returns how many deleted skills were auto-discovered
// templates. Reporting only — never a pruning decision.
func (r SkillPruneReport) AutoTemplatesDeleted() int {
	n := 0
	for _, d := range r.Deleted {
		if d.Auto {
			n++
		}
	}
	return n
}

// Empty reports whether the prune removed nothing.
func (r SkillPruneReport) Empty() bool {
	return r.NodeCounts == 0 && r.PreferredNodes == 0 &&
		r.SuccessCount == 0 && len(r.Deleted) == 0
}

// PruneNodes removes the named target nodes' evidence from every learned
// skill. RecordSuccess increments SuccessCount and NodeCount[node] together,
// so a node's contribution to SuccessCount is exactly its NodeCount value and
// can be subtracted precisely.
//
// Deletion requires that this prune actually TOUCHED the skill — removed a
// NodeCount entry or a PreferredNode reference — and that nothing survives
// afterwards. Zero evidence alone is not sufficient: AutoDiscoverSkills
// creates templates with SuccessCount 0 and a nil NodeCount, and those must
// survive a prune aimed at an unrelated node.
//
// It does not save; callers persist via Update.
func (s *Store) PruneNodes(targets map[string]bool) SkillPruneReport {
	var rep SkillPruneReport
	if len(targets) == 0 {
		return rep
	}

	norm := make(map[string]bool, len(targets))
	for name := range targets {
		norm[strings.ToLower(strings.TrimSpace(name))] = true
	}
	hit := func(name string) bool {
		return name != "" && norm[strings.ToLower(strings.TrimSpace(name))]
	}

	kept := s.Skills[:0]
	for i := range s.Skills {
		sk := s.Skills[i]
		touched := false
		lost := 0

		for node, count := range sk.NodeCount {
			if hit(node) {
				delete(sk.NodeCount, node)
				rep.NodeCounts++
				sk.SuccessCount -= count
				rep.SuccessCount += count
				lost += count
				touched = true
			}
		}
		// A store edited by hand or migrated may carry SuccessCount in
		// excess of sum(NodeCount). Never drive it negative.
		if sk.SuccessCount < 0 {
			sk.SuccessCount = 0
		}

		preferredTargeted := hit(sk.PreferredNode)
		if preferredTargeted {
			// Counted whether the reference is reassigned below or
			// destroyed with the skill: both remove it.
			rep.PreferredNodes++
			touched = true
		}

		// Untouched skills are left exactly as they were — including
		// auto-discovered templates for nodes that were not targeted.
		if !touched {
			kept = append(kept, sk)
			continue
		}

		if sk.SuccessCount == 0 && len(sk.NodeCount) == 0 {
			// Every reference this skill had was to a pruned node.
			// Capture identity here — it is unrecoverable afterwards.
			rep.Deleted = append(rep.Deleted, DeletedSkill{
				ID:          sk.ID,
				Description: sk.Description,
				Auto:        strings.HasPrefix(sk.ID, "auto-"),
				SuccessLost: lost,
			})
			continue
		}

		if preferredTargeted {
			// Fall back to the surviving node with the highest count,
			// breaking ties by name for determinism.
			names := make([]string, 0, len(sk.NodeCount))
			for node := range sk.NodeCount {
				names = append(names, node)
			}
			sort.Strings(names)
			best, bestCount := "", -1
			for _, node := range names {
				if sk.NodeCount[node] > bestCount {
					best, bestCount = node, sk.NodeCount[node]
				}
			}
			sk.PreferredNode = best
		}

		kept = append(kept, sk)
	}
	s.Skills = kept

	return rep
}
```

The `auto-` ID prefix is used **only for reporting**, never for the deletion decision — a template whose ID convention changed would still be pruned correctly by the touched/surviving test.

`sort` is already imported by `internal/skills/skills.go`; add `strings` — it is imported too (`RecordSuccess` uses `strings.EqualFold`).

`sk` is a **copy** here, taken by value so a deleted skill is dropped by not appending. `sk.NodeCount` is a map, so the `delete` calls mutate the shared backing map — intended, since the skill is either kept (with the map correctly pruned) or discarded.

First extract the lock into `internal/persist`, so both stores use one implementation and the cross-store ordering rule has a single home. Create `internal/persist/lock.go`:

```go
// LockFile acquires an exclusive advisory lock for the store at path, using a
// sibling "<path>.lock" file. The returned release function is idempotent.
//
// LOCK ORDERING. A caller taking more than one store lock MUST acquire them
// in this order, and release in reverse:
//
//	1. state.Path()
//	2. skills.Path()
//
// DEADLOCK. flock is associated with the open file description, so a second
// Open+Flock of the same lock file blocks even within one process. Never call
// state.Update or skills.Update while holding a lock — including indirectly
// via state.Load, which persists pending migrations through Update.
func LockFile(path string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening lock for %s: %w", filepath.Base(path), err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %w", filepath.Base(path), err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
		})
	}, nil
}
```

Imports: `fmt`, `os`, `path/filepath`, `sync`, `syscall`.

Refactor the existing `state.Update` (`internal/state/state.go:114`) onto it, preserving its current behaviour exactly — it already uses the same `<path>.lock` convention and `LOCK_EX`. Then add the skills equivalent:

```go
// LoadUnlocked reads the skills store without acquiring the lock. Callers that
// already hold it via persist.LockFile MUST use this rather than Load.
func LoadUnlocked() (*Store, error) { return Load() }

// Update serializes a read-modify-write transaction against the latest skills
// store on disk. An unlocked RecordSuccess/Save from a concurrent execution
// (internal/execution/guarded.go) would otherwise be lost.
//
// Never call Update while holding a store lock; see persist.LockFile.
func Update(mutator func(*Store) error) error {
	if mutator == nil {
		return fmt.Errorf("skills update requires a mutator")
	}

	release, err := persist.LockFile(path())
	if err != nil {
		return err
	}
	defer release()

	latest, err := LoadUnlocked()
	if err != nil && latest == nil {
		return err
	}
	if err := mutator(latest); err != nil {
		return err
	}
	return latest.Save()
}
```

`skills.Load` is already lock-free and runs no migrations, so `LoadUnlocked` is a documented alias; it exists so call sites state which discipline they are under.

`internal/state` needs the same seam, and there the distinction is **not** cosmetic — `state.Load` runs pending migrations and persists them via `Update`, which self-deadlocks under a held lock:

```go
// LoadUnlocked reads the state file without acquiring the lock and without
// running migrations. Callers holding the lock via persist.LockFile MUST use
// this: state.Load persists migrations through Update and would deadlock.
func LoadUnlocked() (*ClusterState, error) { return loadStateFile(Path()) }
```

`ClusterState.Save` (`internal/state/state.go:741`) already takes no lock, so it is safe to call while holding one.

**Every existing writer must migrate in this same change.** A lock that only one writer acquires does not serialize anything — an unlocked `Save` races straight through a locked prune and overwrites it, which is the exact failure `Update` was added to prevent. There are exactly three writers of `skills.json` in the tree:

| Site | Current | Becomes |
| --- | --- | --- |
| `internal/skills/autodiscover.go:53` | `_ = s.Save()` | mutate inside `Update` |
| `internal/execution/guarded.go:1199` (`recordSuccess`) | `_ = skillStore.Save()` | `Update` |
| `internal/execution/guarded.go:1207` (`recordFailure`) | `_ = skillStore.Save()` | `Update` |

For the two `guarded.go` sites, the in-memory `skillStore` is also read elsewhere in the execution path, so mutate the locked copy and keep the caller's view consistent:

```go
func recordSuccess(skillStore *skills.Store, description, command, node string) {
	if skillStore == nil {
		return
	}
	// Mutate the in-memory store so callers holding it see the update,
	// and apply the same mutation to the locked on-disk copy.
	skillStore.RecordSuccess(description, command, node)
	_ = skills.Update(func(s *skills.Store) error {
		s.RecordSuccess(description, command, node)
		return nil
	})
}
```

`recordFailure` takes the same shape with `RecordFailure`.

For `AutoDiscoverSkills`, replace the trailing `_ = s.Save()` by performing the append inside `Update` and returning the resulting store.

**This also closes the truncation path.** `internal/execution/guarded.go:381` substitutes an empty `&skills.Store{}` when the runtime store is nil; under the current code the subsequent `Save` writes that empty store plus one entry over the operator's whole file. Routing through `Update` — which reloads from disk inside the lock — means the nil-store fallback can no longer truncate.

Add a note to the Task 2 commit message that this migration is included; it is a behavioural change to the execution path, not just new API surface.

`TaskExecutionRecord.Node` is a `string` (`internal/state/state.go:54`) and `models.ExecutionObservation.Scope.Node` is the observation's node field — both verified against the current tree, so the code above compiles as written.

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/state/  -run TestPruneNodes    -v
go test ./internal/skills/ -run 'TestSkillsPrune|TestSkillsUpdate' -v
go test ./internal/skills/ -race -count=1
```

Expected: PASS throughout. The `-race` run covers `TestSkillsUpdateSerializesConcurrentWriters`.

- [ ] **Step 5: Write the failing test for the CLI subcommand**

Add to `cmd/axis/context_test.go`:

```go
func TestContextPruneDefaultsToDryRunAndNamesNodes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st := &state.ClusterState{
		Nodes:       map[string]state.NodeState{"ghost": {}},
		TaskHistory: []state.TaskExecutionRecord{{Node: "ghost"}},
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	// apply=false is the default posture.
	if err := runContextPrune(&buf, []string{"ghost"}, false); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if !strings.Contains(out, "ghost") {
		t.Errorf("prune must name the nodes it would remove, got: %s", out)
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("expected dry-run notice, got: %s", out)
	}

	reloaded, err := state.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.TaskHistory) != 1 {
		t.Errorf("dry run modified state: %+v", reloaded.TaskHistory)
	}
}

func TestContextPruneApplyWritesBackupAndPrunesBothStores(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	st := &state.ClusterState{
		Nodes:       map[string]state.NodeState{"ghost": {}},
		TaskHistory: []state.TaskExecutionRecord{{Node: "ghost"}, {Node: "node-a"}},
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	sk := &skills.Store{Skills: []skills.LearnedSkill{{
		ID: "s1", Description: "x", SuccessCount: 2, PreferredNode: "ghost",
		NodeCount: map[string]int{"ghost": 2},
	}}}
	if err := sk.Save(); err != nil {
		t.Fatal(err)
	}
	skillsBefore, err := os.ReadFile(skills.Path())
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := runContextPrune(&buf, []string{"ghost"}, true); err != nil {
		t.Fatal(err)
	}

	reloaded, err := state.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.TaskHistory) != 1 || reloaded.TaskHistory[0].Node != "node-a" {
		t.Errorf("state not pruned: %+v", reloaded.TaskHistory)
	}

	reloadedSkills, err := skills.Load()
	if err != nil {
		t.Fatal(err)
	}
	// All of s1's evidence came from the pruned node, so the skill is gone.
	if len(reloadedSkills.Skills) != 0 {
		t.Errorf("skill backed only by pruned node survived: %+v", reloadedSkills.Skills)
	}

	matches, _ := filepath.Glob(filepath.Join(home, ".axis", "prune-backup-*"))
	if len(matches) != 1 {
		t.Fatalf("expected exactly one backup directory, got %v", matches)
	}
	// The backup must hold the PRE-prune contents, or it is not a backup.
	backedUp, err := os.ReadFile(filepath.Join(matches[0], "skills.json"))
	if err != nil {
		t.Fatalf("skills.json missing from backup: %v", err)
	}
	if !bytes.Equal(backedUp, skillsBefore) {
		t.Errorf("backup does not match pre-prune skills.json\n got: %s\nwant: %s", backedUp, skillsBefore)
	}
	if _, err := os.Stat(filepath.Join(matches[0], "state.json")); err != nil {
		t.Errorf("state.json missing from backup: %v", err)
	}
}

func TestContextPruneNamesDeletedSkillsWithDuplicateIDs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := (&state.ClusterState{
		Nodes: map[string]state.NodeState{"ghost": {}},
	}).Save(); err != nil {
		t.Fatal(err)
	}
	// Skill IDs are NOT unique: RecordSuccess derives them from a
	// one-second timestamp. Two skills sharing an ID, only one pruned.
	sk := &skills.Store{Skills: []skills.LearnedSkill{
		{ID: "20260724-173817", Description: "doomed", SuccessCount: 1,
			PreferredNode: "ghost", NodeCount: map[string]int{"ghost": 1}},
		{ID: "20260724-173817", Description: "survivor", SuccessCount: 1,
			PreferredNode: "node-a", NodeCount: map[string]int{"node-a": 1}},
	}}
	if err := sk.Save(); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := runContextPrune(&buf, []string{"ghost"}, false); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if !strings.Contains(out, `"doomed"`) {
		t.Errorf("deleted skill not named in output:\n%s", out)
	}
	if strings.Contains(out, `"survivor"`) {
		t.Errorf("surviving skill reported as deleted — ID collision mis-attributed:\n%s", out)
	}
}

// seedBothStores writes a state and a skills store that both reference
// "ghost", and returns their exact on-disk bytes.
func seedBothStores(t *testing.T) (stateBytes, skillsBytes []byte) {
	t.Helper()
	if err := (&state.ClusterState{
		Nodes:       map[string]state.NodeState{"ghost": {}},
		TaskHistory: []state.TaskExecutionRecord{{Node: "ghost"}, {Node: "node-a"}},
	}).Save(); err != nil {
		t.Fatal(err)
	}
	if err := (&skills.Store{Skills: []skills.LearnedSkill{{
		ID: "s1", Description: "x", SuccessCount: 1,
		PreferredNode: "ghost", NodeCount: map[string]int{"ghost": 1},
	}}}).Save(); err != nil {
		t.Fatal(err)
	}
	var err error
	if stateBytes, err = os.ReadFile(state.Path()); err != nil {
		t.Fatal(err)
	}
	if skillsBytes, err = os.ReadFile(skills.Path()); err != nil {
		t.Fatal(err)
	}
	return stateBytes, skillsBytes
}

// TestContextPruneRollsBackBothStoresOnWriteFailure covers BOTH write
// positions. WriteFileAtomic can rename successfully and then fail its
// directory sync, so a failing Save may already have changed the file — the
// simulated failures below therefore also mutate the store, and the
// transaction must restore every attempted write regardless.
func TestContextPruneRollsBackBothStoresOnWriteFailure(t *testing.T) {
	cases := []struct {
		name string
		fail string // "state" or "skills"
	}{
		{"state write fails after committing", "state"},
		{"skills write fails after committing", "skills"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			stateBefore, skillsBefore := seedBothStores(t)

			prevState, prevSkills := saveStateStore, saveSkillsStore
			t.Cleanup(func() { saveStateStore, saveSkillsStore = prevState, prevSkills })

			// Write-then-error: the store IS modified, and an error is
			// still returned. This is exactly WriteFileAtomic's behaviour
			// when the parent-directory fsync fails.
			if tc.fail == "state" {
				saveStateStore = func(s *state.ClusterState) error {
					_ = prevState(s)
					return errors.New("sync parent dir: disk failure")
				}
			} else {
				saveSkillsStore = func(s *skills.Store) error {
					_ = prevSkills(s)
					return errors.New("sync parent dir: disk failure")
				}
			}

			var buf bytes.Buffer
			err := runContextPrune(&buf, []string{"ghost"}, true)
			if err == nil {
				t.Fatal("expected an error when the write fails")
			}
			if !strings.Contains(err.Error(), "rolled back") {
				t.Errorf("error should report rollback, got: %v", err)
			}

			stateAfter, readErr := os.ReadFile(state.Path())
			if readErr != nil {
				t.Fatal(readErr)
			}
			skillsAfter, readErr := os.ReadFile(skills.Path())
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(stateBefore, stateAfter) {
				t.Errorf("state.json not rolled back:\nbefore %s\nafter  %s", stateBefore, stateAfter)
			}
			if !bytes.Equal(skillsBefore, skillsAfter) {
				t.Errorf("skills.json not rolled back:\nbefore %s\nafter  %s", skillsBefore, skillsAfter)
			}
		})
	}
}

func TestContextPruneLeavesUnaffectedStoresUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Only skills references the pruned node. state.json does not exist at
	// all, and must not be created by a prune that has nothing to do there.
	if err := (&skills.Store{Skills: []skills.LearnedSkill{{
		ID: "s1", Description: "ghost only", SuccessCount: 1,
		PreferredNode: "ghost", NodeCount: map[string]int{"ghost": 1},
	}}}).Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state.Path()); !os.IsNotExist(err) {
		t.Fatalf("precondition: state.json should be absent, got %v", err)
	}

	var buf bytes.Buffer
	if err := runContextPrune(&buf, []string{"ghost"}, true); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(state.Path()); !os.IsNotExist(err) {
		t.Error("prune created state.json despite having nothing to prune there")
	}

	got, err := skills.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 0 {
		t.Errorf("skills not pruned: %+v", got.Skills)
	}
}

func TestContextPruneSerializesAgainstConcurrentSkillsWriter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := (&state.ClusterState{
		Nodes: map[string]state.NodeState{"ghost": {}},
	}).Save(); err != nil {
		t.Fatal(err)
	}
	if err := (&skills.Store{Skills: []skills.LearnedSkill{{
		ID: "doomed", Description: "ghost only", SuccessCount: 1,
		PreferredNode: "ghost", NodeCount: map[string]int{"ghost": 1},
	}}}).Save(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 4; i++ {
			_ = skills.Update(func(s *skills.Store) error {
				s.RecordSuccess(fmt.Sprintf("concurrent-%d", i), "cmd", "node-a")
				return nil
			})
		}
	}()

	var buf bytes.Buffer
	pruneErr := runContextPrune(&buf, []string{"ghost"}, true)
	wg.Wait()
	if pruneErr != nil {
		t.Fatal(pruneErr)
	}

	got, err := skills.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Five atomic operations (four Updates + one prune) serialize under the
	// lock, so EVERY legal ordering yields the same result: the four
	// concurrent skills present, "doomed" gone. A count below four means an
	// Update was lost; five means the prune was clobbered.
	if len(got.Skills) != 4 {
		t.Errorf("lost an update or clobbered the prune: got %d skills, want 4: %+v",
			len(got.Skills), got.Skills)
	}
	for _, sk := range got.Skills {
		if sk.ID == "doomed" {
			t.Error("pruned skill survived a concurrent write")
		}
		if sk.PreferredNode == "ghost" || sk.NodeCount["ghost"] > 0 {
			t.Errorf("pruned node reference survived: %+v", sk)
		}
	}
}

func TestContextPruneBackupsDoNotCollide(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	st := &state.ClusterState{
		Nodes:       map[string]state.NodeState{"ghost": {}, "ghost2": {}},
		TaskHistory: []state.TaskExecutionRecord{{Node: "ghost"}, {Node: "ghost2"}},
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}

	// Two applies inside the same wall-clock second must not overwrite
	// each other's backup.
	for _, n := range []string{"ghost", "ghost2"} {
		var buf bytes.Buffer
		if err := runContextPrune(&buf, []string{n}, true); err != nil {
			t.Fatal(err)
		}
	}

	matches, _ := filepath.Glob(filepath.Join(home, ".axis", "prune-backup-*"))
	if len(matches) != 2 {
		t.Errorf("expected 2 distinct backup directories, got %v", matches)
	}
}
```

Imports needed: `bytes`, `errors`, `fmt`, `os`, `path/filepath`, `strings`, `sync`, `testing`, plus the `state` and `skills` packages.

Seven tests, all sharing the `TestContextPrune` prefix: dry-run naming, apply with backup, deleted-skill naming under duplicate IDs, rollback at **both** write positions, untouched-store preservation, concurrent-writer serialization, and backup non-collision.

- [ ] **Step 6: Run it to verify it fails**

```bash
go test ./cmd/axis/ -run 'TestContextPrune' -count=1 -v
```

The pattern is `TestContextPrune` — a **prefix** shared by all seven tests above. An earlier draft used `-run TestContextPruneDryRun`, which matches no test in the file: Go reports `ok ... no tests to run` and **exits 0**, so the red stage would have silently passed while verifying nothing.

Expected: FAIL to build — `runContextPrune`, `saveStateStore`, `saveSkillsStore` undefined. Confirm the output names all seven tests once they compile; if you see `no tests to run` or `[no test files]`, the pattern or the file location is wrong, not the code.

- [ ] **Step 7: Implement the subcommand**

Add to `cmd/axis/context.go`, and register `contextPruneCmd()` on the existing `context` command alongside `show` and `clear`:

```go
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
	return persist.WriteFileAtomic(s.path, s.data, 0o600)
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
// (internal/persist/recovery.go:60) renames the temp file into place at line
// 92 and only then fsyncs the parent directory at line 95 — so a sync failure
// returns an error after the new contents are already live. An error from
// Save therefore does not mean the store is unchanged, and rolling back only
// "the other" store would leave a half-applied prune behind.
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
	// Both locks are held across read, backup, and both writes, so nothing
	// can slip between the version we back up and the version we prune.
	// Order is persist.LockFile's documented order (state, then skills);
	// release is deferred, so it happens in reverse.
	//
	// LoadUnlocked, not Load: state.Load persists pending migrations through
	// state.Update, which would deadlock against the lock we now hold.
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

	stRep := st.PruneNodes(targets)
	skRep := sk.PruneNodes(targets)

	// Every field of both reports is printed. An operator cannot judge blast
	// radius from a subset, and the fields omitted from an earlier draft
	// (failure records, tombstones, whole skills) are the destructive ones.
	fmt.Fprintf(w, "\nRecords affected:\n")
	fmt.Fprintf(w, "  state.json\n")
	fmt.Fprintf(w, "    node entries        %d\n", stRep.Nodes)
	fmt.Fprintf(w, "    observations        %d\n", stRep.Observations)
	fmt.Fprintf(w, "    task history rows   %d\n", stRep.TaskHistory)
	fmt.Fprintf(w, "    failure records     %d\n", stRep.Failures)
	fmt.Fprintf(w, "    legacy tombstones   %d\n", stRep.Tombstones)
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
// by an observation, a failure record, a legacy tombstone, or a learned
// skill's NodeCount would otherwise be reported as nothing to prune while its
// records stayed behind — a selection that silently under-reports what it
// missed is worse than one that offers nothing.
func unknownNodeNames() ([]string, error) {
	cfg, err := config.Load()
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

	st, err := state.Load()
	if err != nil {
		return nil, err
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

	sk, err := skills.Load()
	if err != nil {
		return nil, err
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
			"listed before any write, and both stores are backed up before --apply.\n\n" +
			"A dry run prunes nothing, but it is not guaranteed to leave the files " +
			"byte-identical: reading state.json applies any pending schema migration, " +
			"and a store that fails to parse is renamed aside for recovery. Those are " +
			"the normal load-path behaviours of every AXIS command, not effects of prune.",
		RunE: func(cmd *cobra.Command, args []string) error {
			targets := append([]string(nil), nodeNames...)

			if unknownNodes {
				found, err := unknownNodeNames()
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
```

Register `contextPruneCmd()` on the existing `context` command beside `show` and `clear` (`cmd/axis/context.go:12`, which already calls `cmd.AddCommand` twice). Imports to add: `os`, `path/filepath`, `sort`, `strings`, `time`, plus `internal/persist` and `internal/skills`. Follow the surrounding file's conventions for config loading and command wiring.

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./cmd/axis/ -run TestContextPrune -v`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
make lint
go test ./internal/state/ ./internal/skills/ ./internal/execution/ ./cmd/axis/ -count=1
go test ./internal/skills/ ./internal/execution/ -race -count=1
git add internal/state/state.go internal/state/state_test.go \
        internal/skills/skills.go internal/skills/skills_test.go \
        internal/skills/autodiscover.go \
        internal/execution/guarded.go \
        cmd/axis/context.go cmd/axis/context_test.go
git commit -m "feat(context): add prune for per-node placement and skill records

Adds ClusterState.PruneNodes, skills.Store.PruneNodes, and
'axis context prune --node NAME [--apply]' to remove records for explicitly
named nodes from both ~/.axis/state.json and ~/.axis/skills.json, covering
observations, task history, failure records and legacy tombstones on the
state side and node evidence on the skills side.

Destructive by nature, so: dry run by default, every affected record class
counted and whole-skill deletions named before any write, both stores backed
up before apply.

Also adds skills.Update, mirroring state.Update's flock discipline, and
migrates all three existing skills writers (autodiscover, recordSuccess,
recordFailure) onto it. A lock only one writer acquires serializes nothing.
This additionally closes a truncation path: recordSuccess could Save an
empty fallback store over the operator's whole file.

Remediates fixture contamination left by the unisolated test fixed in the
previous commit.

Refs docs/evaluations/2026-07-25-truth-integrity-audit.md C3"
```

- [ ] **Step 10: Remediate this host**

This step mutates real operator state. Get explicit operator go-ahead before the `--apply` run.

First inspect what is actually there, and decide per node — do not assume every unknown node is contamination:

```bash
make install-user
axis context prune --unknown-nodes          # dry run by default
```

Read the node list. For each name, decide whether it is a test fixture or a real node that was retired or temporarily removed. Then prune **only the fixture nodes, by name**:

```bash
axis context prune --node <fixture-name> --apply
```

Verify:

```bash
axis observations
axis task history
axis skills
```

Expected: fixture rows gone from both stores, and `axis skills` shows **no** learned skill backed only by the fixture node — not merely one with its `preferred_node` blanked. On this host that means the `test` skill is gone entirely. Legitimate history for any retired real node is still present.

The backup directory path is printed by the apply run. Keep it until the verification above passes.

---

### Task 3: Fix the Ollama running-state probe

**Why:** `OllamaDiscoveryScript` misreports Ollama's running state in **both** directions. Consumed by `ollamaIsReady()` in placement and by `axis doctor`.

Two compounding defects. First, `pgrep -f "$OLLAMA_BIN"` matches the probe's own shell, because the script text contains the binary path in its fallback list. Second — and this is the one that makes the expression fail in both directions — the `running` value is produced by `$( [ -n \"$PGREP\" ] && echo true || echo false )` inside a double-quoted `echo`, where the escaped quotes are passed to `test` as **literal characters** rather than quoting the expansion.

Measured by executing the real script against fake `pgrep`/`ollama`/`curl` on `PATH`:

| `pgrep` output | current `running` | correct | |
| --- | --- | --- | --- |
| *(empty)* | `true` | `false` | **false positive** — `test` receives `-n` and the two-character string `""`, which is non-empty |
| `4242` | `true` | `true` | passes, by accident |
| `4242\n4243\n4244` | `false` | `true` | word-splits to `[ -n "4242 4243 4244" ]` → `bash: [: too many arguments` on stderr, `false` via `\|\|` |

The audited symptom — `running:false` on a host serving models — is the third row: the self-match guarantees at least two PIDs. But the first row means a host where **nothing** matched reports Ollama as running, which is the more dangerous direction: placement's `ollamaIsReady()` would route inference work to a node with no server. The audit recorded only the false negative because that is what the audited host exhibited.

**Files:**
- Modify: `internal/facts/tools.go:39` (`OllamaDiscoveryScript`), specifically the `PGREP=` assignment and the `running` expression at line 117
- Test: `internal/facts/collectors_test.go`

**Interfaces:**
- Consumes: Task 1 committed.
- Produces: correct `models.OllamaInfo.Running`. No signature changes.

- [ ] **Step 1: Write the failing test**

Add to `internal/facts/collectors_test.go`:

```go
func TestOllamaRunningTrueWhenServerListening(t *testing.T) {
	prev := runOllamaDiscoveryFn
	t.Cleanup(func() { runOllamaDiscoveryFn = prev })

	runOllamaDiscoveryFn = func(context.Context) ([]byte, error) {
		return []byte(`{"installed":true,"path":"/usr/bin/ollama","version":"0.6.0",` +
			`"running":true,"listening":true,"port":11434,"models":["small:latest"],` +
			`"resident_models":[],"gpu_offload":"none","default_keep_alive":""}`), nil
	}

	info, _ := discoverOllamaLocal(context.Background())
	if !info.Running {
		t.Error("Running should be true when the probe reports running")
	}
	if !info.Listening {
		t.Error("Listening should be true")
	}
}

func TestOllamaRunningExpressionHandlesMultiplePIDs(t *testing.T) {
	// The running expression must yield false for an empty PGREP and true for
	// any non-empty value, including multi-line PID lists.
	cases := []struct {
		name  string
		pgrep string
		want  string
	}{
		{"empty", "", "false"},
		{"single", "1234", "true"},
		{"multiple", "1234\n5678\n9012", "true"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := `PGREP="` + tc.pgrep + `"
RUNNING=false
[ -n "$PGREP" ] && RUNNING=true
printf '%s' "$RUNNING"`
			out, err := exec.Command("bash", "-c", script).Output()
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want {
				t.Errorf("PGREP=%q: got %q, want %q", tc.pgrep, out, tc.want)
			}
		})
	}
}
```

Add `"os/exec"` to the file's imports if not already present.

Neither test above touches `OllamaDiscoveryScript` itself — the first stubs the whole probe, the second checks a standalone expression. Both pass before and after the fix, so neither prevents the production script from regressing. Add a hermetic test that executes the **real** script against fake commands on `PATH`:

```go
// writeFakeBin creates an executable shell stub named name in dir.
func writeFakeBin(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// runDiscoveryScriptWithFakes executes the real OllamaDiscoveryScript with
// stub commands on PATH. ollamaPIDs is what a *correct* pgrep would report for
// genuine ollama server processes.
//
// The fake pgrep inspects its own arguments and models BOTH defects:
//
//   - Called with -f (the current, defective form), it appends its parent's
//     PID, reproducing the self-match: pgrep -f scans full command lines, and
//     the probe's own shell command line contains the binary path.
//   - Called with -x (the fixed form), it matches on process name only and
//     returns just the genuine PIDs.
//
// A fake that ignored its arguments would return identical output for both
// forms, and would therefore guard only the quoting half of the defect.
func runDiscoveryScriptWithFakes(t *testing.T, ollamaPIDs string) map[string]any {
	t.Helper()
	bin := t.TempDir()

	writeFakeBin(t, bin, "ollama", `
case "$1" in
  --version) echo "ollama version is 0.6.0" ;;
  list)      printf 'NAME\tID\nsmall:latest\tabc\n' ;;
  ps)        if [ "$2" = "-qq" ]; then echo ""; else echo "NAME"; fi ;;
  *)         echo "" ;;
esac`)

	writeFakeBin(t, bin, "pgrep", `
PIDS="`+ollamaPIDs+`"
case "$1" in
  -f)
    # Full-command-line scan also matches the probe's own shell.
    if [ -n "$PIDS" ]; then printf '%s\n' "$PIDS"; fi
    printf '%s' "$PPID"
    ;;
  -x)
    # Exact process-name match: the shell is named "bash", not "ollama".
    printf '%s' "$PIDS"
    ;;
  *)
    echo "fake pgrep: unexpected invocation: $*" >&2
    exit 2
    ;;
esac`)

	// Keep the probe offline: /api/ps must not be reached.
	writeFakeBin(t, bin, "curl", `exit 1`)
	// Force the listening probe down a deterministic path.
	for _, c := range []string{"lsof", "netstat", "ss"} {
		writeFakeBin(t, bin, c, `exit 1`)
	}

	cmd := exec.Command("bash", "-c", OllamaDiscoveryScript)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))

	// Capture stdout only. The defective form writes
	// "[: too many arguments" to stderr; CombinedOutput would corrupt
	// the JSON and mask which failure occurred.
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script failed: %v (stdout %q)", err, out)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("script emitted invalid JSON: %v\n%s", err, out)
	}
	return parsed
}

func TestDiscoveryScriptReportsNotRunningWhenNoServer(t *testing.T) {
	// The false-positive direction: nothing is running, and the probe must
	// not claim otherwise just because its own shell matched.
	got := runDiscoveryScriptWithFakes(t, "")
	if got["running"] != false {
		t.Errorf("running should be false when no ollama process exists, got %v", got["running"])
	}
}

func TestDiscoveryScriptReportsRunningForSingleServer(t *testing.T) {
	got := runDiscoveryScriptWithFakes(t, "4242")
	if got["running"] != true {
		t.Errorf("running should be true for a single server PID, got %v", got["running"])
	}
}

func TestDiscoveryScriptReportsRunningForMultipleServers(t *testing.T) {
	// The audited defect: multi-line PGREP word-split inside the escaped
	// test expression and yielded false on a host serving models.
	got := runDiscoveryScriptWithFakes(t, "4242\n4243\n4244")
	if got["running"] != true {
		t.Errorf("running should be true when multiple PIDs match, got %v", got["running"])
	}
}
```

Imports: `encoding/json`, `os`, `os/exec`, `path/filepath`, `testing`.

These require a POSIX shell. The repo's CI runs `ubuntu-latest`, so they are safe there; they will also run on macOS. All three are durable regression guards: each fails against the current script and passes only against the fixed one.

- [ ] **Step 2: Run the tests and confirm the exact red state**

First confirm the current expression is broken in both directions, directly:

```bash
for v in "" "1234" "1234
5678" "1234
5678
9012"; do
  PGREP="$v" bash -c 'printf "PGREP=[%s] -> %s\n" \
    "$(printf "%s" "$PGREP" | tr "\n" " ")" \
    "$( [ -n \"$PGREP\" ] && echo true || echo false )"' 2>&1
done
```

Measured output — and this is the point:

```
PGREP=[] -> true                        # WRONG: nothing matched, reports running
PGREP=[1234] -> true                    # right, by accident
bash: line 1: [: "1234: binary operator expected
PGREP=[1234 5678] -> false              # WRONG: a live server reports not running
bash: line 1: [: too many arguments
PGREP=[1234 5678 9012] -> false         # WRONG
```

The diagnostic text differs by PID count — two PIDs give `binary operator expected`, three or more give `too many arguments` — because `test` is receiving a different number of malformed arguments each time. Either message means the same thing: the escaped quotes reached `test` as literal characters instead of quoting the expansion.

Then run: `go test ./internal/facts/ -run 'TestOllamaRunning|TestDiscoveryScript' -v`

Expected **before** the Step 3 fix:

| Test | Result | Why |
| --- | --- | --- |
| `TestOllamaRunningTrueWhenServerListening` | PASS | Stubs the whole probe; proves nothing about the script |
| `TestOllamaRunningExpressionHandlesMultiplePIDs` | PASS | Tests the corrected expression standalone |
| `TestDiscoveryScriptReportsNotRunningWhenNoServer` | **FAIL** | Gets `true`; the self-matched shell PID is the only match |
| `TestDiscoveryScriptReportsRunningForSingleServer` | **FAIL** | Gets `false`; server PID + self PID = two lines |
| `TestDiscoveryScriptReportsRunningForMultipleServers` | **FAIL** | Gets `false`; four lines |

All three `TestDiscoveryScript*` failures are the real red state. Note that `TestDiscoveryScriptReportsRunningForSingleServer` fails **only because** the fake `pgrep` simulates the self-match on `-f`. Against a fake that ignored its arguments this case would pass before the fix, and the self-match half of the defect would be untested — which is exactly the gap this test shape closes.

If any of the three passes before Step 3, the fakes are not being picked up: check that `PATH` is overridden and the fake `pgrep` is executable.

- [ ] **Step 3: Fix the script**

In `internal/facts/tools.go`, in `OllamaDiscoveryScript`:

Replace the `PGREP` assignment so the probe cannot match itself or its own subshells:

```bash
		PGREP=$(pgrep -x ollama 2>/dev/null | head -1 || echo "")
```

`pgrep -x` matches the process *name* exactly rather than scanning full command lines, so the probe's own shell — named `bash`, not `ollama` — can no longer match itself. That removes the need for any explicit self-exclusion filter. `head -1` then guarantees a single-line value regardless of how many servers are running.

The script sets `set -o pipefail`, so a no-match `pgrep` (exit 1) fails the pipeline; the `|| echo ""` is what turns that into an empty string rather than an unset variable.

**Verify the process name on your platform before relying on this.** `pgrep -x ollama` matches a process named exactly `ollama`. Run `pgrep -x ollama; pgrep -l -f ollama` on a host with the server up and confirm the exact-name form finds it. If some platform names the server differently, that is a finding to record — not a reason to return to `-f`.

Then replace the inline `running` expression at line 117 with a precomputed variable. Add above the final `echo`:

```bash
		RUNNING=false
		[ -n "$PGREP" ] && RUNNING=true
```

and change the `echo` to use `\"running\":$RUNNING,` in place of the `$( [ -n \"$PGREP\" ] ... )` substitution. This matches the form already used in `LlamaServerDiscoveryScript` in the same file.

- [ ] **Step 4: Verify against the live probe**

Run:

```bash
cat > /tmp/zz_probe_test.go <<'EOF'
package facts

import (
	"context"
	"testing"
)

func TestTmpLiveOllamaProbe(t *testing.T) {
	info, _ := discoverOllamaLocal(context.Background())
	t.Logf("Installed=%v Running=%v Listening=%v models=%d",
		info.Installed, info.Running, info.Listening, len(info.Models))
}
EOF
cp /tmp/zz_probe_test.go internal/facts/zz_probe_test.go
go test ./internal/facts/ -run TestTmpLiveOllamaProbe -v -count=1
rm internal/facts/zz_probe_test.go
```

Expected on a host with Ollama running: `Running=true Listening=true` with a non-zero model count. Before the fix this printed `Running=false`.

**Delete `internal/facts/zz_probe_test.go` before committing** — the `rm` above does this; verify with `git status`.

- [ ] **Step 5: Confirm doctor and ai backends now agree**

Run:

```bash
make install-user
axis doctor 2>&1 | grep -i "AI Backend: ollama"
axis ai backends 2>&1 | grep ollama
```

Expected: both report the backend running. Before the fix, `doctor` said "installed, not running" while `ai backends` said `ok`.

- [ ] **Step 6: Commit**

```bash
make lint
go test ./internal/facts/ ./internal/placement/ -count=1
git status --short   # confirm no zz_probe_test.go
git add internal/facts/tools.go internal/facts/collectors_test.go
git commit -m "fix(facts): correct ollama running detection

The probe matched its own shell because the script text contains the
binary path, producing a multi-line PGREP value that word-split inside
the escaped test expression and always yielded false. Switch to pgrep -x
on the process name and precompute RUNNING, matching the form already
used by LlamaServerDiscoveryScript.

Fixes disagreement between axis doctor and axis ai backends.
Refs docs/evaluations/2026-07-25-truth-integrity-audit.md A1, C4"
```

---

### Task 4: Stop reporting certainty on non-classifications

**Why:** `reflexFallback` hardcodes `Confidence: 1.0` and returns it even when the match is `unknown`, so `axis llm` prints `Class: unknown` beside `Confidence: 1.00`. It also surfaces raw upstream error text (`ollama status 404`) as user-facing output.

**Files:**
- Modify: `internal/llmrouter/engine.go:294-309` (`reflexFallback`)
- Test: `internal/llmrouter/engine_internal_test.go` (**create**)

**Test package matters here.** `internal/llmrouter/engine_test.go` declares `package llmrouter_test` — an external test package that **cannot** see the unexported `reflexFallback`. Putting these tests there is a compile error, not a style problem. Create a new file `engine_internal_test.go` declaring `package llmrouter`; Go permits both in-package and external test files in the same directory.

**Interfaces:**
- Consumes: Task 1 committed.
- Produces: `reflexFallback` returning `Confidence: 0.0` for `models.ClassUnknown` and a sanitized note. Signature unchanged: `func reflexFallback(prompt string, reason error) (models.WorkloadClass, IntentSignal)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/llmrouter/engine_internal_test.go` with `package llmrouter` (**not** `llmrouter_test`):

```go
package llmrouter

import (
	"errors"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestReflexFallbackUnknownHasZeroConfidence(t *testing.T) {
	// Do NOT t.Skip when a phrase starts classifying: the contract would
	// then lose coverage silently the moment the taxonomy grows. Instead,
	// require that at least one prompt still reaches ClassUnknown, and fail
	// loudly if none do so the fixture is updated deliberately.
	candidates := []string{
		"zzqx frobnicate the wibbletron",
		"compile a rust project",
		"knit a sweater",
	}

	sawUnknown := false
	for _, prompt := range candidates {
		class, sig := reflexFallback(prompt, nil)
		if class != models.ClassUnknown {
			continue
		}
		sawUnknown = true
		if sig.Confidence != 0.0 {
			t.Errorf("unknown classification must not report confidence for %q, got %v",
				prompt, sig.Confidence)
		}
	}

	if !sawUnknown {
		t.Fatal("no candidate prompt reached ClassUnknown; the unknown-confidence " +
			"contract is now untested — add an unmatched phrase to the table")
	}
}

func TestReflexFallbackMatchedClassKeepsDeterministicConfidence(t *testing.T) {
	class, sig := reflexFallback("analyze repo", nil)

	if class == models.ClassUnknown {
		t.Fatal("expected 'analyze repo' to classify")
	}
	if sig.Confidence != 1.0 {
		t.Errorf("deterministic match should report 1.0, got %v", sig.Confidence)
	}
}

func TestReflexFallbackSanitizesUpstreamError(t *testing.T) {
	_, sig := reflexFallback("analyze repo", errors.New("ollama status 404"))

	joined := strings.Join(sig.Notes, " ")
	if strings.Contains(joined, "404") {
		t.Errorf("raw upstream error leaked to notes: %q", joined)
	}
	if !strings.Contains(joined, "semantic classifier unavailable") {
		t.Errorf("expected a sanitized note, got %q", joined)
	}
}
```

The import block above is complete for these three tests — the file is new, so nothing is inherited.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/llmrouter/ -run TestReflexFallback -v`
Expected: `TestReflexFallbackUnknownHasZeroConfidence` FAILS with `got 1`, and `TestReflexFallbackSanitizesUpstreamError` FAILS on the leaked `404`.

- [ ] **Step 3: Implement**

Replace `reflexFallback` in `internal/llmrouter/engine.go`:

```go
func reflexFallback(prompt string, reason error) (models.WorkloadClass, IntentSignal) {
	match := workload.Match(prompt)

	var notes []string
	if reason != nil {
		// The upstream error is diagnostic detail, not operator-facing copy.
		notes = append(notes, "semantic classifier unavailable; used deterministic matching")
	}
	notes = append(notes, match.Notes...)

	// Deterministic matching is certain only when it matched something.
	// Reporting 1.0 for an unmatched prompt states certainty about a
	// non-answer.
	confidence := 1.0
	if match.Class == models.ClassUnknown {
		confidence = 0.0
	}

	return match.Class, IntentSignal{
		Class:      match.Class,
		Confidence: confidence,
		Source:     SourceReflex,
		Notes:      notes,
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/llmrouter/ -run TestReflexFallback -v`
Expected: PASS.

- [ ] **Step 5: Fix downstream test expectations**

`cmd/axis/llm_test.go` has fixtures at lines 183, 424, and 431 asserting the old `"semantic fallback: ..."` note text. Run:

```bash
go test ./cmd/axis/ -run TestLLM -v 2>&1 | head -30
```

Update those fixtures to the new sanitized note. Do not reintroduce raw error strings.

- [ ] **Step 6: Verify against the live command**

```bash
make install-user
axis llm "compile a rust project"
```

Expected: `Class: unknown` with `Confidence: 0.00`, and no raw `404` in the output.

- [ ] **Step 7: Commit**

```bash
make lint
go test ./internal/llmrouter/ ./cmd/axis/ -count=1
git add internal/llmrouter/engine.go internal/llmrouter/engine_internal_test.go cmd/axis/llm_test.go
git commit -m "fix(llm): do not report confidence for unknown classification

reflexFallback returned Confidence 1.0 unconditionally, so an unmatched
prompt rendered as 'Class: unknown, Confidence: 1.00'. Deterministic
matching is certain only when it matches. Also replaces raw upstream
error text in operator-facing notes with a sanitized message.

Refs docs/evaluations/2026-07-25-truth-integrity-audit.md C2"
```

---

### Task 5: Remove inferred topology edges

**Why:** `cmd/axis/dashboard.go` infers a network link when two nodes report an identical CIDR string. On the audited grid this rendered ten edges with no valid evidence — four provably false, because two nodes at different physical sites share the same private /24 — while omitting every Darwin node, whose `subnet` is empty.

**Scope: removal only. No route data is rendered in its place.**

An earlier draft of this task replaced the block with a reachability list built from `NetworkClass` and `SSHHandshakeLatencyMs`. That would have traded one truth violation for another. Both fields are **vantage-relative**, and `docs/decisions/topology-truth-contract.md` requires that any rendering of route data name its vantage (R1), show observation age for cached snapshots (R5), and render the vantage node as local rather than as a route to itself (R7). None of that is possible until `ClusterSnapshot.Vantage` exists — without it, a cached snapshot collected by a daemon on another host would be silently attributed to the CLI host, and the observing node would render as `direct-lan` to itself.

So containment removes the false claim and stops. The reachability view lands with the schema field, in that contract's own implementation.

**Files:**
- Modify: `cmd/axis/dashboard.go:235-320` (pairing loop and render), `cmd/axis/dashboard.go:539` (`speedPriority`, now unused), and the `sort` import
- Test: `cmd/axis/summary_test.go:234` (`TestSummaryRenderTopology`)

**Interfaces:**
- Consumes: Task 1 committed.
- Produces: no topology section in `ClusterSummaryView.Render()`. No exported signature changes.

- [ ] **Step 1: Write the failing test**

Replace `TestSummaryRenderTopology` in `cmd/axis/summary_test.go` with:

```go
func TestSummaryRendersReachabilityNotPairwiseEdges(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name:         "node-a",
				NetworkClass: models.NetworkClassDirectLAN,
				Addresses: []models.NetworkAddress{
					{Kind: "ipv4", Address: "192.0.2.10", Subnet: "192.0.2.0/24", SpeedClass: "gigabit"},
				},
			},
			{
				// Same subnet string as node-a but reached over a relay:
				// the exact case the old pairing loop got wrong.
				Name:         "node-b",
				NetworkClass: models.NetworkClassRelayed,
				Addresses: []models.NetworkAddress{
					{Kind: "ipv4", Address: "192.0.2.11", Subnet: "192.0.2.0/24", SpeedClass: "gigabit"},
				},
			},
			{
				// No subnet at all: previously omitted entirely.
				Name:         "node-c",
				NetworkClass: models.NetworkClassDirectLAN,
				Addresses: []models.NetworkAddress{
					{Kind: "ipv4", Address: "192.0.2.12", SpeedClass: "thunderbolt"},
				},
			},
		},
	}

	out := populateSummaryView(snap, daemon.Metadata{}).Render()

	if strings.Contains(out, "CLUSTER TOPOLOGY") {
		t.Error("pairwise topology section must be gone")
	}
	for _, glyph := range []string{"<========", "<........", "<~~~~~~~~", "<--------"} {
		if strings.Contains(out, glyph) {
			t.Errorf("pairwise connector %q still rendered", glyph)
		}
	}
	// No route data may be rendered until ClusterSnapshot.Vantage exists.
	// See docs/decisions/topology-truth-contract.md R1/R5/R7.
	for _, leaked := range []string{"REACHABILITY", "direct-lan", "relayed", "handshake"} {
		if strings.Contains(out, leaked) {
			t.Errorf("unlabeled vantage-relative route data %q rendered", leaked)
		}
	}
	// The summary must still render.
	if !strings.Contains(out, "AXIS CLUSTER SUMMARY") {
		t.Error("summary header missing")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cmd/axis/ -run TestSummaryRendersReachability -v`
Expected: FAIL — `CLUSTER TOPOLOGY` and the connector glyphs are still rendered.

- [ ] **Step 3: Delete the pairing loop, its helper, and the orphaned import**

Three deletions in `cmd/axis/dashboard.go`:

1. The entire block from `var topoLines []string` through the end of the render loop (lines 235–320).
2. The `speedPriority` function at line 539 — used only by that block.
3. The `sort` import. `sort.Slice` at line 289 is inside the deleted block and is its **only** use in the file (verified: `grep -n "sort\." cmd/axis/dashboard.go` returns exactly that one line). Leaving it produces `"sort" imported and not used`, a compile error.

In place of the block, leave a comment recording why nothing renders here:

```go
	// Network topology is intentionally not rendered. Peer links were
	// previously inferred from CIDR string equality, which fabricated edges
	// between nodes at different sites sharing a private range. AXIS observes
	// routes from the collecting host, not node-to-node edges, and route data
	// cannot be rendered honestly until ClusterSnapshot.Vantage identifies the
	// observing node. See docs/decisions/topology-truth-contract.md
```

After the edit, confirm the import is gone and the package builds:

```bash
grep -n '"sort"' cmd/axis/dashboard.go || echo "sort import removed"
go build ./cmd/axis/
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/axis/ -run TestSummaryRendersReachability -v`
Expected: PASS.

- [ ] **Step 5: Refresh affected golden files**

Run:

```bash
go test ./cmd/axis/ -count=1 2>&1 | head -30
```

`summary_live_snapshot.golden`, `summary_with_nodes.golden`, `summary_daemon_cache.golden`, `summary_empty_state.golden`, and `summary_corrupt_state.golden` may change. Inspect each diff before regenerating — confirm the change is the topology block being replaced and nothing else. Regenerate using the repo's existing golden-update mechanism (check for an `-update` flag in `cmd/axis/summary_test.go`; if none exists, edit the files by hand).

- [ ] **Step 6: Verify against the live cluster**

```bash
make install-user
axis summary
```

Expected: no `CLUSTER TOPOLOGY` block and no connector glyphs. The rest of the summary — nodes, resources, warnings — renders unchanged. No route data appears.

- [ ] **Step 7: Commit**

```bash
make lint
make test
git add cmd/axis/dashboard.go cmd/axis/summary_test.go cmd/axis/testdata/
git commit -m "fix(summary): remove inferred network topology edges

The topology block inferred a peer link from CIDR string equality, which
fabricated edges between nodes at different sites sharing a private range
and omitted every node lacking a subnet. AXIS has no node-to-node edge
data.

Removed rather than replaced: NetworkClass and SSH handshake duration are
vantage-relative, and rendering them without ClusterSnapshot.Vantage would
misattribute cached snapshots collected by another host. The reachability
view lands with that schema field.

Refs docs/decisions/topology-truth-contract.md R1/R5/R7
Refs docs/evaluations/2026-07-25-truth-integrity-audit.md C1, A2"
```

---

### Task 6: Full verification

**Files:** none modified.

**Interfaces:**
- Consumes: Tasks 1–5 committed.
- Produces: a verified containment baseline.

- [ ] **Step 1: Run the full suite with state comparison**

Run the full suite under a disposable, empty `HOME` — same discipline as Task 1 Step 6, for the same reason:

```bash
FINAL=$(mktemp -d)
export GOCACHE=$(go env GOCACHE) GOPATH=$(go env GOPATH) GOMODCACHE=$(go env GOMODCACHE)

HOME="$FINAL" make test
HOME="$FINAL" make test-race
make lint          # gofmt/go vet only; touches no state

echo "--- AXIS files written under a disposable HOME:"
find "$FINAL/.axis" -type f 2>/dev/null | sed "s|^$FINAL|\$HOME|"

echo "--- the two stores this plan fixes:"
for f in state.json skills.json; do
  test -e "$FINAL/.axis/$f" && { echo "LEAK: $f"; false; } || echo "clean: $f"
done
```

Expected: all green, and `clean:` for both stores.

**Scoped to `state.json` and `skills.json` on purpose.** Task 1 Step 6 measured that the suite also writes `snapshot.json`, `events.jsonl`, `event-sequence`, `ledger.json`, `chat-history.json` and `token`. Requiring an absent `.axis` here would make the final gate unpassable for reasons outside this plan's scope, so the gate asserts what this plan actually fixes. The wider leak is tracked as its own audit finding — see "Unfinished: the other six stores" after Task 1.

Toolchain caches are pinned for the same reason as Task 1 Step 6 — otherwise the module cache rebuilds inside the sandbox. `make test-race` runs the suite a second time, so a surviving leak shows here even if it slipped past Task 1 — and shows it in the sandbox, not in the operator's store.

Never substitute a snapshot-and-diff of the real `~/.axis/` for this. That form detects a leak only after it has written, and its "restore" step would discard whatever a concurrent daemon refresh or execution wrote legitimately during the run.

- [ ] **Step 2: Run the repo truth gates**

```bash
./hack/coverage-check.sh
./hack/verify-repo-truth.sh
./hack/verify-doc-facts.sh
```

Expected: all pass. If coverage dropped below a gate, add tests for the uncovered branch rather than lowering the gate.

- [ ] **Step 3: Confirm each audit finding is closed**

```bash
make install-user
axis doctor 2>&1 | grep -i "AI Backend: ollama"     # A1/C4: running
axis ai backends 2>&1 | grep ollama                  # C4: agrees with doctor
axis llm "compile a rust project" 2>&1 | grep -i conf # C2: 0.00, no raw 404
axis summary 2>&1 | grep -c "CLUSTER TOPOLOGY"       # C1: 0
axis task history 2>&1 | head -5                     # C3: no fixture rows
axis skills                                          # C3: no fixture-backed skill at all
```

- [ ] **Step 4: Record completion in the audit document**

**Use the real completion date, not the plan's filename date.** The plan is dated by when it was written; containment lands whenever Steps 1–3 above actually pass. Get it from the machine rather than copying it from anywhere:

```bash
TODAY=$(date -u +%Y-%m-%d)
echo "recording containment completion as $TODAY"
```

Append to `docs/evaluations/2026-07-25-truth-integrity-audit.md`, substituting that value:

```markdown
## Containment status

Containment landed <TODAY> per `docs/superpowers/plans/2026-07-25-truth-integrity-containment.md`.

- A1 / C4 — fixed: ollama running detection corrected in **both** directions.
  The probe previously reported `running:true` when nothing matched, as well as
  `running:false` on a host serving models. Guarded by hermetic tests that
  execute the real discovery script against a fake `pgrep` which distinguishes
  `-f` from `-x`
- C1 — partially addressed: inferred edges removed. No reachability view yet;
  it requires `ClusterSnapshot.Vantage` and lands with the topology contract
- C2 — fixed: unknown classification no longer reports confidence
- C3 — fixed: test `HOME` isolated; `axis context prune` added, covering
  `state.json` (nodes, observations, task history, failure records, legacy
  tombstones) and `skills.json` (node evidence subtracted, skills left without
  evidence deleted). `skills.Update` added so neither store is mutated by an
  unlocked read-modify-write

A2, A3, A4, B1–B4 remain open, as does the reachability half of C1. They are
addressed by the topology, placement-selection, and capability-probe contracts.
```

Only commit this after every check in Steps 1–3 has passed — a completion record written ahead of completion is the same class of defect this audit documents.

```bash
git add docs/evaluations/2026-07-25-truth-integrity-audit.md
git commit -m "docs: record containment completion in truth-integrity audit"
```

---

## Self-Review

**Spec coverage.** All four containment items from the audit's sequencing section are covered: C3 → Tasks 1 and 2 (isolation *and* remediation across **both** stores); A1/C4 → Task 3; C2 → Task 4; C1 → Task 5, **removal only**. Task 6 verifies. A2, A3, A4, B1–B4, and the reachability half of C1 are out of scope and named as such in Task 6 Step 4.

**Placeholder scan.** No TBD/TODO. Every code step carries the actual code. Every API the plan calls was verified present: `state.Update` (`internal/state/state.go:114`), `state.Path` (`:85`), `ClusterState.Failures`/`.Tombstones` (`:68`/`:66`), `TaskExecutionRecord.Node` (`:54`), `skills.Path` (`internal/skills/skills.go:37`), `LearnedSkill.SuccessCount` (`:18`), `persist.AxisDir` (`internal/persist/paths.go:18`), `ClusterSummaryView.Nodes` (`cmd/axis/dashboard.go:164`), and `sort.Slice` as the sole `sort` use (`:289`).

**Claims measured, not assumed.** The Task 3 behaviour table comes from executing the extracted `OllamaDiscoveryScript` against fake binaries, not from reading it: empty `pgrep` → `running:true`, single → `true`, multi-line → `false` plus `[: too many arguments` on stderr. The self-match was confirmed live on the development host — `pgrep -x ollama` returns one PID (the server) while `pgrep -f ollama` also returns the invoking shell. The Task 2 skills contamination shape was read from this host's real `skills.json`, and the `SuccessCount == sum(NodeCount)` invariant from `RecordSuccess` (`internal/skills/skills.go:77`).

**Type consistency.** `PruneReport` and `SkillPruneReport` are distinct types in distinct packages, defined and consumed within Task 2. The parameter is `targets map[string]bool` (nodes to remove) at every call site — Steps 1, 3, and 7 — never the inverted `known` sense an earlier draft used. `runContextPrune(w io.Writer, targetNames []string, apply bool)` matches between Steps 5 and 7. `reflexFallback` keeps its existing signature.

**Ordering.** Task 1 is first and non-negotiable: every subsequent task runs `go test`, and until it lands each run writes into operator state. Its reproduction step runs under a disposable `HOME` — reproducing a state-corruption defect against the real store would have deepened the very contamination this plan repairs.

**Corrections applied after review.**

*First review round (six blocking, three gaps):* reproduction against real `HOME`; `skills.json` unreachable by a `ClusterState`-only prune; over-broad "absent from nodes.yaml" deletion without identities, confirmation, or backup; unlocked `Load`/`Save` where `state.Update` exists; Task 4 tests placed in an external test package that cannot see an unexported function; Task 5 rendering vantage-relative route data with no vantage; no durable test of the real discovery script; the orphaned `sort` import; a pre-declared completion date.

*Second review round (three blocking, four gaps):*

| Defect | Fix |
| --- | --- |
| Prune cleared node *references* but left the fixture skill and its `SuccessCount` | Subtract each pruned node's `NodeCount` from `SuccessCount`; delete skills reaching zero. State prune extended to `Failures` and legacy `Tombstones` |
| `skills.json` still mutated by unlocked `Load`/`Save` | Added `skills.Update` with the same `flock` discipline as `state.Update` |
| Task 1 Step 6 and Task 6 Step 1 ran the suite against the operator's real stores | Both run under a disposable empty `HOME`; the assertion is "the sandbox stays empty" |
| `SANDBOX="$SANDBOX"` placed *after* `python3 -c`, landing in `argv` | `export SANDBOX` before the call |
| `--unknown-nodes` scanned only `Nodes` and `TaskHistory` | `unknownNodeNames()` scans every record type prune can affect, in both stores |
| Fake `pgrep` ignored its arguments, so tests guarded quoting but not self-matching; and the stated red state was wrong | Fake now branches on `-f`/`-x` and appends its parent PID for `-f`. Red state corrected and measured: single-PID **passes** before the fix; empty and multi-PID fail |
| Timestamped backup directories could collide within one second | `os.MkdirTemp` with a timestamp prefix; test asserts backup *contents*, and that two applies produce two directories |
| `t.Skip` would silently drop the unknown-confidence contract | Candidate table; fails if no prompt reaches `ClassUnknown` |

Two of these went further than the review reported. The Ollama probe is wrong in **both** directions, not just the audited false negative — an empty `pgrep` yields `running:true`, which would route inference to a node with no server. And the skills store is exposed to worse than a lost update: `internal/execution/guarded.go:381` substitutes an empty `&skills.Store{}` when the runtime store is nil, so the subsequent `Save` truncates `skills.json` to that one execution's learning.

*Third review round (three blocking, three gaps):*

| Defect | Fix |
| --- | --- |
| **Auto-discovered skills do exist**, and the zero-evidence deletion rule destroyed every one of them on any prune | Deletion now requires the prune to have *touched* the skill. `AutoDiscoverSkills` (`internal/skills/autodiscover.go:12`) creates `SuccessCount: 0`, nil `NodeCount`, `PreferredNode` set — a template for an untargeted node is now provably untouched and survives |
| `skills.Update` serialized nothing, because the plan deferred migrating the existing writers | All three writers migrate in the same change: `autodiscover.go:53`, `guarded.go:1199`, `guarded.go:1207` |
| Dry run omitted failure records, tombstones, success evidence, and whole-skill deletions | Every field of both reports is printed, and deleted skills are named individually |
| Clean-`HOME` assertion could never pass — Go derives `GOCACHE`/`GOPATH`/`GOMODCACHE` from `HOME` | Toolchain caches pinned; the assertion is scoped to `$HOME/.axis` |
| "no changes written" overstated — `state.Load` persists migrations and both loaders quarantine corrupt files by renaming them | Wording corrected to "nothing above was pruned"; the load-path behaviour is documented in the command's help and in a code comment |
| `skills.Update` had no direct test, and the red/green commands ran only `internal/state` | Added `TestSkillsUpdateReloadsFromDiskInsideLock` and `TestSkillsUpdateSerializesConcurrentWriters`; both stages now run `internal/state` **and** `internal/skills`, plus `-race` |

*Fourth review round (three findings):*

| Defect | Fix |
| --- | --- |
| `-run TestContextPruneDryRun` matched no test — `go test` prints `no tests to run` and **exits 0**, so the red stage verified nothing | Pattern is the real shared prefix `TestContextPrune` with `-count=1`. All nine `-run` invocations in this plan were then checked mechanically against every test name the plan defines *and* the 1427 already in the tree; all nine match |
| Backup was taken outside any lock, both stores were mutated through separate `Update` calls, and a skills failure left `state.json` pruned | One transaction: both locks acquired in a fixed order and held across read, backup and both writes; the backup is written from the exact bytes read under lock; a skills-write failure rolls `state.json` back. Crash atomicity is explicitly **not** claimed, and the recovery path is documented and printed before the first write |
| Deleted-skill names came from reloading and diffing by ID — but IDs are not unique (`RecordSuccess` uses one-second resolution) | `SkillPruneReport.Deleted []DeletedSkill` captures identity at the moment of deletion. Tests cover deleted names, duplicate IDs, rollback, and a concurrent writer |

The lock work needed a shared primitive, so `persist.LockFile` now owns the acquisition, the ordering rule, and the deadlock warning; `state.Update` is refactored onto it. Two `LoadUnlocked` seams were added because `state.Load` persists migrations through `Update` and would self-deadlock inside the transaction.

*Fifth review round (two blocking, one gap), all found during execution:*

| Defect | Fix |
| --- | --- |
| Rollback assumed a failed write left its store unchanged. `persist.WriteFileAtomic` renames at `recovery.go:92` and fsyncs the parent at `:95`, so it can error *after* the contents are live — leaving a half-applied prune | `rollbackPrune` restores every **attempted** write, at either failure position. Both write sites go through seams, and the test simulates write-then-error at each |
| Task 6's final gate still required an absent `.axis`, which Task 1's own measurement had just disproved — the gate was unpassable as written | Scoped to `state.json` and `skills.json`, the stores this plan fixes |
| Both stores were written whenever *either* report was non-empty, so a skills-only prune could create an absent `state.json` and a state-only prune could reserialize an untouched `skills.json` | Each store is written only when its own report is non-empty; `TestContextPruneLeavesUnaffectedStoresUntouched` covers it |

The `token` finding was also overstated in the first write-up. `auth.LoadOrGenerateToken` returns a valid existing token unchanged (`internal/auth/auth.go:53-55`) and generates only when the file is absent or fails validation, so the accurate claim is that the suite reaches production auth state and may create or regenerate a token — not that it overwrites a good one.

**A correction to an earlier round.** The claim that `RecordSuccess` was the only writer of `Store.Skills` was wrong, and the method that produced it was the reason: the grep searched for *callers* of the skills package (`skills.Load()`, `RecordSuccess(`) and structurally could not see a writer defined **inside** the package. `AutoDiscoverSkills` appends to `s.Skills` directly. The lesson generalises — a prune must be verified against every writer of a store, found by reading the package, not by grepping its call sites.
