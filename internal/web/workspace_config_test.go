package web

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOpsWorkspaceConfigResolvesRootsAndValidatorModes(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"backend", "special"} {
		if err := os.MkdirAll(filepath.Join(base, "repos", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(base, "akritas", "workspaces.local.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{
  "version": 1,
  "validators": ["go-vet", "yamllint"],
  "workspaces": [
    {"name":"backend","root":"../repos/backend","validators":["go-test"]},
    {"name":"special","root":"../repos/special","validators":["go-test"],"validator_mode":"replace"}
  ]
}`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaces, common, err := loadOpsWorkspaceConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(common) != 2 || len(workspaces) != 2 {
		t.Fatalf("unexpected catalog: common=%v workspaces=%+v", common, workspaces)
	}
	if workspaces["backend"].ReplaceValidators || !workspaces["special"].ReplaceValidators {
		t.Fatalf("validator modes were not preserved: %+v", workspaces)
	}
	if workspaces["backend"].Root != filepath.Join(base, "repos", "backend") {
		t.Fatalf("relative root=%q", workspaces["backend"].Root)
	}
	backendProfiles := mergeChangeValidatorProfiles(common, workspaces["backend"].ValidatorProfiles)
	if len(backendProfiles) != 3 {
		t.Fatalf("append profiles=%v", backendProfiles)
	}
	if len(workspaces["special"].ValidatorProfiles) != 1 || workspaces["special"].ValidatorProfiles[0] != "go-test" {
		t.Fatalf("replace profiles=%v", workspaces["special"].ValidatorProfiles)
	}
	if effective := effectiveWorkspaceValidatorProfiles(common, workspaces["special"]); len(effective) != 1 || effective[0] != "go-test" {
		t.Fatalf("replace effective profiles=%v", effective)
	}
}

func TestLoadOpsWorkspaceConfigIsStrict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"unknown":true,"workspaces":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadOpsWorkspaceConfig(path); err == nil {
		t.Fatal("unknown workspace config field was accepted")
	}
}

func TestMergeOpsWorkspacesRejectsDuplicate(t *testing.T) {
	left := map[string]opsWorkspace{"backend": {Name: "backend", Root: "left"}}
	right := map[string]opsWorkspace{"backend": {Name: "backend", Root: "right"}}
	if err := mergeOpsWorkspaces(left, right); err == nil {
		t.Fatal("duplicate workspace was accepted")
	}
}
