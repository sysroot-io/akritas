package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTelegramPollingAuthorizesUpdatesAndAdvancesOffset(t *testing.T) {
	nextOffset := make(chan int64, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bottoken/getUpdates" {
			http.NotFound(writer, request)
			return
		}
		var input struct {
			Offset int64 `json:"offset"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode getUpdates request: %v", err)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if input.Offset == 0 {
			_, _ = writer.Write([]byte(`{"ok":true,"result":[
				{"update_id":100,"message":{"message_id":1,"from":{"id":7,"is_bot":false},"chat":{"id":99},"text":"forbidden"}},
				{"update_id":101,"message":{"message_id":2,"from":{"id":7,"is_bot":false},"chat":{"id":42},"text":"check pg01"}}
			]}`))
			return
		}
		select {
		case nextOffset <- input.Offset:
		default:
		}
		<-request.Context().Done()
	}))
	defer server.Close()

	dispatcher := newDispatcher(resolvedConfig{receivers: []receiver{&telegramReceiver{
		name: "ops", baseURL: server.URL, botToken: "token", chatID: "42",
		access: botAccess{mode: botInboundPolling, conversations: map[string]bool{"42": true}},
	}}}, server.Client())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	messages := make(chan BotMessage, 1)
	go func() {
		done <- dispatcher.PollTelegram(ctx, "ops", func(message BotMessage) error {
			messages <- message
			return nil
		}, func(err error) {
			t.Errorf("unexpected polling error: %v", err)
		})
	}()

	select {
	case message := <-messages:
		if message.EventID != "telegram:ops:101" || message.Text != "check pg01" {
			t.Fatalf("unexpected polling message: %+v", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for polling message")
	}
	select {
	case offset := <-nextOffset:
		if offset != 102 {
			t.Fatalf("next offset=%d, want 102", offset)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for acknowledged offset")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("polling stopped with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("polling did not stop after cancellation")
	}
}
