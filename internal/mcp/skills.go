package axismcp

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Skill struct {
	Name        string
	Description string
	Body        string
}

var getSkillsDir = func() string {
	// Look up ~/.gemini/skills/
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "skills")
}

func loadSkills(dir string) ([]Skill, error) {
	var skills []Skill
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(dir, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillPath); err != nil {
			continue
		}

		skill, err := parseSkillFile(skillPath, entry.Name())
		if err == nil {
			skills = append(skills, skill)
		}
	}
	return skills, nil
}

type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func parseSkillFile(path string, defaultName string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}

	// Parse YAML frontmatter
	// Frontmatter is enclosed between --- and ---
	content := string(data)
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		// No frontmatter, treat whole file as body
		return Skill{
			Name:        defaultName,
			Description: "Custom skill: " + defaultName,
			Body:        content,
		}, nil
	}

	// Find the closing ---
	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return Skill{
			Name:        defaultName,
			Description: "Custom skill: " + defaultName,
			Body:        content,
		}, nil
	}

	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
		return Skill{
			Name:        defaultName,
			Description: "Custom skill: " + defaultName,
			Body:        content,
		}, nil
	}

	if fm.Name == "" {
		fm.Name = defaultName
	}
	if fm.Description == "" {
		fm.Description = "Custom skill: " + fm.Name
	}

	return Skill{
		Name:        fm.Name,
		Description: fm.Description,
		Body:        strings.TrimSpace(parts[2]),
	}, nil
}
