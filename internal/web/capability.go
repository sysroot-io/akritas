package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	localInvestigationPlanToolName = "local.investigation.submit_plan"
	maximumOpsPlanItems            = 16
	maximumOpsPlanFieldRunes       = 240
)

type opsCapabilityGap struct {
	Step       string `json:"step"`
	Capability string `json:"capability"`
	Reason     string `json:"reason"`
}

type opsInvestigationCheck struct {
	Step      string          `json:"step"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Reason    string          `json:"reason"`
}

type opsInvestigationPlanArguments struct {
	Checks []opsInvestigationCheck `json:"checks"`
	Gaps   []opsCapabilityGap      `json:"gaps"`
}

func registerOpsInvestigationPlanTool(registry *ToolRegistry) error {
	const schema = `{"type":"object","properties":{"checks":{"type":"array","minItems":0,"maxItems":16,"items":{"type":"object","properties":{"step":{"type":"string","description":"Concise description in the host-configured response language; preserve exact technical identifiers."},"tool":{"type":"string"},"arguments":{"type":"object"},"reason":{"type":"string","description":"Concise explanation in the host-configured response language of why this tool and query perform the check."}},"required":["step","tool","arguments","reason"],"additionalProperties":false}},"gaps":{"type":"array","minItems":0,"maxItems":16,"items":{"type":"object","properties":{"step":{"type":"string","description":"Concise description in the host-configured response language of the unavailable check; preserve exact technical identifiers."},"capability":{"type":"string"},"reason":{"type":"string","description":"Concise explanation in the host-configured response language of why no authorized tool can perform the check."}},"required":["step","capability","reason"],"additionalProperties":false}}},"required":["checks","gaps"],"additionalProperties":false}`
	return registry.Register(ToolDefinition{
		Name:        localInvestigationPlanToolName,
		Description: "Submits an investigation plan using the host-configured response language for step and reason fields. Checks select authorized read-only tools and exact arguments for host execution; gaps list only steps for which no suitable authorized tool exists.",
		InputSchema: json.RawMessage(schema), Permission: ToolPermissionRead,
		ValidateArguments: func(raw json.RawMessage) error {
			_, err := decodeOpsInvestigationPlan(raw)
			return err
		},
		Handler: func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
			plan, err := decodeOpsInvestigationPlan(raw)
			if err != nil {
				return nil, err
			}
			return json.Marshal(map[string]int{
				"checks": len(plan.Checks),
				"gaps":   len(plan.Gaps),
			})
		},
	})
}

func decodeOpsInvestigationPlan(raw json.RawMessage) (opsInvestigationPlanArguments, error) {
	var plan opsInvestigationPlanArguments
	if err := decodeStrictJSONObject(raw, &plan); err != nil {
		return opsInvestigationPlanArguments{}, err
	}
	if len(plan.Checks) > maximumOpsPlanItems || len(plan.Gaps) > maximumOpsPlanItems ||
		len(plan.Checks)+len(plan.Gaps) > maximumOpsPlanItems {
		return opsInvestigationPlanArguments{}, fmt.Errorf(
			"investigation plan must contain at most %d checks and gaps in total",
			maximumOpsPlanItems,
		)
	}
	seenChecks := make(map[string]bool, len(plan.Checks))
	seenSteps := make(map[string]string, len(plan.Checks)+len(plan.Gaps))
	for index := range plan.Checks {
		check := &plan.Checks[index]
		check.Step = strings.TrimSpace(check.Step)
		check.Tool = strings.TrimSpace(check.Tool)
		check.Reason = strings.TrimSpace(check.Reason)
		if check.Step == "" || check.Tool == "" || check.Reason == "" {
			return opsInvestigationPlanArguments{}, fmt.Errorf(
				"checks[%d] requires non-empty step, tool, arguments and reason", index,
			)
		}
		if !mcpToolNameValid(check.Tool) {
			return opsInvestigationPlanArguments{}, fmt.Errorf(
				"checks[%d] has invalid tool name %q", index, check.Tool,
			)
		}
		if err := validatePlanArgumentsObject(check.Arguments); err != nil {
			return opsInvestigationPlanArguments{}, fmt.Errorf("checks[%d] arguments: %w", index, err)
		}
		if planFieldTooLong(check.Step) || planFieldTooLong(check.Tool) || planFieldTooLong(check.Reason) {
			return opsInvestigationPlanArguments{}, fmt.Errorf(
				"checks[%d] fields must not exceed %d runes", index, maximumOpsPlanFieldRunes,
			)
		}
		stepKey := strings.ToLower(check.Step)
		checkKey := stepKey + "\x00" + strings.ToLower(check.Tool)
		if seenChecks[checkKey] {
			return opsInvestigationPlanArguments{}, fmt.Errorf("duplicate check at index %d", index)
		}
		seenChecks[checkKey] = true
		seenSteps[stepKey] = "check"
	}
	seenGaps := make(map[string]bool, len(plan.Gaps))
	for index := range plan.Gaps {
		gap := &plan.Gaps[index]
		gap.Step = strings.TrimSpace(gap.Step)
		gap.Capability = strings.TrimSpace(gap.Capability)
		gap.Reason = strings.TrimSpace(gap.Reason)
		if gap.Step == "" || gap.Capability == "" || gap.Reason == "" {
			return opsInvestigationPlanArguments{}, fmt.Errorf(
				"gaps[%d] requires non-empty step, capability and reason", index,
			)
		}
		if !mcpToolNameValid(gap.Capability) {
			return opsInvestigationPlanArguments{}, fmt.Errorf(
				"gaps[%d] has invalid capability name %q", index, gap.Capability,
			)
		}
		if planFieldTooLong(gap.Step) || planFieldTooLong(gap.Capability) || planFieldTooLong(gap.Reason) {
			return opsInvestigationPlanArguments{}, fmt.Errorf(
				"gaps[%d] fields must not exceed %d runes", index, maximumOpsPlanFieldRunes,
			)
		}
		stepKey := strings.ToLower(gap.Step)
		if seenSteps[stepKey] == "check" {
			return opsInvestigationPlanArguments{}, fmt.Errorf(
				"gaps[%d] duplicates a planned check step", index,
			)
		}
		gapKey := stepKey + "\x00" + strings.ToLower(gap.Capability)
		if seenGaps[gapKey] {
			return opsInvestigationPlanArguments{}, fmt.Errorf("duplicate gap at index %d", index)
		}
		seenGaps[gapKey] = true
		seenSteps[stepKey] = "gap"
	}
	return plan, nil
}

func validatePlanArgumentsObject(raw json.RawMessage) error {
	if len(raw) == 0 || !json.Valid(raw) {
		return fmt.Errorf("must be a valid JSON object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return fmt.Errorf("must be a JSON object")
	}
	return nil
}

func planFieldTooLong(value string) bool {
	return len([]rune(value)) > maximumOpsPlanFieldRunes
}

func buildOpsCapabilityGaps(result openAIToolLoopResult) []opsCapabilityGap {
	gaps := make([]opsCapabilityGap, 0)
	for index, call := range result.Calls {
		if call.Name != localInvestigationPlanToolName || index >= len(result.Results) ||
			result.Results[index].Error != nil {
			continue
		}
		plan, err := decodeOpsInvestigationPlan(call.Arguments)
		if err != nil {
			continue
		}
		gaps = append(gaps, plan.Gaps...)
	}
	return gaps
}
