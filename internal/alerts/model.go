package alerts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	SchemaVersion          = 1
	MaximumEventsPerBatch  = 128
	maximumLabels          = 64
	maximumLinks           = 16
	maximumRelatedEntities = 32
)

type State string

const (
	StateFiring   State = "firing"
	StateResolved State = "resolved"
)

type Severity string

const (
	SeverityUnknown  Severity = "unknown"
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type Source struct {
	Type            string `json:"type"`
	Name            string `json:"name"`
	ProviderEventID string `json:"provider_event_id,omitempty"`
}

type Entity struct {
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

type Link struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// Event is the provider-independent alert contract consumed by correlation
// and investigation. Provider-controlled strings remain untrusted data.
type Event struct {
	SchemaVersion    int               `json:"schema_version"`
	ID               string            `json:"event_id"`
	Source           Source            `json:"source"`
	State            State             `json:"state"`
	Name             string            `json:"name"`
	Severity         Severity          `json:"severity"`
	Summary          string            `json:"summary"`
	Description      string            `json:"description,omitempty"`
	Entity           Entity            `json:"entity"`
	RelatedEntities  []Entity          `json:"related_entities,omitempty"`
	StartedAt        time.Time         `json:"started_at"`
	EndedAt          *time.Time        `json:"ended_at,omitempty"`
	ObservedAt       time.Time         `json:"observed_at"`
	Labels           map[string]string `json:"labels,omitempty"`
	Annotations      map[string]string `json:"annotations,omitempty"`
	Links            []Link            `json:"links,omitempty"`
	DeduplicationKey string            `json:"deduplication_key"`
	CorrelationKey   string            `json:"correlation_key"`
}

type Batch struct {
	SchemaVersion int     `json:"schema_version"`
	Alerts        []Event `json:"alerts"`
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)

func Normalize(event Event, sourceType, sourceName string, now time.Time) (Event, error) {
	event.SchemaVersion = SchemaVersion
	event.ID = ""
	event.Source.Type = strings.ToLower(strings.TrimSpace(sourceType))
	event.Source.Name = strings.TrimSpace(sourceName)
	event.Source.ProviderEventID = bounded(event.Source.ProviderEventID, 512)
	event.Name = bounded(event.Name, 256)
	event.Summary = bounded(event.Summary, 4096)
	event.Description = bounded(event.Description, 8192)
	event.Entity = normalizeEntity(event.Entity)
	for index := range event.RelatedEntities {
		event.RelatedEntities[index] = normalizeEntity(event.RelatedEntities[index])
	}
	event.RelatedEntities = deduplicateEntities(event.RelatedEntities)
	event.Labels = normalizeMap(event.Labels)
	event.Annotations = normalizeMap(event.Annotations)
	event.Links = normalizeLinks(event.Links)
	event.DeduplicationKey = bounded(event.DeduplicationKey, 512)
	event.CorrelationKey = bounded(event.CorrelationKey, 512)
	if event.ObservedAt.IsZero() {
		event.ObservedAt = now.UTC()
	} else {
		event.ObservedAt = event.ObservedAt.UTC()
	}
	if event.StartedAt.IsZero() {
		event.StartedAt = event.ObservedAt
	} else {
		event.StartedAt = event.StartedAt.UTC()
	}
	if event.EndedAt != nil {
		ended := event.EndedAt.UTC()
		event.EndedAt = &ended
	}
	if event.Severity == "" {
		event.Severity = SeverityUnknown
	}
	if event.CorrelationKey == "" {
		event.CorrelationKey = deriveCorrelationKey(event)
	}
	if event.DeduplicationKey == "" {
		event.DeduplicationKey = deriveDeduplicationKey(event)
	}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

func (event Event) Validate() error {
	if event.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported alert schema version %d", event.SchemaVersion)
	}
	if !identifierPattern.MatchString(event.Source.Type) || !identifierPattern.MatchString(event.Source.Name) {
		return fmt.Errorf("alert source type and name must be stable identifiers")
	}
	if event.State != StateFiring && event.State != StateResolved {
		return fmt.Errorf("invalid alert state %q", event.State)
	}
	if event.Severity != SeverityUnknown && event.Severity != SeverityInfo && event.Severity != SeverityWarning && event.Severity != SeverityCritical {
		return fmt.Errorf("invalid alert severity %q", event.Severity)
	}
	if event.Name == "" || event.Summary == "" {
		return fmt.Errorf("alert name and summary are required")
	}
	if event.Entity.Kind == "" || event.Entity.ID == "" {
		return fmt.Errorf("alert entity kind and id are required")
	}
	if !identifierPattern.MatchString(event.Entity.Kind) {
		return fmt.Errorf("alert entity kind must be a stable identifier")
	}
	if event.StartedAt.IsZero() || event.ObservedAt.IsZero() {
		return fmt.Errorf("alert started_at and observed_at are required")
	}
	if event.EndedAt != nil && event.EndedAt.Before(event.StartedAt) {
		return fmt.Errorf("alert ended_at must not precede started_at")
	}
	if len(event.RelatedEntities) > maximumRelatedEntities || len(event.Labels) > maximumLabels ||
		len(event.Annotations) > maximumLabels || len(event.Links) > maximumLinks {
		return fmt.Errorf("alert collections exceed canonical limits")
	}
	if event.DeduplicationKey == "" || event.CorrelationKey == "" {
		return fmt.Errorf("alert deduplication and correlation keys are required")
	}
	return nil
}

func NormalizeSeverity(value string) Severity {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "critical", "crit", "emergency", "fatal", "down", "error":
		return SeverityCritical
	case "warning", "warn", "degraded", "minor":
		return SeverityWarning
	case "info", "informational", "notice", "up", "ok":
		return SeverityInfo
	default:
		return SeverityUnknown
	}
}

func normalizeEntity(entity Entity) Entity {
	entity.Kind = strings.ToLower(bounded(entity.Kind, 64))
	entity.ID = bounded(entity.ID, 512)
	entity.DisplayName = bounded(entity.DisplayName, 512)
	return entity
}

func deduplicateEntities(values []Entity) []Entity {
	if len(values) > maximumRelatedEntities {
		values = values[:maximumRelatedEntities]
	}
	seen := make(map[string]bool, len(values))
	result := make([]Entity, 0, len(values))
	for _, value := range values {
		if value.Kind == "" || value.ID == "" {
			continue
		}
		key := value.Kind + "\x00" + value.ID
		if !seen[key] {
			seen[key] = true
			result = append(result, value)
		}
	}
	return result
}

func normalizeMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > maximumLabels {
		keys = keys[:maximumLabels]
	}
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		normalizedKey := strings.ToLower(bounded(key, 128))
		if normalizedKey != "" {
			result[normalizedKey] = bounded(input[key], 2048)
		}
	}
	return result
}

