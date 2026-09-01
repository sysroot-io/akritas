package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"akritas/internal/change"
	"akritas/internal/clients/openai"
	"akritas/internal/mcp"
	"akritas/internal/rag"
	"akritas/internal/runbudget"
)

type openAIToolCall = openai.ToolCall
type openAIToolClient = openai.Client
type openAIToolFunction = openai.ToolFunction
type openAIToolLoopResult = openai.ToolLoopResult
type openAIToolMessage = openai.ToolMessage
type openAIToolRequest = openai.ToolRequest
type openAIToolSpec = openai.ToolSpec

func newOpenAIToolClient(baseURL, model, apiKey string, client *http.Client) (*openAIToolClient, error) {
	return openai.NewToolClient(baseURL, model, apiKey, client)
}

func runOpenAIToolLoop(
	ctx context.Context,
	client *openAIToolClient,
	history []openAIToolMessage,
	registry *ToolRegistry,
	policy ToolAuthorizationPolicy,
	maxCalls, maxTokens int,
	temperature float64,
	tracker *runbudget.Tracker,
) (openAIToolLoopResult, error) {
	return openai.RunToolLoopBudgeted(
		ctx, client, history, registry, policy, maxCalls, maxTokens, temperature, tracker,
	)
}

func buildOpenAITools(definitions []ToolDefinition) ([]openAIToolSpec, map[string]string) {
	return openai.BuildTools(definitions)
}

func openAIToolAlias(name string) string { return openai.ToolAlias(name) }

type ToolAuthorizationPolicy = mcp.ToolAuthorizationPolicy
type ToolCall = mcp.ToolCall
type ToolDefinition = mcp.ToolDefinition
type ToolPermission = mcp.ToolPermission
type ToolRegistry = mcp.ToolRegistry
type ToolResult = mcp.ToolResult
type NamedToolPolicy = mcp.NamedToolPolicy

const (
	ToolPermissionRead      = mcp.ToolPermissionRead
	ToolPermissionWrite     = mcp.ToolPermissionWrite
	ToolPermissionDangerous = mcp.ToolPermissionDangerous
)

func NewToolRegistry() *ToolRegistry { return mcp.NewToolRegistry() }

func mcpToolNameValid(name string) bool { return mcp.IsValidToolName(name) }

func decodeStrictJSONObject(payload []byte, destination any) error {
	return mcp.DecodeStrictJSONObject(json.RawMessage(payload), destination)
}

type RAGIndex = rag.RAGIndex
type RAGChunk = rag.RAGChunk
type RAGPosting = rag.RAGPosting
type RAGSearchToolOptions = rag.RAGSearchToolOptions

const localRAGSearchToolName = rag.SearchToolName

type ragSearchToolArguments struct {
	Query string `json:"query"`
	TopK  int    `json:"top_k,omitempty"`
}

func RegisterRAGSearchTool(registry *ToolRegistry, index *RAGIndex, options RAGSearchToolOptions) error {
	return rag.RegisterRAGSearchTool(registry, index, options)
}

func RAGSearchToolCatalogPrompt() string { return rag.RAGSearchToolCatalogPrompt() }

type opsWorkspace = change.Workspace
type changeSimulationSnapshot = change.Snapshot
type changeSimulationResult = change.Result
type changeSimulationProposal = change.Proposal
type changeSimulationEdit = change.Edit
type changeSimulationAttemptDiagnostic = change.AttemptDiagnostic
type changeSimulationFailure = change.Failure
type pendingChange = change.PendingChange
type changeApproval = change.Approval

const changeSimulationProposalToolName = change.ProposalToolName
const maximumPendingChanges = change.MaximumPendingChanges

func loadChangeSimulationInlineSnapshot(root, request string, files []string) (changeSimulationSnapshot, error) {
	return change.LoadInlineSnapshot(root, request, files)
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

func runChangeSimulationValidated(
	ctx context.Context,
	client *openAIToolClient,
	snapshot changeSimulationSnapshot,
	maxTokens int,
	temperature float64,
	root string,
	profiles []string,
) (changeSimulationResult, error) {
	return change.RunSimulationValidated(ctx, client, snapshot, maxTokens, temperature, root, profiles)
}

func runChangeSimulationValidatedWithLanguage(
	ctx context.Context,
	client *openAIToolClient,
	snapshot changeSimulationSnapshot,
	maxTokens int,
	temperature float64,
	root string,
	profiles []string,
	responseLanguage string,
) (changeSimulationResult, error) {
	return change.RunSimulationValidatedWithLanguage(
		ctx, client, snapshot, maxTokens, temperature, root, profiles, responseLanguage,
	)
}

func formatChangeSimulationResult(result changeSimulationResult) string {
	return change.FormatResult(result)
}

func newPendingChange(workspace opsWorkspace, result changeSimulationResult, now time.Time) (pendingChange, error) {
	return change.NewPendingChange(workspace, result, now)
}

func applyPendingChange(pending pendingChange) ([]string, error) {
	return change.ApplyPendingChange(pending)
}

func mergeChangeValidatorProfiles(groups ...[]string) []string {
	return change.MergeValidatorProfiles(groups...)
}

func validateChangeValidatorProfiles(profiles []string) error {
	return change.ValidateValidatorProfiles(profiles)
}

func sortedChangeValidatorProfiles(profiles []string) []string {
	return change.SortedValidatorProfiles(profiles)
}

func resolveChangeSimulationRoot(path string) (string, error) { return change.ResolveRoot(path) }
