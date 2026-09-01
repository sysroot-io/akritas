package change

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadChangeSimulationSnapshotAndBuildPrompt(t *testing.T) {
	snapshot, err := loadChangeSimulationSnapshot(
		"../../testdata/ops-change/nftables", "REQUEST.md",
		[]string{
			"inventory/backends.yaml",
			"pillar/prod/nftables.sls",
			"salt/nftables/README.md",
			"salt/nftables/templates/backends.nft.jinja",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RequestPath != "REQUEST.md" || len(snapshot.Files) != 4 ||
		snapshot.Files[0].Path != "inventory/backends.yaml" {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	prompt, err := buildChangeSimulationPrompt(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, `"request_path": "REQUEST.md"`) ||
		!strings.Contains(prompt, `"path": "pillar/prod/nftables.sls"`) ||
		!strings.Contains(prompt, "10.20.4.15") {
		t.Fatalf("prompt omitted snapshot data: %s", prompt)
	}
}

func TestLoadChangeSimulationSnapshotRejectsTraversalAndDuplicates(t *testing.T) {
	root := "../../testdata/ops-change/nftables"
	if _, err := loadChangeSimulationSnapshot(
		root, "REQUEST.md", []string{"../outside.yaml"},
	); err == nil || !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("path traversal was accepted: %v", err)
	}
	if _, err := loadChangeSimulationSnapshot(
		root, "REQUEST.md",
		[]string{"inventory/backends.yaml", "inventory/backends.yaml"},
	); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate path was accepted: %v", err)
	}
}

func TestLoadChangeSimulationSnapshotRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape.txt")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	if _, _, err := loadChangeSimulationFile(root, "escape.txt"); err == nil || !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("symlink escape was accepted: %v", err)
	}
}

func TestLoadChangeSimulationSnapshotRejectsOversizedFile(t *testing.T) {
	err := validateChangeSimulationContent(
		[]byte(strings.Repeat("x", maximumChangeSimulationFileBytes+1)),
	)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized file was accepted: %v", err)
	}
}

func TestMaterializeChangeSimulationBuildsHostDiffWithoutChangingSource(t *testing.T) {
	snapshot, err := loadChangeSimulationSnapshot(
		"../../testdata/ops-change/nftables", "REQUEST.md",
		[]string{"inventory/backends.yaml", "pillar/prod/nftables.sls"},
	)
	if err != nil {
		t.Fatal(err)
	}
	original := snapshot.Files[1].Content
	old := `    - name: orders-v1
      address: 10.20.4.10
      port: 8443
      sources:
        - frontend`
	proposal := changeSimulationProposal{
		Analysis: "Pillar is the source of truth.",
		Edits: []changeSimulationEdit{{
			Path: "pillar/prod/nftables.sls", Old: old,
			New: old + `
    - name: orders-v2
      address: 10.20.4.15
      port: 8443
      sources:
        - frontend`,
		}},
		Checks:   []string{"yamllint pillar/prod/nftables.sls"},
		Risks:    []string{"Incorrect source may broaden access."},
		Rollback: "Revert the generated change.",
	}
	result, err := materializeChangeSimulation(snapshot, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ChangedFiles) != 1 || result.ChangedFiles[0] != "pillar/prod/nftables.sls" ||
		!strings.Contains(result.Diff, "@@ -5,3 +5,8 @@") ||
		!strings.Contains(result.Diff, "+    - name: orders-v2") ||
		strings.Contains(result.Diff, "index 123") {
		t.Fatalf("unexpected host diff/result: %+v", result)
	}
	current, _, err := loadChangeSimulationFile(
		mustResolveChangeSimulationRoot(t, "../../testdata/ops-change/nftables"),
		"pillar/prod/nftables.sls",
	)
	if err != nil {
		t.Fatal(err)
	}
	if current != original {
		t.Fatal("source workspace was modified")
	}
}

