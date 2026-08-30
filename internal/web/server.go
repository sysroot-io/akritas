package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"akritas/internal/audit"
)

const (
	maximumOpsAPIRequestBytes = 1024 * 1024
	maximumOpsChatMessages    = 64
	maximumOpsChatBytes       = 256 * 1024
)

const opsServerSystemPrompt = `Ты Akritas, ассистент для операционных задач. Отвечай по-русски, если пользователь не попросил иначе. Host автоматически ищет последний запрос пользователя в подключённой локальной базе знаний и передаёт результат как local.rag.search TOOL_RESULT. Если результат релевантен, используй его содержание, включая инструкции и списки, и называй document_id. Если результатов нет или они не отвечают на вопрос, явно сообщи об этом и отдели общие рекомендации от локальных данных. Host также выполняет отдельное capability planning и передаёт local.capability.report_missing TOOL_RESULT. Каждый gap из него означает, что соответствующий шаг НЕ выполнен и подходящего инструмента НЕТ: не называй такие шаги доступными или выполненными. Различай «шаги найденного документа», «фактически выполненные проверки» и «невыполненные шаги». Фактически выполненной считается только проверка с успешным ToolResult диагностического инструмента; RAG search и capability reporting проверками не являются. Если диагностических ToolResult нет, прямо скажи, что проверки не выполнялись. Write и dangerous действия в этом Web runtime недоступны.`

//go:embed ops_web/index.html
var opsWebFiles embed.FS

type opsServer struct {
	client            *openAIToolClient
	registry          *ToolRegistry
	policy            NamedToolPolicy
	modelID           string
	apiKey            string
	defaultMaxTokens  int
	maxTokensLimit    int
	defaultTemp       float64
	maxToolCalls      int
	requestTimeout    time.Duration
	workspaces        map[string]opsWorkspace
	requestCounter    atomic.Uint64
	generationSlot    chan struct{}
	pendingMu         sync.Mutex
	pendingChanges    map[string]pendingChange
	validatorProfiles []string
	auditStore        *audit.Store
}

type opsChatAPIRequest struct {
	Messages    []openAIChatMessage `json:"messages"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
	Temperature *float64            `json:"temperature,omitempty"`
	Debug       bool                `json:"debug,omitempty"`
}

type opsToolActivity struct {
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Status    string          `json:"status"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Result    *ToolResult     `json:"result,omitempty"`
}

type opsChatAPIResponse struct {
	RunID          string             `json:"run_id,omitempty"`
	Model          string             `json:"model"`
	Answer         string             `json:"answer"`
	Activity       []opsToolActivity  `json:"activity"`
	CapabilityGaps []opsCapabilityGap `json:"capability_gaps"`
}

