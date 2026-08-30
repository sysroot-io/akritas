package mcp

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestMCPStdioHelperProcess(t *testing.T) {
	if os.Getenv("AKRITAS_MCP_HELPER") != "1" {
		return
	}
	if err := serveMCPStdioEcho(os.Stdin, os.Stdout); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestMCPStdioLifecycleAndRegistryAdapter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := StartMCPStdioClient(ctx, MCPStdioConfig{
		Name:        "test",
		Command:     os.Args[0],
		Arguments:   []string{"-test.run=^TestMCPStdioHelperProcess$"},
		Environment: []string{"AKRITAS_MCP_HELPER=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	}()
	initialize := client.InitializeResult()
	if initialize.ProtocolVersion != mcpProtocolVersion {
		t.Fatalf("protocol=%q", initialize.ProtocolVersion)
	}
	if initialize.ServerInfo.Name != "akritas-echo" {
		t.Fatalf("server=%q", initialize.ServerInfo.Name)
	}

	registry := NewToolRegistry()
	bindings, err := RegisterMCPTools(
		ctx,
		registry,
		client,
		"mcp.test",
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 ||
		bindings[0].HostName != "mcp.test.echo" ||
		bindings[0].Permission != ToolPermissionRead {
		t.Fatalf("unexpected bindings: %+v", bindings)
	}

	arguments := json.RawMessage(`{"text":"hello"}`)
	denied := registry.Execute(ctx, ToolCall{
		ID: "call-denied", Name: "mcp.test.echo", Arguments: arguments,
	}, StaticToolPolicy{
		Allowed: map[ToolPermission]bool{ToolPermissionWrite: true},
	})
	if denied.Error == nil || denied.Error.Code != "permission_denied" {
		t.Fatalf("expected permission denial, got %+v", denied)
	}

	allowed := registry.Execute(ctx, ToolCall{
		ID: "call-allowed", Name: "mcp.test.echo", Arguments: arguments,
	}, StaticToolPolicy{
		Allowed: map[ToolPermission]bool{ToolPermissionRead: true},
	})
	if allowed.Error != nil {
		t.Fatalf("MCP call failed: %+v", allowed.Error)
	}
	var output MCPCallToolResult
	if err := json.Unmarshal(allowed.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.IsError || len(output.Content) != 1 {
		t.Fatalf("unexpected MCP output: %+v", output)
	}
	var structured map[string]string
	if err := json.Unmarshal(output.StructuredContent, &structured); err != nil {
		t.Fatal(err)
	}
	if structured["text"] != "hello" {
		t.Fatalf("structured result=%v", structured)
	}
}

func TestMCPRegistryAdapterValidatesArgumentsBeforeRemoteCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := StartMCPStdioClient(ctx, MCPStdioConfig{
		Name:        "validation",
		Command:     os.Args[0],
		Arguments:   []string{"-test.run=^TestMCPStdioHelperProcess$"},
		Environment: []string{"AKRITAS_MCP_HELPER=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	registry := NewToolRegistry()
	if _, err := RegisterMCPTools(
		ctx,
		registry,
		client,
		"mcp.validation",
		time.Second,
	); err != nil {
		t.Fatal(err)
	}
	result := registry.Execute(ctx, ToolCall{
		ID:   "bad-arguments",
		Name: "mcp.validation.echo",
		Arguments: json.RawMessage(
			`{"text":"hello","unexpected":true}`,
		),
	}, StaticToolPolicy{
		Allowed: map[ToolPermission]bool{ToolPermissionRead: true},
	})
	if result.Error == nil || result.Error.Code != "invalid_arguments" {
		t.Fatalf("expected invalid_arguments, got %+v", result)
	}
}

func TestMCPToolExecutionErrorRemainsModelVisibleResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := StartMCPStdioClient(ctx, MCPStdioConfig{
		Name:        "tool-error",
		Command:     os.Args[0],
		Arguments:   []string{"-test.run=^TestMCPStdioHelperProcess$"},
		Environment: []string{"AKRITAS_MCP_HELPER=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.CallTool(
		ctx,
		"echo",
		json.RawMessage(`{"text":""}`),
	)
	if err != nil {
		t.Fatalf("tool execution error became protocol error: %v", err)
	}
	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("expected model-visible tool error, got %+v", result)
	}
}

func TestValidateMCPArgumentsStructuralSchema(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"values":{"type":"array","items":{"type":"integer"}},
			"mode":{"type":"string","enum":["sum","max"]}
		},
		"required":["values"],
		"additionalProperties":false
	}`)
	if err := validateMCPArguments(
		schema,
		json.RawMessage(`{"values":[1,2],"mode":"sum"}`),
	); err != nil {
		t.Fatal(err)
	}
	cases := []json.RawMessage{
		json.RawMessage(`{"values":[1.5]}`),
		json.RawMessage(`{"values":[1],"mode":"other"}`),
		json.RawMessage(`{"values":[1],"extra":true}`),
		json.RawMessage(`{"mode":"sum"}`),
	}
	for _, arguments := range cases {
		if err := validateMCPArguments(schema, arguments); err == nil {
			t.Fatalf("expected validation error for %s", arguments)
		}
	}
}

func TestMCPPermissionMappingUsesPessimisticDefaults(t *testing.T) {
	readOnly := true
	nonDestructive := false
	if got := permissionFromMCPAnnotations(MCPToolAnnotations{
		ReadOnlyHint: &readOnly,
	}); got != ToolPermissionRead {
		t.Fatalf("read-only permission=%q", got)
	}
	if got := permissionFromMCPAnnotations(MCPToolAnnotations{
		DestructiveHint: &nonDestructive,
	}); got != ToolPermissionWrite {
		t.Fatalf("non-destructive permission=%q", got)
	}
	if got := permissionFromMCPAnnotations(
		MCPToolAnnotations{},
	); got != ToolPermissionDangerous {
		t.Fatalf("default permission=%q", got)
	}
}
