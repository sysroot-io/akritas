package change

import (
	"context"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	changeSimulationProposalToolName = "akritas_propose_edits"
	maximumChangeSimulationEdits     = 32
	maximumChangeSimulationListItems = 16
	changeSimulationAttempts         = 2
)

const changeSimulationProposalSchema = `{"type":"object","properties":{"analysis":{"type":"string","description":"Reasoning and explanations for the proposal; not unanswered user questions."},"edits":{"type":"array","description":"Exact repository changes. When non-empty, clarifications must be an empty array.","maxItems":32,"items":{"type":"object","properties":{"operation":{"type":"string","enum":["replace","create"]},"path":{"type":"string"},"old":{"type":"string"},"new":{"type":"string"}},"required":["path","old","new"],"additionalProperties":false}},"checks":{"type":"array","maxItems":16,"items":{"type":"string"}},"risks":{"type":"array","maxItems":16,"items":{"type":"string"}},"rollback":{"type":"string"},"clarifications":{"type":"array","description":"Only concrete unanswered questions for the user. Must be empty when edits is non-empty; never use for explanations, conclusions, requirements, or assumptions.","maxItems":16,"items":{"type":"string"}}},"required":["analysis","edits","checks","risks","rollback","clarifications"],"additionalProperties":false}`

type changeSimulationEdit struct {
	Operation string `json:"operation,omitempty"`
	Path      string `json:"path"`
	Old       string `json:"old"`
	New       string `json:"new"`
}

type changeSimulationProposal struct {
	Analysis       string                 `json:"analysis"`
	Edits          []changeSimulationEdit `json:"edits"`
	Checks         []string               `json:"checks"`
	Risks          []string               `json:"risks"`
	Rollback       string                 `json:"rollback"`
	Clarifications []string               `json:"clarifications"`
}

type changeSimulationResult struct {
	Analysis         string                              `json:"analysis"`
	Diff             string                              `json:"diff"`
	Checks           []string                            `json:"checks"`
	Risks            []string                            `json:"risks"`
	Rollback         string                              `json:"rollback"`
	Clarifications   []string                            `json:"clarifications"`
	ChangedFiles     []string                            `json:"changed_files"`
	CreatedFiles     []string                            `json:"created_files"`
	Warnings         []string                            `json:"warnings,omitempty"`
	Validation       []string                            `json:"validation"`
	Validators       []changeValidatorResult             `json:"validators"`
	RejectedAttempts []changeSimulationAttemptDiagnostic `json:"-"`
	Attempts         int                                 `json:"attempts"`
	originalFiles    map[string]string
	updatedFiles     map[string]string
	createdFiles     map[string]bool
}

type changeSimulationAttemptDiagnostic struct {
	Attempt          int      `json:"attempt"`
	Stage            string   `json:"stage"`
	Error            string   `json:"error"`
	FinishReason     string   `json:"finish_reason,omitempty"`
	CompletionTokens int      `json:"completion_tokens,omitempty"`
	ToolName         string   `json:"tool_name,omitempty"`
	ArgumentBytes    int      `json:"argument_bytes,omitempty"`
	Arguments        string   `json:"arguments,omitempty"`
	ArgumentsPreview string   `json:"arguments_preview,omitempty"`
	Hint             string   `json:"hint,omitempty"`
	ProposedChecks   []string `json:"proposed_checks,omitempty"`
}

type changeSimulationFailure struct {
	Cause       error
	Diagnostics []changeSimulationAttemptDiagnostic
}

func (failure *changeSimulationFailure) Error() string { return failure.Cause.Error() }
func (failure *changeSimulationFailure) Unwrap() error { return failure.Cause }

func runChangeSimulation(
	ctx context.Context,
	client *openAIToolClient,
	snapshot changeSimulationSnapshot,
	maxTokens int,
	temperature float64,
) (changeSimulationResult, error) {
	return runChangeSimulationValidated(ctx, client, snapshot, maxTokens, temperature, "", nil)
}

