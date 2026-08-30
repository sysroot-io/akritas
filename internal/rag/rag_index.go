package rag

import (
	"bufio"
	"encoding/gob"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"akritas/internal/corpus"
)

const (
	ragIndexVersion = 2
	ragIndexMagic   = "TGRAG002"
	ragTitleBoost   = 5
	ragTitleScore   = 3.0
)

const CurrentIndexVersion = ragIndexVersion

// RAGChunk is the independently retrievable unit stored in the local index.
// Source metadata is preserved so a generated answer can cite its origin.
type RAGChunk struct {
	DocumentID string
	Title      string
	Chunk      int
	Source     string
	URL        string
	Text       string
	Tokens     int
}

type RAGPosting struct {
	ChunkIndex int
	Frequency  int
}

// RAGIndex is independent from model weights. Updating factual knowledge
// requires rebuilding this file, not retraining the model.
type RAGIndex struct {
	Version        int
	ManifestPath   string
	ManifestSHA256 string
	ChunkRunes     int
	OverlapRunes   int
	AverageTokens  float64
	Chunks         []RAGChunk
	Postings       map[string][]RAGPosting
}

// ragDiskIndex replaces the runtime postings map with a sorted slice. Go map
// iteration order is deliberately random, so encoding the map directly would
// make two builds of the same corpus produce different binary files.
type ragDiskIndex struct {
	Version        int
	ManifestPath   string
	ManifestSHA256 string
	ChunkRunes     int
	OverlapRunes   int
	AverageTokens  float64
	Chunks         []RAGChunk
	Terms          []ragDiskTerm
}

type ragDiskTerm struct {
	Term     string
	Postings []RAGPosting
}

type RAGSearchResult struct {
	Rank       int     `json:"rank"`
	Score      float64 `json:"score"`
	DocumentID string  `json:"document_id"`
	Title      string  `json:"title,omitempty"`
	Chunk      int     `json:"chunk"`
	Source     string  `json:"source,omitempty"`
	URL        string  `json:"url,omitempty"`
	Text       string  `json:"text"`
}

var errRAGDocumentLimit = fmt.Errorf("RAG index: document limit reached")

func BuildRAGIndex(manifestPath string, chunkRunes, overlapRunes, maximumDocuments int, verify bool) (*RAGIndex, error) {
	if chunkRunes <= 0 || overlapRunes < 0 || overlapRunes >= chunkRunes {
		return nil, fmt.Errorf("RAG index: require chunk-runes > overlap-runes >= 0")
	}
	if maximumDocuments < 0 {
		return nil, fmt.Errorf("RAG index: maximum documents must not be negative")
	}
	corpusReader, err := corpus.OpenCorpus(manifestPath)
	if err != nil {
		return nil, err
	}
	absoluteManifest, err := filepath.Abs(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("RAG index: resolve manifest path: %w", err)
	}
	index := &RAGIndex{
		Version: ragIndexVersion, ManifestPath: absoluteManifest,
		ManifestSHA256: corpusReader.ManifestSHA256, ChunkRunes: chunkRunes,
		OverlapRunes: overlapRunes, Postings: make(map[string][]RAGPosting),
	}
	documents, totalTokens := 0, 0
	err = corpusReader.Iterate(corpusReader.StartCursor(), verify, func(document corpus.CorpusDocument, _ corpus.CorpusCursor) error {
		if maximumDocuments > 0 && documents >= maximumDocuments {
			return errRAGDocumentLimit
		}
		documents++
		title := strings.TrimSpace(document.Metadata["title"])
		for chunkNumber, text := range splitRAGText(document.Text, chunkRunes, overlapRunes) {
			terms := tokenizeRAGTerms(text)
			if len(terms) == 0 {
				continue
			}
			chunkIndex := len(index.Chunks)
			index.Chunks = append(index.Chunks, RAGChunk{
				DocumentID: document.ID, Title: title, Chunk: chunkNumber, Source: document.Source,
				URL: document.URL, Text: text, Tokens: len(terms),
			})
			totalTokens += len(terms)
			frequencies := make(map[string]int)
			for _, term := range terms {
				frequencies[term]++
			}
			// The first chunk represents the article as a whole. Boosting title
			// terms there makes a title-like query retrieve the definition rather
			// than a later list that happens to repeat the same words.
			if chunkNumber == 0 {
				for _, term := range tokenizeRAGTerms(title) {
					frequencies[term] += ragTitleBoost
				}
			}
			for term, frequency := range frequencies {
				index.Postings[term] = append(index.Postings[term], RAGPosting{ChunkIndex: chunkIndex, Frequency: frequency})
			}
		}
		return nil
	})
	if err != nil && err != errRAGDocumentLimit {
		return nil, err
	}
	if len(index.Chunks) == 0 {
		return nil, fmt.Errorf("RAG index: corpus produced no searchable chunks")
	}
	index.AverageTokens = float64(totalTokens) / float64(len(index.Chunks))
	return index, nil
}

