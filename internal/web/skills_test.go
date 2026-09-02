package web

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"akritas/internal/skills"
)

func TestInventoryFactsInjectOnlyMatchingSkill(t *testing.T) {
	root := t.TempDir()
	writeWebTestSkill(t, root, "postgresql", "postgresql", "POSTGRESQL-SKILL-CONTENT")
	writeWebTestSkill(t, root, "redis", "redis", "REDIS-SKILL-CONTENT")
	catalog, err := skills.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	server := &opsServer{skillCatalog: catalog}
	system := "task-specific instructions"
	history := []openAIToolMessage{{Role: "system", Content: &system}}
	state := newSkillRunState()

	history = server.appendSelectedSkills(history, skillFactsFromMessages([]openAIChatMessage{{
		Role: "user", Content: "HighCPU\nhost=pg01",
	}}), state)
	if len(state.selected) != 0 {
		t.Fatalf("host name selected an unrelated skill: %+v", state.selected)
	}

	results := []ToolResult{{
		Name:   "mcp.inventory.host",
		Output: json.RawMessage(`{"content":[{"type":"text","text":"pg01:\n  role: postgresql"}]}`),
	}}
	history = server.appendSelectedSkills(history, skillFactsFromToolResults(results), state)
	if _, exists := state.selected["postgresql"]; len(state.selected) != 1 || !exists {
		t.Fatalf("unexpected selected skills: %+v", state.selected)
	}
	if history[0].Content == nil || !strings.Contains(*history[0].Content, "POSTGRESQL-SKILL-CONTENT") ||
		strings.Contains(*history[0].Content, "REDIS-SKILL-CONTENT") {
		t.Fatalf("unexpected system skill context: %v", history[0].Content)
	}
}

func TestExplicitServiceSelectsSkillBeforePlanning(t *testing.T) {
	root := t.TempDir()
	writeWebTestSkill(t, root, "postgresql", "postgresql", "POSTGRESQL-SKILL-CONTENT")
	catalog, err := skills.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	server := &opsServer{skillCatalog: catalog}
	system := "task-specific instructions"
	history := []openAIToolMessage{{Role: "system", Content: &system}}
	state := newSkillRunState()
	history = server.appendSelectedSkills(history, skillFactsFromMessages([]openAIChatMessage{{
		Role: "user", Content: "HighCPU\nservice=postgresql",
	}}), state)
	if _, exists := state.selected["postgresql"]; !exists || history[0].Content == nil || !strings.Contains(*history[0].Content, "POSTGRESQL-SKILL-CONTENT") {
		t.Fatalf("explicit service did not select PostgreSQL skill: selected=%+v history=%+v", state.selected, history)
	}
	planning := selectedSkillPlanningInputs(state.selected)
	if len(planning) != 1 || planning[0]["name"] != "postgresql" || planning[0]["instructions"] != "POSTGRESQL-SKILL-CONTENT" {
		t.Fatalf("planner did not receive selected skill: %+v", planning)
	}
}

