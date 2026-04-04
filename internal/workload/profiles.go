package workload

import "github.com/toasterbook88/axis/internal/models"

// Profile defines the hardware and tool requirements for a class of work.
type Profile struct {
	Class             models.WorkloadClass
	Keywords          []string
	RequiredTools     []string
	MinFreeRAMMB      int64
	PrefersTurboQuant bool
	PreferredBackends []string
}

// DefaultRegistry returns the canonical set of workload profiles.
func DefaultRegistry() []Profile {
	return []Profile{
		{
			Class:    models.ClassAppleIntelligence,
			Keywords: []string{"apple-intelligence", "apple intelligence", "apple foundation models", "apple-foundation-models", "language model session"},
			RequiredTools: []string{"apple-foundation-models"},
			MinFreeRAMMB:  -1, // Sentinel to force 0 and block aggregation
		},
		{
			Class:    models.ClassLlamaServer,
			Keywords: []string{"llama.cpp", "llama-cli", "llama server", "llama-server"},
			RequiredTools: []string{"llama-server"},
			MinFreeRAMMB:  6144,
		},
		{
			Class:    models.ClassLongContextInference,
			Keywords: []string{"128k", "256k", "512k", "1m tokens", "long context", "million token", "book length", "needle-in-a-haystack", "needle in a haystack"},
			MinFreeRAMMB:  6144,
			PrefersTurboQuant: true,
		},
		{
			Class:    models.ClassLocalLLMInference,
			Keywords: []string{"70b"},
			RequiredTools: []string{"ollama"},
			MinFreeRAMMB:  12288,
		},
		{
			Class:    models.ClassLocalLLMInference,
			Keywords: []string{"13b", "heavy"},
			RequiredTools: []string{"ollama"},
			MinFreeRAMMB:  8192,
		},
		{
			Class:    models.ClassLocalLLMInference,
			Keywords: []string{"run ollama", "inference", "ollama run", "ollama", "llm", "gpu"},
			RequiredTools: []string{"ollama"},
			MinFreeRAMMB:  6144,
		},
		{
			Class:    models.ClassLocalLLMInference,
			Keywords: []string{"7b", "model"},
			MinFreeRAMMB:  4096,
		},
		{
			Class:    models.ClassRepoAnalysis,
			Keywords: []string{"analyze repo", "review codebase", "scan repo", "code analysis", "clone", "repo", "code", "commit"},
			RequiredTools: []string{"git"},
		},
		{
			Class:    models.ClassGoBuild,
			Keywords: []string{"go build", "run go tests", "compile go", "go test", "build", "compile"},
			RequiredTools: []string{"go", "git"},
		},
		{
			Class:    models.ClassDockerBuild,
			Keywords: []string{"docker build", "build image", "containerize", "docker", "container"},
			RequiredTools: []string{"docker"},
		},
		{
			Class:    models.ClassIndexingIO,
			Keywords: []string{"index", "embed", "vectorize", "scan filesystem"},
			MinFreeRAMMB: 2048,
		},
		{
			Class:    models.ClassBatchScript,
			Keywords: []string{"batch", "script", "run python", "data processing"},
		},
	}
}
