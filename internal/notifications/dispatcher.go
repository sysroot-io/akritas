package notifications

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"akritas/internal/investigation"
	"akritas/internal/runbudget"
)

const (
	EventIncidentInvestigated = "incident.investigated"
	maximumWebhookBytes       = 512 * 1024
	maximumResponseBytes      = 64 * 1024
	maximumTelegramRunes      = 4000
	maximumMattermostRunes    = 16000
)

type Incident struct {
	RunID          string               `json:"run_id,omitempty"`
	IncidentID     string               `json:"incident_id,omitempty"`
	RunURL         string               `json:"run_url,omitempty"`
	Model          string               `json:"model"`
	AlertStatus    string               `json:"alert_status"`
	AlertName      string               `json:"alert_name,omitempty"`
	Source         string               `json:"source,omitempty"`
	Entity         string               `json:"entity,omitempty"`
	GroupKey       string               `json:"group_key"`
	Answer         string               `json:"answer"`
	Skills         []string             `json:"skills,omitempty"`
	Activity       []ToolActivity       `json:"activity"`
	CapabilityGaps []CapabilityGap      `json:"capability_gaps"`
	Investigation  investigation.Result `json:"investigation"`
	Budget         runbudget.Snapshot   `json:"budget"`
}

type ToolActivity struct {
	Name       string `json:"name"`
	ID         string `json:"id"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
}

type CapabilityGap struct {
	Step       string `json:"step"`
	Capability string `json:"capability"`
	Reason     string `json:"reason"`
}

type WebhookEnvelope struct {
	Version    int      `json:"version"`
	Event      string   `json:"event"`
	DeliveryID string   `json:"delivery_id"`
	CreatedAt  string   `json:"created_at"`
	Incident   Incident `json:"incident"`
}

type DeliveryResult struct {
	Receiver   string `json:"receiver"`
	Type       string `json:"type"`
	DeliveryID string `json:"delivery_id"`
	Status     string `json:"status"`
	StatusCode int    `json:"status_code,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
}

type receiver interface {
	Name() string
	Type() string
	Deliver(context.Context, *http.Client, WebhookEnvelope) (int, string)
}

type Dispatcher struct {
	timeout   time.Duration
	receivers []receiver
	client    *http.Client
	bot       BotSettings
}

func newDispatcher(config resolvedConfig, client *http.Client) *Dispatcher {
	if client == nil {
		client = &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	return &Dispatcher{timeout: config.timeout, receivers: config.receivers, client: client, bot: config.bot}
}

func (dispatcher *Dispatcher) Len() int {
	if dispatcher == nil {
		return 0
	}
	return len(dispatcher.receivers)
}

func (dispatcher *Dispatcher) Deliver(ctx context.Context, incident Incident) []DeliveryResult {
	if dispatcher == nil || len(dispatcher.receivers) == 0 {
		return nil
	}
	results := make([]DeliveryResult, len(dispatcher.receivers))
	var wait sync.WaitGroup
	for index, configured := range dispatcher.receivers {
		wait.Add(1)
		go func(index int, configured receiver) {
			defer wait.Done()
			deliveryID := newDeliveryID()
			envelope := WebhookEnvelope{
				Version: 1, Event: EventIncidentInvestigated, DeliveryID: deliveryID,
				CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Incident: incident,
			}
			deliveryContext, cancel := context.WithTimeout(ctx, dispatcher.timeout)
			defer cancel()
			statusCode, errorCode := configured.Deliver(deliveryContext, dispatcher.client, envelope)
			status := "delivered"
			if errorCode != "" {
				status = "failed"
			}
			results[index] = DeliveryResult{
				Receiver: configured.Name(), Type: configured.Type(), DeliveryID: deliveryID,
				Status: status, StatusCode: statusCode, ErrorCode: errorCode,
			}
		}(index, configured)
	}
	wait.Wait()
	return results
}

type webhookReceiver struct {
	name        string
	endpoint    string
	bearerToken string
	hmacSecret  string
}

func (receiver *webhookReceiver) Name() string { return receiver.name }
func (receiver *webhookReceiver) Type() string { return "webhook" }

func (receiver *webhookReceiver) Deliver(ctx context.Context, client *http.Client, envelope WebhookEnvelope) (int, string) {
	body, err := json.Marshal(envelope)
	if err != nil || len(body) > maximumWebhookBytes {
		return 0, "encode_payload"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, receiver.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, "create_request"
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Akritas/incident-webhook")
	request.Header.Set("X-Akritas-Event", envelope.Event)
	request.Header.Set("X-Akritas-Delivery", envelope.DeliveryID)
	if envelope.Incident.RunID != "" {
		request.Header.Set("X-Akritas-Run-ID", envelope.Incident.RunID)
	}
	if receiver.bearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+receiver.bearerToken)
	}
	if receiver.hmacSecret != "" {
		mac := hmac.New(sha256.New, []byte(receiver.hmacSecret))
		_, _ = mac.Write(body)
		request.Header.Set("X-Akritas-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	return performRequest(client, request, false)
}

type telegramReceiver struct {
	name            string
	baseURL         string
	botToken        string
	chatID          string
	messageThreadID int64
	access          botAccess
}

func (receiver *telegramReceiver) Name() string { return receiver.name }
func (receiver *telegramReceiver) Type() string { return "telegram" }

func (receiver *telegramReceiver) Deliver(ctx context.Context, client *http.Client, envelope WebhookEnvelope) (int, string) {
	payload := map[string]any{
		"chat_id": receiver.chatID,
		"text":    truncateRunes(formatIncidentMessage(envelope.Incident), maximumTelegramRunes),
	}
	if receiver.messageThreadID > 0 {
		payload["message_thread_id"] = receiver.messageThreadID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, "encode_payload"
	}
	endpoint := receiver.baseURL + "/bot" + receiver.botToken + "/sendMessage"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, "create_request"
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Akritas/telegram")
	return performRequest(client, request, true)
}

type mattermostReceiver struct {
	name      string
	baseURL   string
	botToken  string
	channelID string
	access    botAccess
}

func (receiver *mattermostReceiver) Name() string { return receiver.name }
func (receiver *mattermostReceiver) Type() string { return "mattermost" }

func (receiver *mattermostReceiver) Deliver(ctx context.Context, client *http.Client, envelope WebhookEnvelope) (int, string) {
	body, err := json.Marshal(map[string]any{
		"channel_id": receiver.channelID,
		"message":    truncateRunes(escapeMattermostMarkdown(formatIncidentMessage(envelope.Incident)), maximumMattermostRunes),
	})
	if err != nil {
		return 0, "encode_payload"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, receiver.baseURL+"/api/v4/posts", bytes.NewReader(body))
	if err != nil {
		return 0, "create_request"
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+receiver.botToken)
	request.Header.Set("User-Agent", "Akritas/mattermost")
	return performRequest(client, request, false)
}

func performRequest(client *http.Client, request *http.Request, requireOK bool) (int, string) {
	response, err := client.Do(request)
	if err != nil {
		if request.Context().Err() == context.DeadlineExceeded {
			return 0, "timeout"
		}
		if request.Context().Err() == context.Canceled {
			return 0, "canceled"
		}
		return 0, "request_failed"
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumResponseBytes))
		return response.StatusCode, "http_status"
	}
	if !requireOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumResponseBytes))
		return response.StatusCode, ""
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil {
		return response.StatusCode, "read_response"
	}
	if len(body) > maximumResponseBytes {
		return response.StatusCode, "response_too_large"
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(body, &result); err != nil || !result.OK {
		return response.StatusCode, "remote_rejected"
	}
	return response.StatusCode, ""
}