func runChangeSimulationValidated(
	ctx context.Context,
	client *openAIToolClient,
	snapshot changeSimulationSnapshot,
	maxTokens int,
	temperature float64,
	root string,
	validatorProfiles []string,
) (changeSimulationResult, error) {
	if client == nil || maxTokens <= 0 || temperature < 0 {
		return changeSimulationResult{}, fmt.Errorf("change simulation requires a client and valid generation limits")
	}
	previousError := ""
	diagnostics := make([]changeSimulationAttemptDiagnostic, 0, changeSimulationAttempts)
	for attempt := 1; attempt <= changeSimulationAttempts; attempt++ {
		proposal, diagnostic, err := requestChangeSimulationProposal(
			ctx, client, snapshot, previousError, maxTokens, temperature,
		)
		diagnostic.Attempt = attempt
		if err != nil {
			diagnostic.Error = err.Error()
			diagnostics = append(diagnostics, diagnostic)
			previousError = err.Error()
			if attempt == changeSimulationAttempts {
				cause := fmt.Errorf(
					"structured change proposal rejected after %d attempts: %w",
					attempt, err,
				)
				return changeSimulationResult{}, &changeSimulationFailure{Cause: cause, Diagnostics: diagnostics}
			}
			continue
		}
		result, err := materializeChangeSimulation(snapshot, proposal)
		if err == nil {
			err = runChangeValidators(ctx, root, &result, validatorProfiles)
		}
		if err == nil {
			result.Attempts = attempt
			result.RejectedAttempts = append([]changeSimulationAttemptDiagnostic(nil), diagnostics...)
			return result, nil
		}
		diagnostic.Stage = "host_validation"
		diagnostic.Error = err.Error()
		diagnostics = append(diagnostics, diagnostic)
		previousError = err.Error()
		if attempt == changeSimulationAttempts {
			cause := fmt.Errorf(
				"structured change proposal rejected after %d attempts: %w",
				attempt, err,
			)
			return changeSimulationResult{}, &changeSimulationFailure{Cause: cause, Diagnostics: diagnostics}
		}
	}
	return changeSimulationResult{}, fmt.Errorf("structured change proposal failed")
}

