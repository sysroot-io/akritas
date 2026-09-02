package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	DefaultDirectory        = "skills"
	MaximumSkills           = 128
	MaximumSelectedSkills   = 8
	MaximumSkillBytes       = 64 * 1024
	MaximumCatalogBytes     = 512 * 1024
	MaximumDescriptionBytes = 512
	maximumSkillNameLength  = 64
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type Skill struct {
	Name        string
	Description string
	Path        string
	Selectors   []string
	Content     string
}

type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type Catalog struct {
	skills []Skill
}

func Load(root string) (*Catalog, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("skills directory is empty")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve skills directory %q: %w", root, err)
	}
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return nil, fmt.Errorf("read skills directory %q: %w", root, err)
	}
	catalog := &Catalog{}
	totalBytes := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("skill directory %q must not be a symlink", entry.Name())
		}
		if len(catalog.skills) == MaximumSkills {
			return nil, fmt.Errorf("skills catalog exceeds %d skills", MaximumSkills)
		}
		path := filepath.Join(absolute, entry.Name(), "SKILL.md")
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("stat skill %q: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("skill %q SKILL.md is not a regular file", entry.Name())
		}
		if info.Size() > MaximumSkillBytes {
			return nil, fmt.Errorf("skill %q exceeds %d bytes", entry.Name(), MaximumSkillBytes)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read skill %q: %w", entry.Name(), err)
		}
		if len(raw) > MaximumSkillBytes || totalBytes+len(raw) > MaximumCatalogBytes {
			return nil, fmt.Errorf("skills catalog exceeds configured size limits")
		}
		if !utf8.Valid(raw) {
			return nil, fmt.Errorf("skill %q is not valid UTF-8", entry.Name())
		}
		skill, err := parseSkill(entry.Name(), filepath.ToSlash(filepath.Join(entry.Name(), "SKILL.md")), string(raw))
		if err != nil {
			return nil, err
		}
		catalog.skills = append(catalog.skills, skill)
		totalBytes += len(raw)
	}
	if len(catalog.skills) == 0 {
		return nil, fmt.Errorf("skills directory %q contains no skills", root)
	}
	sort.Slice(catalog.skills, func(left, right int) bool {
		return catalog.skills[left].Name < catalog.skills[right].Name
	})
	return catalog, nil
}

func parseSkill(directory, path, raw string) (Skill, error) {
	if !skillNamePattern.MatchString(directory) || len(directory) > maximumSkillNameLength {
		return Skill{}, fmt.Errorf("invalid skill directory name %q", directory)
	}
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		content := strings.TrimSpace(normalized)
		if content == "" {
			return Skill{}, fmt.Errorf("skill %q has empty instructions", directory)
		}
		return Skill{Name: directory, Path: path, Selectors: []string{directory}, Content: content}, nil
	}
	end := strings.Index(normalized[4:], "\n---\n")
	if end < 0 {
		return Skill{}, fmt.Errorf("skill %q has unterminated front matter", directory)
	}
	end += 4
	metadata := normalized[4:end]
	content := strings.TrimSpace(normalized[end+5:])
	if content == "" {
		return Skill{}, fmt.Errorf("skill %q has empty instructions", directory)
	}
	name := ""
	description := ""
	selectors := make([]string, 0)
	inMatch := false
	seen := make(map[string]bool)
	for lineNumber, line := range strings.Split(metadata, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			if !inMatch {
				return Skill{}, fmt.Errorf("skill %q front matter line %d has an unexpected list item", directory, lineNumber+2)
			}
			selector, err := normalizeSelector(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
			if err != nil {
				return Skill{}, fmt.Errorf("skill %q front matter line %d: %w", directory, lineNumber+2, err)
			}
			if seen[selector] {
				return Skill{}, fmt.Errorf("skill %q contains duplicate selector %q", directory, selector)
			}
			seen[selector] = true
			selectors = append(selectors, selector)
			continue
		}
		inMatch = false
		key, value, found := strings.Cut(trimmed, ":")
		if !found {
			return Skill{}, fmt.Errorf("skill %q front matter line %d is invalid", directory, lineNumber+2)
		}
		switch strings.TrimSpace(key) {
		case "name":
			name = strings.TrimSpace(value)
		case "description":
			description = strings.TrimSpace(value)
		case "match":
			if strings.TrimSpace(value) != "" {
				return Skill{}, fmt.Errorf("skill %q match must be a YAML list", directory)
			}
			inMatch = true
		default:
			return Skill{}, fmt.Errorf("skill %q has unknown front matter key %q", directory, strings.TrimSpace(key))
		}
	}
	if name != directory || !skillNamePattern.MatchString(name) {
		return Skill{}, fmt.Errorf("skill %q name must equal its directory", directory)
	}
	if len(description) > MaximumDescriptionBytes {
		return Skill{}, fmt.Errorf("skill %q description exceeds %d bytes", directory, MaximumDescriptionBytes)
	}
	if len(selectors) == 0 {
		selectors = append(selectors, name)
	}
	sort.Strings(selectors)
	return Skill{Name: name, Description: description, Path: path, Selectors: selectors, Content: content}, nil
}

func normalizeSelector(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !skillNamePattern.MatchString(value) {
		return "", fmt.Errorf("invalid match selector %q", value)
	}
	return value, nil
}

func (catalog *Catalog) Len() int {
	if catalog == nil {
		return 0
	}
	return len(catalog.skills)
}

func (catalog *Catalog) List() []Metadata {
	if catalog == nil {
		return nil
	}
	metadata := make([]Metadata, 0, len(catalog.skills))
	for _, skill := range catalog.skills {
		metadata = append(metadata, Metadata{
			Name:        skill.Name,
			Description: skill.Description,
		})
	}
	return metadata
}

func (catalog *Catalog) Get(name string) (Skill, bool) {
	if catalog == nil || !skillNamePattern.MatchString(name) {
		return Skill{}, false
	}
	index := sort.Search(len(catalog.skills), func(index int) bool {
		return catalog.skills[index].Name >= name
	})
	if index >= len(catalog.skills) || catalog.skills[index].Name != name {
		return Skill{}, false
	}
	skill := catalog.skills[index]
	skill.Selectors = append([]string(nil), skill.Selectors...)
	return skill, true
}

// Select returns only skills with an exact normalized selector in facts.
func (catalog *Catalog) Select(facts []string) []Skill {
	if catalog == nil || len(facts) == 0 {
		return nil
	}
	factSet := make(map[string]bool, len(facts))
	for _, fact := range facts {
		if normalized, err := normalizeSelector(fact); err == nil {
			factSet[normalized] = true
		}
	}
	selected := make([]Skill, 0)
	for _, skill := range catalog.skills {
		matched := false
		for _, selector := range skill.Selectors {
			if factSet[selector] {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		selected = append(selected, skill)
		if len(selected) == MaximumSelectedSkills {
			break
		}
	}
	return selected
}
