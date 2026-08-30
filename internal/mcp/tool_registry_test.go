package mcp

import (
	"context"
	"encoding/json"
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
}
