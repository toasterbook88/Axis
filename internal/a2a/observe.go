// Copyright (c) 2026 Smith Software Solutions
package a2a

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ObserveData is the thin read facade used by observe-tier skills.
// Implementations must NOT invoke shell / RunGuarded (F5).
type ObserveData interface {
	// StatusJSON returns cluster status / snapshot summary bytes.
	StatusJSON() ([]byte, error)
	// FactsJSON returns local node facts bytes.
	FactsJSON() ([]byte, error)
	// PlaceJSON returns a placement advisory for the given description.
	PlaceJSON(description string) ([]byte, error)
	// ReservationsJSON returns active reservations bytes.
	ReservationsJSON() ([]byte, error)
	// WorkspaceReadJSON returns a constrained workspace listing / note.
	WorkspaceReadJSON(query string) ([]byte, error)
}

// AllowedSkillIDs returns the live allow-list for a scope tier (gate B / F4).
func AllowedSkillIDs(scope ScopeTier) map[string]bool {
	out := make(map[string]bool)
	for _, s := range skillsForScope(scope) {
		out[s.ID] = true
	}
	return out
}

// IsExecShaped reports skills that must never run on an observe-only wire-up.
// Slice 2v1 rejects these fail-closed even when a client asks (F2 / F4).
func IsExecShaped(skillID string) bool {
	switch strings.TrimSpace(skillID) {
	case "guarded-exec", "workspace-write":
		return true
	default:
		return false
	}
}

// RunObserveSkill maps an observe skill id to a read helper and returns
// artifact text. Unknown / over-tier callers must be rejected before this.
func RunObserveSkill(data ObserveData, skillID, messageText string) (artifactName, artifactText string, err error) {
	if data == nil {
		data = nilObserve{}
	}
	var raw []byte
	switch skillID {
	case "axis-status":
		raw, err = data.StatusJSON()
		artifactName = "axis-status"
	case "axis-facts":
		raw, err = data.FactsJSON()
		artifactName = "axis-facts"
	case "axis-place":
		raw, err = data.PlaceJSON(messageText)
		artifactName = "axis-place"
	case "axis-reservations":
		raw, err = data.ReservationsJSON()
		artifactName = "axis-reservations"
	case "workspace-read":
		raw, err = data.WorkspaceReadJSON(messageText)
		artifactName = "workspace-read"
	default:
		return "", "", fmt.Errorf("skill %q is not an observe read mapping", skillID)
	}
	if err != nil {
		return artifactName, "", err
	}
	// Stamp a stable canary so E2E can prove the observe path ran.
	wrapped := map[string]any{
		"skill":  skillID,
		"canary": "a2a-slice2-observe",
		"data":   json.RawMessage(raw),
	}
	out, mErr := json.Marshal(wrapped)
	if mErr != nil {
		return artifactName, string(raw), nil
	}
	return artifactName, string(out), nil
}

// nilObserve returns structured "unavailable" payloads when no cache is wired.
type nilObserve struct{}

func (nilObserve) StatusJSON() ([]byte, error) {
	return []byte(`{"ok":true,"note":"snapshot cache unavailable","surface":"a2a-task"}`), nil
}
func (nilObserve) FactsJSON() ([]byte, error) {
	return []byte(`{"ok":true,"note":"local facts unavailable without snapshot","surface":"a2a-task"}`), nil
}
func (nilObserve) PlaceJSON(description string) ([]byte, error) {
	return []byte(fmt.Sprintf(`{"ok":true,"note":"placement advisory unavailable without snapshot","description":%q,"surface":"a2a-task"}`, description)), nil
}
func (nilObserve) ReservationsJSON() ([]byte, error) {
	return []byte(`{"ok":true,"reservations":[],"note":"ledger unavailable","surface":"a2a-task"}`), nil
}
func (nilObserve) WorkspaceReadJSON(query string) ([]byte, error) {
	return []byte(fmt.Sprintf(`{"ok":true,"note":"workspace-read observe facade (no shell)","query":%q,"surface":"a2a-task"}`, query)), nil
}
