package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type echoToolArguments struct {
	Text string `json:"text"`
}

func RegisterEchoTool(registry *ToolRegistry) error {
	return registry.Register(ToolDefinition{
		Name:        "local.echo",
		Description: "Returns the provided text as JSON.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
		Permission:  ToolPermissionRead,
		ValidateArguments: func(raw json.RawMessage) error {
			var arguments echoToolArguments
			if err := decodeStrictJSONObject(raw, &arguments); err != nil {
				return err
			}
			if strings.TrimSpace(arguments.Text) == "" {
				return fmt.Errorf("text must not be empty")
			}
			return nil
		},
		Handler: func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var arguments echoToolArguments
			if err := json.Unmarshal(raw, &arguments); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]string{"text": arguments.Text})
		},
	})
}

func serveMCPStdioEcho(reader io.Reader, writer io.Writer) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxMCPMessageBytes)
	initialized := false
	for scanner.Scan() {
		var message mcpRPCMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return fmt.Errorf("decode request: %w", err)
		}
		if message.JSONRPC != "2.0" {
			return fmt.Errorf("invalid JSON-RPC version")
		}
		if len(message.ID) == 0 {
			if message.Method == "notifications/initialized" {
				initialized = true
			}
			continue
		}
		var result any
		var rpcError *mcpRPCError
		switch message.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": mcpProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]bool{"listChanged": false}},
				"serverInfo":      map[string]string{"name": "akritas-echo", "version": "0.1.0"},
			}
		case "tools/list":
			if !initialized {
				rpcError = &mcpRPCError{Code: -32002, Message: "server is not initialized"}
				break
			}
			readOnly := true
			result = MCPListToolsResult{Tools: []MCPTool{{
				Name: "echo", Description: "Returns text through an MCP subprocess.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
				Annotations: MCPToolAnnotations{ReadOnlyHint: &readOnly},
			}}}
		case "tools/call":
			if !initialized {
				rpcError = &mcpRPCError{Code: -32002, Message: "server is not initialized"}
				break
			}
			result, rpcError = handleMCPEchoCall(message.Params)
		default:
			rpcError = &mcpRPCError{Code: -32601, Message: "method not found"}
		}
		response := mcpRPCMessage{JSONRPC: "2.0", ID: message.ID, Error: rpcError}
		if rpcError == nil {
			encoded, err := json.Marshal(result)
			if err != nil {
				return err
			}
			response.Result = encoded
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return err
		}
		if _, err := writer.Write(append(encoded, '\n')); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func handleMCPEchoCall(raw json.RawMessage) (MCPCallToolResult, *mcpRPCError) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || params.Name != "echo" {
		return MCPCallToolResult{}, &mcpRPCError{Code: -32602, Message: "invalid tools/call params"}
	}
	var arguments echoToolArguments
	if err := decodeStrictJSONObject(params.Arguments, &arguments); err != nil || strings.TrimSpace(arguments.Text) == "" {
		block, _ := json.Marshal(map[string]string{"type": "text", "text": "text must be a non-empty string"})
		return MCPCallToolResult{Content: []json.RawMessage{block}, IsError: true}, nil
	}
	block, _ := json.Marshal(map[string]string{"type": "text", "text": arguments.Text})
	structured, _ := json.Marshal(map[string]string{"text": arguments.Text})
	return MCPCallToolResult{Content: []json.RawMessage{block}, StructuredContent: structured}, nil
}