func TestKnowledgeSkillToolsListMetadataAndLoadExactSkill(t *testing.T) {
	root := t.TempDir()
	writeWebTestSkill(t, root, "postgresql", "postgres", "POSTGRESQL-SKILL-CONTENT")
	writeWebTestSkill(t, root, "redis", "redis", "REDIS-SKILL-CONTENT")
	catalog, err := skills.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewToolRegistry()
	if err := registerOpsKnowledgeSkillTools(registry, catalog); err != nil {
		t.Fatal(err)
	}
	policy := NamedToolPolicy{Allowed: map[string]bool{
		localKnowledgeListSkillsName: true,
		localKnowledgeLoadSkillName:  true,
	}}
	state := newSkillRunState()
	executionRegistry, err := opsExecutionToolRegistry(registry, policy, catalog, state)
	if err != nil {
		t.Fatal(err)
	}
	listed := executionRegistry.Execute(context.Background(), ToolCall{
		ID: "call_list", Name: localKnowledgeListSkillsName, Arguments: json.RawMessage(`{}`),
	}, policy)
	if listed.Error != nil || !strings.Contains(string(listed.Output), `"name":"postgresql"`) ||
		strings.Contains(string(listed.Output), "POSTGRESQL-SKILL-CONTENT") {
		t.Fatalf("unexpected list result: %+v", listed)
	}
	loaded := executionRegistry.Execute(context.Background(), ToolCall{
		ID: "call_load", Name: localKnowledgeLoadSkillName,
		Arguments: json.RawMessage(`{"name":"postgresql"}`),
	}, policy)
	if loaded.Error != nil || !strings.Contains(string(loaded.Output), `"loaded":true`) ||
		strings.Contains(string(loaded.Output), "POSTGRESQL-SKILL-CONTENT") {
		t.Fatalf("unexpected load result: %+v", loaded)
	}
	if _, exists := state.selected["postgresql"]; !exists {
		t.Fatalf("loaded skill was not recorded: %+v", state.selected)
	}
	server := &opsServer{skillCatalog: catalog}
	system := "task-specific instructions"
	history := server.appendSelectedSkills(
		[]openAIToolMessage{{Role: "system", Content: &system}}, nil, state,
	)
	if history[0].Content == nil || !strings.Contains(*history[0].Content, "POSTGRESQL-SKILL-CONTENT") {
		t.Fatalf("loaded skill was not injected into system context: %+v", history)
	}
	alias := executionRegistry.Execute(context.Background(), ToolCall{
		ID: "call_alias", Name: localKnowledgeLoadSkillName,
		Arguments: json.RawMessage(`{"name":"postgres"}`),
	}, policy)
	if alias.Error == nil || alias.Error.Code != "invalid_arguments" {
		t.Fatalf("skill alias unexpectedly loaded by name: %+v", alias)
	}
}

func TestKnowledgeLoadSkillEnforcesPerRunSelectionLimit(t *testing.T) {
	root := t.TempDir()
	for index := 0; index <= skills.MaximumSelectedSkills; index++ {
		name := fmt.Sprintf("skill-%d", index)
		writeWebTestSkill(t, root, name, name, "GUIDANCE-"+name)
	}
	catalog, err := skills.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewToolRegistry()
	if err := registerOpsKnowledgeSkillTools(registry, catalog); err != nil {
		t.Fatal(err)
	}
	policy := NamedToolPolicy{Allowed: map[string]bool{
		localKnowledgeListSkillsName: true,
		localKnowledgeLoadSkillName:  true,
	}}
	state := newSkillRunState()
	executionRegistry, err := opsExecutionToolRegistry(registry, policy, catalog, state)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < skills.MaximumSelectedSkills; index++ {
		arguments := json.RawMessage(fmt.Sprintf(`{"name":"skill-%d"}`, index))
		result := executionRegistry.Execute(context.Background(), ToolCall{
			ID: fmt.Sprintf("call_%d", index), Name: localKnowledgeLoadSkillName, Arguments: arguments,
		}, policy)
		if result.Error != nil {
			t.Fatalf("load %d failed: %+v", index, result)
		}
	}
	overLimit := executionRegistry.Execute(context.Background(), ToolCall{
		ID: "call_over_limit", Name: localKnowledgeLoadSkillName,
		Arguments: json.RawMessage(fmt.Sprintf(`{"name":"skill-%d"}`, skills.MaximumSelectedSkills)),
	}, policy)
	if overLimit.Error == nil || overLimit.Error.Code != "tool_error" ||
		!strings.Contains(overLimit.Error.Message, "maximum") {
		t.Fatalf("unexpected over-limit result: %+v", overLimit)
	}
}

func writeWebTestSkill(t *testing.T, root, name, selector, content string) {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	value := "---\nname: " + name + "\ndescription: Guidance for " + name + ".\nmatch:\n  - " + selector + "\n---\n" + content + "\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}
