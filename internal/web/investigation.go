package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"akritas/internal/investigation"
	"akritas/internal/modeltext"
)

const investigationResultToolName = "local.investigation.submit_result"

var investigationResultSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "finding_status": {"type": "string", "enum": ["confirmed", "suspected", "unknown"]},
    "actionability": {"type": "string", "enum": ["proposal_possible", "requires_human", "no_action"]},
    "confidence": {"type": "string", "enum": ["high", "medium", "low"]},
    "summary": {"type": "string", "minLength": 1, "maxLength": 4096},
    "hypothesis": {"type": "string", "maxLength": 4096},
    "evidence": {"type": "array", "maxItems": 64, "items": {"type": "string", "maxLength": 1024}},
    "affected_components": {"type": "array", "maxItems": 64, "items": {"type": "string", "maxLength": 1024}},
    "recommended_actions": {"type": "array", "maxItems": 64, "items": {"type": "string", "maxLength": 1024}}
  },
  "required": ["finding_status", "actionability", "confidence", "summary", "evidence", "affected_components", "recommended_actions"]
}`)

type investigationEvidenceItem struct {
	Reference string `json:"reference"`
	Tool      string `json:"tool"`
	Status    string `json:"status"`
}

func (server *opsServer) structureInvestigationResult(
	ctx context.Context,
	loopResult openAIToolLoopResult,
) (investigation.Result, error) {
	evidence := make([]investigationEvidenceItem, 0, len(loopResult.Calls))
	available := make(map[string]struct{}, len(loopResult.Calls))
	for index, call := range loopResult.Calls {
		status := "succeeded"
		if index < len(loopResult.Results) && loopResult.Results[index].Error != nil {
			status = "failed"
		}
		reference := "tool-call:" + call.ID
		evidence = append(evidence, investigationEvidenceItem{
			Reference: reference, Tool: call.Name, Status: status,
		})
		available[reference] = struct{}{}
	}
	input, err := json.Marshal(map[string]any{
		"answer":              strings.TrimSpace(loopResult.Answer),
		"host_owned_evidence": evidence,
	})
	if err != nil {
		return investigation.Result{}, fmt.Errorf("encode investigation result input: %w", err)
	}
	systemPrompt := `Convert the investigation answer into the required structured result. Use only evidence references listed in host_owned_evidence. Evidence may be empty. Confidence is descriptive only and never authorizes an action. Call the required tool exactly once.` + "\n\n" + modeltext.LanguageInstruction(server.responseLanguage)
	definition := ToolDefinition{
		Name:        investigationResultToolName,
		Description: "Submit a structured investigation result for host validation.",
		InputSchema: investigationResultSchema,
	}
	tools, aliases := buildOpenAITools([]ToolDefinition{definition})
	message, err := server.client.CompleteWithToolChoiceBudgeted(
		ctx,
		[]openAIToolMessage{
			{Role: "system", Content: &systemPrompt},
			{Role: "user", Content: openAIStringPointer(string(input))},
		},
		tools, "required", server.defaultMaxTokens, 0, loopResult.Tracker,
	)
	if err != nil {
		return investigation.Result{}, fmt.Errorf("structure investigation result: %w", err)
	}
	if len(message.ToolCalls) != 1 {
		return investigation.Result{}, fmt.Errorf("structured investigation returned %d tool calls, want 1", len(message.ToolCalls))
	}
	call := message.ToolCalls[0]
	internalName, exists := aliases[call.Function.Name]
	if !exists || internalName != investigationResultToolName {
		return investigation.Result{}, fmt.Errorf("structured investigation requested unexpected tool %q", call.Function.Name)
	}
	var result investigation.Result
	if err := decodeStrictJSONObject([]byte(call.Function.Arguments), &result); err != nil {
		return investigation.Result{}, fmt.Errorf("decode structured investigation result: %w", err)
	}
	result = result.Normalized()
	if err := result.Validate(available); err != nil {
		return investigation.Result{}, fmt.Errorf("validate structured investigation result: %w", err)
	}
	return result, nil
}
