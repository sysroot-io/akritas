package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
	"testing"
	"time"
)

func TestToolRegistryDeniesCallsWithoutExplicitPolicy(t *testing.T) {
	registry := NewToolRegistry()
	if err := registry.Register(ToolDefinition{
		Name: "security.read", Description: "security test", Permission: ToolPermissionRead,
		InputSchema:       json.RawMessage(`{"type":"object","additionalProperties":false}`),
		ValidateArguments: func(json.RawMessage) error { return nil },
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
		Timeout: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	result := registry.Execute(context.Background(), ToolCall{
		ID: "call_1", Name: "security.read", Arguments: json.RawMessage(`{}`),
	}, nil)
	if result.Error == nil || result.Error.Code != "permission_denied" {
		t.Fatalf("tool executed without explicit policy: %+v", result)
	}
}

func TestToolRegistryEnforcesExecutionTimeout(t *testing.T) {
	var logs bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previousWriter)
	registry := NewToolRegistry()
	if err := registry.Register(ToolDefinition{
		Name: "security.slow", Description: "security timeout test", Permission: ToolPermissionRead,
		InputSchema:       json.RawMessage(`{"type":"object","additionalProperties":false}`),
		ValidateArguments: func(json.RawMessage) error { return nil },
		Handler: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Timeout: 10 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	result := registry.Execute(context.Background(), ToolCall{
		ID: "call_1", Name: "security.slow", Arguments: json.RawMessage(`{}`),
	}, NamedToolPolicy{Allowed: map[string]bool{"security.slow": true}})
	if result.Error == nil || result.Error.Code != "timeout" {
		t.Fatalf("slow tool was not terminated: %+v", result)
	}
	if result.DurationMS < 5 {
		t.Fatalf("tool duration=%dms, want at least 5ms", result.DurationMS)
	}
	logged := logs.String()
	if !strings.Contains(logged, `event=start tool="security.slow" call_id="call_1"`) ||
		!strings.Contains(logged, `event=finish tool="security.slow" call_id="call_1" status="timeout"`) {
		t.Fatalf("tool lifecycle logs are incomplete: %s", logged)
	}
}
