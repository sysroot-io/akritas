package change

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplyPendingChangeWritesApprovedContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(path, []byte("enabled: false\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	change := pendingChange{
		ID: "test", Workspace: opsWorkspace{Name: "test", Root: root},
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute),
		Original: map[string]string{"config.yaml": "enabled: false\n"},
		Updated:  map[string]string{"config.yaml": "enabled: true\n"},
	}
	paths, err := applyPendingChange(change)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "config.yaml" || string(content) != "enabled: true\n" {
		t.Fatalf("unexpected applied change: paths=%v content=%q", paths, content)
	}
}

func TestApplyPendingChangeRejectsStaleSource(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(path, []byte("enabled: user-edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	change := pendingChange{
		Workspace: opsWorkspace{Name: "test", Root: root},
		Original:  map[string]string{"config.yaml": "enabled: false\n"},
		Updated:   map[string]string{"config.yaml": "enabled: true\n"},
	}
	if _, err := applyPendingChange(change); err == nil || !strings.Contains(err.Error(), "stale preview") {
		t.Fatalf("expected stale preview error, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "enabled: user-edit\n" {
		t.Fatalf("stale source was overwritten: %q", content)
	}
}

func TestApplyPendingChangeCreatesApprovedNewFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o700); err != nil {
		t.Fatal(err)
	}
	change := pendingChange{
		Workspace: opsWorkspace{Name: "test", Root: root},
		Updated:   map[string]string{"api/server_test.go": "package api\n"},
		Created:   map[string]bool{"api/server_test.go": true},
	}
	paths, err := applyPendingChange(change)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "api", "server_test.go"))
	if err != nil || string(content) != "package api\n" || len(paths) != 1 {
		t.Fatalf("created content=%q paths=%v err=%v", content, paths, err)
	}
}

func TestApplyPendingChangeRejectsCreateWhenTargetAppeared(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "server_test.go"), []byte("user content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	change := pendingChange{
		Workspace: opsWorkspace{Name: "test", Root: root},
		Updated:   map[string]string{"server_test.go": "generated\n"},
		Created:   map[string]bool{"server_test.go": true},
	}
	if _, err := applyPendingChange(change); err == nil || !strings.Contains(err.Error(), "stale preview") {
		t.Fatalf("expected stale create rejection, got %v", err)
	}
	content, _ := os.ReadFile(filepath.Join(root, "server_test.go"))
	if string(content) != "user content\n" {
		t.Fatalf("appeared target was overwritten: %q", content)
	}
}
