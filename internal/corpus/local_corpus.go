package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

type LocalCorpusImportOptions struct {
	Root               string
	OutputDir          string
	Source             string
	License            string
	Extensions         []string
	ExcludedDirs       []string
	MinimumRunes       int
	MaximumFileBytes   int64
	MaximumDocuments   int
	ValidationFraction float64
	ShardBytes         int64
	Gzip               bool
}

type LocalCorpusImportStats struct {
	FilesDiscovered      int
	DocumentsAccepted    int
	TrainingDocuments    int
	ValidationDocuments  int
	DuplicateDocuments   int
	SkippedExtension     int
	SkippedTooLarge      int
	SkippedInvalidUTF8   int
	SkippedBinary        int
	SkippedTooShort      int
	SkippedDocumentLimit int
}

type LocalCorpusImportResult struct {
	TrainManifestPath      string
	TrainManifest          CorpusManifest
	ValidationManifestPath string
	ValidationManifest     CorpusManifest
	Stats                  LocalCorpusImportStats
}

type localCorpusCandidate struct {
	path       string
	relative   string
	textHash   [sha256.Size]byte
	validation bool
}

func ImportLocalCorpus(
	options LocalCorpusImportOptions,
) (LocalCorpusImportResult, error) {
	var result LocalCorpusImportResult
	if err := validateLocalCorpusImportOptions(options); err != nil {
		return result, err
	}
	root, err := filepath.Abs(options.Root)
	if err != nil {
		return result, fmt.Errorf("resolve local corpus root: %w", err)
	}
	output, err := filepath.Abs(options.OutputDir)
	if err != nil {
		return result, fmt.Errorf("resolve local corpus output: %w", err)
	}
	if root == output {
		return result, fmt.Errorf("local corpus output must differ from input root")
	}
	info, err := os.Stat(root)
	if err != nil {
		return result, fmt.Errorf("inspect local corpus root %q: %w", root, err)
	}
	if !info.IsDir() {
		return result, fmt.Errorf("local corpus root %q is not a directory", root)
	}
	if err := requireEmptyOrMissingDirectory(output); err != nil {
		return result, err
	}

	extensions, acceptEveryExtension, err := normalizeLocalExtensions(options.Extensions)
	if err != nil {
		return result, err
	}
	excluded := make(map[string]bool, len(options.ExcludedDirs))
	for _, name := range options.ExcludedDirs {
		name = strings.TrimSpace(name)
		if name != "" {
			excluded[name] = true
		}
	}

	paths := make([]string, 0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (path == output || excluded[entry.Name()]) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		result.Stats.FilesDiscovered++
		if !acceptEveryExtension && !extensions[strings.ToLower(filepath.Ext(path))] {
			result.Stats.SkippedExtension++
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("scan local corpus root %q: %w", root, err)
	}
	sort.Strings(paths)

	seenText := make(map[[sha256.Size]byte]bool)
	candidates := make([]localCorpusCandidate, 0, len(paths))
	// The first pass retains only paths and hashes. The second pass rereads one
	// accepted file at a time while writing shards, so RAM usage is bounded by
	// the largest allowed file instead of the total corpus size.
	for pathIndex, path := range paths {
		if options.MaximumDocuments > 0 &&
			len(candidates) >= options.MaximumDocuments {
			result.Stats.SkippedDocumentLimit += len(paths) - pathIndex
			break
		}
		_, hash, skip, err := readLocalCorpusText(path, options)
		if err != nil {
			return result, err
		}
		if skip != "" {
			observeLocalCorpusSkip(&result.Stats, skip)
			continue
		}
		if seenText[hash] {
			result.Stats.DuplicateDocuments++
			continue
		}
		seenText[hash] = true
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return result, fmt.Errorf("make local corpus path relative: %w", err)
		}
		candidate := localCorpusCandidate{
			path:       path,
			relative:   filepath.ToSlash(relative),
			textHash:   hash,
			validation: localCorpusValidationSplit(hash, options.ValidationFraction),
		}
		candidates = append(candidates, candidate)
		if candidate.validation {
			result.Stats.ValidationDocuments++
		} else {
			result.Stats.TrainingDocuments++
		}
	}
	result.Stats.DocumentsAccepted = len(candidates)
	if result.Stats.TrainingDocuments == 0 {
		return result, fmt.Errorf("local corpus import produced no training documents")
	}
	if options.ValidationFraction > 0 && result.Stats.ValidationDocuments == 0 {
		return result, fmt.Errorf(
			"local corpus validation split produced no documents; add data or change validation percentage",
		)
	}

	trainWriter, err := NewCorpusWriter(
		filepath.Join(output, "train"),
		options.ShardBytes,
		options.Gzip,
	)
	if err != nil {
		return result, err
	}
	var validationWriter *CorpusWriter
	if options.ValidationFraction > 0 {
		validationWriter, err = NewCorpusWriter(
			filepath.Join(output, "validation"),
			options.ShardBytes,
			options.Gzip,
		)
		if err != nil {
			return result, err
		}
	}

	for _, candidate := range candidates {
		text, hash, skip, err := readLocalCorpusText(candidate.path, options)
		if err != nil {
			return result, err
		}
		if skip != "" || hash != candidate.textHash {
			return result, fmt.Errorf(
				"local corpus file %q changed while it was being imported",
				candidate.path,
			)
		}
		document := CorpusDocument{
			ID:      candidate.relative,
			Text:    text,
			Source:  options.Source,
			License: options.License,
			Metadata: map[string]string{
				"path":        candidate.relative,
				"extension":   strings.ToLower(filepath.Ext(candidate.relative)),
				"text_sha256": hex.EncodeToString(hash[:]),
			},
		}
		writer := trainWriter
		if candidate.validation {
			writer = validationWriter
			document.Metadata["split"] = "validation"
		} else {
			document.Metadata["split"] = "train"
		}
		if err := writer.Add(document); err != nil {
			return result, err
		}
	}
	result.TrainManifestPath, result.TrainManifest, err = trainWriter.Close()
	if err != nil {
		return result, err
	}
	if validationWriter != nil {
		result.ValidationManifestPath,
			result.ValidationManifest,
			err = validationWriter.Close()
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func validateLocalCorpusImportOptions(options LocalCorpusImportOptions) error {
	if strings.TrimSpace(options.Root) == "" ||
		strings.TrimSpace(options.OutputDir) == "" ||
		strings.TrimSpace(options.Source) == "" ||
		strings.TrimSpace(options.License) == "" {
		return fmt.Errorf("local corpus root, output, source and license are required")
	}
	if options.MinimumRunes < 0 || options.MaximumFileBytes <= 0 ||
		options.MaximumDocuments < 0 || options.ValidationFraction < 0 ||
		options.ValidationFraction >= 1 || options.ShardBytes <= 0 {
		return fmt.Errorf("local corpus import contains invalid numeric options")
	}
	if len(options.Extensions) == 0 {
		return fmt.Errorf("local corpus import requires at least one extension")
	}
	return nil
}

func requireEmptyOrMissingDirectory(path string) error {
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read local corpus output %q: %w", path, err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("local corpus output directory %q must be empty", path)
	}
	return nil
}

func normalizeLocalExtensions(values []string) (map[string]bool, bool, error) {
	result := make(map[string]bool, len(values))
	acceptEvery := false
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "*" {
			acceptEvery = true
			continue
		}
		if value == "" {
			continue
		}
		if !strings.HasPrefix(value, ".") {
			value = "." + value
		}
		if strings.ContainsAny(value, `/\\`) {
			return nil, false, fmt.Errorf("invalid local corpus extension %q", value)
		}
		result[value] = true
	}
	if !acceptEvery && len(result) == 0 {
		return nil, false, fmt.Errorf("local corpus extension list is empty")
	}
	return result, acceptEvery, nil
}

