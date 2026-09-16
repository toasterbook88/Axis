package axismcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	mcpproto "github.com/mark3labs/mcp-go/mcp"
)

func TestParseSkillFile(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name        string
		defaultName string
		content     string
		wantName    string
		wantDesc    string
		wantBody    string
	}{
		{
			name:        "with frontmatter",
			defaultName: "def-name",
			content: `---
name: override-name
description: custom description
---
The actual body content.`,
			wantName: "override-name",
			wantDesc: "custom description",
			wantBody: "The actual body content.",
		},
		{
			name:        "without frontmatter",
			defaultName: "def-name",
			content:     "Whole file is the body.",
			wantName:    "def-name",
			wantDesc:    "Custom skill: def-name",
			wantBody:    "Whole file is the body.",
		},
		{
			name:        "invalid YAML frontmatter",
			defaultName: "def-name",
			content: `---
name: [invalid yaml
---
Body content here.`,
			wantName: "def-name",
			wantDesc: "Custom skill: def-name",
			wantBody: `---
name: [invalid yaml
---
Body content here.`,
		},
		{
			name:        "empty name frontmatter",
			defaultName: "def-name",
			content: `---
description: custom description
---
Body content here.`,
			wantName: "def-name",
			wantDesc: "custom description",
			wantBody: "Body content here.",
		},
		{
			name:        "unclosed frontmatter",
			defaultName: "def-name",
			content: `---
name: override-name
Body content here.`,
			wantName: "def-name",
			wantDesc: "Custom skill: def-name",
			wantBody: `---
name: override-name
Body content here.`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(tmpDir, tc.name+".md")
			if err := os.WriteFile(path, []byte(tc.content), 0644); err != nil {
				t.Fatal(err)
			}

			skill, err := parseSkillFile(path, tc.defaultName)
			if err != nil {
				t.Fatalf("parseSkillFile: %v", err)
			}

			if skill.Name != tc.wantName {
				t.Errorf("got name %q, want %q", skill.Name, tc.wantName)
			}
			if skill.Description != tc.wantDesc {
				t.Errorf("got description %q, want %q", skill.Description, tc.wantDesc)
			}
			if skill.Body != tc.wantBody {
				t.Errorf("got body %q, want %q", skill.Body, tc.wantBody)
			}
		})
	}
}

func TestServerPrompts(t *testing.T) {
	tmpDir := t.TempDir()

	// Skill 1: with frontmatter
	skill1Dir := filepath.Join(tmpDir, "skill1")
	if err := os.MkdirAll(skill1Dir, 0755); err != nil {
		t.Fatal(err)
	}
	skill1Content := `---
name: test-skill-1
description: "This is a test description"
---
This is the test prompt body for skill 1.`
	if err := os.WriteFile(filepath.Join(skill1Dir, "SKILL.md"), []byte(skill1Content), 0644); err != nil {
		t.Fatal(err)
	}

	// Skill 2: without frontmatter
	skill2Dir := filepath.Join(tmpDir, "skill2")
	if err := os.MkdirAll(skill2Dir, 0755); err != nil {
		t.Fatal(err)
	}
	skill2Content := `This is the body of skill 2 without frontmatter.`
	if err := os.WriteFile(filepath.Join(skill2Dir, "SKILL.md"), []byte(skill2Content), 0644); err != nil {
		t.Fatal(err)
	}

	// Stub getSkillsDir
	origGetSkillsDir := getSkillsDir
	getSkillsDir = func() string {
		return tmpDir
	}
	defer func() { getSkillsDir = origGetSkillsDir }()

	// Create MCP server
	s := NewServer(false, "", nil)
	if s == nil {
		t.Fatal("expected non-nil server")
	}

	// List prompts
	prompts := s.ListPrompts()
	if len(prompts) != 2 {
		t.Fatalf("expected 2 prompts, got %d", len(prompts))
	}

	// Verify details
	p1, ok := prompts["test-skill-1"]
	if !ok {
		t.Fatal("expected test-skill-1 prompt to be registered")
	}
	if p1.Prompt.Name != "test-skill-1" || p1.Prompt.Description != "This is a test description" {
		t.Fatalf("unexpected p1: %#v", p1.Prompt)
	}

	p2, ok := prompts["skill2"]
	if !ok {
		t.Fatal("expected skill2 prompt to be registered")
	}
	if p2.Prompt.Name != "skill2" || p2.Prompt.Description != "Custom skill: skill2" {
		t.Fatalf("unexpected p2: %#v", p2.Prompt)
	}

	// Execute p1 handler
	res, err := p1.Handler(context.Background(), mcpproto.GetPromptRequest{
		Params: mcpproto.GetPromptParams{
			Name: "test-skill-1",
		},
	})
	if err != nil {
		t.Fatalf("p1.Handler: %v", err)
	}

	if len(res.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(res.Messages))
	}
	txt, ok := mcpproto.AsTextContent(res.Messages[0].Content)
	if !ok {
		t.Fatalf("expected text content, got %#v", res.Messages[0].Content)
	}
	if txt.Text != "This is the test prompt body for skill 1." {
		t.Fatalf("expected prompt 1 body content, got %q", txt.Text)
	}

	// Execute p2 handler
	res2, err := p2.Handler(context.Background(), mcpproto.GetPromptRequest{
		Params: mcpproto.GetPromptParams{
			Name: "skill2",
		},
	})
	if err != nil {
		t.Fatalf("p2.Handler: %v", err)
	}
	if len(res2.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(res2.Messages))
	}
	txt2, ok := mcpproto.AsTextContent(res2.Messages[0].Content)
	if !ok {
		t.Fatalf("expected text content, got %#v", res2.Messages[0].Content)
	}
	if txt2.Text != "This is the body of skill 2 without frontmatter." {
		t.Fatalf("expected prompt 2 body content, got %q", txt2.Text)
	}
}
