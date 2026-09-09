//go:build smoke

package smoke_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const smokeAPIKey = "smoke-api-key"

func TestCompiledServerAlertLifecycle(t *testing.T) {
	repository := repositoryRoot(t)
	temporary := t.TempDir()
	binary := filepath.Join(temporary, "akritas")
	build := exec.Command("go", "build", "-o", binary, "./cmd/akritas")
	build.Dir = repository
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Akritas: %v\n%s", err, output)
	}

	var modelCalls atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/models":
			writeJSON(t, writer, map[string]any{
				"object": "list",
				"data":   []any{map[string]any{"id": "smoke-model", "object": "model"}},
			})
		case request.Method == http.MethodPost && request.URL.Path == "/v1/chat/completions":
			modelCalls.Add(1)
			var input struct {
				Tools []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tools"`
			}
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				http.Error(writer, "invalid request", http.StatusBadRequest)
				return
			}
			names := make(map[string]bool, len(input.Tools))
			for _, tool := range input.Tools {
				names[tool.Function.Name] = true
			}
			switch {
			case names[toolAlias("local.investigation.submit_plan")]:
				writeToolCall(t, writer, toolAlias("local.investigation.submit_plan"), "smoke-plan", `{
  "checks": [],
  "gaps": [{"step":"Inspect the affected host","capability":"linux.host.inspect","reason":"The smoke server has no external diagnostic tools"}]
}`)
			case names[toolAlias("local.investigation.submit_result")]:
				writeToolCall(t, writer, toolAlias("local.investigation.submit_result"), "smoke-result", `{
  "finding_status":"inconclusive",
  "actionability":"requires_human",
  "confidence":"low",
  "summary":"The alert was accepted but the smoke environment has no diagnostic tools.",
  "hypothesis":"The reported service may be unavailable.",
  "impact":"The smoke service endpoint may be unavailable.",
  "evidence":[],
  "ruled_out":[],
  "affected_components":["service:smoke-service"],
  "recommended_actions":["Run an authorized service health check."],
  "production_writes":0
}`)
			default:
				writeModelText(t, writer, "The alert requires an authorized service health check.")
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	defer model.Close()

	alertConfig := filepath.Join(temporary, "alerts.json")
	if err := os.WriteFile(alertConfig, []byte(`{"version":1,"sources":[{"name":"smoke","type":"generic","allow_unauthenticated":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	address := reserveAddress(t)
	logPath := filepath.Join(temporary, "akritas.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary,
		"serve",
		"-address", address,
		"-base-url", model.URL+"/v1",
		"-api-key-env", "AKRITAS_SMOKE_API_KEY",
		"-system-instructions", filepath.Join(repository, "instructions", "SYSTEM.md"),
		"-skills-dir", "",
		"-alert-sources-config", alertConfig,
		"-alert-store", filepath.Join(temporary, "alerts.jsonl"),
		"-audit-log", filepath.Join(temporary, "audit.jsonl"),
		"-request-timeout", "5s",
	)
	command.Dir = repository
	command.Env = append(os.Environ(), "AKRITAS_SMOKE_API_KEY="+smokeAPIKey)
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Signal(os.Interrupt)
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			if command.Process != nil {
				_ = command.Process.Kill()
			}
			<-done
		}
		_ = logFile.Close()
	})

	baseURL := "http://" + address
	waitForReady(t, baseURL+"/health", done, logPath)
	health := request(t, http.MethodGet, baseURL+"/health", "", nil, http.StatusOK)
	for _, marker := range []string{`"status":"ok"`, `"alert_sources":1`, `"alert_ingestion":true`} {
		if !bytes.Contains(health, []byte(marker)) {
			t.Fatalf("health response is missing %s: %s", marker, health)
		}
	}
	request(t, http.MethodGet, baseURL+"/v1/models", "", nil, http.StatusUnauthorized)
	models := request(t, http.MethodGet, baseURL+"/v1/models", smokeAPIKey, nil, http.StatusOK)
	if !bytes.Contains(models, []byte(`"id":"akritas"`)) {
		t.Fatalf("model list does not expose Akritas: %s", models)
	}
	index := request(t, http.MethodGet, baseURL+"/", "", nil, http.StatusOK)
	if !bytes.Contains(index, []byte("Akritas")) {
		t.Fatalf("Web UI marker is missing: %s", index)
	}

	payload := []byte(`{"schema_version":1,"alerts":[{"schema_version":1,"state":"firing","name":"SmokeServiceDown","severity":"critical","summary":"The smoke service is unavailable","entity":{"kind":"service","id":"smoke-service"},"started_at":"2026-09-09T09:00:00Z","observed_at":"2026-09-09T09:01:00Z","deduplication_key":"smoke-delivery-1","correlation_key":"smoke-incident-1"}]}`)
	acceptedBody := request(t, http.MethodPost, baseURL+"/api/v1/alerts/smoke/webhook", "", payload, http.StatusAccepted)
	var accepted struct {
		Accepted bool `json:"accepted"`
		Events   []struct {
			EventID    string `json:"event_id"`
			IncidentID string `json:"incident_id"`
			JobID      string `json:"job_id"`
			Status     string `json:"status"`
		} `json:"events"`
	}
	if err := json.Unmarshal(acceptedBody, &accepted); err != nil {
		t.Fatal(err)
	}
	if !accepted.Accepted || len(accepted.Events) != 1 || accepted.Events[0].JobID == "" {
		t.Fatalf("alert was not durably accepted: %s", acceptedBody)
	}
	job := waitForJob(t, baseURL, accepted.Events[0].JobID)
	if job.Status != "succeeded" || job.RunID == "" {
		t.Fatalf("investigation job did not succeed: %+v", job)
	}
	run := request(t, http.MethodGet, baseURL+"/api/v1/runs/"+job.RunID, smokeAPIKey, nil, http.StatusOK)
	for _, marker := range []string{`"status":"succeeded"`, `"finding_status":"inconclusive"`, `"production_writes":0`} {
		if !bytes.Contains(run, []byte(marker)) {
			t.Fatalf("Run response is missing %s: %s", marker, run)
		}
	}
	duplicate := request(t, http.MethodPost, baseURL+"/api/v1/alerts/smoke/webhook", "", payload, http.StatusOK)
	if !bytes.Contains(duplicate, []byte(`"status":"duplicate"`)) {
		t.Fatalf("duplicate alert was not suppressed: %s", duplicate)
	}
	if calls := modelCalls.Load(); calls != 3 {
		t.Fatalf("model calls=%d, want 3", calls)
	}
}

type smokeJob struct {
	Status string `json:"status"`
	RunID  string `json:"run_id"`
	Error  string `json:"error"`
}

func waitForJob(t *testing.T, baseURL, id string) smokeJob {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		body := request(t, http.MethodGet, baseURL+"/api/v1/investigation-jobs/"+id, smokeAPIKey, nil, http.StatusOK)
		var job smokeJob
		if err := json.Unmarshal(body, &job); err != nil {
			t.Fatal(err)
		}
		if job.Status == "succeeded" || job.Status == "failed" {
			return job
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("timed out waiting for investigation job")
	return smokeJob{}
}

func waitForReady(t *testing.T, url string, done chan error, logPath string) {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			done <- err
			logBody, _ := os.ReadFile(logPath)
			t.Fatalf("Akritas exited before becoming ready: %v\n%s", err, logBody)
		default:
		}
		response, err := client.Get(url)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	logBody, _ := os.ReadFile(logPath)
	t.Fatalf("Akritas did not become ready\n%s", logBody)
}

func request(t *testing.T, method, url, token string, body []byte, wantStatus int) []byte {
	t.Helper()
	request, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var output bytes.Buffer
	if _, err := output.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, url, response.StatusCode, wantStatus, output.Bytes())
	}
	return output.Bytes()
}

func reserveAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate smoke test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}

func toolAlias(name string) string {
	var builder strings.Builder
	builder.WriteString("akritas_")
	for _, char := range name {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('_')
		}
	}
	digest := sha256.Sum256([]byte(name))
	alias := builder.String()
	if len(alias) > 55 {
		alias = alias[:55]
	}
	return alias + "_" + hex.EncodeToString(digest[:4])
}

func writeToolCall(t *testing.T, writer http.ResponseWriter, name, id, arguments string) {
	t.Helper()
	writeJSON(t, writer, map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{
				"role": "assistant", "content": nil,
				"tool_calls": []any{map[string]any{
					"id": id, "type": "function",
					"function": map[string]any{"name": name, "arguments": arguments},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	})
}

func writeModelText(t *testing.T, writer http.ResponseWriter, content string) {
	t.Helper()
	writeJSON(t, writer, map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	})
}

func writeJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode mock model response: %v", err)
	}
}

func TestToolAliasMatchesReleaseContract(t *testing.T) {
	if got := toolAlias("local.investigation.submit_plan"); got != "akritas_local_investigation_submit_plan_175a114e" {
		t.Fatalf("tool alias=%q", got)
	}
	if !strings.HasPrefix(toolAlias("local.investigation.submit_result"), "akritas_") {
		t.Fatal("tool alias prefix is missing")
	}
}