func TestMaterializeChangeSimulationRejectsMissingOrAmbiguousOldText(t *testing.T) {
	snapshot := changeSimulationSnapshot{
		RequestPath: "REQUEST.md", Request: "Change value.",
		Files: []changeSimulationFile{{Path: "config.yaml", Content: "value: old\nvalue: old\n"}},
	}
	proposal := changeSimulationProposal{
		Analysis: "Change config.",
		Edits:    []changeSimulationEdit{{Path: "config.yaml", Old: "value: old", New: "value: new"}},
		Rollback: "Revert.",
	}
	if _, err := materializeChangeSimulation(snapshot, proposal); err == nil ||
		!strings.Contains(err.Error(), "matches=2") ||
		!strings.Contains(err.Error(), "matching_start_lines=[1 2]") ||
		!strings.Contains(err.Error(), "longer exact old substring") {
		t.Fatalf("ambiguous replacement was accepted: %v", err)
	}
	proposal.Edits[0].Old = "missing: value"
	if _, err := materializeChangeSimulation(snapshot, proposal); err == nil ||
		!strings.Contains(err.Error(), "matches=0") {
		t.Fatalf("missing replacement was accepted: %v", err)
	}
	proposal.Edits[0] = changeSimulationEdit{
		Path: "nested/../config.yaml", Old: "value: old\nvalue: old", New: "value: new",
	}
	if _, err := materializeChangeSimulation(snapshot, proposal); err == nil ||
		!strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("non-canonical edit path was accepted: %v", err)
	}
}

func TestMaterializeChangeSimulationExplainsEmptyReplaceAndDropsNoOp(t *testing.T) {
	snapshot := changeSimulationSnapshot{
		RequestPath: "REQUEST.md", Request: "Change value.",
		Files: []changeSimulationFile{{Path: "config.yaml", Content: "value: old\n"}},
	}
	proposal := changeSimulationProposal{
		Analysis: "Change config.", Rollback: "Revert.",
		Edits: []changeSimulationEdit{{Path: "config.yaml", Old: "", New: "value: new\n"}},
	}
	if _, err := materializeChangeSimulation(snapshot, proposal); err == nil ||
		!strings.Contains(err.Error(), `replace path "config.yaml" requires non-empty exact old text`) {
		t.Fatalf("empty old diagnostic=%v", err)
	}
	proposal.Edits = []changeSimulationEdit{
		{Path: "config.yaml", Old: "value: old", New: "value: old"},
		{Path: "config.yaml", Old: "value: old", New: "value: new"},
	}
	result, err := materializeChangeSimulation(snapshot, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "ignored no-op edits[0]") ||
		!strings.Contains(result.Diff, "+value: new") {
		t.Fatalf("no-op was not dropped cleanly: %+v", result)
	}
}

func TestMaterializeChangeSimulationExplainsAllNoOpProposal(t *testing.T) {
	snapshot := changeSimulationSnapshot{
		RequestPath: "<inline>", Request: "Return plain text.",
		Files: []changeSimulationFile{{Path: "server.go", Content: "package main\n"}},
	}
	proposal := changeSimulationProposal{
		Analysis: "Change response.", Rollback: "Revert.",
		Edits: []changeSimulationEdit{{Path: "server.go", Old: "package main", New: "package main"}},
	}
	_, err := materializeChangeSimulation(snapshot, proposal)
	if err == nil || !strings.Contains(err.Error(), "ignoring 1 no-op edits") ||
		!strings.Contains(err.Error(), `edits[0] for "server.go"`) ||
		!strings.Contains(err.Error(), "old and new are identical") {
		t.Fatalf("all-no-op diagnostic=%v", err)
	}
}