func normalizeLinks(input []Link) []Link {
	if len(input) > maximumLinks {
		input = input[:maximumLinks]
	}
	result := make([]Link, 0, len(input))
	for _, item := range input {
		item.Type = strings.ToLower(bounded(item.Type, 64))
		item.URL = bounded(item.URL, 4096)
		parsed, err := url.Parse(item.URL)
		if item.Type == "" || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			continue
		}
		result = append(result, item)
	}
	return result
}

func deriveCorrelationKey(event Event) string {
	identity := event.Source.ProviderEventID
	if identity == "" {
		identity = event.Name + "\x00" + event.Entity.Kind + "\x00" + event.Entity.ID
	}
	return digest("correlation", event.Source.Type, event.Source.Name, identity)
}

func deriveDeduplicationKey(event Event) string {
	ended := ""
	if event.EndedAt != nil {
		ended = event.EndedAt.Format(time.RFC3339Nano)
	}
	return digest(
		"event", event.Source.Type, event.Source.Name, event.Source.ProviderEventID,
		string(event.State), event.Name, event.Entity.Kind, event.Entity.ID,
		event.StartedAt.Format(time.RFC3339Nano), ended, event.Summary,
	)
}

func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func bounded(value string, maximumRunes int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= maximumRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximumRunes])
}
