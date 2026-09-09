package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"akritas/internal/investigation"
)

func TestStorePersistsRunLifecycleAndRedactsMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.StartRun("chat", "api-key", "backend", "akritas", map[string]string{
		"request_kind": "chat", "authorization": "Bearer secret", "api_key": "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddToolEvent(run.ID, "ok", map[string]string{
		"tool": "local.echo", "call_id": "call_1", "token": "secret",
	}, json.RawMessage(`{"value":"request"}`), json.RawMessage(`{"output":{"value":"response"}}`)); err != nil {
		t.Fatal(err)
	}
	result := investigation.Result{
		FindingStatus:      investigation.FindingConfirmed,
		Actionability:      investigation.ActionRequiresHuman,
		Confidence:         investigation.ConfidenceHigh,
		Summary:            "The check confirmed the finding.",
		Evidence:           []string{"tool-call:call_1"},
		AffectedComponents: []string{"backend"},
		RecommendedActions: []string{"Review the result."},
	}
	if err := store.SetInvestigationResult(run.ID, result); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(run.ID, RunSucceeded, "", map[string]string{"answer_bytes": "12"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddFollowUpEvent(run.ID, "follow_up", "succeeded", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Bearer secret") || strings.Contains(string(raw), `"api_key"`) ||
		strings.Contains(string(raw), `"token"`) {
		t.Fatalf("audit store leaked sensitive metadata: %s", raw)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, exists := reopened.Get(run.ID)
	if !exists || loaded.Status != RunSucceeded || len(loaded.Events) != 2 ||
		loaded.Events[0].Metadata["tool"] != "local.echo" ||
		string(loaded.Events[0].Arguments) != `{"value":"request"}` ||
		string(loaded.Events[0].Result) != `{"output":{"value":"response"}}` ||
		loaded.Investigation == nil || loaded.Investigation.Summary != result.Summary ||
		loaded.Metadata["request_kind"] != "chat" || loaded.Metadata["answer_bytes"] != "12" {
		t.Fatalf("unexpected reloaded run: %+v", loaded)
	}
}

func TestStoreRejectsCorruptHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(path, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("corrupt audit history was accepted")
	}
}

func TestStoreRejectsFinishingRunTwice(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	run, err := store.StartRun("change", "anonymous", "demo", "akritas", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(run.ID, RunFailed, "rejected", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(run.ID, RunSucceeded, "", nil); err == nil {
		t.Fatal("audit run was finalized twice")
	}
}
