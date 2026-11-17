package capability

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Policy holds the thresholds one capability must meet to be promoted.
// Ceilings are not policy: the operator sets them with `axis capability ceiling`.
type Policy struct {
	ID          string `yaml:"id" json:"id"`
	Rung        int    `yaml:"rung,omitempty" json:"rung,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// PromoteAfter is the run of consecutive counted passes a promotion needs.
	PromoteAfter int `yaml:"promote_after,omitempty" json:"promote_after"`
	// Window is how many recent counted outcomes MinRate is measured over.
	Window int `yaml:"window,omitempty" json:"window"`
	// MinRate is the least share of passes in the window a promotion needs.
	MinRate float64 `yaml:"min_rate,omitempty" json:"min_rate"`
	// MinSpan is the least time a promoting streak must cover, as a Go
	// duration ("6h"), so a level cannot be earned in one burst of checks.
	MinSpan string `yaml:"min_span,omitempty" json:"min_span,omitempty"`
}

// Thresholds are the numbers the state machine judges a promotion by. Each
// journal entry records the ones in force, so the journal replays exactly.
type Thresholds struct {
	PromoteAfter   int     `json:"promote_after"`
	Window         int     `json:"window"`
	MinRate        float64 `json:"min_rate"`
	MinSpanMinutes int     `json:"min_span_minutes"`
}

// MinSpan is the least time a promoting streak must cover.
func (t Thresholds) MinSpan() time.Duration { return time.Duration(t.MinSpanMinutes) * time.Minute }

// Defaults for a capability the policy file does not mention.
const (
	DefaultPromoteAfter = 20
	DefaultWindow       = 50
	DefaultMinRate      = 0.98
	DefaultMinSpan      = "6h"
)

// Floors: the least any policy may ask for. A policy file below them is
// refused, and the state machine raises any thresholds it is given to meet
// them, so a one-line policy cannot make a level cheap.
const (
	MinPromoteAfter = 10
	MinRateFloor    = 0.9
	MaxWindow       = 1000
	MinSpanFloor    = time.Hour
	MaxSpan         = 30 * 24 * time.Hour
)

// floored raises t to the floors.
func (t Thresholds) floored() Thresholds {
	if t.PromoteAfter < MinPromoteAfter {
		t.PromoteAfter = MinPromoteAfter
	}
	if !(t.MinRate >= MinRateFloor) { // also catches NaN
		t.MinRate = MinRateFloor
	}
	if t.MinRate > 1 {
		t.MinRate = 1
	}
	if t.Window > MaxWindow {
		t.Window = MaxWindow
	}
	if t.Window < t.PromoteAfter {
		t.Window = t.PromoteAfter
	}
	if floor := int(MinSpanFloor / time.Minute); t.MinSpanMinutes < floor {
		t.MinSpanMinutes = floor
	}
	return t
}

// DefaultPolicy is the policy for a capability with no entry of its own.
func DefaultPolicy(id string) Policy {
	return Policy{ID: id, PromoteAfter: DefaultPromoteAfter, Window: DefaultWindow, MinRate: DefaultMinRate, MinSpan: DefaultMinSpan}
}

// Thresholds returns the policy's thresholds, raised to the floors. A span
// that does not parse counts as the floor.
func (p Policy) Thresholds() Thresholds {
	span, _ := time.ParseDuration(p.MinSpan)
	return Thresholds{PromoteAfter: p.PromoteAfter, Window: p.Window, MinRate: p.MinRate,
		MinSpanMinutes: int(span / time.Minute)}.floored()
}

// Resolved fills unset thresholds with the defaults and refuses any below the floors.
func (p Policy) Resolved() (Policy, error) {
	if p.PromoteAfter == 0 {
		p.PromoteAfter = DefaultPromoteAfter
	}
	if p.Window == 0 {
		p.Window = DefaultWindow
	}
	if p.MinRate == 0 {
		p.MinRate = DefaultMinRate
	}
	if p.MinSpan == "" {
		p.MinSpan = DefaultMinSpan
	}
	if span, err := time.ParseDuration(p.MinSpan); err != nil || span < MinSpanFloor || span > MaxSpan {
		return p, fmt.Errorf("capability %q: min_span is %q; it must be a duration between %s and %s", p.ID, p.MinSpan, MinSpanFloor, MaxSpan)
	}
	if p.PromoteAfter < MinPromoteAfter {
		return p, fmt.Errorf("capability %q: promote_after is %d; the floor is %d", p.ID, p.PromoteAfter, MinPromoteAfter)
	}
	if !(p.MinRate >= MinRateFloor) || p.MinRate > 1 {
		return p, fmt.Errorf("capability %q: min_rate is %g; it must be between %g and 1", p.ID, p.MinRate, MinRateFloor)
	}
	if p.Window < p.PromoteAfter || p.Window > MaxWindow {
		return p, fmt.Errorf("capability %q: window is %d; it must be between promote_after (%d) and %d", p.ID, p.Window, p.PromoteAfter, MaxWindow)
	}
	return p, nil
}

// Policies maps a capability ID to its policy.
type Policies map[string]Policy

// For returns the resolved policy for id. An entry that does not resolve (one
// built in code below the floors, say) falls back to the default thresholds.
func (ps Policies) For(id string) Policy {
	def := DefaultPolicy(id)
	p, ok := ps[id]
	if !ok {
		return def
	}
	if r, err := p.Resolved(); err == nil {
		return r
	}
	def.Rung, def.Description = p.Rung, p.Description
	return def
}

type policyFile struct {
	Capabilities []Policy `yaml:"capabilities"`
}

// LoadPolicies reads the operator's policy file. A missing file is not an
// error: every capability then uses the defaults.
func LoadPolicies(path string) (Policies, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Policies{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading capability policy: %w", err)
	}
	var file policyFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parsing capability policy %s: %w", path, err)
	}
	out := Policies{}
	for _, p := range file.Capabilities {
		if err := ValidateCapabilityID(p.ID); err != nil {
			return nil, fmt.Errorf("capability policy %s: %w", path, err)
		}
		if _, dup := out[p.ID]; dup {
			return nil, fmt.Errorf("capability policy %s: %q is listed twice", path, p.ID)
		}
		resolved, err := p.Resolved()
		if err != nil {
			return nil, fmt.Errorf("capability policy %s: %w", path, err)
		}
		out[p.ID] = resolved
	}
	return out, nil
}
