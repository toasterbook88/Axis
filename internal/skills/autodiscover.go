package skills

import (
	"context"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

// AutoDiscoverSkills turns every discovered CLI on a node into a reusable skill template.
//
// The append happens inside Update, so discovery serializes against the other
// skills writers instead of racing them with an unlocked Save.
func AutoDiscoverSkills(ctx context.Context, nodes []models.NodeFacts) *Store {
	result := newStore()
	if err := Update(func(s *Store) error {
		discoverInto(s, nodes)
		result = s
		return nil
	}); err != nil {
		// Persisting failed. Still return what discovery would have produced,
		// so callers get a usable in-memory store as they did before.
		result = newStore()
		discoverInto(result, nodes)
	}
	return result
}

func discoverInto(s *Store, nodes []models.NodeFacts) {
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
