package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	akritasinstructions "akritas/internal/instructions"
	akritasskills "akritas/internal/skills"
)

const (
	opsServerConfigVersion      = 1
	maximumOpsServerConfigBytes = 1024 * 1024
	opsServerConfigEnvironment  = "AKRITAS_CONFIG"
)

type opsServerOptions struct {
	Address                   string
	BaseURL                   string
	UpstreamModel             string
	ModelID                   string
	UpstreamAPIKeyEnvironment string
	APIKeyEnvironment         string
	SystemInstructionsPath    string
	SkillsDirectory           string
	ResponseLanguage          string
	RAGIndexPath              string
	MCPConfigPath             string
	WorkspaceConfigPath       string
	AuditLogPath              string
	SearchTopK                int
	ResultRunes               int
	DefaultMaxTokens          int
	MaxTokensLimit            int
	MaxToolCalls              int
	MaxIterations             int
	MaxToolResultBytes        int
	MaxRetrievedContextBytes  int
	MaxContextTokens          int
	MaxModelTokens            int
	Temperature               float64
	RequestTimeout            time.Duration
	Workspaces                []string
	ChangeValidatorProfiles   []string
}

type opsServerConfigFile struct {
	Version                   int      `json:"version"`
	Address                   *string  `json:"address,omitempty"`
	BaseURL                   *string  `json:"base_url,omitempty"`
	UpstreamModel             *string  `json:"upstream_model,omitempty"`
	ModelID                   *string  `json:"model,omitempty"`
	UpstreamAPIKeyEnvironment *string  `json:"upstream_api_key_env,omitempty"`
	APIKeyEnvironment         *string  `json:"api_key_env,omitempty"`
	SystemInstructionsPath    *string  `json:"system_instructions,omitempty"`
	SkillsDirectory           *string  `json:"skills_dir,omitempty"`
	ResponseLanguage          *string  `json:"response_language,omitempty"`
	RAGIndexPath              *string  `json:"rag_index,omitempty"`
	MCPConfigPath             *string  `json:"mcp_config,omitempty"`
	WorkspaceConfigPath       *string  `json:"workspace_config,omitempty"`
	AuditLogPath              *string  `json:"audit_log,omitempty"`
	SearchTopK                *int     `json:"search_top_k,omitempty"`
	ResultRunes               *int     `json:"result_runes,omitempty"`
	DefaultMaxTokens          *int     `json:"max_tokens,omitempty"`
	MaxTokensLimit            *int     `json:"max_tokens_limit,omitempty"`
	MaxToolCalls              *int     `json:"max_tool_calls,omitempty"`
	MaxIterations             *int     `json:"max_iterations,omitempty"`
	MaxToolResultBytes        *int     `json:"max_tool_result_bytes,omitempty"`
	MaxRetrievedContextBytes  *int     `json:"max_retrieved_context_bytes,omitempty"`
	MaxContextTokens          *int     `json:"max_context_tokens,omitempty"`
	MaxModelTokens            *int     `json:"max_model_tokens,omitempty"`
	Temperature               *float64 `json:"temperature,omitempty"`
	RequestTimeout            *string  `json:"request_timeout,omitempty"`
	Workspaces                []string `json:"workspaces,omitempty"`
	ChangeValidatorProfiles   []string `json:"change_validators,omitempty"`
}

func defaultOpsServerOptions() opsServerOptions {
	return opsServerOptions{
		Address:                   "127.0.0.1:8090",
		BaseURL:                   "http://127.0.0.1:8080/v1",
		ModelID:                   "akritas",
		UpstreamAPIKeyEnvironment: "OPENAI_API_KEY",
		APIKeyEnvironment:         "AKRITAS_API_KEY",
		SystemInstructionsPath:    akritasinstructions.DefaultPath,
		SkillsDirectory:           akritasskills.DefaultDirectory,
		ResponseLanguage:          defaultResponseLanguage,
		AuditLogPath:              "data/audit/akritas.jsonl",
		SearchTopK:                5,
		ResultRunes:               800,
		DefaultMaxTokens:          1024,
		MaxTokensLimit:            4096,
		MaxToolCalls:              8,
		MaxIterations:             12,
		MaxToolResultBytes:        2 * 1024 * 1024,
		MaxRetrievedContextBytes:  128 * 1024,
		MaxContextTokens:          40000,
		MaxModelTokens:            16384,
		Temperature:               0.2,
		RequestTimeout:            10 * time.Minute,
	}
}

func loadOpsServerOptions(
	arguments []string,
	lookupEnvironment func(string) (string, bool),
) (opsServerOptions, string, error) {
	if lookupEnvironment == nil {
		lookupEnvironment = os.LookupEnv
	}
	options := defaultOpsServerOptions()
	configPath, err := opsServerConfigPath(arguments, lookupEnvironment)
	if err != nil {
		return opsServerOptions{}, "", err
	}
	if configPath != "" {
		file, err := loadOpsServerConfigFile(configPath)
		if err != nil {
			return opsServerOptions{}, "", err
		}
		if err := applyOpsServerConfigFile(&options, file); err != nil {
			return opsServerOptions{}, "", err
		}
	}
	if err := applyOpsServerEnvironment(&options, lookupEnvironment); err != nil {
		return opsServerOptions{}, "", err
	}
	return options, configPath, nil
}