func TestMaterializeChangeSimulationCreatesExplicitNewFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot := changeSimulationSnapshot{
		Root: root, RequestPath: "<inline>", Request: "Add test.",
		Files: []changeSimulationFile{{Path: "api/server.go", Content: "package api\n"}},
	}
	proposal := changeSimulationProposal{
		Analysis: "Add coverage.", Rollback: "Remove the new test.",
		Edits: []changeSimulationEdit{{
			Operation: "create", Path: "api/server_test.go", Old: "",
			New: "package api\n\nfunc Example() {}\n",
		}},
	}
	result, err := materializeChangeSimulation(snapshot, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CreatedFiles) != 1 || result.CreatedFiles[0] != "api/server_test.go" ||
		!strings.Contains(result.Diff, "new file mode 100644") ||
		!strings.Contains(result.Diff, "--- /dev/null") || result.originalFiles["api/server_test.go"] != "" ||
		!result.createdFiles["api/server_test.go"] {
		t.Fatalf("unexpected create result: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "api", "server_test.go")); !os.IsNotExist(err) {
		t.Fatalf("preview created source file: %v", err)
	}
}

func TestMaterializeChangeSimulationFormatsGoBeforeDiff(t *testing.T) {
	root := t.TempDir()
	original := "package api\n\nfunc Existing() {}\n"
	if err := os.WriteFile(filepath.Join(root, "server.go"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := changeSimulationSnapshot{
		Root: root, RequestPath: "<inline>", Request: "Add handler.",
		Files: []changeSimulationFile{{Path: "server.go", Content: original}},
	}
	proposal := changeSimulationProposal{
		Analysis: "Add handler.",
		Edits: []changeSimulationEdit{{
			Path: "server.go", Old: "func Existing() {}",
			New: "func Existing(){}\nfunc ListJA4( )[]string{return nil}",
		}},
		Checks: []string{"go test ./..."}, Risks: []string{"New API."}, Rollback: "Revert.",
	}
	result, err := materializeChangeSimulation(snapshot, proposal)
	if err != nil {
		t.Fatal(err)
	}
	want := "func ListJA4() []string { return nil }"
	if !strings.Contains(result.updatedFiles["server.go"], want) || !strings.Contains(result.Diff, "+"+want) {
		t.Fatalf("host-formatted result is missing from content/diff: %+v\n%s", result.updatedFiles, result.Diff)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "host gofmt normalized") {
		t.Fatalf("missing host gofmt warning: %+v", result.Warnings)
	}
	content, err := os.ReadFile(filepath.Join(root, "server.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("source workspace was changed: %q", content)
	}
}

func TestBuildHostUnifiedDiffUsesValidHunkCounts(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
		want string
	}{
		{name: "replace", old: "a\nb\nc\n", new: "a\nx\nc\n", want: "@@ -1,3 +1,3 @@"},
		{name: "create content", old: "", new: "x\n", want: "@@ -0,0 +1,1 @@"},
		{name: "remove content", old: "x\n", new: "", want: "@@ -1,1 +0,0 @@"},
		{name: "missing newline", old: "x", new: "y", want: "\\ No newline at end of file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			diff := buildHostUnifiedDiff("config.txt", test.old, test.new)
			if !strings.Contains(diff, test.want) {
				t.Fatalf("diff does not contain %q:\n%s", test.want, diff)
			}
		})
	}
}

func TestRunChangeSimulationUsesForcedStructuredProposalAndRepairs(t *testing.T) {
	snapshot := changeSimulationSnapshot{
		RequestPath: "REQUEST.md", Request: "Allow orders-v2.",
		Files: []changeSimulationFile{{
			Path: "pillar/nftables.sls", Content: "allowlist: []\n",
		}},
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			http.NotFound(writer, request)
			return
		}
		var input openAIToolRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests++
		if input.Model != "qwen-test" || len(input.Tools) != 1 || len(input.Messages) != 2 ||
			input.ToolChoice != "required" || input.Tools[0].Function.Name != changeSimulationProposalToolName {
			t.Errorf("unexpected OpenAI input: %+v", input)
		}
		if input.Messages[0].Role != "system" || input.Messages[0].Content == nil ||
			!strings.Contains(*input.Messages[0].Content, `BCP 47 tag "fr"`) ||
			input.Messages[1].Content == nil ||
			!strings.Contains(*input.Messages[1].Content, "pillar/nftables.sls") {
			t.Errorf("snapshot was not passed in messages: %+v", input.Messages)
		}
		old := "missing"
		if requests == 2 {
			old = "allowlist: []"
			if !strings.Contains(*input.Messages[1].Content, "HOST_VALIDATION_ERROR") {
				t.Error("repair request omitted host validation error")
			}
		}
		proposal := changeSimulationProposal{
			Analysis: "Update source of truth.",
			Edits: []changeSimulationEdit{{
				Path: "pillar/nftables.sls", Old: old,
				New: "allowlist:\n  - orders-v2",
			}},
			Checks: []string{"yamllint pillar/nftables.sls"},
			Risks:  []string{"Wrong allowlist entry."}, Rollback: "Revert.",
		}
		arguments, err := json.Marshal(proposal)
		if err != nil {
			t.Fatal(err)
		}
		writeJSON(writer, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": nil,
				"tool_calls": []any{map[string]any{
					"id": "call_change", "type": "function",
					"function": map[string]any{
						"name":      changeSimulationProposalToolName,
						"arguments": string(arguments),
					},
				}},
			}}},
		})
	}))
	defer server.Close()
	client, err := newOpenAIToolClient(server.URL+"/v1", "qwen-test", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := runChangeSimulationWithLanguage(
		context.Background(), client, snapshot, 512, 0, "fr",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 2 || requests != 2 ||
		!strings.Contains(result.Diff, "+  - orders-v2") {
		t.Fatalf("unexpected repaired result: %+v requests=%d", result, requests)
	}
	if len(result.RejectedAttempts) != 1 ||
		len(result.RejectedAttempts[0].ProposedChecks) != 1 ||
		result.RejectedAttempts[0].ProposedChecks[0] != "yamllint pillar/nftables.sls" {
		t.Fatalf("rejected attempt lost model-proposed checks: %+v", result.RejectedAttempts)
	}
}

func TestRunChangeSimulationRepairsUnsupportedNoChangeConclusion(t *testing.T) {
	snapshot := changeSimulationSnapshot{
		RequestPath: "<inline>", Request: "Add an endpoint that lists JA4 fingerprints.",
		Files: []changeSimulationFile{{Path: "main.go", Content: "package main\n\nfunc existing() {}\n"}},
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input openAIToolRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests++
		proposal := changeSimulationProposal{Analysis: "JA4 config already exists."}
		if requests == 2 {
			if input.Messages[1].Content == nil ||
				!strings.Contains(*input.Messages[1].Content, "route or handler") ||
				!strings.Contains(*input.Messages[1].Content, "specific question") {
				t.Fatalf("repair prompt is not actionable: %+v", input.Messages[1])
			}
			proposal = changeSimulationProposal{
				Analysis: "Expose existing aggregation through the handler.",
				Edits: []changeSimulationEdit{{
					Path: "main.go", Old: "func existing() {}", New: "func existing() {}\n\nfunc listJA4() []string { return nil }",
				}},
				Checks: []string{"go test ./..."}, Risks: []string{"API contract may need refinement."}, Rollback: "Revert handler change.",
			}
		}
		arguments, err := json.Marshal(proposal)
		if err != nil {
			t.Fatal(err)
		}
		writeJSON(writer, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "tool_calls": []any{map[string]any{
					"id": "call_change", "type": "function", "function": map[string]any{
						"name": changeSimulationProposalToolName, "arguments": string(arguments),
					},
				}},
			}}},
		})
	}))
	defer server.Close()
	client, err := newOpenAIToolClient(server.URL+"/v1", "qwen-test", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := runChangeSimulation(context.Background(), client, snapshot, 512, 0)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || result.Attempts != 2 || !strings.Contains(result.Diff, "+func listJA4") {
		t.Fatalf("unexpected repaired result: requests=%d result=%+v", requests, result)
	}
	if len(result.RejectedAttempts) != 1 || result.RejectedAttempts[0].Hint == "" {
		t.Fatalf("missing rejected-attempt hint: %+v", result.RejectedAttempts)
	}
}

