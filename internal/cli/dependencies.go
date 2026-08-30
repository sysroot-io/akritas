package cli

import (
	"context"
	"net/http"
	"time"

	"akritas/internal/change"
	"akritas/internal/clients/openai"
	"akritas/internal/corpus"
	"akritas/internal/mcp"
	"akritas/internal/rag"
	"akritas/internal/web"
)

type CorpusCursor = corpus.CorpusCursor
type CorpusDocument = corpus.CorpusDocument
type CorpusQualityStats = corpus.CorpusQualityStats
type LocalCorpusImportOptions = corpus.LocalCorpusImportOptions

var ImportLocalCorpus = corpus.ImportLocalCorpus
var NewCorpusWriter = corpus.NewCorpusWriter
var OpenCorpus = corpus.OpenCorpus
var ReadCorpusJSONL = corpus.ReadCorpusJSONL

type RAGDocumentListOptions = rag.RAGDocumentListOptions
type RAGIndex = rag.RAGIndex
type RAGSearchToolOptions = rag.RAGSearchToolOptions

var BuildRAGIndex = rag.BuildRAGIndex
var InspectRAGIndex = rag.InspectRAGIndex
var ListRAGDocuments = rag.ListRAGDocuments
var LoadRAGIndex = rag.LoadRAGIndex
var SaveRAGIndex = rag.SaveRAGIndex

type openAIToolClient = openai.Client

func newOpenAIToolClient(baseURL, model, apiKey string, client *http.Client) (*openAIToolClient, error) {
	return openai.NewToolClient(baseURL, model, apiKey, client)
}

type changeSimulationSnapshot = change.Snapshot

const changeSimulationSystemPrompt = change.SimulationSystemPrompt
const changeSimulationProposalToolName = change.ProposalToolName
const changeSimulationProposalSchema = change.ProposalSchema

var buildChangeSimulationPrompt = change.BuildPrompt
var formatChangeSimulationResult = change.FormatResult
var loadChangeSimulationSnapshot = change.LoadSnapshot
var runChangeSimulation = change.RunSimulation

func loadChangeRequest(root, requestPath string) (string, string, string, error) {
	return change.LoadRequest(root, requestPath)
}

func discoverChangeSimulationSnapshot(
	ctx context.Context,
	client *openAIToolClient,
	root, requestPath, request string,
	maxTokens int,
	temperature float64,
) (changeSimulationSnapshot, error) {
	return change.DiscoverSnapshot(ctx, client, root, requestPath, request, maxTokens, temperature)
}

type opsWorkspace = web.Workspace

var declaredChangeValidatorProfiles = change.DeclaredValidatorProfiles
var mergeChangeValidatorProfiles = change.MergeValidatorProfiles
var preflightChangeValidators = change.PreflightValidators
var sortedChangeValidatorProfiles = change.SortedValidatorProfiles
var validateChangeValidatorProfiles = change.ValidateValidatorProfiles
var resolveChangeValidatorGoToolchain = change.ResolveValidatorGoToolchain
var slicesContainAny = change.ContainsAny

var loadOpsWorkspaceConfig = web.LoadWorkspaceConfig
var mergeOpsWorkspaces = web.MergeWorkspaces
var parseOpsWorkspaces = web.ParseWorkspaces

type ToolRegistry = mcp.ToolRegistry
type NamedToolPolicy = mcp.NamedToolPolicy
type MCPHost = mcp.MCPHost

var LoadMCPHostConfig = mcp.LoadMCPHostConfig
var NewToolRegistry = mcp.NewToolRegistry
var StartMCPHost = mcp.StartMCPHost

const localRAGSearchToolName = rag.SearchToolName
const localCapabilityGapToolName = web.CapabilityGapToolName

var RegisterRAGSearchTool = rag.RegisterRAGSearchTool
var registerOpsCapabilityGapTool = web.RegisterCapabilityGapTool
var registerReadOnlyTools = web.RegisterReadOnlyTools

func newOpsServer(
	client *openAIToolClient,
	registry *ToolRegistry,
	policy NamedToolPolicy,
	modelID, apiKey string,
	defaultMaxTokens, maxTokensLimit int,
	defaultTemperature float64,
	maxToolCalls int,
	requestTimeout time.Duration,
	workspaces map[string]opsWorkspace,
) (*web.Server, error) {
	return web.NewServer(
		client, registry, policy, modelID, apiKey, defaultMaxTokens,
		maxTokensLimit, defaultTemperature, maxToolCalls, requestTimeout, workspaces,
	)
}
