package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultLocalCorpusExtensions = ".txt,.md,.rst,.go,.c,.h,.cc,.cpp,.hpp,.rs,.java,.kt,.js,.jsx,.ts,.tsx,.cs,.php,.rb,.py,.sh,.sql,.json,.yaml,.yml,.toml,.xml"

func runImportLocalCorpus(arguments []string) {
	flags := flag.NewFlagSet("import-local", flag.ExitOnError)
	root := flags.String("input", "", "root directory recursively scanned for text files")
	output := flags.String("output", "data/corpus-local", "empty output directory")
	source := flags.String("source", "", "stable source name stored in every document")
	license := flags.String("license", "", "license or ownership declaration stored in every document")
	extensions := flags.String("extensions", defaultLocalCorpusExtensions, "comma-separated extensions, or * for every UTF-8 file")
	excludedDirs := flags.String("exclude-dirs", ".git,.hg,.svn,node_modules,vendor,dist,build,.cache", "comma-separated directory names skipped recursively")
	minimumRunes := flags.Int("minimum-runes", 100, "skip normalized documents shorter than this")
	maximumFileMB := flags.Int64("maximum-file-mb", 16, "skip individual files larger than this")
	maximumDocuments := flags.Int("max-documents", 0, "maximum accepted documents; 0 means unlimited")
	validationPercent := flags.Float64("validation-percent", 2, "deterministic held-out percentage in [0,100)")
	shardMB := flags.Int64("shard-mb", 64, "maximum uncompressed JSONL MiB per shard")
	useGzip := flags.Bool("gzip", true, "compress shards with gzip")
	_ = flags.Parse(arguments)

	if strings.TrimSpace(*root) == "" || strings.TrimSpace(*source) == "" ||
		strings.TrimSpace(*license) == "" {
		panic("import-local requires -input, -source and -license")
	}
	if *maximumFileMB <= 0 || *shardMB <= 0 || *validationPercent < 0 ||
		*validationPercent >= 100 {
		panic("maximum-file-mb and shard-mb must be positive; validation-percent must be in [0,100)")
	}
	result, err := ImportLocalCorpus(LocalCorpusImportOptions{
		Root:               *root,
		OutputDir:          *output,
		Source:             strings.TrimSpace(*source),
		License:            strings.TrimSpace(*license),
		Extensions:         splitCommaSeparated(*extensions),
		ExcludedDirs:       splitCommaSeparated(*excludedDirs),
		MinimumRunes:       *minimumRunes,
		MaximumFileBytes:   *maximumFileMB * 1024 * 1024,
		MaximumDocuments:   *maximumDocuments,
		ValidationFraction: *validationPercent / 100,
		ShardBytes:         *shardMB * 1024 * 1024,
		Gzip:               *useGzip,
	})
	if err != nil {
		panic(err)
	}
	stats := result.Stats
	fmt.Printf(
		"Local corpus imported: discovered=%d accepted=%d train=%d validation=%d duplicates=%d skipped_extension=%d skipped_large=%d skipped_utf8=%d skipped_binary=%d skipped_short=%d skipped_limit=%d\n",
		stats.FilesDiscovered,
		stats.DocumentsAccepted,
		stats.TrainingDocuments,
		stats.ValidationDocuments,
		stats.DuplicateDocuments,
		stats.SkippedExtension,
		stats.SkippedTooLarge,
		stats.SkippedInvalidUTF8,
		stats.SkippedBinary,
		stats.SkippedTooShort,
		stats.SkippedDocumentLimit,
	)
	fmt.Printf("Train manifest: %s\n", result.TrainManifestPath)
	if result.ValidationManifestPath != "" {
		fmt.Printf("Validation manifest: %s\n", result.ValidationManifestPath)
	}
}

