package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".axis", "skills.json")
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

func (s *Store) Save() error {
	data, _ := json.MarshalIndent(s, "", "  ")
	return persist.WriteFileAtomic(path(), data, 0o644)
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

func newStore() *Store {
	return &Store{
		Skills:   []LearnedSkill{},
		Failures: []LearnedFailure{},
	}
}
