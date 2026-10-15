package models

// Runnable reports whether a runtime can serve the weight as found: GGUF,
// safetensors, and Ollama models (and projectors alongside them). Bundles of
// kind unknown (Core AI, Core ML) are inventory only.
func (w DiskWeight) Runnable() bool { return w.Kind != "unknown" }

// RunnableDiskWeights filters weights to the ones a runtime can serve.
func RunnableDiskWeights(ws []DiskWeight) []DiskWeight {
	var out []DiskWeight
	for _, w := range ws {
		if w.Runnable() {
			out = append(out, w)
		}
	}
	return out
}