func (index *RAGIndex) Search(query string, topK int) ([]RAGSearchResult, error) {
	if err := index.validate(); err != nil {
		return nil, err
	}
	if topK <= 0 {
		return nil, fmt.Errorf("RAG search: top-k must be positive")
	}
	queryTerms := tokenizeRAGTerms(query)
	if len(queryTerms) == 0 {
		return nil, fmt.Errorf("RAG search: query has no searchable terms")
	}
	queryFrequency := make(map[string]int)
	for _, term := range queryTerms {
		queryFrequency[term]++
	}
	scores := make(map[int]float64)
	idfByTerm := make(map[string]float64)
	documentCount := float64(len(index.Chunks))
	const k1, b = 1.2, 0.75
	for term, queryCount := range queryFrequency {
		postings := index.Postings[term]
		if len(postings) == 0 {
			continue
		}
		documentFrequency := float64(len(postings))
		idf := math.Log(1 + (documentCount-documentFrequency+0.5)/(documentFrequency+0.5))
		idfByTerm[term] = idf
		queryWeight := 1 + math.Log(float64(queryCount))
		for _, posting := range postings {
			chunk := index.Chunks[posting.ChunkIndex]
			frequency := float64(posting.Frequency)
			lengthRatio := float64(chunk.Tokens) / index.AverageTokens
			denominator := frequency + k1*(1-b+b*lengthRatio)
			scores[posting.ChunkIndex] += idf * queryWeight * frequency * (k1 + 1) / denominator
		}
	}
	for chunkIndex := range scores {
		chunk := index.Chunks[chunkIndex]
		if chunk.Chunk != 0 || chunk.Title == "" {
			continue
		}
		titleTerms := make(map[string]struct{})
		for _, term := range tokenizeRAGTerms(chunk.Title) {
			titleTerms[term] = struct{}{}
		}
		for term, queryCount := range queryFrequency {
			if _, matches := titleTerms[term]; matches {
				scores[chunkIndex] += ragTitleScore * idfByTerm[term] *
					(1 + math.Log(float64(queryCount)))
			}
		}
	}
	type scoredChunk struct {
		index int
		score float64
	}
	ranked := make([]scoredChunk, 0, len(scores))
	for chunkIndex, score := range scores {
		ranked = append(ranked, scoredChunk{index: chunkIndex, score: score})
	}
	sort.Slice(ranked, func(left, right int) bool {
		if ranked[left].score == ranked[right].score {
			return ranked[left].index < ranked[right].index
		}
		return ranked[left].score > ranked[right].score
	})
	results := make([]RAGSearchResult, 0, min(topK, len(ranked)))
	seenDocuments := make(map[string]struct{})
	for _, scored := range ranked {
		chunk := index.Chunks[scored.index]
		if _, exists := seenDocuments[chunk.DocumentID]; exists {
			continue
		}
		seenDocuments[chunk.DocumentID] = struct{}{}
		results = append(results, RAGSearchResult{
			Rank: len(results) + 1, Score: scored.score, DocumentID: chunk.DocumentID,
			Title: chunk.Title, Chunk: chunk.Chunk, Source: chunk.Source,
			URL: chunk.URL, Text: chunk.Text,
		})
		if len(results) == topK {
			break
		}
	}
	return results, nil
}

