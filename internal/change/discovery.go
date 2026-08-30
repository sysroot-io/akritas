package change

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	changeDiscoveryToolName          = "akritas_plan_repository_search"
	maximumChangeDiscoveryQueries    = 8
	maximumChangeDiscoveryFiles      = 10000
	maximumChangeDiscoveryScanBytes  = 64 * 1024 * 1024
	maximumChangeDiscoveryCandidates = 16
)

const changeDiscoverySchema = `{"type":"object","properties":{"queries":{"type":"array","minItems":1,"maxItems":8,"items":{"type":"string"}}},"required":["queries"],"additionalProperties":false}`

var changeDiscoveryExcludedDirectories = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true,
	"vendor": true, "dist": true, "build": true, ".cache": true,
}

type changeDiscoveryPlan struct {
	Queries []string `json:"queries"`
}

type changeDiscoveryDocument struct {
	Path    string
	Content string
	Score   float64
}

func discoverChangeSimulationSnapshot(
	ctx context.Context,
	client *openAIToolClient,
	rootPath string,
	requestPath string,
	request string,
	maxTokens int,
	temperature float64,
) (changeSimulationSnapshot, error) {
	if client == nil {
		return changeSimulationSnapshot{}, fmt.Errorf("change discovery requires a model client")
	}
	root, err := resolveChangeSimulationRoot(rootPath)
	if err != nil {
		return changeSimulationSnapshot{}, err
	}
	plan, err := requestChangeDiscoveryPlan(ctx, client, request, maxTokens, temperature)
	if err != nil {
		return changeSimulationSnapshot{}, err
	}
	documents, err := scanChangeDiscoveryWorkspace(root)
	if err != nil {
		return changeSimulationSnapshot{}, err
	}
	ranked := rankChangeDiscoveryDocuments(documents, append([]string{request}, plan.Queries...))
	if len(ranked) == 0 {
		return changeSimulationSnapshot{}, fmt.Errorf(
			"automatic discovery found no matching UTF-8 files; provide files manually",
		)
	}
	paths := make([]string, 0, maximumChangeDiscoveryCandidates)
	totalBytes := len(request)
	for _, document := range ranked {
		if len(paths) == maximumChangeDiscoveryCandidates {
			break
		}
		if totalBytes+len(document.Content) > maximumChangeSimulationTotalBytes {
			continue
		}
		paths = append(paths, document.Path)
		totalBytes += len(document.Content)
	}
	if len(paths) == 0 {
		return changeSimulationSnapshot{}, fmt.Errorf(
			"automatic discovery candidates exceed the %d-byte snapshot limit; provide files manually",
			maximumChangeSimulationTotalBytes,
		)
	}
	return loadChangeSimulationSnapshotContent(root, requestPath, request, paths)
}

func requestChangeDiscoveryPlan(
	ctx context.Context,
	client *openAIToolClient,
	request string,
	maxTokens int,
	temperature float64,
) (changeDiscoveryPlan, error) {
	tool := openAIToolSpec{Type: "function", Function: openAIToolFunction{
		Name:        changeDiscoveryToolName,
		Description: "Returns concise repository search queries for locating source-of-truth files, inventories, documentation and generated-file ownership.",
		Parameters:  json.RawMessage(changeDiscoverySchema),
	}}
	system := `Ты планировщик поиска по инфраструктурному репозиторию. По запросу пользователя сформируй 2–8 коротких lexical queries: имена сущностей, ключи конфигурации, вероятные технологии и слова для поиска source of truth. Не придумывай пути. Обязательно вызови единственную функцию ровно один раз.`
	user := strings.TrimSpace(request)
	planningTokens := maxTokens
	if planningTokens > 512 {
		planningTokens = 512
	}
	message, err := client.CompleteWithToolChoice(
		ctx,
		[]openAIToolMessage{{Role: "system", Content: &system}, {Role: "user", Content: &user}},
		[]openAIToolSpec{tool}, "required", planningTokens, temperature,
	)
	if err != nil {
		return changeDiscoveryPlan{}, fmt.Errorf("plan repository discovery: %w", err)
	}
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Name != changeDiscoveryToolName {
		return changeDiscoveryPlan{}, fmt.Errorf("repository discovery requires exactly one %s call", changeDiscoveryToolName)
	}
	var plan changeDiscoveryPlan
	if err := decodeStrictJSONObject(json.RawMessage(message.ToolCalls[0].Function.Arguments), &plan); err != nil {
		return changeDiscoveryPlan{}, fmt.Errorf("decode repository discovery plan: %w", err)
	}
	if len(plan.Queries) == 0 || len(plan.Queries) > maximumChangeDiscoveryQueries {
		return changeDiscoveryPlan{}, fmt.Errorf("repository discovery requires 1..%d queries", maximumChangeDiscoveryQueries)
	}
	seen := make(map[string]bool, len(plan.Queries))
	for index := range plan.Queries {
		plan.Queries[index] = strings.TrimSpace(plan.Queries[index])
		key := strings.ToLower(plan.Queries[index])
		if key == "" || seen[key] {
			return changeDiscoveryPlan{}, fmt.Errorf("repository discovery query %d is empty or duplicate", index)
		}
		seen[key] = true
	}
	return plan, nil
}

