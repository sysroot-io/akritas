package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDecodeTelegramAuthenticatesAndAuthorizesTextUpdates(t *testing.T) {
	dispatcher := newDispatcher(resolvedConfig{
		timeout: time.Second, bot: BotSettings{QueueSize: 8},
		receivers: []receiver{&telegramReceiver{
			name: "ops", access: botAccess{
				mode: botInboundWebhook, secret: "webhook_secret", conversations: map[string]bool{"-10042": true}, users: map[string]bool{"77": true},
			},
		}}}, nil)
	body := []byte(`{"update_id":123,"message":{"message_id":9,"message_thread_id":4,"from":{"id":77,"is_bot":false},"chat":{"id":-10042},"text":"  Check pg01  "}}`)
	message, err := dispatcher.DecodeTelegram("ops", "webhook_secret", body)
	if err != nil {
		t.Fatal(err)
	}
	if message.EventID != "telegram:ops:123" || message.ConversationID != "-10042" || message.UserID != "77" || message.ReplyToID != "9" || message.ThreadID != 4 || message.Text != "Check pg01" {
		t.Fatalf("unexpected message: %+v", message)
	}
	if _, err := dispatcher.DecodeTelegram("ops", "wrong", body); !errors.Is(err, ErrBotUnauthorized) {
		t.Fatalf("unexpected authentication error: %v", err)
	}
	forbidden := strings.Replace(string(body), `"id":77`, `"id":78`, 1)
	if _, err := dispatcher.DecodeTelegram("ops", "webhook_secret", []byte(forbidden)); !errors.Is(err, ErrBotForbidden) {
		t.Fatalf("unexpected authorization error: %v", err)
	}
	bot := strings.Replace(string(body), `"is_bot":false`, `"is_bot":true`, 1)
	if _, err := dispatcher.DecodeTelegram("ops", "webhook_secret", []byte(bot)); !errors.Is(err, ErrBotUpdateIgnored) {
		t.Fatalf("unexpected bot-message error: %v", err)
	}
}

func TestDecodeMattermostSupportsFormAndStripsTriggerWord(t *testing.T) {
	dispatcher := newDispatcher(resolvedConfig{
		timeout: time.Second, bot: BotSettings{QueueSize: 8},
		receivers: []receiver{&mattermostReceiver{
			name: "ops", access: botAccess{
				mode: botInboundWebhook, secret: "outgoing-token", conversations: map[string]bool{"channel-1": true}, users: map[string]bool{"user-1": true},
			},
		}}}, nil)
	form := url.Values{
		"token": {"outgoing-token"}, "channel_id": {"channel-1"}, "user_id": {"user-1"},
		"post_id": {"post-1"}, "trigger_word": {"akritas"}, "text": {"akritas check pg01"},
	}
	message, err := dispatcher.DecodeMattermost("ops", "application/x-www-form-urlencoded", []byte(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	if message.EventID != "mattermost:ops:post-1" || message.ConversationID != "channel-1" || message.ReplyToID != "post-1" || message.Text != "check pg01" {
		t.Fatalf("unexpected message: %+v", message)
	}
	form.Set("token", "wrong")
	if _, err := dispatcher.DecodeMattermost("ops", "application/x-www-form-urlencoded", []byte(form.Encode())); !errors.Is(err, ErrBotUnauthorized) {
		t.Fatalf("unexpected authentication error: %v", err)
	}
}

func TestBotReplyUsesProviderConversationAndSplitsLongTelegramText(t *testing.T) {
	requests := make(chan map[string]any, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode reply: %v", err)
		}
		requests <- payload
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	dispatcher := newDispatcher(resolvedConfig{timeout: time.Second, receivers: []receiver{
		&telegramReceiver{name: "ops", baseURL: server.URL, botToken: "token", access: botAccess{mode: botInboundWebhook, secret: "secret"}},
	}}, server.Client())
	result := dispatcher.Reply(context.Background(), BotMessage{
		Receiver: "ops", Type: "telegram", ConversationID: "-10042", ReplyToID: "9", ThreadID: 4,
	}, strings.Repeat("ж", maximumTelegramRunes+100))
	if result.Status != "delivered" {
		t.Fatalf("unexpected delivery: %+v", result)
	}
	if len(requests) != 2 {
		t.Fatalf("requests=%d, want 2", len(requests))
	}
	for index := 0; index < 2; index++ {
		payload := <-requests
		if payload["chat_id"] != "-10042" || payload["message_thread_id"] != float64(4) {
			t.Fatalf("unexpected payload: %+v", payload)
		}
		if len([]rune(payload["text"].(string))) > maximumTelegramRunes {
			t.Fatalf("Telegram chunk exceeds limit")
		}
	}
}

func TestMattermostBotReplyUsesBotTokenChannelAndThread(t *testing.T) {
	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v4/posts" || request.Header.Get("Authorization") != "Bearer mattermost-token" {
			t.Errorf("unexpected Mattermost request: path=%s headers=%v", request.URL.Path, request.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode reply: %v", err)
		}
		requests <- payload
		writer.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	dispatcher := newDispatcher(resolvedConfig{timeout: time.Second, receivers: []receiver{
		&mattermostReceiver{name: "ops", baseURL: server.URL, botToken: "mattermost-token", access: botAccess{mode: botInboundWebhook, secret: "secret"}},
	}}, server.Client())
	result := dispatcher.Reply(context.Background(), BotMessage{
		Receiver: "ops", Type: "mattermost", ConversationID: "channel-1", ReplyToID: "post-1",
	}, "Check @channel and *CPU*.")
	if result.Status != "delivered" {
		t.Fatalf("unexpected delivery: %+v", result)
	}
	payload := <-requests
	if payload["channel_id"] != "channel-1" || payload["root_id"] != "post-1" ||
		strings.Contains(payload["message"].(string), "@channel") || !strings.Contains(payload["message"].(string), `\*CPU\*`) {
		t.Fatalf("unexpected Mattermost payload: %+v", payload)
	}
}
