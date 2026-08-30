package change

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/format"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	changeValidatorTimeout          = 2 * time.Minute
	changeValidatorPreflightTimeout = 10 * time.Second
	maximumChangeValidatorOutput    = 64 * 1024
	maximumValidationWorkspaceFiles = 20000
	maximumValidationWorkspaceBytes = 256 * 1024 * 1024
)

var allowedChangeValidatorProfiles = map[string]bool{
	"go-vet": true, "go-test": true, "yamllint": true,
}

type changeValidatorResult struct {
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	Files      []string `json:"files,omitempty"`
	Command    []string `json:"command,omitempty"`
	Output     string   `json:"output,omitempty"`
	DurationMS int64    `json:"duration_ms"`
}

func validateChangeValidatorProfiles(profiles []string) error {
	seen := make(map[string]bool)
	for _, profile := range profiles {
		if !allowedChangeValidatorProfiles[profile] {
			return fmt.Errorf("unknown change validator profile %q", profile)
		}
		if seen[profile] {
			return fmt.Errorf("duplicate change validator profile %q", profile)
		}
		seen[profile] = true
	}
	return nil
}

func preflightChangeValidators(
	ctx context.Context,
	commonProfiles []string,
	workspaces map[string]opsWorkspace,
) error {
	declared := declaredChangeValidatorProfiles(commonProfiles, workspaces)
	if len(declared) == 0 {
		return nil
	}
	if slicesContainAny(declared, "go-vet", "go-test") {
		if _, err := resolveChangeValidatorGoRuntime(); err != nil {
			return fmt.Errorf("validator preflight: resolve active Go toolchain: %w", err)
		}
	}
	temporaryRoot, err := os.MkdirTemp("", "akritas-validator-preflight-")
	if err != nil {
		return fmt.Errorf("validator preflight: create temporary environment: %w", err)
	}
	defer os.RemoveAll(temporaryRoot)
	for _, profile := range declared {
		executable, arguments, isGo := "", []string{"--version"}, false
		switch profile {
		case "go-vet", "go-test":
			executable, arguments, isGo = "go", []string{"version"}, true
		case "yamllint":
			executable = "yamllint"
		default:
			return fmt.Errorf("validator preflight: profile %q is not allowlisted", profile)
		}
		if err := runChangeValidatorPreflightCommand(ctx, temporaryRoot, temporaryRoot, executable, arguments, isGo); err != nil {
			return fmt.Errorf("validator preflight %s: %w", profile, err)
		}
	}
	workspaceNames := make([]string, 0, len(workspaces))
	for name := range workspaces {
		workspaceNames = append(workspaceNames, name)
	}
	sort.Strings(workspaceNames)
	for _, name := range workspaceNames {
		workspace := workspaces[name]
		profiles := effectiveWorkspaceValidatorProfiles(commonProfiles, workspace)
		if !slicesContainAny(profiles, "go-vet", "go-test") {
			continue
		}
		if err := runChangeValidatorPreflightCommand(
			ctx, workspace.Root, temporaryRoot, "go", []string{"list", "-m"}, true,
		); err != nil {
			return fmt.Errorf("validator preflight workspace %q Go module: %w", name, err)
		}
	}
	return nil
}

func declaredChangeValidatorProfiles(commonProfiles []string, workspaces map[string]opsWorkspace) []string {
	declared := append([]string(nil), commonProfiles...)
	for _, workspace := range workspaces {
		declared = mergeChangeValidatorProfiles(declared, workspace.ValidatorProfiles)
	}
	return sortedChangeValidatorProfiles(declared)
}

func effectiveWorkspaceValidatorProfiles(common []string, workspace opsWorkspace) []string {
	if workspace.ReplaceValidators {
		return append([]string(nil), workspace.ValidatorProfiles...)
	}
	return mergeChangeValidatorProfiles(common, workspace.ValidatorProfiles)
}

