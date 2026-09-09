package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"akritas/internal/alerts"
	"akritas/internal/audit"
)

func TestConfiguredAlertIngressAndIncidentAPI(t *testing.T) {
	server := newOpsTestServer(t, "http://127.0.0.1:1/v1", "api-secret", nil)
	configPath := filepath.Join(t.TempDir(), "alerts.json")
	if err := os.WriteFile(configPath, []byte(`{"version":1,"sources":[{"name":"monitor","type":"generic","allow_unauthenticated":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := alerts.LoadRegistry(configPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := alerts.OpenStore(filepath.Join(t.TempDir(), "alerts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.CloseAlertWorker(); _ = store.Close() })
	if err := server.SetAlertRuntime(registry, store); err != nil {
		t.Fatal(err)
	}
	payload := `{"schema_version":1,"alerts":[{"schema_version":1,"state":"resolved","name":"EndpointDown","severity":"info","summary":"Endpoint recovered","entity":{"kind":"service","id":"payments"},"started_at":"2026-09-04T08:00:00Z","ended_at":"2026-09-04T08:01:00Z","observed_at":"2026-09-04T08:01:00Z"}]}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/monitor/webhook", bytes.NewBufferString(payload))
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("ingress status=%d body=%s", response.Code, response.Body.String())
	}
	var accepted opsAlertIngestionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if !accepted.Accepted || len(accepted.Events) != 1 || accepted.Events[0].IncidentID == "" || accepted.Events[0].JobID != "" {
		t.Fatalf("unexpected ingestion response: %+v", accepted)
	}
	get := httptest.NewRequest(http.MethodGet, "/api/v1/incidents/"+accepted.Events[0].IncidentID, nil)
	get.Header.Set("Authorization", "Bearer api-secret")
	detail := httptest.NewRecorder()
	server.handler().ServeHTTP(detail, get)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"state":"resolved"`) {
		t.Fatalf("incident status=%d body=%s", detail.Code, detail.Body.String())
	}
}

func TestFailedAlertJobExposesSanitizedDetailAndRunID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, `model unavailable: token=upstream-secret`, http.StatusBadGateway)
	}))
	defer upstream.Close()
	server := newOpsTestServer(t, upstream.URL+"/v1", "api-secret", nil)
	auditStore, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	server.SetAuditStore(auditStore)
	configPath := filepath.Join(t.TempDir(), "alerts.json")
	if err := os.WriteFile(configPath, []byte(`{"version":1,"sources":[{"name":"monitor","type":"generic","allow_unauthenticated":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := alerts.LoadRegistry(configPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	alertStore, err := alerts.OpenStore(filepath.Join(t.TempDir(), "alerts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		server.CloseAlertWorker()
		_ = alertStore.Close()
		_ = auditStore.Close()
	})
	if err := server.SetAlertRuntime(registry, alertStore); err != nil {
		t.Fatal(err)
	}
	payload := `{"schema_version":1,"alerts":[{"schema_version":1,"state":"firing","name":"HighCPU","severity":"critical","summary":"CPU high","entity":{"kind":"host","id":"pg01"},"started_at":"2026-09-06T23:50:00Z","observed_at":"2026-09-06T23:51:00Z"}]}`
	ingest := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/monitor/webhook", bytes.NewBufferString(payload))
	ingestResponse := httptest.NewRecorder()
	server.handler().ServeHTTP(ingestResponse, ingest)
	if ingestResponse.Code != http.StatusAccepted {
		t.Fatalf("ingress status=%d body=%s", ingestResponse.Code, ingestResponse.Body.String())
	}
	var accepted opsAlertIngestionResponse
	if err := json.Unmarshal(ingestResponse.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	jobContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	job, err := alertStore.WaitJob(jobContext, accepted.Events[0].JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != alerts.JobFailed || job.RunID == "" || job.Error != "investigation_failed" ||
		!strings.Contains(job.ErrorDetail, "HTTP 502") || strings.Contains(job.ErrorDetail, "upstream-secret") {
		t.Fatalf("unexpected failed job: %+v", job)
	}
	run, exists := auditStore.Get(job.RunID)
	if !exists || run.Status != audit.RunFailed {
		t.Fatalf("failed audit Run is missing: %+v exists=%v", run, exists)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/investigation-jobs/"+job.ID, nil)
	request.Header.Set("Authorization", "Bearer api-secret")
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("job status=%d body=%s", response.Code, response.Body.String())
	}
	var exposed alerts.Job
	if err := json.Unmarshal(response.Body.Bytes(), &exposed); err != nil {
		t.Fatal(err)
	}
	if exposed.RunID != job.RunID || exposed.ErrorDetail != job.ErrorDetail {
		t.Fatalf("job API omitted diagnostics: %+v", exposed)
	}
}

func TestRunFollowUpAppendsToCompletedRun(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input openAIToolRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if len(input.Messages) < 2 || input.Messages[1].Content == nil || !strings.Contains(*input.Messages[1].Content, "run_test") {
			t.Errorf("Run context is missing: %+v", input.Messages)
		}
		writeJSON(writer, http.StatusOK, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "The disk check was conclusive."}}}})
	}))
	defer upstream.Close()
	server := newOpsTestServer(t, upstream.URL+"/v1", "secret", nil)
	store, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server.SetAuditStore(store)
	run, err := store.StartRun("alert:generic", "alert-source:test", "", "akritas", map[string]string{"alert": "DiskFreeLow"})
	if err != nil {
		t.Fatal(err)
	}
	// Use a stable marker that the upstream assertion can find in the context.
	if err := store.AddEvent(run.ID, "marker", "ok", map[string]string{"value": "run_test"}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(run.ID, audit.RunSucceeded, "", nil); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+run.ID+"/chat", bytes.NewBufferString(`{"message":"Why was disk saturation ruled out?"}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "conclusive") {
		t.Fatalf("follow-up status=%d body=%s", response.Code, response.Body.String())
	}
	loaded, _ := store.Get(run.ID)
	if len(loaded.Events) != 3 || loaded.Events[1].Type != "follow_up" || loaded.Events[2].Status != "succeeded" {
		t.Fatalf("follow-up timeline was not appended: %+v", loaded.Events)
	}
}

func TestRunPagesServeEmbeddedUI(t *testing.T) {
	server := newOpsTestServer(t, "http://127.0.0.1:1/v1", "", nil)
	for _, path := range []string{"/runs", "/runs/run_123"} {
		response := httptest.NewRecorder()
		server.handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Investigation Runs") {
			t.Fatalf("path=%s status=%d", path, response.Code)
		}
	}
}
