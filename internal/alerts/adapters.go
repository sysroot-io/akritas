package alerts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	AdapterGeneric      = "generic"
	AdapterAlertmanager = "alertmanager"
	AdapterUptimeKuma   = "uptime-kuma"
	AdapterPingdom      = "pingdom"
)

type Adapter interface {
	Decode(body []byte, observedAt time.Time) ([]Event, error)
}

func NewAdapter(providerType string) (Adapter, error) {
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case AdapterGeneric:
		return genericAdapter{}, nil
	case AdapterAlertmanager:
		return alertmanagerAdapter{}, nil
	case AdapterUptimeKuma:
		return uptimeKumaAdapter{}, nil
	case AdapterPingdom:
		return pingdomAdapter{}, nil
	default:
		return nil, fmt.Errorf("unsupported alert source type %q", providerType)
	}
}

type genericAdapter struct{}

func (genericAdapter) Decode(body []byte, _ time.Time) ([]Event, error) {
	var batch Batch
	if err := decodeStrict(body, &batch); err != nil {
		return nil, fmt.Errorf("decode generic alert batch: %w", err)
	}
	if batch.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported generic alert schema version %d", batch.SchemaVersion)
	}
	if len(batch.Alerts) == 0 || len(batch.Alerts) > MaximumEventsPerBatch {
		return nil, fmt.Errorf("generic alert batch requires 1..%d alerts", MaximumEventsPerBatch)
	}
	return batch.Alerts, nil
}

type alertmanagerAdapter struct{}

type alertmanagerWebhook struct {
	Version            string                     `json:"version"`
	GroupKey           string                     `json:"groupKey"`
	TruncatedAlerts    int                        `json:"truncatedAlerts"`
	Status             string                     `json:"status"`
	Receiver           string                     `json:"receiver"`
	GroupLabels        map[string]string          `json:"groupLabels"`
	CommonLabels       map[string]string          `json:"commonLabels"`
	CommonAnnotations  map[string]string          `json:"commonAnnotations"`
	RouteLabels        map[string]string          `json:"routeLabels"`
	ExternalURL        string                     `json:"externalURL"`
	NotificationReason string                     `json:"notification_reason"`
	Alerts             []alertmanagerWebhookAlert `json:"alerts"`
}

type alertmanagerWebhookAlert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

func (alertmanagerAdapter) Decode(body []byte, observedAt time.Time) ([]Event, error) {
	var payload alertmanagerWebhook
	if err := decodeStrict(body, &payload); err != nil {
		return nil, fmt.Errorf("decode Alertmanager webhook: %w", err)
	}
	if payload.Version != "4" {
		return nil, fmt.Errorf("unsupported Alertmanager webhook version %q; expected version 4", payload.Version)
	}
	if strings.TrimSpace(payload.Receiver) == "" {
		return nil, fmt.Errorf("Alertmanager webhook requires receiver")
	}
	if payload.TruncatedAlerts < 0 || len(payload.Alerts) == 0 || len(payload.Alerts) > MaximumEventsPerBatch {
		return nil, fmt.Errorf("Alertmanager webhook requires 1..%d non-truncated alerts", MaximumEventsPerBatch)
	}
	events := make([]Event, 0, len(payload.Alerts))
	for index, alert := range payload.Alerts {
		state, err := normalizeState(alert.Status)
		if err != nil {
			return nil, fmt.Errorf("alerts[%d]: %w", index, err)
		}
		if len(alert.Labels) == 0 {
			return nil, fmt.Errorf("alerts[%d] requires labels", index)
		}
		startedAt, err := parseProviderTime(alert.StartsAt, observedAt)
		if err != nil {
			return nil, fmt.Errorf("alerts[%d].startsAt: %w", index, err)
		}
		endedAt, err := parseOptionalProviderTime(alert.EndsAt)
		if err != nil {
			return nil, fmt.Errorf("alerts[%d].endsAt: %w", index, err)
		}
		name := firstNonEmpty(alert.Labels["alertname"], payload.GroupLabels["alertname"], "AlertmanagerAlert")
		entity := alertmanagerEntity(alert.Labels, name)
		summary := firstNonEmpty(alert.Annotations["summary"], alert.Annotations["message"], payload.CommonAnnotations["summary"], name)
		description := firstNonEmpty(alert.Annotations["description"], payload.CommonAnnotations["description"])
		links := []Link{}
		if strings.TrimSpace(alert.GeneratorURL) != "" {
			links = append(links, Link{Type: "generator", URL: alert.GeneratorURL})
		}
		if strings.TrimSpace(payload.ExternalURL) != "" {
			links = append(links, Link{Type: "alertmanager", URL: payload.ExternalURL})
		}
		events = append(events, Event{
			Source: Source{ProviderEventID: strings.TrimSpace(alert.Fingerprint)}, State: state,
			Name: name, Severity: NormalizeSeverity(alert.Labels["severity"]), Summary: summary,
			Description: description, Entity: entity, StartedAt: startedAt, EndedAt: endedAt,
			ObservedAt: observedAt, Labels: mergeStringMaps(payload.CommonLabels, alert.Labels),
			Annotations: mergeStringMaps(payload.CommonAnnotations, alert.Annotations), Links: links,
			CorrelationKey: firstNonEmpty(alert.Fingerprint, payload.GroupKey),
		})
	}
	return events, nil
}

func alertmanagerEntity(labels map[string]string, fallback string) Entity {
	for _, candidate := range []struct{ label, kind string }{
		{"instance", "instance"}, {"host", "host"}, {"hostname", "host"},
		{"pod", "pod"}, {"service", "service"}, {"job", "job"},
	} {
		if value := strings.TrimSpace(labels[candidate.label]); value != "" {
			return Entity{Kind: candidate.kind, ID: value, DisplayName: value}
		}
	}
	return Entity{Kind: "alert", ID: fallback, DisplayName: fallback}
}

func normalizeState(value string) (State, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "firing", "triggered", "down", "failed", "alert", "open":
		return StateFiring, nil
	case "resolved", "up", "ok", "recovered", "closed":
		return StateResolved, nil
	default:
		return "", fmt.Errorf("invalid alert state %q", value)
	}
}

func parseProviderTime(value string, fallback time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback.UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("must be RFC3339: %w", err)
	}
	return parsed.UTC(), nil
}

func parseOptionalProviderTime(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "0001-01-01T") {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, fmt.Errorf("must be RFC3339: %w", err)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func mergeStringMaps(groups ...map[string]string) map[string]string {
	result := make(map[string]string)
	for _, group := range groups {
		for key, value := range group {
			result[key] = value
		}
	}
	return result
}

func decodeStrict(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func decodeProvider(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}
