# Model Discovery PR 1: Probe-Backed Load State Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The MLX, llama.cpp, and Apple Foundation Models collectors publish load state only from a runtime load signal, with provenance, and the catalog and `axis agent` `/models` show that state.

**Architecture:** Collector scripts (bash, run with `bash -c` locally and over SSH) gain localhost probes that set `state`, `load_signal`, `context_window`, and `provenance` on each `ResidentModel`. Apple facts come from a read-only `--facts` mode of the existing cached Swift helper. `modelinventory.Catalog` maps those fields; the agent's choice list reads the catalog. No new store, no new surface.

**Tech Stack:** Go 1.26.9, bash + awk + grep + sed + curl (existing script toolset), Swift (FoundationModels) for the Apple helper.

**Spec:** `docs/superpowers/specs/2026-10-09-model-discovery-design.md` (section "1. Correct the existing collectors (PR 1)").

## Global Constraints

- Probes dial `127.0.0.1` only. Hard timeout `--max-time 2` per request.
- `loaded` only from a load signal: Ollama `/api/ps`, llama.cpp argv `-m` + `/health` 200, Apple `availability == .available`.
- MLX is never `loaded`. `/v1/models` of `mlx_lm.server` lists the Hugging Face cache; it is never read for residents.
- `context_window` only from a served-context field: llama.cpp `/props` `default_generation_settings.n_ctx`, Apple `contextSize`. Never `meta.n_ctx_train`.
- A probe timeout is `unknown`, never a negative.
- No generation during sweeps: the Apple helper's `--self-test` is not run by discovery.
- A resident row with no `state` (older collectors, or `curl` missing) keeps #507's runtime mapping in `Catalog`.
- `AppleFoundationModelsInfo.Available` and `.Verified` keep their meaning: placement (`internal/placement/ranker.go:404`) requires both.
- Remote collection installs nothing new on nodes.
- Files over 500 lines (`internal/models/types.go`, `internal/facts/remote.go`, `cmd/axis/agent_startup_model.go`, `cmd/axis/agent_slash.go`) must not grow; extract or replace instead.
- Public repo: no hostnames, IP addresses other than `127.0.0.1` and RFC 5737 examples, or user paths in code, tests, docs, or commit messages.
- Gates before each commit: `./hack/hermetic-go-test.sh -count=1 <pkgs>`; before the PR: `GOTOOLCHAIN=go1.26.9 ./hack/ci-preflight.sh`, then restore `artifacts/` (`git checkout -- artifacts/a2a-slice2-e2e-latest.json` and remove the new `artifacts/a2a-slice2-e2e-<sha>.json`).

## File Structure

| File | Responsibility |
|---|---|
| `internal/models/resident_model.go` (new) | `ResidentModel` moved verbatim from `types.go`, plus `State`, `LoadSignal`, `ContextWindow`, `Provenance` |
| `internal/models/apple_fm.go` (new) | `AppleFoundationModelsInfo` moved verbatim from `types.go`, plus `State`, `Reason`, `ContextWindow`, `Model`, `Capabilities` |
| `internal/models/model_catalog.go` | `ModelCatalogLoading`, `ModelCatalogDown`; `ModelCatalogEntry.ContextWindow`, `.LoadSignal` |
| `internal/modelinventory/catalog.go` | resident state from `ResidentModel.State`; Apple entry |
| `internal/facts/tools.go` | llama.cpp `/health` `/props` `/v1/models` probes; MLX argv model |
| `internal/facts/apple_foundation_models_source.go` | Swift `--facts` mode (shared const), discovery script runs `--facts` |
| `hack/apple-foundation-models.swift` | identical to the embedded helper source |
| `internal/facts/apple_fm_facts.go` (new) | `appleFMFromProbe`: probe output → `AppleFoundationModelsInfo` |
| `internal/facts/remote.go` | Apple switch replaced by `appleFMFromProbe` (shrinks) |
| `internal/facts/local_ai.go` | local probe runs `--facts`, parses with `appleFMFromProbe` |
| `internal/agent/model_target.go` | `Loading bool` |
| `cmd/axis/agent_model_choices.go` | loading/down/Apple choice states and wording |
| `docs/current-state.md` | describe load signals |

---

### Task 1: Move resident and Apple types into their own files and add the new fields

**Files:**
- Create: `internal/models/resident_model.go`
- Create: `internal/models/apple_fm.go`
- Modify: `internal/models/types.go` (delete `ResidentModel` at `:261` and `AppleFoundationModelsInfo` at `:308`, with their doc comments)
- Test: `internal/models/resident_model_test.go` (new)

**Interfaces:**
- Produces: `models.ResidentModel{State ModelCatalogState; LoadSignal string; ContextWindow int; Provenance map[string]string}` (JSON `state`, `load_signal`, `context_window`, `provenance`, all `omitempty`); `models.AppleFoundationModelsInfo{State string; Reason string; ContextWindow int; Model string; Capabilities []string}` (JSON `state`, `reason`, `context_window`, `model`, `capabilities`, all `omitempty`); constants `models.AppleFMReady = "ready"`, `AppleFMCold = "cold"`, `AppleFMUnavailable = "unavailable"`, `AppleFMUnknown = "unknown"`.

- [ ] **Step 1: Write the failing test**

```go
package models

import (
	"encoding/json"
	"strings"
	"testing"
)

// Older payloads have no load-state fields; they must decode unchanged and
// re-encode without the new keys.
func TestResidentModelNewFieldsAreOptional(t *testing.T) {
	var r ResidentModel
	if err := json.Unmarshal([]byte(`{"name":"m","runtime":"llama.cpp","port":8080}`), &r); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(r)
	for _, key := range []string{"state", "load_signal", "context_window", "provenance"} {
		if strings.Contains(string(out), `"`+key+`"`) {
			t.Fatalf("encoded %s contains %q for a row without it", out, key)
		}
	}
}

func TestResidentModelCarriesLoadState(t *testing.T) {
	in := `{"name":"m","runtime":"llama.cpp","state":"loading","load_signal":"health-loading","context_window":4096,"provenance":{"state":"GET /health 503"}}`
	var r ResidentModel
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatal(err)
	}
	if r.State != ModelCatalogLoading || r.LoadSignal != "health-loading" || r.ContextWindow != 4096 || r.Provenance["state"] != "GET /health 503" {
		t.Fatalf("decoded %+v", r)
	}
}

func TestAppleFMInfoCarriesFacts(t *testing.T) {
	in := `{"available":true,"verified":true,"state":"ready","context_window":8192,"model":"AFM 3 Core","capabilities":["toolCalling","vision"]}`
	var a AppleFoundationModelsInfo
	if err := json.Unmarshal([]byte(in), &a); err != nil {
		t.Fatal(err)
	}
	if a.State != AppleFMReady || a.ContextWindow != 8192 || a.Model != "AFM 3 Core" || len(a.Capabilities) != 2 {
		t.Fatalf("decoded %+v", a)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./hack/hermetic-go-test.sh -count=1 -run 'TestResidentModel|TestAppleFMInfo' ./internal/models/`