type opsChangeAPIRequest struct {
	Workspace   string   `json:"workspace"`
	Request     string   `json:"request"`
	Files       []string `json:"files"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type opsChangeAPIResponse struct {
	RunID     string                 `json:"run_id,omitempty"`
	Model     string                 `json:"model"`
	Workspace string                 `json:"workspace"`
	Answer    string                 `json:"answer"`
	Result    changeSimulationResult `json:"result"`
	Approval  *changeApproval        `json:"approval,omitempty"`
}

type opsApplyChangeRequest struct {
	Confirm bool `json:"confirm"`
}

func newOpsServer(
	client *openAIToolClient,
	registry *ToolRegistry,
	policy NamedToolPolicy,
	modelID string,
	apiKey string,
	defaultMaxTokens int,
	maxTokensLimit int,
	defaultTemperature float64,
	maxToolCalls int,
	requestTimeout time.Duration,
	workspaces map[string]opsWorkspace,
) (*opsServer, error) {
	if client == nil || registry == nil || strings.TrimSpace(modelID) == "" ||
		defaultMaxTokens <= 0 || maxTokensLimit < defaultMaxTokens ||
		defaultTemperature < 0 || maxToolCalls < 0 || requestTimeout <= 0 {
		return nil, fmt.Errorf("serve requires a client, registry and valid limits")
	}
	if policy.Allowed == nil {
		policy.Allowed = make(map[string]bool)
	}
	return &opsServer{
		client: client, registry: registry, policy: policy,
		modelID: modelID, apiKey: apiKey,
		defaultMaxTokens: defaultMaxTokens, maxTokensLimit: maxTokensLimit,
		defaultTemp: defaultTemperature, maxToolCalls: maxToolCalls,
		requestTimeout: requestTimeout, workspaces: workspaces,
		generationSlot: make(chan struct{}, 1),
		pendingChanges: make(map[string]pendingChange),
	}, nil
}

func (server *opsServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", server.handleIndex)
	mux.HandleFunc("GET /health", server.handleHealth)
	mux.Handle("GET /api/v1/workspaces", server.authenticate(http.HandlerFunc(server.handleWorkspaces)))
	mux.Handle("GET /api/v1/runs", server.authenticate(http.HandlerFunc(server.handleRuns)))
	mux.Handle("GET /api/v1/runs/{id}", server.authenticate(http.HandlerFunc(server.handleRun)))
	mux.Handle("POST /api/v1/chat", server.authenticate(http.HandlerFunc(server.handleChatAPI)))
	mux.Handle("POST /api/v1/alertmanager/webhook", server.authenticate(http.HandlerFunc(server.handleAlertmanagerWebhook)))
	mux.Handle("POST /api/v1/change/simulations", server.authenticate(http.HandlerFunc(server.handleChangeAPI)))
	mux.Handle("POST /api/v1/change/simulations/{id}/apply", server.authenticate(http.HandlerFunc(server.handleApplyChangeAPI)))
	mux.Handle("GET /v1/models", server.authenticate(http.HandlerFunc(server.handleModels)))
	mux.Handle("GET /v1/models/{model}", server.authenticate(http.HandlerFunc(server.handleModel)))
	mux.Handle("POST /v1/chat/completions", server.authenticate(http.HandlerFunc(server.handleOpenAIChat)))
	return server.logRejectedResponses(mux)
}

type opsStatusWriter struct {
	http.ResponseWriter
	status int
}

func (writer *opsStatusWriter) WriteHeader(status int) {
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *opsStatusWriter) Write(value []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	return writer.ResponseWriter.Write(value)
}

func (writer *opsStatusWriter) Flush() {
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (server *opsServer) logRejectedResponses(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		tracked := &opsStatusWriter{ResponseWriter: writer}
		started := time.Now()
		next.ServeHTTP(tracked, request)
		if tracked.status >= http.StatusBadRequest {
			log.Printf(
				"level=warn component=akritas method=%s path=%q status=%d remote=%q duration_ms=%d",
				request.Method, request.URL.Path, tracked.status, request.RemoteAddr,
				time.Since(started).Milliseconds(),
			)
		}
	})
}

func (server *opsServer) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if server.apiKey != "" && request.Header.Get("Authorization") != "Bearer "+server.apiKey {
			writeOpenAIError(writer, http.StatusUnauthorized, "invalid API key", nil, "invalid_api_key")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (server *opsServer) handleIndex(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(writer, request)
		return
	}
	page, err := opsWebFiles.ReadFile("ops_web/index.html")
	if err != nil {
		http.Error(writer, "embedded UI is unavailable", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write(page)
}

func (server *opsServer) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"status": "ok", "model": server.modelID,
		"tools":      len(server.registry.Definitions()),
		"workspaces": len(server.workspaces),
	})
}

func (server *opsServer) modelObject() openAIModelObject {
	return openAIModelObject{
		ID: server.modelID, Object: "model", Created: time.Now().Unix(),
		OwnedBy: "akritas",
	}
}

func (server *opsServer) handleModels(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, openAIModelList{
		Object: "list", Data: []openAIModelObject{server.modelObject()},
	})
}

func (server *opsServer) handleModel(writer http.ResponseWriter, request *http.Request) {
	if request.PathValue("model") != server.modelID {
		writeOpenAIError(writer, http.StatusNotFound, "model not found", openAIStringPointer("model"), "model_not_found")
		return
	}
	writeJSON(writer, http.StatusOK, server.modelObject())
}

func (server *opsServer) handleWorkspaces(writer http.ResponseWriter, _ *http.Request) {
	names := make([]string, 0, len(server.workspaces))
	for name := range server.workspaces {
		names = append(names, name)
	}
	sort.Strings(names)
	writeJSON(writer, http.StatusOK, map[string]any{"workspaces": names})
}

func (server *opsServer) handleRuns(writer http.ResponseWriter, request *http.Request) {
	if server.auditStore == nil {
		writeOpsError(writer, http.StatusServiceUnavailable, fmt.Errorf("audit store is disabled"))
		return
	}
	limit := 100
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000 {
			writeOpsError(writer, http.StatusBadRequest, fmt.Errorf("limit must be between 1 and 1000"))
			return
		}
		limit = parsed
	}
	writeJSON(writer, http.StatusOK, map[string]any{"runs": server.auditStore.List(limit)})
}

func (server *opsServer) handleRun(writer http.ResponseWriter, request *http.Request) {
	if server.auditStore == nil {
		writeOpsError(writer, http.StatusServiceUnavailable, fmt.Errorf("audit store is disabled"))
		return
	}
	run, exists := server.auditStore.Get(strings.TrimSpace(request.PathValue("id")))
	if !exists {
		writeOpsError(writer, http.StatusNotFound, fmt.Errorf("run not found"))
		return
	}
	writeJSON(writer, http.StatusOK, run)
}

func (server *opsServer) handleChatAPI(writer http.ResponseWriter, request *http.Request) {
	var input opsChatAPIRequest
	if err := decodeOpsRequest(writer, request, &input); err != nil {
		writeOpsError(writer, http.StatusBadRequest, err)
		return
	}
	maxTokens, temperature, err := server.resolveGenerationLimits(input.MaxTokens, input.Temperature)
	if err != nil {
		writeOpsError(writer, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), server.requestTimeout)
	defer cancel()
	auditRun := server.beginAuditRun("chat", "", nil)
	defer auditRun.failIfRunning("request_incomplete")
	result, err := server.completeChat(ctx, input.Messages, maxTokens, temperature)
	if err != nil {
		writeOpsError(writer, http.StatusBadGateway, err)
		return
	}
	auditRun.addToolEvents(result)
	auditRun.succeed(map[string]string{"tool_calls": strconv.Itoa(len(result.Calls))})
	writeJSON(writer, http.StatusOK, opsChatAPIResponse{
		RunID: auditRun.id(), Model: server.modelID, Answer: result.Answer,
		Activity:       buildOpsToolActivity(result, input.Debug),
		CapabilityGaps: buildOpsCapabilityGaps(result),
	})
}

func (server *opsServer) handleChangeAPI(writer http.ResponseWriter, request *http.Request) {
	var input opsChangeAPIRequest
	if err := decodeOpsRequest(writer, request, &input); err != nil {
		writeOpsError(writer, http.StatusBadRequest, err)
		return
	}
	workspace, exists := server.workspaces[strings.TrimSpace(input.Workspace)]
	if !exists {
		writeOpsError(writer, http.StatusBadRequest, fmt.Errorf("unknown workspace %q", input.Workspace))
		return
	}
	maxTokens, temperature, err := server.resolveGenerationLimits(input.MaxTokens, input.Temperature)
	if err != nil {
		writeOpsError(writer, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), server.requestTimeout)
	defer cancel()
	auditRun := server.beginAuditRun("change_preview", workspace.Name, nil)
	defer auditRun.failIfRunning("request_incomplete")
	if err := server.acquireGeneration(ctx); err != nil {
		writeOpsError(writer, http.StatusServiceUnavailable, err)
		return
	}
	defer server.releaseGeneration()
	var snapshot changeSimulationSnapshot
	if len(input.Files) == 0 {
		if strings.TrimSpace(input.Request) == "" {
			writeOpsError(writer, http.StatusBadRequest, fmt.Errorf("change simulation requires an inline request"))
			return
		}
		snapshot, err = discoverChangeSimulationSnapshot(
			ctx, server.client, workspace.Root, "<inline>", input.Request, maxTokens, temperature,
		)
	} else {
		snapshot, err = loadChangeSimulationInlineSnapshot(workspace.Root, input.Request, input.Files)
	}
	if err != nil {
		writeOpsError(writer, http.StatusBadRequest, err)
		return
	}
	validatorProfiles := effectiveWorkspaceValidatorProfiles(server.validatorProfiles, workspace)
	result, err := runChangeSimulationValidated(
		ctx, server.client, snapshot, maxTokens, temperature,
		workspace.Root, validatorProfiles,
	)
	if err != nil {
		var failure *changeSimulationFailure
		if errors.As(err, &failure) {
			logChangeAttemptDiagnostics(workspace.Name, failure.Diagnostics)
			writeJSON(writer, http.StatusBadGateway, map[string]any{"error": map[string]any{
				"message": failure.Error(), "details": failure.Diagnostics,
			}})
		} else {
			writeOpsError(writer, http.StatusBadGateway, err)
		}
		return
	}
	logChangeAttemptDiagnostics(workspace.Name, result.RejectedAttempts)
	logModelProposedChecks(workspace.Name, result.Attempts, result.Checks)
	for _, warning := range result.Warnings {
		log.Printf("level=warn component=akritas workspace=%q change_warning=%q", workspace.Name, warning)
	}
	response := opsChangeAPIResponse{
		Model: server.modelID, Workspace: workspace.Name,
		Answer: formatChangeSimulationResult(result), Result: result,
	}
	if len(result.ChangedFiles) > 0 {
		pending, pendingErr := newPendingChange(workspace, result, time.Now())
		if pendingErr != nil {
			writeOpsError(writer, http.StatusInternalServerError, pendingErr)
			return
		}
		server.storePendingChange(pending)
		response.Approval = &changeApproval{ID: pending.ID, Status: "pending", ExpiresAt: pending.ExpiresAt}
	}
	auditRun.addEvent("proposal", "accepted", map[string]string{
		"changed_files":     strconv.Itoa(len(result.ChangedFiles)),
		"approval_required": strconv.FormatBool(len(result.ChangedFiles) > 0),
	})
	auditRun.succeed(map[string]string{"changed_files": strconv.Itoa(len(result.ChangedFiles))})
	response.RunID = auditRun.id()
	writeJSON(writer, http.StatusOK, response)
}

func effectiveWorkspaceValidatorProfiles(common []string, workspace opsWorkspace) []string {
	if workspace.ReplaceValidators {
		return append([]string(nil), workspace.ValidatorProfiles...)
	}
	return mergeChangeValidatorProfiles(common, workspace.ValidatorProfiles)
}

func logChangeAttemptDiagnostics(workspace string, diagnostics []changeSimulationAttemptDiagnostic) {
	for _, diagnostic := range diagnostics {
		log.Printf(
			"level=warn component=akritas workspace=%q change_attempt=%d stage=%q finish_reason=%q completion_tokens=%d tool=%q argument_bytes=%d error=%q",
			workspace, diagnostic.Attempt, diagnostic.Stage, diagnostic.FinishReason,
			diagnostic.CompletionTokens, diagnostic.ToolName,
			diagnostic.ArgumentBytes, diagnostic.Error,
		)
		logModelProposedChecks(workspace, diagnostic.Attempt, diagnostic.ProposedChecks)
	}
}

func logModelProposedChecks(workspace string, attempt int, checks []string) {
	for index, check := range checks {
		log.Printf(
			"level=info component=akritas workspace=%q change_attempt=%d model_check_index=%d model_check_status=not_executed model_check=%q",
			workspace, attempt, index, previewOpsModelCheck(check),
		)
	}
}

func previewOpsModelCheck(check string) string {
	const maximumRunes = 512
	check = strings.TrimSpace(check)
	runes := []rune(check)
	if len(runes) <= maximumRunes {
		return check
	}
	return string(runes[:maximumRunes]) + "... <truncated by Akritas>"
}

func (server *opsServer) storePendingChange(change pendingChange) {
	server.pendingMu.Lock()
	defer server.pendingMu.Unlock()
	now := time.Now()
	for id, candidate := range server.pendingChanges {
		if !candidate.ExpiresAt.After(now) {
			delete(server.pendingChanges, id)
		}
	}
	if len(server.pendingChanges) >= maximumPendingChanges {
		var oldestID string
		var oldest time.Time
		for id, candidate := range server.pendingChanges {
			if oldestID == "" || candidate.CreatedAt.Before(oldest) {
				oldestID, oldest = id, candidate.CreatedAt
			}
		}
		delete(server.pendingChanges, oldestID)
	}
	server.pendingChanges[change.ID] = change
}

func (server *opsServer) takePendingChange(id string) (pendingChange, error) {
	server.pendingMu.Lock()
	defer server.pendingMu.Unlock()
	change, exists := server.pendingChanges[id]
	if !exists {
		return pendingChange{}, fmt.Errorf("pending change not found or already used")
	}
	delete(server.pendingChanges, id)
	if !change.ExpiresAt.After(time.Now()) {
		return pendingChange{}, fmt.Errorf("pending change expired")
	}
	return change, nil
}

func (server *opsServer) handleApplyChangeAPI(writer http.ResponseWriter, request *http.Request) {
	var input opsApplyChangeRequest
	if err := decodeOpsRequest(writer, request, &input); err != nil {
		writeOpsError(writer, http.StatusBadRequest, err)
		return
	}
	if !input.Confirm {
		writeOpsError(writer, http.StatusBadRequest, fmt.Errorf("explicit confirm=true is required"))
		return
	}
	change, err := server.takePendingChange(strings.TrimSpace(request.PathValue("id")))
	if err != nil {
		writeOpsError(writer, http.StatusNotFound, err)
		return
	}
	auditRun := server.beginAuditRun("change_apply", change.Workspace.Name, nil)
	defer auditRun.failIfRunning("request_incomplete")
	paths, err := applyPendingChange(change)
	if err != nil {
		writeOpsError(writer, http.StatusConflict, err)
		return
	}
	auditRun.addEvent("workspace_apply", "succeeded", map[string]string{"changed_files": strconv.Itoa(len(paths))})
	auditRun.succeed(map[string]string{"changed_files": strconv.Itoa(len(paths))})
	writeJSON(writer, http.StatusOK, map[string]any{
		"run_id": auditRun.id(), "status": "applied", "workspace": change.Workspace.Name, "changed_files": paths,
	})
}

func (server *opsServer) handleOpenAIChat(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, maximumOpsAPIRequestBytes)
	decoder := json.NewDecoder(request.Body)
	var input openAIChatCompletionRequest
	if err := decoder.Decode(&input); err != nil {
		writeOpenAIError(writer, http.StatusBadRequest, "invalid JSON request: "+err.Error(), nil, "invalid_json")
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeOpenAIError(writer, http.StatusBadRequest, err.Error(), nil, "invalid_json")
		return
	}
	if input.Model != server.modelID {
		writeOpenAIError(writer, http.StatusBadRequest, "unknown model", openAIStringPointer("model"), "model_not_found")
		return
	}
	if input.N != nil && *input.N != 1 {
		writeOpenAIError(writer, http.StatusBadRequest, "only n=1 is supported", openAIStringPointer("n"), "invalid_request")
		return
	}
	if input.TopP != nil && *input.TopP != 1 {
		writeOpenAIError(writer, http.StatusBadRequest, "top_p is not supported; omit it or use 1", openAIStringPointer("top_p"), "invalid_request")
		return
	}
	if input.Seed != nil {
		writeOpenAIError(writer, http.StatusBadRequest, "seed is not supported", openAIStringPointer("seed"), "invalid_request")
		return
	}
	if input.MaxTokens != nil && input.MaxCompletionTokens != nil {
		writeOpenAIError(writer, http.StatusBadRequest, "use either max_tokens or max_completion_tokens", openAIStringPointer("max_tokens"), "invalid_request")
		return
	}
	requestedTokens := 0
	if input.MaxTokens != nil {
		requestedTokens = *input.MaxTokens
	} else if input.MaxCompletionTokens != nil {
		requestedTokens = *input.MaxCompletionTokens
	}
	ctx, cancel := context.WithTimeout(request.Context(), server.requestTimeout)
	defer cancel()
	auditRun := server.beginAuditRun("openai_chat", "", nil)
	defer auditRun.failIfRunning("request_incomplete")
	result, err := server.completeOpenAIInput(ctx, input.Messages, requestedTokens, input.Temperature)
	if err != nil {
		writeOpenAIError(writer, http.StatusBadGateway, err.Error(), nil, "generation_error")
		return
	}
	auditRun.addToolEvents(result)
	auditRun.succeed(map[string]string{"tool_calls": strconv.Itoa(len(result.Calls))})
	if auditRun.id() != "" {
		writer.Header().Set("X-Akritas-Run-ID", auditRun.id())
	}
	id := fmt.Sprintf("chatcmpl-akritas-%d", server.requestCounter.Add(1))
	created := time.Now().Unix()
	if input.Stream {
		writeOpsBufferedStream(writer, id, created, server.modelID, result.Answer)
		return
	}
	writeJSON(writer, http.StatusOK, openAIChatCompletionResponse{
		ID: id, Object: "chat.completion", Created: created, Model: server.modelID,
		Choices: []openAICompletionChoice{{
			Index: 0, Message: openAIChatMessage{Role: "assistant", Content: result.Answer},
			FinishReason: "stop",
		}},
	})
}

func (server *opsServer) completeOpenAIInput(
	ctx context.Context,
	messages []openAIChatMessage,
	requestedTokens int,
	temperature *float64,
) (openAIToolLoopResult, error) {
	maxTokens, resolvedTemperature, err := server.resolveGenerationLimits(requestedTokens, temperature)
	if err != nil {
		return openAIToolLoopResult{}, err
	}
	return server.completeChat(ctx, messages, maxTokens, resolvedTemperature)
}

func (server *opsServer) completeChat(
	ctx context.Context,
	messages []openAIChatMessage,
	maxTokens int,
	temperature float64,
) (openAIToolLoopResult, error) {
	history, err := buildOpsChatHistory(messages)
	if err != nil {
		return openAIToolLoopResult{}, err
	}
	if err := server.acquireGeneration(ctx); err != nil {
		return openAIToolLoopResult{}, err
	}
	defer server.releaseGeneration()
	prefetchedCall, prefetchedResult, history, err := server.prefetchRAG(ctx, history, messages)
	if err != nil {
		return openAIToolLoopResult{}, err
	}
	initialCalls := make([]ToolCall, 0, 2)
	initialResults := make([]ToolResult, 0, 2)
	if prefetchedCall != nil {
		initialCalls = append(initialCalls, *prefetchedCall)
		initialResults = append(initialResults, *prefetchedResult)
	}
	if len(initialCalls) < server.maxToolCalls {
		gapCall, gapResult, plannedHistory, planErr := server.planCapabilityGaps(
			ctx, history, messages, prefetchedResult,
		)
		if planErr != nil {
			return openAIToolLoopResult{}, planErr
		}
		history = plannedHistory
		if gapCall != nil {
			initialCalls = append(initialCalls, *gapCall)
			initialResults = append(initialResults, *gapResult)
		}
	}
	executionRegistry, err := opsExecutionToolRegistry(server.registry)
	if err != nil {
		return openAIToolLoopResult{}, err
	}
	remainingCalls := server.maxToolCalls - len(initialCalls)
	result, err := runOpenAIToolLoop(
		ctx, server.client, history, executionRegistry, server.policy,
		remainingCalls, maxTokens, temperature,
	)
	if err != nil {
		return openAIToolLoopResult{}, err
	}
	result.Calls = append(initialCalls, result.Calls...)
	result.Results = append(initialResults, result.Results...)
	return result, nil
}

func (server *opsServer) prefetchRAG(
	ctx context.Context,
	history []openAIToolMessage,
	messages []openAIChatMessage,
) (*ToolCall, *ToolResult, []openAIToolMessage, error) {
	if server.maxToolCalls == 0 || !server.policy.Allowed[localRAGSearchToolName] ||
		!opsRegistryHasTool(server.registry, localRAGSearchToolName) {
		return nil, nil, history, nil
	}
	query := strings.TrimSpace(messages[len(messages)-1].Content)
	arguments, err := json.Marshal(ragSearchToolArguments{Query: query})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encode automatic RAG query: %w", err)
	}
	call := ToolCall{
		ID:        "call_akritas_rag_prefetch",
		Name:      localRAGSearchToolName,
		Arguments: arguments,
	}
	result := server.registry.Execute(ctx, call, server.policy)
	serializedResult, err := json.Marshal(result)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encode automatic RAG result: %w", err)
	}
	alias := openAIToolAlias(localRAGSearchToolName)
	externalCall := openAIToolCall{
		ID: call.ID, Type: "function",
		Function: openAIToolFunction{Name: alias, Arguments: string(arguments)},
	}
	history = append(history,
		openAIToolMessage{Role: "assistant", ToolCalls: []openAIToolCall{externalCall}},
		openAIToolMessage{
			Role: "tool", Content: openAIStringPointer(string(serializedResult)),
			Name: alias, ToolCallID: call.ID,
		},
	)
	return &call, &result, history, nil
}

func opsRegistryHasTool(registry *ToolRegistry, name string) bool {
	for _, definition := range registry.Definitions() {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func (server *opsServer) planCapabilityGaps(
	ctx context.Context,
	history []openAIToolMessage,
	messages []openAIChatMessage,
	ragResult *ToolResult,
) (*ToolCall, *ToolResult, []openAIToolMessage, error) {
	var reportDefinition *ToolDefinition
	executionCatalog := make([]map[string]string, 0)
	for _, definition := range server.registry.Definitions() {
		switch definition.Name {
		case localCapabilityGapToolName:
			copyDefinition := definition
			reportDefinition = &copyDefinition
		case localRAGSearchToolName:
		default:
			executionCatalog = append(executionCatalog, map[string]string{
				"name": definition.Name, "description": definition.Description,
			})
		}
	}
	if reportDefinition == nil || !server.policy.Allowed[localCapabilityGapToolName] {
		return nil, nil, history, nil
	}
	var knowledge any
	if ragResult != nil {
		knowledge = ragResult
	}
	planningInput, err := json.Marshal(map[string]any{
		"request":                   strings.TrimSpace(messages[len(messages)-1].Content),
		"knowledge_search_result":   knowledge,
		"available_execution_tools": executionCatalog,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encode capability planning input: %w", err)
	}
	systemPrompt := `Ты capability planner. Проанализируй запрос и найденный локальный документ. Сопоставь каждый требуемый шаг только с available_execution_tools. RAG search и reporting tool не выполняют диагностику. Обязательно вызови единственный доступный reporting tool ровно один раз. В gaps перечисли каждый шаг, для которого нет подходящего execution tool. Поле capability должно быть предлагаемым стабильным API-style идентификатором с точками, например metrics.query_range, logs.search или kubernetes.list_pods, а не описательной фразой. Поля step и reason пиши на языке запроса пользователя. Если все шаги покрыты или запрос не требует действий, передай {"gaps":[]}. Не утверждай, что инструмент доступен, если его нет в каталоге.`
	userPrompt := string(planningInput)
	tools, aliases := buildOpenAITools([]ToolDefinition{*reportDefinition})
	message, err := server.client.CompleteWithToolChoice(
		ctx,
		[]openAIToolMessage{
			{Role: "system", Content: &systemPrompt},
			{Role: "user", Content: &userPrompt},
		},
		tools, "required", 1024, 0,
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("capability planning: %w", err)
	}
	if len(message.ToolCalls) != 1 {
		return nil, nil, nil, fmt.Errorf(
			"capability planning returned %d tool calls, want 1", len(message.ToolCalls),
		)
	}
	externalCall := message.ToolCalls[0]
	internalName, exists := aliases[externalCall.Function.Name]
	if !exists || internalName != localCapabilityGapToolName {
		return nil, nil, nil, fmt.Errorf(
			"capability planning requested unexpected tool %q", externalCall.Function.Name,
		)
	}
	externalCall.ID = "call_akritas_capability_plan"
	call := ToolCall{
		ID: externalCall.ID, Name: internalName,
		Arguments: json.RawMessage(externalCall.Function.Arguments),
	}
	result := server.registry.Execute(ctx, call, server.policy)
	serializedResult, err := json.Marshal(result)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encode capability planning result: %w", err)
	}
	message.ToolCalls = []openAIToolCall{externalCall}
	history = append(history, message, openAIToolMessage{
		Role: "tool", Content: openAIStringPointer(string(serializedResult)),
		Name: externalCall.Function.Name, ToolCallID: externalCall.ID,
	})
	return &call, &result, history, nil
}

func opsExecutionToolRegistry(source *ToolRegistry) (*ToolRegistry, error) {
	destination := NewToolRegistry()
	for _, definition := range source.Definitions() {
		if definition.Name == localRAGSearchToolName || definition.Name == localCapabilityGapToolName {
			continue
		}
		if err := destination.Register(definition); err != nil {
			return nil, fmt.Errorf("build Akritas execution tool registry: %w", err)
		}
	}
	return destination, nil
}

func buildOpsChatHistory(messages []openAIChatMessage) ([]openAIToolMessage, error) {
	if len(messages) == 0 || len(messages) > maximumOpsChatMessages {
		return nil, fmt.Errorf("chat requires 1..%d messages", maximumOpsChatMessages)
	}
	totalBytes := 0
	dialogue := make([]DialogueMessage, len(messages))
	for index, message := range messages {
		totalBytes += len(message.Content)
		if totalBytes > maximumOpsChatBytes {
			return nil, fmt.Errorf("chat messages exceed %d bytes", maximumOpsChatBytes)
		}
		role := DialogueRole(message.Role)
		if role != RoleSystem && role != RoleUser && role != RoleAssistant {
			return nil, fmt.Errorf("messages[%d] has unsupported role %q", index, message.Role)
		}
		dialogue[index] = DialogueMessage{Role: role, Content: message.Content}
	}
	if err := ValidateDialogue(dialogue); err != nil {
		return nil, err
	}
	if dialogue[len(dialogue)-1].Role != RoleUser {
		return nil, fmt.Errorf("last message must have role user")
	}
	systemPrompt := opsServerSystemPrompt
	start := 0
	if dialogue[0].Role == RoleSystem {
		systemPrompt += "\n\nДополнительный контекст клиента:\n" + dialogue[0].Content
		start = 1
	}
	history := []openAIToolMessage{{Role: "system", Content: &systemPrompt}}
	for _, message := range dialogue[start:] {
		content := message.Content
		history = append(history, openAIToolMessage{Role: string(message.Role), Content: &content})
	}
	return history, nil
}

func (server *opsServer) resolveGenerationLimits(
	requestedTokens int,
	temperature *float64,
) (int, float64, error) {
	maxTokens := requestedTokens
	if maxTokens == 0 {
		maxTokens = server.defaultMaxTokens
	}
	if maxTokens <= 0 || maxTokens > server.maxTokensLimit {
		return 0, 0, fmt.Errorf("max_tokens must be between 1 and %d", server.maxTokensLimit)
	}
	resolvedTemperature := server.defaultTemp
	if temperature != nil {
		resolvedTemperature = *temperature
	}
	if resolvedTemperature < 0 {
		return 0, 0, fmt.Errorf("temperature must be non-negative")
	}
	return maxTokens, resolvedTemperature, nil
}

func (server *opsServer) acquireGeneration(ctx context.Context) error {
	select {
	case server.generationSlot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for generation slot: %w", ctx.Err())
	}
}

func (server *opsServer) releaseGeneration() {
	<-server.generationSlot
}

func buildOpsToolActivity(result openAIToolLoopResult, debug bool) []opsToolActivity {
	activity := make([]opsToolActivity, len(result.Calls))
	for index, call := range result.Calls {
		status := "missing_result"
		var toolResult *ToolResult
		if index < len(result.Results) {
			status = "ok"
			copyResult := result.Results[index]
			if copyResult.Error != nil {
				status = copyResult.Error.Code
			}
			if debug {
				toolResult = &copyResult
			}
		}
		activity[index] = opsToolActivity{Name: call.Name, ID: call.ID, Status: status}
		if debug {
			activity[index].Arguments = append(json.RawMessage(nil), call.Arguments...)
			activity[index].Result = toolResult
		}
	}
	return activity
}

func decodeOpsRequest(writer http.ResponseWriter, request *http.Request, destination any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maximumOpsAPIRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON request: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	return nil
}

func writeOpsError(writer http.ResponseWriter, status int, err error) {
	log.Printf("level=warn component=akritas status=%d error=%q", status, err)
	writeJSON(writer, status, map[string]any{"error": map[string]string{"message": err.Error()}})
}

func writeOpsBufferedStream(writer http.ResponseWriter, id string, created int64, model, content string) {
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	writer.WriteHeader(http.StatusOK)
	finishReason := "stop"
	chunks := []openAIChatCompletionChunk{
		{
			ID: id, Object: "chat.completion.chunk", Created: created, Model: model,
			Choices: []openAIStreamChoice{{
				Index: 0, Delta: openAIStreamDelta{Role: "assistant", Content: content},
			}},
		},
		{
			ID: id, Object: "chat.completion.chunk", Created: created, Model: model,
			Choices: []openAIStreamChoice{{
				Index: 0, Delta: openAIStreamDelta{}, FinishReason: &finishReason,
			}},
		},
	}
	for _, chunk := range chunks {
		encoded, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(writer, "data: %s\n\n", encoded)
	}
	_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func registerReadOnlyTools(
	destination *ToolRegistry,
	policy NamedToolPolicy,
	source *ToolRegistry,
	sourcePolicy NamedToolPolicy,
) (int, error) {
	ignored := 0
	for _, definition := range source.Definitions() {
		if !sourcePolicy.Allowed[definition.Name] {
			continue
		}
		if definition.Permission != ToolPermissionRead {
			ignored++
			continue
		}
		if err := destination.Register(definition); err != nil {
			return ignored, err
		}
		policy.Allowed[definition.Name] = true
	}
	return ignored, nil
}

func parseOpsWorkspaces(values []string) (map[string]opsWorkspace, error) {
	workspaces := make(map[string]opsWorkspace, len(values))
	for _, value := range values {
		name, rootPath, exists := strings.Cut(value, "=")
		name = strings.TrimSpace(name)
		rootPath = strings.TrimSpace(rootPath)
		if !exists || !mcpToolNameValid(name) || rootPath == "" {
			return nil, fmt.Errorf("workspace must use name=directory with a valid name")
		}
		if _, duplicate := workspaces[name]; duplicate {
			return nil, fmt.Errorf("duplicate workspace %q", name)
		}
		root, err := resolveChangeSimulationRoot(rootPath)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: %w", name, err)
		}
		workspaces[name] = opsWorkspace{Name: name, Root: root}
	}
	return workspaces, nil
}
