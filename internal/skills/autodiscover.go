package skills

import (
	"context"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

// AutoDiscoverSkills turns every discovered CLI on a node into a reusable skill template.
func AutoDiscoverSkills(ctx context.Context, nodes []models.NodeFacts) *Store {
	s, err := Load()
	if s == nil {
		s = newStore()
	}
	if err != nil && s == nil {
		return newStore()
	}

	for _, n := range nodes {
		if n.Tools == nil {
			continue
		}
		for _, t := range n.Tools {
			name := strings.ToLower(t.Name)
			if alreadyKnown(s, name) || name == "ollama" {
				continue
			}

			skill := LearnedSkill{
				ID:            "auto-" + name + "-" + time.Now().Format("20060102"),
				Description:   "use " + name + " (auto-discovered on " + n.Name + ")",
				Command:       name + ` $(cat "$AXIS_CONTEXT_FILE" | jq -r '.snapshot.summary')`,
				SuccessCount:  0,
				LastUsed:      time.Now().UTC(),
				PreferredNode: n.Name,
			}

			// Special badass templates
			if name == "gemini" {
				skill.Command = `gemini "$(cat)" --model gemini-2.0-flash`
				skill.Description = "ask Gemini CLI (auto-discovered)"
			}
			if name == "uv" {
				skill.Command = `uv run python -c "print('uv detected and ready')"`
			}

			s.Skills = append(s.Skills, skill)
		}
	}

	_ = s.Save()
	return s
}

func alreadyKnown(s *Store, name string) bool {
	for _, sk := range s.Skills {
		descLower := strings.ToLower(sk.Description)
		if strings.Contains(descLower, "use "+name) || strings.Contains(descLower, name+" (auto-discovered") || strings.Contains(descLower, "ask "+name) {
			return true
		}
	}
	return false
}