func requestChangeSimulationProposal(
	ctx context.Context,
	client *openAIToolClient,
	snapshot changeSimulationSnapshot,
	previousError string,
	maxTokens int,
	temperature float64,
) (changeSimulationProposal, changeSimulationAttemptDiagnostic, error) {
	diagnostic := changeSimulationAttemptDiagnostic{Stage: "request"}
	userPrompt, err := buildChangeSimulationPrompt(snapshot)
	if err != nil {
		return changeSimulationProposal{}, diagnostic, err
	}
	if previousError != "" {
		userPrompt += "\n\nHOST_VALIDATION_ERROR предыдущей попытки:\n" + previousError +
			"\n" + changeSimulationRepairInstruction(previousError)
	}
	tool := openAIToolSpec{Type: "function", Function: openAIToolFunction{
		Name:        changeSimulationProposalToolName,
		Description: "Returns exact, non-applied repository edits. Clarifications are only unanswered user questions and must be empty when edits are present.",
		Parameters:  json.RawMessage(changeSimulationProposalSchema),
	}}
	systemPrompt := changeSimulationSystemPrompt
	message, err := client.CompleteWithToolChoice(
		ctx,
		[]openAIToolMessage{
			{Role: "system", Content: &systemPrompt},
			{Role: "user", Content: &userPrompt},
		},
		[]openAIToolSpec{tool}, "required", maxTokens, temperature,
	)
	if err != nil {
		diagnostic.Stage = "upstream"
		return changeSimulationProposal{}, diagnostic, err
	}
	diagnostic.Stage = "tool_call"
	diagnostic.FinishReason = message.FinishReason
	diagnostic.CompletionTokens = message.CompletionTokens
	if len(message.ToolCalls) != 1 ||
		message.ToolCalls[0].Function.Name != changeSimulationProposalToolName {
		if len(message.ToolCalls) > 0 {
			diagnostic.ToolName = message.ToolCalls[0].Function.Name
		}
		return changeSimulationProposal{}, diagnostic, fmt.Errorf(
			"change simulation requires exactly one %s call",
			changeSimulationProposalToolName,
		)
	}
	arguments := message.ToolCalls[0].Function.Arguments
	diagnostic.ToolName = message.ToolCalls[0].Function.Name
	diagnostic.ArgumentBytes = len(arguments)
	diagnostic.Arguments = arguments
	diagnostic.ArgumentsPreview = previewChangeSimulationArguments(arguments)
	var proposal changeSimulationProposal
	if err := decodeStrictJSONObject(
		json.RawMessage(arguments), &proposal,
	); err != nil {
		diagnostic.Stage = "decode_arguments"
		if strings.Contains(err.Error(), "unexpected EOF") || message.FinishReason == "length" {
			diagnostic.Hint = "Ответ модели, вероятно, обрезан лимитом токенов: увеличьте max_tokens или уменьшите число/размер файлов snapshot."
		}
		return changeSimulationProposal{}, diagnostic, fmt.Errorf("decode structured change proposal: %w", err)
	}
	diagnostic.ProposedChecks = append([]string(nil), proposal.Checks...)
	if err := validateChangeSimulationProposal(proposal); err != nil {
		diagnostic.Stage = "schema_validation"
		diagnostic.Hint = changeSimulationProposalHint(err.Error())
		return changeSimulationProposal{}, diagnostic, err
	}
	diagnostic.Stage = "host_validation"
	return proposal, diagnostic, nil
}

func changeSimulationRepairInstruction(previousError string) string {
	if strings.Contains(previousError, "proposal without edits requires clarifications") {
		return "Пересмотри request как требование к изменению наблюдаемого поведения. " +
			"Не считай наличие похожего config field или внутренней функции доказательством готовности: " +
			"проверь route/handler, вызов логики и response/output. Если изменение можно определить " +
			"по snapshot, верни exact edits. Иначе верни edits=[] и хотя бы один конкретный вопрос " +
			"в clarifications о недостающем API contract, entry point или source of truth. " +
			"Верни полное исправленное structured proposal."
	}
	if strings.Contains(previousError, "proposal with edits must not contain clarifications") {
		return "Proposal уже содержит edits, поэтому clarifications должен быть пустым массивом. " +
			"Тексты, которые являются пояснениями, выводами или предположениями, перенеси в analysis. " +
			"Если какой-либо пункт действительно является блокирующим вопросом к пользователю, удали edits " +
			"и сформулируй этот пункт как конкретный вопрос. При запросе на новую ручку не заменяй " +
			"существующий endpoint: сохрани обратную совместимость, добавь новый route и явно построй " +
			"требуемый response type. Верни полное исправленное structured proposal."
	}
	if strings.Contains(previousError, "old text must match") {
		return "Используй только точное текущее содержимое repository file. Если matches=0, заново скопируй old из snapshot. " +
			"Если matches больше 1, расширь old уникальным окружающим контекстом нужного route, handler, function или config section; " +
			"не меняй все совпадения и не выбирай одно наугад. Одновременно перепроверь семантику вызываемой функции: " +
			"не считай 0, пустую строку или nil значением «без ограничения», если это прямо не следует из реализации. " +
			"Верни полное исправленное structured proposal."
	}
	if strings.Contains(previousError, "no effective changes after ignoring") {
		return "Все предложенные edits оказались no-op: old и new были одинаковыми. Верни реальное изменение с отличающимся new. " +
			"Повторно проверь требуемый observable response. Если request требует не JSON/plain text, не вызывай JSON helper: " +
			"собери нужные элементы, задай подходящий Content-Type и явно сериализуй тело. Не изменяй другие endpoints. " +
			"Если изменение невозможно определить по snapshot, вместо no-op верни edits=[] и конкретный вопрос в clarifications. " +
			"Верни полное исправленное structured proposal."
	}
	return "Верни полное исправленное structured proposal."
}

