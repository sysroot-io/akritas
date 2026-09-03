package notifications

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"akritas/internal/investigation"
)

func TestDispatcherDeliversWebhookTelegramAndMattermost(t *testing.T) {
	var lock sync.Mutex
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		lock.Lock()
		defer lock.Unlock()
		switch request.URL.Path {
		case "/generic":
			seen["webhook"] = true
			if request.Header.Get("Authorization") != "Bearer webhook-token" || request.Header.Get("X-Akritas-Event") != EventIncidentInvestigated {
				t.Errorf("unexpected webhook headers: %+v", request.Header)
			}
			mac := hmac.New(sha256.New, []byte("signing-secret"))
			_, _ = mac.Write(body)
			expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
			if request.Header.Get("X-Akritas-Signature") != expected {
				t.Errorf("signature=%q, want %q", request.Header.Get("X-Akritas-Signature"), expected)
			}
			var payload WebhookEnvelope
			if err := json.Unmarshal(body, &payload); err != nil || payload.Incident.RunID != "run-1" || payload.DeliveryID == "" {
				t.Errorf("unexpected webhook payload: %+v err=%v", payload, err)
			}
			writer.WriteHeader(http.StatusNoContent)
		case "/bottelegram-token/sendMessage":
			seen["telegram"] = true
			var payload struct {
				ChatID          string `json:"chat_id"`
				Text            string `json:"text"`
				MessageThreadID int64  `json:"message_thread_id"`
			}
			if err := json.Unmarshal(body, &payload); err != nil || payload.ChatID != "-10042" || payload.MessageThreadID != 7 || strings.Contains(payload.Text, "@ops") || !strings.Contains(payload.Text, "＠ops") {
				t.Errorf("unexpected Telegram payload: %+v err=%v", payload, err)
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"ok":true}`))
		case "/api/v4/posts":
			seen["mattermost"] = true
			var payload struct {
				ChannelID string `json:"channel_id"`
				Message   string `json:"message"`
			}
			if err := json.Unmarshal(body, &payload); err != nil || payload.ChannelID != "channel-1" || request.Header.Get("Authorization") != "Bearer mattermost-token" || strings.Contains(payload.Message, "@ops") || !strings.Contains(payload.Message, `\*load\*`) {
				t.Errorf("unexpected Mattermost request: payload=%+v headers=%+v err=%v", payload, request.Header, err)
			}
			writer.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	dispatcher := newDispatcher(resolvedConfig{
		timeout: 2 * time.Second,
		receivers: []receiver{
			&webhookReceiver{name: "automation", endpoint: server.URL + "/generic", bearerToken: "webhook-token", hmacSecret: "signing-secret"},
			&telegramReceiver{name: "telegram-ops", baseURL: server.URL, botToken: "telegram-token", chatID: "-10042", messageThreadID: 7},
			&mattermostReceiver{name: "mattermost-ops", baseURL: server.URL, botToken: "mattermost-token", channelID: "channel-1"},
		},
	}, server.Client())
	results := dispatcher.Deliver(context.Background(), testIncident())
	if len(results) != 3 {
		t.Fatalf("results=%d, want 3", len(results))
	}
	for index, result := range results {
		if result.Status != "delivered" || result.ErrorCode != "" || result.DeliveryID == "" {
			t.Fatalf("result[%d]=%+v", index, result)
		}
	}
	for _, receiverType := range []string{"webhook", "telegram", "mattermost"} {
		if !seen[receiverType] {
			t.Errorf("%s receiver was not called", receiverType)
		}
	}
}

func TestDispatcherReportsFailuresWithoutStoppingOtherReceivers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/failed" {
			http.Error(writer, "do not expose this response", http.StatusBadGateway)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	dispatcher := newDispatcher(resolvedConfig{timeout: time.Second, receivers: []receiver{
		&webhookReceiver{name: "failed", endpoint: server.URL + "/failed"},
		&webhookReceiver{name: "healthy", endpoint: server.URL + "/healthy"},
	}}, server.Client())
	results := dispatcher.Deliver(context.Background(), testIncident())
	if results[0].Status != "failed" || results[0].StatusCode != http.StatusBadGateway || results[0].ErrorCode != "http_status" {
		t.Fatalf("failed result=%+v", results[0])
	}
	if results[1].Status != "delivered" || results[1].StatusCode != http.StatusNoContent {
		t.Fatalf("healthy result=%+v", results[1])
	}
}

func TestTruncateRunesPreservesLimit(t *testing.T) {
	result := truncateRunes(strings.Repeat("ж", 100), 30)
	if length := len([]rune(result)); length != 30 || !strings.HasSuffix(result, "truncated by Akritas") {
		t.Fatalf("length=%d result=%q", length, result)
	}
}

func testIncident() Incident {
	return Incident{
		RunID: "run-1", Model: "akritas", AlertStatus: "firing", GroupKey: "cpu",
		Answer: "raw answer",
		Investigation: investigation.Result{
			FindingStatus: investigation.FindingSuspected, Actionability: investigation.ActionRequiresHuman,
			Confidence: investigation.ConfidenceMedium, Summary: "CPU *load* is high; ask @ops.",
			Evidence: []string{}, AffectedComponents: []string{"pg01"}, RecommendedActions: []string{"Inspect active queries."},
		},
	}
}
