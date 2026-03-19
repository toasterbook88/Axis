package knowledge

import (
	"encoding/json"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

type ClusterKnowledge struct {
	Timestamp time.Time                    `json:"timestamp"`
	Snapshot  models.ClusterSnapshot       `json:"snapshot"`
	State     *state.ClusterState          `json:"state"`
	Ollama    map[string]models.OllamaInfo `json:"ollama"`
	Load      map[string]float64           `json:"load"`
	BestNode  string                       `json:"best_node"`
}

func Build(snap *models.ClusterSnapshot, st *state.ClusterState, bestNode string) *ClusterKnowledge {
	ollamaMap := make(map[string]models.OllamaInfo)
	for _, n := range snap.Nodes {
		if n.Ollama != nil {
			ollamaMap[n.Name] = *n.Ollama
		}
	}

	k := &ClusterKnowledge{
		Timestamp: time.Now().UTC(),
		Snapshot:  *snap,
		State:     st,
		Ollama:    ollamaMap,
		Load:      make(map[string]float64),
		BestNode:  bestNode,
	}

	for _, n := range snap.Nodes {
		if n.Resources != nil {
			k.Load[n.Name] = 0 // Fill from /proc/loadavg if you want
		}
	}
	return k
}

func (k *ClusterKnowledge) JSON() string {
	b, _ := json.MarshalIndent(k, "", "  ")
	return string(b)
}
