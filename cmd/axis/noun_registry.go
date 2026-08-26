package main

type nounTier string

const (
	nounOperate nounTier = "operate"
	nounInspect nounTier = "inspect"
)

type freshness string

const (
	freshnessLive    freshness = "live"
	freshnessSession freshness = "session"
	freshnessNA      freshness = "n/a"
)

type nounEntry struct {
	Name           string
	Tier           nounTier
	CLIFreshness   freshness
	SlashFreshness freshness
}

func nounRegistry() []nounEntry {
	return []nounEntry{
		{Name: "cluster", Tier: nounOperate, CLIFreshness: freshnessLive, SlashFreshness: freshnessSession},
		{Name: "node", Tier: nounOperate, CLIFreshness: freshnessLive, SlashFreshness: freshnessSession},
		{Name: "model", Tier: nounOperate, CLIFreshness: freshnessNA, SlashFreshness: freshnessNA},
		{Name: "task", Tier: nounOperate, CLIFreshness: freshnessNA, SlashFreshness: freshnessNA},
		{Name: "daemon", Tier: nounOperate, CLIFreshness: freshnessNA, SlashFreshness: freshnessNA},
		{Name: "agent", Tier: nounOperate, CLIFreshness: freshnessNA, SlashFreshness: freshnessNA},
		{Name: "doctor", Tier: nounInspect, CLIFreshness: freshnessLive, SlashFreshness: freshnessNA},
		{Name: "mesh", Tier: nounInspect, CLIFreshness: freshnessLive, SlashFreshness: freshnessNA},
		{Name: "ai", Tier: nounInspect, CLIFreshness: freshnessLive, SlashFreshness: freshnessNA},
		{Name: "init", Tier: nounInspect, CLIFreshness: freshnessNA, SlashFreshness: freshnessNA},
		{Name: "serve", Tier: nounInspect, CLIFreshness: freshnessNA, SlashFreshness: freshnessNA},
		{Name: "update", Tier: nounInspect, CLIFreshness: freshnessNA, SlashFreshness: freshnessNA},
		{Name: "version", Tier: nounInspect, CLIFreshness: freshnessNA, SlashFreshness: freshnessNA},
	}
}

func nounByName(reg []nounEntry, name string) nounEntry {
	for _, n := range reg {
		if n.Name == name {
			return n
		}
	}
	return nounEntry{}
}

// sessionOnlySlashes are REPL control verbs, not operator nouns.
func sessionOnlySlashes() []string {
	return []string{
		"/help", "/clear", "/context", "/history", "/tools",
		"/model", "/models", "/mcp", "/reservations", "/skills",
		"/plan", "/todo", "/diff", "/undo", "/compact", "/autonomy", "/export", "/fleet",
		"/exit", "/quit",
	}
}

// slashGaps is operate nouns without a slash handler. Shrinks to empty. Do not grow.
func slashGaps() []string {
	return []string{"model", "task", "daemon"}
}
