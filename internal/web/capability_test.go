package web

import (
	"context"
	"encoding/json"
	"testing"
)

func TestOpsCapabilityGapToolValidatesAndBuildsReport(t *testing.T) {
	registry := NewToolRegistry()
	if err := registerOpsCapabilityGapTool(registry); err != nil {
		t.Fatal(err)
	}
	policy := NamedToolPolicy{Allowed: map[string]bool{localCapabilityGapToolName: true}}
	call := ToolCall{
		ID: "call-gap", Name: localCapabilityGapToolName,
		Arguments: json.RawMessage(`{"gaps":[{"step":"Проверить CPU","capability":"metrics.query_range","reason":"Нет инструмента метрик"}]}`),
	}
	toolResult := registry.Execute(context.Background(), call, policy)
	if toolResult.Error != nil {
		t.Fatalf("report tool failed: %+v", toolResult.Error)
	}
	loopResult := openAIToolLoopResult{Calls: []ToolCall{call}, Results: []ToolResult{toolResult}}
	gaps := buildOpsCapabilityGaps(loopResult)
	if len(gaps) != 1 || gaps[0].Capability != "metrics.query_range" ||
		gaps[0].Step != "Проверить CPU" {
		t.Fatalf("unexpected capability gaps: %+v", gaps)
	}

	call.Arguments = json.RawMessage(`{"gaps":[{"step":"CPU","capability":"metrics.query","reason":"нет"},{"step":"CPU","capability":"metrics.query","reason":"нет"}]}`)
	invalid := registry.Execute(context.Background(), call, policy)
	if invalid.Error == nil || invalid.Error.Code != "invalid_arguments" {
		t.Fatalf("duplicate capability gap was accepted: %+v", invalid)
	}
}
