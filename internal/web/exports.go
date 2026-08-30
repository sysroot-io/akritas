package web

import (
	"net/http"
	"time"

	"akritas/internal/audit"
	"akritas/internal/clients/openai"
	"akritas/internal/mcp"
)

type Server = opsServer
type Workspace = opsWorkspace

func NewServer(
	client *openai.Client,
	registry *mcp.ToolRegistry,
	policy mcp.NamedToolPolicy,
	modelID, apiKey string,
	defaultMaxTokens, maxTokensLimit int,
	defaultTemperature float64,
	maxToolCalls int,
	requestTimeout time.Duration,
	workspaces map[string]Workspace,
) (*Server, error) {
	return newOpsServer(
		client, registry, policy, modelID, apiKey, defaultMaxTokens,
		maxTokensLimit, defaultTemperature, maxToolCalls, requestTimeout, workspaces,
	)
}

func (server *opsServer) Handler() http.Handler { return server.handler() }

func (server *opsServer) SetValidatorProfiles(profiles []string) {
	server.validatorProfiles = append([]string(nil), profiles...)
}

func (server *opsServer) SetAuditStore(store *audit.Store) {
	server.auditStore = store
}

func LoadWorkspaceConfig(path string) (map[string]Workspace, []string, error) {
	return loadOpsWorkspaceConfig(path)
}

func MergeWorkspaces(destination, source map[string]Workspace) error {
	return mergeOpsWorkspaces(destination, source)
}

func ParseWorkspaces(values []string) (map[string]Workspace, error) {
	return parseOpsWorkspaces(values)
}

func RegisterReadOnlyTools(
	destination *mcp.ToolRegistry,
	policy mcp.NamedToolPolicy,
	source *mcp.ToolRegistry,
	sourcePolicy mcp.NamedToolPolicy,
) (int, error) {
	return registerReadOnlyTools(destination, policy, source, sourcePolicy)
}

func RegisterCapabilityGapTool(registry *mcp.ToolRegistry) error {
	return registerOpsCapabilityGapTool(registry)
}

const CapabilityGapToolName = localCapabilityGapToolName
