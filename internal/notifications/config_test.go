package notifications

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLoadResolvesAllReceiverTypesFromEnvironment(t *testing.T) {
	path := writeNotificationConfig(t, `{
		"version": 1,
		"timeout": "3s",
		"receivers": [
			{"name":"automation","type":"webhook","url_env":"HOOK_URL","bearer_token_env":"HOOK_TOKEN","hmac_secret_env":"HOOK_SECRET"},
			{"name":"telegram-ops","type":"telegram","base_url":"https://telegram.example","bot_token_env":"TG_TOKEN","chat_id_env":"TG_CHAT","message_thread_id_env":"TG_THREAD"},
			{"name":"mattermost-ops","type":"mattermost","base_url_env":"MM_URL","bot_token_env":"MM_TOKEN","channel_id_env":"MM_CHANNEL"}
		]
	}`)
	values := map[string]string{
		"HOOK_URL": "https://hooks.example/incidents", "HOOK_TOKEN": "bearer", "HOOK_SECRET": "signing",
		"TG_TOKEN": "telegram-token", "TG_CHAT": "-100123", "TG_THREAD": "42",
		"MM_URL": "https://mattermost.example", "MM_TOKEN": "mattermost-token", "MM_CHANNEL": "channel-id",
	}
	dispatcher, err := Load(path, func(name string) (string, bool) {
		value, exists := values[name]
		return value, exists
	})
	if err != nil {
		t.Fatal(err)
	}
	if dispatcher.Len() != 3 || dispatcher.BotReceiverCount() != 0 {
		t.Fatalf("receivers=%d bots=%d, want 3 and 0", dispatcher.Len(), dispatcher.BotReceiverCount())
	}
}

func TestShippedNotificationConfigurationIsValid(t *testing.T) {
	values := map[string]string{
		"AKRITAS_INCIDENT_WEBHOOK_URL":   "https://hooks.example/incidents",
		"AKRITAS_INCIDENT_WEBHOOK_TOKEN": "bearer", "AKRITAS_INCIDENT_WEBHOOK_HMAC_SECRET": "signing",
		"AKRITAS_TELEGRAM_BOT_TOKEN": "telegram-token", "AKRITAS_TELEGRAM_CHAT_ID": "-100123",
		"AKRITAS_TELEGRAM_WEBHOOK_SECRET": "telegram_webhook_secret", "AKRITAS_TELEGRAM_ALLOWED_CHAT_IDS": "-100123",
		"AKRITAS_TELEGRAM_ALLOWED_USER_IDS": "77",
		"AKRITAS_MATTERMOST_URL":            "https://mattermost.example", "AKRITAS_MATTERMOST_BOT_TOKEN": "mattermost-token",
		"AKRITAS_MATTERMOST_CHANNEL_ID":             "channel-id",
		"AKRITAS_MATTERMOST_OUTGOING_WEBHOOK_TOKEN": "outgoing-token", "AKRITAS_MATTERMOST_ALLOWED_CHANNEL_IDS": "channel-id",
		"AKRITAS_MATTERMOST_ALLOWED_USER_IDS": "user-id",
	}
	dispatcher, err := Load("../../configs/akritas/notifications.example.json", func(name string) (string, bool) {
		value, exists := values[name]
		return value, exists
	})
	if err != nil {
		t.Fatal(err)
	}
	if dispatcher.Len() != 3 || dispatcher.BotReceiverCount() != 2 {
		t.Fatalf("receivers=%d bots=%d, want 3 and 2", dispatcher.Len(), dispatcher.BotReceiverCount())
	}
}

func TestLoadRejectsUnknownFieldsAndMissingSecrets(t *testing.T) {
	unknown := writeNotificationConfig(t, `{"version":1,"receivers":[{"name":"ops","type":"webhook","url":"https://example.test","unexpected":true}]}`)
	if _, err := Load(unknown, nil); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unexpected unknown-field error: %v", err)
	}

	missing := writeNotificationConfig(t, `{"version":1,"receivers":[{"name":"ops","type":"telegram","bot_token_env":"TG_TOKEN","chat_id":"42"}]}`)
	if _, err := Load(missing, func(string) (string, bool) { return "", false }); err == nil || !strings.Contains(err.Error(), "TG_TOKEN") {
		t.Fatalf("unexpected missing-secret error: %v", err)
	}
}

