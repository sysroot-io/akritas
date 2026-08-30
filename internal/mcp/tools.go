package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type MCPToolAnnotations struct {
	ReadOnlyHint    *bool `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
}

type MCPTool struct {
	Name        string             `json:"name"`
	Title       string             `json:"title,omitempty"`
	Description string             `json:"description,omitempty"`
	InputSchema json.RawMessage    `json:"inputSchema"`
	Annotations MCPToolAnnotations `json:"annotations,omitempty"`
}

type MCPListToolsResult struct {
	Tools      []MCPTool `json:"tools"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

type MCPCallToolResult struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent,omitempty"`
	IsError           bool              `json:"isError,omitempty"`
}

type MCPToolBinding struct {
	HostName   string
	RemoteName string
	Permission ToolPermission
}

func (client *MCPStdioClient) ListTools(
	ctx context.Context,
) ([]MCPTool, error) {
	var tools []MCPTool
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page MCPListToolsResult
		if err := client.request(ctx, "tools/list", params, &page); err != nil {
			return nil, err
		}
		tools = append(tools, page.Tools...)
		if page.NextCursor == "" {
			return tools, nil
		}
		if page.NextCursor == cursor {
			return nil, fmt.Errorf("MCP tools/list cursor did not advance")
		}
		cursor = page.NextCursor
	}
}

func (client *MCPStdioClient) CallTool(
	ctx context.Context,
	name string,
	arguments json.RawMessage,
) (MCPCallToolResult, error) {
	var argumentsObject map[string]any
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()
	if err := decoder.Decode(&argumentsObject); err != nil ||
		argumentsObject == nil {
		return MCPCallToolResult{}, fmt.Errorf(
			"MCP tool arguments must be an object",
		)
	}
	var result MCPCallToolResult
	err := client.request(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": argumentsObject,
	}, &result)
	return result, err
}

// RegisterMCPTools imports remote schemas into the existing host registry.
// Permission hints are interpreted conservatively: explicitly read-only is
// read, explicitly non-destructive mutation is write, and everything else is
// dangerous. MCP defines a missing destructiveHint as true.
func RegisterMCPTools(
	ctx context.Context,
	registry *ToolRegistry,
	client *MCPStdioClient,
	namespace string,
	timeout time.Duration,
) ([]MCPToolBinding, error) {
	if registry == nil || client == nil {
		return nil, fmt.Errorf("MCP registration requires registry and client")
	}
	if !toolNamePattern.MatchString(namespace) {
		return nil, fmt.Errorf("invalid MCP namespace %q", namespace)
	}
	remoteTools, err := client.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list MCP tools: %w", err)
	}
	bindings := make([]MCPToolBinding, 0, len(remoteTools))
	for _, remoteTool := range remoteTools {
		if !toolNamePattern.MatchString(remoteTool.Name) {
			return nil, fmt.Errorf(
				"MCP server returned invalid tool name %q",
				remoteTool.Name,
			)
		}
		hostName := namespace + "." + remoteTool.Name
		if !toolNamePattern.MatchString(hostName) {
			return nil, fmt.Errorf("namespaced MCP tool name %q is invalid", hostName)
		}
		description := strings.TrimSpace(remoteTool.Description)
		if description == "" {
			description = "MCP tool " + remoteTool.Name
		}
		schema := append(json.RawMessage(nil), remoteTool.InputSchema...)
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		permission := permissionFromMCPAnnotations(remoteTool.Annotations)
		remoteName := remoteTool.Name
		definition := ToolDefinition{
			Name:        hostName,
			Description: description,
			InputSchema: schema,
			Permission:  permission,
			Timeout:     timeout,
			ValidateArguments: func(arguments json.RawMessage) error {
				return validateMCPArguments(schema, arguments)
			},
			Handler: func(
				callContext context.Context,
				arguments json.RawMessage,
			) (json.RawMessage, error) {
				result, err := client.CallTool(
					callContext,
					remoteName,
					arguments,
				)
				if err != nil {
					return nil, err
				}
				return json.Marshal(result)
			},
		}
		if err := registry.Register(definition); err != nil {
			return nil, err
		}
		bindings = append(bindings, MCPToolBinding{
			HostName:   hostName,
			RemoteName: remoteName,
			Permission: permission,
		})
	}
	return bindings, nil
}

func permissionFromMCPAnnotations(
	annotations MCPToolAnnotations,
) ToolPermission {
	if annotations.ReadOnlyHint != nil && *annotations.ReadOnlyHint {
		return ToolPermissionRead
	}
	if annotations.DestructiveHint != nil && !*annotations.DestructiveHint {
		return ToolPermissionWrite
	}
	return ToolPermissionDangerous
}

// validateMCPArguments enforces the security-relevant structural subset of
// JSON Schema 2020-12: object properties, required, additionalProperties,
// primitive types, arrays, items and enum. The MCP server remains responsible
// for full schema validation.
func validateMCPArguments(
	schemaRaw json.RawMessage,
	argumentsRaw json.RawMessage,
) error {
	var schema map[string]any
	if err := decodeJSONNumber(schemaRaw, &schema); err != nil || schema == nil {
		return fmt.Errorf("invalid MCP input schema")
	}
	var arguments any
	if err := decodeJSONNumber(argumentsRaw, &arguments); err != nil {
		return fmt.Errorf("invalid JSON arguments: %w", err)
	}
	if _, ok := arguments.(map[string]any); !ok {
		return fmt.Errorf("arguments must be a JSON object")
	}
	return validateJSONSchemaValue(schema, arguments, "$", 0)
}

func validateJSONSchemaValue(
	schema map[string]any,
	value any,
	path string,
	depth int,
) error {
	if depth > 32 {
		return fmt.Errorf("%s exceeds validation depth", path)
	}
	if enumValues, ok := schema["enum"].([]any); ok {
		matched := false
		for _, allowed := range enumValues {
			if reflect.DeepEqual(value, allowed) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s is not an allowed enum value", path)
		}
	}
	expectedType, _ := schema["type"].(string)
	if expectedType != "" && !matchesJSONType(expectedType, value) {
		return fmt.Errorf("%s must have type %s", path, expectedType)
	}

	switch typed := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]any)
		for _, nameValue := range required {
			name, ok := nameValue.(string)
			if ok {
				if _, exists := typed[name]; !exists {
					return fmt.Errorf("%s.%s is required", path, name)
				}
			}
		}
		allowAdditional, hasAdditional := schema["additionalProperties"].(bool)
		for name, childValue := range typed {
			childSchemaValue, known := properties[name]
			if !known {
				if hasAdditional && !allowAdditional {
					return fmt.Errorf("%s.%s is not allowed", path, name)
				}
				continue
			}
			childSchema, ok := childSchemaValue.(map[string]any)
			if ok {
				if err := validateJSONSchemaValue(
					childSchema,
					childValue,
					path+"."+name,
					depth+1,
				); err != nil {
					return err
				}
			}
		}
	case []any:
		itemSchema, _ := schema["items"].(map[string]any)
		for index, item := range typed {
			if itemSchema != nil {
				if err := validateJSONSchemaValue(
					itemSchema,
					item,
					fmt.Sprintf("%s[%d]", path, index),
					depth+1,
				); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func matchesJSONType(expected string, value any) bool {
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	case "null":
		return value == nil
	default:
		return true
	}
}

func decodeJSONNumber(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(destination)
}