Expected: FAIL to compile (`r.State undefined`, `ModelCatalogLoading undefined`, `AppleFMReady undefined`).

- [ ] **Step 3: Move the types and add fields**

Delete the two type declarations (and their preceding doc comments) from `internal/models/types.go` with the Edit tool. Create `internal/models/resident_model.go` containing the deleted `ResidentModel` declaration verbatim, followed inside the struct by:

```go
	// State is the load state the collector observed, from a runtime load
	// signal only (see LoadSignal). Empty means the collector did not
	// determine it; Catalog then falls back to the runtime mapping.
	State ModelCatalogState `json:"state,omitempty" yaml:"state,omitempty"`
	// LoadSignal names the evidence behind State, e.g. "ollama-ps",
	// "health-ok", "health-loading", "health-silent", "served-id-differs", "none".
	LoadSignal string `json:"load_signal,omitempty" yaml:"load_signal,omitempty"`
	// ContextWindow is the served context in tokens, only when the runtime
	// states it (llama.cpp /props n_ctx). Never training context.
	ContextWindow int `json:"context_window,omitempty" yaml:"context_window,omitempty"`
	// Provenance maps a field name to the probe that produced it.
	Provenance map[string]string `json:"provenance,omitempty" yaml:"provenance,omitempty"`
```

(`resident_model.go` imports `time` for `ExpiresAt`.)

Create `internal/models/apple_fm.go` containing the deleted `AppleFoundationModelsInfo` declaration verbatim, followed inside the struct by:

```go
	// State is ready (availability .available), cold (.modelNotReady),
	// unavailable (Apple Intelligence not enabled / device not eligible),
	// or unknown (probe timed out or gave no facts). Empty on older payloads.
	State string `json:"state,omitempty" yaml:"state,omitempty"`
	// Reason is Apple's UnavailableReason when State is cold or unavailable.
	Reason string `json:"reason,omitempty" yaml:"reason,omitempty"`
	// ContextWindow is SystemLanguageModel.contextSize: tokens per session,
	// input plus output.
	ContextWindow int `json:"context_window,omitempty" yaml:"context_window,omitempty"`
	// Model is SystemLanguageModel.variant.displayName (macOS 27+).
	Model string `json:"model,omitempty" yaml:"model,omitempty"`
	// Capabilities lists LanguageModelCapabilities present (macOS 27+).
	Capabilities []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
```

and after the struct:

```go
const (
	AppleFMReady       = "ready"
	AppleFMCold        = "cold"
	AppleFMUnavailable = "unavailable"
	AppleFMUnknown     = "unknown"
)
```

In `internal/models/model_catalog.go` add to the `ModelCatalogState` const block:

```go
	// ModelCatalogLoading means the runtime reports it is still loading
	// (llama.cpp /health 503).
	ModelCatalogLoading ModelCatalogState = "loading"
	// ModelCatalogDown means the process was seen but its endpoint did not
	// answer.
	ModelCatalogDown ModelCatalogState = "down"
```

- [ ] **Step 4: Run to verify it passes, and that nothing else broke**

Run: `gofmt -l internal/models && ./hack/hermetic-go-test.sh -count=1 ./internal/models/ ./internal/facts/ ./internal/modelinventory/ && go build ./...`
Expected: no gofmt output; all `ok`; build succeeds. `wc -l internal/models/types.go` is smaller than 827.

- [ ] **Step 5: Commit**

```bash
git add internal/models/
git commit -m "refactor(models): move ResidentModel and Apple facts to own files; add load-state fields"
```

---

### Task 2: Catalog reads resident state; Apple entry

**Files:**
- Modify: `internal/modelinventory/catalog.go` (`nodeCatalogEntries`, `residentState`)
- Modify: `internal/models/model_catalog.go` (`ModelCatalogEntry`)
- Test: `internal/modelinventory/catalog_test.go`

**Interfaces:**
- Consumes: Task 1 fields.
- Produces: `ModelCatalogEntry.ContextWindow int` (`context_window,omitempty`), `ModelCatalogEntry.LoadSignal string` (`load_signal,omitempty`); Apple entries with `Engine == "apple-foundation-models"`, `Port == 0`.

- [ ] **Step 1: Write the failing tests** (append to `catalog_test.go`)

```go
func TestCatalogUsesCollectorStateWhenPresent(t *testing.T) {
	snap := &models.ClusterSnapshot{Nodes: []models.NodeFacts{{
		Name: "n", Status: models.StatusComplete,
		ResidentModels: []models.ResidentModel{
			{Name: "warming", Runtime: "llama.cpp", Port: 8080, State: models.ModelCatalogLoading, LoadSignal: "health-loading"},
			{Name: "gone", Runtime: "llama.cpp", Port: 8081, State: models.ModelCatalogDown, LoadSignal: "health-silent"},
			{Name: "ok", Runtime: "llama.cpp", Port: 8082, State: models.ModelCatalogLoaded, LoadSignal: "health-ok", ContextWindow: 4096},
			{Name: "legacy", Runtime: "llama.cpp", Port: 8083},
		},
	}}}
	got := map[string]models.ModelCatalogEntry{}
	for _, e := range Catalog(snap, "test").Entries {
		got[e.Model] = e
	}
	if got["warming"].State != models.ModelCatalogLoading || got["gone"].State != models.ModelCatalogDown {
		t.Fatalf("states = %v / %v", got["warming"].State, got["gone"].State)
	}
	if got["ok"].State != models.ModelCatalogLoaded || got["ok"].ContextWindow != 4096 || got["ok"].LoadSignal != "health-ok" {
		t.Fatalf("ok = %+v", got["ok"])
	}
	if got["legacy"].State != models.ModelCatalogLoaded {
		t.Fatalf("legacy row without state = %q, want #507 mapping loaded", got["legacy"].State)
	}
}

func TestCatalogAddsAppleFoundationModelsEntry(t *testing.T) {
	ready := &models.ClusterSnapshot{Nodes: []models.NodeFacts{{
		Name: "mac", Status: models.StatusComplete,
		AppleFM: &models.AppleFoundationModelsInfo{Available: true, Verified: true, State: models.AppleFMReady, ContextWindow: 8192, Model: "AFM 3 Core", Capabilities: []string{"toolCalling"}},
	}}}
	entries := Catalog(ready, "test").Entries
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	e := entries[0]
	if e.Engine != "apple-foundation-models" || e.State != models.ModelCatalogLoaded || e.Model != "AFM 3 Core" || e.ContextWindow != 8192 || e.Locality != models.ModelLocalityOnNode || e.LoadSignal != "availability" {
		t.Fatalf("apple entry = %+v", e)
	}

	for state, want := range map[string]int{models.AppleFMCold: 1, models.AppleFMUnavailable: 0, models.AppleFMUnknown: 0, "": 0} {
		snap := &models.ClusterSnapshot{Nodes: []models.NodeFacts{{Name: "mac", Status: models.StatusComplete, AppleFM: &models.AppleFoundationModelsInfo{State: state}}}}
		got := Catalog(snap, "test").Entries
		if len(got) != want {
			t.Fatalf("state %q: entries = %+v, want %d", state, got, want)
		}
		if want == 1 && (got[0].State != models.ModelCatalogInstalled || got[0].Model != "apple-foundation-models") {
			t.Fatalf("cold entry = %+v", got[0])
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./hack/hermetic-go-test.sh -count=1 -run 'TestCatalogUsesCollectorState|TestCatalogAddsApple' ./internal/modelinventory/`
Expected: FAIL (`e.ContextWindow undefined`, then wrong states).

