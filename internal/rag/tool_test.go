package rag

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"akritas/internal/mcp"
)

func TestRAGSearchToolIsReadOnlyBoundedAndStrict(t *testing.T) {
	index := testRAGIndex(t)
	registry := mcp.NewToolRegistry()
	if err := RegisterRAGSearchTool(registry, index, RAGSearchToolOptions{
		DefaultTopK: 1, MaximumTopK: 2, MaximumRunes: 20,
	}); err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 1 || definitions[0].Permission != mcp.ToolPermissionRead {
		t.Fatalf("unexpected definitions: %+v", definitions)
	}

	call := mcp.ToolCall{
		ID: "call-1", Name: localRAGSearchToolName,
		Arguments: json.RawMessage(`{"query":"who developed Go"}`),
	}
	denied := registry.Execute(context.Background(), call, mcp.StaticToolPolicy{
		Allowed: map[mcp.ToolPermission]bool{},
	})
	if denied.Error == nil || denied.Error.Code != "permission_denied" {
		t.Fatalf("read permission was not enforced: %+v", denied)
	}
	result := registry.Execute(context.Background(), call, mcp.StaticToolPolicy{
		Allowed: map[mcp.ToolPermission]bool{mcp.ToolPermissionRead: true},
	})
	if result.Error != nil {
		t.Fatalf("RAG tool failed: %+v", result.Error)
	}
	var output ragSearchToolOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Results) != 1 || output.Results[0].DocumentID != "go" ||
		len([]rune(output.Results[0].Text)) > 20 {
		t.Fatalf("unexpected bounded output: %+v", output)
	}

	call.Arguments = json.RawMessage(`{"query":"Go","top_k":3}`)
	invalid := registry.Execute(context.Background(), call, mcp.StaticToolPolicy{
		Allowed: map[mcp.ToolPermission]bool{mcp.ToolPermissionRead: true},
	})
	if invalid.Error == nil || invalid.Error.Code != "invalid_arguments" ||
		!strings.Contains(invalid.Error.Message, "top_k") {
		t.Fatalf("invalid top_k was accepted: %+v", invalid)
	}
}

func TestCompactRAGToolTextKeepsContextAroundQueryBearingSentence(t *testing.T) {
	text := "An introductory sentence without numbers. The river is 473 km long and flows into the Oka. Conclusion."
	compact := compactRAGToolText(text, "how long is the river and where does it flow", 100)
	if !strings.Contains(compact, "473") || !strings.Contains(compact, "Oka") {
		t.Fatalf("query-bearing context was not selected: %q", compact)
	}
}

func TestCompactRAGToolTextKeepsShortRunbookWhole(t *testing.T) {
	text := `# Runbook: high CPU usage

## Checks

1. Check CPU over the last 30 minutes.
2. Compare load across pods.
3. Check throttling.
4. Review errors and latency.
5. Check recent deployments.

## Possible causes

- increased incoming traffic;
- an infinite loop.`
	compact := compactRAGToolText(text, "high cpu", 800)
	if compact != text {
		t.Fatalf("short runbook was compacted:\n%s", compact)
	}
	if !strings.Contains(compact, "5. Check recent deployments.") {
		t.Fatalf("runbook checklist is missing: %q", compact)
	}
}

func TestCompactRAGToolTextBoundsLongRunbookWithFollowingContext(t *testing.T) {
	text := "Introduction without matches.\n\n# High CPU\n\n## Checks\n\n1. Check CPU.\n2. Check throttling.\n" + strings.Repeat("Additional context. ", 30)
	compact := compactRAGToolText(text, "high cpu", 120)
	if len([]rune(compact)) > 120 {
		t.Fatalf("context has %d runes, limit 120", len([]rune(compact)))
	}
	if !strings.Contains(compact, "## Checks") || !strings.Contains(compact, "1. Check CPU") {
		t.Fatalf("context following the matching heading is missing: %q", compact)
	}
}

func testRAGIndex(t *testing.T) *RAGIndex {
	t.Helper()
	chunks := []RAGChunk{
		{DocumentID: "go", Title: "Go", Chunk: 0, Source: "test", URL: "https://go.dev", Text: "Go was developed at Google by Robert Griesemer, Rob Pike, and Ken Thompson.", Tokens: 10},
		{DocumentID: "python", Title: "Python", Chunk: 0, Source: "test", Text: "Python was created by Guido van Rossum.", Tokens: 6},
	}
	index := &RAGIndex{
		Version: ragIndexVersion, ManifestSHA256: strings.Repeat("a", 64),
		ChunkRunes: 100, OverlapRunes: 10, AverageTokens: 8,
		Chunks: chunks, Postings: make(map[string][]RAGPosting),
	}
	for chunkIndex, chunk := range chunks {
		frequencies := make(map[string]int)
		for _, term := range tokenizeRAGTerms(chunk.Title + " " + chunk.Text) {
			frequencies[term]++
		}
		for term, frequency := range frequencies {
			index.Postings[term] = append(index.Postings[term], RAGPosting{
				ChunkIndex: chunkIndex, Frequency: frequency,
			})
		}
	}
	if err := index.validate(); err != nil {
		t.Fatal(err)
	}
	return index
}
