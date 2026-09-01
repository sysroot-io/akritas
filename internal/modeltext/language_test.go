package modeltext

import (
	"strings"
	"testing"
)

func TestNormalizeLanguageTag(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "en", want: "en"},
		{input: " RU ", want: "ru"},
		{input: "pt-BR", want: "pt-BR"},
		{input: "zh-Hant", want: "zh-Hant"},
		{input: "es-419", want: "es-419"},
		{input: "x-private", want: "x-private"},
	} {
		t.Run(test.input, func(t *testing.T) {
			got, err := NormalizeLanguageTag(test.input)
			if err != nil || got != test.want {
				t.Fatalf("NormalizeLanguageTag(%q)=%q, %v; want %q", test.input, got, err, test.want)
			}
		})
	}
}

func TestNormalizeLanguageTagRejectsInvalidValues(t *testing.T) {
	for _, input := range []string{"", "e", "en_US", "en--US", "en-123456789", "English language", "日本語"} {
		if _, err := NormalizeLanguageTag(input); err == nil {
			t.Fatalf("NormalizeLanguageTag(%q) succeeded", input)
		}
	}
}

func TestLanguageInstructionKeepsLanguageHostOwned(t *testing.T) {
	instruction := LanguageInstruction("pt-BR")
	for _, expected := range []string{"BCP 47 tag \"pt-BR\"", "Do not infer or change", "Preserve exact technical identifiers"} {
		if !strings.Contains(instruction, expected) {
			t.Fatalf("instruction is missing %q: %s", expected, instruction)
		}
	}
}
