package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

func runBuildRAGIndex(arguments []string) {
	flags := flag.NewFlagSet("build-rag-index", flag.ExitOnError)
	manifest := flags.String("manifest", "", "source corpus manifest")
	output := flags.String("output", "", "binary RAG index output")
	chunkRunes := flags.Int("chunk-runes", 1200, "maximum runes per searchable chunk")
	overlapRunes := flags.Int("overlap-runes", 150, "overlap between adjacent chunks")
	maximumDocuments := flags.Int("max-documents", 0, "optional document limit; zero means all")
	verify := flags.Bool("verify", true, "verify corpus shard hashes")
	_ = flags.Parse(arguments)
	if strings.TrimSpace(*manifest) == "" || strings.TrimSpace(*output) == "" {
		panic("manifest and output are required")
	}
	index, err := BuildRAGIndex(*manifest, *chunkRunes, *overlapRunes, *maximumDocuments, *verify)
	if err != nil {
		panic(err)
	}
	if err := SaveRAGIndex(*output, index); err != nil {
		panic(err)
	}
	state, err := os.Stat(*output)
	if err != nil {
		panic(err)
	}
	fmt.Printf(
		"RAG index saved: path=%s chunks=%d terms=%d average_tokens=%.2f bytes=%d manifest_sha256=%s\n",
		*output, len(index.Chunks), len(index.Postings), index.AverageTokens,
		state.Size(), index.ManifestSHA256,
	)
}

func runSearchRAG(arguments []string) {
	flags := flag.NewFlagSet("search-rag", flag.ExitOnError)
	load := flags.String("load", "", "binary RAG index")
	query := flags.String("query", "", "search query")
	topK := flags.Int("top-k", 5, "maximum search results")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	_ = flags.Parse(arguments)
	if strings.TrimSpace(*load) == "" || strings.TrimSpace(*query) == "" {
		panic("load and query are required")
	}
	index, err := LoadRAGIndex(*load)
	if err != nil {
		panic(err)
	}
	results, err := index.Search(*query, *topK)
	if err != nil {
		panic(err)
	}
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(results); err != nil {
			panic(err)
		}
		return
	}
	for _, result := range results {
		fmt.Printf(
			"[%d] score=%.6f document=%q title=%q chunk=%d source=%q url=%q\n%s\n\n",
			result.Rank, result.Score, result.DocumentID, result.Title, result.Chunk,
			result.Source, result.URL, result.Text,
		)
	}
}