func TestChangeSimulationRepairSeparatesEditsFromClarifications(t *testing.T) {
	errorText := "proposal with edits must not contain clarifications"
	repair := changeSimulationRepairInstruction(errorText)
	for _, expected := range []string{"clarifications must be an empty array", "Move explanatory text", "do not replace", "response type"} {
		if !strings.Contains(repair, expected) {
			t.Fatalf("repair instruction does not contain %q: %s", expected, repair)
		}
	}
	if hint := changeSimulationProposalHint(errorText); hint == "" || !strings.Contains(hint, "explanations") {
		t.Fatalf("missing actionable hint: %q", hint)
	}
}

func TestChangeSimulationRepairExpandsAmbiguousExactOld(t *testing.T) {
	errorText := `edits[0] old text must match "server.go" exactly once; matches=2; matching_start_lines=[73 87]`
	repair := changeSimulationRepairInstruction(errorText)
	for _, expected := range []string{"unique surrounding context", "route", "Do not change every match", "do not treat 0"} {
		if !strings.Contains(repair, expected) {
			t.Fatalf("repair instruction does not contain %q: %s", expected, repair)
		}
	}
	if hint := changeSimulationProposalHint(errorText); hint == "" || !strings.Contains(hint, "ambiguous") {
		t.Fatalf("missing ambiguous-old hint: %q", hint)
	}
}

