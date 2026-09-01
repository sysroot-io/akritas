package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// ServeStdio exposes a ToolRegistry as an MCP server over newline-delimited
// JSON-RPC. The registry remains the enforcement point for validation,
// permissions, timeouts, and result-size limits.
func ServeStdio(
	ctx context.Context,
	reader io.Reader,
	writer io.Writer,
	serverName string,
	serverVersion string,
	registry *ToolRegistry,
) error {
	if !toolNamePattern.MatchString(serverName) || serverVersion == "" {
		return fmt.Errorf("invalid MCP server identity")
	}
	if registry == nil {
		return fmt.Errorf("MCP server registry is nil")
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxMCPMessageBytes)
	initialized := false
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}

		var message mcpRPCMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return fmt.Errorf("decode MCP request: %w", err)
		}
		if message.JSONRPC != "2.0" {
			return fmt.Errorf("MCP request has invalid jsonrpc version")
		}
		if len(message.ID) == 0 {
			if message.Method == "notifications/initialized" {
				initialized = true
			}
			continue
		}

		result, rpcError := handleServerRequest(
			ctx, message, initialized, serverName, serverVersion, registry,
		)
		response := mcpRPCMessage{JSONRPC: "2.0", ID: message.ID, Error: rpcError}
		if rpcError == nil {
			encoded, err := json.Marshal(result)
			if err != nil {
				return fmt.Errorf("encode MCP result: %w", err)
			}
			response.Result = encoded
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return fmt.Errorf("encode MCP response: %w", err)
		}
		if _, err := writer.Write(append(encoded, '\n')); err != nil {
			return fmt.Errorf("write MCP response: %w", err)
		}
	}
	return scanner.Err()
}

func handleServerRequest(
	ctx context.Context,
	message mcpRPCMessage,
	initialized bool,
	serverName string,
	serverVersion string,
	registry *ToolRegistry,
) (any, *mcpRPCError) {
	switch message.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil ||
			params.ProtocolVersion != mcpProtocolVersion {
			return nil, &mcpRPCError{Code: -32602, Message: "unsupported MCP protocol version"}
		}
		return map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]bool{"listChanged": false},
			},
			"serverInfo": map[string]string{"name": serverName, "version": serverVersion},
		}, nil
	case "tools/list":
		if !initialized {
			return nil, serverNotInitializedError()
		}
		return MCPListToolsResult{Tools: serverTools(registry)}, nil
	case "tools/call":
		if !initialized {
			return nil, serverNotInitializedError()
		}
		return callServerTool(ctx, message.Params, registry), nil
	default:
		return nil, &mcpRPCError{Code: -32601, Message: "method not found"}
	}
}

func serverNotInitializedError() *mcpRPCError {
	return &mcpRPCError{Code: -32002, Message: "server is not initialized"}
}

func serverTools(registry *ToolRegistry) []MCPTool {
	definitions := registry.Definitions()
	tools := make([]MCPTool, 0, len(definitions))
	for _, definition := range definitions {
		readOnly := definition.Permission == ToolPermissionRead
		destructive := definition.Permission == ToolPermissionDangerous
		tools = append(tools, MCPTool{
			Name:        definition.Name,
			Description: definition.Description,
			InputSchema: append(json.RawMessage(nil), definition.InputSchema...),
			Annotations: MCPToolAnnotations{
				ReadOnlyHint: &readOnly, DestructiveHint: &destructive,
			},
		})
	}
	return tools
}

func callServerTool(
	ctx context.Context,
	paramsRaw json.RawMessage,
	registry *ToolRegistry,
) MCPCallToolResult {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := decodeStrictJSONObject(paramsRaw, &params); err != nil || len(params.Arguments) == 0 {
		return serverToolError("invalid tools/call params")
	}
	result := registry.Execute(ctx, ToolCall{
		ID: "mcp-call", Name: params.Name, Arguments: params.Arguments,
	}, StaticToolPolicy{Allowed: map[ToolPermission]bool{ToolPermissionRead: true}})
	if result.Error != nil {
		return serverToolError(result.Error.Message)
	}
	block, err := json.Marshal(map[string]string{
		"type": "text", "text": "The tool returned structured JSON content.",
	})
	if err != nil {
		return serverToolError("could not encode tool result")
	}
	return MCPCallToolResult{
		Content:           []json.RawMessage{block},
		StructuredContent: append(json.RawMessage(nil), result.Output...),
	}
}

func serverToolError(message string) MCPCallToolResult {
	block, _ := json.Marshal(map[string]string{"type": "text", "text": message})
	return MCPCallToolResult{Content: []json.RawMessage{block}, IsError: true}
}
