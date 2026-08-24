package modellife

import (
	"fmt"
	"path"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// StartPlan is the argv Axis will exec. It does not launch anything.
type StartPlan struct {
	Node    string
	Port    int
	Weights string
	Volume  string
	Argv    []string
}

// PlanStart validates weights sit on a named local volume and that
// llama-server is an observed tool. Port must be explicit and valid.
func PlanStart(node models.NodeFacts, weights string, port int) (StartPlan, error) {
	weights = path.Clean(strings.TrimSpace(weights))
	if port < 1 || port > 65535 {
		return StartPlan{}, fmt.Errorf("port must be between 1 and 65535")
	}
	if weights == "" || weights == "." {
		return StartPlan{}, fmt.Errorf("weights path is required")
	}
	if !hasTool(node, "llama-server") {
		return StartPlan{}, fmt.Errorf("node %s has no observed llama-server tool", node.Name)
	}
	vol, ok := namedLocalVolume(node, weights)
	if !ok {
		return StartPlan{}, fmt.Errorf("weights %s are not on a named local volume", weights)
	}
	bin := toolPath(node, "llama-server")
	if bin == "" {
		bin = "llama-server"
	}
	return StartPlan{
		Node:    node.Name,
		Port:    port,
		Weights: weights,
		Volume:  vol,
		Argv:    []string{bin, "-m", weights, "--port", fmt.Sprintf("%d", port), "--host", "127.0.0.1"},
	}, nil
}

func hasTool(node models.NodeFacts, name string) bool {
	for _, t := range node.Tools {
		if strings.EqualFold(t.Name, name) {
			return true
		}
	}
	return false
}

func toolPath(node models.NodeFacts, name string) string {
	for _, t := range node.Tools {
		if strings.EqualFold(t.Name, name) {
			return t.Path
		}
	}
	return ""
}

func namedLocalVolume(node models.NodeFacts, weights string) (string, bool) {
	if node.Resources == nil {
		return "", false
	}
	best := ""
	for _, v := range node.Resources.Volumes {
		if v.Kind == "network" || v.Mount == "" {
			continue
		}
		mount := path.Clean(v.Mount)
		if weights == mount || strings.HasPrefix(weights, mount+"/") {
			if len(mount) > len(best) {
				best = mount
			}
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}
