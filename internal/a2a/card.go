// Package a2a implements a minimal, read-only slice of the Agent-to-Agent
// (A2A) protocol: an Agent Card that advertises this node's identity and
// capabilities to the rest of the mesh, served at
// /.well-known/agent-card.json by the local AXIS daemon.
//
// Scope discipline (see findings/2026-09-25-a2a-agent-card-mesh-proposal.md):
//   - v1 exposes ONLY the card. Task methods (tasks/send, tasks/sendSubscribe)
//     are deliberately not implemented in this slice.
//   - Skills are derived from the schema-mask tool scopes: a card reflects
//     what the local node actually serves, never what a caller asks for.
//   - The card is public-by-design (no secret, no auth) — it advertises
//     metadata only, the same information the mesh gossip already carries,
//     in the standardized well-known location.
package a2a

import (
	"encoding/json"
	"net/http"
)

// AgentCard is the minimal A2A Agent Card served at
// /.well-known/agent-card.json. Field names follow the A2A spec
// (https://a2a-protocol.org/latest/specification/).
type AgentCard struct {
	Name               string       `json:"name"`
	Description        string       `json:"description"`
	Version            string       `json:"version"`
	URL                string       `json:"url"`
	Capabilities       Capabilities `json:"capabilities"`
	DefaultInputModes  []string     `json:"defaultInputModes"`
	DefaultOutputModes []string     `json:"defaultOutputModes"`
	Skills             []Skill      `json:"skills"`
}

// Capabilities mirrors the A2A spec's AgentCapabilities flags.
type Capabilities struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications"`
}

// Skill is one advertised capability unit. Axis maps skills to the node's
// schema-mask tool scopes: one skill per scope tier the node serves.
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// ScopeTier names one schema-mask tier for card advertising. Values match
// agent.ToolScope display names so cards and /tools output agree.
type ScopeTier string

const (
	ScopeObserve ScopeTier = "observe"
	ScopeEdit    ScopeTier = "edit"
	ScopeExec    ScopeTier = "exec"
)

// CardOptions configures card generation for one node.
type CardOptions struct {
	Name    string    // cluster-unique node name (mesh identity)
	Version string    // axis build version (buildinfo.Version)
	URL     string    // base URL the A2A surface is reachable at; omit for Unix-socket listeners (v1 limit: no URL advertised)
	Scope   ScopeTier // v1 is always ScopeObserve (see Card); the field exists so edit/exec wiring needs no signature change
}

// Card builds the AgentCard for a node at the given scope tier. Skills are
// derived from the tier, not from caller input — a node can never advertise
// a tier it is not currently serving.
func Card(o CardOptions) AgentCard {
	skills := skillsForScope(o.Scope)
	desc := "AXIS cluster node — " + string(o.Scope) + " scope"
	return AgentCard{
		Name:               o.Name,
		Description:        desc,
		Version:            o.Version,
		URL:                o.URL,
		Capabilities:       Capabilities{Streaming: false}, // v1: card-only; no task/streaming methods yet
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills:             skills,
	}
}

// skillsForScope derives the advertised skill list from the scope tier.
// Mirrors the schema-mask contract: observe is read-only; edit adds
// workspace writes; exec adds guarded execution (advertised only when the
// node is actually serving that tier).
func skillsForScope(scope ScopeTier) []Skill {
	var out []Skill
	add := func(id, name, desc string) {
		out = append(out, Skill{ID: id, Name: name, Description: desc, Tags: []string{string(scope)}})
	}

	if scope == ScopeObserve || scope == ScopeEdit || scope == ScopeExec {
		add("axis-status", "Cluster status", "Read the current cluster snapshot: node health, resources, pressure.")
		add("axis-facts", "Local facts", "Local hardware and software facts for this node.")
		add("axis-place", "Placement advisory", "Advisory node placement for a task description.")
		add("axis-reservations", "Reservations", "List active reservations and task assignments.")
		add("workspace-read", "Workspace read", "Read files, list directories, and grep within this node's workspace.")
	}
	if scope == ScopeEdit || scope == ScopeExec {
		add("workspace-write", "Workspace write", "Create and edit files in this node's workspace (session checkpoints enable undo).")
	}
	if scope == ScopeExec {
		add("guarded-exec", "Guarded execution", "Run shell commands and cluster tasks behind this node's operator confirmation and safety gates.")
	}
	return out
}

// ServeCard registers the standard well-known route on an HTTP mux. Public
// by design: the card is metadata, the same information the gossip mesh
// already carries, in the standardized location.
func ServeCard(mux *http.ServeMux, cardFn func() AgentCard) {
	mux.HandleFunc("/.well-known/agent-card.json", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cardFn())
	})
}