func runChangeValidatorPreflightCommand(
	ctx context.Context,
	workingDirectory string,
	environmentRoot string,
	executable string,
	arguments []string,
	includeGoCache bool,
) error {
	path := ""
	var err error
	if includeGoCache {
		var runtimeInfo changeValidatorGoRuntimeInfo
		runtimeInfo, err = resolveChangeValidatorGoRuntime()
		if err != nil {
			return err
		}
		path = runtimeInfo.Executable
	} else {
		path, err = exec.LookPath(executable)
		if err != nil {
			return fmt.Errorf("find executable %q: %w", executable, err)
		}
	}
	commandCtx, cancel := context.WithTimeout(ctx, changeValidatorPreflightTimeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, path, arguments...)
	command.Dir = workingDirectory
	command.Env = changeValidatorEnvironment(environmentRoot, includeGoCache)
	var output limitedChangeValidatorBuffer
	command.Stdout, command.Stderr = &output, &output
	err = command.Run()
	if commandCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("command %s timed out after %s", executable, changeValidatorPreflightTimeout)
	}
	if err != nil {
		details := strings.TrimSpace(output.String())
		if details == "" {
			details = err.Error()
		}
		if executable == "go" {
			details = strings.TrimSpace(details + "\n" + changeValidatorGoRuntimeDiagnostic(path, workingDirectory, command.Env))
		}
		return fmt.Errorf("run %s %s: %s", path, strings.Join(arguments, " "), previewChangeSimulationArguments(details))
	}
	return nil
}

func slicesContainAny(values []string, candidates ...string) bool {
	for _, value := range values {
		for _, candidate := range candidates {
			if value == candidate {
				return true
			}
		}
	}
	return false
}

func runChangeValidators(
	ctx context.Context,
	root string,
	result *changeSimulationResult,
	profiles []string,
) error {
	if result == nil || len(result.ChangedFiles) == 0 {
		return nil
	}
	builtin, err := runBuiltinChangeValidators(result)
	result.Validators = append(result.Validators, builtin...)
	if err != nil {
		return err
	}
	if len(profiles) == 0 {
		return nil
	}
	validationRoot, err := copyChangeValidationWorkspace(root)
	if err != nil {
		return err
	}
	defer os.RemoveAll(validationRoot)
	for path, content := range result.updatedFiles {
		target := filepath.Join(validationRoot, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("create validation parent for %q: %w", path, err)
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			return fmt.Errorf("write validation file %q: %w", path, err)
		}
	}
	for _, profile := range profiles {
		validation := runExternalChangeValidator(ctx, validationRoot, result.ChangedFiles, profile)
		result.Validators = append(result.Validators, validation)
		if validation.Status == "failed" {
			return fmt.Errorf("validator %s failed: %s", validation.Name, previewChangeSimulationArguments(validation.Output))
		}
	}
	return nil
}

func runBuiltinChangeValidators(result *changeSimulationResult) ([]changeValidatorResult, error) {
	validations := make([]changeValidatorResult, 0)
	for _, path := range result.ChangedFiles {
		content := result.updatedFiles[path]
		switch strings.ToLower(filepath.Ext(path)) {
		case ".go":
			started := time.Now()
			formatted, err := format.Source([]byte(content))
			validation := changeValidatorResult{Name: "go-format", Files: []string{path}, DurationMS: time.Since(started).Milliseconds()}
			if err != nil {
				validation.Status, validation.Output = "failed", err.Error()
				validations = append(validations, validation)
				return validations, fmt.Errorf("validator go-format failed for %q: %w", path, err)
			}
			if !bytes.Equal(formatted, []byte(content)) {
				validation.Status = "failed"
				validation.Output = "file is not gofmt-formatted"
				validations = append(validations, validation)
				return validations, fmt.Errorf("validator go-format failed for %q: file is not gofmt-formatted", path)
			}
			validation.Status = "passed"
			validations = append(validations, validation)
		case ".json":
			started := time.Now()
			validation := changeValidatorResult{Name: "json-syntax", Files: []string{path}, DurationMS: time.Since(started).Milliseconds()}
			if !json.Valid([]byte(content)) {
				validation.Status, validation.Output = "failed", "invalid JSON"
				validations = append(validations, validation)
				return validations, fmt.Errorf("validator json-syntax failed for %q", path)
			}
			validation.Status = "passed"
			validations = append(validations, validation)
		}
	}
	return validations, nil
}

