package web

import (
	"context"
	"encoding/json"
	"testing"
)

func TestOpsInvestigationPlanToolValidatesAndBuildsGaps(t *testing.T) {
	registry := NewToolRegistry()
	if err := registerOpsInvestigationPlanTool(registry); err != nil {
		t.Fatal(err)
	}
	policy := NamedToolPolicy{Allowed: map[string]bool{localInvestigationPlanToolName: true}}
	call := ToolCall{
		ID: "call-plan", Name: localInvestigationPlanToolName,
		Arguments: json.RawMessage(`{"checks":[{"step":"Check CPU","tool":"mcp.metrics.query_range","arguments":{"query":"cpu"},"reason":"Metrics tool is available"}],"gaps":[{"step":"Check logs","capability":"logs.search","reason":"No logs tool is available"}]}`),
	}
	toolResult := registry.Execute(context.Background(), call, policy)
	if toolResult.Error != nil {
		t.Fatalf("plan tool failed: %+v", toolResult.Error)
	}
	loopResult := openAIToolLoopResult{Calls: []ToolCall{call}, Results: []ToolResult{toolResult}}
	gaps := buildOpsCapabilityGaps(loopResult)
	if len(gaps) != 1 || gaps[0].Capability != "logs.search" || gaps[0].Step != "Check logs" {
		t.Fatalf("unexpected capability gaps: %+v", gaps)
	}

	call.Arguments = json.RawMessage(`{"checks":[{"step":"CPU","tool":"metrics.query","arguments":{},"reason":"available"}],"gaps":[{"step":"CPU","capability":"metrics.query","reason":"unavailable"}]}`)
	invalid := registry.Execute(context.Background(), call, policy)
	if invalid.Error == nil || invalid.Error.Code != "invalid_arguments" {
		t.Fatalf("check duplicated as a gap was accepted: %+v", invalid)
	}
}
