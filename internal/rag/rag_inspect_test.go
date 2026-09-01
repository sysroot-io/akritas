package rag

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectRAGIndex(t *testing.T) {
	index := testRAGIndex(t)
	index.Chunks = append(index.Chunks, RAGChunk{
		DocumentID: "go", Title: "Go", Chunk: 1, Source: "test",
		Text: "The second part of the document.", Tokens: 4,
	})
	path := filepath.Join(t.TempDir(), "index.tgr")
	if err := SaveRAGIndex(path, index); err != nil {
		t.Fatal(err)
	}

	inspection, err := InspectRAGIndex(path, index)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Documents != 2 || inspection.Chunks != 3 ||
		len(inspection.Sources) != 1 || inspection.Sources[0].Documents != 2 ||
		inspection.Sources[0].Chunks != 3 || inspection.FileBytes <= 0 {
		t.Fatalf("unexpected inspection: %+v", inspection)
	}
}

func TestListRAGDocumentsFiltersPaginatesAndPreviews(t *testing.T) {
	index := testRAGIndex(t)
	result, err := ListRAGDocuments(index, RAGDocumentListOptions{
		TitleContains: "go", Limit: 10, PreviewRunes: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched != 1 || len(result.Documents) != 1 ||
		result.Documents[0].DocumentID != "go" ||
		len([]rune(strings.TrimSuffix(result.Documents[0].Preview, "…"))) > 8 {
		t.Fatalf("unexpected filtered result: %+v", result)
	}

	page, err := ListRAGDocuments(index, RAGDocumentListOptions{Offset: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Matched != 2 || len(page.Documents) != 1 || page.Documents[0].Title != "Python" {
		t.Fatalf("unexpected page: %+v", page)
	}
}

func TestListRAGDocumentsRejectsInvalidBounds(t *testing.T) {
	_, err := ListRAGDocuments(testRAGIndex(t), RAGDocumentListOptions{Limit: 0})
	if err == nil {
		t.Fatal("expected invalid limit error")
	}
}
