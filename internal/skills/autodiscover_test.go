package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestAutoDiscoverSkillsAddsTemplatesAndSkipsKnownTools(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	store := newStore()
	store.Skills = append(store.Skills, LearnedSkill{Description: "use git (auto-discovered on node-a)"})
	if err := store.Save(); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	got := AutoDiscoverSkills(context.Background(), []models.NodeFacts{
		{
			Name: "node-a",
			Tools: []models.ToolInfo{
				{Name: "git"},
				{Name: "uv"},
				{Name: "gemini"},
				{Name: "ollama"},
			},
		},
	})

	if got == nil {
		t.Fatal("expected store")
	}
	if len(got.Skills) != 3 {
		t.Fatalf("expected 3 skills (seed + uv + gemini), got %+v", got.Skills)
	}

	var descriptions []string
	for _, skill := range got.Skills {
		descLower := strings.ToLower(skill.Description)
		descriptions = append(descriptions, descLower)
		switch {
		case strings.Contains(descLower, "gemini"):
			if !strings.Contains(skill.Command, "gemini") {
				t.Fatalf("expected gemini command, got %q", skill.Command)
			}
		case strings.Contains(descLower, "use uv"):
			if !strings.Contains(skill.Command, "uv run python") {
				t.Fatalf("expected uv template, got %q", skill.Command)
			}
		case strings.Contains(descLower, "ollama"):
			t.Fatalf("did not expect ollama auto-discovery, got %+v", skill)
		}
	}
	joined := strings.Join(descriptions, "\n")
	if !strings.Contains(joined, "use uv") || !strings.Contains(joined, "gemini") {
		t.Fatalf("expected uv and gemini skills, got %+v", got.Skills)
	}
}

func TestAlreadyKnownRecognizesAutoDiscoveredPatterns(t *testing.T) {
	store := &Store{
		Skills: []LearnedSkill{
			{Description: "use git (auto-discovered on node-a)"},
			{Description: "ask gemini cli (auto-discovered)"},
		},
	}

	if !alreadyKnown(store, "git") {
		t.Fatal("expected git to be recognized as known")
	}
	if !alreadyKnown(store, "gemini") {
		t.Fatal("expected gemini to be recognized as known")
	}
	if alreadyKnown(store, "docker") {
		t.Fatal("did not expect docker to be known")
	}
}
