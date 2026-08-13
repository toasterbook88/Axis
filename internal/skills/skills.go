package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/persist"
)

type LearnedSkill struct {
	ID            string         `json:"id"`
	Description   string         `json:"description"`
	Command       string         `json:"command"`
	SuccessCount  int            `json:"success_count"`
	LastUsed      time.Time      `json:"last_used"`
	PreferredNode string         `json:"preferred_node,omitempty"`
	NodeCount     map[string]int `json:"node_count"` // tracks which nodes worked best
}

type LearnedFailure struct {
	Description string    `json:"description"`
	Reason      string    `json:"reason"`
	Time        time.Time `json:"time"`
}

type Store struct {
	Skills   []LearnedSkill   `json:"skills"`
	Failures []LearnedFailure `json:"failures"`
}

var quarantineCorruptSkillsFile = persist.QuarantineCorruptFile

func Path() string {
	return persist.AxisPath("skills.json")
}

func path() string {
	return Path()
}

func Load() (*Store, error) {
	filePath := path()
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return newStore(), nil
		}
		return nil, err
	}
	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		warnErr := quarantineCorruptSkillsFile(filePath, err)
		if _, ok := warnErr.(*persist.RecoveryWarning); ok {
			return newStore(), fmt.Errorf("recovered learned skills store: %w", warnErr)
		}
		return nil, warnErr
	}
	if s.Skills == nil {
		s.Skills = []LearnedSkill{}
	}
	if s.Failures == nil {
		s.Failures = []LearnedFailure{}
	}
	return &s, nil
}

// LoadUnlocked reads the skills store without acquiring the lock. Callers that
// already hold it via persist.LockFile MUST use this rather than Load.
//
// skills.Load is already lock-free and runs no migrations, so this is a
// documented alias; it exists so call sites state which discipline they
// are under.
func LoadUnlocked() (*Store, error) { return Load() }

// Update serializes a read-modify-write transaction against the latest skills
// store on disk. An unlocked RecordSuccess/Save from a concurrent execution
// (internal/execution/guarded.go) would otherwise be lost.
//
// Never call Update while holding a store lock; see persist.LockFile.
func Update(mutator func(*Store) error) error {
	if mutator == nil {
		return fmt.Errorf("skills update requires a mutator")
	}

	release, err := persist.LockFile(path())
	if err != nil {
		return err
	}
	defer release()

	latest, err := LoadUnlocked()
	if err != nil && latest == nil {
		return err
	}
	if err := mutator(latest); err != nil {
		return err
	}
	return latest.Save()
}

func (s *Store) Save() error {
	data, _ := json.MarshalIndent(s, "", "  ")
	return persist.WritePrivateFileAtomic(path(), data)
}

// RecordSuccess learns from real usage
func (s *Store) RecordSuccess(desc, command, node string) {
	for i := range s.Skills {
		if strings.EqualFold(s.Skills[i].Description, desc) {
			s.Skills[i].SuccessCount++
			s.Skills[i].LastUsed = time.Now().UTC()
			if s.Skills[i].NodeCount == nil {
				s.Skills[i].NodeCount = make(map[string]int)
			}
			s.Skills[i].NodeCount[node]++

			// Derive preferred node dynamically
			maxCount := 0
			for _, c := range s.Skills[i].NodeCount {
				if c > maxCount {
					maxCount = c
				}
			}
			var candidates []string
			for n, c := range s.Skills[i].NodeCount {
				if c == maxCount {
					candidates = append(candidates, n)
				}
			}
			sort.Strings(candidates)

			foundCurrent := false
			for _, cand := range candidates {
				if cand == s.Skills[i].PreferredNode {
					foundCurrent = true
					break
				}
			}
			if foundCurrent && s.Skills[i].PreferredNode != "" {
				// preserve
			} else if len(candidates) > 0 {
				s.Skills[i].PreferredNode = candidates[0]
			}
			return
		}
	}
	// new skill learned
	s.Skills = append(s.Skills, LearnedSkill{
		ID:            time.Now().Format("20060102-150405"),
		Description:   desc,
		Command:       command,
		SuccessCount:  1,
		LastUsed:      time.Now().UTC(),
		PreferredNode: node,
		NodeCount:     map[string]int{node: 1},
	})
}

// BestMatch returns the most successful learned skill for this description
func (s *Store) BestMatch(desc string) (LearnedSkill, bool) {
	lower := strings.ToLower(desc)
	var best LearnedSkill
	var bestScore float64

	for _, skill := range s.Skills {
		if !strings.Contains(lower, strings.ToLower(skill.Description)) && !strings.Contains(strings.ToLower(skill.Description), lower) {
			continue // MUST MATCH keywords
		}
		score := float64(skill.SuccessCount) * 10 // success weight
		score += 50

		if score > bestScore {
			bestScore = score
			best = skill
		}
	}
	return best, bestScore > 0
}

// RecordFailure notes bad actions to prevent repeats
func (s *Store) RecordFailure(desc, reason string) {
	s.Failures = append(s.Failures, LearnedFailure{
		Description: desc,
		Reason:      reason,
		Time:        time.Now().UTC(),
	})
}

// IsKnownBad checks if this exact failure is known
func (s *Store) IsKnownBad(desc string) bool {
	lower := strings.ToLower(desc)
	for _, f := range s.Failures {
		if strings.EqualFold(f.Description, desc) || strings.EqualFold(f.Description, lower) {
			return true
		}
	}
	return false
}

