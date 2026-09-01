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
)

func runOpsServer(arguments []string) {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	address := flags.String("address", "127.0.0.1:8090", "HTTP listen address")
	baseURL := flags.String("base-url", "http://127.0.0.1:8080/v1", "upstream OpenAI-compatible API ending in /v1")
	upstreamModel := flags.String("upstream-model", "", "upstream model ID; empty discovers the first model")
	modelID := flags.String("model", "akritas", "model ID exposed by this server")
	upstreamAPIKeyEnvironment := flags.String("upstream-api-key-env", "OPENAI_API_KEY", "environment variable containing upstream API key")
	apiKeyEnvironment := flags.String("api-key-env", "AKRITAS_API_KEY", "environment variable containing inbound Bearer API key")
	systemInstructionsPath := flags.String("system-instructions", akritasinstructions.DefaultPath, "UTF-8 Markdown file with global model instructions")
	responseLanguage := flags.String("response-language", defaultResponseLanguage, "BCP 47 language tag for model-generated prose; the Web UI remains English")
	ragIndexPath := flags.String("rag-index", "", "local RAG index; empty disables RAG")
	mcpConfigPath := flags.String("mcp-config", "", "MCP configuration; only authorized read tools are exposed")
	workspaceConfigPath := flags.String("workspace-config", "", "strict JSON workspace catalog; roots are relative to the config file")
	auditLogPath := flags.String("audit-log", "data/audit/akritas.jsonl", "append-only JSONL run and audit log; empty disables persistence")
	searchTopK := flags.Int("search-top-k", 5, "default and maximum RAG results")
	resultRunes := flags.Int("result-runes", 800, "maximum runes in each RAG excerpt")
	defaultMaxTokens := flags.Int("max-tokens", 1024, "default response token limit")
	maxTokensLimit := flags.Int("max-tokens-limit", 4096, "hard response token limit")
	maxToolCalls := flags.Int("max-tool-calls", 8, "maximum read-only tool calls per chat request")
	maxIterations := flags.Int("max-iterations", 12, "maximum upstream model calls per Run")
	maxToolResultBytes := flags.Int("max-tool-result-bytes", 2*1024*1024, "maximum aggregate tool result bytes per Run")
	maxRetrievedContextBytes := flags.Int("max-retrieved-context-bytes", 128*1024, "maximum retrieved context bytes per Run")
	maxContextTokens := flags.Int("max-context-tokens", 40000, "conservative host-counted context token limit per Run")
	maxModelTokens := flags.Int("max-model-tokens", 16384, "maximum aggregate model output tokens per Run")
	temperature := flags.Float64("temperature", 0.2, "default model sampling temperature")
	requestTimeout := flags.Duration("request-timeout", 10*time.Minute, "upstream request and generation timeout")
	var workspaceValues repeatedStringFlag
	var changeValidatorProfiles repeatedStringFlag
	flags.Var(&workspaceValues, "workspace", "allowed change workspace as name=directory; flag may be repeated")
	flags.Var(&changeValidatorProfiles, "change-validator", "opt-in executable validator: go-vet, go-test or yamllint; may be repeated")
	_ = flags.Parse(arguments)
	if strings.TrimSpace(*address) == "" || strings.TrimSpace(*modelID) == "" ||
		*searchTopK <= 0 || *resultRunes <= 0 || *defaultMaxTokens <= 0 ||
		*maxTokensLimit < *defaultMaxTokens || *maxToolCalls < 0 || *maxIterations <= 0 ||
		*maxToolResultBytes <= 0 || *maxRetrievedContextBytes <= 0 ||
		*maxRetrievedContextBytes > *maxToolResultBytes || *maxContextTokens <= 0 || *maxModelTokens <= 0 ||
		*temperature < 0 || *requestTimeout <= 0 {
		panic("serve requires valid addresses, names and generation limits")
	}
	normalizedResponseLanguage, err := normalizeResponseLanguage(*responseLanguage)
	if err != nil {
		panic(err)
	}
	systemInstructions, err := akritasinstructions.Load(*systemInstructionsPath)
	if err != nil {
		panic(err)
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
	httpServer := &http.Server{
		Addr: *address, Handler: server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      *requestTimeout + time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	fmt.Printf(
		"Akritas Web UI: http://%s model=%s upstream=%s response_language=%s system_instructions=%s tools=%d workspaces=%d approved_changes=true\n",
		*address, *modelID, client.Model, normalizedResponseLanguage, *systemInstructionsPath, len(registry.Definitions()), len(workspaces),
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