func runExternalChangeValidator(ctx context.Context, root string, changedFiles []string, profile string) changeValidatorResult {
	validation := changeValidatorResult{Name: profile}
	var executable string
	var arguments []string
	switch profile {
	case "go-vet":
		if !changePathsMatch(changedFiles, ".go", "go.mod", "go.sum") {
			validation.Status, validation.Output = "skipped", "no changed Go files"
			return validation
		}
		executable, arguments = "go", []string{"vet", "./..."}
	case "go-test":
		if !changePathsMatch(changedFiles, ".go", "go.mod", "go.sum") {
			validation.Status, validation.Output = "skipped", "no changed Go files"
			return validation
		}
		executable, arguments = "go", []string{"test", "./..."}
	case "yamllint":
		for _, path := range changedFiles {
			extension := strings.ToLower(filepath.Ext(path))
			if extension == ".yaml" || extension == ".yml" || extension == ".sls" {
				validation.Files = append(validation.Files, path)
			}
		}
		if len(validation.Files) == 0 {
			validation.Status, validation.Output = "skipped", "no changed YAML/SLS files"
			return validation
		}
		executable, arguments = "yamllint", append([]string(nil), validation.Files...)
	default:
		validation.Status, validation.Output = "failed", "profile is not allowlisted"
		return validation
	}
	isGo := executable == "go"
	if isGo {
		runtimeInfo, err := resolveChangeValidatorGoRuntime()
		if err != nil {
			validation.Status, validation.Output = "failed", err.Error()
			return validation
		}
		executable = runtimeInfo.Executable
	} else {
		path, err := exec.LookPath(executable)
		if err != nil {
			validation.Status, validation.Output = "failed", err.Error()
			return validation
		}
		executable = path
	}
	validation.Command = append([]string{executable}, arguments...)
	validatorCtx, cancel := context.WithTimeout(ctx, changeValidatorTimeout)
	defer cancel()
	command := exec.CommandContext(validatorCtx, executable, arguments...)
	command.Dir = root
	command.Env = changeValidatorEnvironment(root, isGo)
	var output limitedChangeValidatorBuffer
	command.Stdout, command.Stderr = &output, &output
	started := time.Now()
	err := command.Run()
	validation.DurationMS = time.Since(started).Milliseconds()
	validation.Output = strings.TrimSpace(output.String())
	if output.Truncated {
		validation.Output += "\n<output truncated by Akritas>"
	}
	if validatorCtx.Err() == context.DeadlineExceeded {
		validation.Status = "failed"
		validation.Output = strings.TrimSpace(validation.Output + "\nvalidator timed out")
		return validation
	}
	if err != nil {
		validation.Status = "failed"
		if validation.Output == "" {
			validation.Output = err.Error()
		}
		if isGo {
			validation.Output = strings.TrimSpace(validation.Output + "\n" + changeValidatorGoRuntimeDiagnostic(executable, root, command.Env))
		}
		return validation
	}
	validation.Status = "passed"
	return validation
}