- [ ] **Step 3: Implement**

In `ModelCatalogEntry` (after `SizeBytes`) add:

```go
	// ContextWindow is the served context the runtime stated; 0 = unknown.
	ContextWindow int `json:"context_window,omitempty" yaml:"context_window,omitempty"`
	// LoadSignal names the evidence behind State.
	LoadSignal string `json:"load_signal,omitempty" yaml:"load_signal,omitempty"`
```

In `catalog.go`, in the resident loop of `nodeCatalogEntries`, replace `e.State = residentState(engine)` with:

```go
		e.State = residentState(r)
		e.LoadSignal = r.LoadSignal
		if r.ContextWindow > 0 {
			e.ContextWindow = r.ContextWindow
		}
```

Replace `residentState(engine string)` with:

```go
// residentState prefers the state the collector observed from a load
// signal. Rows without one (older collectors, or curl absent on the node)
// keep the #507 mapping: loaded only for ollama and llama.cpp.
func residentState(r models.ResidentModel) models.ModelCatalogState {
	if r.State != "" {
		return r.State
	}
	switch strings.ToLower(strings.TrimSpace(r.Runtime)) {
	case "ollama", "llama.cpp":
		return models.ModelCatalogLoaded
	default:
		return models.ModelCatalogListed
	}
}
```

At the end of `nodeCatalogEntries`, before building `out`, add:

```go
	if a := node.AppleFM; a != nil && (a.State == models.AppleFMReady || a.State == models.AppleFMCold) {
		name := a.Model
		if name == "" {
			name = "apple-foundation-models"
		}
		state := models.ModelCatalogLoaded
		if a.State == models.AppleFMCold {
			state = models.ModelCatalogInstalled
		}
		add("apple-foundation-models\x00"+name, models.ModelCatalogEntry{
			Model:         name,
			Engine:        "apple-foundation-models",
			State:         state,
			Locality:      models.ModelLocalityOnNode,
			Capabilities:  append([]string(nil), a.Capabilities...),
			ContextWindow: a.ContextWindow,
			LoadSignal:    "availability",
		})
	}
```

- [ ] **Step 4: Run to verify it passes**

Run: `./hack/hermetic-go-test.sh -count=1 ./internal/modelinventory/ ./internal/models/ ./cmd/axis/`
Expected: all `ok` (the #507 MLX test `TestCatalogMarksMLXResidentListedNotLoaded` still passes through the fallback).

- [ ] **Step 5: Commit**

```bash
git add internal/modelinventory/ internal/models/model_catalog.go
git commit -m "feat(model): catalog uses collector load state; add Apple Foundation Models entry"
```

---

### Task 3: llama.cpp collector probes `/health`, `/props`, `/v1/models`

**Files:**
- Modify: `internal/facts/tools.go` (`LlamaServerDiscoveryScript`, per-PID loop, the `ITEM=` line)
- Test: `internal/facts/resident_models_test.go`

**Interfaces:**
- Consumes: Task 1 JSON keys `state`, `load_signal`, `context_window`, `provenance`.
- Produces: llama.cpp resident rows with `state` in {`loaded`, `loading`, `listed`, `down`} when `curl` exists; no `state` when it does not.

- [ ] **Step 1: Write the failing tests** (append; reuse the stub style of `TestLlamaServerDiscoveryScriptFindsRunningBinaryOutsidePATH`)

```go
// runLlamaProbe runs the real script with a llama-server process on :8181
// serving model.gguf, and the given curl stub.
func runLlamaProbe(t *testing.T, curlBody string) llamaServerDiscoveryPayload {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin := t.TempDir()
	model := filepath.Join(t.TempDir(), "served-model.gguf")
	if err := os.WriteFile(model, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub("pgrep", `echo 4242`)
	stub("ps", `echo "/opt/llama.cpp/bin/llama-server --model `+model+` --port 8181"`)
	stub("readlink", `echo /opt/llama.cpp/bin/llama-server`)
	stub("lsof", `exit 1`)
	stub("ss", `exit 1`)
	stub("netstat", `exit 1`)
	tools := []string{"head", "awk", "grep", "basename", "sed", "stat"}
	if curlBody != "" {
		stub("curl", curlBody)
	}
	cmd := exec.Command("bash", "-c", LlamaServerDiscoveryScript)
	cmd.Env = withExactToolPATH(t, bin, tools...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var payload llamaServerDiscoveryPayload
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json %q: %v", bytes.TrimSpace(out), err)
	}
	if len(payload.ResidentModels) != 1 {
		t.Fatalf("residents = %+v, want 1", payload.ResidentModels)
	}
	return payload
}

func TestLlamaServerHealthOKIsLoadedWithServedContext(t *testing.T) {
	got := runLlamaProbe(t, `case "$*" in
  *"/health"*) printf 200 ;;
  *"/props"*) echo '{"default_generation_settings":{"n_ctx":4096,"model":"x"},"total_slots":1}' ;;
  *"/v1/models"*) echo '{"data":[{"id":"/models/served-model.gguf","meta":{"n_ctx_train":131072}}]}' ;;
