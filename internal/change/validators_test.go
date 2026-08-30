package change

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinChangeValidatorsAcceptFormattedGoAndJSON(t *testing.T) {
	result := changeSimulationResult{
		ChangedFiles: []string{"api.go", "config.json"},
		updatedFiles: map[string]string{
			"api.go":      "package api\n\nfunc Enabled() bool { return true }\n",
			"config.json": "{\"enabled\":true}\n",
		},
	}
	if err := runChangeValidators(context.Background(), "", &result, nil); err != nil {
		t.Fatal(err)
	}
	if len(result.Validators) != 2 || result.Validators[0].Status != "passed" || result.Validators[1].Status != "passed" {
		t.Fatalf("unexpected validators: %+v", result.Validators)
	}
}

func TestBuiltinChangeValidatorsRejectInvalidGo(t *testing.T) {
	result := changeSimulationResult{
		ChangedFiles: []string{"api.go"},
		updatedFiles: map[string]string{"api.go": "package api\nfunc broken(\n"},
	}
	if err := runChangeValidators(context.Background(), "", &result, nil); err == nil || !strings.Contains(err.Error(), "go-format") {
		t.Fatalf("expected go-format failure, got %v", err)
	}
	if len(result.Validators) != 1 || result.Validators[0].Status != "failed" {
		t.Fatalf("unexpected validators: %+v", result.Validators)
	}
}

func TestExternalValidatorSkipsUnmatchedFiles(t *testing.T) {
	result := runExternalChangeValidator(context.Background(), t.TempDir(), []string{"README.md"}, "go-test")
	if result.Status != "skipped" || result.Name != "go-test" {
		t.Fatalf("unexpected skipped validator: %+v", result)
	}
}

func TestValidationWorkspaceExcludesSecretsAndGit(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"main.go": "package main\n", ".env": "TOKEN=secret\n", ".git/config": "secret\n",
	} {
		target := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	copyRoot, err := copyChangeValidationWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(copyRoot)
	if _, err := os.Stat(filepath.Join(copyRoot, "main.go")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".env", filepath.Join(".git", "config")} {
		if _, err := os.Stat(filepath.Join(copyRoot, path)); !os.IsNotExist(err) {
			t.Fatalf("excluded path %q exists or stat failed unexpectedly: %v", path, err)
		}
	}
}

func TestChangeValidatorEnvironmentExcludesAPIKeys(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "secret")
	t.Setenv("AKRITAS_API_KEY", "secret")
	environment := strings.Join(changeValidatorEnvironment(t.TempDir(), true), "\n")
	if strings.Contains(environment, "OPENAI_API_KEY") || strings.Contains(environment, "AKRITAS_API_KEY") {
		t.Fatalf("validator environment leaked API keys: %s", environment)
	}
	if !strings.Contains(environment, "GOPROXY=off") ||
		!strings.Contains(environment, "GOTOOLCHAIN=local") ||
		!strings.Contains(environment, "CGO_ENABLED=0") {
		t.Fatalf("validator environment is missing restrictions: %s", environment)
	}
}

func TestValidateChangeValidatorProfilesRejectsUnknownAndDuplicate(t *testing.T) {
	if err := validateChangeValidatorProfiles([]string{"go-vet", "yamllint"}); err != nil {
		t.Fatal(err)
	}
	if err := validateChangeValidatorProfiles([]string{"shell"}); err == nil {
		t.Fatal("unknown validator profile was accepted")
	}
	if err := validateChangeValidatorProfiles([]string{"go-test", "go-test"}); err == nil {
		t.Fatal("duplicate validator profile was accepted")
	}
}

func TestPreflightChangeValidatorsAcceptsLocalGoModule(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/preflight\n\ngo 1.20\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaces := map[string]opsWorkspace{
		"backend": {Name: "backend", Root: root},
	}
	if err := preflightChangeValidators(context.Background(), []string{"go-vet"}, workspaces); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightChangeValidatorsRejectsMissingExecutable(t *testing.T) {
	t.Setenv("PATH", "")
	err := preflightChangeValidators(context.Background(), []string{"go-vet"}, nil)
	if err == nil || !strings.Contains(err.Error(), `resolve active Go toolchain`) ||
		!strings.Contains(err.Error(), `find executable "go"`) {
		t.Fatalf("expected missing Go executable, got %v", err)
	}
}

func TestResolveChangeValidatorGoToolchainMatchesActiveGo(t *testing.T) {
	runtimeInfo, err := resolveChangeValidatorGoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(runtimeInfo.Version, "go1.") || runtimeInfo.Root == "" || runtimeInfo.Executable == "" {
		t.Fatalf("resolved runtime=%+v", runtimeInfo)
	}
	environment := strings.Join(changeValidatorEnvironment(t.TempDir(), true), "\n")
	if !strings.Contains(environment, "GOTOOLCHAIN=local") ||
		!strings.Contains(environment, "GOROOT="+runtimeInfo.Root) {
		t.Fatalf("validator environment does not pin active runtime %+v: %s", runtimeInfo, environment)
	}
}

func TestPreflightChangeValidatorsChecksWorkspaceGoRequirement(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/future\n\ngo 999.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaces := map[string]opsWorkspace{
		"future": {Name: "future", Root: root, ValidatorProfiles: []string{"go-test"}, ReplaceValidators: true},
	}
	err := preflightChangeValidators(context.Background(), nil, workspaces)
	if err == nil || !strings.Contains(err.Error(), `workspace "future" Go module`) ||
		!strings.Contains(err.Error(), "Akritas Go runtime") {
		t.Fatalf("expected workspace Go requirement failure, got %v", err)
	}
}

func TestDeclaredChangeValidatorProfilesIncludesWorkspaceOverrides(t *testing.T) {
	workspaces := map[string]opsWorkspace{
		"backend": {ValidatorProfiles: []string{"go-test"}},
		"config":  {ValidatorProfiles: []string{"yamllint"}, ReplaceValidators: true},
	}
	profiles := declaredChangeValidatorProfiles([]string{"go-vet"}, workspaces)
	if strings.Join(profiles, ",") != "go-test,go-vet,yamllint" {
		t.Fatalf("declared profiles=%v", profiles)
	}
}