func changeSimulationProposalHint(validationError string) string {
	if strings.Contains(validationError, "proposal without edits requires clarifications") {
		return "Модель не предложила изменений и не задала вопросов. Akritas повторно попросит проследить внешнее поведение и вернуть exact edits либо конкретные clarifications."
	}
	if strings.Contains(validationError, "proposal with edits must not contain clarifications") {
		return "Модель предложила edits, но записала пояснения в clarifications. Akritas попросит очистить clarifications, перенести пояснения в analysis и сохранить существующий endpoint при добавлении нового."
	}
	if strings.Contains(validationError, "old text must match") {
		return "Exact old отсутствует или неоднозначен. Akritas попросит скопировать актуальный old либо расширить его уникальным контекстом нужного route/handler и не угадывать семантику special values."
	}
	if strings.Contains(validationError, "no effective changes after ignoring") {
		return "Все edits были no-op с одинаковыми old/new. Akritas попросит вернуть реальное изменение wire format либо конкретный clarification вместо фиктивного diff."
	}
	return ""
}

func previewChangeSimulationArguments(arguments string) string {
	const maximumPreviewBytes = 4096
	if len(arguments) <= maximumPreviewBytes {
		return arguments
	}
	const half = maximumPreviewBytes / 2
	return arguments[:half] + "\n... <arguments truncated by Akritas> ...\n" + arguments[len(arguments)-half:]
}

func validateChangeSimulationProposal(proposal changeSimulationProposal) error {
	proposal.Analysis = strings.TrimSpace(proposal.Analysis)
	proposal.Rollback = strings.TrimSpace(proposal.Rollback)
	if proposal.Analysis == "" {
		return fmt.Errorf("structured change proposal requires analysis")
	}
	if len(proposal.Edits) > maximumChangeSimulationEdits ||
		len(proposal.Checks) > maximumChangeSimulationListItems ||
		len(proposal.Risks) > maximumChangeSimulationListItems ||
		len(proposal.Clarifications) > maximumChangeSimulationListItems {
		return fmt.Errorf("structured change proposal exceeds item limits")
	}
	if len(proposal.Edits) == 0 {
		if len(proposal.Clarifications) == 0 {
			return fmt.Errorf("proposal without edits requires clarifications")
		}
		return validateChangeSimulationStrings(proposal.Clarifications, "clarifications")
	}
	if len(proposal.Clarifications) != 0 {
		return fmt.Errorf("proposal with edits must not contain clarifications")
	}
	if proposal.Rollback == "" {
		return fmt.Errorf("proposal with edits requires rollback")
	}
	if err := validateChangeSimulationStrings(proposal.Checks, "checks"); err != nil {
		return err
	}
	return validateChangeSimulationStrings(proposal.Risks, "risks")
}

func validateChangeSimulationStrings(values []string, field string) error {
	for index, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s[%d] must not be empty", field, index)
		}
	}
	return nil
}