esac`).ResidentModels[0]
	if got.State != models.ModelCatalogLoaded || got.LoadSignal != "health-ok" {
		t.Fatalf("state = %q signal %q, want loaded/health-ok", got.State, got.LoadSignal)
	}
	if got.ContextWindow != 4096 {
		t.Fatalf("context_window = %d, want served n_ctx 4096 (not n_ctx_train)", got.ContextWindow)
	}
	if got.Name != "served-model" || got.Provenance["state"] != "GET /health 200" {
		t.Fatalf("row = %+v", got)
	}
}

func TestLlamaServerHealth503IsLoading(t *testing.T) {
	got := runLlamaProbe(t, `case "$*" in *"/health"*) printf 503 ;; esac`).ResidentModels[0]
	if got.State != models.ModelCatalogLoading || got.ContextWindow != 0 {
		t.Fatalf("row = %+v, want loading without context", got)
	}
}

func TestLlamaServerSilentIsDown(t *testing.T) {
	got := runLlamaProbe(t, `printf 000; exit 7`).ResidentModels[0]
	if got.State != models.ModelCatalogDown || got.LoadSignal != "health-silent" {
		t.Fatalf("row = %+v, want down/health-silent", got)
	}
}

// A keyed server: /health is public, the rest answers 401.
func TestLlamaServerKeyedIsLoadedFromHealth(t *testing.T) {
	got := runLlamaProbe(t, `case "$*" in
  *"/health"*) printf 200 ;;
  *) echo '{"error":{"message":"Invalid API Key","code":401}}' ;;
esac`).ResidentModels[0]
	if got.State != models.ModelCatalogLoaded || got.ContextWindow != 0 || got.Name != "served-model" {
		t.Fatalf("row = %+v, want loaded under argv name, no context", got)
	}
}

func TestLlamaServerServedIDDifferentFromArgvIsListed(t *testing.T) {
	got := runLlamaProbe(t, `case "$*" in
  *"/health"*) printf 200 ;;
  *"/v1/models"*) echo '{"data":[{"id":"other-model.gguf"}]}' ;;
esac`).ResidentModels[0]
	if got.State != models.ModelCatalogListed || got.Name != "other-model" || got.LoadSignal != "served-id-differs" {
		t.Fatalf("row = %+v, want listed under served id", got)
	}
}

// Without curl the collector cannot observe load state; it leaves state
// empty and Catalog keeps the #507 mapping.
func TestLlamaServerWithoutCurlLeavesStateEmpty(t *testing.T) {
	got := runLlamaProbe(t, "").ResidentModels[0]
	if got.State != "" {
		t.Fatalf("state = %q, want empty without curl", got.State)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./hack/hermetic-go-test.sh -count=1 -run 'TestLlamaServer' ./internal/facts/`
Expected: the five probe tests FAIL (`state = ""`); `WithoutCurl` passes.

- [ ] **Step 3: Implement**

In `LlamaServerDiscoveryScript`, after the line that sets `MNAME=$(basename "$MODEL" | sed 's/\.[^.]*$//')` and before `MNAME_ESC=...`, insert:

```bash
			STATE_JSON=""
			CTX_JSON=""
			if command -v curl >/dev/null 2>&1; then
				# /health and /v1/health are the only endpoints llama-server
				# exempts from --api-key; 200 means the model finished loading.
				HEALTH=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:$THIS_PORT/health" 2>/dev/null || true)
				case "$HEALTH" in
					200) STATE="loaded"; LOAD_SIGNAL="health-ok" ;;
					503) STATE="loading"; LOAD_SIGNAL="health-loading" ;;
					*) STATE="down"; LOAD_SIGNAL="health-silent"; HEALTH="${HEALTH:-000}" ;;
				esac
				PROV="\"state\":\"GET /health $HEALTH\""
				if [ "$STATE" = "loaded" ]; then
					N_CTX=$(curl -s --max-time 2 "http://127.0.0.1:$THIS_PORT/props" 2>/dev/null | grep -o '"n_ctx":[0-9]*' | head -1 | sed 's/.*://')
					if printf '%s' "$N_CTX" | grep -qE '^[0-9]+$'; then
						CTX_JSON=",\"context_window\":$N_CTX"
						PROV="$PROV,\"context_window\":\"GET /props default_generation_settings.n_ctx\""
					fi
					SERVED=$(curl -s --max-time 2 "http://127.0.0.1:$THIS_PORT/v1/models" 2>/dev/null | grep -o '"id":"[^"]*"' | head -1 | sed 's/^"id":"//; s/"$//')
					if [ -n "$SERVED" ]; then
						SERVED_NAME=$(basename "$SERVED" | sed 's/\.[^.]*$//')
						if [ "$SERVED_NAME" != "$MNAME" ]; then
							STATE="listed"; LOAD_SIGNAL="served-id-differs"; MNAME="$SERVED_NAME"
							PROV="$PROV,\"name\":\"GET /v1/models data[0].id\""
						fi
					fi
				fi
				STATE_JSON=",\"state\":\"$STATE\",\"load_signal\":\"$LOAD_SIGNAL\",\"provenance\":{$PROV}"
			fi
```

Then change the `ITEM=` line so the item ends with `...\"source\":\"llama-server-ps\"$STATE_JSON$CTX_JSON$GPU_JSON}"` (insert `$STATE_JSON$CTX_JSON` before `$GPU_JSON`).

Note: `/props` returns the first `"n_ctx"` under `default_generation_settings`; `/v1/models` `meta.n_ctx_train` is a different key and never matches `"n_ctx":`.

- [ ] **Step 4: Run to verify they pass, plus the existing script tests**

Run: `./hack/hermetic-go-test.sh -count=1 ./internal/facts/`
Expected: `ok`. Existing llama tests either have no `curl` on their exact PATH (state empty) or a sandboxed PATH where real curl gets connection refused (state `down`); neither asserts on the new fields.

- [ ] **Step 5: Live check, then commit**

