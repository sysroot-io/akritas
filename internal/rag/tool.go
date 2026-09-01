package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"akritas/internal/mcp"
)

const localRAGSearchToolName = "local.rag.search"

const SearchToolName = localRAGSearchToolName

type RAGSearchToolOptions struct {
	DefaultTopK  int
	MaximumTopK  int
	MaximumRunes int
	Timeout      time.Duration
}

type ragSearchToolArguments struct {
	Query string `json:"query"`
	TopK  int    `json:"top_k,omitempty"`
}

type RAGToolSearchResult struct {
	Rank       int    `json:"rank"`
	DocumentID string `json:"document_id"`
	Title      string `json:"title,omitempty"`
	Text       string `json:"text"`
}

type ragSearchToolOutput struct {
	Query   string                `json:"query"`
	Results []RAGToolSearchResult `json:"results"`
}

func (options RAGSearchToolOptions) normalized() (RAGSearchToolOptions, error) {
	if options.DefaultTopK == 0 {
		options.DefaultTopK = 3
	}
	if options.MaximumTopK == 0 {
		options.MaximumTopK = 5
	}
	if options.MaximumRunes == 0 {
		options.MaximumRunes = 1200
	}
	if options.Timeout == 0 {
		options.Timeout = mcp.DefaultToolTimeout
	}
	if options.DefaultTopK <= 0 || options.MaximumTopK <= 0 ||
		options.DefaultTopK > options.MaximumTopK || options.MaximumRunes <= 0 ||
		options.Timeout <= 0 {
		return RAGSearchToolOptions{}, fmt.Errorf(
			"RAG tool requires 0 < default_top_k <= maximum_top_k, positive maximum_runes and timeout",
		)
	}
	return options, nil
}

// RegisterRAGSearchTool exposes one bounded, read-only view of a local index.
// The handler has no filesystem path argument, so a model cannot switch the
// index or read arbitrary files through crafted tool arguments.
func RegisterRAGSearchTool(
	registry *mcp.ToolRegistry,
	index *RAGIndex,
	options RAGSearchToolOptions,
) error {
	if registry == nil {
		return fmt.Errorf("register RAG tool: registry is nil")
	}
	if err := index.validate(); err != nil {
		return fmt.Errorf("register RAG tool: %w", err)
	}
	options, err := options.normalized()
	if err != nil {
		return fmt.Errorf("register RAG tool: %w", err)
	}
	schema := fmt.Sprintf(
		`{"type":"object","properties":{"query":{"type":"string"},"top_k":{"type":"integer","minimum":1,"maximum":%d}},"required":["query"],"additionalProperties":false}`,
		options.MaximumTopK,
	)
	decodeArguments := func(raw json.RawMessage) (ragSearchToolArguments, error) {
		var arguments ragSearchToolArguments
		if err := mcp.DecodeStrictJSONObject(raw, &arguments); err != nil {
			return ragSearchToolArguments{}, err
		}
		arguments.Query = strings.TrimSpace(arguments.Query)
		if len(tokenizeRAGTerms(arguments.Query)) == 0 {
			return ragSearchToolArguments{}, fmt.Errorf(
				"query must contain searchable letters or digits",
			)
		}
		if arguments.TopK == 0 {
			arguments.TopK = options.DefaultTopK
		}
		if arguments.TopK < 1 || arguments.TopK > options.MaximumTopK {
			return ragSearchToolArguments{}, fmt.Errorf(
				"top_k must be between 1 and %d",
				options.MaximumTopK,
			)
		}
		return arguments, nil
	}
	return registry.Register(mcp.ToolDefinition{
		Name:        localRAGSearchToolName,
		Description: "Searches the configured local knowledge index and returns bounded source excerpts with document IDs and URLs.",
		InputSchema: json.RawMessage(schema),
		Permission:  mcp.ToolPermissionRead,
		Timeout:     options.Timeout,
		ValidateArguments: func(raw json.RawMessage) error {
			_, err := decodeArguments(raw)
			return err
		},
		Handler: func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
			arguments, err := decodeArguments(raw)
			if err != nil {
				return nil, err
			}
			results, err := index.Search(arguments.Query, arguments.TopK)
			if err != nil {
				return nil, err
			}
			compact := make([]RAGToolSearchResult, len(results))
			for resultIndex, result := range results {
				compact[resultIndex] = RAGToolSearchResult{
					Rank: result.Rank, DocumentID: result.DocumentID,
					Title: result.Title,
					Text: compactRAGToolText(
						result.Text,
						arguments.Query,
						options.MaximumRunes,
					),
				}
			}
			return json.Marshal(ragSearchToolOutput{
				Query:   arguments.Query,
				Results: compact,
			})
		},
	})
}

func compactRAGToolText(text, query string, maximumRunes int) string {
	text = strings.TrimSpace(text)
	if text == "" || maximumRunes <= 0 {
		return ""
	}
	textRunes := []rune(text)
	if len(textRunes) <= maximumRunes {
		return text
	}
	queryTerms := make(map[string]struct{})
	for _, term := range tokenizeRAGTerms(query) {
		queryTerms[term] = struct{}{}
	}
	bestStart, bestEnd, bestScore := 0, 0, -1
	segmentStart := 0
	for position := 0; position <= len(textRunes); position++ {
		if position < len(textRunes) && !isRAGContextBoundary(textRunes[position]) {
			continue
		}
		segment := strings.TrimSpace(string(textRunes[segmentStart:position]))
		score := 0
		seen := make(map[string]struct{})
		for _, term := range tokenizeRAGTerms(segment) {
			if _, relevant := queryTerms[term]; !relevant {
				continue
			}
			if _, duplicate := seen[term]; !duplicate {
				score++
				seen[term] = struct{}{}
			}
		}
		if score > bestScore {
			bestStart, bestEnd, bestScore = segmentStart, position, score
		}
		segmentStart = position + 1
	}

	// Keep the matching passage inside a larger source window instead of
	// returning one isolated sentence. In particular, a query matching a
	// Markdown heading must retain the checklist that follows that heading.
	start := bestStart - maximumRunes/4
	if start < 0 {
		start = 0
	}
	if bestEnd > start+maximumRunes {
		start = bestEnd - maximumRunes
	}
	if start+maximumRunes > len(textRunes) {
		start = len(textRunes) - maximumRunes
	}
	return strings.TrimSpace(string(textRunes[start : start+maximumRunes]))
}

func isRAGContextBoundary(value rune) bool {
	switch value {
	case '.', '!', '?', '\n', '\r':
		return true
	default:
		return false
	}
}

func RAGSearchToolCatalogPrompt() string {
	return `Tool: local.rag.search {"query":string,"top_k"?:integer} - read-only search of the local knowledge base. To search, first output a TOOL_CALL with JSON. The host adds TOOL_RESULT. Answer only from its sources and cite them as [1]; if the sources do not support an answer, say that no answer is available.`
}
