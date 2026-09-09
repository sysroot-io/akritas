package alerts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type uptimeKumaAdapter struct{}

type uptimeKumaPayload struct {
	Heartbeat *struct {
		MonitorID json.Number `json:"monitorID"`
		Status    int         `json:"status"`
		Time      string      `json:"time"`
		Message   string      `json:"msg"`
		Ping      *float64    `json:"ping"`
		Important bool        `json:"important"`
		Duration  int         `json:"duration"`
	} `json:"heartbeat"`
	Monitor *struct {
		ID       json.Number `json:"id"`
		Name     string      `json:"name"`
		Type     string      `json:"type"`
		URL      string      `json:"url"`
		Hostname string      `json:"hostname"`
		Port     any         `json:"port"`
	} `json:"monitor"`
	Message string `json:"msg"`
}

func (uptimeKumaAdapter) Decode(body []byte, observedAt time.Time) ([]Event, error) {
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '"' {
		var encoded string
		if err := json.Unmarshal(body, &encoded); err != nil {
			return nil, fmt.Errorf("decode Uptime Kuma string payload: %w", err)
		}
		body = []byte(encoded)
	}
	var payload uptimeKumaPayload
	if err := decodeProvider(body, &payload); err != nil {
		return nil, fmt.Errorf("decode Uptime Kuma webhook: %w", err)
	}
	if payload.Heartbeat == nil || payload.Monitor == nil {
		return nil, fmt.Errorf("Uptime Kuma webhook requires heartbeat and monitor")
	}
	state := StateFiring
	severity := SeverityCritical
	switch payload.Heartbeat.Status {
	case 0:
	case 1:
		state, severity = StateResolved, SeverityInfo
	default:
		return nil, fmt.Errorf("unsupported Uptime Kuma heartbeat status %d", payload.Heartbeat.Status)
	}
	monitorID := firstNonEmpty(payload.Heartbeat.MonitorID.String(), payload.Monitor.ID.String())
	if monitorID == "" {
		return nil, fmt.Errorf("Uptime Kuma webhook requires monitor ID")
	}
	startedAt, err := parseUptimeKumaTime(payload.Heartbeat.Time, observedAt)
	if err != nil {
		return nil, err
	}
	name := firstNonEmpty(payload.Monitor.Name, "Uptime Kuma monitor "+monitorID)
	target := firstNonEmpty(payload.Monitor.Hostname, payload.Monitor.URL, name)
	summary := firstNonEmpty(payload.Message, payload.Heartbeat.Message, name)
	labels := map[string]string{
		"monitor_id": monitorID, "monitor_type": payload.Monitor.Type,
	}
	if payload.Heartbeat.Ping != nil {
		labels["ping_ms"] = strconv.FormatFloat(*payload.Heartbeat.Ping, 'f', -1, 64)
	}
	if payload.Heartbeat.Duration > 0 {
		labels["duration_seconds"] = strconv.Itoa(payload.Heartbeat.Duration)
	}
	links := []Link{}
	if strings.HasPrefix(payload.Monitor.URL, "http://") || strings.HasPrefix(payload.Monitor.URL, "https://") {
		links = append(links, Link{Type: "target", URL: payload.Monitor.URL})
	}
	return []Event{{
		Source: Source{ProviderEventID: monitorID}, State: state, Name: name, Severity: severity,
		Summary: summary, Description: payload.Heartbeat.Message,
		Entity:    Entity{Kind: "monitor", ID: monitorID, DisplayName: target},
		StartedAt: startedAt, ObservedAt: observedAt, Labels: labels, Links: links,
		CorrelationKey: monitorID,
	}}, nil
}

func parseUptimeKumaTime(value string, fallback time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback.UTC(), nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("Uptime Kuma heartbeat time must be ISO 8601")
}
