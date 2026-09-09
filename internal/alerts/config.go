package alerts

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	maximumConfigBytes = 1024 * 1024
	maximumSources     = 32
	maximumSecretBytes = 8 * 1024
)

var (
	ErrUnknownSource       = errors.New("unknown alert source")
	ErrUnauthorized        = errors.New("alert source authentication failed")
	sourceNamePattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type Config struct {
	Version int            `json:"version"`
	Sources []SourceConfig `json:"sources"`
}

type SourceConfig struct {
	Name                 string `json:"name"`
	Type                 string `json:"type"`
	BearerTokenEnv       string `json:"bearer_token_env,omitempty"`
	HMACSecretEnv        string `json:"hmac_secret_env,omitempty"`
	SecretHeader         string `json:"secret_header,omitempty"`
	SecretEnv            string `json:"secret_env,omitempty"`
	AllowUnauthenticated bool   `json:"allow_unauthenticated,omitempty"`
}

type configuredSource struct {
	name         string
	providerType string
	adapter      Adapter
	bearerToken  string
	hmacSecret   string
	headerName   string
	headerValue  string
}

type Registry struct {
	sources map[string]configuredSource
}

func LoadRegistry(path string, lookup func(string) (string, bool)) (*Registry, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat alert source config %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumConfigBytes {
		return nil, fmt.Errorf("alert source config %q must be a regular file containing 1..%d bytes", path, maximumConfigBytes)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read alert source config %q: %w", path, err)
	}
	var config Config
	if err := decodeStrict(body, &config); err != nil {
		return nil, fmt.Errorf("decode alert source config %q: %w", path, err)
	}
	if config.Version != SchemaVersion {
		return nil, fmt.Errorf("unsupported alert source config version %d", config.Version)
	}
	if len(config.Sources) == 0 || len(config.Sources) > maximumSources {
		return nil, fmt.Errorf("alert source config requires 1..%d sources", maximumSources)
	}
	registry := &Registry{sources: make(map[string]configuredSource, len(config.Sources))}
	for index, item := range config.Sources {
		resolved, err := resolveSource(item, lookup)
		if err != nil {
			return nil, fmt.Errorf("sources[%d] %q: %w", index, item.Name, err)
		}
		if _, exists := registry.sources[resolved.name]; exists {
			return nil, fmt.Errorf("duplicate alert source %q", resolved.name)
		}
		registry.sources[resolved.name] = resolved
	}
	return registry, nil
}

func (registry *Registry) Len() int {
	if registry == nil {
		return 0
	}
	return len(registry.sources)
}

func (registry *Registry) Names() []string {
	if registry == nil {
		return nil
	}
	names := make([]string, 0, len(registry.sources))
	for name := range registry.sources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (registry *Registry) Decode(sourceName string, header http.Header, body []byte, observedAt time.Time) ([]Event, error) {
	if registry == nil {
		return nil, ErrUnknownSource
	}
	source, exists := registry.sources[strings.TrimSpace(sourceName)]
	if !exists {
		return nil, ErrUnknownSource
	}
	if err := source.authenticate(header, body); err != nil {
		return nil, err
	}
	decoded, err := source.adapter.Decode(body, observedAt.UTC())
	if err != nil {
		return nil, err
	}
	if len(decoded) == 0 || len(decoded) > MaximumEventsPerBatch {
		return nil, fmt.Errorf("adapter returned invalid alert count %d", len(decoded))
	}
	events := make([]Event, len(decoded))
	for index, event := range decoded {
		normalized, err := Normalize(event, source.providerType, source.name, observedAt)
		if err != nil {
			return nil, fmt.Errorf("alerts[%d]: %w", index, err)
		}
		events[index] = normalized
	}
	return events, nil
}

func DecodeWithAdapter(adapter Adapter, sourceType, sourceName string, body []byte, observedAt time.Time) ([]Event, error) {
	decoded, err := adapter.Decode(body, observedAt)
	if err != nil {
		return nil, err
	}
	events := make([]Event, len(decoded))
	for index, event := range decoded {
		events[index], err = Normalize(event, sourceType, sourceName, observedAt)
		if err != nil {
			return nil, fmt.Errorf("alerts[%d]: %w", index, err)
		}
	}
	return events, nil
}

func resolveSource(config SourceConfig, lookup func(string) (string, bool)) (configuredSource, error) {
	name := strings.TrimSpace(config.Name)
	providerType := strings.ToLower(strings.TrimSpace(config.Type))
	if !sourceNamePattern.MatchString(name) {
		return configuredSource{}, fmt.Errorf("invalid source name")
	}
	adapter, err := NewAdapter(providerType)
	if err != nil {
		return configuredSource{}, err
	}
	resolved := configuredSource{name: name, providerType: providerType, adapter: adapter}
	if config.BearerTokenEnv != "" {
		resolved.bearerToken, err = resolveSecret(config.BearerTokenEnv, lookup)
		if err != nil {
			return configuredSource{}, fmt.Errorf("bearer_token_env: %w", err)
		}
	}
	if config.HMACSecretEnv != "" {
		resolved.hmacSecret, err = resolveSecret(config.HMACSecretEnv, lookup)
		if err != nil {
			return configuredSource{}, fmt.Errorf("hmac_secret_env: %w", err)
		}
	}
	if config.SecretHeader != "" || config.SecretEnv != "" {
		if config.SecretHeader == "" || config.SecretEnv == "" {
			return configuredSource{}, fmt.Errorf("secret_header and secret_env must be configured together")
		}
		if !validHeaderName(config.SecretHeader) {
			return configuredSource{}, fmt.Errorf("invalid secret_header")
		}
		resolved.headerName = http.CanonicalHeaderKey(config.SecretHeader)
		resolved.headerValue, err = resolveSecret(config.SecretEnv, lookup)
		if err != nil {
			return configuredSource{}, fmt.Errorf("secret_env: %w", err)
		}
	}
	authConfigured := resolved.bearerToken != "" || resolved.hmacSecret != "" || resolved.headerValue != ""
	if config.AllowUnauthenticated == authConfigured {
		return configuredSource{}, fmt.Errorf("configure authentication or explicitly set allow_unauthenticated, but not both")
	}
	return resolved, nil
}

func (source configuredSource) authenticate(header http.Header, body []byte) error {
	if source.bearerToken != "" {
		provided := strings.TrimSpace(strings.TrimPrefix(header.Get("Authorization"), "Bearer "))
		if subtle.ConstantTimeCompare([]byte(source.bearerToken), []byte(provided)) != 1 {
			return ErrUnauthorized
		}
	}
	if source.headerValue != "" && subtle.ConstantTimeCompare([]byte(source.headerValue), []byte(header.Get(source.headerName))) != 1 {
		return ErrUnauthorized
	}
	if source.hmacSecret != "" {
		provided := strings.TrimSpace(strings.TrimPrefix(header.Get("X-Akritas-Signature"), "sha256="))
		decoded, err := hex.DecodeString(provided)
		if err != nil {
			return ErrUnauthorized
		}
		mac := hmac.New(sha256.New, []byte(source.hmacSecret))
		_, _ = mac.Write(body)
		if !hmac.Equal(mac.Sum(nil), decoded) {
			return ErrUnauthorized
		}
	}
	return nil
}

func resolveSecret(environment string, lookup func(string) (string, bool)) (string, error) {
	environment = strings.TrimSpace(environment)
	if !environmentNamePattern.MatchString(environment) {
		return "", fmt.Errorf("invalid environment variable name")
	}
	value, exists := lookup(environment)
	if !exists || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("environment variable %s is empty or missing", environment)
	}
	value = strings.TrimSpace(value)
	if len(value) > maximumSecretBytes {
		return "", fmt.Errorf("environment variable %s exceeds %d bytes", environment, maximumSecretBytes)
	}
	return value, nil
}

func validHeaderName(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	for _, item := range value {
		if !(item >= 'a' && item <= 'z') && !(item >= 'A' && item <= 'Z') && !(item >= '0' && item <= '9') && item != '-' {
			return false
		}
	}
	return true
}
