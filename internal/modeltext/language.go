package modeltext

import (
	"fmt"
	"strings"
)

const DefaultLanguage = "en"

// NormalizeLanguageTag validates a practical BCP 47 language tag for use in
// host-owned model instructions. It accepts common tags such as en, ru,
// pt-BR, zh-Hant, and es-419 without maintaining a fixed language list.
func NormalizeLanguageTag(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 63 {
		return "", fmt.Errorf("response language must be a BCP 47 tag of at most 63 characters")
	}
	parts := strings.Split(value, "-")
	if !validPrimaryLanguageSubtag(parts[0]) {
		return "", fmt.Errorf("response language %q has an invalid primary subtag", value)
	}
	for _, part := range parts[1:] {
		if len(part) < 1 || len(part) > 8 || !asciiAlphaNumeric(part) {
			return "", fmt.Errorf("response language %q has an invalid subtag", value)
		}
	}
	parts[0] = strings.ToLower(parts[0])
	return strings.Join(parts, "-"), nil
}

func LanguageInstruction(language string) string {
	return fmt.Sprintf(
		"Write all model-generated human-readable prose in the language identified by BCP 47 tag %q. "+
			"Do not infer or change the response language based on the user's input language. "+
			"Preserve exact technical identifiers, code, commands, paths, hostnames, metric names, "+
			"label names and values, and evidence references.",
		language,
	)
}

func validPrimaryLanguageSubtag(value string) bool {
	if value == "x" || value == "X" {
		return true
	}
	return len(value) >= 2 && len(value) <= 8 && asciiLetters(value)
}

func asciiLetters(value string) bool {
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') {
			return false
		}
	}
	return true
}

func asciiAlphaNumeric(value string) bool {
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') {
			return false
		}
	}
	return true
}