func materializeChangeSimulation(
	snapshot changeSimulationSnapshot,
	proposal changeSimulationProposal,
) (changeSimulationResult, error) {
	if err := validateChangeSimulationProposal(proposal); err != nil {
		return changeSimulationResult{}, err
	}
	result := changeSimulationResult{
		Analysis:       strings.TrimSpace(proposal.Analysis),
		Checks:         append([]string(nil), proposal.Checks...),
		Risks:          append([]string(nil), proposal.Risks...),
		Rollback:       strings.TrimSpace(proposal.Rollback),
		Clarifications: append([]string(nil), proposal.Clarifications...),
		Validation:     []string{"structured proposal schema: valid"},
	}
	if len(proposal.Edits) == 0 {
		result.Validation = append(result.Validation, "workspace changes: none; clarification required")
		return result, nil
	}
	temporaryRoot, err := os.MkdirTemp("", "akritas-change-")
	if err != nil {
		return changeSimulationResult{}, fmt.Errorf("create temporary change workspace: %w", err)
	}
	defer os.RemoveAll(temporaryRoot)
	temporaryWorkspace, err := os.OpenRoot(temporaryRoot)
	if err != nil {
		return changeSimulationResult{}, fmt.Errorf("open temporary change workspace: %w", err)
	}
	defer temporaryWorkspace.Close()

	original := make(map[string]string, len(snapshot.Files))
	for _, file := range snapshot.Files {
		original[file.Path] = file.Content
		target := filepath.FromSlash(file.Path)
		if err := temporaryWorkspace.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return changeSimulationResult{}, fmt.Errorf("create temporary parent for %q: %w", file.Path, err)
		}
		if err := temporaryWorkspace.WriteFile(target, []byte(file.Content), 0o600); err != nil {
			return changeSimulationResult{}, fmt.Errorf("write temporary file %q: %w", file.Path, err)
		}
	}

	changed := make(map[string]bool)
	created := make(map[string]bool)
	ignoredNoOps := make([]string, 0)
	for index, edit := range proposal.Edits {
		path, err := normalizeChangeSimulationEditPath(edit.Path)
		if err != nil {
			return changeSimulationResult{}, fmt.Errorf("edits[%d] path %q: %w", index, edit.Path, err)
		}
		operation := strings.TrimSpace(edit.Operation)
		if operation == "" {
			operation = "replace"
		}
		if operation == "create" {
			if edit.Old != "" {
				return changeSimulationResult{}, fmt.Errorf("edits[%d] create path %q requires empty old", index, path)
			}
			if edit.New == "" {
				return changeSimulationResult{}, fmt.Errorf("edits[%d] create path %q requires non-empty new content", index, path)
			}
			if _, exists := original[path]; exists || created[path] {
				return changeSimulationResult{}, fmt.Errorf("edits[%d] create path %q already exists in snapshot or proposal", index, path)
			}
			if err := validateChangeSimulationCreatePath(snapshot.Root, path); err != nil {
				return changeSimulationResult{}, fmt.Errorf("edits[%d] create path %q: %w", index, path, err)
			}
			if err := validateChangeSimulationContent([]byte(edit.New)); err != nil {
				return changeSimulationResult{}, fmt.Errorf("validate created file %q: %w", path, err)
			}
			target := filepath.FromSlash(path)
			if err := temporaryWorkspace.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return changeSimulationResult{}, fmt.Errorf("create temporary parent for %q: %w", path, err)
			}
			if err := temporaryWorkspace.WriteFile(target, []byte(edit.New), 0o600); err != nil {
				return changeSimulationResult{}, fmt.Errorf("write created temporary file %q: %w", path, err)
			}
			created[path], changed[path] = true, true
			continue
		}
		if operation != "replace" {
			return changeSimulationResult{}, fmt.Errorf("edits[%d] path %q has unsupported operation %q", index, path, operation)
		}
		if _, exists := original[path]; !exists {
			return changeSimulationResult{}, fmt.Errorf("edits[%d] replace path %q is not in snapshot; use operation=create only for a new file", index, path)
		}
		if edit.Old == "" {
			return changeSimulationResult{}, fmt.Errorf("edits[%d] replace path %q requires non-empty exact old text", index, path)
		}
		if edit.Old == edit.New {
			warning := fmt.Sprintf(
				"ignored no-op edits[%d] for %q because old and new are identical", index, path,
			)
			result.Warnings = append(result.Warnings, warning)
			ignoredNoOps = append(ignoredNoOps, warning)
			continue
		}
		target := filepath.FromSlash(path)
		content, err := temporaryWorkspace.ReadFile(target)
		if err != nil {
			return changeSimulationResult{}, fmt.Errorf("read temporary file %q: %w", path, err)
		}
		matches := strings.Count(string(content), edit.Old)
		if matches != 1 {
			lines := changeSimulationExactMatchLines(string(content), edit.Old, 8)
			return changeSimulationResult{}, fmt.Errorf(
				"edits[%d] old text must match %q exactly once; matches=%d; matching_start_lines=%v; use a longer exact old substring with unique route/handler/section context when matches>1",
				index, path, matches, lines,
			)
		}
		updated := strings.Replace(string(content), edit.Old, edit.New, 1)
		if err := validateChangeSimulationContent([]byte(updated)); err != nil {
			return changeSimulationResult{}, fmt.Errorf("validate edited file %q: %w", path, err)
		}
		if err := temporaryWorkspace.WriteFile(target, []byte(updated), 0o600); err != nil {
			return changeSimulationResult{}, fmt.Errorf("write edited temporary file %q: %w", path, err)
		}
		changed[path] = true
	}
	if len(changed) == 0 {
		return changeSimulationResult{}, fmt.Errorf(
			"proposal contains no effective changes after ignoring %d no-op edits: %s",
			len(ignoredNoOps), strings.Join(ignoredNoOps, "; "),
		)
	}

	paths := make([]string, 0, len(changed))
	for path := range changed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if !strings.EqualFold(filepath.Ext(path), ".go") {
			continue
		}
		target := filepath.FromSlash(path)
		content, err := temporaryWorkspace.ReadFile(target)
		if err != nil {
			return changeSimulationResult{}, fmt.Errorf("read Go file for host formatting %q: %w", path, err)
		}
		formatted, err := format.Source(content)
		if err != nil {
			return changeSimulationResult{}, fmt.Errorf("host gofmt failed for %q: %w", path, err)
		}
		if string(formatted) == string(content) {
			continue
		}
		if err := temporaryWorkspace.WriteFile(target, formatted, 0o600); err != nil {
			return changeSimulationResult{}, fmt.Errorf("write host-formatted Go file %q: %w", path, err)
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"host gofmt normalized %q before diff and validators", path,
		))
	}
	var diff strings.Builder
	totalBytes := 0
	for _, file := range snapshot.Files {
		updated, err := temporaryWorkspace.ReadFile(filepath.FromSlash(file.Path))
		if err != nil {
			return changeSimulationResult{}, fmt.Errorf("read final temporary file %q: %w", file.Path, err)
		}
		totalBytes += len(updated)
	}
	for path := range created {
		updated, err := temporaryWorkspace.ReadFile(filepath.FromSlash(path))
		if err != nil {
			return changeSimulationResult{}, fmt.Errorf("read created temporary file %q: %w", path, err)
		}
		totalBytes += len(updated)
	}
	if totalBytes > maximumChangeSimulationTotalBytes {
		return changeSimulationResult{}, fmt.Errorf("edited snapshot exceeds %d bytes", maximumChangeSimulationTotalBytes)
	}
	for _, path := range paths {
		updated, err := temporaryWorkspace.ReadFile(filepath.FromSlash(path))
		if err != nil {
			return changeSimulationResult{}, fmt.Errorf("read final temporary file %q: %w", path, err)
		}
		if !created[path] && string(updated) == original[path] {
			return changeSimulationResult{}, fmt.Errorf("edits for %q produce no final change", path)
		}
		diff.WriteString(buildHostUnifiedDiffForOperation(path, original[path], string(updated), created[path]))
	}
	result.Diff = strings.TrimSuffix(diff.String(), "\n")
	result.ChangedFiles = paths
	result.CreatedFiles = sortedChangeSimulationPaths(created)
	result.originalFiles = make(map[string]string, len(paths))
	result.updatedFiles = make(map[string]string, len(paths))
	result.createdFiles = make(map[string]bool, len(created))
	for _, path := range paths {
		updated, err := temporaryWorkspace.ReadFile(filepath.FromSlash(path))
		if err != nil {
			return changeSimulationResult{}, fmt.Errorf("read approved content %q: %w", path, err)
		}
		if !created[path] {
			result.originalFiles[path] = original[path]
		}
		result.updatedFiles[path] = string(updated)
		result.createdFiles[path] = created[path]
	}
	result.Validation = append(result.Validation,
		"replace paths: authorized snapshot files only",
		"create paths: absent files under existing safe workspace parents only",
		"exact replacements: unique and applied in temporary workspace",
		"unified diff: generated by host",
		"source workspace: unchanged",
		"model-proposed checks: not executed",
	)
	return result, nil
}

