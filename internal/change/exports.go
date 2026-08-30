package change

import (
	"context"
	"time"

	"akritas/internal/clients/openai"
)

type Snapshot = changeSimulationSnapshot
type Result = changeSimulationResult
type Proposal = changeSimulationProposal
type Edit = changeSimulationEdit
type AttemptDiagnostic = changeSimulationAttemptDiagnostic
type Failure = changeSimulationFailure
type ValidatorResult = changeValidatorResult
type PendingChange = pendingChange
type Approval = changeApproval

const ProposalToolName = changeSimulationProposalToolName
const MaximumPendingChanges = maximumPendingChanges
const SimulationSystemPrompt = changeSimulationSystemPrompt
const ProposalSchema = changeSimulationProposalSchema

func LoadSnapshot(rootPath, requestPath string, filePaths []string) (Snapshot, error) {
	return loadChangeSimulationSnapshot(rootPath, requestPath, filePaths)
}

func LoadRequest(rootPath, requestPath string) (root, normalizedPath, request string, err error) {
	root, err = resolveChangeSimulationRoot(rootPath)
	if err != nil {
		return "", "", "", err
	}
	request, normalizedPath, err = loadChangeSimulationFile(root, requestPath)
	return root, normalizedPath, request, err
}

func LoadInlineSnapshot(rootPath, request string, filePaths []string) (Snapshot, error) {
	return loadChangeSimulationInlineSnapshot(rootPath, request, filePaths)
}

func DiscoverSnapshot(
	ctx context.Context,
	client *openai.Client,
	rootPath, requestPath, request string,
	maxTokens int,
	temperature float64,
) (Snapshot, error) {
	return discoverChangeSimulationSnapshot(ctx, client, rootPath, requestPath, request, maxTokens, temperature)
}

func RunSimulation(
	ctx context.Context,
	client *openai.Client,
	snapshot Snapshot,
	maxTokens int,
	temperature float64,
) (Result, error) {
	return runChangeSimulation(ctx, client, snapshot, maxTokens, temperature)
}

func RunSimulationValidated(
	ctx context.Context,
	client *openai.Client,
	snapshot Snapshot,
	maxTokens int,
	temperature float64,
	root string,
	validatorProfiles []string,
) (Result, error) {
	return runChangeSimulationValidated(ctx, client, snapshot, maxTokens, temperature, root, validatorProfiles)
}

func FormatResult(result Result) string { return formatChangeSimulationResult(result) }

func BuildPrompt(snapshot Snapshot) (string, error) {
	return buildChangeSimulationPrompt(snapshot)
}

func NewPendingChange(workspace Workspace, result Result, now time.Time) (PendingChange, error) {
	return newPendingChange(workspace, result, now)
}

func ApplyPendingChange(pending PendingChange) ([]string, error) {
	return applyPendingChange(pending)
}

func PreflightValidators(
	ctx context.Context,
	commonProfiles []string,
	workspaces map[string]Workspace,
) error {
	return preflightChangeValidators(ctx, commonProfiles, workspaces)
}

func DeclaredValidatorProfiles(commonProfiles []string, workspaces map[string]Workspace) []string {
	return declaredChangeValidatorProfiles(commonProfiles, workspaces)
}

func MergeValidatorProfiles(groups ...[]string) []string {
	return mergeChangeValidatorProfiles(groups...)
}

func ValidateValidatorProfiles(profiles []string) error {
	return validateChangeValidatorProfiles(profiles)
}

func SortedValidatorProfiles(profiles []string) []string {
	return sortedChangeValidatorProfiles(profiles)
}

func ResolveValidatorGoToolchain() (string, error) {
	return resolveChangeValidatorGoToolchain()
}

func ContainsAny(values []string, candidates ...string) bool {
	return slicesContainAny(values, candidates...)
}

func ResolveRoot(path string) (string, error) { return resolveChangeSimulationRoot(path) }
