# AXIS Basic Usage Examples

These examples show common CLI workflows for inspecting your cluster,
finding the best node for a task, and running guarded executions.

## Prerequisites

1. Build or install AXIS:
   ```bash
   make build
   # or
   go install github.com/toasterbook88/axis/cmd/axis@latest
   ```

2. Create your cluster config at `~/.axis/nodes.yaml`.
   See [`nodes.yaml`](nodes.yaml) for a full example.

3. Ensure SSH key-based auth works to every remote node.

## Inspect the local machine

```bash
axis facts
```

Shows hostname, OS, architecture, RAM, CPU, GPU, installed tools,
Ollama status, and TurboQuant / Apple Foundation Model support.

Use `--format json` or `--format yaml` for programmatic consumption:

```bash
axis facts --format json | jq '.ram_mb'
```

## Inspect the cluster

```bash
axis status
```

Collects a live snapshot from every configured node and prints a summary
with node health, resources, and warnings.

Use the daemon cache for faster reads:

```bash
axis status --cached
```

Require cached data only (fail if the daemon is down):

```bash
axis status --cached-only
```

## Find the best node for a task

```bash
axis task place "run ollama inference on a 7b model"
```

This is advisory only. It returns the selected node, a FitScore (0–100),
and reasoning strings. Use `--cached` to avoid live discovery:

```bash
axis task place --cached "transcode a video with ffmpeg"
```

## Get compact context for an LLM

```bash
axis task context "deploy a docker container"
```

Emits a ~200-token summary of cluster state tailored to the task description.
Useful for pasting into Gemini, Copilot, or Codex prompts.

```bash
axis task context --format json "compile a large rust project"
```

## Run a task on the best node

AXIS executes tasks with safety gates. You must be explicit about intent.

### Run a built-in script

```bash
axis task run --script "start local ollama server"
```

### Run a raw command

```bash
axis task run --exec "nvidia-smi"
```

### Dry-run to preview the plan

```bash
axis task run --dry-run --exec "ffmpeg -i input.mp4 output.mp4"
```

## Placement explanation

See a detailed per-node breakdown of why a node was or was not selected:

```bash
axis placement explain "run a 7b LLM inference"
```

## Health diagnostics

```bash
axis doctor
```

Validates config syntax, SSH connectivity, and daemon health.

## Learn more

- `axis --help` — list all commands
- `axis <command> --help` — flags for a specific command
