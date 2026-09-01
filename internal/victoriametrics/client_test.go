package victoriametrics

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"akritas/internal/mcp"
)

func TestClientDebugLogsBoundedErrorResponseWithoutRequestSecrets(t *testing.T) {
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`unsupported path requested: "/api/v1/query"`))
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		BaseURL: server.URL, BearerToken: "secret-token", Debug: true,
		Logger: log.New(&logs, "", 0), HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Query(context.Background(), url.Values{"query": {"secret-query"}})
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("unexpected error: %v", err)
	}
	logged := logs.String()
	for _, expected := range []string{
		`level=debug component=victoriametrics event=response_error`,
		`reason="invalid_json"`, `endpoint="/api/v1/query"`,
		`status_code=400`, `content_type="text/plain; charset=utf-8"`,
		`unsupported path requested`,
	} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("debug log %q does not contain %q", logged, expected)
		}
	}
	if strings.Contains(logged, "secret-token") || strings.Contains(logged, "secret-query") {
		t.Fatalf("debug log contains request secret: %q", logged)
	}
}

func TestClientDoesNotLogErrorResponseWithoutDebug(t *testing.T) {
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte("proxy failure"))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{
		BaseURL: server.URL, Logger: log.New(&logs, "", 0), HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = client.Query(context.Background(), url.Values{"query": {"up"}})
	if logs.Len() != 0 {
		t.Fatalf("unexpected debug log: %q", logs.String())
	}
}

func TestClientBuildsBoundedAuthenticatedVictoriaMetricsRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/select/42/prometheus/api/v1/query_range" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer secret" ||
			request.Header.Get("AccountID") != "42" || request.Header.Get("ProjectID") != "7" {
			t.Fatalf("unexpected headers: %+v", request.Header)
		}
		query := request.URL.Query()
		if query.Get("query") != "up" || query.Get("start") != "-1h" ||
			query.Get("end") != "now" || query.Get("step") != "1m" ||
			query.Get("extra_label") != "team=ops" {
			t.Fatalf("unexpected query values: %v", query)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		BaseURL:     server.URL + "/select/42/prometheus?extra_label=team%3Dops",
		BearerToken: "secret", AccountID: "42", ProjectID: "7",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.QueryRange(context.Background(), url.Values{
		"query": {"up"}, "start": {"-1h"}, "end": {"now"}, "step": {"1m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(result) {
		t.Fatalf("invalid result: %s", result)
	}
}

func TestClientRejectsOversizedAndRemoteErrorResponses(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`{"status":"success","data":"` + strings.Repeat("x", 1100) + `"}`))
		}))
		defer server.Close()
		client, err := NewClient(ClientOptions{BaseURL: server.URL, MaximumResponseBytes: 1024, HTTPClient: server.Client()})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Query(context.Background(), url.Values{"query": {"up"}}); err == nil ||
			!strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("remote error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = writer.Write([]byte(`{"status":"error","errorType":"bad_data","error":"invalid query"}`))
		}))
		defer server.Close()
		client, err := NewClient(ClientOptions{BaseURL: server.URL, HTTPClient: server.Client()})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Query(context.Background(), url.Values{"query": {"up"}}); err == nil ||
			!strings.Contains(err.Error(), "bad_data") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestRegisterToolsExposesOnlyBoundedReadOperations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/prometheus/api/v1/series" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		if request.URL.Query().Get("match[]") != `{job="api"}` || request.URL.Query().Get("limit") != "10" {
			t.Fatalf("unexpected values: %v", request.URL.Query())
		}
		_, _ = writer.Write([]byte(`{"status":"success","data":[{"__name__":"up","job":"api"}]}`))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL + "/prometheus", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	registry := mcp.NewToolRegistry()
	if err := RegisterTools(registry, client); err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 5 {
		t.Fatalf("tools=%d, want 5", len(definitions))
	}
	for _, definition := range definitions {
		if definition.Permission != mcp.ToolPermissionRead {
			t.Fatalf("tool %s permission=%s", definition.Name, definition.Permission)
		}
	}
	arguments := json.RawMessage(`{"match":["{job=\"api\"}"],"limit":10}`)
	result := registry.Execute(context.Background(), mcp.ToolCall{
		ID: "test", Name: "series", Arguments: arguments,
	}, mcp.StaticToolPolicy{Allowed: map[mcp.ToolPermission]bool{mcp.ToolPermissionRead: true}})
	if result.Error != nil || !strings.Contains(string(result.Output), `"job":"api"`) {
		t.Fatalf("unexpected result: %+v", result)
	}

	invalid := registry.Execute(context.Background(), mcp.ToolCall{
		ID: "invalid", Name: "series", Arguments: json.RawMessage(`{"match":[],"limit":1001}`),
	}, mcp.StaticToolPolicy{Allowed: map[mcp.ToolPermission]bool{mcp.ToolPermissionRead: true}})
	if invalid.Error == nil || invalid.Error.Code != "invalid_arguments" {
		t.Fatalf("unexpected invalid result: %+v", invalid)
	}
}

func TestNewClientRejectsCredentialAndURLAmbiguity(t *testing.T) {
	cases := []ClientOptions{
		{BaseURL: "file:///tmp/vm"},
		{BaseURL: "https://user@example.com", Username: "user", Password: "pass"},
		{BaseURL: "https://example.com", BearerToken: "token", Username: "user", Password: "pass"},
		{BaseURL: "https://example.com", ProjectID: "7"},
	}
	for _, options := range cases {
		if _, err := NewClient(options); err == nil {
			t.Fatalf("expected rejection for %+v", options)
		}
	}
}