func changeSimulationExactMatchLines(content, old string, maximum int) []int {
	if old == "" || maximum <= 0 {
		return nil
	}
	lines := make([]int, 0)
	offset := 0
	for len(lines) < maximum {
		index := strings.Index(content[offset:], old)
		if index < 0 {
			break
		}
		absolute := offset + index
		lines = append(lines, 1+strings.Count(content[:absolute], "\n"))
		offset = absolute + len(old)
	}
	return lines
}

func normalizeChangeSimulationEditPath(rawPath string) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" || filepath.IsAbs(rawPath) || filepath.VolumeName(rawPath) != "" {
		return "", fmt.Errorf("must be non-empty and relative")
	}
	clean := filepath.Clean(filepath.FromSlash(rawPath))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("escapes workspace root")
	}
	normalized := filepath.ToSlash(clean)
	if normalized != rawPath {
		return "", fmt.Errorf("is not canonical")
	}
	if isChangeDiscoverySensitiveName(filepath.Base(clean)) {
		return "", fmt.Errorf("uses a protected secret filename")
	}
	return normalized, nil
}

func validateChangeSimulationCreatePath(root, path string) error {
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("workspace root is unavailable")
	}
	parent := filepath.Join(root, filepath.Dir(filepath.FromSlash(path)))
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("parent directory must already exist: %w", err)
	}
	relative, err := filepath.Rel(root, resolvedParent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("parent directory escapes workspace root")
	}
	state, err := os.Stat(resolvedParent)
	if err != nil || !state.IsDir() {
		return fmt.Errorf("parent path is not a directory")
	}
	target := filepath.Join(resolvedParent, filepath.Base(filepath.FromSlash(path)))
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("target already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect target: %w", err)
	}
	return nil
}

