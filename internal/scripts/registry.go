package scripts

import (
	"sort"
	"strings"
)

type Script struct {
	Name          string   `json:"name"`
	Command       string   `json:"command"`
	RequiredTools []string `json:"required_tools"`
	EstRAMMB      int64    `json:"est_ram_mb"`
	Description   string   `json:"description"`
	Category      string   `json:"category"`
	Keywords      []string `json:"keywords,omitempty"`
}

var Registry = map[string][]Script{
	"git": {
		{Name: "git-status", Command: `cat "$AXIS_CONTEXT_FILE" | jq -r '.snapshot.nodes[] | select(.name == "'$BEST_NODE'") | .name' && git status --short && git log --oneline -10`, RequiredTools: []string{"git"}, EstRAMMB: 128, Description: "repo status on best node", Category: "git", Keywords: []string{"git status", "repo status", "status repo", "inspect repo", "working tree", "git log"}},
		{Name: "git-deep-analyze", Command: `cat "$AXIS_CONTEXT_FILE" | jq '.snapshot.summary' && git status && git diff --stat HEAD~5`, RequiredTools: []string{"git"}, EstRAMMB: 256, Description: "deep repo analysis with cluster context", Category: "git", Keywords: []string{"analyze repo", "review changes", "git diff", "inspect changes", "deep repo"}},
	},

	"build": {
		{Name: "go-full-build", Command: `cat "$AXIS_CONTEXT_FILE" | jq '.snapshot.nodes[] | select(.name == "'$BEST_NODE'") | .resources.ram_free_mb' && go build -v ./... && go test -short ./...`, RequiredTools: []string{"go"}, EstRAMMB: 2048, Description: "full Go build + test on least loaded node", Category: "build", Keywords: []string{"go build", "run tests", "test project", "compile go", "build project"}},
		{Name: "cargo-release", Command: `cargo build --release && echo "built on node with $(cat "$AXIS_CONTEXT_FILE" | jq -r '.ollama["'$BEST_NODE'"].gpu_offload') GPU"`, RequiredTools: []string{"cargo"}, EstRAMMB: 3072, Description: "Rust release build with GPU context", Category: "build", Keywords: []string{"cargo build", "rust build", "release build", "compile rust"}},
	},

	"clean": {
		{Name: "nuke-caches", Command: `cat "$AXIS_CONTEXT_FILE" | jq '.snapshot.summary.total_free_ram_mb' && docker system prune -af && go clean -cache -modcache && rm -rf ~/.cache/* /tmp/go-build* 2>/dev/null && echo "Caches nuked on $(cat "$AXIS_CONTEXT_FILE" | jq -r '.best_node')"`, RequiredTools: []string{"docker", "go"}, EstRAMMB: 256, Description: "aggressive cache purge with live RAM context", Category: "clean", Keywords: []string{"clean caches", "clear caches", "free space", "nuke caches", "cleanup build cache"}},
		{Name: "docker-full-prune", Command: `docker system prune -af && docker volume prune -f && echo "docker clean on node with $(cat "$AXIS_CONTEXT_FILE" | jq '.load["'$BEST_NODE'"]') load"`, RequiredTools: []string{"docker"}, EstRAMMB: 128, Description: "full docker cleanup", Category: "clean", Keywords: []string{"docker prune", "clean docker", "container cleanup", "prune containers"}},
	},

	"ollama": {
		{Name: "ollama-ensure", Command: `ollama serve & sleep 3; ollama list | jq -r '.models[]?.name'`, RequiredTools: []string{"ollama"}, EstRAMMB: 6144, Description: "start server + list models with context", Category: "ai", Keywords: []string{"ensure ollama", "start ollama", "ollama status", "list models", "show ollama models"}},
		{Name: "ollama-run-smart", Command: `MODEL=$(cat "$AXIS_CONTEXT_FILE" | jq -r '.ollama["'$BEST_NODE'"].models[0] // "phi3"'); ollama run $MODEL "$(cat)"`, RequiredTools: []string{"ollama"}, EstRAMMB: 1024, Description: "smart local inference with node context", Category: "ai", Keywords: []string{"ollama inference", "local model", "run model", "small local model", "ask local model", "generate with ollama", "run ollama"}},
	},

	"system": {
		{Name: "ram-hogs", Command: `cat "$AXIS_CONTEXT_FILE" | jq '.snapshot.summary' && ps aux --sort=-%mem | head -10`, RequiredTools: []string{}, EstRAMMB: 64, Description: "top RAM consumers with cluster totals", Category: "system", Keywords: []string{"ram hogs", "memory hogs", "top memory", "memory usage"}},
		{Name: "disk-deep", Command: `df -h && du -sh /* 2>/dev/null | sort -hr | head -10 && echo "Free on best node: $(cat "$AXIS_CONTEXT_FILE" | jq '.snapshot.nodes[] | select(.name == "'$BEST_NODE'") | .resources.disk_free_gb')GB"`, RequiredTools: []string{}, EstRAMMB: 64, Description: "full disk analysis", Category: "system", Keywords: []string{"disk usage", "free disk", "analyze disk", "storage usage"}},
		{Name: "load-watch", Command: `uptime && ps aux --sort=-%cpu | head -8 && cat "$AXIS_CONTEXT_FILE" | jq '.load'`, RequiredTools: []string{}, EstRAMMB: 64, Description: "load + CPU hogs with full context", Category: "system", Keywords: []string{"system load", "cpu load", "cpu hogs", "load watch"}},
	},
}

// GetBestScript now uses description + context keywords
func GetBestScript(desc string) (Script, bool) {
	lower := strings.ToLower(desc)
	keys := make([]string, 0, len(Registry))
	for key := range Registry {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var best Script
	bestScore := 0
	for _, key := range keys {
		for _, s := range Registry[key] {
			score := scriptMatchScore(lower, key, s)
			if score > bestScore {
				bestScore = score
				best = s
			}
		}
	}
	if bestScore == 0 {
		return Script{}, false
	}
	return best, true
}

func scriptMatchScore(desc, key string, s Script) int {
	score := 0

	if strings.Contains(desc, strings.ToLower(s.Name)) {
		score += 100
	}

	normName := strings.ReplaceAll(strings.ToLower(s.Name), "-", " ")
	if strings.Contains(desc, normName) {
		score += 90
	}

	if strings.Contains(desc, strings.ToLower(key)) {
		score += 60
	}

	if strings.Contains(desc, strings.ToLower(s.Category)) {
		score += 25
	}

	for _, kw := range s.Keywords {
		if strings.Contains(desc, strings.ToLower(kw)) {
			score += 80
		}
	}

	for _, tool := range s.RequiredTools {
		if strings.Contains(desc, strings.ToLower(tool)) {
			score += 40
		}
	}

	if strings.Contains(desc, strings.ToLower(s.Description)) {
		score += 30
	}

	return score
}