func opsServerConfigPath(
	arguments []string,
	lookupEnvironment func(string) (string, bool),
) (string, error) {
	path := ""
	if value, exists := lookupEnvironment(opsServerConfigEnvironment); exists {
		path = strings.TrimSpace(value)
	}
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if strings.HasPrefix(argument, "-config=") {
			path = strings.TrimSpace(strings.TrimPrefix(argument, "-config="))
			continue
		}
		if strings.HasPrefix(argument, "--config=") {
			path = strings.TrimSpace(strings.TrimPrefix(argument, "--config="))
			continue
		}
		if argument != "-config" && argument != "--config" {
			continue
		}
		if index+1 >= len(arguments) {
			return "", fmt.Errorf("%s requires a path", argument)
		}
		index++
		path = strings.TrimSpace(arguments[index])
	}
	return path, nil
}

func loadOpsServerConfigFile(path string) (opsServerConfigFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return opsServerConfigFile{}, fmt.Errorf("stat Akritas config %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return opsServerConfigFile{}, fmt.Errorf("Akritas config %q is not a regular file", path)
	}
	if info.Size() <= 0 || info.Size() > maximumOpsServerConfigBytes {
		return opsServerConfigFile{}, fmt.Errorf("Akritas config %q must contain 1..%d bytes", path, maximumOpsServerConfigBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return opsServerConfigFile{}, fmt.Errorf("read Akritas config %q: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var file opsServerConfigFile
	if err := decoder.Decode(&file); err != nil {
		return opsServerConfigFile{}, fmt.Errorf("decode Akritas config %q: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return opsServerConfigFile{}, fmt.Errorf("Akritas config %q contains multiple JSON values", path)
		}
		return opsServerConfigFile{}, fmt.Errorf("decode trailing Akritas config %q: %w", path, err)
	}
	if file.Version != opsServerConfigVersion {
		return opsServerConfigFile{}, fmt.Errorf("unsupported Akritas config version %d", file.Version)
	}
	return file, nil
}

func applyOpsServerConfigFile(options *opsServerOptions, file opsServerConfigFile) error {
	applyString := func(target *string, value *string) {
		if value != nil {
			*target = *value
		}
	}
	applyInt := func(target *int, value *int) {
		if value != nil {
			*target = *value
		}
	}
	applyString(&options.Address, file.Address)
	applyString(&options.BaseURL, file.BaseURL)
	applyString(&options.UpstreamModel, file.UpstreamModel)
	applyString(&options.ModelID, file.ModelID)
	applyString(&options.UpstreamAPIKeyEnvironment, file.UpstreamAPIKeyEnvironment)
	applyString(&options.APIKeyEnvironment, file.APIKeyEnvironment)
	applyString(&options.SystemInstructionsPath, file.SystemInstructionsPath)
	applyString(&options.SkillsDirectory, file.SkillsDirectory)
	applyString(&options.ResponseLanguage, file.ResponseLanguage)
	applyString(&options.RAGIndexPath, file.RAGIndexPath)
	applyString(&options.MCPConfigPath, file.MCPConfigPath)
	applyString(&options.WorkspaceConfigPath, file.WorkspaceConfigPath)
	applyString(&options.AuditLogPath, file.AuditLogPath)
	applyInt(&options.SearchTopK, file.SearchTopK)
	applyInt(&options.ResultRunes, file.ResultRunes)
	applyInt(&options.DefaultMaxTokens, file.DefaultMaxTokens)
	applyInt(&options.MaxTokensLimit, file.MaxTokensLimit)
	applyInt(&options.MaxToolCalls, file.MaxToolCalls)
	applyInt(&options.MaxIterations, file.MaxIterations)
	applyInt(&options.MaxToolResultBytes, file.MaxToolResultBytes)
	applyInt(&options.MaxRetrievedContextBytes, file.MaxRetrievedContextBytes)
	applyInt(&options.MaxContextTokens, file.MaxContextTokens)
	applyInt(&options.MaxModelTokens, file.MaxModelTokens)
	if file.Temperature != nil {
		options.Temperature = *file.Temperature
	}
	if file.RequestTimeout != nil {
		value, err := time.ParseDuration(strings.TrimSpace(*file.RequestTimeout))
		if err != nil {
			return fmt.Errorf("config request_timeout: %w", err)
		}
		options.RequestTimeout = value
	}
	if file.Workspaces != nil {
		options.Workspaces = append([]string(nil), file.Workspaces...)
	}
	if file.ChangeValidatorProfiles != nil {
		options.ChangeValidatorProfiles = append([]string(nil), file.ChangeValidatorProfiles...)
	}
	return nil
}

func applyOpsServerEnvironment(
	options *opsServerOptions,
	lookup func(string) (string, bool),
) error {
	if value, exists := lookup("AKRITAS_UPSTREAM_BASE_URL"); exists {
		options.BaseURL = value
	}
	stringsByName := map[string]*string{
		"AKRITAS_ADDRESS":              &options.Address,
		"AKRITAS_BASE_URL":             &options.BaseURL,
		"AKRITAS_UPSTREAM_MODEL":       &options.UpstreamModel,
		"AKRITAS_MODEL":                &options.ModelID,
		"AKRITAS_UPSTREAM_API_KEY_ENV": &options.UpstreamAPIKeyEnvironment,
		"AKRITAS_API_KEY_ENV":          &options.APIKeyEnvironment,
		"AKRITAS_SYSTEM_INSTRUCTIONS":  &options.SystemInstructionsPath,
		"AKRITAS_SKILLS_DIR":           &options.SkillsDirectory,
		"AKRITAS_RESPONSE_LANGUAGE":    &options.ResponseLanguage,
		"AKRITAS_RAG_INDEX":            &options.RAGIndexPath,
		"AKRITAS_MCP_CONFIG":           &options.MCPConfigPath,
		"AKRITAS_WORKSPACE_CONFIG":     &options.WorkspaceConfigPath,
		"AKRITAS_AUDIT_LOG":            &options.AuditLogPath,
	}
	for name, target := range stringsByName {
		if value, exists := lookup(name); exists {
			*target = value
		}
	}
	intsByName := map[string]*int{
		"AKRITAS_SEARCH_TOP_K":                &options.SearchTopK,
		"AKRITAS_RESULT_RUNES":                &options.ResultRunes,
		"AKRITAS_MAX_TOKENS":                  &options.DefaultMaxTokens,
		"AKRITAS_MAX_TOKENS_LIMIT":            &options.MaxTokensLimit,
		"AKRITAS_MAX_TOOL_CALLS":              &options.MaxToolCalls,
		"AKRITAS_MAX_ITERATIONS":              &options.MaxIterations,
		"AKRITAS_MAX_TOOL_RESULT_BYTES":       &options.MaxToolResultBytes,
		"AKRITAS_MAX_RETRIEVED_CONTEXT_BYTES": &options.MaxRetrievedContextBytes,
		"AKRITAS_MAX_CONTEXT_TOKENS":          &options.MaxContextTokens,
		"AKRITAS_MAX_MODEL_TOKENS":            &options.MaxModelTokens,
	}
	for name, target := range intsByName {
		value, exists := lookup(name)
		if !exists {
			continue
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("environment %s must be an integer: %w", name, err)
		}
		*target = parsed
	}
	if value, exists := lookup("AKRITAS_TEMPERATURE"); exists {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return fmt.Errorf("environment AKRITAS_TEMPERATURE must be a number: %w", err)
		}
		options.Temperature = parsed
	}
	if value, exists := lookup("AKRITAS_REQUEST_TIMEOUT"); exists {
		parsed, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("environment AKRITAS_REQUEST_TIMEOUT must be a duration: %w", err)
		}
		options.RequestTimeout = parsed
	}
	if value, exists := lookup("AKRITAS_WORKSPACES"); exists {
		parsed, err := parseOpsServerEnvironmentList(value)
		if err != nil {
			return fmt.Errorf("environment AKRITAS_WORKSPACES: %w", err)
		}
		options.Workspaces = parsed
	}
	if value, exists := lookup("AKRITAS_CHANGE_VALIDATORS"); exists {
		parsed, err := parseOpsServerEnvironmentList(value)
		if err != nil {
			return fmt.Errorf("environment AKRITAS_CHANGE_VALIDATORS: %w", err)
		}
		options.ChangeValidatorProfiles = parsed
	}
	return nil
}

func parseOpsServerEnvironmentList(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return []string{}, nil
	}
	if strings.HasPrefix(value, "[") {
		var values []string
		decoder := json.NewDecoder(strings.NewReader(value))
		if err := decoder.Decode(&values); err != nil {
			return nil, fmt.Errorf("decode JSON array: %w", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, fmt.Errorf("JSON array contains trailing data")
		}
		return normalizedOpsServerList(values)
	}
	return normalizedOpsServerList(strings.Split(value, ","))
}

func normalizedOpsServerList(values []string) ([]string, error) {
	normalized := make([]string, 0, len(values))
	for index, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("item %d is empty", index)
		}
		normalized = append(normalized, value)
	}
	return normalized, nil
}

type overridingRepeatedStringFlag struct {
	values *[]string
	set    bool
}

func (value *overridingRepeatedStringFlag) String() string {
	if value == nil || value.values == nil {
		return ""
	}
	return strings.Join(*value.values, ",")
}

func (value *overridingRepeatedStringFlag) Set(item string) error {
	if value == nil || value.values == nil {
		return fmt.Errorf("repeated flag destination is nil")
	}
	if !value.set {
		*value.values = nil
		value.set = true
	}
	*value.values = append(*value.values, item)
	return nil
}
