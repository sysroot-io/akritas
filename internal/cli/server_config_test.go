package cli

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadOpsServerOptionsUsesConfigThenEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "akritas.json")
	content := `{
  "version": 1,
  "address": "127.0.0.1:9000",
  "base_url": "https://config.example/v1",
  "skills_dir": "config-skills",
  "max_tool_calls": 3,
  "request_timeout": "45s",
  "workspaces": ["config=/srv/config"]
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{
		"AKRITAS_CONFIG":         path,
		"AKRITAS_ADDRESS":        "0.0.0.0:8090",
		"AKRITAS_MAX_TOOL_CALLS": "6",
		"AKRITAS_WORKSPACES":     `["api=/srv/api","ops=/srv/ops"]`,
	}
	options, selectedPath, err := loadOpsServerOptions(nil, func(name string) (string, bool) {
		value, exists := environment[name]
		return value, exists
	})
	if err != nil {
		t.Fatal(err)
	}
	if selectedPath != path || options.Address != "0.0.0.0:8090" ||
		options.BaseURL != "https://config.example/v1" || options.SkillsDirectory != "config-skills" ||
		options.MaxToolCalls != 6 || options.RequestTimeout != 45*time.Second ||
		len(options.Workspaces) != 2 || options.Workspaces[1] != "ops=/srv/ops" {
		t.Fatalf("unexpected options: path=%q options=%+v", selectedPath, options)
	}
}

func TestOpsServerConfigPathCLIOverridesEnvironment(t *testing.T) {
	path, err := opsServerConfigPath([]string{"-config", "cli.json"}, func(name string) (string, bool) {
		if name == opsServerConfigEnvironment {
			return "environment.json", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "cli.json" {
		t.Fatalf("config path = %q", path)
	}
}

func TestOpsServerRepeatedCLIFlagOverridesConfiguredList(t *testing.T) {
	values := []string{"config=/srv/config"}
	override := overridingRepeatedStringFlag{values: &values}
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	flags.Var(&override, "workspace", "workspace")
	if err := flags.Parse([]string{"-workspace", "api=/srv/api", "-workspace", "ops=/srv/ops"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(values, ",") != "api=/srv/api,ops=/srv/ops" {
		t.Fatalf("values = %+v", values)
	}
}

func TestLoadOpsServerConfigRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "akritas.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOpsServerConfigFile(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestShippedOpsServerConfigurationsAreValid(t *testing.T) {
	for _, path := range []string{
		"../../configs/akritas/server.example.json",
		"../../configs/akritas/server.container.json",
	} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if _, err := loadOpsServerConfigFile(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestApplyOpsServerEnvironmentMapsEveryOption(t *testing.T) {
	environment := map[string]string{
		"AKRITAS_UPSTREAM_BASE_URL":           "https://legacy.example/v1",
		"AKRITAS_ADDRESS":                     "0.0.0.0:9000",
		"AKRITAS_BASE_URL":                    "https://current.example/v1",
		"AKRITAS_UPSTREAM_MODEL":              "upstream",
		"AKRITAS_MODEL":                       "public",
		"AKRITAS_UPSTREAM_API_KEY_ENV":        "UPSTREAM_SECRET",
		"AKRITAS_API_KEY_ENV":                 "INBOUND_SECRET",
		"AKRITAS_SYSTEM_INSTRUCTIONS":         "/etc/akritas/SYSTEM.md",
		"AKRITAS_SKILLS_DIR":                  "/etc/akritas/skills",
		"AKRITAS_RESPONSE_LANGUAGE":           "fr",
		"AKRITAS_RAG_INDEX":                   "/data/index.tgr",
		"AKRITAS_MCP_CONFIG":                  "/etc/akritas/mcp.json",
		"AKRITAS_NOTIFICATIONS_CONFIG":        "/etc/akritas/notifications.json",
		"AKRITAS_WORKSPACE_CONFIG":            "/etc/akritas/workspaces.json",
		"AKRITAS_AUDIT_LOG":                   "/data/audit.jsonl",
		"AKRITAS_SEARCH_TOP_K":                "7",
		"AKRITAS_RESULT_RUNES":                "900",
		"AKRITAS_MAX_TOKENS":                  "1200",
		"AKRITAS_MAX_TOKENS_LIMIT":            "4800",
		"AKRITAS_MAX_TOOL_CALLS":              "9",
		"AKRITAS_MAX_ITERATIONS":              "14",
		"AKRITAS_MAX_TOOL_RESULT_BYTES":       "3000000",
		"AKRITAS_MAX_RETRIEVED_CONTEXT_BYTES": "150000",
		"AKRITAS_MAX_CONTEXT_TOKENS":          "50000",
		"AKRITAS_MAX_MODEL_TOKENS":            "20000",
		"AKRITAS_TEMPERATURE":                 "0.4",
		"AKRITAS_REQUEST_TIMEOUT":             "75s",
		"AKRITAS_WORKSPACES":                  `["api=/srv/api","ops=/srv/ops"]`,
		"AKRITAS_CHANGE_VALIDATORS":           "go-vet,yamllint",
	}
	options := defaultOpsServerOptions()
	if err := applyOpsServerEnvironment(&options, func(name string) (string, bool) {
		value, exists := environment[name]
		return value, exists
	}); err != nil {
		t.Fatal(err)
	}
	expected := opsServerOptions{
		Address:                   "0.0.0.0:9000",
		BaseURL:                   "https://current.example/v1",
		UpstreamModel:             "upstream",
		ModelID:                   "public",
		UpstreamAPIKeyEnvironment: "UPSTREAM_SECRET",
		APIKeyEnvironment:         "INBOUND_SECRET",
		SystemInstructionsPath:    "/etc/akritas/SYSTEM.md",
		SkillsDirectory:           "/etc/akritas/skills",
		ResponseLanguage:          "fr",
		RAGIndexPath:              "/data/index.tgr",
		MCPConfigPath:             "/etc/akritas/mcp.json",
		NotificationsConfigPath:   "/etc/akritas/notifications.json",
		WorkspaceConfigPath:       "/etc/akritas/workspaces.json",
		AuditLogPath:              "/data/audit.jsonl",
		SearchTopK:                7,
		ResultRunes:               900,
		DefaultMaxTokens:          1200,
		MaxTokensLimit:            4800,
		MaxToolCalls:              9,
		MaxIterations:             14,
		MaxToolResultBytes:        3000000,
		MaxRetrievedContextBytes:  150000,
		MaxContextTokens:          50000,
		MaxModelTokens:            20000,
		Temperature:               0.4,
		RequestTimeout:            75 * time.Second,
		Workspaces:                []string{"api=/srv/api", "ops=/srv/ops"},
		ChangeValidatorProfiles:   []string{"go-vet", "yamllint"},
	}
	if !reflect.DeepEqual(options, expected) {
		t.Fatalf("options = %+v, want %+v", options, expected)
	}
}
