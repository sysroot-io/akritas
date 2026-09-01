package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"akritas/internal/audit"
	"akritas/internal/investigation"
	"akritas/internal/rag"
)

func TestRunAuditEndpointsRequireAuthenticationAndReturnPersistedRuns(t *testing.T) {
	server := newOpsTestServer(t, "http://127.0.0.1:1/v1", "secret", nil)
	store, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server.SetAuditStore(store)
	run, err := store.StartRun("chat", "api-key", "", "akritas", map[string]string{"kind": "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(run.ID, audit.RunSucceeded, "", nil); err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRecorder()
	server.handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/runs?limit=1", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), run.ID) {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+run.ID, nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"succeeded"`) {
		t.Fatalf("get status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestToolCatalogRequiresAuthenticationAndListsOnlyAuthorizedTools(t *testing.T) {
	server := newOpsTestServer(t, "http://127.0.0.1:1/v1", "secret", nil)
	for _, name := range []string{"test.allowed", "test.denied"} {
		if err := server.registry.Register(ToolDefinition{
			Name: name, Description: "Catalog entry for " + name,
			InputSchema:       json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Permission:        ToolPermissionRead,
			ValidateArguments: func(json.RawMessage) error { return nil },
			Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"ok":true}`), nil
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	server.policy.Allowed["test.allowed"] = true

	unauthorized := httptest.NewRecorder()
	server.handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/tools", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/tools", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"name":"test.allowed"`) ||
		!strings.Contains(response.Body.String(), `"permission":"read"`) ||
		strings.Contains(response.Body.String(), "test.denied") {
		t.Fatalf("catalog status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOpsWebIncludesFullProposalDiagnostics(t *testing.T) {
	page, err := opsWebFiles.ReadFile("ops_web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	content := string(page)
	for _, expected := range []string{
		"Show the complete structured proposal",
		"Download proposal.json",
		"downloadTextFile",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("Ops Web UI is missing %q", expected)
		}
	}
}

func TestOpsWebIncludesToolCatalogAndUnambiguousRunLog(t *testing.T) {
	page, err := opsWebFiles.ReadFile("ops_web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	content := string(page)
	for _, expected := range []string{
		`id="tools-dialog"`,
		"loadToolCatalog",
		"Run log",
		"Investigation plan",
		"Unavailable checks",
		"item.duration_ms",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("Ops Web UI is missing %q", expected)
		}
	}
	if strings.Contains(content, "The report contains no confirmed gaps.") {
		t.Fatal("empty investigation plan is still presented as a missing-tools error")
	}
}

func TestOpsWebRollsBackFailedChatMessage(t *testing.T) {
	page, err := opsWebFiles.ReadFile("ops_web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	content := string(page)
	for _, expected := range []string{
		"state.messages.pop();",
		"input.value = value;",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("Ops Web UI does not roll back failed chat messages: missing %q", expected)
		}
	}
}

func TestOpsWebRendersMarkdownTablesAndHorizontalRules(t *testing.T) {
	page, err := opsWebFiles.ReadFile("ops_web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	content := string(page)
	for _, expected := range []string{
		"splitMarkdownTableRow",
		"markdownTableAlignment",
		"document.createElement('table')",
		"document.createElement('thead')",
		"document.createElement('tbody')",
		"document.createElement('hr')",
		".content .table-wrap",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("Ops Web Markdown renderer is missing %q", expected)
		}
	}
}

func TestOpsWebInterfaceContainsNoCyrillicText(t *testing.T) {
	page, err := opsWebFiles.ReadFile("ops_web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if index := strings.IndexFunc(string(page), func(value rune) bool {
		return unicode.Is(unicode.Cyrillic, value)
	}); index >= 0 {
		t.Fatalf("Ops Web UI contains Cyrillic text at byte %d", index)
	}
}

func TestOpsChatHistoryUsesConfiguredResponseLanguage(t *testing.T) {
	history, err := buildOpsChatHistory([]openAIChatMessage{{
		Role: "user", Content: "Inspect CPU.",
	}}, "pt-BR")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Content == nil {
		t.Fatalf("unexpected chat history: %+v", history)
	}
	for _, expected := range []string{
		`BCP 47 tag "pt-BR"`,
		"Do not infer or change the response language",
		"Preserve exact technical identifiers",
	} {
		if !strings.Contains(*history[0].Content, expected) {
			t.Fatalf("Ops system prompt is missing %q", expected)
		}
	}
}

func TestOpsServerAcceptsAlertmanagerWebhook(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamCalls++
		var input openAIToolRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode upstream request: %v", err)
			http.Error(writer, "invalid test request", http.StatusBadRequest)
			return
		}
		if upstreamCalls == 2 {
			writeJSON(writer, http.StatusOK, map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{
					"role": "assistant", "content": nil,
					"tool_calls": []any{map[string]any{
						"id": "result_1", "type": "function",
						"function": map[string]any{
							"name":      input.Tools[0].Function.Name,
							"arguments": `{"finding_status":"suspected","actionability":"requires_human","confidence":"low","summary":"High CPU requires diagnostic evidence.","evidence":[],"affected_components":["api-01"],"recommended_actions":["Collect CPU diagnostics."]}`,
						},
					},
					}}, "finish_reason": "tool_calls"}},
			})
			return
		}
		if len(input.Messages) != 2 || input.Messages[1].Content == nil ||
			!strings.Contains(*input.Messages[1].Content, "HighCPU") ||
			!strings.Contains(*input.Messages[1].Content, "api-01") ||
			!strings.Contains(*input.Messages[1].Content, "untrusted monitoring data") {
			t.Errorf("Alertmanager payload is missing from prompt: %+v", input.Messages)
		}
		writeJSON(writer, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "Found the high-cpu runbook; no checks were executed.",
			}}},
		})
	}))
	defer upstream.Close()
	server := newOpsTestServer(t, upstream.URL+"/v1", "secret", nil)
	payload := `{
		"version":"4","groupKey":"{}:{alertname=\"HighCPU\"}","truncatedAlerts":0,
		"status":"firing","receiver":"akritas","groupLabels":{"alertname":"HighCPU"},
		"commonLabels":{"severity":"critical"},"commonAnnotations":{"summary":"CPU is high"},
		"routeLabels":{"team":"platform"},"externalURL":"http://alertmanager:9093",
		"notification_reason":"first notification","alerts":[{
			"status":"firing","labels":{"alertname":"HighCPU","instance":"api-01"},
			"annotations":{"description":"CPU above 95%"},"startsAt":"2026-08-23T10:00:00Z",
			"endsAt":"0001-01-01T00:00:00Z","generatorURL":"http://prometheus:9090/graph","fingerprint":"abc123"
		}]}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/alertmanager/webhook", bytes.NewBufferString(payload))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Alertmanager status=%d body=%s", response.Code, response.Body.String())
	}
	var output opsAlertmanagerAPIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if !output.Accepted || output.Status != "firing" || output.GroupKey == "" ||
		!strings.Contains(output.Answer, "high-cpu") || output.Activity == nil ||
		output.Investigation.FindingStatus != investigation.FindingSuspected ||
		output.Budget.Usage.Iterations != 2 {
		t.Fatalf("unexpected Alertmanager response: %+v", output)
	}

	invalid := strings.Replace(payload, `"version":"4"`, `"version":"3"`, 1)
	invalidRequest := httptest.NewRequest(http.MethodPost, "/api/v1/alertmanager/webhook", bytes.NewBufferString(invalid))
	invalidRequest.Header.Set("Authorization", "Bearer secret")
	invalidResponse := httptest.NewRecorder()
	server.handler().ServeHTTP(invalidResponse, invalidRequest)
	if invalidResponse.Code != http.StatusBadRequest || upstreamCalls != 2 {
		t.Fatalf("invalid webhook status=%d upstream_calls=%d body=%s", invalidResponse.Code, upstreamCalls, invalidResponse.Body.String())
	}
}

func TestLogModelProposedChecksMarksThemNotExecuted(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)

	logModelProposedChecks("backend", 2, []string{"go test ./...\n--unexpected", strings.Repeat("x", 600)})
	logged := output.String()
	if !strings.Contains(logged, `workspace="backend"`) ||
		!strings.Contains(logged, "change_attempt=2") ||
		!strings.Contains(logged, "model_check_status=not_executed") ||
		!strings.Contains(logged, `go test ./...\n--unexpected`) ||
		!strings.Contains(logged, "<truncated by Akritas>") {
		t.Fatalf("unexpected model check log: %s", logged)
	}
}

func TestOpsServerChatAndOpenAIEndpoints(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			http.NotFound(writer, request)
			return
		}
		upstreamCalls++
		var input openAIToolRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		if input.Model != "qwen-test" || len(input.Tools) != 0 ||
			len(input.Messages) != 2 || input.Messages[0].Role != "system" {
			t.Errorf("unexpected upstream request: %+v", input)
		}
		writeJSON(writer, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "Diagnostic answer.",
			}}},
		})
	}))
	defer upstream.Close()
	server := newOpsTestServer(t, upstream.URL+"/v1", "", nil)
	handler := server.handler()

	customRequest := httptest.NewRequest(
		http.MethodPost, "/api/v1/chat",
		bytes.NewBufferString(`{"messages":[{"role":"user","content":"What should be checked?"}]}`),
	)
	customResponse := httptest.NewRecorder()
	handler.ServeHTTP(customResponse, customRequest)
	if customResponse.Code != http.StatusOK {
		t.Fatalf("custom chat status=%d body=%s", customResponse.Code, customResponse.Body.String())
	}
	var customOutput opsChatAPIResponse
	if err := json.Unmarshal(customResponse.Body.Bytes(), &customOutput); err != nil {
		t.Fatal(err)
	}
	if customOutput.Answer != "Diagnostic answer." || customOutput.Activity == nil {
		t.Fatalf("unexpected custom response: %+v", customOutput)
	}

	openAIRequest := httptest.NewRequest(
		http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"akritas","messages":[{"role":"user","content":"What should be checked?"}],"stream":false}`),
	)
	openAIResponse := httptest.NewRecorder()
	handler.ServeHTTP(openAIResponse, openAIRequest)
	if openAIResponse.Code != http.StatusOK ||
		!strings.Contains(openAIResponse.Body.String(), "Diagnostic answer") {
		t.Fatalf("OpenAI response status=%d body=%s", openAIResponse.Code, openAIResponse.Body.String())
	}
	streamRequest := httptest.NewRequest(
		http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"akritas","messages":[{"role":"user","content":"What should be checked?"}],"stream":true}`),
	)
	streamResponse := httptest.NewRecorder()
	handler.ServeHTTP(streamResponse, streamRequest)
	if streamResponse.Code != http.StatusOK ||
		streamResponse.Header().Get("Content-Type") != "text/event-stream" ||
		!strings.Contains(streamResponse.Body.String(), "data: [DONE]") {
		t.Fatalf("stream response status=%d headers=%v body=%s", streamResponse.Code, streamResponse.Header(), streamResponse.Body.String())
	}
	if upstreamCalls != 3 {
		t.Fatalf("upstream calls=%d, want 3", upstreamCalls)
	}
}

func TestOpsServerPrefetchesRAGForEveryUserTurn(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input openAIToolRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		if len(input.Messages) != 4 || len(input.Messages[2].ToolCalls) != 1 ||
			input.Messages[3].Role != "tool" || input.Messages[3].Content == nil ||
			!strings.Contains(*input.Messages[3].Content, "Google") {
			t.Errorf("automatic RAG result is missing: %+v", input.Messages)
		}
		writeJSON(writer, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "Go was developed at Google [go].",
			}}},
		})
	}))
	defer upstream.Close()
	server := newOpsTestServer(t, upstream.URL+"/v1", "", nil)
	if err := RegisterRAGSearchTool(server.registry, testRAGIndex(t), RAGSearchToolOptions{
		DefaultTopK: 1, MaximumTopK: 1, MaximumRunes: 800,
	}); err != nil {
		t.Fatal(err)
	}
	server.policy.Allowed[localRAGSearchToolName] = true

	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/chat",
		bytes.NewBufferString(`{"messages":[{"role":"user","content":"Who developed Go?"}],"debug":true}`),
	)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("chat status=%d body=%s", response.Code, response.Body.String())
	}
	var output opsChatAPIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Activity) != 1 || output.Activity[0].Name != localRAGSearchToolName ||
		output.Activity[0].Result == nil || len(output.Activity[0].Arguments) == 0 {
		t.Fatalf("automatic RAG activity or debug payload is missing: %+v", output.Activity)
	}
}

func TestOpsServerReturnsStructuredCapabilityGaps(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamCalls++
		var input openAIToolRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		if upstreamCalls == 1 {
			if input.ToolChoice != "required" || len(input.Tools) != 1 {
				t.Errorf("capability planner was not forced: %+v", input)
			}
			alias := openAIToolAlias(localInvestigationPlanToolName)
			writeJSON(writer, http.StatusOK, map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{
					"role": "assistant", "content": nil,
					"tool_calls": []any{map[string]any{
						"id": "call_gap_1", "type": "function",
						"function": map[string]any{
							"name":      alias,
							"arguments": `{"checks":[],"gaps":[{"step":"Check CPU","capability":"metrics.query_range","reason":"No metrics tool is registered"}]}`,
						},
					}},
				}}},
			})
			return
		}
		if len(input.Tools) != 0 {
			t.Errorf("reporting tool leaked into execution loop: %+v", input.Tools)
		}
		writeJSON(writer, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "The CPU check was not executed.",
			}}},
		})
	}))
	defer upstream.Close()
	server := newOpsTestServer(t, upstream.URL+"/v1", "", nil)
	if err := registerOpsInvestigationPlanTool(server.registry); err != nil {
		t.Fatal(err)
	}
	server.policy.Allowed[localInvestigationPlanToolName] = true

	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/chat",
		bytes.NewBufferString(`{"messages":[{"role":"user","content":"Check CPU"}],"debug":true}`),
	)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("chat status=%d body=%s", response.Code, response.Body.String())
	}
	var output opsChatAPIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.CapabilityGaps) != 1 ||
		output.CapabilityGaps[0].Capability != "metrics.query_range" ||
		len(output.Activity) != 1 || output.Activity[0].Result == nil {
		t.Fatalf("structured investigation gaps are missing: %+v", output)
	}
	if upstreamCalls != 2 {
		t.Fatalf("upstream calls=%d, want 2", upstreamCalls)
	}
}

func TestOpsServerExecutesEveryPlannedReadOnlyCheck(t *testing.T) {
	upstreamCalls := 0
	toolExecutions := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamCalls++
		var input openAIToolRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		if upstreamCalls == 1 {
			if input.ToolChoice != "required" || len(input.Tools) != 1 ||
				input.Messages[0].Content == nil ||
				!strings.Contains(*input.Messages[0].Content, `BCP 47 tag "pt-BR"`) ||
				input.Messages[1].Content == nil ||
				!strings.Contains(*input.Messages[1].Content, `"response_language":"pt-BR"`) ||
				!strings.Contains(*input.Messages[1].Content, `"input_schema"`) ||
				!strings.Contains(*input.Messages[1].Content, `"test.metrics.query"`) ||
				!strings.Contains(string(input.Tools[0].Function.Parameters), "host-configured response language") {
				t.Errorf("investigation planner did not receive the authorized tool schema: %+v", input)
			}
			alias := openAIToolAlias(localInvestigationPlanToolName)
			writeJSON(writer, http.StatusOK, map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{
					"role": "assistant", "content": nil,
					"tool_calls": []any{map[string]any{
						"id": "call_plan_1", "type": "function",
						"function": map[string]any{
							"name":      alias,
							"arguments": `{"checks":[{"step":"Check CPU","tool":"test.metrics.query","arguments":{"host":"node-1"},"reason":"Metrics are available"}],"gaps":[]}`,
						},
					}},
				}}},
			})
			return
		}
		foundResult := false
		for _, message := range input.Messages {
			if message.Role == "tool" && message.ToolCallID == "call_akritas_planned_1" &&
				message.Content != nil && strings.Contains(*message.Content, `"cpu":87`) {
				foundResult = true
			}
		}
		if !foundResult {
			t.Errorf("planned tool result was not supplied to the final model request: %+v", input.Messages)
		}
		writeJSON(writer, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "CPU was confirmed by metrics.",
			}}},
		})
	}))
	defer upstream.Close()
	server := newOpsTestServer(t, upstream.URL+"/v1", "", nil)
	server.responseLanguage = "pt-BR"
	if err := registerOpsInvestigationPlanTool(server.registry); err != nil {
		t.Fatal(err)
	}
	server.policy.Allowed[localInvestigationPlanToolName] = true
	if err := server.registry.Register(ToolDefinition{
		Name: "test.metrics.query", Description: "Read CPU metrics for one host.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"host":{"type":"string"}},"required":["host"],"additionalProperties":false}`),
		Permission:  ToolPermissionRead,
		ValidateArguments: func(raw json.RawMessage) error {
			var arguments struct {
				Host string `json:"host"`
			}
			if err := decodeStrictJSONObject(raw, &arguments); err != nil {
				return err
			}
			if arguments.Host == "" {
				return fmt.Errorf("host is required")
			}
			return nil
		},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			toolExecutions++
			return json.RawMessage(`{"cpu":87}`), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	server.policy.Allowed["test.metrics.query"] = true

	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/chat",
		bytes.NewBufferString(`{"messages":[{"role":"user","content":"Check CPU on node-1"}],"debug":true}`),
	)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("chat status=%d body=%s", response.Code, response.Body.String())
	}
	var output opsChatAPIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if toolExecutions != 1 || len(output.Activity) != 2 ||
		output.Activity[1].Name != "test.metrics.query" || output.Activity[1].Status != "ok" {
		t.Fatalf("planned check was not executed exactly once: executions=%d output=%+v", toolExecutions, output)
	}
	if upstreamCalls != 2 {
		t.Fatalf("upstream calls=%d, want 2", upstreamCalls)
	}
}

