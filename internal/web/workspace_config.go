package web

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const opsWorkspaceConfigVersion = 1

type opsWorkspaceConfigFile struct {
	Version    int                       `json:"version"`
	Validators []string                  `json:"validators"`
	Workspaces []opsWorkspaceConfigEntry `json:"workspaces"`
}

type opsWorkspaceConfigEntry struct {
	Name          string   `json:"name"`
	Root          string   `json:"root"`
	Validators    []string `json:"validators,omitempty"`
	ValidatorMode string   `json:"validator_mode,omitempty"`
}

func loadOpsWorkspaceConfig(path string) (map[string]opsWorkspace, []string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return make(map[string]opsWorkspace), nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open workspace config: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maximumOpsAPIRequestBytes+1))
	decoder.DisallowUnknownFields()
	var config opsWorkspaceConfigFile
	if err := decoder.Decode(&config); err != nil {
		return nil, nil, fmt.Errorf("decode workspace config: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, nil, fmt.Errorf("decode workspace config: %w", err)
	}
	if config.Version != opsWorkspaceConfigVersion {
		return nil, nil, fmt.Errorf("workspace config version=%d, want %d", config.Version, opsWorkspaceConfigVersion)
	}
	if err := validateChangeValidatorProfiles(config.Validators); err != nil {
		return nil, nil, fmt.Errorf("workspace config common validators: %w", err)
	}
	absoluteConfig, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve workspace config path: %w", err)
	}
	baseDirectory := filepath.Dir(absoluteConfig)
	workspaces := make(map[string]opsWorkspace, len(config.Workspaces))
	for index, entry := range config.Workspaces {
		entry.Name = strings.TrimSpace(entry.Name)
		entry.Root = strings.TrimSpace(entry.Root)
		entry.ValidatorMode = strings.TrimSpace(entry.ValidatorMode)
		if !mcpToolNameValid(entry.Name) || entry.Root == "" {
			return nil, nil, fmt.Errorf("workspace config workspaces[%d] requires valid name and root", index)
		}
		if _, duplicate := workspaces[entry.Name]; duplicate {
			return nil, nil, fmt.Errorf("workspace config duplicate workspace %q", entry.Name)
		}
		if entry.ValidatorMode == "" {
			entry.ValidatorMode = "append"
		}
		if entry.ValidatorMode != "append" && entry.ValidatorMode != "replace" {
			return nil, nil, fmt.Errorf("workspace %q validator_mode must be append or replace", entry.Name)
		}
		if err := validateChangeValidatorProfiles(entry.Validators); err != nil {
			return nil, nil, fmt.Errorf("workspace %q validators: %w", entry.Name, err)
		}
		rootPath := entry.Root
		if !filepath.IsAbs(rootPath) {
			rootPath = filepath.Join(baseDirectory, rootPath)
		}
		root, err := resolveChangeSimulationRoot(rootPath)
		if err != nil {
			return nil, nil, fmt.Errorf("workspace %q: %w", entry.Name, err)
		}
		workspaces[entry.Name] = opsWorkspace{
			Name: entry.Name, Root: root,
			ValidatorProfiles: sortedChangeValidatorProfiles(entry.Validators),
			ReplaceValidators: entry.ValidatorMode == "replace",
		}
	}
	return workspaces, sortedChangeValidatorProfiles(config.Validators), nil
}

func mergeOpsWorkspaces(destination, source map[string]opsWorkspace) error {
	for name, workspace := range source {
		if _, duplicate := destination[name]; duplicate {
			return fmt.Errorf("duplicate workspace %q across CLI and config", name)
		}
		destination[name] = workspace
	}
	return nil
}
