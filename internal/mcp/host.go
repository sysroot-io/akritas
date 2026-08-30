package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	mcpHostConfigVersion = 1
	maxMCPConfigBytes    = 1024 * 1024
)

type MCPHostConfig struct {
	Version int               `json:"version"`
	Servers []MCPServerConfig `json:"servers"`
}

type MCPServerConfig struct {
	Name             string            `json:"name"`
	Command          string            `json:"command"`
	Arguments        []string          `json:"args,omitempty"`
	Environment      map[string]string `json:"env,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	Namespace        string            `json:"namespace,omitempty"`
	Allow            []ToolPermission  `json:"allow"`
	TimeoutMS        int               `json:"timeout_ms,omitempty"`
}

type NamedToolPolicy struct {
	Allowed map[string]bool
}

func (policy NamedToolPolicy) Authorize(
	definition ToolDefinition,
	_ ToolCall,
) error {
	if !policy.Allowed[definition.Name] {
		return fmt.Errorf(
			"tool %q requires explicit permission in MCP configuration",
			definition.Name,
		)
	}
	return nil
}

type MCPHost struct {
	Registry *ToolRegistry
	Policy   NamedToolPolicy
	Clients  []*MCPStdioClient
}

func LoadMCPHostConfig(path string) (MCPHostConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return MCPHostConfig{}, fmt.Errorf("read MCP config %q: %w", path, err)
	}
	if len(raw) == 0 || len(raw) > maxMCPConfigBytes {
		return MCPHostConfig{}, fmt.Errorf(
			"MCP config must contain 1..%d bytes",
			maxMCPConfigBytes,
		)
	}
	var config MCPHostConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return MCPHostConfig{}, fmt.Errorf("decode MCP config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return MCPHostConfig{}, fmt.Errorf(
				"decode trailing MCP config data: %w",
				err,
			)
		}
		return MCPHostConfig{}, fmt.Errorf("MCP config contains multiple JSON values")
	}
	if err := validateMCPHostConfig(config); err != nil {
		return MCPHostConfig{}, err
	}
	return config, nil
}

func validateMCPHostConfig(config MCPHostConfig) error {
	if config.Version != mcpHostConfigVersion {
		return fmt.Errorf(
			"unsupported MCP config version %d",
			config.Version,
		)
	}
	if len(config.Servers) == 0 {
		return fmt.Errorf("MCP config contains no servers")
	}
	names := make(map[string]bool)
	namespaces := make(map[string]bool)
	for index, server := range config.Servers {
		if !toolNamePattern.MatchString(server.Name) {
			return fmt.Errorf("servers[%d] has invalid name %q", index, server.Name)
		}
		if names[server.Name] {
			return fmt.Errorf("duplicate MCP server name %q", server.Name)
		}
		names[server.Name] = true
		if strings.TrimSpace(server.Command) == "" {
			return fmt.Errorf("MCP server %q has empty command", server.Name)
		}
		namespace := server.Namespace
		if namespace == "" {
			namespace = "mcp." + server.Name
		}
		if !toolNamePattern.MatchString(namespace) {
			return fmt.Errorf(
				"MCP server %q has invalid namespace %q",
				server.Name,
				namespace,
			)
		}
		if namespaces[namespace] {
			return fmt.Errorf("duplicate MCP namespace %q", namespace)
		}
		namespaces[namespace] = true
		if len(server.Allow) == 0 {
			return fmt.Errorf(
				"MCP server %q must explicitly list allowed permissions",
				server.Name,
			)
		}
		for _, permission := range server.Allow {
			if permission != ToolPermissionRead &&
				permission != ToolPermissionWrite &&
				permission != ToolPermissionDangerous {
				return fmt.Errorf(
					"MCP server %q has invalid permission %q",
					server.Name,
					permission,
				)
			}
		}
		if server.TimeoutMS < 0 {
			return fmt.Errorf(
				"MCP server %q has negative timeout_ms",
				server.Name,
			)
		}
		for key := range server.Environment {
			if key == "" || strings.ContainsRune(key, '=') {
				return fmt.Errorf(
					"MCP server %q has invalid environment key %q",
					server.Name,
					key,
				)
			}
		}
	}
	return nil
}

func StartMCPHost(
	ctx context.Context,
	config MCPHostConfig,
) (*MCPHost, error) {
	if err := validateMCPHostConfig(config); err != nil {
		return nil, err
	}
	host := &MCPHost{
		Registry: NewToolRegistry(),
		Policy: NamedToolPolicy{
			Allowed: make(map[string]bool),
		},
	}
	for _, server := range config.Servers {
		namespace := server.Namespace
		if namespace == "" {
			namespace = "mcp." + server.Name
		}
		client, err := StartMCPStdioClient(ctx, MCPStdioConfig{
			Name:             server.Name,
			Command:          server.Command,
			Arguments:        append([]string(nil), server.Arguments...),
			Environment:      sortedEnvironment(server.Environment),
			WorkingDirectory: server.WorkingDirectory,
		})
		if err != nil {
			_ = host.Close()
			return nil, err
		}
		host.Clients = append(host.Clients, client)
		timeout := defaultToolTimeout
		if server.TimeoutMS > 0 {
			timeout = time.Duration(server.TimeoutMS) * time.Millisecond
		}
		bindings, err := RegisterMCPTools(
			ctx,
			host.Registry,
			client,
			namespace,
			timeout,
		)
		if err != nil {
			_ = host.Close()
			return nil, fmt.Errorf(
				"register tools from MCP server %q: %w",
				server.Name,
				err,
			)
		}
		allowedPermissions := make(map[ToolPermission]bool)
		for _, permission := range server.Allow {
			allowedPermissions[permission] = true
		}
		for _, binding := range bindings {
			if allowedPermissions[binding.Permission] {
				host.Policy.Allowed[binding.HostName] = true
			}
		}
	}
	return host, nil
}

func (host *MCPHost) Close() error {
	if host == nil {
		return nil
	}
	var firstError error
	for index := len(host.Clients) - 1; index >= 0; index-- {
		if err := host.Clients[index].Close(); err != nil && firstError == nil {
			firstError = err
		}
	}
	host.Clients = nil
	return firstError
}

func (host *MCPHost) ToolCatalogPrompt() string {
	if host == nil || host.Registry == nil {
		return ""
	}
	var builder strings.Builder
	builder.WriteString(
		"\n\nAvailable external tools follow. Use a tool only when needed. " +
			"To call one, emit TOOL_CALL as the first generated control token, " +
			"then one JSON object {\"id\":\"...\",\"name\":\"...\",\"arguments\":{...}}, " +
			"then EOS. Never invent TOOL_RESULT; the host supplies it.\n",
	)
	for _, definition := range host.Registry.Definitions() {
		if !host.Policy.Allowed[definition.Name] {
			continue
		}
		fmt.Fprintf(
			&builder,
			"- %s permission=%s: %s input_schema=%s\n",
			definition.Name,
			definition.Permission,
			definition.Description,
			definition.InputSchema,
		)
	}
	return strings.TrimSpace(builder.String())
}

func sortedEnvironment(environment map[string]string) []string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+environment[key])
	}
	return result
}
