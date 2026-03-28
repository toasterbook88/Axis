package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	corruptStampPattern = regexp.MustCompile(`\.corrupt-\d{8}T\d{6}Z`)
	timePattern         = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z`)
)

func TestContextShowCorruptStateGolden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeCorruptAxisFile(t, home, "state.json", "{")

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := contextCmd()
		cmd.SetArgs([]string{"show"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("context show: %v", err)
	}

	assertQuarantinedAxisFile(t, home, "state.json")
	assertGoldenText(t,
		filepath.Join("testdata", "context_show_corrupt_state.golden"),
		renderGoldenSections(normalizeDegradedOutput(stderr, home), normalizeDegradedOutput(stdout, home)),
	)
}

func TestSkillsCommandCorruptStoreGolden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeCorruptAxisFile(t, home, "skills.json", "{")

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := skillsCmd()
		cmd.SetArgs(nil)
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("skills: %v", err)
	}

	assertQuarantinedAxisFile(t, home, "skills.json")
	assertGoldenText(t,
		filepath.Join("testdata", "skills_corrupt_store.golden"),
		renderGoldenSections(normalizeDegradedOutput(stderr, home), normalizeDegradedOutput(stdout, home)),
	)
}

func captureProcessOutput(t *testing.T, fn func() error) (string, string, error) {
	t.Helper()

	oldStdout := os.Stdout
	oldStderr := os.Stderr

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}

	os.Stdout = stdoutWriter
	os.Stderr = stderrWriter

	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	}()

	stdoutCh := make(chan string, 1)
	stderrCh := make(chan string, 1)

	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(stdoutReader)
		stdoutCh <- buf.String()
	}()
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(stderrReader)
		stderrCh <- buf.String()
	}()

	runErr := fn()

	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	stdout := <-stdoutCh
	stderr := <-stderrCh
	return stdout, stderr, runErr
}

func writeCorruptAxisFile(t *testing.T, home string, name string, content string) {
	t.Helper()
	path := filepath.Join(home, ".axis", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir axis dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
}

func assertQuarantinedAxisFile(t *testing.T, home string, name string) {
	t.Helper()
	originalPath := filepath.Join(home, ".axis", name)
	if _, err := os.Stat(originalPath); !os.IsNotExist(err) {
		t.Fatalf("expected original %s to be quarantined, stat err=%v", name, err)
	}

	matches, err := filepath.Glob(originalPath + ".corrupt-*")
	if err != nil {
		t.Fatalf("glob quarantine files: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one quarantine file for %s, got %v", name, matches)
	}
}

func normalizeDegradedOutput(s string, home string) string {
	s = strings.ReplaceAll(s, home, "$HOME")
	s = corruptStampPattern.ReplaceAllString(s, ".corrupt-<STAMP>")
	s = timePattern.ReplaceAllString(s, "<TIME>")
	return strings.TrimSpace(s)
}

func renderGoldenSections(stderr string, stdout string) string {
	return "STDERR:\n" + stderr + "\n\nSTDOUT:\n" + stdout + "\n"
}

func assertGoldenText(t *testing.T, path string, actual string) {
	t.Helper()
	expectedBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	expected := string(expectedBytes)
	if actual != expected {
		t.Fatalf("golden mismatch for %s\nEXPECTED:\n%s\nACTUAL:\n%s", path, expected, actual)
	}
}
