package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogSelectsOnlyMatchingSkills(t *testing.T) {
	root := t.TempDir()
	writeTestSkill(t, root, "postgresql", []string{"postgresql", "postgres"}, "Check active queries and autovacuum.")
	writeTestSkill(t, root, "redis", []string{"redis"}, "Check Redis clients.")
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	selected := catalog.Select([]string{"PostgreSQL", "unrelated"})
	if len(selected) != 1 || selected[0].Name != "postgresql" || strings.Contains(selected[0].Content, "Redis") {
		t.Fatalf("unexpected selection: %+v", selected)
	}
	if selected := catalog.Select([]string{"mysql"}); len(selected) != 0 {
		t.Fatalf("unmatched facts selected skills: %+v", selected)
	}
}

func TestCatalogRejectsInvalidSkillMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "postgresql")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("---\nname: other\nmatch:\n  - postgresql\n---\nRules.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "name must equal") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestCatalogRejectsOversizedDescription(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "postgresql")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: postgresql\ndescription: " + strings.Repeat("x", MaximumDescriptionBytes+1) + "\n---\nRules.\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "description exceeds") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestCatalogUsesDirectoryNameWhenFrontMatterIsAbsent(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "postgresql")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("# PostgreSQL\n\nCheck active queries.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	selected := catalog.Select([]string{"postgresql"})
	if len(selected) != 1 || selected[0].Name != "postgresql" {
		t.Fatalf("unexpected selection: %+v", selected)
	}
}

func TestCatalogListsMetadataAndGetsOnlyExactNames(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "postgresql")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: postgresql\ndescription: PostgreSQL investigation guidance.\nmatch:\n  - postgres\n---\nSECRET-INSTRUCTIONS\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	metadata := catalog.List()
	if len(metadata) != 1 || metadata[0].Name != "postgresql" ||
		metadata[0].Description != "PostgreSQL investigation guidance." {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
	if encoded := metadata[0].Name + metadata[0].Description; strings.Contains(encoded, "SECRET-INSTRUCTIONS") {
		t.Fatalf("skill instructions leaked through metadata: %+v", metadata)
	}
	if skill, exists := catalog.Get("postgresql"); !exists || skill.Content != "SECRET-INSTRUCTIONS" {
		t.Fatalf("exact skill lookup failed: exists=%t skill=%+v", exists, skill)
	}
	if _, exists := catalog.Get("postgres"); exists {
		t.Fatal("selector alias unexpectedly resolved as an exact skill name")
	}
}

func writeTestSkill(t *testing.T, root, name string, selectors []string, content string) {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	var value strings.Builder
	value.WriteString("---\nname: " + name + "\nmatch:\n")
	for _, selector := range selectors {
		value.WriteString("  - " + selector + "\n")
	}
	value.WriteString("---\n" + content + "\n")
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(value.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}
