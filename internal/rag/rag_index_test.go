package rag

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"akritas/internal/corpus"
)

func TestRAGIndexRoundTripAndSearch(t *testing.T) {
	directory := t.TempDir()
	corpusDirectory := filepath.Join(directory, "corpus")
	writer, err := corpus.NewCorpusWriter(corpusDirectory, 1024, false)
	if err != nil {
		t.Fatal(err)
	}
	documents := []corpus.CorpusDocument{
		{ID: "go", Source: "docs", URL: "https://go.dev", Metadata: map[string]string{"title": "Go"}, Text: "Go - компилируемый язык со статической типизацией. Его разработали в Google."},
		{ID: "moscow", Source: "wiki", URL: "https://ru.wikipedia.org/wiki/Москва", Metadata: map[string]string{"title": "Москва"}, Text: "Москва - столица России и крупный город."},
		{ID: "python", Source: "docs", Text: "Python - интерпретируемый язык программирования."},
	}
	for _, document := range documents {
		if err := writer.Add(document); err != nil {
			t.Fatal(err)
		}
	}
	manifestPath, _, err := writer.Close()
	if err != nil {
		t.Fatal(err)
	}
	index, err := BuildRAGIndex(manifestPath, 80, 10, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "knowledge.tgr")
	if err := SaveRAGIndex(path, index); err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(directory, "knowledge-second.tgr")
	if err := SaveRAGIndex(secondPath, index); err != nil {
		t.Fatal(err)
	}
	firstBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("RAG binary encoding is not deterministic")
	}
	state, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && state.Mode().Perm() != 0o644 {
		t.Fatalf("RAG index permissions=%#o, want 0644", state.Mode().Perm())
	}
	loaded, err := LoadRAGIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	results, err := loaded.Search("кто разработал язык Go", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].DocumentID != "go" {
		t.Fatalf("unexpected RAG ranking: %+v", results)
	}
	if results[0].Title != "Go" || results[0].URL != "https://go.dev" || results[0].Score <= 0 {
		t.Fatalf("missing RAG provenance or score: %+v", results[0])
	}
	seen := make(map[string]struct{})
	for _, result := range results {
		if _, duplicate := seen[result.DocumentID]; duplicate {
			t.Fatalf("duplicate RAG document in top-k: %+v", results)
		}
		seen[result.DocumentID] = struct{}{}
	}
}

func TestSplitRAGTextPreservesUnicodeAndOverlap(t *testing.T) {
	chunks := splitRAGText("один два три четыре пять шесть", 15, 4)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %q", chunks)
	}
	for _, chunk := range chunks {
		if chunk == "" {
			t.Fatal("empty RAG chunk")
		}
	}
}
