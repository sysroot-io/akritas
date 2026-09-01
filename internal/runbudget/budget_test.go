package runbudget

import (
	"errors"
	"testing"
	"time"
)

func testLimits() Limits {
	return Limits{
		MaxDuration: time.Minute, MaxIterations: 2, MaxToolCalls: 1,
		MaxToolResultBytes: 16, MaxRetrievedContextBytes: 8,
		MaxContextTokens: 12, MaxModelTokens: 10,
	}
}

func TestTrackerEnforcesHardLimits(t *testing.T) {
	tracker, err := New(testLimits())
	if err != nil {
		t.Fatal(err)
	}
	reservation, allowed, err := tracker.BeginModelCall(8)
	if err != nil || allowed != 8 {
		t.Fatalf("first reservation = %d, %v", allowed, err)
	}
	if err := tracker.FinishModelCall(reservation, 4); err != nil {
		t.Fatal(err)
	}
	_, allowed, err = tracker.BeginModelCall(8)
	if err != nil || allowed != 6 {
		t.Fatalf("second reservation = %d, %v", allowed, err)
	}
	if _, _, err := tracker.BeginModelCall(1); !errors.Is(err, ErrExhausted) {
		t.Fatalf("iteration limit was not enforced: %v", err)
	}
	if err := tracker.RecordToolCall(); err != nil {
		t.Fatal(err)
	}
	if err := tracker.RecordToolCall(); !errors.Is(err, ErrExhausted) {
		t.Fatalf("tool call limit was not enforced: %v", err)
	}
	if err := tracker.RecordToolResult(9, true); !errors.Is(err, ErrExhausted) {
		t.Fatalf("retrieved context limit was not enforced: %v", err)
	}
	if err := tracker.RecordContextBytes(13); !errors.Is(err, ErrExhausted) {
		t.Fatalf("context limit was not enforced: %v", err)
	}
}

func TestTrackerChargesReservationWhenUsageIsMissing(t *testing.T) {
	tracker, err := New(testLimits())
	if err != nil {
		t.Fatal(err)
	}
	reservation, _, err := tracker.BeginModelCall(7)
	if err != nil {
		t.Fatal(err)
	}
	if err := tracker.FinishModelCall(reservation, -1); err != nil {
		t.Fatal(err)
	}
	if got := tracker.Snapshot().Usage.ModelTokens; got != 7 {
		t.Fatalf("model tokens = %d, want conservative reservation 7", got)
	}
}