func TestChangeSimulationRepairReplacesNoOpWithRealWireFormatChange(t *testing.T) {
	errorText := `proposal contains no effective changes after ignoring 1 no-op edits: ignored no-op edits[0] for "server.go" because old and new are identical`
	repair := changeSimulationRepairInstruction(errorText)
	for _, expected := range []string{"real change", "do not call a JSON helper", "Content-Type", "Do not modify other endpoints"} {
		if !strings.Contains(repair, expected) {
			t.Fatalf("repair instruction does not contain %q: %s", expected, repair)
		}
	}
	if hint := changeSimulationProposalHint(errorText); hint == "" || !strings.Contains(hint, "identical old and new") {
		t.Fatalf("missing all-no-op hint: %q", hint)
	}
}

func TestRunChangeSimulationReportsTruncatedToolArguments(t *testing.T) {
	snapshot := changeSimulationSnapshot{
		RequestPath: "REQUEST.md", Request: "Change config.",
		Files: []changeSimulationFile{{Path: "config.yaml", Content: "enabled: false\n"}},
	}
	arguments := `{"analysis":"` + strings.Repeat("partial", 800)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{
				"finish_reason": "length",
				"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
					"id": "truncated", "type": "function", "function": map[string]any{
						"name": changeSimulationProposalToolName, "arguments": arguments,
					},
				}}},
			}},
			"usage": map[string]any{"completion_tokens": 64},
		})
	}))
	defer server.Close()
	client, err := newOpenAIToolClient(server.URL+"/v1", "qwen-test", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = runChangeSimulation(context.Background(), client, snapshot, 64, 0)
	var failure *changeSimulationFailure
	if !errors.As(err, &failure) || len(failure.Diagnostics) != 2 {
		t.Fatalf("expected two attempt diagnostics, got error=%v failure=%+v", err, failure)
	}
	last := failure.Diagnostics[1]
	if last.Stage != "decode_arguments" || last.FinishReason != "length" ||
		last.CompletionTokens != 64 || last.ArgumentBytes != len(arguments) || last.Arguments != arguments ||
		!strings.Contains(last.ArgumentsPreview, "<arguments truncated by Akritas>") ||
		len(last.ArgumentsPreview) >= len(last.Arguments) || last.Hint == "" {
		t.Fatalf("unexpected truncation diagnostic: %+v", last)
	}
}

func mustResolveChangeSimulationRoot(t *testing.T, path string) string {
	t.Helper()
	root, err := resolveChangeSimulationRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	return root
}
