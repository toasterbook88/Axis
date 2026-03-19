# Local Assist Runbook

Use this when you want a safe local AXIS assist flow without assuming the model runtime is healthy.

## Verify

```bash
go test ./...
go run ./cmd/axis task context "analyze this repo"
go run ./cmd/axis chat "Tell me a joke"
```

## Expected behavior

- `axis task context` emits a compact context block even if some nodes are unreachable.
- `axis chat` tries the local Ollama daemon first.
- If Ollama is reachable but the model is missing, AXIS prints the structured fallback instead of crashing.
- If Ollama is down entirely, AXIS still prints the fallback with the connection error.

## Useful follow-ups

```bash
ollama list
ollama pull llama3
go run ./cmd/axis mcp serve
```
