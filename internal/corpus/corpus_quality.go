package corpus

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// isTableLikeLine detects common MediaWiki table remnants without importing
// the unrelated wiki ingestion pipeline into Akritas.
func isTableLikeLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	pipeCount := strings.Count(line, "|")
	if strings.HasPrefix(line, "{|") || strings.HasPrefix(line, "|-") ||
		strings.HasPrefix(line, "|}") {
		return true
	}
	if strings.HasPrefix(line, "!") &&
		(strings.Contains(line, "!!") || pipeCount > 0) {
		return true
	}
	return pipeCount >= 3
}

// CorpusQualityStats measures common extraction artifacts without making
// language-specific claims about document quality. Counts are deterministic
// and can be compared before and after rebuilding a corpus.
type CorpusQualityStats struct {
	Documents       int
	Runes           int64
	Lines           int64
	PipeCharacters  int64
	TableLikeLines  int64
	ReplacementRune int64
	ControlRunes    int64
}

func (s *CorpusQualityStats) Observe(document CorpusDocument) {
	s.Documents++
	s.Runes += int64(utf8.RuneCountInString(document.Text))
	for _, current := range document.Text {
		switch {
		case current == '\uFFFD':
			s.ReplacementRune++
		case unicode.IsControl(current) && current != '\n' &&
			current != '\r' && current != '\t':
			s.ControlRunes++
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(document.Text, "\r\n", "\n"), "\n") {
		s.Lines++
		s.PipeCharacters += int64(strings.Count(line, "|"))
		if isTableLikeLine(line) {
			s.TableLikeLines++
		}
	}
}

func (s CorpusQualityStats) PipesPerMillionRunes() float64 {
	if s.Runes == 0 {
		return 0
	}
	return float64(s.PipeCharacters) * 1_000_000 / float64(s.Runes)
}
