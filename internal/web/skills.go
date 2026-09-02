package web

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"akritas/internal/skills"
)

const (
	maximumSkillSelectionFacts   = 64
	localKnowledgeListSkillsName = "knowledge.list_skills"
	localKnowledgeLoadSkillName  = "knowledge.load_skill"
)

var skillFactPattern = regexp.MustCompile(`(?i)(?:^|[\s,{])"?(?:role|service|technology|component|database|db|engine|platform)"?\s*[:=]\s*"?([a-z0-9][a-z0-9._-]{0,63})`)

var skillFactKeys = map[string]bool{
	"role": true, "roles": true,
	"service": true, "services": true,
	"technology": true, "technologies": true,
	"component": true, "components": true,
	"database": true, "databases": true,
	"db": true, "engine": true, "platform": true,
}

type skillFactCollector struct {
	seen   map[string]bool
	values []string
}

type skillRunState struct {
	selected map[string]skills.Skill
	injected map[string]bool
}

type knowledgeLoadSkillArguments struct {
	Name string `json:"name"`
}

func newSkillRunState() *skillRunState {
	return &skillRunState{
		selected: make(map[string]skills.Skill),
		injected: make(map[string]bool),
	}
}

func (state *skillRunState) add(skill skills.Skill) error {
	if state == nil {
		return fmt.Errorf("skill Run state is nil")
	}
	if _, exists := state.selected[skill.Name]; exists {
		return nil
	}
	if len(state.selected) >= skills.MaximumSelectedSkills {
		return fmt.Errorf("Run already selected the maximum of %d skills", skills.MaximumSelectedSkills)
	}
	state.selected[skill.Name] = skill
	return nil
}

func registerOpsKnowledgeSkillTools(registry *ToolRegistry, catalog *skills.Catalog) error {
	if registry == nil || catalog == nil {
		return fmt.Errorf("knowledge skill tools require a registry and catalog")
	}
	for _, definition := range knowledgeSkillToolDefinitions(catalog, nil) {
		if err := registry.Register(definition); err != nil {
			return err
		}
	}
	return nil
}

func knowledgeSkillToolDefinitions(catalog *skills.Catalog, state *skillRunState) []ToolDefinition {
	listSchema := json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`)
	loadSchema := json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "Exact skill name returned by knowledge.list_skills."}
  },
  "required": ["name"],
  "additionalProperties": false
}`)
	return []ToolDefinition{
		{
			Name:        localKnowledgeListSkillsName,
			Description: "Lists the names and short descriptions of available operational skills without loading their instructions. Use only when explicit request fields and inventory or CMDB evidence do not identify enough guidance.",
			InputSchema: listSchema,
			Permission:  ToolPermissionRead,
			ValidateArguments: func(raw json.RawMessage) error {
				var arguments struct{}
				return decodeStrictJSONObject(raw, &arguments)
			},
			Handler: func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
				return json.Marshal(map[string]any{"skills": catalog.List()})
			},
		},
		{
			Name:        localKnowledgeLoadSkillName,
			Description: "Loads one exact operational skill from the host catalog. Loading supplies trusted investigation guidance but does not prove the affected technology, grant a capability, or authorize an action.",
			InputSchema: loadSchema,
			Permission:  ToolPermissionRead,
			ValidateArguments: func(raw json.RawMessage) error {
				var arguments knowledgeLoadSkillArguments
				if err := decodeStrictJSONObject(raw, &arguments); err != nil {
					return err
				}
				if _, exists := catalog.Get(arguments.Name); !exists {
					return fmt.Errorf("unknown exact skill name %q", arguments.Name)
				}
				return nil
			},
			Handler: func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
				var arguments knowledgeLoadSkillArguments
				if err := decodeStrictJSONObject(raw, &arguments); err != nil {
					return nil, err
				}
				skill, exists := catalog.Get(arguments.Name)
				if !exists {
					return nil, fmt.Errorf("unknown exact skill name %q", arguments.Name)
				}
				if state == nil {
					return nil, fmt.Errorf("load skill requires an active Run")
				}
				if err := state.add(skill); err != nil {
					return nil, err
				}
				return json.Marshal(map[string]any{
					"loaded": true, "name": skill.Name, "source": skill.Path,
				})
			},
		},
	}
}

func newSkillFactCollector() *skillFactCollector {
	return &skillFactCollector{seen: make(map[string]bool)}
}