func TestLoadRejectsReceiverSpecificFields(t *testing.T) {
	path := writeNotificationConfig(t, `{"version":1,"receivers":[{"name":"ops","type":"mattermost","base_url":"https://example.test","bot_token_env":"MM_TOKEN","channel_id":"channel","chat_id":"wrong"}]}`)
	if _, err := Load(path, func(string) (string, bool) { return "token", true }); err == nil || !strings.Contains(err.Error(), "not valid for mattermost") {
		t.Fatalf("unexpected receiver-field error: %v", err)
	}
}

func TestLoadResolvesInboundBotPolicyAndLimits(t *testing.T) {
	path := writeNotificationConfig(t, `{
		"version":1,"bot_queue_size":12,"bot_history_messages":10,"bot_history_bytes":8192,"bot_session_ttl":"2h","bot_max_sessions":32,
		"receivers":[{"name":"ops","type":"telegram","bot_token_env":"TG_TOKEN","chat_id":"42","inbound_secret_env":"TG_WEBHOOK_SECRET","allowed_conversation_ids_env":"TG_CHATS","allowed_user_ids":["7"]}]
	}`)
	values := map[string]string{"TG_TOKEN": "token", "TG_WEBHOOK_SECRET": "valid_secret", "TG_CHATS": `["42","43"]`}
	dispatcher, err := Load(path, func(name string) (string, bool) { value, exists := values[name]; return value, exists })
	if err != nil {
		t.Fatal(err)
	}
	settings := dispatcher.BotSettings()
	if dispatcher.BotReceiverCount() != 1 || settings.QueueSize != 12 || settings.HistoryMessages != 10 || settings.HistoryBytes != 8192 || settings.SessionTTL != 2*time.Hour || settings.MaxSessions != 32 {
		t.Fatalf("unexpected bot configuration: receivers=%d settings=%+v", dispatcher.BotReceiverCount(), settings)
	}
}

func TestLoadSupportsTelegramPollingWithoutWebhookSecret(t *testing.T) {
	path := writeNotificationConfig(t, `{
		"version":1,
		"receivers":[{"name":"ops","type":"telegram","bot_token_env":"TG_TOKEN","chat_id":"42","inbound_mode":"polling"}]
	}`)
	dispatcher, err := Load(path, func(name string) (string, bool) {
		if name == "TG_TOKEN" {
			return "token", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if dispatcher.BotReceiverCount() != 1 || !slices.Equal(dispatcher.TelegramPollingReceiverNames(), []string{"ops"}) {
		t.Fatalf("unexpected polling receivers: bots=%d names=%v", dispatcher.BotReceiverCount(), dispatcher.TelegramPollingReceiverNames())
	}
	receiver := dispatcher.receivers[0].(*telegramReceiver)
	if !receiver.access.conversations["42"] {
		t.Fatalf("outbound chat ID was not used as the default polling allowlist: %+v", receiver.access.conversations)
	}
}

func TestLoadRejectsUnsafeInboundBotConfiguration(t *testing.T) {
	mattermost := writeNotificationConfig(t, `{"version":1,"receivers":[{"name":"ops","type":"mattermost","base_url":"https://mattermost.example","bot_token_env":"BOT","channel_id":"channel","inbound_secret_env":"INBOUND","allowed_conversation_ids":["channel"]}]}`)
	lookup := func(name string) (string, bool) { return "secret", true }
	if _, err := Load(mattermost, lookup); err == nil || !strings.Contains(err.Error(), "requires allowed_user_ids") {
		t.Fatalf("unexpected Mattermost allowlist error: %v", err)
	}

	telegram := writeNotificationConfig(t, `{"version":1,"receivers":[{"name":"ops","type":"telegram","bot_token_env":"BOT","chat_id":"42","inbound_secret_env":"INBOUND","allowed_user_ids":["7"]}]}`)
	if _, err := Load(telegram, func(name string) (string, bool) {
		if name == "INBOUND" {
			return "not valid!", true
		}
		return "token", true
	}); err == nil || !strings.Contains(err.Error(), "Telegram inbound secret") {
		t.Fatalf("unexpected Telegram secret error: %v", err)
	}

	pollingWithSecret := writeNotificationConfig(t, `{"version":1,"receivers":[{"name":"ops","type":"telegram","bot_token_env":"BOT","chat_id":"42","inbound_mode":"polling","inbound_secret_env":"INBOUND"}]}`)
	if _, err := Load(pollingWithSecret, lookup); err == nil || !strings.Contains(err.Error(), "not valid for Telegram polling") {
		t.Fatalf("unexpected polling secret error: %v", err)
	}
}

func writeNotificationConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notifications.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
