package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestServeStdioListsAndCallsReadOnlyTool(t *testing.T) {
	registry := NewToolRegistry()
	if err := RegisterEchoTool(registry); err != nil {
		t.Fatal(err)
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"local.echo","arguments":{"text":"ok"}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := ServeStdio(context.Background(), strings.NewReader(input), &output, "test-server", "0.1.0", registry); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("responses=%d, want 3: %s", len(lines), output.String())
	}
	var listed struct {
		Result MCPListToolsResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Result.Tools) != 1 || listed.Result.Tools[0].Annotations.ReadOnlyHint == nil ||
		!*listed.Result.Tools[0].Annotations.ReadOnlyHint {
		t.Fatalf("unexpected tools/list response: %+v", listed.Result)
	}
	var called struct {
		Result MCPCallToolResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &called); err != nil {
		t.Fatal(err)
	}
	if called.Result.IsError || string(called.Result.StructuredContent) != `{"text":"ok"}` {
		t.Fatalf("unexpected tools/call response: %+v", called.Result)
	}
}

func TestServeStdioRejectsCallBeforeInitialization(t *testing.T) {
	registry := NewToolRegistry()
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n"
	var output bytes.Buffer
	if err := ServeStdio(context.Background(), strings.NewReader(input), &output, "test-server", "0.1.0", registry); err != nil {
		t.Fatal(err)
	}
	var response mcpRPCMessage
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != -32002 {
		t.Fatalf("unexpected response: %+v", response)
	}
}