Run on one unkeyed and one keyed llama-server node (SSH by alias):
```bash
SCRIPT=$(awk '/^const LlamaServerDiscoveryScript = `/{f=1; sub(/^const LlamaServerDiscoveryScript = `/,""); print; next} f&&/^\t`$/{exit} f{print}' internal/facts/tools.go)
ssh <node> "$SCRIPT" | jq -c '.resident_models[] | {name, state, load_signal, context_window, provenance}'
```
Expected: unkeyed → `loaded`, `health-ok`, `context_window` set; keyed → `loaded`, `health-ok`, no `context_window`.

```bash
git add internal/facts/tools.go internal/facts/resident_models_test.go
git commit -m "feat(facts): llama.cpp load state from /health, served context from /props"
```

---

### Task 4: MLX collector takes the served model from argv

**Files:**
- Modify: `internal/facts/tools.go` (`MLXDiscoveryScript`: the `RESIDENT="[]"` block through the `python3` heredoc)
- Test: `internal/facts/resident_models_test.go` (update `TestMLXDiscoveryScriptReportsResidentRAMNotVRAM`, `TestMLXDiscoveryScriptReportsProcessGenerationEvidence` stubs to include `--model`; add new tests)

**Interfaces:**
- Consumes: Task 1 JSON keys.
- Produces: at most one MLX resident per server, `source: mlx-argv`, `state` `listed` (endpoint answered) or `down` (silent), no `state` without curl; never `loaded`.

- [ ] **Step 1: Write the failing tests** (append)

```go
func runMLXProbe(t *testing.T, args, curlBody string) mlxDiscoveryPayload {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin := t.TempDir()
	stub := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub("mlx_lm", `exit 0`)
	stub("pgrep", `echo 4242`)
	stub("ps", `case "$*" in
  "-p 4242 -o args=") echo "`+args+`" ;;
  "-o rss= -p 4242") echo 3145728 ;;
esac`)
	if curlBody != "" {
		stub("curl", curlBody)
	}
	cmd := exec.Command("bash", "-c", MLXDiscoveryScript)
	cmd.Env = withExactToolPATH(t, bin, "head", "awk", "grep", "sed", "python3")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var payload mlxDiscoveryPayload
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json %q: %v", bytes.TrimSpace(out), err)
	}
	return payload
}

// mlx_lm.server's /v1/models lists the Hugging Face cache, not the loaded
// model. Only the argv model is published, and as listed, never loaded.
func TestMLXDiscoveryPublishesArgvModelNotCacheListing(t *testing.T) {
	got := runMLXProbe(t, "python -m mlx_lm.server --model mlx-community/Served-4bit --port 8183",
		`case "$*" in *"-w"*) printf 200 ;; *) echo '{"data":[{"id":"mlx-community/Cached-A"},{"id":"mlx-community/Cached-B"},{"id":"mlx-community/Served-4bit"}]}' ;; esac`)
	if len(got.ResidentModels) != 1 {
		t.Fatalf("residents = %+v, want only the served model", got.ResidentModels)
	}
	r := got.ResidentModels[0]
	if r.Name != "Served-4bit" || r.Source != "mlx-argv" || r.State != models.ModelCatalogListed || r.LoadSignal != "none" {
		t.Fatalf("resident = %+v", r)
	}
	if r.Provenance["name"] != "argv --model" {
		t.Fatalf("provenance = %v", r.Provenance)
	}
}

func TestMLXDiscoverySilentServerIsDown(t *testing.T) {
	got := runMLXProbe(t, "python -m mlx_lm.server --model /models/local-mlx --port 8183", `printf 000; exit 7`)
	if len(got.ResidentModels) != 1 || got.ResidentModels[0].State != models.ModelCatalogDown || got.ResidentModels[0].Name != "local-mlx" {
		t.Fatalf("residents = %+v, want local-mlx down", got.ResidentModels)
	}
}

// A server started without --model serves nothing named; no resident row.
func TestMLXDiscoveryWithoutModelArgPublishesNoResident(t *testing.T) {
	got := runMLXProbe(t, "python -m mlx_lm.server --port 8183", `printf 200`)
	if !got.Running || len(got.ResidentModels) != 0 {
		t.Fatalf("payload = %+v, want running with no residents", got)
	}
}
```

Update the two existing MLX tests' `ps` stubs from `python -m mlx_lm.server --port 8183` to `python -m mlx_lm.server --model org/mlx-model --port 8183` and add a `curl` stub `printf 200`; their assertions (one resident, `size_ram_mb` 3072, generation evidence) stay.

- [ ] **Step 2: Run to verify it fails**

Run: `./hack/hermetic-go-test.sh -count=1 -run 'TestMLXDiscovery' ./internal/facts/`
Expected: FAIL (three residents from the listing; no `state`).

- [ ] **Step 3: Implement**

In `MLXDiscoveryScript`, replace everything from `RESIDENT="[]"` through the closing `fi` of the `if [ "$RUNNING" = "true" ] && command -v curl` block (the `python3` heredoc) with:

```bash
		RESIDENT="[]"
		if [ "$RUNNING" = "true" ]; then
			# mlx_lm.server's /v1/models lists the Hugging Face cache, not the
			# loaded model. The served model is the one on the command line.
			MODEL_ARG=$(echo "$CMDLINE" | awk '{for(i=1;i<=NF;i++){if($i=="--model"||$i=="-m"){print $(i+1);exit}if($i~/^(--model=|-m=)/){sub(/^[^=]*=/,"",$i);print $i;exit}}}')
			if [ -n "$MODEL_ARG" ]; then
				MNAME="${MODEL_ARG%/}"
				MNAME="${MNAME##*/}"
				STATE_JSON=""
				if command -v curl >/dev/null 2>&1; then
					HTTP=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:$PORT/v1/models" 2>/dev/null || true)
					case "$HTTP" in 2*) STATE="listed" ;; *) STATE="down"; HTTP="${HTTP:-000}" ;; esac
					STATE_JSON=",\"state\":\"$STATE\",\"load_signal\":\"none\",\"provenance\":{\"name\":\"argv --model\",\"state\":\"GET /v1/models $HTTP\"}"
				fi
				MNAME_ESC=$(echo "$MNAME" | sed 's/\\/\\\\/g; s/"/\\"/g')
				EXE_ESC=$(echo "$EXECUTABLE" | sed 's/\\/\\\\/g; s/"/\\"/g')
				TOK_ESC=$(echo "$PROCESS_START_TOKEN" | sed 's/\\/\\\\/g; s/"/\\"/g')
				RESIDENT="[{\"name\":\"$MNAME_ESC\",\"runtime\":\"mlx\",\"processor\":\"gpu\",\"size_ram_mb\":$SIZE_MB,\"source\":\"mlx-argv\",\"pid\":$PGREP,\"executable\":\"$EXE_ESC\",\"process_start_token\":\"$TOK_ESC\"$STATE_JSON}]"
			fi
		fi
```

(If the environment's edit hook flags `127.0.0.1` as a hard-coded host, add `# interlinked-ignore: ubs_hardcoded_localhost — probes the node's own loopback server` on the line above, as #507 did.)

- [ ] **Step 4: Run to verify they pass**

Run: `./hack/hermetic-go-test.sh -count=1 ./internal/facts/`
Expected: `ok`.

- [ ] **Step 5: Live check on a Mac with MLX installed, then commit**