// DeletedSkill identifies a learned skill removed in its entirety.
//
// Identity is captured here, at the moment of deletion, because it cannot be
// recovered afterwards: skill IDs are NOT unique. RecordSuccess derives them
// from time.Now().Format("20060102-150405") — one-second resolution — and
// AutoDiscoverSkills from a date alone. Reloading the store and diffing by ID
// would mis-attribute deletions whenever two skills collide.
type DeletedSkill struct {
	ID          string
	Description string
	Auto        bool // ID carries the auto-discovery prefix; reporting only
	SuccessLost int  // evidence this skill contributed to SuccessCount
}

// SkillPruneReport records what PruneNodes removed.
//
// Field semantics — all counts include references removed as part of a whole
// skill deletion, so the totals describe everything the prune destroyed:
//
//	NodeCounts     NodeCount map entries removed, across all skills
//	PreferredNodes PreferredNode references removed, whether reassigned to a
//	               surviving node or destroyed with the skill
//	SuccessCount   success evidence subtracted, including from deleted skills
//	Deleted        skills removed entirely, with identity captured in place
type SkillPruneReport struct {
	NodeCounts     int
	PreferredNodes int
	SuccessCount   int
	Deleted        []DeletedSkill
}

// SkillsDeleted returns the number of skills removed entirely.
func (r SkillPruneReport) SkillsDeleted() int { return len(r.Deleted) }

// AutoTemplatesDeleted returns how many deleted skills were auto-discovered
// templates. Reporting only — never a pruning decision.
func (r SkillPruneReport) AutoTemplatesDeleted() int {
	n := 0
	for _, d := range r.Deleted {
		if d.Auto {
			n++
		}
	}
	return n
}

// Empty reports whether the prune removed nothing.
func (r SkillPruneReport) Empty() bool {
	return r.NodeCounts == 0 && r.PreferredNodes == 0 &&
		r.SuccessCount == 0 && len(r.Deleted) == 0
}

// PruneNodes removes the named target nodes' evidence from every learned
// skill. RecordSuccess increments SuccessCount and NodeCount[node] together,
// so a node's contribution to SuccessCount is exactly its NodeCount value and
// can be subtracted precisely.
//
// Deletion requires that this prune actually TOUCHED the skill — removed a
// NodeCount entry or a PreferredNode reference — and that nothing survives
// afterwards. Zero evidence alone is not sufficient: AutoDiscoverSkills
// creates templates with SuccessCount 0 and a nil NodeCount, and those must
// survive a prune aimed at an unrelated node.
//
// It does not save; callers persist via Update.
func (s *Store) PruneNodes(targets map[string]bool) SkillPruneReport {
	var rep SkillPruneReport
	if len(targets) == 0 {
		return rep
	}

	norm := make(map[string]bool, len(targets))
	for name := range targets {
		norm[strings.ToLower(strings.TrimSpace(name))] = true
	}
	hit := func(name string) bool {
		return name != "" && norm[strings.ToLower(strings.TrimSpace(name))]
	}

	// sk is a copy, taken by value so a deleted skill is dropped by not
	// appending. sk.NodeCount is a map, so the delete calls below mutate the
	// shared backing map — intended, since the skill is either kept (with the
	// map correctly pruned) or discarded.
	kept := s.Skills[:0]
	for i := range s.Skills {
		sk := s.Skills[i]
		touched := false
		lost := 0

		for node, count := range sk.NodeCount {
			if hit(node) {
				delete(sk.NodeCount, node)
				rep.NodeCounts++
				sk.SuccessCount -= count
				rep.SuccessCount += count
				lost += count
				touched = true
			}
		}
		// A store edited by hand or migrated may carry SuccessCount in
		// excess of sum(NodeCount). Never drive it negative.
		if sk.SuccessCount < 0 {
			sk.SuccessCount = 0
		}

		preferredTargeted := hit(sk.PreferredNode)
		if preferredTargeted {
			// Counted whether the reference is reassigned below or
			// destroyed with the skill: both remove it.
			rep.PreferredNodes++
			touched = true
		}

		// Untouched skills are left exactly as they were — including
		// auto-discovered templates for nodes that were not targeted.
		if !touched {
			kept = append(kept, sk)
			continue
		}

		if sk.SuccessCount == 0 && len(sk.NodeCount) == 0 {
			// Every reference this skill had was to a pruned node.
			// Capture identity here — it is unrecoverable afterwards.
			// The "auto-" prefix is used ONLY for reporting, never for the
			// deletion decision: a template whose ID convention changed is
			// still pruned correctly by the touched/surviving test.
			rep.Deleted = append(rep.Deleted, DeletedSkill{
				ID:          sk.ID,
				Description: sk.Description,
				Auto:        strings.HasPrefix(sk.ID, "auto-"),
				SuccessLost: lost,
			})
			continue
		}

		if preferredTargeted {
			// Fall back to the surviving node with the highest count,
			// breaking ties by name for determinism.
			names := make([]string, 0, len(sk.NodeCount))
			for node := range sk.NodeCount {
				names = append(names, node)
			}
			sort.Strings(names)
			best, bestCount := "", -1
			for _, node := range names {
				if sk.NodeCount[node] > bestCount {
					best, bestCount = node, sk.NodeCount[node]
				}
			}
			sk.PreferredNode = best
		}

		kept = append(kept, sk)
	}
	s.Skills = kept

	return rep
}

func newStore() *Store {
	return &Store{
		Skills:   []LearnedSkill{},
		Failures: []LearnedFailure{},
	}
}