func splitCommaSeparated(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func runBuildCorpus(arguments []string) {
	flags := flag.NewFlagSet("build-corpus", flag.ExitOnError)
	var textPaths repeatedStringFlag
	var jsonlPaths repeatedStringFlag
	flags.Var(&textPaths, "text", "UTF-8 text file stored as one document; may be repeated")
	flags.Var(&jsonlPaths, "jsonl", "document JSONL or JSONL.GZ input; may be repeated")
	outputDir := flags.String("output", "data/corpus", "empty output directory")
	shardMB := flags.Int64("shard-mb", 64, "maximum uncompressed JSONL MiB per shard")
	useGzip := flags.Bool("gzip", true, "compress shards with gzip")
	_ = flags.Parse(arguments)

	if len(textPaths) == 0 && len(jsonlPaths) == 0 {
		panic("build-corpus requires at least one -text or -jsonl input")
	}
	if *shardMB <= 0 {
		panic("shard-mb must be positive")
	}

	writer, err := NewCorpusWriter(*outputDir, *shardMB*1024*1024, *useGzip)
	if err != nil {
		panic(err)
	}
	for _, path := range textPaths {
		raw, err := os.ReadFile(path)
		if err != nil {
			panic(fmt.Errorf("read corpus text %q: %w", path, err))
		}
		document := CorpusDocument{
			ID:     filepath.ToSlash(path),
			Text:   string(raw),
			Source: "local-text",
		}
		if err := writer.Add(document); err != nil {
			panic(err)
		}
	}
	for _, path := range jsonlPaths {
		if err := ReadCorpusJSONL(path, writer.Add); err != nil {
			panic(err)
		}
	}

	manifestPath, manifest, err := writer.Close()
	if err != nil {
		panic(err)
	}
	fmt.Printf(
		"Corpus built: documents=%d utf8_bytes=%d jsonl_bytes=%d shards=%d compression=%s\n",
		manifest.Documents,
		manifest.UTF8Bytes,
		manifest.JSONLBytes,
		len(manifest.Shards),
		manifest.Compression,
	)
	fmt.Printf("Manifest saved: %s\n", manifestPath)
}

func runInspectCorpus(arguments []string) {
	flags := flag.NewFlagSet("inspect-corpus", flag.ExitOnError)
	manifestPath := flags.String("manifest", "data/corpus/manifest.json", "corpus manifest")
	verify := flags.Bool("verify", true, "verify every shard SHA-256 and byte size")
	sampleCount := flags.Int("samples", 3, "number of document previews")
	qualityEnabled := flags.Bool("quality", true, "report table/pipe/control-character quality metrics")
	maxTableLines := flags.Int64("max-table-lines", -1, "fail above this table-like line count; -1 disables")
	maxPipesPerMillion := flags.Float64("max-pipes-per-million", -1, "fail above this pipe/rune rate; -1 disables")
	maxReplacementRunes := flags.Int64("max-replacement-runes", -1, "fail above this replacement-rune count; -1 disables")
	maxControlRunes := flags.Int64("max-control-runes", -1, "fail above this control-rune count; -1 disables")
	_ = flags.Parse(arguments)
	if *sampleCount < 0 || *maxTableLines < -1 || *maxPipesPerMillion < -1 ||
		*maxReplacementRunes < -1 || *maxControlRunes < -1 {
		panic("samples must not be negative; quality limits must be -1 or non-negative")
	}

	reader, err := OpenCorpus(*manifestPath)
	if err != nil {
		panic(err)
	}
	samples := make([]CorpusDocument, 0, *sampleCount)
	documents := 0
	textBytes := int64(0)
	quality := CorpusQualityStats{}
	err = reader.Iterate(reader.StartCursor(), *verify, func(
		document CorpusDocument,
		_ CorpusCursor,
	) error {
		documents++
		textBytes += int64(len([]byte(document.Text)))
		if *qualityEnabled {
			quality.Observe(document)
		}
		if len(samples) < *sampleCount {
			samples = append(samples, document)
		}
		return nil
	})
	if err != nil {
		panic(err)
	}

	fmt.Printf(
		"Corpus OK: documents=%d utf8_bytes=%d shards=%d compression=%s manifest_sha256=%s\n",
		documents,
		textBytes,
		len(reader.Manifest.Shards),
		reader.Manifest.Compression,
		reader.ManifestSHA256,
	)
	if *qualityEnabled {
		pipeRate := quality.PipesPerMillionRunes()
		fmt.Printf(
			"Quality: runes=%d lines=%d table_like_lines=%d pipe_chars=%d pipes_per_million=%.2f replacement_runes=%d control_runes=%d\n",
			quality.Runes,
			quality.Lines,
			quality.TableLikeLines,
			quality.PipeCharacters,
			pipeRate,
			quality.ReplacementRune,
			quality.ControlRunes,
		)
		if *maxTableLines >= 0 && quality.TableLikeLines > *maxTableLines {
			panic(fmt.Sprintf(
				"corpus quality: table-like lines %d exceed limit %d",
				quality.TableLikeLines,
				*maxTableLines,
			))
		}
		if *maxPipesPerMillion >= 0 && pipeRate > *maxPipesPerMillion {
			panic(fmt.Sprintf(
				"corpus quality: pipes per million %.2f exceed limit %.2f",
				pipeRate,
				*maxPipesPerMillion,
			))
		}
		if *maxReplacementRunes >= 0 && quality.ReplacementRune > *maxReplacementRunes {
			panic(fmt.Sprintf(
				"corpus quality: replacement runes %d exceed limit %d",
				quality.ReplacementRune,
				*maxReplacementRunes,
			))
		}
		if *maxControlRunes >= 0 && quality.ControlRunes > *maxControlRunes {
			panic(fmt.Sprintf(
				"corpus quality: control runes %d exceed limit %d",
				quality.ControlRunes,
				*maxControlRunes,
			))
		}
	}
	for index, document := range samples {
		preview := strings.Join(strings.Fields(document.Text), " ")
		runes := []rune(preview)
		if len(runes) > 120 {
			preview = string(runes[:120]) + "…"
		}
		fmt.Printf(
			"sample[%d] id=%q source=%q text=%q\n",
			index,
			document.ID,
			document.Source,
			preview,
		)
	}
}
