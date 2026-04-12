package workload

type workloadSignals struct {
	apple          bool
	llama          bool
	longContext    bool
	localLLM       bool
	repo           bool
	goBuild        bool
	dockerBuild    bool
	indexingIO     bool
	batchScript    bool
	explicitOllama bool
}

func analyzeDescription(desc string) workloadSignals {
	d := newDescriptionView(desc)
	longContext := matchesLongContextHint(d)

	return workloadSignals{
		apple:          matchesAppleIntent(d),
		llama:          matchesLlamaServerIntent(d),
		longContext:    longContext,
		localLLM:       matchesLocalLLMIntent(d, longContext),
		repo:           matchesRepoIntent(d),
		goBuild:        matchesGoBuildIntent(d),
		dockerBuild:    matchesDockerBuildIntent(d),
		indexingIO:     matchesIndexingIntent(d),
		batchScript:    matchesBatchScriptIntent(d),
		explicitOllama: d.has("ollama"),
	}
}

func matchesAppleIntent(d descriptionView) bool {
	return d.hasAny(
		"apple intelligence",
		"apple intelligence via apple foundation models",
		"apple foundation models",
		"apple foundation model",
		"language model session",
	)
}

func matchesLlamaServerIntent(d descriptionView) bool {
	return d.hasAny("llama cpp", "llama cli", "llama server")
}

func matchesLongContextHint(d descriptionView) bool {
	return d.hasAny(
		"128k",
		"256k",
		"512k",
		"1m context",
		"1m tokens",
		"million token",
		"million tokens",
		"book length",
		"needle in a haystack",
		"long context",
	)
}

func matchesLocalLLMIntent(d descriptionView, longContext bool) bool {
	action := d.hasAny("run", "serve", "host", "chat", "generate", "summarize")
	modelSize := d.hasAny("7b", "8b", "13b", "14b", "32b", "34b", "70b")
	modelTarget := d.has("model") || modelSize
	explicitInference := d.has("inference")
	explicitRuntime := d.hasAny(
		"ollama",
		"llm",
		"mlx",
		"llama cpp",
		"llama server",
		"llama cli",
		"apple intelligence",
		"apple foundation models",
	)

	switch {
	case d.hasAny("run ollama", "ollama run", "ollama chat", "local llm", "llm inference"):
		return true
	case action && d.hasAll("local", "model"):
		return true
	case action && modelSize && (modelTarget || d.has("local") || explicitRuntime):
		return true
	case explicitInference && (modelTarget || explicitRuntime || longContext):
		return true
	case action && explicitRuntime && (modelTarget || d.has("local") || longContext):
		return true
	default:
		return false
	}
}

func matchesRepoIntent(d descriptionView) bool {
	switch {
	case d.hasAny("analyze repo", "review codebase", "scan repo", "clone repo", "clone this repo", "repository analysis"):
		return true
	case d.hasAny("repo", "repository", "codebase") && d.hasAny("analyze", "review", "scan", "inspect", "clone"):
		return true
	case d.has("commit") && d.hasAny("repo", "repository", "history"):
		return true
	default:
		return false
	}
}

func matchesGoBuildIntent(d descriptionView) bool {
	switch {
	case d.hasAny("go build", "go test", "run go tests", "compile go", "compile the go binary"):
		return true
	case d.has("go") && d.hasAny("build", "compile", "test"):
		return true
	default:
		return false
	}
}

func matchesDockerBuildIntent(d descriptionView) bool {
	switch {
	case d.hasAny("docker build", "build image", "containerize", "docker container"):
		return true
	case d.has("docker") && d.hasAny("build", "container", "image", "spin up"):
		return true
	default:
		return false
	}
}

func matchesIndexingIntent(d descriptionView) bool {
	switch {
	case d.hasAny("scan filesystem", "vectorize", "embed", "embeddings"):
		return true
	case d.has("index") && d.hasAny("filesystem", "files", "documents", "corpus"):
		return true
	default:
		return false
	}
}

func matchesBatchScriptIntent(d descriptionView) bool {
	switch {
	case d.hasAny("batch script", "batch job", "run python", "data processing", "process data"):
		return true
	case d.has("batch") && d.hasAny("script", "job"):
		return true
	default:
		return false
	}
}
