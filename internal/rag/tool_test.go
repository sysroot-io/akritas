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
		Arguments: json.RawMessage(`{"query":"кто разработал Go"}`),
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
	text := "Первая вводная фраза без чисел. Длина реки составляет 473 км, и она впадает в Оку. Заключение."
	compact := compactRAGToolText(text, "какова длина и куда впадает река", 100)
	if !strings.Contains(compact, "473") || !strings.Contains(compact, "Оку") {
		t.Fatalf("query-bearing context was not selected: %q", compact)
	}
}

func TestCompactRAGToolTextKeepsShortRunbookWhole(t *testing.T) {
	text := `# Runbook: высокая загрузка CPU

## Проверки

1. Проверить CPU за последние 30 минут.
2. Сравнить нагрузку между pod.
3. Проверить throttling.
4. Посмотреть ошибки и latency.
5. Проверить последние deployments.

## Возможные причины

- рост входящего трафика;
- бесконечный цикл.`
	compact := compactRAGToolText(text, "high cpu", 800)
	if compact != text {
		t.Fatalf("short runbook was compacted:\n%s", compact)
	}
	if !strings.Contains(compact, "5. Проверить последние deployments.") {
		t.Fatalf("runbook checklist is missing: %q", compact)
	}
}

func TestCompactRAGToolTextBoundsLongRunbookWithFollowingContext(t *testing.T) {
	text := "Введение без совпадений.\n\n# High CPU\n\n## Проверки\n\n1. Проверить CPU.\n2. Проверить throttling.\n" + strings.Repeat("Дополнение. ", 30)
	compact := compactRAGToolText(text, "high cpu", 120)
	if len([]rune(compact)) > 120 {
		t.Fatalf("context has %d runes, limit 120", len([]rune(compact)))
	}
	if !strings.Contains(compact, "## Проверки") || !strings.Contains(compact, "1. Проверить CPU") {
		t.Fatalf("context following the matching heading is missing: %q", compact)
	}
}

func testRAGIndex(t *testing.T) *RAGIndex {
	t.Helper()
	chunks := []RAGChunk{
		{DocumentID: "go", Title: "Go", Chunk: 0, Source: "test", URL: "https://go.dev", Text: "Go разработали в Google Роберт Гризмер, Роб Пайк и Кен Томпсон.", Tokens: 10},
		{DocumentID: "python", Title: "Python", Chunk: 0, Source: "test", Text: "Python создал Гвидо ван Россум.", Tokens: 6},
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