func TestOpsServerProtectsAPIAndUsesAllowedWorkspace(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input openAIToolRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		if len(input.Tools) != 1 || input.ToolChoice != "required" || input.Messages[1].Content == nil ||
			!strings.Contains(*input.Messages[1].Content, "10.20.4.15") {
			t.Errorf("change snapshot missing or tools exposed: %+v", input)
		}
		old := "        - frontend"
		proposal := changeSimulationProposal{
			Analysis: "Production pillar is source of truth.",
			Edits: []changeSimulationEdit{{
				Path: "pillar/prod/nftables.sls", Old: old,
				New: old + "\n    - name: orders-v2\n      address: 10.20.4.15\n      port: 8443\n      sources:\n        - frontend",
			}},
			Checks: []string{"yamllint pillar/prod/nftables.sls"},
			Risks:  []string{"Incorrect rule."}, Rollback: "Revert the change.",
		}
		arguments, err := json.Marshal(proposal)
		if err != nil {
			t.Fatal(err)
		}
		writeJSON(writer, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": nil,
				"tool_calls": []any{map[string]any{
					"id": "call_change", "type": "function",
					"function": map[string]any{
						"name":      changeSimulationProposalToolName,
						"arguments": string(arguments),
					},
				}},
			}}},
		})
	}))
	defer upstream.Close()
	root, err := resolveChangeSimulationRoot("../../testdata/ops-change/nftables")
	if err != nil {
		t.Fatal(err)
	}
	server := newOpsTestServer(t, upstream.URL+"/v1", "secret", map[string]opsWorkspace{
		"nftables": {Name: "nftables", Root: root},
	})
	handler := server.handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	authorizedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer secret")
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK || !strings.Contains(authorized.Body.String(), `"nftables"`) ||
		strings.Contains(authorized.Body.String(), root) {
		t.Fatalf("workspace catalog leaked root or failed: status=%d body=%s", authorized.Code, authorized.Body.String())
	}

	payload := `{
		"workspace":"nftables",
		"request":"Allow orders-v2.",
		"files":["inventory/backends.yaml","pillar/prod/nftables.sls"]
	}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/change/simulations", bytes.NewBufferString(payload))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "orders-v2") ||
		!strings.Contains(response.Body.String(), `"changed_files":["pillar/prod/nftables.sls"]`) {
		t.Fatalf("change status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRegisterReadOnlyToolsExcludesWriteAndDangerous(t *testing.T) {
	source := NewToolRegistry()
	for _, permission := range []ToolPermission{
		ToolPermissionRead, ToolPermissionWrite, ToolPermissionDangerous,
	} {
		name := "test." + string(permission)
		if err := source.Register(ToolDefinition{
			Name: name, Description: "test tool", Permission: permission,
			InputSchema:       json.RawMessage(`{"type":"object","additionalProperties":false}`),
			ValidateArguments: func(json.RawMessage) error { return nil },
			Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"ok":true}`), nil
			},
			Timeout: time.Second,
		}); err != nil {
			t.Fatal(err)
		}
	}
	sourcePolicy := NamedToolPolicy{Allowed: map[string]bool{
		"test.read": true, "test.write": true, "test.dangerous": true,
	}}
	destination := NewToolRegistry()
	destinationPolicy := NamedToolPolicy{Allowed: make(map[string]bool)}
	ignored, err := registerReadOnlyTools(destination, destinationPolicy, source, sourcePolicy)
	if err != nil {
		t.Fatal(err)
	}
	if ignored != 2 || len(destination.Definitions()) != 1 ||
		destination.Definitions()[0].Name != "test.read" ||
		!destinationPolicy.Allowed["test.read"] {
		t.Fatalf("unexpected filtered tools: ignored=%d definitions=%+v policy=%+v", ignored, destination.Definitions(), destinationPolicy)
	}
}

