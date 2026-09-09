package alerts

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type pingdomAdapter struct{}

type pingdomPayload struct {
	CheckID     json.Number `json:"check_id"`
	CheckName   string      `json:"check_name"`
	CheckType   string      `json:"check_type"`
	CheckParams struct {
		FullURL  string `json:"full_url"`
		Hostname string `json:"hostname"`
		Port     int    `json:"port"`
	} `json:"check_params"`
	Tags                  []string `json:"tags"`
	PreviousState         string   `json:"previous_state"`
	CurrentState          string   `json:"current_state"`
	ImportanceLevel       string   `json:"importance_level"`
	StateChangedTimestamp int64    `json:"state_changed_timestamp"`
	StateChangedUTCTime   string   `json:"state_changed_utc_time"`
	LongDescription       string   `json:"long_description"`
	Description           string   `json:"description"`
}

func (pingdomAdapter) Decode(body []byte, observedAt time.Time) ([]Event, error) {
	var payload pingdomPayload
	if err := decodeProvider(body, &payload); err != nil {
		return nil, fmt.Errorf("decode Pingdom webhook: %w", err)
	}
	checkID := strings.TrimSpace(payload.CheckID.String())
	if checkID == "" || strings.TrimSpace(payload.CheckName) == "" {
		return nil, fmt.Errorf("Pingdom webhook requires check_id and check_name")
	}
	state, err := normalizeState(payload.CurrentState)
	if err != nil {
		return nil, fmt.Errorf("Pingdom current_state: %w", err)
	}
	changedAt, err := parsePingdomTime(payload, observedAt)
	if err != nil {
		return nil, err
	}
	severity := NormalizeSeverity(payload.ImportanceLevel)
	if state == StateResolved {
		severity = SeverityInfo
	} else if severity == SeverityUnknown {
		severity = SeverityWarning
	}
	target := firstNonEmpty(payload.CheckParams.Hostname, payload.CheckParams.FullURL, payload.CheckName)
	summary := fmt.Sprintf("%s is %s", strings.TrimSpace(payload.CheckName), strings.ToLower(strings.TrimSpace(payload.CurrentState)))
	if strings.TrimSpace(payload.Description) != "" {
		summary += ": " + strings.TrimSpace(payload.Description)
	}
	labels := map[string]string{
		"check_id": checkID, "check_type": payload.CheckType,
		"previous_state": payload.PreviousState, "current_state": payload.CurrentState,
	}
	if payload.CheckParams.Port > 0 {
		labels["port"] = strconv.Itoa(payload.CheckParams.Port)
	}
	if len(payload.Tags) > 0 {
		labels["tags"] = strings.Join(payload.Tags, ",")
	}
	links := []Link{}
	if strings.HasPrefix(payload.CheckParams.FullURL, "http://") || strings.HasPrefix(payload.CheckParams.FullURL, "https://") {
		links = append(links, Link{Type: "target", URL: payload.CheckParams.FullURL})
	}
	return []Event{{
		Source: Source{ProviderEventID: checkID}, State: state, Name: payload.CheckName,
		Severity: severity, Summary: summary, Description: payload.LongDescription,
		Entity:    Entity{Kind: "check", ID: checkID, DisplayName: target},
		StartedAt: changedAt, ObservedAt: observedAt, Labels: labels, Links: links,
		CorrelationKey: checkID,
	}}, nil
}

func parsePingdomTime(payload pingdomPayload, fallback time.Time) (time.Time, error) {
	if payload.StateChangedTimestamp > 0 {
		return time.Unix(payload.StateChangedTimestamp, 0).UTC(), nil
	}
	value := strings.TrimSpace(payload.StateChangedUTCTime)
	if value == "" {
		return fallback.UTC(), nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("Pingdom state change time must be ISO 8601")
}
