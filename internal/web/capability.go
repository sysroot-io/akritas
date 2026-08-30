package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	localCapabilityGapToolName = "local.capability.report_missing"
	maximumOpsCapabilityGaps   = 16
	maximumOpsGapFieldRunes    = 240
)

type opsCapabilityGap struct {
	Step       string `json:"step"`
	Capability string `json:"capability"`
	Reason     string `json:"reason"`
}

type opsCapabilityGapArguments struct {
	Gaps []opsCapabilityGap `json:"gaps"`
}

func registerOpsCapabilityGapTool(registry *ToolRegistry) error {
	const schema = `{"type":"object","properties":{"gaps":{"type":"array","minItems":0,"maxItems":16,"items":{"type":"object","properties":{"step":{"type":"string"},"capability":{"type":"string"},"reason":{"type":"string"}},"required":["step","capability","reason"],"additionalProperties":false}}},"required":["gaps"],"additionalProperties":false}`
	decode := func(raw json.RawMessage) (opsCapabilityGapArguments, error) {
		var arguments opsCapabilityGapArguments
		if err := decodeStrictJSONObject(raw, &arguments); err != nil {
			return opsCapabilityGapArguments{}, err
		}
		if len(arguments.Gaps) > maximumOpsCapabilityGaps {
			return opsCapabilityGapArguments{}, fmt.Errorf(
				"gaps must contain at most %d items", maximumOpsCapabilityGaps,
			)
		}
		seen := make(map[string]bool, len(arguments.Gaps))
		for index := range arguments.Gaps {
			gap := &arguments.Gaps[index]
			gap.Step = strings.TrimSpace(gap.Step)
			gap.Capability = strings.TrimSpace(gap.Capability)
			gap.Reason = strings.TrimSpace(gap.Reason)
			if gap.Step == "" || gap.Capability == "" || gap.Reason == "" {
				return opsCapabilityGapArguments{}, fmt.Errorf(
					"gaps[%d] requires non-empty step, capability and reason", index,
				)
			}
			if len([]rune(gap.Step)) > maximumOpsGapFieldRunes ||
				len([]rune(gap.Capability)) > maximumOpsGapFieldRunes ||
				len([]rune(gap.Reason)) > maximumOpsGapFieldRunes {
				return opsCapabilityGapArguments{}, fmt.Errorf(
					"gaps[%d] fields must not exceed %d runes", index, maximumOpsGapFieldRunes,
				)
			}
			key := strings.ToLower(gap.Step + "\x00" + gap.Capability)
			if seen[key] {
				return opsCapabilityGapArguments{}, fmt.Errorf("duplicate gap at index %d", index)
			}
			seen[key] = true
		}
		return arguments, nil
	}
	return registry.Register(ToolDefinition{
		Name: localCapabilityGapToolName,
		Description: "Reports requested or runbook steps that cannot be executed because no available tool provides the required capability. " +
			"This records a capability gap and does not execute the missing step. Call once with every missing capability before the final answer; do not report tools that were successfully called.",
		InputSchema: json.RawMessage(schema), Permission: ToolPermissionRead,
		ValidateArguments: func(raw json.RawMessage) error {
			_, err := decode(raw)
			return err
		},
		Handler: func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
			arguments, err := decode(raw)
			if err != nil {
				return nil, err
			}
			return json.Marshal(map[string]int{"recorded": len(arguments.Gaps)})
		},
	})
}

func buildOpsCapabilityGaps(result openAIToolLoopResult) []opsCapabilityGap {
	gaps := make([]opsCapabilityGap, 0)
	for index, call := range result.Calls {
		if call.Name != localCapabilityGapToolName || index >= len(result.Results) ||
			result.Results[index].Error != nil {
			continue
		}
		var arguments opsCapabilityGapArguments
		if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
			continue
		}
		gaps = append(gaps, arguments.Gaps...)
	}
	return gaps
}
