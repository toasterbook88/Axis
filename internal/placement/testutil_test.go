package placement

import (
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

func names(nodes []models.NodeFacts) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Name)
	}
	return out
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
