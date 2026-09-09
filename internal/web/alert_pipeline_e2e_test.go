package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"akritas/internal/alerts"
	"akritas/internal/audit"
	"akritas/internal/investigation"
	"akritas/internal/notifications"
	"akritas/internal/rag"
	"akritas/internal/skills"
)

// TestAlertPipelineHighCPUPostgreSQL is the deterministic golden path for the
// alert workflow. External systems are controlled HTTP/tool doubles, while the
// production normalization, stores, worker, planning, skill selection,
// evidence validation, notification, and query APIs all execute unchanged.
func TestAlertPipelineHighCPUPostgreSQL(t *testing.T) {
	var modelCalls atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		modelCalls.Add(1)
		var input openAIToolRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode model request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		switch {
		case requestHasTool(input, localInvestigationPlanToolName):
			assertModelTextContains(t, input, "linux-high-cpu.md", "inventory.host", "linux.processes", "postgres.activity")
			writeE2EToolCall(writer, openAIToolAlias(localInvestigationPlanToolName), "model_plan", `{
  "checks": [
    {"step":"Resolve host role","tool":"inventory.host","arguments":{"host":"pg01"},"reason":"Inventory identifies the service role"},
    {"step":"Find CPU consumers","tool":"linux.processes","arguments":{"host":"pg01"},"reason":"Process usage identifies the CPU owner"}
  ],
  "gaps": []
}`)
		case requestHasTool(input, investigationResultToolName):
			assertModelTextContains(t, input, "tool-call:call_akritas_planned_1", "tool-call:call_akritas_planned_2", "tool-call:call_postgres_activity")
			writeE2EToolCall(writer, openAIToolAlias(investigationResultToolName), "model_result", `{
  "finding_status":"confirmed",
  "actionability":"requires_human",
  "confidence":"high",
  "summary":"PostgreSQL autovacuum is consuming most CPU on pg01.",
  "hypothesis":"Autovacuum on public.operation_old is the primary CPU consumer.",
  "impact":"Database workloads on pg01 may experience increased latency.",
  "evidence":["tool-call:call_akritas_planned_1","tool-call:call_akritas_planned_2","tool-call:call_postgres_activity"],
  "ruled_out":["CPU use by a non-PostgreSQL process"],
  "affected_components":["postgresql:pg01"],
  "recommended_actions":["Inspect table bloat and autovacuum settings for public.operation_old."],
  "production_writes":0
}`)
		case requestHasTool(input, "postgres.activity"):
			assertModelTextContains(t, input, `<AKRITAS_SKILL name="postgresql"`, `"role":"postgresql"`, `"cpu_percent":690`)
			if requestHasToolResult(input, "call_postgres_activity") {
				writeE2EModelText(writer, "Observed PostgreSQL autovacuum consuming the CPU; no production changes were made.")
				return
			}
			writeE2EToolCall(writer, openAIToolAlias("postgres.activity"), "call_postgres_activity", `{"host":"pg01"}`)
		default:
			t.Errorf("unexpected model request: %+v", input)
			http.Error(writer, "unexpected model request", http.StatusBadRequest)
		}
	}))
	defer model.Close()

	notificationsReceived := make(chan notifications.WebhookEnvelope, 1)
	notificationTarget := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var envelope notifications.WebhookEnvelope
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			t.Errorf("decode notification: %v", err)
			http.Error(writer, "invalid notification", http.StatusBadRequest)
			return
		}
		notificationsReceived <- envelope
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer notificationTarget.Close()

	server := newOpsTestServer(t, model.URL+"/v1", "api-secret", nil)
	server.maxToolCalls = 8
	server.runBudgetLimits.MaxToolCalls = 8
	server.runBudgetLimits.MaxIterations = 12
	server.SetPublicURL("https://akritas.test")
	if err := server.client.SetSystemInstructions("Global deterministic test rules."); err != nil {
		t.Fatal(err)
	}
	if err := registerOpsInvestigationPlanTool(server.registry); err != nil {
		t.Fatal(err)
	}
	server.policy.Allowed[localInvestigationPlanToolName] = true
	if err := RegisterRAGSearchTool(server.registry, highCPUTestIndex(), RAGSearchToolOptions{
		DefaultTopK: 1, MaximumTopK: 1, MaximumRunes: 1200,
	}); err != nil {
		t.Fatal(err)
	}
	server.policy.Allowed[localRAGSearchToolName] = true

	skillRoot := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(filepath.Join(skillRoot, "postgresql"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "postgresql", "SKILL.md"), []byte(`---
name: postgresql
description: PostgreSQL read-only diagnostics.
match:
  - postgresql
---
Check active queries, autovacuum, and waits using authorized tools only.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	skillCatalog, err := skills.Load(skillRoot)
	if err != nil {
		t.Fatal(err)
	}
	server.SetSkillCatalog(skillCatalog)
	if err := registerOpsKnowledgeSkillTools(server.registry, skillCatalog); err != nil {
		t.Fatal(err)
	}
	server.policy.Allowed[localKnowledgeListSkillsName] = true
	server.policy.Allowed[localKnowledgeLoadSkillName] = true

	var executionsMu sync.Mutex
	executions := make([]string, 0, 3)
	registerE2EHostTool(t, server, "inventory.host", `{"host":"pg01","role":"postgresql"}`, &executionsMu, &executions)
	registerE2EHostTool(t, server, "linux.processes", `{"host":"pg01","processes":[{"name":"postgres","cpu_percent":690}]}`, &executionsMu, &executions)
	registerE2EHostTool(t, server, "postgres.activity", `{"host":"pg01","active_queries":[{"pid":48192,"query":"autovacuum: VACUUM public.operation_old","wait_event":null}]}`, &executionsMu, &executions)

	auditStore, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	server.SetAuditStore(auditStore)
	alertStore, err := alerts.OpenStore(filepath.Join(t.TempDir(), "alerts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	alertConfig := filepath.Join(t.TempDir(), "alert-sources.json")
	if err := os.WriteFile(alertConfig, []byte(`{"version":1,"sources":[{"name":"alertmanager-main","type":"alertmanager","bearer_token_env":"ALERT_TOKEN"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	alertRegistry, err := alerts.LoadRegistry(alertConfig, func(name string) (string, bool) {
		return map[string]string{"ALERT_TOKEN": "source-secret"}[name], name == "ALERT_TOKEN"
	})
	if err != nil {
		t.Fatal(err)
	}
	notificationConfig := filepath.Join(t.TempDir(), "notifications.json")
	if err := os.WriteFile(notificationConfig, []byte(fmt.Sprintf(`{"version":1,"receivers":[{"name":"capture","type":"webhook","url":%q}]}`, notificationTarget.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := notifications.Load(notificationConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.SetNotificationDispatcher(dispatcher)
	t.Cleanup(func() {
		server.CloseAlertWorker()
		_ = alertStore.Close()
		_ = auditStore.Close()
	})
	if err := server.SetAlertRuntime(alertRegistry, alertStore); err != nil {
		t.Fatal(err)
	}

	payload := []byte(`{
  "version":"4","groupKey":"{}:{alertname=\"HighCPU\"}","truncatedAlerts":0,
  "status":"firing","receiver":"akritas","groupLabels":{"alertname":"HighCPU"},
  "commonLabels":{"severity":"critical"},"commonAnnotations":{"summary":"CPU is high"},
  "routeLabels":{"team":"platform"},"externalURL":"http://alertmanager:9093",
  "alerts":[{"status":"firing","labels":{"alertname":"HighCPU","instance":"pg01"},
    "annotations":{"description":"CPU above 95%"},"startsAt":"2026-09-06T23:50:00Z",
    "endsAt":"0001-01-01T00:00:00Z","generatorURL":"http://prometheus:9090/graph","fingerprint":"highcpu-pg01"}]
}`)
	accepted := sendE2EAlert(t, server, payload, http.StatusAccepted)
	if len(accepted.Events) != 1 || accepted.Events[0].JobID == "" {
		t.Fatalf("alert did not create a job: %+v", accepted)
	}
	jobContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	job, err := alertStore.WaitJob(jobContext, accepted.Events[0].JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != alerts.JobSucceeded || job.RunID == "" || job.Error != "" {
		t.Fatalf("unexpected completed job: %+v", job)
	}
	if modelCalls.Load() != 4 {
		t.Fatalf("model calls=%d, want 4", modelCalls.Load())
	}
	executionsMu.Lock()
	executed := strings.Join(executions, ",")
	executionsMu.Unlock()
	if executed != "inventory.host,linux.processes,postgres.activity" {
		t.Fatalf("tool execution order=%q", executed)
	}

	run, exists := auditStore.Get(job.RunID)
	if !exists || run.Status != audit.RunSucceeded || run.Investigation == nil {
		t.Fatalf("completed Run is missing: %+v exists=%v", run, exists)
	}
	if run.Investigation.FindingStatus != investigation.FindingConfirmed ||
		run.Investigation.ProductionWrites != 0 ||
		strings.Join(run.Investigation.Evidence, ",") != "tool-call:call_akritas_planned_1,tool-call:call_akritas_planned_2,tool-call:call_postgres_activity" ||
		run.Metadata["skills"] != "postgresql" {
		t.Fatalf("unexpected structured investigation: run=%+v result=%+v", run, run.Investigation)
	}
	assertE2ERunEvents(t, run)

	select {
	case envelope := <-notificationsReceived:
		if envelope.Event != notifications.EventIncidentInvestigated ||
			envelope.Incident.RunID != run.ID ||
			envelope.Incident.RunURL != "https://akritas.test/runs/"+run.ID ||
			envelope.Incident.Investigation.FindingStatus != investigation.FindingConfirmed ||
			strings.Join(envelope.Incident.Skills, ",") != "postgresql" {
			t.Fatalf("unexpected notification: %+v", envelope)
		}
		raw, _ := json.Marshal(envelope)
		if bytes.Contains(raw, []byte(`"cpu_percent":690`)) || bytes.Contains(raw, []byte("VACUUM public.operation_old")) {
			t.Fatalf("notification leaked raw tool results: %s", raw)
		}
	default:
		t.Fatal("notification was not delivered")
	}

	assertE2EQueryAPIs(t, server, job, accepted.Events[0].IncidentID)
	duplicate := sendE2EAlert(t, server, payload, http.StatusOK)
	if duplicate.Accepted || len(duplicate.Events) != 1 || duplicate.Events[0].Status != alerts.DeliveryDuplicate || duplicate.Events[0].JobID != "" {
		t.Fatalf("duplicate alert was not suppressed: %+v", duplicate)
	}
	if modelCalls.Load() != 4 {
		t.Fatalf("duplicate alert triggered model calls: %d", modelCalls.Load())
	}
}

func TestAlertPipelineInconclusiveWhenCapabilitiesAreMissing(t *testing.T) {
	var modelCalls atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		modelCalls.Add(1)
		var input openAIToolRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode model request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		switch {
		case requestHasTool(input, localInvestigationPlanToolName):
			assertModelTextContains(t, input, "DiskFreeLow", "app-03")
			writeE2EToolCall(writer, openAIToolAlias(localInvestigationPlanToolName), "model_gap_plan", `{
  "checks": [],
  "gaps": [
    {"step":"Inspect filesystem usage","capability":"linux.filesystem","reason":"No authorized filesystem tool is available"},
    {"step":"Inspect largest directories","capability":"linux.directory_usage","reason":"No authorized directory-usage tool is available"}
  ]
}`)
		case requestHasTool(input, investigationResultToolName):
			writeE2EToolCall(writer, openAIToolAlias(investigationResultToolName), "model_gap_result", `{
  "finding_status":"inconclusive",
  "actionability":"requires_human",
  "confidence":"low",
  "summary":"DiskFreeLow could not be diagnosed because filesystem diagnostics are unavailable.",
  "hypothesis":"The filesystem may be full, but the primary contributor is unknown.",
  "impact":"app-03 may fail writes if free space continues to decrease.",
  "evidence":[],
  "ruled_out":[],
  "affected_components":["host:app-03"],
  "recommended_actions":["Run authorized filesystem and directory-usage diagnostics."],
  "production_writes":0
}`)
		default:
			if len(input.Tools) != 0 {
				t.Errorf("unexpected execution tools for missing-capability scenario: %+v", input.Tools)
			}
			assertModelTextContains(t, input, "linux.filesystem", "No authorized filesystem tool is available")
			writeE2EModelText(writer, "The investigation is inconclusive because the required read-only checks are unavailable.")
		}
	}))
	defer model.Close()

	server := newOpsTestServer(t, model.URL+"/v1", "api-secret", nil)
	if err := registerOpsInvestigationPlanTool(server.registry); err != nil {
		t.Fatal(err)
	}
	server.policy.Allowed[localInvestigationPlanToolName] = true
	auditStore, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	server.SetAuditStore(auditStore)
	alertStore, err := alerts.OpenStore(filepath.Join(t.TempDir(), "alerts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	alertConfig := filepath.Join(t.TempDir(), "alert-sources.json")
	if err := os.WriteFile(alertConfig, []byte(`{"version":1,"sources":[{"name":"canonical","type":"generic","allow_unauthenticated":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	alertRegistry, err := alerts.LoadRegistry(alertConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		server.CloseAlertWorker()
		_ = alertStore.Close()
		_ = auditStore.Close()
	})
	if err := server.SetAlertRuntime(alertRegistry, alertStore); err != nil {
		t.Fatal(err)
	}

	payload := []byte(`{"schema_version":1,"alerts":[{
  "schema_version":1,"state":"firing","name":"DiskFreeLow","severity":"critical",
  "summary":"Only 4% remains on /var","entity":{"kind":"host","id":"app-03"},
  "started_at":"2026-09-07T03:00:00Z","observed_at":"2026-09-07T03:01:00Z",
  "labels":{"mount":"/var"}
}]}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/canonical/webhook", bytes.NewReader(payload))
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("canonical alert status=%d body=%s", response.Code, response.Body.String())
	}
	var accepted opsAlertIngestionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	jobContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	job, err := alertStore.WaitJob(jobContext, accepted.Events[0].JobID)
	if err != nil {
		t.Fatal(err)
	}
	run, exists := auditStore.Get(job.RunID)
	if job.Status != alerts.JobSucceeded || !exists || run.Investigation == nil ||
		run.Investigation.FindingStatus != investigation.FindingInconclusive ||
		len(run.Investigation.Evidence) != 0 || run.Investigation.ProductionWrites != 0 {
		t.Fatalf("missing capabilities did not produce a valid inconclusive Run: job=%+v run=%+v", job, run)
	}
	if modelCalls.Load() != 3 {
		t.Fatalf("model calls=%d, want 3", modelCalls.Load())
	}
	foundGaps := false
	for _, event := range run.Events {
		if event.Type == "tool_call" && event.Metadata["tool"] == localInvestigationPlanToolName &&
			bytes.Contains(event.Arguments, []byte(`"capability":"linux.filesystem"`)) &&
			bytes.Contains(event.Arguments, []byte(`"capability":"linux.directory_usage"`)) {
			foundGaps = true
		}
		if event.Type == "tool_call" && event.Metadata["tool"] != localInvestigationPlanToolName {
			t.Errorf("unexpected diagnostic tool was executed: %+v", event)
		}
	}
	if !foundGaps {
		t.Fatalf("capability gaps were not persisted in the Run: %+v", run.Events)
	}
}

func requestHasTool(input openAIToolRequest, name string) bool {
	alias := openAIToolAlias(name)
	for _, tool := range input.Tools {
		if tool.Function.Name == alias {
			return true
		}
	}
	return false
}

func requestHasToolResult(input openAIToolRequest, callID string) bool {
	for _, message := range input.Messages {
		if message.Role == "tool" && message.ToolCallID == callID {
			return true
		}
	}
	return false
}

func assertModelTextContains(t *testing.T, input openAIToolRequest, expected ...string) {
	t.Helper()
	var content strings.Builder
	for _, message := range input.Messages {
		if message.Content != nil {
			content.WriteString(*message.Content)
		}
		for _, call := range message.ToolCalls {
			content.WriteString(call.Function.Arguments)
		}
	}
	for _, value := range expected {
		if !strings.Contains(content.String(), value) {
			t.Errorf("model request is missing %q: %s", value, content.String())
		}
	}
}

func writeE2EToolCall(writer http.ResponseWriter, name, id, arguments string) {
	writeJSON(writer, http.StatusOK, map[string]any{"choices": []any{map[string]any{
		"message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
			"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": arguments},
		}}}, "finish_reason": "tool_calls",
	}}})
}

func writeE2EModelText(writer http.ResponseWriter, content string) {
	writeJSON(writer, http.StatusOK, map[string]any{"choices": []any{map[string]any{
		"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop",
	}}})
}

func highCPUTestIndex() *RAGIndex {
	chunk := RAGChunk{
		DocumentID: "linux-high-cpu.md", Title: "Linux High CPU", Chunk: 0, Source: "test-runbooks",
		Text: "For HighCPU on a host, resolve inventory, inspect top processes, and follow technology-specific read-only checks.", Tokens: 17,
	}
	index := &RAGIndex{
		Version: rag.CurrentIndexVersion, ManifestSHA256: strings.Repeat("a", 64), ChunkRunes: 1200,
		OverlapRunes: 100, AverageTokens: 17, Chunks: []RAGChunk{chunk}, Postings: make(map[string][]RAGPosting),
	}
	frequencies := make(map[string]int)
	for _, term := range rag.TokenizeTerms(chunk.Title + " " + chunk.Text) {
		frequencies[term]++
	}
	for term, frequency := range frequencies {
		index.Postings[term] = []RAGPosting{{ChunkIndex: 0, Frequency: frequency}}
	}
	return index
}

func registerE2EHostTool(t *testing.T, server *opsServer, name, output string, mu *sync.Mutex, executions *[]string) {
	t.Helper()
	if err := server.registry.Register(ToolDefinition{
		Name: name, Description: "Deterministic read-only test tool for " + name,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"host":{"type":"string"}},"required":["host"],"additionalProperties":false}`),
		Permission:  ToolPermissionRead,
		ValidateArguments: func(raw json.RawMessage) error {
			var arguments struct {
				Host string `json:"host"`
			}
			if err := decodeStrictJSONObject(raw, &arguments); err != nil {
				return err
			}
			if arguments.Host != "pg01" {
				return fmt.Errorf("host must be pg01")
			}
			return nil
		},
		Handler: func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
			mu.Lock()
			*executions = append(*executions, name)
			mu.Unlock()
			return json.RawMessage(output), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	server.policy.Allowed[name] = true
}

func sendE2EAlert(t *testing.T, server *opsServer, payload []byte, wantStatus int) opsAlertIngestionResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/alertmanager-main/webhook", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer source-secret")
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("alert status=%d want=%d body=%s", response.Code, wantStatus, response.Body.String())
	}
	var output opsAlertIngestionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	return output
}

func assertE2ERunEvents(t *testing.T, run audit.Run) {
	t.Helper()
	wanted := map[string]bool{
		"alert_received": false, "context_retrieved": false, "skill_selected": false,
		"investigation_result": false, "notification_delivery": false,
	}
	toolCalls := make(map[string]bool)
	for _, event := range run.Events {
		if _, exists := wanted[event.Type]; exists {
			wanted[event.Type] = true
		}
		if event.Type == "tool_call" {
			toolCalls[event.Metadata["tool"]] = true
		}
	}
	for eventType, found := range wanted {
		if !found {
			t.Errorf("Run is missing %s event: %+v", eventType, run.Events)
		}
	}
	for _, tool := range []string{localRAGSearchToolName, localInvestigationPlanToolName, "inventory.host", "linux.processes", "postgres.activity"} {
		if !toolCalls[tool] {
			t.Errorf("Run is missing %s tool event: %+v", tool, run.Events)
		}
	}
}

func assertE2EQueryAPIs(t *testing.T, server *opsServer, job alerts.Job, incidentID string) {
	t.Helper()
	for _, item := range []struct {
		path, marker string
	}{
		{"/api/v1/investigation-jobs/" + job.ID, job.RunID},
		{"/api/v1/incidents/" + incidentID, job.ID},
		{"/api/v1/runs/" + job.RunID, "postgres.activity"},
		{"/api/v1/runs?limit=10", job.RunID},
	} {
		request := httptest.NewRequest(http.MethodGet, item.path, nil)
		request.Header.Set("Authorization", "Bearer api-secret")
		response := httptest.NewRecorder()
		server.handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), item.marker) {
			t.Errorf("GET %s status=%d marker=%q body=%s", item.path, response.Code, item.marker, response.Body.String())
		}
	}
}