```bash
SCRIPT=$(awk '/^const MLXDiscoveryScript = `/{f=1; sub(/^const MLXDiscoveryScript = `/,""); print; next} f&&/^\t`$/{exit} f{print}' internal/facts/tools.go)
ssh <mac-alias> "$SCRIPT"
```
Expected: no MLX server running → `{"installed":true,"running":false,...,"resident_models":[]}`.

```bash
git add internal/facts/tools.go internal/facts/resident_models_test.go
git commit -m "fix(facts): MLX resident is the argv model, not the Hugging Face cache listing"
```

---

### Task 5: Apple Foundation Models read-only facts

**Files:**
- Modify: `internal/facts/apple_foundation_models_source.go` (helper source composed with a shared `--facts` body; discovery script)
- Modify: `hack/apple-foundation-models.swift` (must equal the embedded source)
- Create: `internal/facts/apple_fm_facts.go`
- Modify: `internal/facts/remote.go` (`discoverAppleFoundationModels`: replace the `switch` on probe output)
- Modify: `internal/facts/local_ai.go` (`runAppleFoundationModelsProbe` argument, `detectAppleFoundationModels` body)
- Test: `internal/facts/apple_fm_facts_test.go` (new), `internal/facts/resident_models_test.go` (Apple remote test cases)

**Interfaces:**
- Consumes: Task 1 `AppleFoundationModelsInfo` fields and `AppleFM*` constants.
- Produces: `func appleFMFromProbe(version, out string, err error) *models.AppleFoundationModelsInfo`; Swift helper flag `--facts` printing one JSON object with keys `availability` (`available`|`unavailable`), `reason`, `context_size`, `model`, `capabilities`, `languages`.

- [ ] **Step 1: Write the failing tests**

`internal/facts/apple_fm_facts_test.go`:

```go
package facts

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestAppleFMFromProbeReady(t *testing.T) {
	got := appleFMFromProbe("27.2", `{"availability":"available","context_size":8192,"model":"AFM 3 Core Advanced","capabilities":["toolCalling","guidedGeneration","vision"],"languages":24}`, nil)
	if !got.Available || !got.Verified || got.State != models.AppleFMReady {
		t.Fatalf("got %+v, want available/verified/ready", got)
	}
	if got.ContextWindow != 8192 || got.Model != "AFM 3 Core Advanced" || !slices.Contains(got.Capabilities, "vision") || got.Version != "27.2" {
		t.Fatalf("facts = %+v", got)
	}
}

func TestAppleFMFromProbeModelNotReadyIsCold(t *testing.T) {
	got := appleFMFromProbe("27.2", `{"availability":"unavailable","reason":"modelNotReady"}`, nil)
	if got.Available || got.State != models.AppleFMCold || got.Reason != "modelNotReady" {
		t.Fatalf("got %+v, want cold/modelNotReady", got)
	}
}

func TestAppleFMFromProbeNotEnabledIsUnavailable(t *testing.T) {
	got := appleFMFromProbe("27.2", `{"availability":"unavailable","reason":"appleIntelligenceNotEnabled"}`, nil)
	if got.Available || got.State != models.AppleFMUnavailable || got.Reason != "appleIntelligenceNotEnabled" {
		t.Fatalf("got %+v", got)
	}
}

// A timeout says nothing about availability.
func TestAppleFMFromProbeTimeoutIsUnknown(t *testing.T) {
	got := appleFMFromProbe("27.2", "", context.DeadlineExceeded)
	if got.Available || got.State != models.AppleFMUnknown {
		t.Fatalf("got %+v, want unknown", got)
	}
	got = appleFMFromProbe("27.2", "", errors.New("Process exited with status 1: context deadline exceeded"))
	if got.State != models.AppleFMUnknown {
		t.Fatalf("got %+v, want unknown for wrapped deadline", got)
	}
}

// Older helpers still print the legacy markers.
func TestAppleFMFromProbeLegacyOutputs(t *testing.T) {
	if got := appleFMFromProbe("27.2", "OK\n", nil); !got.Available || !got.Verified || got.State != models.AppleFMReady {
		t.Fatalf("OK -> %+v", got)
	}
	if got := appleFMFromProbe("27.2", "UNAVAILABLE:modelNotReady", nil); got.Available || got.State != models.AppleFMUnavailable {
		t.Fatalf("UNAVAILABLE -> %+v", got)
	}
	if got := appleFMFromProbe("27.2", "UNVERIFIED", nil); got.Available || got.State != models.AppleFMUnknown {
		t.Fatalf("UNVERIFIED -> %+v", got)
	}
}

// The checked-in helper is the embedded helper; they must not drift.
func TestAppleFMHelperSourceMatchesHackCopy(t *testing.T) {
	hack, err := os.ReadFile("../../hack/apple-foundation-models.swift")
	if err != nil {
		t.Fatal(err)
	}
	if string(hack) != appleFoundationModelsHelperSource {
		t.Fatal("hack/apple-foundation-models.swift differs from appleFoundationModelsHelperSource")
	}
}

// Sweeps never generate: the discovery script runs --facts, never --self-test.
func TestAppleFMDiscoveryScriptNeverGenerates(t *testing.T) {
	if !strings.Contains(AppleFoundationModelsDiscoveryScript, "--facts") || strings.Contains(AppleFoundationModelsDiscoveryScript, "--self-test") {
		t.Fatal("discovery script must run --facts and never --self-test")
	}
}
```

(add `"strings"` to the imports.)

In `resident_models_test.go`'s `TestRemoteCollectorDiscoversAppleFoundationModelsOnDarwinArm64`, add a subtest with the exec result `{"availability":"available","context_size":4096,"model":"AFM 3 Core"}` asserting `State == models.AppleFMReady`, `ContextWindow == 4096`, and the `apple-foundation-models` tool appended; and one with `err: context.DeadlineExceeded` asserting `State == models.AppleFMUnknown` and no tool appended.

- [ ] **Step 2: Run to verify it fails**

Run: `./hack/hermetic-go-test.sh -count=1 -run 'TestAppleFM|TestRemoteCollectorDiscoversAppleFoundationModels' ./internal/facts/`
Expected: FAIL to compile (`appleFMFromProbe undefined`).

- [ ] **Step 3: Implement the Go parser** — `internal/facts/apple_fm_facts.go`:

```go
package facts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

type appleFMFactsPayload struct {
	Availability string   `json:"availability"`
	Reason       string   `json:"reason"`
	ContextSize  int      `json:"context_size"`
	Model        string   `json:"model"`
	Capabilities []string `json:"capabilities"`
}

// appleFMFromProbe turns the Apple helper's output into node facts. The
// helper's --facts mode reads SystemLanguageModel availability, contextSize,
// variant, and capabilities without generating. A timeout is unknown, never
// unavailable. Legacy outputs (OK, AVAILABLE, UNAVAILABLE:…, UNVERIFIED)
// from older helpers are still understood.
func appleFMFromProbe(version, out string, err error) *models.AppleFoundationModelsInfo {
	info := &models.AppleFoundationModelsInfo{Version: version}
	trimmed := strings.TrimSpace(out)

	if err != nil && (errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "deadline exceeded")) {
		info.State = models.AppleFMUnknown
		info.Error = "probe timed out; availability unknown"
		return info
	}

	if strings.HasPrefix(trimmed, "{") {
		var p appleFMFactsPayload
		if jsonErr := json.Unmarshal([]byte(trimmed), &p); jsonErr == nil && p.Availability != "" {
			info.Verified = true
			info.Reason = p.Reason
			switch p.Availability {
			case "available":
				info.Available = true
				info.State = models.AppleFMReady
				info.ContextWindow = p.ContextSize
				info.Model = p.Model
				info.Capabilities = p.Capabilities
			case "unavailable":
				info.State = models.AppleFMUnavailable
				if p.Reason == "modelNotReady" {
					info.State = models.AppleFMCold
				}
				info.Error = "unavailable: " + p.Reason
			default:
				info.Verified = false
				info.State = models.AppleFMUnknown
				info.Error = "unrecognised availability " + p.Availability
			}
			return info
		}
	}

	switch {
	case err == nil && (trimmed == "OK" || trimmed == "AVAILABLE"):
		info.Available, info.Verified, info.State = true, true, models.AppleFMReady
	case err == nil && strings.HasPrefix(trimmed, "UNAVAILABLE:"):
		info.State = models.AppleFMUnavailable
		info.Error = trimmed
	case err == nil && trimmed == "UNVERIFIED":
		info.State = models.AppleFMUnknown
		info.Error = "foundation models framework imported but runtime availability unverified"
	default:
		info.State = models.AppleFMUnknown
		switch {
		case trimmed != "":
			info.Error = trimmed
		case err != nil:
			info.Error = err.Error()
		default:
			info.Error = "apple foundation models probe failed"
		}
	}
	return info
}
```

- [ ] **Step 4: Implement the Swift `--facts` mode**

In `apple_foundation_models_source.go`, add a constant holding the facts function (no single quotes or backticks, so it can also sit inside a bash single-quoted `xcrun swift -e` argument):

```go
// appleFoundationModelsFactsSwift reads SystemLanguageModel state without
// creating a session or generating. It defines appleFMFactsJSON().
const appleFoundationModelsFactsSwift = `func appleFMFactsJSON() -> String {
	let model = SystemLanguageModel.default
	var out: [String: Any] = [:]
	switch model.availability {
	case .available:
		out["availability"] = "available"
	case .unavailable(let reason):
		out["availability"] = "unavailable"
		switch reason {
		case .appleIntelligenceNotEnabled: out["reason"] = "appleIntelligenceNotEnabled"
		case .deviceNotEligible: out["reason"] = "deviceNotEligible"
		case .modelNotReady: out["reason"] = "modelNotReady"
		@unknown default: out["reason"] = "\(reason)"
		}
	}
	if model.isAvailable {
		out["context_size"] = model.contextSize
		out["languages"] = model.supportedLanguages.count
		if #available(macOS 27.0, *) {
			out["model"] = model.variant.displayName
			var caps: [String] = []
			let known: [(String, LanguageModelCapabilities.Capability)] = [("toolCalling", .toolCalling), ("guidedGeneration", .guidedGeneration), ("reasoning", .reasoning), ("vision", .vision)]
			for (name, cap) in known where model.capabilities.contains(cap) { caps.append(name) }
			out["capabilities"] = caps
		}
	}
	guard let data = try? JSONSerialization.data(withJSONObject: out, options: [.sortedKeys]) else { return "{}" }
	return String(decoding: data, as: UTF8.self)
}
`
```

Edit the existing `appleFoundationModelsHelperSource` literal in place (it stays a single raw-string constant), making exactly three insertions:
1. Immediately after the closing `}` of `struct CLIError` and its following blank line, paste the body of `appleFoundationModelsFactsSwift` verbatim (from `func appleFMFactsJSON() -> String {` through its closing `}`), followed by one blank line.
2. In `usage()`, after the line `	       apple-foundation-models.swift --self-test`, add the line `	       apple-foundation-models.swift --facts`.
3. Immediately before the line `let parsed: (prompt: String, json: Bool)`, insert:

```swift
if CommandLine.arguments.dropFirst().contains("--facts") {
	print(appleFMFactsJSON())
	Darwin.exit(0)
}

