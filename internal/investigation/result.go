package investigation

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	maximumSummaryRunes    = 4096
	maximumHypothesisRunes = 4096
	maximumListItems       = 64
	maximumItemRunes       = 1024
)

type FindingStatus string

const (
	FindingConfirmed FindingStatus = "confirmed"
	FindingSuspected FindingStatus = "suspected"
	FindingUnknown   FindingStatus = "unknown"
)

type Actionability string

const (
	ActionProposalPossible Actionability = "proposal_possible"
	ActionRequiresHuman    Actionability = "requires_human"
	ActionNone             Actionability = "no_action"
)

type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

type Result struct {
	FindingStatus      FindingStatus `json:"finding_status"`
	Actionability      Actionability `json:"actionability"`
	Confidence         Confidence    `json:"confidence"`
	Summary            string        `json:"summary"`
	Hypothesis         string        `json:"hypothesis,omitempty"`
	Evidence           []string      `json:"evidence"`
	AffectedComponents []string      `json:"affected_components"`
	RecommendedActions []string      `json:"recommended_actions"`
}

var evidenceReferencePattern = regexp.MustCompile(`^(tool-call|context|event|artifact):[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Validate verifies the model-provided structure against host-owned evidence.
// availableEvidence is the complete allowlist of references created by the host.
func (result Result) Validate(availableEvidence map[string]struct{}) error {
	if !validFindingStatus(result.FindingStatus) {
		return fmt.Errorf("invalid finding_status %q", result.FindingStatus)
	}
	if !validActionability(result.Actionability) {
		return fmt.Errorf("invalid actionability %q", result.Actionability)
	}
	if !validConfidence(result.Confidence) {
		return fmt.Errorf("invalid confidence %q", result.Confidence)
	}
	if err := validateText("summary", result.Summary, maximumSummaryRunes, true); err != nil {
		return err
	}
	if err := validateText("hypothesis", result.Hypothesis, maximumHypothesisRunes, false); err != nil {
		return err
	}
	if err := validateList("evidence", result.Evidence, maximumListItems, maximumItemRunes); err != nil {
		return err
	}
	if err := validateList("affected_components", result.AffectedComponents, maximumListItems, maximumItemRunes); err != nil {
		return err
	}
	if err := validateList("recommended_actions", result.RecommendedActions, maximumListItems, maximumItemRunes); err != nil {
		return err
	}
	for index, reference := range result.Evidence {
		if !evidenceReferencePattern.MatchString(reference) {
			return fmt.Errorf("evidence[%d] has invalid reference %q", index, reference)
		}
		if _, exists := availableEvidence[reference]; !exists {
			return fmt.Errorf("evidence[%d] references unknown host evidence %q", index, reference)
		}
	}
	return nil
}

func (result Result) Normalized() Result {
	result.Summary = strings.TrimSpace(result.Summary)
	result.Hypothesis = strings.TrimSpace(result.Hypothesis)
	result.Evidence = normalizedList(result.Evidence)
	result.AffectedComponents = normalizedList(result.AffectedComponents)
	result.RecommendedActions = normalizedList(result.RecommendedActions)
	return result
}

func validFindingStatus(value FindingStatus) bool {
	return value == FindingConfirmed || value == FindingSuspected || value == FindingUnknown
}

func validActionability(value Actionability) bool {
	return value == ActionProposalPossible || value == ActionRequiresHuman || value == ActionNone
}

func validConfidence(value Confidence) bool {
	return value == ConfidenceHigh || value == ConfidenceMedium || value == ConfidenceLow
}

func validateText(name, value string, maximumRunes int, required bool) error {
	trimmed := strings.TrimSpace(value)
	if required && trimmed == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len([]rune(trimmed)) > maximumRunes {
		return fmt.Errorf("%s exceeds %d runes", name, maximumRunes)
	}
	return nil
}

func validateList(name string, values []string, maximumItems, maximumRunes int) error {
	if len(values) > maximumItems {
		return fmt.Errorf("%s exceeds %d items", name, maximumItems)
	}
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return fmt.Errorf("%s[%d] is empty", name, index)
		}
		if len([]rune(trimmed)) > maximumRunes {
			return fmt.Errorf("%s[%d] exceeds %d runes", name, index, maximumRunes)
		}
		if _, exists := seen[trimmed]; exists {
			return fmt.Errorf("%s contains duplicate value %q", name, trimmed)
		}
		seen[trimmed] = struct{}{}
	}
	return nil
}

func normalizedList(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	normalized := make([]string, len(values))
	for index := range values {
		normalized[index] = strings.TrimSpace(values[index])
	}
	sort.Strings(normalized)
	return normalized
}