func changeValidatorEnvironment(root string, includeGoCache bool) []string {
	allowed := map[string]bool{
		"PATH": true, "PATHEXT": true, "SYSTEMROOT": true, "WINDIR": true,
		"GOPATH": true,
	}
	environment := make([]string, 0, len(allowed)+8)
	for _, value := range os.Environ() {
		key, _, found := strings.Cut(value, "=")
		if found && allowed[strings.ToUpper(key)] {
			environment = append(environment, value)
		}
	}
	temporary := filepath.Join(root, ".akritas-validator-tmp")
	cache := filepath.Join(root, ".akritas-validator-cache")
	home := filepath.Join(root, ".akritas-validator-home")
	_ = os.MkdirAll(temporary, 0o700)
	_ = os.MkdirAll(cache, 0o700)
	_ = os.MkdirAll(home, 0o700)
	environment = append(environment,
		"HOME="+home, "TMPDIR="+temporary, "TMP="+temporary, "TEMP="+temporary,
		"GOPROXY=off", "GOSUMDB=off", "CGO_ENABLED=0",
	)
	if includeGoCache {
		runtimeInfo, err := resolveChangeValidatorGoRuntime()
		if err == nil {
			environment = append(environment,
				"GOROOT="+runtimeInfo.Root,
				"GOTOOLCHAIN=local",
			)
		} else {
			environment = append(environment, "GOTOOLCHAIN=auto")
		}
		environment = append(environment, "GOCACHE="+cache)
		goExecutable := "go"
		if err == nil {
			goExecutable = runtimeInfo.Executable
		}
		cacheCommand := exec.Command(goExecutable, "env", "GOMODCACHE")
		cacheCommand.Env = environment
		if output, err := cacheCommand.Output(); err == nil {
			if moduleCache := strings.TrimSpace(string(output)); moduleCache != "" {
				environment = append(environment, "GOMODCACHE="+moduleCache)
			}
		}
	}
	return environment
}

type changeValidatorGoRuntimeInfo struct {
	Executable string
	Version    string
	Root       string
}

func resolveChangeValidatorGoToolchain() (string, error) {
	runtimeInfo, err := resolveChangeValidatorGoRuntime()
	if err != nil {
		return "", err
	}
	return runtimeInfo.Version, nil
}