func (collector *skillFactCollector) add(value string) {
	if collector == nil || len(collector.values) == maximumSkillSelectionFacts {
		return
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if skillSelectionToken(value) {
		if !collector.seen[value] {
			collector.seen[value] = true
			collector.values = append(collector.values, value)
		}
		return
	}
	for _, token := range strings.FieldsFunc(value, func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '.' && character != '_' && character != '-'
	}) {
		if skillSelectionToken(token) && !collector.seen[token] {
			collector.seen[token] = true
			collector.values = append(collector.values, token)
			if len(collector.values) == maximumSkillSelectionFacts {
				return
			}
		}
	}
}

func (collector *skillFactCollector) addMatches(value string) {
	for _, match := range skillFactPattern.FindAllStringSubmatch(value, -1) {
		if len(match) == 2 {
			collector.add(match[1])
		}
	}
}

func skillSelectionToken(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			(index > 0 && (character == '.' || character == '_' || character == '-')) {
			continue
		}
		return false
	}
	return true
}

func skillFactsFromMessages(messages []openAIChatMessage) []string {
	collector := newSkillFactCollector()
	if len(messages) == 0 {
		return nil
	}
	collector.addMatches(messages[len(messages)-1].Content)
	return collector.values
}

func skillFactsFromToolResults(results []ToolResult) []string {
	collector := newSkillFactCollector()
	for _, result := range results {
		if result.Error != nil || len(result.Output) == 0 || !inventoryLikeTool(result.Name) {
			continue
		}
		var value any
		if err := json.Unmarshal(result.Output, &value); err != nil {
			continue
		}
		walkSkillFactValue(value, "", true, collector, 0)
	}
	return collector.values
}

func inventoryLikeTool(name string) bool {
	for _, segment := range strings.FieldsFunc(strings.ToLower(name), func(character rune) bool {
		return character == '.' || character == '-' || character == '_'
	}) {
		if segment == "inventory" || segment == "cmdb" {
			return true
		}
	}
	return false
}

func walkSkillFactValue(value any, parentKey string, scanText bool, collector *skillFactCollector, depth int) {
	if collector == nil || depth > 32 || len(collector.values) == maximumSkillSelectionFacts {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalizedKey := strings.ToLower(strings.TrimSpace(key))
			walkSkillFactValue(child, normalizedKey, scanText, collector, depth+1)
		}
	case []any:
		for _, child := range typed {
			walkSkillFactValue(child, parentKey, scanText, collector, depth+1)
		}
	case string:
		if skillFactKeys[parentKey] {
			collector.add(typed)
		}
		if scanText {
			collector.addMatches(typed)
		}
	}
}

func (server *opsServer) appendSelectedSkills(
	history []openAIToolMessage,
	facts []string,
	state *skillRunState,
) []openAIToolMessage {
	if server == nil || server.skillCatalog == nil || len(history) == 0 || state == nil {
		return history
	}
	if len(state.selected) < skills.MaximumSelectedSkills {
		for _, skill := range server.skillCatalog.Select(facts) {
			if err := state.add(skill); err != nil {
				break
			}
		}
	}
	return appendPendingSkillContext(history, state)
}

func appendPendingSkillContext(history []openAIToolMessage, state *skillRunState) []openAIToolMessage {
	if len(history) == 0 || state == nil || len(state.selected) == 0 {
		return history
	}
	names := sortedSelectedSkillNames(state.selected)
	var addition strings.Builder
	for _, name := range names {
		if state.injected[name] {
			continue
		}
		skill := state.selected[name]
		state.injected[name] = true
		fmt.Fprintf(
			&addition,
			"\n\n<AKRITAS_SKILL name=%q source=%q>\n%s\n</AKRITAS_SKILL>",
			skill.Name, skill.Path, skill.Content,
		)
	}
	if addition.Len() == 0 {
		return history
	}
	prefix := "Host-selected operational skills follow. They are trusted guidance, but they do not add capabilities or authorize actions."
	content := prefix + addition.String()
	if history[0].Role == "system" {
		if history[0].Content != nil && strings.TrimSpace(*history[0].Content) != "" {
			content = strings.TrimSpace(*history[0].Content) + "\n\n" + content
		}
		history[0].Content = &content
		return history
	}
	history = append([]openAIToolMessage{{Role: "system", Content: &content}}, history...)
	return history
}

func sortedSelectedSkillNames(selected map[string]skills.Skill) []string {
	if len(selected) == 0 {
		return nil
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func selectedSkillPlanningInputs(selected map[string]skills.Skill) []map[string]string {
	names := sortedSelectedSkillNames(selected)
	if len(names) == 0 {
		return []map[string]string{}
	}
	inputs := make([]map[string]string, 0, len(names))
	for _, name := range names {
		skill := selected[name]
		inputs = append(inputs, map[string]string{
			"name": skill.Name, "source": skill.Path, "instructions": skill.Content,
		})
	}
	return inputs
}