func TestPendingChangeIsOneTime(t *testing.T) {
	server := &opsServer{pendingChanges: make(map[string]pendingChange)}
	pending := pendingChange{ID: "once", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	server.storePendingChange(pending)
	if _, err := server.takePendingChange("once"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.takePendingChange("once"); err == nil {
		t.Fatal("second approval unexpectedly succeeded")
	}
}

func TestPendingChangeRejectsExpiredApproval(t *testing.T) {
	server := &opsServer{pendingChanges: make(map[string]pendingChange)}
	server.pendingChanges["expired"] = pendingChange{
		ID: "expired", CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(-time.Minute),
	}
	if _, err := server.takePendingChange("expired"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired approval was accepted: %v", err)
	}
	if _, exists := server.pendingChanges["expired"]; exists {
		t.Fatal("expired approval remained reusable")
	}
}

func TestApplyChangeAPIRequiresConfirmationAndAppliesOnce(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(target, []byte("enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := newOpsTestServer(t, "http://127.0.0.1:1/v1", "secret", map[string]opsWorkspace{
		"test": {Name: "test", Root: root},
	})
	server.storePendingChange(pendingChange{
		ID: "approval", Workspace: server.workspaces["test"], CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute),
		Original: map[string]string{"config.yaml": "enabled: false\n"}, Updated: map[string]string{"config.yaml": "enabled: true\n"},
	})
	handler := server.handler()

	unconfirmed := httptest.NewRequest(http.MethodPost, "/api/v1/change/simulations/approval/apply", bytes.NewBufferString(`{"confirm":false}`))
	unconfirmed.Header.Set("Authorization", "Bearer secret")
	unconfirmedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unconfirmedResponse, unconfirmed)
	if unconfirmedResponse.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed status=%d", unconfirmedResponse.Code)
	}

	confirmed := httptest.NewRequest(http.MethodPost, "/api/v1/change/simulations/approval/apply", bytes.NewBufferString(`{"confirm":true}`))
	confirmed.Header.Set("Authorization", "Bearer secret")
	confirmedResponse := httptest.NewRecorder()
	handler.ServeHTTP(confirmedResponse, confirmed)
	if confirmedResponse.Code != http.StatusOK {
		t.Fatalf("confirmed status=%d body=%s", confirmedResponse.Code, confirmedResponse.Body.String())
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "enabled: true\n" {
		t.Fatalf("approved content=%q err=%v", content, err)
	}

	repeated := httptest.NewRequest(http.MethodPost, "/api/v1/change/simulations/approval/apply", bytes.NewBufferString(`{"confirm":true}`))
	repeated.Header.Set("Authorization", "Bearer secret")
	repeatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(repeatedResponse, repeated)
	if repeatedResponse.Code != http.StatusNotFound {
		t.Fatalf("repeated status=%d", repeatedResponse.Code)
	}
}

