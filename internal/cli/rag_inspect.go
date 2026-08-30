package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

func runInspectRAGIndex(arguments []string) {
	flags := flag.NewFlagSet("inspect-rag-index", flag.ExitOnError)
	load := flags.String("load", "", "binary RAG index")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	_ = flags.Parse(arguments)
	if strings.TrimSpace(*load) == "" {
		panic("inspect-rag-index requires -load")
	}
	index, err := LoadRAGIndex(*load)
	if err != nil {
		panic(err)
	}
	inspection, err := InspectRAGIndex(*load, index)
	if err != nil {
		panic(err)
	}
	if *jsonOutput {
		writeIndentedJSON(inspection)
		return
	}
	fmt.Printf("path=%q\n", inspection.Path)
	fmt.Printf("file_bytes=%d version=%d\n", inspection.FileBytes, inspection.Version)
	fmt.Printf("manifest=%q\n", inspection.ManifestPath)
	fmt.Printf("manifest_sha256=%s\n", inspection.ManifestSHA256)
	fmt.Printf(
		"documents=%d chunks=%d terms=%d average_tokens=%.2f untitled_documents=%d\n",
		inspection.Documents, inspection.Chunks, inspection.Terms,
		inspection.AverageTokens, inspection.UntitledDocuments,
	)
	fmt.Printf("chunk_runes=%d overlap_runes=%d\n", inspection.ChunkRunes, inspection.OverlapRunes)
	for _, source := range inspection.Sources {
		fmt.Printf("source=%q documents=%d chunks=%d\n", source.Source, source.Documents, source.Chunks)
	}
}

func runListRAGDocuments(arguments []string) {
	flags := flag.NewFlagSet("list-rag-documents", flag.ExitOnError)
	load := flags.String("load", "", "binary RAG index")
	id := flags.String("id", "", "case-insensitive document ID substring")
	title := flags.String("title", "", "case-insensitive title substring")
	source := flags.String("source", "", "case-insensitive source substring")
	offset := flags.Int("offset", 0, "number of matching documents to skip")
	limit := flags.Int("limit", 50, "maximum documents to print, up to 10000")
	previewRunes := flags.Int("preview-runes", 0, "optional first-chunk preview length")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	_ = flags.Parse(arguments)
	if strings.TrimSpace(*load) == "" {
		panic("list-rag-documents requires -load")
	}
	index, err := LoadRAGIndex(*load)
	if err != nil {
		panic(err)
	}
	result, err := ListRAGDocuments(index, RAGDocumentListOptions{
		IDContains: *id, TitleContains: *title, SourceContains: *source,
		Offset: *offset, Limit: *limit, PreviewRunes: *previewRunes,
	})
	if err != nil {
		panic(err)
	}
	if *jsonOutput {
		writeIndentedJSON(result)
		return
	}
	fmt.Printf(
		"RAG documents: matched=%d offset=%d shown=%d limit=%d\n",
		result.Matched, result.Offset, len(result.Documents), result.Limit,
	)
	for index, document := range result.Documents {
		fmt.Printf(
			"[%d] document=%q title=%q source=%q chunks=%d tokens=%d indexed_runes=%d url=%q\n",
			result.Offset+index+1, document.DocumentID, document.Title,
			document.Source, document.Chunks, document.Tokens,
			document.IndexedRunes, document.URL,
		)
		if document.Preview != "" {
			fmt.Printf("%s\n", document.Preview)
		}
	}
}

func writeIndentedJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		panic(err)
	}
}
