package investigation

import "testing"

func TestResultValidatesHostOwnedEvidence(t *testing.T) {
	result := Result{
		FindingStatus:      FindingConfirmed,
		Actionability:      ActionRequiresHuman,
		Confidence:         ConfidenceHigh,
		Summary:            "Storage latency coincided with backup activity.",
		Hypothesis:         "The backup saturated the storage device.",
		Evidence:           []string{"tool-call:call_1"},
		AffectedComponents: []string{"postgresql:primary"},
		RecommendedActions: []string{"Review the backup window."},
	}
	if err := result.Validate(map[string]struct{}{"tool-call:call_1": {}}); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	result.Evidence = []string{"tool-call:invented"}
	if err := result.Validate(map[string]struct{}{"tool-call:call_1": {}}); err == nil {
		t.Fatal("invented evidence reference was accepted")
	}
}

func TestResultKeepsConfidenceOutOfAuthorization(t *testing.T) {
	for _, confidence := range []Confidence{ConfidenceLow, ConfidenceMedium, ConfidenceHigh} {
		result := Result{
			FindingStatus:      FindingSuspected,
			Actionability:      ActionRequiresHuman,
			Confidence:         confidence,
			Summary:            "The cause requires human verification.",
			Evidence:           []string{},
			AffectedComponents: []string{},
			RecommendedActions: []string{},
		}
		if err := result.Validate(nil); err != nil {
			t.Fatalf("confidence %q changed structural validity: %v", confidence, err)
		}
	}
}