```

Add to `apple_fm_facts_test.go` a test that keeps the two copies of the facts function identical:

```go
func TestAppleFMHelperEmbedsFactsFunction(t *testing.T) {
	if !strings.Contains(appleFoundationModelsHelperSource, appleFoundationModelsFactsSwift) {
		t.Fatal("helper source must contain appleFoundationModelsFactsSwift verbatim")
	}
}
```

Then make `hack/apple-foundation-models.swift` identical to the edited literal: apply the same three insertions to it with the Edit tool. `TestAppleFMHelperSourceMatchesHackCopy` fails on any difference, including whitespace.

Replace `AppleFoundationModelsDiscoveryScript` with (constant concatenation keeps the Swift body in one place):

```go
const AppleFoundationModelsDiscoveryScript = `set +e
for h in "$HOME/.axis/cache/apple-foundation-models-helper" "$HOME/Library/Caches/axis/apple-foundation-models-helper" "$HOME/.cache/axis/apple-foundation-models-helper"; do
  if [ -x "$h" ]; then
    out=$("$h" --facts 2>/dev/null)
    case "$out" in "{"*) printf '%s\n' "$out"; exit 0 ;; esac
  fi
done
xcrun swift -e 'import Foundation
import FoundationModels
` + appleFoundationModelsFactsSwift + `print(appleFMFactsJSON())' 2>/dev/null && exit 0
xcrun swift -e 'import FoundationModels' 2>/dev/null && echo "UNVERIFIED" && exit 0
exit 1
`
```

(A cached helper built from the old source has no `--facts`; its output is not JSON, so the loop falls through to the `xcrun` path. The local collector rebuilds the cached helper when the embedded source changes: `appleFoundationModelsHelperUpToDate`.)

- [ ] **Step 5: Wire both collectors to the parser**

In `remote.go` `discoverAppleFoundationModels`, replace the whole `switch { case err == nil && (trimmed == "OK" … default: … facts.AppleFM = info }` block, and the `trimmed`/`lines` locals above it, with:

```go
	facts.AppleFM = appleFMFromProbe(facts.OSVersion, out, err)
```

(the following `if facts.AppleFM != nil && facts.AppleFM.Available && facts.AppleFM.Verified { … }` tool-append block stays). `remote.go` shrinks.

In `local_ai.go`, change `appleFoundationModelsProbeCommandFn(ctx, result.path, "--self-test")` to `appleFoundationModelsProbeCommandFn(ctx, result.path, "--facts")`, and in `detectAppleFoundationModels` replace everything from `trimmedOut := strings.TrimSpace(out)` to the end of the function with:

```go
	return appleFMFromProbe(osVersion, out, err)
```

- [ ] **Step 6: Run to verify**

Run: `gofmt -l internal && ./hack/hermetic-go-test.sh -count=1 ./internal/facts/ ./internal/placement/ ./internal/modelinventory/`
Expected: `ok`. Fix any local-probe test that asserted the old `Available: err == nil` shape so it feeds `--facts` JSON (or `OK`) and asserts `State`.

- [ ] **Step 7: Live check on each macOS node, then commit**

Build a dev binary (`go build -o <scratch>/axis-dev ./cmd/axis`) and run `<scratch>/axis-dev model catalog --live --format json | jq '.entries[] | select(.engine=="apple-foundation-models")'`. Expected: one entry per macOS 26+/27 node with `state: loaded`, `context_window` matching the node (4096 or 8192), `model` "AFM 3 Core" / "AFM 3 Core Advanced" on 27.x, `load_signal: availability`. A node previously reported unavailable by timeout now shows `loaded` or is listed with `state: unknown` in its facts, never `unavailable`.

```bash
git add internal/facts/apple_foundation_models_source.go internal/facts/apple_fm_facts.go internal/facts/apple_fm_facts_test.go internal/facts/remote.go internal/facts/local_ai.go internal/facts/resident_models_test.go hack/apple-foundation-models.swift
git commit -m "feat(facts): Apple Foundation Models facts from availability, contextSize, variant; no generation in sweeps"
```

---

### Task 6: `/models` shows loading, down, and Apple entries honestly

**Files:**
- Modify: `internal/agent/model_target.go` (add `Loading bool`)
- Modify: `cmd/axis/agent_model_choices.go` (`catalogModelChoices`, `modelChoiceDetail`)
- Test: `cmd/axis/agent_model_choices_test.go`

**Interfaces:**
- Consumes: Task 2 catalog states and Apple entries.
- Produces: `ModelTarget.Loading`; Apple choices disabled with reason `runs in-process; no HTTP endpoint`; `down` choices disabled with reason `server not answering`.

- [ ] **Step 1: Write the failing tests** (append)

```go
func TestCollectModelChoicesLoadingDownAndApple(t *testing.T) {
	stubNoAIConfigAndRecordProbes(t)
	rt := catalogChoicesRuntime(t)
	rt.Snapshot.Nodes[1].ResidentModels = append(rt.Snapshot.Nodes[1].ResidentModels,
		models.ResidentModel{Name: "warming", Runtime: "llama.cpp", Port: 8090, State: models.ModelCatalogLoading},
		models.ResidentModel{Name: "gone", Runtime: "llama.cpp", Port: 8091, State: models.ModelCatalogDown},
	)
	rt.Snapshot.Nodes[1].AppleFM = &models.AppleFoundationModelsInfo{Available: true, Verified: true, State: models.AppleFMReady, Model: "AFM 3 Core"}
	choices := collectModelChoices(rt)

	warming, _ := choiceByModel(t, choices, "warming")
	if !warming.Loading || warming.Loaded || warming.Disabled {
		t.Fatalf("warming = %+v, want loading, selectable", warming)
	}
	gone, _ := choiceByModel(t, choices, "gone")
	if !gone.Disabled || gone.DisabledReason != "server not answering" {
		t.Fatalf("gone = %+v", gone)
	}
	afm, ok := choiceByModel(t, choices, "AFM 3 Core")
	if !ok || !afm.Disabled || afm.DisabledReason != "runs in-process; no HTTP endpoint" {
		t.Fatalf("afm = %+v", afm)
	}
	if got := modelChoiceDetail(warming); !strings.Contains(got, "loading") {
		t.Fatalf("detail %q, want loading", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./hack/hermetic-go-test.sh -count=1 -run 'TestCollectModelChoicesLoadingDownAndApple' ./cmd/axis/`
Expected: FAIL to compile (`warming.Loading undefined`).

- [ ] **Step 3: Implement**

`internal/agent/model_target.go`, after `Listed bool …`:

```go
	Loading      bool     // the runtime reports it is still loading
```

In `cmd/axis/agent_model_choices.go` `catalogModelChoices`, after the `Listed:` field line add `Loading: e.State == models.ModelCatalogLoading,`, and after the existing endpoint/port disable checks add:

```go
		switch {
		case e.Engine == "apple-foundation-models":
			choice.Disabled, choice.DisabledReason = true, "runs in-process; no HTTP endpoint"
		case e.State == models.ModelCatalogDown:
			choice.Disabled, choice.DisabledReason = true, "server not answering"
		}
```

In `modelChoiceDetail`, after the `Listed` block:

```go
	if c.Loading {
		parts = append(parts, "loading")
	}
```

- [ ] **Step 4: Run**

Run: `./hack/hermetic-go-test.sh -count=1 ./cmd/axis/ ./internal/agent/`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/model_target.go cmd/axis/agent_model_choices.go cmd/axis/agent_model_choices_test.go
git commit -m "feat(agent): /models shows loading, down, and Apple Foundation Models entries"
```

---

### Task 7: Docs, full gates, live fleet verification, PR

**Files:**
- Modify: `docs/current-state.md` (the model catalog bullet from #507)

- [ ] **Step 1: Update the catalog sentence**

Replace the parenthetical about state in the model catalog bullet with: "state (`loaded` only from a load signal: Ollama `/api/ps`, llama.cpp `-m` plus a public `/health` 200, or Apple `SystemLanguageModel.availability == .available`; `loading` on llama.cpp `/health` 503; `listed` when a server names a model without a load signal, including the MLX server's command-line model; `down` when the process is seen but its endpoint is silent; `installed` otherwise), served context where the runtime states it (llama.cpp `/props` `n_ctx`, Apple `contextSize`)".

- [ ] **Step 2: Full gates**

Run: `GOTOOLCHAIN=go1.26.9 ./hack/ci-preflight.sh`
Expected: `CI preflight passed`. Then restore `artifacts/` as in Global Constraints.

- [ ] **Step 3: Live fleet verification**

Build `go build -o <scratch>/axis-dev ./cmd/axis` and run `<scratch>/axis-dev model catalog --live`. Expected: llama.cpp rows `loaded` with `load_signal` `health-ok` (keyed servers included); unkeyed llama.cpp rows carry `context_window`; no MLX row unless a server runs with `--model`; Apple rows on macOS nodes with per-node `context_window`; no node's Apple facts read `unavailable` from a timeout.

- [ ] **Step 4: Commit, push, open the PR**

```bash
git add docs/current-state.md
git commit -m "docs: describe probe-backed load state in current-state"
git push -u origin <branch>
gh pr create --base main --title "Probe-backed model load state for llama.cpp, MLX, and Apple Foundation Models" --body-file <body>
```

The PR body lists the spec, each commit's change, the verification evidence (without hostnames), and the out-of-scope items (PR 2 candidates, PR 3 PCC and disk kinds). Do not merge (operator merges).
