package scripts

import "testing"

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
