package rag

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type RAGSourceInspection struct {
	Source    string `json:"source"`
	Documents int    `json:"documents"`
	Chunks    int    `json:"chunks"`
}

type RAGIndexInspection struct {
	Path              string                `json:"path"`
	FileBytes         int64                 `json:"file_bytes"`
	Version           int                   `json:"version"`
	ManifestPath      string                `json:"manifest_path"`
	ManifestSHA256    string                `json:"manifest_sha256"`
	ChunkRunes        int                   `json:"chunk_runes"`
	OverlapRunes      int                   `json:"overlap_runes"`
	Documents         int                   `json:"documents"`
	Chunks            int                   `json:"chunks"`
	Terms             int                   `json:"terms"`
	AverageTokens     float64               `json:"average_tokens"`
	UntitledDocuments int                   `json:"untitled_documents"`
	Sources           []RAGSourceInspection `json:"sources"`
}

type RAGDocumentSummary struct {
	DocumentID   string `json:"document_id"`
	Title        string `json:"title,omitempty"`
	Source       string `json:"source,omitempty"`
	URL          string `json:"url,omitempty"`
	Chunks       int    `json:"chunks"`
	Tokens       int    `json:"tokens"`
	IndexedRunes int    `json:"indexed_runes"`
	Preview      string `json:"preview,omitempty"`
}

type RAGDocumentListOptions struct {
	IDContains     string
	TitleContains  string
	SourceContains string
	Offset         int
	Limit          int
	PreviewRunes   int
}

type RAGDocumentListResult struct {
	Matched   int                  `json:"matched"`
	Offset    int                  `json:"offset"`
	Limit     int                  `json:"limit"`
	Documents []RAGDocumentSummary `json:"documents"`
}

func InspectRAGIndex(path string, index *RAGIndex) (RAGIndexInspection, error) {
	if index == nil {
		return RAGIndexInspection{}, fmt.Errorf("inspect RAG index: index is nil")
	}
	if err := index.validate(); err != nil {
		return RAGIndexInspection{}, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return RAGIndexInspection{}, fmt.Errorf("inspect RAG index path: %w", err)
	}
	state, err := os.Stat(path)
	if err != nil {
		return RAGIndexInspection{}, fmt.Errorf("inspect RAG index file: %w", err)
	}
	type sourceAccumulator struct {
		chunks    int
		documents map[string]bool
	}
	sources := make(map[string]*sourceAccumulator)
	documents := make(map[string]bool)
	titles := make(map[string]string)
	for _, chunk := range index.Chunks {
		documentKey := chunk.Source + "\x00" + chunk.DocumentID
		documents[documentKey] = true
		if _, exists := titles[documentKey]; !exists {
			titles[documentKey] = chunk.Title
		}
		accumulator := sources[chunk.Source]
		if accumulator == nil {
			accumulator = &sourceAccumulator{documents: make(map[string]bool)}
			sources[chunk.Source] = accumulator
		}
		accumulator.chunks++
		accumulator.documents[documentKey] = true
	}
	sourceNames := make([]string, 0, len(sources))
	for source := range sources {
		sourceNames = append(sourceNames, source)
	}
	sort.Strings(sourceNames)
	inspection := RAGIndexInspection{
		Path: absolute, FileBytes: state.Size(), Version: index.Version,
		ManifestPath: index.ManifestPath, ManifestSHA256: index.ManifestSHA256,
		ChunkRunes: index.ChunkRunes, OverlapRunes: index.OverlapRunes,
		Documents: len(documents), Chunks: len(index.Chunks),
		Terms: len(index.Postings), AverageTokens: index.AverageTokens,
	}
	for _, title := range titles {
		if strings.TrimSpace(title) == "" {
			inspection.UntitledDocuments++
		}
	}
	for _, source := range sourceNames {
		accumulator := sources[source]
		inspection.Sources = append(inspection.Sources, RAGSourceInspection{
			Source: source, Documents: len(accumulator.documents),
			Chunks: accumulator.chunks,
		})
	}
	return inspection, nil
}

func ListRAGDocuments(index *RAGIndex, options RAGDocumentListOptions) (RAGDocumentListResult, error) {
	if index == nil {
		return RAGDocumentListResult{}, fmt.Errorf("list RAG documents: index is nil")
	}
	if err := index.validate(); err != nil {
		return RAGDocumentListResult{}, err
	}
	if options.Offset < 0 || options.Limit <= 0 || options.Limit > 10000 || options.PreviewRunes < 0 {
		return RAGDocumentListResult{}, fmt.Errorf(
			"list RAG documents requires offset >= 0, 1 <= limit <= 10000 and preview-runes >= 0",
		)
	}
	documents := make(map[string]*RAGDocumentSummary)
	for _, chunk := range index.Chunks {
		key := chunk.Source + "\x00" + chunk.DocumentID
		document := documents[key]
		if document == nil {
			document = &RAGDocumentSummary{
				DocumentID: chunk.DocumentID, Title: chunk.Title,
				Source: chunk.Source, URL: chunk.URL,
			}
			documents[key] = document
		}
		document.Chunks++
		document.Tokens += chunk.Tokens
		document.IndexedRunes += len([]rune(chunk.Text))
		if options.PreviewRunes > 0 && document.Preview == "" {
			document.Preview = truncateRAGPreview(chunk.Text, options.PreviewRunes)
		}
	}
	filtered := make([]RAGDocumentSummary, 0, len(documents))
	for _, document := range documents {
		if !containsFold(document.DocumentID, options.IDContains) ||
			!containsFold(document.Title, options.TitleContains) ||
			!containsFold(document.Source, options.SourceContains) {
			continue
		}
		filtered = append(filtered, *document)
	}
	sort.Slice(filtered, func(left, right int) bool {
		leftTitle := strings.ToLower(filtered[left].Title)
		rightTitle := strings.ToLower(filtered[right].Title)
		if leftTitle == rightTitle {
			if filtered[left].Source == filtered[right].Source {
				return filtered[left].DocumentID < filtered[right].DocumentID
			}
			return filtered[left].Source < filtered[right].Source
		}
		return leftTitle < rightTitle
	})
	result := RAGDocumentListResult{
		Matched: len(filtered), Offset: options.Offset, Limit: options.Limit,
	}
	if options.Offset >= len(filtered) {
		return result, nil
	}
	end := min(options.Offset+options.Limit, len(filtered))
	result.Documents = append(result.Documents, filtered[options.Offset:end]...)
	return result, nil
}

func containsFold(value, filter string) bool {
	filter = strings.TrimSpace(filter)
	return filter == "" || strings.Contains(strings.ToLower(value), strings.ToLower(filter))
}

func truncateRAGPreview(text string, maximumRunes int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= maximumRunes {
		return text
	}
	return strings.TrimSpace(string(runes[:maximumRunes])) + "…"
}
