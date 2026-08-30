package corpus

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestImportLocalCorpusFiltersDeduplicatesAndSplits(t *testing.T) {
	root := t.TempDir()
	trainingTexts, validationTexts := localCorpusTextsForBothSplits(t, 8)
	allTexts := append(append([]string(nil), trainingTexts...), validationTexts...)
	for index, text := range allTexts {
		path := filepath.Join(root, "documents", fmt.Sprintf("doc-%02d.md", index))
		writeTestFile(t, path, text)
	}
	writeTestFile(t, filepath.Join(root, "documents", "duplicate.md"), allTexts[0])
	writeTestFile(t, filepath.Join(root, "documents", "short.md"), "short")
	writeTestFile(t, filepath.Join(root, "documents", "ignored.bin"), strings.Repeat("valid text ", 20))
	writeTestBytes(t, filepath.Join(root, "documents", "invalid.txt"), []byte{0xff, 0xfe})
	writeTestBytes(t, filepath.Join(root, "documents", "binary.txt"), []byte("valid\x00binary"))
	writeTestFile(t, filepath.Join(root, ".git", "internal.md"), strings.Repeat("ignored ", 20))

	options := LocalCorpusImportOptions{
		Root:               root,
		OutputDir:          filepath.Join(t.TempDir(), "corpus"),
		Source:             "test-docs",
		License:            "CC-BY-4.0",
		Extensions:         []string{"md", ".txt"},
		ExcludedDirs:       []string{".git"},
		MinimumRunes:       20,
		MaximumFileBytes:   1024 * 1024,
		ValidationFraction: 0.5,
		ShardBytes:         300,
		Gzip:               true,
	}
	first, err := ImportLocalCorpus(options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Stats.DocumentsAccepted != 16 ||
		first.Stats.TrainingDocuments != 8 ||
		first.Stats.ValidationDocuments != 8 ||
		first.Stats.DuplicateDocuments != 1 ||
		first.Stats.SkippedExtension != 1 ||
		first.Stats.SkippedInvalidUTF8 != 1 ||
		first.Stats.SkippedBinary != 1 ||
		first.Stats.SkippedTooShort != 1 {
		t.Fatalf("unexpected import stats: %+v", first.Stats)
	}

	train := readLocalCorpusDocuments(t, first.TrainManifestPath)
	validation := readLocalCorpusDocuments(t, first.ValidationManifestPath)
	assertLocalCorpusMetadata(t, train, "train")
	assertLocalCorpusMetadata(t, validation, "validation")
	if len(train) != 8 || len(validation) != 8 {
		t.Fatalf("train=%d validation=%d, want 8 and 8", len(train), len(validation))
	}
	trainHashes := make(map[string]bool, len(train))
	for _, document := range train {
		trainHashes[document.Metadata["text_sha256"]] = true
	}
	for _, document := range validation {
		if trainHashes[document.Metadata["text_sha256"]] {
			t.Fatalf("document %q appears in both splits", document.ID)
		}
	}

	options.OutputDir = filepath.Join(t.TempDir(), "corpus-again")
	second, err := ImportLocalCorpus(options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(
		localCorpusDocumentIDs(train),
		localCorpusDocumentIDs(readLocalCorpusDocuments(t, second.TrainManifestPath)),
	) || !reflect.DeepEqual(
		localCorpusDocumentIDs(validation),
		localCorpusDocumentIDs(readLocalCorpusDocuments(t, second.ValidationManifestPath)),
	) {
		t.Fatal("repeated import produced a different deterministic split")
	}
}

func TestImportLocalCorpusCanDisableValidation(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "book.txt"), strings.Repeat("chapter text ", 20))
	result, err := ImportLocalCorpus(LocalCorpusImportOptions{
		Root:               root,
		OutputDir:          filepath.Join(t.TempDir(), "corpus"),
		Source:             "owned-book",
		License:            "owned",
		Extensions:         []string{".txt"},
		MinimumRunes:       1,
		MaximumFileBytes:   1024,
		ValidationFraction: 0,
		ShardBytes:         1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ValidationManifestPath != "" || result.Stats.TrainingDocuments != 1 {
		t.Fatalf("unexpected no-validation result: %+v", result)
	}
}

func TestImportLocalCorpusRejectsNonemptyOutput(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "document.txt"), strings.Repeat("text ", 20))
	output := t.TempDir()
	writeTestFile(t, filepath.Join(output, "keep.txt"), "user data")
	_, err := ImportLocalCorpus(LocalCorpusImportOptions{
		Root:             root,
		OutputDir:        output,
		Source:           "test",
		License:          "owned",
		Extensions:       []string{".txt"},
		MinimumRunes:     1,
		MaximumFileBytes: 1024,
		ShardBytes:       1024,
	})
	if err == nil || !strings.Contains(err.Error(), "must be empty") {
		t.Fatalf("nonempty output error = %v", err)
	}
}

func localCorpusTextsForBothSplits(t *testing.T, count int) ([]string, []string) {
	t.Helper()
	training := make([]string, 0, count)
	validation := make([]string, 0, count)
	for candidate := 0; len(training) < count || len(validation) < count; candidate++ {
		text := normalizeLocalCorpusText(strings.Repeat(
			fmt.Sprintf("unique document %d content ", candidate),
			5,
		))
		hash := sha256.Sum256([]byte(text))
		if localCorpusValidationSplit(hash, 0.5) {
			if len(validation) < count {
				validation = append(validation, text)
			}
		} else if len(training) < count {
			training = append(training, text)
		}
	}
	return training, validation
}

func writeTestFile(t *testing.T, path, text string) {
	t.Helper()
	writeTestBytes(t, path, []byte(text))
}

func writeTestBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readLocalCorpusDocuments(t *testing.T, manifestPath string) []CorpusDocument {
	t.Helper()
	reader, err := OpenCorpus(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	documents := make([]CorpusDocument, 0, reader.Manifest.Documents)
	if err := reader.Iterate(reader.StartCursor(), true, func(
		document CorpusDocument,
		_ CorpusCursor,
	) error {
		documents = append(documents, document)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return documents
}

func assertLocalCorpusMetadata(t *testing.T, documents []CorpusDocument, split string) {
	t.Helper()
	for _, document := range documents {
		if document.Source != "test-docs" || document.License != "CC-BY-4.0" ||
			document.Metadata["split"] != split ||
			document.Metadata["path"] != document.ID ||
			len(document.Metadata["text_sha256"]) != 64 {
			t.Fatalf("invalid imported document metadata: %+v", document)
		}
	}
}

func localCorpusDocumentIDs(documents []CorpusDocument) []string {
	ids := make([]string, len(documents))
	for index, document := range documents {
		ids[index] = document.ID
	}
	sort.Strings(ids)
	return ids
}
