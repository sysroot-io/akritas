package instructions

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	DefaultPath  = "instructions/SYSTEM.md"
	MaximumBytes = 64 * 1024
)

// Load reads the global model instructions configured by the operator.
func Load(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("system instructions path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat system instructions %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("system instructions %q is not a regular file", path)
	}
	if info.Size() > MaximumBytes {
		return "", fmt.Errorf("system instructions %q exceeds %d bytes", path, MaximumBytes)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read system instructions %q: %w", path, err)
	}
	if len(content) > MaximumBytes {
		return "", fmt.Errorf("system instructions %q exceeds %d bytes", path, MaximumBytes)
	}
	if !utf8.Valid(content) {
		return "", fmt.Errorf("system instructions %q is not valid UTF-8", path)
	}
	value := strings.TrimSpace(string(content))
	if value == "" {
		return "", fmt.Errorf("system instructions %q is empty", path)
	}
	return value, nil
}