func formatIncidentMessage(incident Incident) string {
	var message strings.Builder
	fmt.Fprintf(&message, "Akritas incident investigation\nAlert status: %s\nFinding: %s\nActionability: %s\nConfidence: %s\n",
		safeChatText(incident.AlertStatus), incident.Investigation.FindingStatus,
		incident.Investigation.Actionability, incident.Investigation.Confidence,
	)
	if incident.RunID != "" {
		fmt.Fprintf(&message, "Run ID: %s\n", safeChatText(incident.RunID))
	}
	if incident.AlertName != "" {
		fmt.Fprintf(&message, "Alert: %s\n", safeChatText(incident.AlertName))
	}
	if incident.Entity != "" {
		fmt.Fprintf(&message, "Entity: %s\n", safeChatText(incident.Entity))
	}
	if incident.RunURL != "" {
		fmt.Fprintf(&message, "Run: %s\n", safeChatText(incident.RunURL))
	}
	if incident.GroupKey != "" {
		fmt.Fprintf(&message, "Group: %s\n", safeChatText(incident.GroupKey))
	}
	fmt.Fprintf(&message, "\nSummary\n%s", safeChatText(incident.Investigation.Summary))
	if incident.Investigation.Impact != "" {
		fmt.Fprintf(&message, "\n\nImpact\n%s", safeChatText(incident.Investigation.Impact))
	}
	if len(incident.Investigation.Evidence) > 0 {
		message.WriteString("\n\nEvidence")
		for _, evidence := range incident.Investigation.Evidence {
			fmt.Fprintf(&message, "\n- %s", safeChatText(evidence))
		}
	}
	if len(incident.Investigation.RuledOut) > 0 {
		message.WriteString("\n\nRuled out")
		for _, hypothesis := range incident.Investigation.RuledOut {
			fmt.Fprintf(&message, "\n- %s", safeChatText(hypothesis))
		}
	}
	if len(incident.Investigation.AffectedComponents) > 0 {
		message.WriteString("\n\nAffected components")
		for _, component := range incident.Investigation.AffectedComponents {
			fmt.Fprintf(&message, "\n- %s", safeChatText(component))
		}
	}
	if len(incident.Investigation.RecommendedActions) > 0 {
		message.WriteString("\n\nRecommended actions")
		for _, action := range incident.Investigation.RecommendedActions {
			fmt.Fprintf(&message, "\n- %s", safeChatText(action))
		}
	}
	fmt.Fprintf(&message, "\n\nProduction writes: %d", incident.Investigation.ProductionWrites)
	return message.String()
}

func safeChatText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = strings.ReplaceAll(value, "@", "＠")
	return value
}

func escapeMattermostMarkdown(value string) string {
	return strings.NewReplacer(
		`\`, `\\`, "`", "\\`", "*", "\\*", "_", "\\_", "~", "\\~",
		"[", "\\[", "]", "\\]", "<", "\\<", ">", "\\>", "#", "\\#",
	).Replace(value)
}

func truncateRunes(value string, maximum int) string {
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	suffix := "\n… truncated by Akritas"
	runes := []rune(value)
	return string(runes[:maximum-len([]rune(suffix))]) + suffix
}

func newDeliveryID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("delivery-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}