func readLocalCorpusText(
	path string,
	options LocalCorpusImportOptions,
) (string, [sha256.Size]byte, string, error) {
	var emptyHash [sha256.Size]byte
	info, err := os.Stat(path)
	if err != nil {
		return "", emptyHash, "", fmt.Errorf("inspect local corpus file %q: %w", path, err)
	}
	if info.Size() > options.MaximumFileBytes {
		return "", emptyHash, "too-large", nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", emptyHash, "", fmt.Errorf("read local corpus file %q: %w", path, err)
	}
	if !utf8.Valid(raw) {
		return "", emptyHash, "invalid-utf8", nil
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return "", emptyHash, "binary", nil
	}
	text := normalizeLocalCorpusText(string(raw))
	if utf8.RuneCountInString(text) < options.MinimumRunes {
		return "", emptyHash, "too-short", nil
	}
	return text, sha256.Sum256([]byte(text)), "", nil
}

func normalizeLocalCorpusText(text string) string {
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimSpace(text)
	if text == "" {
		return text
	}
	return text + "\n"
}

func localCorpusValidationSplit(
	textHash [sha256.Size]byte,
	fraction float64,
) bool {
	if fraction <= 0 {
		return false
	}
	value := binary.BigEndian.Uint64(textHash[:8])
	return float64(value)/float64(^uint64(0)) < fraction
}

func observeLocalCorpusSkip(stats *LocalCorpusImportStats, reason string) {
	switch reason {
	case "too-large":
		stats.SkippedTooLarge++
	case "invalid-utf8":
		stats.SkippedInvalidUTF8++
	case "binary":
		stats.SkippedBinary++
	case "too-short":
		stats.SkippedTooShort++
	default:
		panic("unknown local corpus skip reason: " + reason)
	}
}
