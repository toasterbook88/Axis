package scripts

import (
	"strings"
	"testing"
)

func TestGetBestScriptMatchesNaturalLanguageOllamaTask(t *testing.T) {
	script, ok := GetBestScript("run a small local model with ollama inference")
	if !ok {
		t.Fatal("expected a script match")
	}
	if script.Name != "ollama-run-smart" {
		t.Fatalf("expected ollama-run-smart, got %q", script.Name)
	}
}

func TestGetBestScriptMatchesNaturalLanguageBuildTask(t *testing.T) {
	script, ok := GetBestScript("build project and run tests")
	if !ok {
		t.Fatal("expected a script match")
	}
	if script.Name != "go-full-build" {
		t.Fatalf("expected go-full-build, got %q", script.Name)
	}
}

func TestGetBestScriptMatchesNaturalLanguageGitTask(t *testing.T) {
	script, ok := GetBestScript("analyze repo changes")
	if !ok {
		t.Fatal("expected a script match")
	}
	if script.Name != "git-deep-analyze" {
		t.Fatalf("expected git-deep-analyze, got %q", script.Name)
	}
}

func TestRegistryRequiresJQWhenScriptUsesIt(t *testing.T) {
	for category, scripts := range Registry {
		for _, script := range scripts {
			if !strings.Contains(script.Command, "jq") {
				continue
			}
			if !containsRequiredTool(script.RequiredTools, "jq") {
				t.Fatalf("%s/%s uses jq but does not require it", category, script.Name)
			}
		}
	}
}

func containsRequiredTool(tools []string, want string) bool {
	for _, tool := range tools {
		if strings.EqualFold(tool, want) {
			return true
		}
	}
	return false
}
