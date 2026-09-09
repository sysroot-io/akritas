package cli

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"akritas/internal/audit"
	akritasinstructions "akritas/internal/instructions"
	"akritas/internal/runbudget"
	akritasskills "akritas/internal/skills"
)

func runOpsServer(arguments []string) {
	options, configuredPath, err := loadOpsServerOptions(arguments, os.LookupEnv)
	if err != nil {
		panic(err)
	}
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := flags.String("config", configuredPath, "strict JSON service configuration; AKRITAS_CONFIG provides the default path")
	address := flags.String("address", options.Address, "HTTP listen address")
	baseURL := flags.String("base-url", options.BaseURL, "upstream OpenAI-compatible API ending in /v1")
	upstreamModel := flags.String("upstream-model", options.UpstreamModel, "upstream model ID; empty discovers the first model")
	modelID := flags.String("model", options.ModelID, "model ID exposed by this server")
	upstreamAPIKeyEnvironment := flags.String("upstream-api-key-env", options.UpstreamAPIKeyEnvironment, "environment variable containing upstream API key")
	apiKeyEnvironment := flags.String("api-key-env", options.APIKeyEnvironment, "environment variable containing inbound Bearer API key")
	systemInstructionsPath := flags.String("system-instructions", options.SystemInstructionsPath, "UTF-8 Markdown file with global model instructions")
	skillsDirectory := flags.String("skills-dir", options.SkillsDirectory, "directory of selectively loaded */SKILL.md operational skills; empty disables skills")
	responseLanguage := flags.String("response-language", options.ResponseLanguage, "BCP 47 language tag for model-generated prose; the Web UI remains English")
	ragIndexPath := flags.String("rag-index", options.RAGIndexPath, "local RAG index; empty disables RAG")
	mcpConfigPath := flags.String("mcp-config", options.MCPConfigPath, "MCP configuration; only authorized read tools are exposed")
	notificationsConfigPath := flags.String("notifications-config", options.NotificationsConfigPath, "incident notification and bot adapter configuration; empty disables both")
	alertSourcesConfigPath := flags.String("alert-sources-config", options.AlertSourcesConfigPath, "provider alert source configuration; empty disables provider-specific webhook routes")
	alertStorePath := flags.String("alert-store", options.AlertStorePath, "append-only JSONL alert, incident and investigation-job store; empty disables alert ingestion")
	publicURL := flags.String("public-url", options.PublicURL, "externally reachable Akritas base URL used in notification links")
	workspaceConfigPath := flags.String("workspace-config", options.WorkspaceConfigPath, "strict JSON workspace catalog; roots are relative to the config file")
	auditLogPath := flags.String("audit-log", options.AuditLogPath, "append-only JSONL run and audit log; empty disables persistence")
	searchTopK := flags.Int("search-top-k", options.SearchTopK, "default and maximum RAG results")
	resultRunes := flags.Int("result-runes", options.ResultRunes, "maximum runes in each RAG excerpt")
	defaultMaxTokens := flags.Int("max-tokens", options.DefaultMaxTokens, "default response token limit")
	maxTokensLimit := flags.Int("max-tokens-limit", options.MaxTokensLimit, "hard response token limit")
	maxToolCalls := flags.Int("max-tool-calls", options.MaxToolCalls, "maximum read-only tool calls per chat request")
	maxIterations := flags.Int("max-iterations", options.MaxIterations, "maximum upstream model calls per Run")
	maxToolResultBytes := flags.Int("max-tool-result-bytes", options.MaxToolResultBytes, "maximum aggregate tool result bytes per Run")
	maxRetrievedContextBytes := flags.Int("max-retrieved-context-bytes", options.MaxRetrievedContextBytes, "maximum retrieved context bytes per Run")
	maxContextTokens := flags.Int("max-context-tokens", options.MaxContextTokens, "conservative host-counted context token limit per Run")
	maxModelTokens := flags.Int("max-model-tokens", options.MaxModelTokens, "maximum aggregate model output tokens per Run")
	temperature := flags.Float64("temperature", options.Temperature, "default model sampling temperature")
	requestTimeout := flags.Duration("request-timeout", options.RequestTimeout, "upstream request and generation timeout")
	workspaceValues := append([]string(nil), options.Workspaces...)
	changeValidatorProfiles := append([]string(nil), options.ChangeValidatorProfiles...)
	workspaceFlag := overridingRepeatedStringFlag{values: &workspaceValues}
	validatorFlag := overridingRepeatedStringFlag{values: &changeValidatorProfiles}
	flags.Var(&workspaceFlag, "workspace", "allowed change workspace as name=directory; flag may be repeated")
	flags.Var(&validatorFlag, "change-validator", "opt-in executable validator: go-vet, go-test or yamllint; may be repeated")
	_ = flags.Parse(arguments)
	if strings.TrimSpace(*address) == "" || strings.TrimSpace(*modelID) == "" ||
		*searchTopK <= 0 || *resultRunes <= 0 || *defaultMaxTokens <= 0 ||
		*maxTokensLimit < *defaultMaxTokens || *maxToolCalls < 0 || *maxIterations <= 0 ||
		*maxToolResultBytes <= 0 || *maxRetrievedContextBytes <= 0 ||
		*maxRetrievedContextBytes > *maxToolResultBytes || *maxContextTokens <= 0 || *maxModelTokens <= 0 ||
		*temperature < 0 || *requestTimeout <= 0 {
		panic("serve requires valid addresses, names and generation limits")
	}
	if strings.TrimSpace(*alertSourcesConfigPath) != "" && strings.TrimSpace(*alertStorePath) == "" {
		panic("serve requires -alert-store when -alert-sources-config is enabled")
	}
	normalizedResponseLanguage, err := normalizeResponseLanguage(*responseLanguage)
	if err != nil {
		panic(err)
	}
	normalizedPublicURL, err := normalizeOpsPublicURL(*publicURL)
	if err != nil {
		panic(err)
	}
	systemInstructions, err := akritasinstructions.Load(*systemInstructionsPath)
	if err != nil {
		panic(err)
	}
	var skillCatalog *akritasskills.Catalog
	if strings.TrimSpace(*skillsDirectory) != "" {
		skillCatalog, err = akritasskills.Load(*skillsDirectory)
		if err != nil {
			panic(err)
		}
	}
	workspaces, err := parseOpsWorkspaces(workspaceValues)
	if err != nil {
		panic(err)
	}
	configuredWorkspaces, commonConfigValidators, err := loadOpsWorkspaceConfig(*workspaceConfigPath)
	if err != nil {
		panic(err)
	}
	if err := mergeOpsWorkspaces(workspaces, configuredWorkspaces); err != nil {
		panic(err)
	}
	if err := validateChangeValidatorProfiles(changeValidatorProfiles); err != nil {
		panic(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commonValidatorProfiles := sortedChangeValidatorProfiles(
		mergeChangeValidatorProfiles(commonConfigValidators, changeValidatorProfiles),
	)
	if err := preflightChangeValidators(ctx, commonValidatorProfiles, workspaces); err != nil {
		panic(err)
	}
	declaredValidatorProfiles := declaredChangeValidatorProfiles(commonValidatorProfiles, workspaces)
	if len(declaredValidatorProfiles) > 0 {
		goToolchain := ""
		if slicesContainAny(declaredValidatorProfiles, "go-vet", "go-test") {
			resolved, err := resolveChangeValidatorGoToolchain()
			if err != nil {
				panic(err)
			}
			goToolchain = " go_toolchain=" + resolved
		}
		fmt.Printf(
			"Validator preflight: passed profiles=%s workspaces=%d%s\n",
			strings.Join(declaredValidatorProfiles, ","), len(workspaces), goToolchain,
		)
	}
	registry := NewToolRegistry()
	policy := NamedToolPolicy{Allowed: make(map[string]bool)}
	if err := registerOpsInvestigationPlanTool(registry); err != nil {
		panic(err)
	}
	policy.Allowed[localInvestigationPlanToolName] = true
	if skillCatalog != nil {
		if err := registerOpsKnowledgeSkillTools(registry, skillCatalog); err != nil {
			panic(err)
		}
		policy.Allowed[localKnowledgeListSkillsName] = true
		policy.Allowed[localKnowledgeLoadSkillName] = true
	}
	var host *MCPHost
	ignoredMCPTools := 0
	if strings.TrimSpace(*mcpConfigPath) != "" {
		config, err := LoadMCPHostConfig(*mcpConfigPath)
		if err != nil {
			panic(err)
		}
		host, err = StartMCPHost(ctx, config)
		if err != nil {
			panic(err)
		}
		defer func() {
			if err := host.Close(); err != nil {
				fmt.Fprintln(os.Stderr, "close MCP host:", err)
			}
		}()
		ignoredMCPTools, err = registerReadOnlyTools(registry, policy, host.Registry, host.Policy)
		if err != nil {
			panic(err)
		}
	}
	if strings.TrimSpace(*ragIndexPath) != "" {
		index, err := LoadRAGIndex(*ragIndexPath)
		if err != nil {
			panic(err)
		}
		if err := RegisterRAGSearchTool(registry, index, RAGSearchToolOptions{
			DefaultTopK: *searchTopK, MaximumTopK: *searchTopK,
			MaximumRunes: *resultRunes,
		}); err != nil {
			panic(err)
		}
		policy.Allowed[localRAGSearchToolName] = true
	}
	var notificationDispatcher *NotificationDispatcher
	if strings.TrimSpace(*notificationsConfigPath) != "" {
		notificationDispatcher, err = LoadNotificationDispatcher(*notificationsConfigPath, os.LookupEnv)
		if err != nil {
			panic(err)
		}
	}
	var alertRegistry *AlertRegistry
	if strings.TrimSpace(*alertSourcesConfigPath) != "" {
		alertRegistry, err = LoadAlertRegistry(*alertSourcesConfigPath, os.LookupEnv)
		if err != nil {
			panic(err)
		}
	}

	upstreamAPIKey := ""
	if strings.TrimSpace(*upstreamAPIKeyEnvironment) != "" {
		upstreamAPIKey = os.Getenv(*upstreamAPIKeyEnvironment)
	}
	client, err := newOpenAIToolClient(
		*baseURL, *upstreamModel, upstreamAPIKey,
		&http.Client{Timeout: *requestTimeout},
	)
	if err != nil {
		panic(err)
	}
	if err := client.SetSystemInstructions(systemInstructions); err != nil {
		panic(err)
	}
	if err := client.DiscoverModel(ctx); err != nil {
		panic(err)
	}
	inboundAPIKey := ""
	if strings.TrimSpace(*apiKeyEnvironment) != "" {
		inboundAPIKey = os.Getenv(*apiKeyEnvironment)
	}
	server, err := newOpsServer(
		client, registry, policy, strings.TrimSpace(*modelID), inboundAPIKey,
		normalizedResponseLanguage,
		*defaultMaxTokens, *maxTokensLimit, *temperature, *maxToolCalls,
		*requestTimeout, workspaces,
	)
	if err != nil {
		panic(err)
	}
	if err := server.SetRunBudget(runbudget.Limits{
		MaxDuration: *requestTimeout, MaxIterations: *maxIterations,
		MaxToolCalls: *maxToolCalls, MaxToolResultBytes: *maxToolResultBytes,
		MaxRetrievedContextBytes: *maxRetrievedContextBytes,
		MaxContextTokens:         *maxContextTokens, MaxModelTokens: *maxModelTokens,
	}); err != nil {
		panic(err)
	}
	server.SetValidatorProfiles(commonValidatorProfiles)
	server.SetSkillCatalog(skillCatalog)
	server.SetNotificationDispatcher(notificationDispatcher)
	server.SetPublicURL(normalizedPublicURL)
	defer server.CloseBotGateway()
	if strings.TrimSpace(*auditLogPath) != "" {
		auditStore, err := audit.Open(*auditLogPath)
		if err != nil {
			panic(err)
		}
		defer func() {
			if err := auditStore.Close(); err != nil {
				fmt.Fprintln(os.Stderr, "close audit store:", err)
			}
		}()
		server.SetAuditStore(auditStore)
	}
	if strings.TrimSpace(*alertStorePath) != "" {
		alertStore, err := OpenAlertStore(*alertStorePath)
		if err != nil {
			panic(err)
		}
		defer func() {
			if err := alertStore.Close(); err != nil {
				fmt.Fprintln(os.Stderr, "close alert store:", err)
			}
		}()
		if err := server.SetAlertRuntime(alertRegistry, alertStore); err != nil {
			panic(err)
		}
		defer server.CloseAlertWorker()
	}
	httpServer := &http.Server{
		Addr: *address, Handler: server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      *requestTimeout + time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	fmt.Printf(
		"Akritas Web UI: http://%s model=%s upstream=%s config=%s response_language=%s system_instructions=%s skills_dir=%s skills=%d alert_sources=%d notifications=%d bots=%d tools=%d workspaces=%d approved_changes=true\n",
		*address, *modelID, client.Model, *configPath, normalizedResponseLanguage, *systemInstructionsPath,
		*skillsDirectory, skillCatalog.Len(), alertRegistry.Len(), notificationDispatcher.Len(), notificationDispatcher.BotReceiverCount(), len(registry.Definitions()), len(workspaces),
	)
	if ignoredMCPTools > 0 {
		fmt.Printf("Ignored non-read MCP tools: %d\n", ignoredMCPTools)
	}
	if inboundAPIKey == "" {
		fmt.Println("Authentication: disabled; keep the default loopback bind or use a trusted reverse proxy")
	} else {
		fmt.Printf("Authentication: Bearer token from %s is required for API endpoints\n", *apiKeyEnvironment)
	}
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		panic(err)
	}
}