func newOpsTestServer(
	t *testing.T,
	baseURL string,
	apiKey string,
	workspaces map[string]opsWorkspace,
) *opsServer {
	t.Helper()
	client, err := newOpenAIToolClient(baseURL, "qwen-test", "", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	server, err := newOpsServer(
		client, NewToolRegistry(), NamedToolPolicy{Allowed: make(map[string]bool)},
		"akritas", apiKey, "en", 256, 1024, 0, 4, time.Minute, workspaces,
	)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func testRAGIndex(t *testing.T) *RAGIndex {
	t.Helper()
	chunks := []RAGChunk{
		{DocumentID: "go", Title: "Go", Chunk: 0, Source: "test", URL: "https://go.dev", Text: "Go was developed at Google by Robert Griesemer, Rob Pike, and Ken Thompson.", Tokens: 13},
		{DocumentID: "python", Title: "Python", Chunk: 0, Source: "test", Text: "Python was created by Guido van Rossum.", Tokens: 7},
	}
	index := &RAGIndex{
		Version: rag.CurrentIndexVersion, ManifestSHA256: strings.Repeat("a", 64),
		ChunkRunes: 100, OverlapRunes: 10, AverageTokens: 10,
		Chunks: chunks, Postings: make(map[string][]RAGPosting),
	}
	for chunkIndex, chunk := range chunks {
		frequencies := make(map[string]int)
		for _, term := range rag.TokenizeTerms(chunk.Title + " " + chunk.Text) {
			frequencies[term]++
		}
		for term, frequency := range frequencies {
			index.Postings[term] = append(index.Postings[term], RAGPosting{ChunkIndex: chunkIndex, Frequency: frequency})
		}
	}
	if err := index.Validate(); err != nil {
		t.Fatal(err)
	}
	return index
}