func sortedChangeSimulationPaths(paths map[string]bool) []string {
	result := make([]string, 0, len(paths))
	for path, included := range paths {
		if included {
			result = append(result, path)
		}
	}
	sort.Strings(result)
	return result
}

func buildHostUnifiedDiffForOperation(path, oldContent, newContent string, created bool) string {
	if !created {
		return buildHostUnifiedDiff(path, oldContent, newContent)
	}
	diff := buildHostUnifiedDiff(path, "", newContent)
	diff = strings.Replace(diff, "--- a/"+path+"\n", "new file mode 100644\n--- /dev/null\n", 1)
	return diff
}

func buildHostUnifiedDiff(path, oldContent, newContent string) string {
	oldLines, oldNewline := splitChangeDiffLines(oldContent)
	newLines, newNewline := splitChangeDiffLines(newContent)
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	if prefix == len(oldLines) && prefix == len(newLines) && oldNewline != newNewline && prefix > 0 {
		prefix--
		suffix = 0
	}
	oldChangeEnd := len(oldLines) - suffix
	newChangeEnd := len(newLines) - suffix
	start := prefix - 3
	if start < 0 {
		start = 0
	}
	oldEnd := oldChangeEnd + 3
	if oldEnd > len(oldLines) {
		oldEnd = len(oldLines)
	}
	newEnd := newChangeEnd + 3
	if newEnd > len(newLines) {
		newEnd = len(newLines)
	}
	oldStartLine := start + 1
	newStartLine := start + 1
	if len(oldLines) == 0 {
		oldStartLine = 0
	}
	if len(newLines) == 0 {
		newStartLine = 0
	}
	var output strings.Builder
	fmt.Fprintf(&output, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n", path, path, path, path)
	fmt.Fprintf(&output, "@@ -%d,%d +%d,%d @@\n", oldStartLine, oldEnd-start, newStartLine, newEnd-start)
	for index := start; index < prefix; index++ {
		writeChangeDiffLine(&output, ' ', oldLines[index], index == len(oldLines)-1 && !oldNewline)
	}
	for index := prefix; index < oldChangeEnd; index++ {
		writeChangeDiffLine(&output, '-', oldLines[index], index == len(oldLines)-1 && !oldNewline)
	}
	for index := prefix; index < newChangeEnd; index++ {
		writeChangeDiffLine(&output, '+', newLines[index], index == len(newLines)-1 && !newNewline)
	}
	for index := oldChangeEnd; index < oldEnd; index++ {
		writeChangeDiffLine(&output, ' ', oldLines[index], index == len(oldLines)-1 && !oldNewline && !newNewline)
	}
	return output.String()
}

func splitChangeDiffLines(content string) ([]string, bool) {
	if content == "" {
		return nil, false
	}
	endsWithNewline := strings.HasSuffix(content, "\n")
	if endsWithNewline {
		content = strings.TrimSuffix(content, "\n")
	}
	return strings.Split(content, "\n"), endsWithNewline
}

func writeChangeDiffLine(output *strings.Builder, prefix rune, line string, noNewline bool) {
	output.WriteRune(prefix)
	output.WriteString(line)
	output.WriteByte('\n')
	if noNewline {
		output.WriteString("\\ No newline at end of file\n")
	}
}

func formatChangeSimulationResult(result changeSimulationResult) string {
	var output strings.Builder
	output.WriteString("## Анализ\n\n")
	output.WriteString(result.Analysis)
	output.WriteString("\n")
	if len(result.Clarifications) > 0 {
		output.WriteString("\n## Нужно уточнить\n\n")
		writeChangeSimulationList(&output, result.Clarifications)
	} else {
		output.WriteString("\n## Host-generated diff\n\n```diff\n")
		output.WriteString(result.Diff)
		output.WriteString("\n```\n")
	}
	output.WriteString("\n## Host validation\n\n")
	writeChangeSimulationList(&output, result.Validation)
	if len(result.Warnings) > 0 {
		output.WriteString("\n## Host warnings\n\n")
		writeChangeSimulationList(&output, result.Warnings)
	}
	if len(result.Checks) > 0 {
		output.WriteString("\n## Предлагаемые проверки - не выполнены\n\n")
		writeChangeSimulationList(&output, result.Checks)
	}
	if len(result.Risks) > 0 {
		output.WriteString("\n## Риски\n\n")
		writeChangeSimulationList(&output, result.Risks)
	}
	if result.Rollback != "" {
		output.WriteString("\n## Откат\n\n")
		output.WriteString(result.Rollback)
		output.WriteString("\n")
	}
	if len(result.Validators) > 0 {
		output.WriteString("\n## Выполненные host validators\n\n")
		for _, validator := range result.Validators {
			fmt.Fprintf(&output, "- `%s`: **%s** (%d ms)", validator.Name, validator.Status, validator.DurationMS)
			if validator.Output != "" {
				output.WriteString(" - ")
				output.WriteString(strings.ReplaceAll(strings.TrimSpace(validator.Output), "\n", " "))
			}
			output.WriteByte('\n')
		}
	}
	fmt.Fprintf(&output, "\n_Model attempts: %d_", result.Attempts)
	return strings.TrimSpace(output.String())
}

func writeChangeSimulationList(output *strings.Builder, values []string) {
	for _, value := range values {
		output.WriteString("- ")
		output.WriteString(strings.TrimSpace(value))
		output.WriteByte('\n')
	}
}