func scanChangeDiscoveryWorkspace(root string) ([]changeDiscoveryDocument, error) {
	workspaceRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open change workspace: %w", err)
	}
	defer workspaceRoot.Close()
	documents := make([]changeDiscoveryDocument, 0)
	totalBytes := int64(0)
	err = fs.WalkDir(workspaceRoot.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != "." && changeDiscoveryExcludedDirectories[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || isChangeDiscoverySensitiveName(entry.Name()) {
			return nil
		}
		state, err := entry.Info()
		if err != nil {
			return err
		}
		if !state.Mode().IsRegular() {
			return nil
		}
		if state.Size() > maximumChangeSimulationFileBytes {
			return nil
		}
		totalBytes += state.Size()
		if totalBytes > maximumChangeDiscoveryScanBytes {
			return fmt.Errorf("workspace discovery exceeds %d scanned bytes", maximumChangeDiscoveryScanBytes)
		}
		content, err := workspaceRoot.ReadFile(filepath.FromSlash(path))
		if err != nil {
			return err
		}
		if validateChangeSimulationContent(content) != nil {
			return nil
		}
		documents = append(documents, changeDiscoveryDocument{
			Path: filepath.ToSlash(path), Content: string(content),
		})
		if len(documents) > maximumChangeDiscoveryFiles {
			return fmt.Errorf("workspace discovery exceeds %d text files", maximumChangeDiscoveryFiles)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan change workspace: %w", err)
	}
	return documents, nil
}

func isChangeDiscoverySensitiveName(name string) bool {
	lower := strings.ToLower(name)
	if lower == ".env" || strings.HasPrefix(lower, ".env.") ||
		lower == "id_rsa" || lower == "id_ed25519" {
		return true
	}
	switch strings.ToLower(filepath.Ext(lower)) {
	case ".pem", ".key", ".p12", ".pfx":
		return true
	default:
		return false
	}
}

func rankChangeDiscoveryDocuments(
	documents []changeDiscoveryDocument,
	queries []string,
) []changeDiscoveryDocument {
	terms := make(map[string]bool)
	for _, query := range queries {
		for _, term := range tokenizeChangeDiscoveryTerms(query) {
			if len([]rune(term)) >= 3 {
				terms[term] = true
			}
		}
	}
	for index := range documents {
		path := strings.ToLower(documents[index].Path)
		content := strings.ToLower(documents[index].Content)
		score := 0.0
		for term := range terms {
			if strings.Contains(path, term) {
				score += 8
			}
			occurrences := strings.Count(content, term)
			if occurrences > 5 {
				occurrences = 5
			}
			score += float64(occurrences)
		}
		if strings.EqualFold(filepath.Base(path), "readme.md") && score > 0 {
			score += 1
		}
		documents[index].Score = score / math.Sqrt(float64(len([]rune(content))/1000)+1)
	}
	sort.Slice(documents, func(left, right int) bool {
		if documents[left].Score != documents[right].Score {
			return documents[left].Score > documents[right].Score
		}
		return documents[left].Path < documents[right].Path
	})
	result := documents[:0]
	for _, document := range documents {
		if document.Score <= 0 {
			continue
		}
		result = append(result, document)
	}
	return result
}

func tokenizeChangeDiscoveryTerms(text string) []string {
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
