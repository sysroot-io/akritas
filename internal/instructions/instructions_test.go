package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTrimsValidInstructions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SYSTEM.md")
	if err := os.WriteFile(path, []byte("\n  global rule  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if value != "global rule" {
		t.Fatalf("Load() = %q", value)
	}
}

func TestLoadRejectsMissingEmptyAndOversizedInstructions(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{name: "empty", content: []byte(" \n\t"), want: "is empty"},
		{name: "oversized", content: []byte(strings.Repeat("x", MaximumBytes+1)), want: "exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "SYSTEM.md")
			if err := os.WriteFile(path, test.content, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want substring %q", err, test.want)
			}
		})
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.md")); err == nil {
		t.Fatal("Load() accepted a missing file")
	}
}
