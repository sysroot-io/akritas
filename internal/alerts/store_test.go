package alerts

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreDeduplicatesCorrelatesAndRecoversJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.jsonl")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	firing := canonicalTestEvent(t, StateFiring, "event-firing", "incident-key", now)
	delivery, err := store.Ingest("test", []byte("first"), []Event{firing}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(delivery.Events) != 1 || delivery.Events[0].Status != DeliveryAccepted || delivery.Events[0].JobID == "" {
		t.Fatalf("unexpected first delivery: %+v", delivery)
	}
	duplicate, err := store.Ingest("test", []byte("duplicate"), []Event{firing}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Events[0].Status != DeliveryDuplicate || duplicate.Events[0].EventID != delivery.Events[0].EventID {
		t.Fatalf("unexpected duplicate delivery: %+v", duplicate)
	}
	job, found, err := store.ClaimNext()
	if err != nil || !found || job.ID != delivery.Events[0].JobID || job.Attempts != 1 {
		t.Fatalf("unexpected claimed job: %+v found=%v err=%v", job, found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if recovered, err := store.RecoverRunning(); err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	job, found, err = store.ClaimNext()
	if err != nil || !found || job.Attempts != 2 {
		t.Fatalf("unexpected recovered job: %+v found=%v err=%v", job, found, err)
	}
	if err := store.CompleteJob(job.ID, JobSucceeded, "run_1", "", ""); err != nil {
		t.Fatal(err)
	}
	finished, err := store.WaitJob(context.Background(), job.ID)
	if err != nil || finished.Status != JobSucceeded || finished.RunID != "run_1" {
		t.Fatalf("unexpected completed job: %+v err=%v", finished, err)
	}

	resolved := canonicalTestEvent(t, StateResolved, "event-resolved", "incident-key", now.Add(time.Minute))
	resolvedDelivery, err := store.Ingest("test", []byte("resolved"), []Event{resolved}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	incident, exists := store.GetIncident(resolvedDelivery.Events[0].IncidentID)
	if !exists || incident.State != StateResolved || incident.ResolvedAt == nil || incident.LastRunID != "run_1" {
		t.Fatalf("unexpected resolved incident: %+v", incident)
	}

	reopened := canonicalTestEvent(t, StateFiring, "event-reopened", "incident-key", now.Add(2*time.Minute))
	reopenedDelivery, err := store.Ingest("test", []byte("reopened"), []Event{reopened}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if reopenedDelivery.Events[0].IncidentID == incident.ID || reopenedDelivery.Events[0].JobID == "" {
		t.Fatalf("reopened alert did not create a new incident and job: %+v", reopenedDelivery)
	}
}

func TestStorePersistsFailedJobDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.jsonl")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 6, 23, 51, 40, 0, time.UTC)
	delivery, err := store.Ingest(
		"test", []byte("failure"),
		[]Event{canonicalTestEvent(t, StateFiring, "failure-event", "failure-incident", now)}, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := store.ClaimNext()
	if err != nil || !found {
		t.Fatalf("claim failed job: found=%v err=%v", found, err)
	}
	detail := "OpenAI chat completions: HTTP 401: api_key=top-secret Authorization: Bearer abc.def\nrequest denied"
	if err := store.CompleteJob(job.ID, JobFailed, "run_failed", "investigation_failed", detail); err != nil {
		t.Fatal(err)
	}
	failed, exists := store.GetJob(job.ID)
	if !exists || failed.Status != JobFailed || failed.RunID != "run_failed" ||
		failed.Error != "investigation_failed" || !strings.Contains(failed.ErrorDetail, "HTTP 401") ||
		strings.Contains(failed.ErrorDetail, "top-secret") || strings.Contains(failed.ErrorDetail, "abc.def") {
		t.Fatalf("unexpected failed job: %+v", failed)
	}
	incident, exists := store.GetIncident(delivery.Events[0].IncidentID)
	if !exists || incident.LastRunID != "run_failed" || len(incident.RunIDs) != 1 {
		t.Fatalf("failed Run was not linked to incident: %+v", incident)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, exists := reopened.GetJob(job.ID)
	if !exists || persisted.RunID != "run_failed" || persisted.ErrorDetail != failed.ErrorDetail {
		t.Fatalf("failed diagnostics did not survive restart: %+v", persisted)
	}
}

func canonicalTestEvent(t *testing.T, state State, dedupeKey, correlationKey string, observedAt time.Time) Event {
	t.Helper()
	event, err := Normalize(Event{
		State: state, Name: "HighCPU", Severity: SeverityCritical, Summary: "CPU high",
		Entity: Entity{Kind: "host", ID: "pg01"}, StartedAt: observedAt, ObservedAt: observedAt,
		DeduplicationKey: dedupeKey, CorrelationKey: correlationKey,
	}, "generic", "test", observedAt)
	if err != nil {
		t.Fatal(err)
	}
	return event
}
