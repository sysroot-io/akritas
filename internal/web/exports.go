package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"akritas/internal/alerts"
	"akritas/internal/audit"
	"akritas/internal/clients/openai"
	"akritas/internal/mcp"
	"akritas/internal/notifications"
	"akritas/internal/runbudget"
	"akritas/internal/skills"
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

func (server *opsServer) SetAlertRuntime(registry *alerts.Registry, store *alerts.Store) error {
	return server.setAlertRuntime(registry, store)
}

func (server *opsServer) CloseAlertWorker() {
	server.closeAlertWorker()
}

func (server *opsServer) SetPublicURL(value string) {
	server.publicURL = strings.TrimRight(strings.TrimSpace(value), "/")
}

func (server *opsServer) SetSkillCatalog(catalog *skills.Catalog) {
	server.skillCatalog = catalog
}

func (server *opsServer) SetNotificationDispatcher(dispatcher *notifications.Dispatcher) {
	if server.botGateway != nil {
		server.botGateway.close()
	}
	server.notifications = dispatcher
	server.botGateway = newOpsBotGateway(server, dispatcher)
}

func (server *opsServer) CloseBotGateway() {
	if server.botGateway != nil {
		server.botGateway.close()
		server.botGateway = nil
	}
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

func RegisterKnowledgeSkillTools(registry *mcp.ToolRegistry, catalog *skills.Catalog) error {
	return registerOpsKnowledgeSkillTools(registry, catalog)
}

const InvestigationPlanToolName = localInvestigationPlanToolName
const KnowledgeListSkillsToolName = localKnowledgeListSkillsName
const KnowledgeLoadSkillToolName = localKnowledgeLoadSkillName
