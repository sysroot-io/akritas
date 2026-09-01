package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	defaultToolTimeout      = 10 * time.Second
	maxToolCallPayloadBytes = 64 * 1024
	maxToolResultBytes      = 256 * 1024
)

// DefaultToolTimeout is used by adapters that do not specify a tighter bound.
const DefaultToolTimeout = defaultToolTimeout

var toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// IsValidToolName reports whether a tool name is valid in registry calls.
func IsValidToolName(name string) bool { return toolNamePattern.MatchString(name) }

type ToolPermission string

const (
	ToolPermissionRead      ToolPermission = "read"
	ToolPermissionWrite     ToolPermission = "write"
	ToolPermissionDangerous ToolPermission = "dangerous"
)

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ToolResult struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Output     json.RawMessage `json:"output,omitempty"`
	Error      *ToolError      `json:"error,omitempty"`
	DurationMS int64           `json:"-"`
}

type ToolArgumentValidator func(json.RawMessage) error
type ToolHandler func(context.Context, json.RawMessage) (json.RawMessage, error)

type ToolDefinition struct {
	Name              string
	Description       string
	InputSchema       json.RawMessage
	Permission        ToolPermission
	Timeout           time.Duration
	ValidateArguments ToolArgumentValidator
	Handler           ToolHandler
}

type ToolAuthorizationPolicy interface {
	Authorize(ToolDefinition, ToolCall) error
}

type StaticToolPolicy struct {
	Allowed map[ToolPermission]bool
}

func (policy StaticToolPolicy) Authorize(definition ToolDefinition, _ ToolCall) error {
	if !policy.Allowed[definition.Permission] {
		return fmt.Errorf("permission %q requires explicit approval", definition.Permission)
	}
	return nil
}

type ToolRegistry struct {
	tools map[string]ToolDefinition
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]ToolDefinition)}
}

func (registry *ToolRegistry) Definitions() []ToolDefinition {
	if registry == nil {
		return nil
	}
	definitions := make([]ToolDefinition, 0, len(registry.tools))
	for _, definition := range registry.tools {
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(left, right int) bool {
		return definitions[left].Name < definitions[right].Name
	})
	return definitions
}

func (registry *ToolRegistry) Register(definition ToolDefinition) error {
	if registry == nil {
		return fmt.Errorf("tool registry is nil")
	}
	if !toolNamePattern.MatchString(definition.Name) {
		return fmt.Errorf("invalid tool name %q", definition.Name)
	}
	if strings.TrimSpace(definition.Description) == "" {
		return fmt.Errorf("tool %q has no description", definition.Name)
	}
	if definition.Permission != ToolPermissionRead && definition.Permission != ToolPermissionWrite && definition.Permission != ToolPermissionDangerous {
		return fmt.Errorf("tool %q has invalid permission %q", definition.Name, definition.Permission)
	}
	if len(definition.InputSchema) == 0 || !json.Valid(definition.InputSchema) {
		return fmt.Errorf("tool %q has invalid input schema", definition.Name)
	}
	var schema map[string]any
	if err := json.Unmarshal(definition.InputSchema, &schema); err != nil || schema == nil {
		return fmt.Errorf("tool %q input schema must be a JSON object", definition.Name)
	}
	if definition.ValidateArguments == nil || definition.Handler == nil {
		return fmt.Errorf("tool %q requires argument validation and a handler", definition.Name)
	}
	if definition.Timeout <= 0 {
		definition.Timeout = defaultToolTimeout
	}
	if _, exists := registry.tools[definition.Name]; exists {
		return fmt.Errorf("tool %q is already registered", definition.Name)
	}
	registry.tools[definition.Name] = definition
	return nil
}

func (registry *ToolRegistry) Execute(
	ctx context.Context,
	call ToolCall,
	policy ToolAuthorizationPolicy,
) (result ToolResult) {
	started := time.Now()
	result = ToolResult{ID: call.ID, Name: call.Name}
	log.Printf(
		"level=info component=akritas_tool event=start tool=%q call_id=%q",
		call.Name, call.ID,
	)
	defer func() {
		result.DurationMS = time.Since(started).Milliseconds()
		status := "ok"
		if result.Error != nil {
			status = result.Error.Code
		}
		log.Printf(
			"level=info component=akritas_tool event=finish tool=%q call_id=%q status=%q duration_ms=%d",
			call.Name, call.ID, status, result.DurationMS,
		)
	}()
	if err := validateToolCall(call); err != nil {
		result.Error = &ToolError{Code: "invalid_call", Message: err.Error()}
		return result
	}
	definition, exists := registry.tools[call.Name]
	if !exists {
		result.Error = &ToolError{Code: "unknown_tool", Message: "tool is not registered"}
		return result
	}
	if policy == nil {
		result.Error = &ToolError{Code: "permission_denied", Message: "no authorization policy"}
		return result
	}
	if err := policy.Authorize(definition, call); err != nil {
		result.Error = &ToolError{Code: "permission_denied", Message: err.Error()}
		return result
	}
	if err := definition.ValidateArguments(call.Arguments); err != nil {
		result.Error = &ToolError{Code: "invalid_arguments", Message: err.Error()}
		return result
	}
	callContext, cancel := context.WithTimeout(ctx, definition.Timeout)
	defer cancel()
	output, err := definition.Handler(callContext, call.Arguments)
	if errors.Is(callContext.Err(), context.DeadlineExceeded) {
		result.Error = &ToolError{Code: "timeout", Message: "tool execution timed out"}
		return result
	}
	if err != nil {
		result.Error = &ToolError{Code: "tool_error", Message: err.Error()}
		return result
	}
	if len(output) > maxToolResultBytes {
		result.Error = &ToolError{Code: "result_too_large", Message: fmt.Sprintf("tool result exceeds %d bytes", maxToolResultBytes)}
		return result
	}
	if len(output) == 0 || !json.Valid(output) {
		result.Error = &ToolError{Code: "invalid_result", Message: "tool returned invalid JSON"}
		return result
	}
	result.Output = append(json.RawMessage(nil), output...)
	return result
}

func validateToolCall(call ToolCall) error {
	if !toolNamePattern.MatchString(call.ID) || !toolNamePattern.MatchString(call.Name) {
		return fmt.Errorf("tool call has invalid ID or name")
	}
	if len(call.Arguments) == 0 || len(call.Arguments) > maxToolCallPayloadBytes || !json.Valid(call.Arguments) {
		return fmt.Errorf("tool arguments are not valid bounded JSON")
	}
	var arguments map[string]any
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil || arguments == nil {
		return fmt.Errorf("tool arguments must be a JSON object")
	}
	return nil
}

func decodeStrictJSONObject(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

// DecodeStrictJSONObject decodes one JSON object and rejects unknown fields
// and trailing values.
func DecodeStrictJSONObject(payload []byte, destination any) error {
	return decodeStrictJSONObject(payload, destination)
}
