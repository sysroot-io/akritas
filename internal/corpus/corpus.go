package corpus

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	corpusManifestVersion = 1
	corpusManifestName    = "manifest.json"
)

// CorpusDocument is one independently addressable pretraining document.
// Metadata deliberately stays string-to-string so the on-disk schema remains
// predictable and can be produced by small external extractors.
type CorpusDocument struct {
	ID       string            `json:"id"`
	Text     string            `json:"text"`
	Source   string            `json:"source,omitempty"`
	License  string            `json:"license,omitempty"`
	URL      string            `json:"url,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type CorpusManifest struct {
	Version        int                      `json:"version"`
	DocumentFormat string                   `json:"document_format"`
	Compression    string                   `json:"compression"`
	Documents      int                      `json:"documents"`
	UTF8Bytes      int64                    `json:"utf8_bytes"`
	JSONLBytes     int64                    `json:"jsonl_bytes"`
	Sources        []CorpusSourceDescriptor `json:"sources,omitempty"`
	Shards         []CorpusShard            `json:"shards"`
}

// CorpusSourceDescriptor records where corpus documents came from and which
// terms apply to the dataset. Inputs make a local import reproducible without
// putting the (potentially very large) source files in Git.
type CorpusSourceDescriptor struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	DatasetURL  string            `json:"dataset_url"`
	License     string            `json:"license"`
	LicenseURL  string            `json:"license_url"`
	TermsURL    string            `json:"terms_url,omitempty"`
	Attribution string            `json:"attribution"`
	Revision    string            `json:"revision"`
	Language    string            `json:"language,omitempty"`
	Notes       string            `json:"notes,omitempty"`
	Inputs      []CorpusInputFile `json:"inputs,omitempty"`
}

type CorpusInputFile struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	FileBytes int64  `json:"file_bytes"`
}

type CorpusShard struct {
	Path       string `json:"path"`
	Documents  int    `json:"documents"`
	UTF8Bytes  int64  `json:"utf8_bytes"`
	JSONLBytes int64  `json:"jsonl_bytes"`
	FileBytes  int64  `json:"file_bytes"`
	SHA256     string `json:"sha256"`
}

// CorpusCursor points to the next document to process. ManifestSHA256 prevents
// a progress file from being reused after the corpus manifest has changed.
type CorpusCursor struct {
	Version        int    `json:"version"`
	ManifestSHA256 string `json:"manifest_sha256"`
	Shard          int    `json:"shard"`
	Document       int    `json:"document"`
}

type CorpusWriter struct {
	outputDir        string
	targetJSONLBytes int64
	compression      string
	manifest         CorpusManifest
	current          *corpusShardWriter
	closed           bool
}

type corpusShardWriter struct {
	path       string
	relative   string
	file       *os.File
	buffer     *bufio.Writer
	gzip       *gzip.Writer
	writer     io.Writer
	documents  int
	utf8Bytes  int64
	jsonlBytes int64
}

func NewCorpusWriter(
	outputDir string,
	targetJSONLBytes int64,
	useGzip bool,
) (*CorpusWriter, error) {
	if strings.TrimSpace(outputDir) == "" {
		return nil, fmt.Errorf("corpus writer: output directory is empty")
	}
	if targetJSONLBytes <= 0 {
		return nil, fmt.Errorf("corpus writer: shard target must be positive")
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create corpus directory %q: %w", outputDir, err)
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil, fmt.Errorf("read corpus directory %q: %w", outputDir, err)
	}
	if len(entries) != 0 {
		return nil, fmt.Errorf(
			"corpus writer: output directory %q must be empty",
			outputDir,
		)
	}

	compression := "none"
	if useGzip {
		compression = "gzip"
	}
	return &CorpusWriter{
		outputDir:        outputDir,
		targetJSONLBytes: targetJSONLBytes,
		compression:      compression,
		manifest: CorpusManifest{
			Version:        corpusManifestVersion,
			DocumentFormat: "jsonl",
			Compression:    compression,
		},
	}, nil
}

func (w *CorpusWriter) Add(document CorpusDocument) error {
	if w.closed {
		return fmt.Errorf("corpus writer: writer is closed")
	}
	if err := validateCorpusDocument(document); err != nil {
		return err
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode corpus document %q: %w", document.ID, err)
	}
	encoded = append(encoded, '\n')

	if w.current == nil ||
		(w.current.documents > 0 &&
			w.current.jsonlBytes+int64(len(encoded)) > w.targetJSONLBytes) {
		if err := w.finishCurrentShard(); err != nil {
			return err
		}
		if err := w.startShard(); err != nil {
			return err
		}
	}
	if _, err := w.current.writer.Write(encoded); err != nil {
		return fmt.Errorf("write corpus shard %q: %w", w.current.path, err)
	}

	textBytes := int64(len([]byte(document.Text)))
	w.current.documents++
	w.current.utf8Bytes += textBytes
	w.current.jsonlBytes += int64(len(encoded))
	return nil
}

// SetSources attaches immutable provenance to the manifest. It must be called
// before the first document so every shard is covered by the same declaration.
func (w *CorpusWriter) SetSources(sources []CorpusSourceDescriptor) error {
	if w.closed {
		return fmt.Errorf("corpus writer: writer is closed")
	}
	if w.current != nil || w.manifest.Documents != 0 {
		return fmt.Errorf("corpus writer: sources must be set before documents")
	}
	if err := validateCorpusSources(sources); err != nil {
		return err
	}
	w.manifest.Sources = make([]CorpusSourceDescriptor, len(sources))
	for index, source := range sources {
		source.Inputs = append([]CorpusInputFile(nil), source.Inputs...)
		w.manifest.Sources[index] = source
	}
	return nil
}

func (w *CorpusWriter) Close() (string, CorpusManifest, error) {
	if w.closed {
		return "", CorpusManifest{}, fmt.Errorf("corpus writer: already closed")
	}
	w.closed = true
	if w.current == nil {
		return "", CorpusManifest{}, fmt.Errorf("corpus writer: no documents were added")
	}
	if err := w.finishCurrentShard(); err != nil {
		return "", CorpusManifest{}, err
	}

	manifestPath := filepath.Join(w.outputDir, corpusManifestName)
	if err := writeJSONAtomic(manifestPath, w.manifest); err != nil {
		return "", CorpusManifest{}, err
	}
	return manifestPath, w.manifest, nil
}

func (w *CorpusWriter) startShard() error {
	index := len(w.manifest.Shards)
	extension := ".jsonl"
	if w.compression == "gzip" {
		extension += ".gz"
	}
	relative := fmt.Sprintf("shard-%05d%s", index, extension)
	path := filepath.Join(w.outputDir, relative)
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create corpus shard %q: %w", path, err)
	}

	shard := &corpusShardWriter{
		path:     path,
		relative: filepath.ToSlash(relative),
		file:     file,
	}
	if w.compression == "gzip" {
		shard.gzip = gzip.NewWriter(file)
		shard.writer = shard.gzip
	} else {
		shard.buffer = bufio.NewWriterSize(file, 256*1024)
		shard.writer = shard.buffer
	}
	w.current = shard
	return nil
}

func (w *CorpusWriter) finishCurrentShard() error {
	if w.current == nil {
		return nil
	}
	shard := w.current
	if shard.gzip != nil {
		if err := shard.gzip.Close(); err != nil {
			shard.file.Close()
			return fmt.Errorf("close gzip shard %q: %w", shard.path, err)
		}
	}
	if shard.buffer != nil {
		if err := shard.buffer.Flush(); err != nil {
			shard.file.Close()
			return fmt.Errorf("flush corpus shard %q: %w", shard.path, err)
		}
	}
	if err := shard.file.Close(); err != nil {
		return fmt.Errorf("close corpus shard %q: %w", shard.path, err)
	}

	hash, fileBytes, err := hashFile(shard.path)
	if err != nil {
		return err
	}
	state := CorpusShard{
		Path:       shard.relative,
		Documents:  shard.documents,
		UTF8Bytes:  shard.utf8Bytes,
		JSONLBytes: shard.jsonlBytes,
		FileBytes:  fileBytes,
		SHA256:     hash,
	}
	w.manifest.Shards = append(w.manifest.Shards, state)
	w.manifest.Documents += state.Documents
	w.manifest.UTF8Bytes += state.UTF8Bytes
	w.manifest.JSONLBytes += state.JSONLBytes
	w.current = nil
	return nil
}

type CorpusReader struct {
	ManifestPath   string
	Manifest       CorpusManifest
	ManifestSHA256 string
	baseDir        string
}

type CorpusIterator struct {
	corpus           *CorpusReader
	verifyHashes     bool
	shardIndex       int
	documentIndex    int
	decoder          *json.Decoder
	closeReader      func() error
	decodedDocuments int
	decodedUTF8Bytes int64
	closed           bool
}

func OpenCorpus(manifestPath string) (*CorpusReader, error) {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read corpus manifest %q: %w", manifestPath, err)
	}
	var manifest CorpusManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("decode corpus manifest %q: %w", manifestPath, err)
	}
	baseDir, err := filepath.Abs(filepath.Dir(manifestPath))
	if err != nil {
		return nil, fmt.Errorf("resolve corpus directory: %w", err)
	}
	if err := validateCorpusManifest(manifest, baseDir); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(raw)
	return &CorpusReader{
		ManifestPath:   manifestPath,
		Manifest:       manifest,
		ManifestSHA256: hex.EncodeToString(hash[:]),
		baseDir:        baseDir,
	}, nil
}

// Iterate streams documents starting at cursor. next points immediately after
// the delivered document and can be saved after a successful training update.
func (r *CorpusReader) Iterate(
	cursor CorpusCursor,
	verifyHashes bool,
	visit func(document CorpusDocument, next CorpusCursor) error,
) error {
	if visit == nil {
		return fmt.Errorf("iterate corpus: visit function is nil")
	}
	if cursor.ManifestSHA256 == "" {
		cursor = r.StartCursor()
	}
	if cursor.Version != corpusManifestVersion ||
		cursor.ManifestSHA256 != r.ManifestSHA256 {
		return fmt.Errorf("iterate corpus: cursor belongs to a different manifest")
	}
	if cursor.Shard < 0 || cursor.Shard > len(r.Manifest.Shards) {
		return fmt.Errorf("iterate corpus: cursor shard %d is out of range", cursor.Shard)
	}
	if cursor.Shard == len(r.Manifest.Shards) {
		if cursor.Document != 0 {
			return fmt.Errorf("iterate corpus: completed cursor has non-zero document")
		}
		return nil
	}

	for shardIndex := cursor.Shard; shardIndex < len(r.Manifest.Shards); shardIndex++ {
		shardState := r.Manifest.Shards[shardIndex]
		startDocument := 0
		if shardIndex == cursor.Shard {
			startDocument = cursor.Document
		}
		if startDocument < 0 || startDocument > shardState.Documents {
			return fmt.Errorf(
				"iterate corpus: document %d is out of range for shard %d",
				startDocument,
				shardIndex,
			)
		}

		path, err := safeShardPath(r.baseDir, shardState.Path)
		if err != nil {
			return err
		}
		if verifyHashes {
			hash, fileBytes, err := hashFile(path)
			if err != nil {
				return err
			}
			if hash != shardState.SHA256 || fileBytes != shardState.FileBytes {
				return fmt.Errorf("corpus shard %q failed SHA-256 or size verification", path)
			}
		}

		documentCount, textBytes, err := r.iterateShard(
			path,
			shardIndex,
			startDocument,
			visit,
		)
		if err != nil {
			return err
		}
		if documentCount != shardState.Documents || textBytes != shardState.UTF8Bytes {
			return fmt.Errorf(
				"corpus shard %q counts differ from manifest",
				path,
			)
		}
	}
	return nil
}

func (r *CorpusReader) StartCursor() CorpusCursor {
	return CorpusCursor{
		Version:        corpusManifestVersion,
		ManifestSHA256: r.ManifestSHA256,
	}
}

// WithShardOrder returns a lightweight reader view whose cursor shard index is
// an index into order. Files and the manifest fingerprint remain unchanged, so
// checkpoint validation still refers to the original immutable corpus.
func (r *CorpusReader) WithShardOrder(order []int) (*CorpusReader, error) {
	if len(order) == 0 {
		return r, nil
	}
	if len(order) != len(r.Manifest.Shards) {
		return nil, fmt.Errorf(
			"corpus shard order has %d entries, want %d",
			len(order),
			len(r.Manifest.Shards),
		)
	}
	seen := make([]bool, len(order))
	shards := make([]CorpusShard, len(order))
	for position, shardIndex := range order {
		if shardIndex < 0 || shardIndex >= len(order) || seen[shardIndex] {
			return nil, fmt.Errorf("corpus shard order is not a permutation")
		}
		seen[shardIndex] = true
		shards[position] = r.Manifest.Shards[shardIndex]
	}
	manifest := r.Manifest
	manifest.Shards = shards
	return &CorpusReader{
		ManifestPath:   r.ManifestPath,
		Manifest:       manifest,
		ManifestSHA256: r.ManifestSHA256,
		baseDir:        r.baseDir,
	}, nil
}

func (r *CorpusReader) NewIterator(
	cursor CorpusCursor,
	verifyHashes bool,
) (*CorpusIterator, error) {
	if cursor.ManifestSHA256 == "" {
		cursor = r.StartCursor()
	}
	if cursor.Version != corpusManifestVersion ||
		cursor.ManifestSHA256 != r.ManifestSHA256 {
		return nil, fmt.Errorf("corpus iterator: cursor belongs to a different manifest")
	}
	if cursor.Shard < 0 || cursor.Shard > len(r.Manifest.Shards) {
		return nil, fmt.Errorf("corpus iterator: shard %d is out of range", cursor.Shard)
	}
	if cursor.Shard == len(r.Manifest.Shards) && cursor.Document != 0 {
		return nil, fmt.Errorf("corpus iterator: completed cursor has non-zero document")
	}
	if cursor.Shard < len(r.Manifest.Shards) &&
		(cursor.Document < 0 || cursor.Document > r.Manifest.Shards[cursor.Shard].Documents) {
		return nil, fmt.Errorf("corpus iterator: document %d is out of range", cursor.Document)
	}
	return &CorpusIterator{
		corpus:        r,
		verifyHashes:  verifyHashes,
		shardIndex:    cursor.Shard,
		documentIndex: cursor.Document,
	}, nil
}

// Next returns one document and a cursor pointing immediately after it. The
// current gzip stream remains open between calls.
func (i *CorpusIterator) Next() (CorpusDocument, CorpusCursor, error) {
	if i.closed {
		return CorpusDocument{}, CorpusCursor{}, fmt.Errorf("corpus iterator is closed")
	}
	for i.shardIndex < len(i.corpus.Manifest.Shards) {
		if i.decoder == nil {
			if err := i.openShard(); err != nil {
				return CorpusDocument{}, CorpusCursor{}, err
			}
		}

		var document CorpusDocument
		err := i.decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			if err := i.finishShard(); err != nil {
				return CorpusDocument{}, CorpusCursor{}, err
			}
			i.shardIndex++
			i.documentIndex = 0
			continue
		}
		if err != nil {
			return CorpusDocument{}, CorpusCursor{}, fmt.Errorf(
				"decode corpus shard %d document %d: %w",
				i.shardIndex,
				i.decodedDocuments,
				err,
			)
		}
		if err := validateCorpusDocument(document); err != nil {
			return CorpusDocument{}, CorpusCursor{}, err
		}

		decodedIndex := i.decodedDocuments
		i.decodedDocuments++
		i.decodedUTF8Bytes += int64(len([]byte(document.Text)))
		if decodedIndex < i.documentIndex {
			continue
		}

		i.documentIndex = decodedIndex + 1
		next := CorpusCursor{
			Version:        corpusManifestVersion,
			ManifestSHA256: i.corpus.ManifestSHA256,
			Shard:          i.shardIndex,
			Document:       i.documentIndex,
		}
		if next.Document == i.corpus.Manifest.Shards[i.shardIndex].Documents {
			next.Shard++
			next.Document = 0
		}
		return document, next, nil
	}
	return CorpusDocument{}, CorpusCursor{}, io.EOF
}

func (i *CorpusIterator) Close() error {
	if i.closed {
		return nil
	}
	i.closed = true
	if i.closeReader != nil {
		return i.closeReader()
	}
	return nil
}

func (i *CorpusIterator) openShard() error {
	state := i.corpus.Manifest.Shards[i.shardIndex]
	path, err := safeShardPath(i.corpus.baseDir, state.Path)
	if err != nil {
		return err
	}
	if i.verifyHashes {
		hash, fileBytes, err := hashFile(path)
		if err != nil {
			return err
		}
		if hash != state.SHA256 || fileBytes != state.FileBytes {
			return fmt.Errorf("corpus shard %q failed SHA-256 or size verification", path)
		}
	}
	reader, closeReader, err := openMaybeGzip(path, i.corpus.Manifest.Compression)
	if err != nil {
		return err
	}
	i.decoder = json.NewDecoder(reader)
	i.closeReader = closeReader
	i.decodedDocuments = 0
	i.decodedUTF8Bytes = 0
	return nil
}

func (i *CorpusIterator) finishShard() error {
	state := i.corpus.Manifest.Shards[i.shardIndex]
	if i.decodedDocuments != state.Documents ||
		i.decodedUTF8Bytes != state.UTF8Bytes {
		return fmt.Errorf("corpus iterator: shard %d counts differ from manifest", i.shardIndex)
	}
	if i.closeReader != nil {
		if err := i.closeReader(); err != nil {
			return err
		}
	}
	i.decoder = nil
	i.closeReader = nil
	i.decodedDocuments = 0
	i.decodedUTF8Bytes = 0
	return nil
}

func (r *CorpusReader) iterateShard(
	path string,
	shardIndex int,
	startDocument int,
	visit func(document CorpusDocument, next CorpusCursor) error,
) (int, int64, error) {
	reader, closeReader, err := openMaybeGzip(path, r.Manifest.Compression)
	if err != nil {
		return 0, 0, err
	}
	defer closeReader()

	decoder := json.NewDecoder(reader)
	documentIndex := 0
	textBytes := int64(0)
	for {
		var document CorpusDocument
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, 0, fmt.Errorf(
				"decode corpus shard %q document %d: %w",
				path,
				documentIndex,
				err,
			)
		}
		if err := validateCorpusDocument(document); err != nil {
			return 0, 0, fmt.Errorf("corpus shard %q: %w", path, err)
		}
		textBytes += int64(len([]byte(document.Text)))

		if documentIndex >= startDocument {
			next := CorpusCursor{
				Version:        corpusManifestVersion,
				ManifestSHA256: r.ManifestSHA256,
				Shard:          shardIndex,
				Document:       documentIndex + 1,
			}
			if next.Document == r.Manifest.Shards[shardIndex].Documents {
				next.Shard++
				next.Document = 0
			}
			if err := visit(document, next); err != nil {
				return 0, 0, err
			}
		}
		documentIndex++
	}
	return documentIndex, textBytes, nil
}

func SaveCorpusCursor(path string, cursor CorpusCursor) error {
	if cursor.Version != corpusManifestVersion || cursor.ManifestSHA256 == "" ||
		cursor.Shard < 0 || cursor.Document < 0 {
		return fmt.Errorf("save corpus cursor: invalid cursor")
	}
	return writeJSONAtomic(path, cursor)
}

func LoadCorpusCursor(path string) (CorpusCursor, error) {
	file, err := os.Open(path)
	if err != nil {
		return CorpusCursor{}, fmt.Errorf("open corpus cursor %q: %w", path, err)
	}
	defer file.Close()
	var cursor CorpusCursor
	if err := json.NewDecoder(file).Decode(&cursor); err != nil {
		return CorpusCursor{}, fmt.Errorf("decode corpus cursor %q: %w", path, err)
	}
	if cursor.Version != corpusManifestVersion || cursor.ManifestSHA256 == "" ||
		cursor.Shard < 0 || cursor.Document < 0 {
		return CorpusCursor{}, fmt.Errorf("decode corpus cursor %q: invalid cursor", path)
	}
	return cursor, nil
}

func ReadCorpusJSONL(path string, visit func(CorpusDocument) error) error {
	compression := "none"
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		compression = "gzip"
	}
	reader, closeReader, err := openMaybeGzip(path, compression)
	if err != nil {
		return err
	}
	defer closeReader()

	decoder := json.NewDecoder(reader)
	index := 0
	for {
		var document CorpusDocument
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decode corpus input %q document %d: %w", path, index, err)
		}
		if err := validateCorpusDocument(document); err != nil {
			return fmt.Errorf("corpus input %q document %d: %w", path, index, err)
		}
		if err := visit(document); err != nil {
			return err
		}
		index++
	}
}

func validateCorpusDocument(document CorpusDocument) error {
	if strings.TrimSpace(document.ID) == "" {
		return fmt.Errorf("corpus document: id is empty")
	}
	if document.Text == "" {
		return fmt.Errorf("corpus document %q: text is empty", document.ID)
	}
	if !utf8.ValidString(document.ID) || !utf8.ValidString(document.Text) ||
		!utf8.ValidString(document.Source) || !utf8.ValidString(document.License) ||
		!utf8.ValidString(document.URL) {
		return fmt.Errorf("corpus document %q: fields must be valid UTF-8", document.ID)
	}
	for key, value := range document.Metadata {
		if !utf8.ValidString(key) || !utf8.ValidString(value) {
			return fmt.Errorf("corpus document %q: metadata must be valid UTF-8", document.ID)
		}
	}
	return nil
}

func validateCorpusManifest(manifest CorpusManifest, baseDir string) error {
	if manifest.Version != corpusManifestVersion {
		return fmt.Errorf(
			"corpus manifest: unsupported version %d",
			manifest.Version,
		)
	}
	if manifest.DocumentFormat != "jsonl" {
		return fmt.Errorf("corpus manifest: unsupported document format %q", manifest.DocumentFormat)
	}
	if manifest.Compression != "none" && manifest.Compression != "gzip" {
		return fmt.Errorf("corpus manifest: unsupported compression %q", manifest.Compression)
	}
	if len(manifest.Shards) == 0 {
		return fmt.Errorf("corpus manifest: no shards")
	}
	if err := validateCorpusSources(manifest.Sources); err != nil {
		return err
	}

	documents := 0
	utf8Bytes := int64(0)
	jsonlBytes := int64(0)
	for index, shard := range manifest.Shards {
		if shard.Documents <= 0 || shard.UTF8Bytes <= 0 ||
			shard.JSONLBytes <= 0 || shard.FileBytes <= 0 {
			return fmt.Errorf("corpus manifest: shard %d has invalid counters", index)
		}
		decodedHash, err := hex.DecodeString(shard.SHA256)
		if err != nil || len(decodedHash) != sha256.Size {
			return fmt.Errorf("corpus manifest: shard %d has invalid SHA-256", index)
		}
		if _, err := safeShardPath(baseDir, shard.Path); err != nil {
			return err
		}
		documents += shard.Documents
		utf8Bytes += shard.UTF8Bytes
		jsonlBytes += shard.JSONLBytes
	}
	if documents != manifest.Documents || utf8Bytes != manifest.UTF8Bytes ||
		jsonlBytes != manifest.JSONLBytes {
		return fmt.Errorf("corpus manifest: total counters do not match shards")
	}
	return nil
}

func validateCorpusSources(sources []CorpusSourceDescriptor) error {
	seen := make(map[string]struct{}, len(sources))
	for index, source := range sources {
		if strings.TrimSpace(source.ID) == "" ||
			strings.TrimSpace(source.Name) == "" ||
			strings.TrimSpace(source.DatasetURL) == "" ||
			strings.TrimSpace(source.License) == "" ||
			strings.TrimSpace(source.LicenseURL) == "" ||
			strings.TrimSpace(source.Attribution) == "" ||
			strings.TrimSpace(source.Revision) == "" {
			return fmt.Errorf("corpus manifest: source %d has missing required fields", index)
		}
		if _, exists := seen[source.ID]; exists {
			return fmt.Errorf("corpus manifest: duplicate source id %q", source.ID)
		}
		seen[source.ID] = struct{}{}
		for inputIndex, input := range source.Inputs {
			if strings.TrimSpace(input.Path) == "" || input.FileBytes <= 0 {
				return fmt.Errorf(
					"corpus manifest: source %q input %d has invalid path or size",
					source.ID,
					inputIndex,
				)
			}
			decodedHash, err := hex.DecodeString(input.SHA256)
			if err != nil || len(decodedHash) != sha256.Size {
				return fmt.Errorf(
					"corpus manifest: source %q input %d has invalid SHA-256",
					source.ID,
					inputIndex,
				)
			}
		}
	}
	return nil
}

func safeShardPath(baseDir, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("corpus manifest: unsafe shard path %q", relative)
	}
	path := filepath.Join(baseDir, filepath.FromSlash(relative))
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve corpus shard %q: %w", relative, err)
	}
	rel, err := filepath.Rel(baseDir, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("corpus manifest: shard path %q escapes corpus directory", relative)
	}
	return absPath, nil
}

func openMaybeGzip(path, compression string) (io.Reader, func() error, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open corpus file %q: %w", path, err)
	}
	if compression == "none" {
		return file, file.Close, nil
	}
	if compression != "gzip" {
		file.Close()
		return nil, nil, fmt.Errorf("open corpus file: unsupported compression %q", compression)
	}
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		file.Close()
		return nil, nil, fmt.Errorf("open gzip corpus file %q: %w", path, err)
	}
	closeReader := func() error {
		gzipErr := gzipReader.Close()
		fileErr := file.Close()
		if gzipErr != nil {
			return gzipErr
		}
		return fileErr
	}
	return gzipReader, closeReader, nil
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open file for SHA-256 %q: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, fmt.Errorf("hash file %q: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

func writeJSONAtomic(path string, value any) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create JSON directory %q: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, ".tmp-*.json")
	if err != nil {
		return fmt.Errorf("create temporary JSON for %q: %w", path, err)
	}
	temporaryPath := temporary.Name()
	succeeded := false
	defer func() {
		if !succeeded {
			temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()

	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode JSON %q: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary JSON %q: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		// Windows does not replace an existing destination with os.Rename.
		if removeErr := os.Remove(path); removeErr != nil &&
			!errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("remove previous JSON %q: %w", path, removeErr)
		}
		if renameErr := os.Rename(temporaryPath, path); renameErr != nil {
			return fmt.Errorf("replace JSON %q: %w", path, renameErr)
		}
	}
	succeeded = true
	return nil
}
