package web

import (
	"fmt"
	"net/http"
	"time"

	"akritas/internal/audit"
	"akritas/internal/clients/openai"
	"akritas/internal/mcp"
	"akritas/internal/runbudget"
)

type Server = opsServer
type Workspace = opsWorkspace

func NewServer(
	client *openai.Client,
	registry *mcp.ToolRegistry,
	policy mcp.NamedToolPolicy,
	modelID, apiKey, responseLanguage string,
	defaultMaxTokens, maxTokensLimit int,
	defaultTemperature float64,
	maxToolCalls int,
	requestTimeout time.Duration,
	workspaces map[string]Workspace,
) (*Server, error) {
	return newOpsServer(
		client, registry, policy, modelID, apiKey, responseLanguage, defaultMaxTokens,
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

func (server *opsServer) SetRunBudget(limits runbudget.Limits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	if limits.MaxToolCalls != server.maxToolCalls {
		return fmt.Errorf("run budget max tool calls must equal server max tool calls")
	}
	server.runBudgetLimits = limits
	return nil
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

func RegisterInvestigationPlanTool(registry *mcp.ToolRegistry) error {
	return registerOpsInvestigationPlanTool(registry)
}

const InvestigationPlanToolName = localInvestigationPlanToolName