func resolveChangeValidatorGoRuntime() (changeValidatorGoRuntimeInfo, error) {
	path, err := exec.LookPath("go")
	if err != nil {
		return changeValidatorGoRuntimeInfo{}, fmt.Errorf("find executable %q: %w", "go", err)
	}
	command := exec.Command(path, "env", "GOVERSION", "GOROOT")
	output, err := command.CombinedOutput()
	if err != nil {
		details := strings.TrimSpace(string(output))
		if details == "" {
			details = err.Error()
		}
		return changeValidatorGoRuntimeInfo{}, fmt.Errorf("run %s env GOVERSION GOROOT: %s", path, previewChangeSimulationArguments(details))
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 2 {
		return changeValidatorGoRuntimeInfo{}, fmt.Errorf("go env GOVERSION GOROOT returned %d lines, want 2", len(lines))
	}
	version := strings.TrimSpace(lines[0])
	root := strings.TrimSpace(lines[1])
	if !strings.HasPrefix(version, "go1.") || strings.ContainsAny(version, " \t\r\n") {
		return changeValidatorGoRuntimeInfo{}, fmt.Errorf("go env GOVERSION returned unsupported value %q", version)
	}
	if root == "" || strings.ContainsAny(root, "\r\n") {
		return changeValidatorGoRuntimeInfo{}, fmt.Errorf("go env GOROOT returned unsupported value %q", root)
	}
	executable := filepath.Join(root, "bin", "go")
	if _, err := os.Stat(executable); err != nil {
		executable += ".exe"
	}
	state, err := os.Stat(executable)
	if err != nil {
		return changeValidatorGoRuntimeInfo{}, fmt.Errorf("stat active Go executable %q: %w", executable, err)
	}
	if !state.Mode().IsRegular() {
		return changeValidatorGoRuntimeInfo{}, fmt.Errorf("active Go executable %q is not a regular file", executable)
	}
	return changeValidatorGoRuntimeInfo{Executable: executable, Version: version, Root: root}, nil
}

func changeValidatorGoRuntimeDiagnostic(path, root string, environment []string) string {
	command := exec.Command(path, "version")
	command.Dir = root
	command.Env = environment
	output, err := command.CombinedOutput()
	version := strings.TrimSpace(string(output))
	if err != nil {
		if version == "" {
			version = err.Error()
		} else {
			version += "; " + err.Error()
		}
	}
	toolchain := "unknown"
	for _, value := range environment {
		if strings.HasPrefix(value, "GOTOOLCHAIN=") {
			toolchain = strings.TrimPrefix(value, "GOTOOLCHAIN=")
			break
		}
	}
	return fmt.Sprintf("Akritas Go runtime: path=%s; %s; GOTOOLCHAIN=%s", path, version, toolchain)
}

func changePathsMatch(paths []string, extensionsAndNames ...string) bool {
	for _, path := range paths {
		for _, candidate := range extensionsAndNames {
			if strings.HasPrefix(candidate, ".") && strings.EqualFold(filepath.Ext(path), candidate) || filepath.Base(path) == candidate {
				return true
			}
		}
	}
	return false
}

func copyChangeValidationWorkspace(root string) (string, error) {
	destination, err := os.MkdirTemp("", "akritas-validate-")
	if err != nil {
		return "", fmt.Errorf("create validation workspace: %w", err)
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(destination)
		}
	}()
	sourceRoot, err := os.OpenRoot(root)
	if err != nil {
		return "", fmt.Errorf("open validation source workspace: %w", err)
	}
	defer sourceRoot.Close()
	destinationRoot, err := os.OpenRoot(destination)
	if err != nil {
		return "", fmt.Errorf("open validation destination workspace: %w", err)
	}
	defer destinationRoot.Close()
	files, totalBytes := 0, int64(0)
	err = fs.WalkDir(sourceRoot.FS(), ".", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != "." && entry.Name() != "vendor" && changeDiscoveryExcludedDirectories[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || isChangeDiscoverySensitiveName(entry.Name()) {
			return nil
		}
		state, err := entry.Info()
		if err != nil || !state.Mode().IsRegular() {
			return err
		}
		files++
		totalBytes += state.Size()
		if files > maximumValidationWorkspaceFiles || totalBytes > maximumValidationWorkspaceBytes {
			return fmt.Errorf("validation workspace exceeds copy limits")
		}
		relative := filepath.FromSlash(path)
		if err := destinationRoot.MkdirAll(filepath.Dir(relative), 0o700); err != nil {
			return err
		}
		source, err := sourceRoot.Open(relative)
		if err != nil {
			return err
		}
		output, err := destinationRoot.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, state.Mode().Perm())
		if err != nil {
			_ = source.Close()
			return err
		}
		_, copyErr := io.Copy(output, source)
		sourceCloseErr := source.Close()
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if sourceCloseErr != nil {
			return sourceCloseErr
		}
		return closeErr
	})
	if err != nil {
		return "", fmt.Errorf("copy validation workspace: %w", err)
	}
	failed = false
	return destination, nil
}

type limitedChangeValidatorBuffer struct {
	Buffer    bytes.Buffer
	Truncated bool
}

func (buffer *limitedChangeValidatorBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := maximumChangeValidatorOutput - buffer.Buffer.Len()
	if remaining <= 0 {
		buffer.Truncated = true
		return original, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		buffer.Truncated = true
	}
	_, _ = buffer.Buffer.Write(value)
	return original, nil
}

func (buffer *limitedChangeValidatorBuffer) String() string { return buffer.Buffer.String() }

func sortedChangeValidatorProfiles(profiles []string) []string {
	result := mergeChangeValidatorProfiles(nil, profiles)
	sort.Strings(result)
	return result
}

func mergeChangeValidatorProfiles(groups ...[]string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0)
	for _, group := range groups {
		for _, profile := range group {
			if !seen[profile] {
				seen[profile] = true
				result = append(result, profile)
			}
		}
	}
	return result
}