func SaveRAGIndex(path string, index *RAGIndex) error {
	if err := index.validate(); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create RAG index directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".rag-index-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary RAG index: %w", err)
	}
	// CreateTemp deliberately starts with 0600. RAG indexes are read-only
	// knowledge artifacts rather than secrets, and they are often built in a
	// Docker container before being consumed by a non-root WSL process.
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		os.Remove(temporary.Name())
		return fmt.Errorf("set RAG index permissions: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	writer := bufio.NewWriterSize(temporary, 1024*1024)
	if _, err := writer.WriteString(ragIndexMagic); err != nil {
		return fmt.Errorf("write RAG index header: %w", err)
	}
	terms := make([]string, 0, len(index.Postings))
	for term := range index.Postings {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	disk := ragDiskIndex{
		Version: index.Version, ManifestPath: index.ManifestPath,
		ManifestSHA256: index.ManifestSHA256, ChunkRunes: index.ChunkRunes,
		OverlapRunes: index.OverlapRunes, AverageTokens: index.AverageTokens,
		Chunks: index.Chunks, Terms: make([]ragDiskTerm, len(terms)),
	}
	for position, term := range terms {
		disk.Terms[position] = ragDiskTerm{Term: term, Postings: index.Postings[term]}
	}
	if err := gob.NewEncoder(writer).Encode(disk); err != nil {
		return fmt.Errorf("encode RAG index: %w", err)
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush RAG index: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync RAG index: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close RAG index: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install RAG index: %w", err)
	}
	keep = true
	return nil
}

func LoadRAGIndex(path string) (*RAGIndex, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open RAG index %q: %w", path, err)
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 1024*1024)
	header := make([]byte, len(ragIndexMagic))
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, fmt.Errorf("read RAG index header: %w", err)
	}
	if string(header) != ragIndexMagic {
		return nil, fmt.Errorf("RAG index %q has an unsupported header", path)
	}
	var disk ragDiskIndex
	if err := gob.NewDecoder(reader).Decode(&disk); err != nil {
		return nil, fmt.Errorf("decode RAG index %q: %w", path, err)
	}
	index := RAGIndex{
		Version: disk.Version, ManifestPath: disk.ManifestPath,
		ManifestSHA256: disk.ManifestSHA256, ChunkRunes: disk.ChunkRunes,
		OverlapRunes: disk.OverlapRunes, AverageTokens: disk.AverageTokens,
		Chunks: disk.Chunks, Postings: make(map[string][]RAGPosting, len(disk.Terms)),
	}
	previous := ""
	for position, term := range disk.Terms {
		if term.Term == "" || (position > 0 && term.Term <= previous) {
			return nil, fmt.Errorf("decode RAG index %q: terms are not strictly sorted", path)
		}
		index.Postings[term.Term] = term.Postings
		previous = term.Term
	}
	if err := index.validate(); err != nil {
		return nil, fmt.Errorf("decode RAG index %q: %w", path, err)
	}
	return &index, nil
}

func (index *RAGIndex) validate() error {
	if index == nil || index.Version != ragIndexVersion || index.ManifestSHA256 == "" ||
		index.ChunkRunes <= 0 || index.OverlapRunes < 0 || index.OverlapRunes >= index.ChunkRunes ||
		len(index.Chunks) == 0 || index.AverageTokens <= 0 || index.Postings == nil {
		return fmt.Errorf("RAG index: invalid or unsupported index")
	}
	for term, postings := range index.Postings {
		if strings.TrimSpace(term) == "" {
			return fmt.Errorf("RAG index: empty posting term")
		}
		for _, posting := range postings {
			if posting.ChunkIndex < 0 || posting.ChunkIndex >= len(index.Chunks) || posting.Frequency <= 0 {
				return fmt.Errorf("RAG index: invalid posting for %q", term)
			}
		}
	}
	return nil
}

func (index *RAGIndex) Validate() error { return index.validate() }

func tokenizeRAGTerms(text string) []string {
	terms := make([]string, 0, len(text)/6)
	var builder strings.Builder
	flush := func() {
		if builder.Len() > 0 {
			terms = append(terms, builder.String())
			builder.Reset()
		}
	}
	for _, value := range strings.ToLower(text) {
		if unicode.IsLetter(value) || unicode.IsDigit(value) || value == '_' {
			builder.WriteRune(value)
		} else {
			flush()
		}
	}
	flush()
	return terms
}

func TokenizeTerms(text string) []string { return tokenizeRAGTerms(text) }

func splitRAGText(text string, chunkRunes, overlapRunes int) []string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) == 0 {
		return nil
	}
	chunks := make([]string, 0, (len(runes)+chunkRunes-1)/chunkRunes)
	for start := 0; start < len(runes); {
		end := min(start+chunkRunes, len(runes))
		if end < len(runes) {
			originalEnd := end
			minimumEnd := start + chunkRunes/2
			for end > minimumEnd && !unicode.IsSpace(runes[end-1]) {
				end--
			}
			if end == minimumEnd {
				end = originalEnd
			}
		}
		if chunk := strings.TrimSpace(string(runes[start:end])); chunk != "" {
			chunks = append(chunks, chunk)
		}
		if end == len(runes) {
			break
		}
		next := end - overlapRunes
		if next <= start {
			next = end
		}
		start = next
	}
	return chunks
}
