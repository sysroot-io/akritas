package corpus

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCorpusShardsRoundTripAndResume(t *testing.T) {
	documents := []CorpusDocument{
		{ID: "doc-1", Text: strings.Repeat("Первый документ. ", 5), Source: "test"},
		{ID: "doc-2", Text: strings.Repeat("Second document. ", 5), Source: "test"},
		{ID: "doc-3", Text: strings.Repeat("第三个文档。", 8), Source: "test"},
	}
	writer, err := NewCorpusWriter(t.TempDir(), 120, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, document := range documents {
		if err := writer.Add(document); err != nil {
			t.Fatal(err)
		}
	}
	manifestPath, manifest, err := writer.Close()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Documents != len(documents) || len(manifest.Shards) < 2 {
		t.Fatalf(
			"manifest has %d documents and %d shards",
			manifest.Documents,
			len(manifest.Shards),
		)
	}
	if manifest.Compression != "gzip" {
		t.Fatalf("compression = %q, want gzip", manifest.Compression)
	}
	var streamedInput []CorpusDocument
	for _, shard := range manifest.Shards {
		path := filepath.Join(
			filepath.Dir(manifestPath),
			filepath.FromSlash(shard.Path),
		)
		if err := ReadCorpusJSONL(path, func(document CorpusDocument) error {
			streamedInput = append(streamedInput, document)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(streamedInput, documents) {
		t.Fatal("streaming JSONL.GZ input did not reproduce documents")
	}

	reader, err := OpenCorpus(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var actual []CorpusDocument
	if err := reader.Iterate(reader.StartCursor(), true, func(
		document CorpusDocument,
		_ CorpusCursor,
	) error {
		actual = append(actual, document)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, documents) {
		t.Fatalf("round-trip documents differ:\n got: %#v\nwant: %#v", actual, documents)
	}

	stop := errors.New("intentional stop")
	var resume CorpusCursor
	err = reader.Iterate(reader.StartCursor(), true, func(
		_ CorpusDocument,
		next CorpusCursor,
	) error {
		resume = next
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("early stop error = %v, want sentinel", err)
	}

	cursorPath := filepath.Join(t.TempDir(), "progress.json")
	if err := SaveCorpusCursor(cursorPath, resume); err != nil {
		t.Fatal(err)
	}
	// Updating the same path exercises Windows-compatible cursor replacement.
	if err := SaveCorpusCursor(cursorPath, resume); err != nil {
		t.Fatal(err)
	}
	loadedCursor, err := LoadCorpusCursor(cursorPath)
	if err != nil {
		t.Fatal(err)
	}
	var resumedIDs []string
	if err := reader.Iterate(loadedCursor, true, func(
		document CorpusDocument,
		_ CorpusCursor,
	) error {
		resumedIDs = append(resumedIDs, document.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resumedIDs, []string{"doc-2", "doc-3"}) {
		t.Fatalf("resumed IDs = %v, want [doc-2 doc-3]", resumedIDs)
	}
}

func TestCorpusVerificationRejectsModifiedShard(t *testing.T) {
	directory := t.TempDir()
	writer, err := NewCorpusWriter(directory, 1024, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Add(CorpusDocument{ID: "doc", Text: "original text"}); err != nil {
		t.Fatal(err)
	}
	manifestPath, manifest, err := writer.Close()
	if err != nil {
		t.Fatal(err)
	}

	shardPath := filepath.Join(directory, filepath.FromSlash(manifest.Shards[0].Path))
	file, err := os.OpenFile(shardPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("X"), 0); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenCorpus(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	err = reader.Iterate(reader.StartCursor(), true, func(
		CorpusDocument,
		CorpusCursor,
	) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "failed SHA-256") {
		t.Fatalf("verification error = %v, want SHA-256 failure", err)
	}
}

func TestCorpusManifestRejectsEscapingShardPath(t *testing.T) {
	directory := t.TempDir()
	manifest := CorpusManifest{
		Version:        corpusManifestVersion,
		DocumentFormat: "jsonl",
		Compression:    "none",
		Documents:      1,
		UTF8Bytes:      1,
		JSONLBytes:     1,
		Shards: []CorpusShard{{
			Path:       "../outside.jsonl",
			Documents:  1,
			UTF8Bytes:  1,
			JSONLBytes: 1,
			FileBytes:  1,
			SHA256:     strings.Repeat("0", 64),
		}},
	}
	path := filepath.Join(directory, corpusManifestName)
	if err := writeJSONAtomic(path, manifest); err != nil {
		t.Fatal(err)
	}
	_, err := OpenCorpus(path)
	if err == nil || !strings.Contains(err.Error(), "escapes corpus directory") {
		t.Fatalf("OpenCorpus error = %v, want unsafe path rejection", err)
	}
}

func TestCorpusCursorRejectsDifferentManifest(t *testing.T) {
	build := func(text string) *CorpusReader {
		t.Helper()
		writer, err := NewCorpusWriter(t.TempDir(), 1024, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.Add(CorpusDocument{ID: text, Text: text}); err != nil {
			t.Fatal(err)
		}
		path, _, err := writer.Close()
		if err != nil {
			t.Fatal(err)
		}
		reader, err := OpenCorpus(path)
		if err != nil {
			t.Fatal(err)
		}
		return reader
	}

	first := build("first")
	second := build("second")
	err := second.Iterate(first.StartCursor(), false, func(
		CorpusDocument,
		CorpusCursor,
	) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "different manifest") {
		t.Fatalf("cursor mismatch error = %v", err)
	}
}

func TestCorpusReaderUsesDeterministicShardPermutation(t *testing.T) {
	directory := t.TempDir()
	writer, err := NewCorpusWriter(directory, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if err := writer.Add(CorpusDocument{ID: id, Text: "text-" + id}); err != nil {
			t.Fatal(err)
		}
	}
	manifestPath, manifest, err := writer.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Shards) != 4 {
		t.Fatalf("shards = %d, want 4", len(manifest.Shards))
	}
	reader, err := OpenCorpus(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	ordered, err := reader.WithShardOrder([]int{3, 1, 0, 2})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := ordered.Iterate(ordered.StartCursor(), true, func(
		document CorpusDocument,
		_ CorpusCursor,
	) error {
		ids = append(ids, document.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"d", "b", "a", "c"}) {
		t.Fatalf("permuted IDs = %v", ids)
	}
	if ordered.ManifestSHA256 != reader.ManifestSHA256 {
		t.Fatal("shard view changed immutable manifest fingerprint")
	}
	if _, err := reader.WithShardOrder([]int{0, 0, 1, 2}); err == nil {
		t.Fatal("duplicate shard permutation was accepted")
	}
}
