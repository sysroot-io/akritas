package notifications

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	maximumBotIngressBytes = 64 * 1024
	maximumBotTextBytes    = 32 * 1024
	maximumBotIDBytes      = 256
	botInboundWebhook      = "webhook"
	botInboundPolling      = "polling"
)

var (
	ErrUnknownBotReceiver = errors.New("unknown bot receiver")
	ErrBotUnauthorized    = errors.New("bot webhook authentication failed")
	ErrBotForbidden       = errors.New("bot actor or conversation is not allowed")
	ErrBotUpdateIgnored   = errors.New("bot update does not contain an eligible text message")
)

type BotSettings struct {
	QueueSize       int
	HistoryMessages int
	HistoryBytes    int
	SessionTTL      time.Duration
	MaxSessions     int
}

type botAccess struct {
	mode          string
	secret        string
	conversations map[string]bool
	users         map[string]bool
}

func (access botAccess) enabled() bool { return access.mode != "" }
func (access botAccess) polling() bool { return access.mode == botInboundPolling }
func (access botAccess) webhook() bool { return access.mode == botInboundWebhook }

func (access botAccess) authorizedWebhook(secret, conversationID, userID string) error {
	if !access.webhook() {
		return ErrUnknownBotReceiver
	}
	if subtle.ConstantTimeCompare([]byte(access.secret), []byte(secret)) != 1 {
		return ErrBotUnauthorized
	}
	return access.authorizedActor(conversationID, userID)
}

func (access botAccess) authorizedActor(conversationID, userID string) error {
	if len(access.conversations) > 0 && !access.conversations[conversationID] {
		return ErrBotForbidden
	}
	if len(access.users) > 0 && !access.users[userID] {
		return ErrBotForbidden
	}
	return nil
}

type BotMessage struct {
	Receiver       string
	Type           string
	EventID        string
	ConversationID string
	UserID         string
	ReplyToID      string
	ThreadID       int64
	Text           string
}

func (dispatcher *Dispatcher) BotSettings() BotSettings {
	if dispatcher == nil {
		return BotSettings{}
	}
	return dispatcher.bot
}

func (dispatcher *Dispatcher) BotReceiverCount() int {
	if dispatcher == nil {
		return 0
	}
	count := 0
	for _, configured := range dispatcher.receivers {
		switch receiver := configured.(type) {
		case *telegramReceiver:
			if receiver.access.enabled() {
				count++
			}
		case *mattermostReceiver:
			if receiver.access.enabled() {
				count++
			}
		}
	}
	return count
}

func (dispatcher *Dispatcher) DecodeTelegram(receiverName, secret string, body []byte) (BotMessage, error) {
	configured, err := dispatcher.botReceiver(receiverName, "telegram")
	if err != nil {
		return BotMessage{}, err
	}
	receiver := configured.(*telegramReceiver)
	if !receiver.access.webhook() {
		return BotMessage{}, ErrUnknownBotReceiver
	}
	message, _, err := decodeTelegramUpdate(receiverName, body)
	if err != nil {
		return BotMessage{}, err
	}
	if err := receiver.access.authorizedWebhook(secret, message.ConversationID, message.UserID); err != nil {
		return BotMessage{}, err
	}
	return message, nil
}

func decodeTelegramUpdate(receiverName string, body []byte) (BotMessage, int64, error) {
	if len(body) == 0 || len(body) > maximumBotIngressBytes {
		return BotMessage{}, -1, fmt.Errorf("Telegram update must contain 1..%d bytes", maximumBotIngressBytes)
	}
	var update struct {
		UpdateID int64 `json:"update_id"`
		Message  *struct {
			MessageID       int64 `json:"message_id"`
			MessageThreadID int64 `json:"message_thread_id"`
			From            *struct {
				ID    int64 `json:"id"`
				IsBot bool  `json:"is_bot"`
			} `json:"from"`
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
			Text string `json:"text"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &update); err != nil {
		return BotMessage{}, -1, fmt.Errorf("decode Telegram update: %w", err)
	}
	if update.UpdateID < 0 || update.Message == nil || update.Message.From == nil ||
		update.Message.From.IsBot || update.Message.Chat.ID == 0 || update.Message.MessageID <= 0 ||
		strings.TrimSpace(update.Message.Text) == "" {
		return BotMessage{}, update.UpdateID, ErrBotUpdateIgnored
	}
	if len(update.Message.Text) > maximumBotTextBytes {
		return BotMessage{}, update.UpdateID, fmt.Errorf("Telegram message text exceeds %d bytes", maximumBotTextBytes)
	}
	conversationID := strconv.FormatInt(update.Message.Chat.ID, 10)
	userID := strconv.FormatInt(update.Message.From.ID, 10)
	return BotMessage{
		Receiver: receiverName, Type: "telegram", EventID: "telegram:" + receiverName + ":" + strconv.FormatInt(update.UpdateID, 10),
		ConversationID: conversationID, UserID: userID,
		ReplyToID: strconv.FormatInt(update.Message.MessageID, 10), ThreadID: update.Message.MessageThreadID,
		Text: strings.TrimSpace(update.Message.Text),
	}, update.UpdateID, nil
}

func (dispatcher *Dispatcher) DecodeMattermost(receiverName, contentType string, body []byte) (BotMessage, error) {
	configured, err := dispatcher.botReceiver(receiverName, "mattermost")
	if err != nil {
		return BotMessage{}, err
	}
	receiver := configured.(*mattermostReceiver)
	if !receiver.access.enabled() {
		return BotMessage{}, ErrUnknownBotReceiver
	}
	if len(body) == 0 || len(body) > maximumBotIngressBytes {
		return BotMessage{}, fmt.Errorf("Mattermost webhook must contain 1..%d bytes", maximumBotIngressBytes)
	}
	values, err := decodeMattermostValues(contentType, body)
	if err != nil {
		return BotMessage{}, err
	}
	conversationID := strings.TrimSpace(values.Get("channel_id"))
	userID := strings.TrimSpace(values.Get("user_id"))
	text := strings.TrimSpace(values.Get("text"))
	postID := strings.TrimSpace(values.Get("post_id"))
	if conversationID == "" || userID == "" || text == "" {
		return BotMessage{}, ErrBotUpdateIgnored
	}
	if len(conversationID) > maximumBotIDBytes || len(userID) > maximumBotIDBytes ||
		len(postID) > maximumBotIDBytes || len(text) > maximumBotTextBytes {
		return BotMessage{}, fmt.Errorf("Mattermost identifiers or text exceed bot ingress limits")
	}
	if err := receiver.access.authorizedWebhook(strings.TrimSpace(values.Get("token")), conversationID, userID); err != nil {
		return BotMessage{}, err
	}
	trigger := strings.TrimSpace(values.Get("trigger_word"))
	if trigger != "" {
		if text == trigger {
			text = ""
		} else if strings.HasPrefix(text, trigger+" ") {
			text = strings.TrimSpace(strings.TrimPrefix(text, trigger))
		}
	}
	eventID := postID
	if eventID == "" {
		sum := sha256.Sum256(body)
		eventID = hex.EncodeToString(sum[:16])
	}
	return BotMessage{
		Receiver: receiverName, Type: "mattermost", EventID: "mattermost:" + receiverName + ":" + eventID,
		ConversationID: conversationID, UserID: userID, ReplyToID: postID, Text: text,
	}, nil
}

func decodeMattermostValues(contentType string, body []byte) (url.Values, error) {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "application/json" {
		var fields map[string]any
		if err := json.Unmarshal(body, &fields); err != nil {
			return nil, fmt.Errorf("decode Mattermost JSON: %w", err)
		}
		values := make(url.Values, len(fields))
		for name, value := range fields {
			switch typed := value.(type) {
			case string:
				values.Set(name, typed)
			case json.Number:
				values.Set(name, typed.String())
			case float64:
				values.Set(name, strconv.FormatFloat(typed, 'f', -1, 64))
			}
		}
		return values, nil
	}
	if mediaType != "application/x-www-form-urlencoded" && mediaType != "" {
		return nil, fmt.Errorf("unsupported Mattermost content type %q", mediaType)
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, fmt.Errorf("decode Mattermost form: %w", err)
	}
	return values, nil
}

func (dispatcher *Dispatcher) Reply(ctx context.Context, message BotMessage, text string) DeliveryResult {
	deliveryID := newDeliveryID()
	result := DeliveryResult{Receiver: message.Receiver, Type: message.Type, DeliveryID: deliveryID, Status: "failed"}
	configured, err := dispatcher.botReceiver(message.Receiver, message.Type)
	if err != nil {
		result.ErrorCode = "unknown_receiver"
		return result
	}
	deliveryContext, cancel := context.WithTimeout(ctx, dispatcher.timeout)
	defer cancel()
	var statusCode int
	var errorCode string
	switch receiver := configured.(type) {
	case *telegramReceiver:
		statusCode, errorCode = receiver.reply(deliveryContext, dispatcher.client, message, text)
	case *mattermostReceiver:
		statusCode, errorCode = receiver.reply(deliveryContext, dispatcher.client, message, text)
	default:
		errorCode = "unsupported_receiver"
	}
	result.StatusCode, result.ErrorCode = statusCode, errorCode
	if errorCode == "" {
		result.Status = "delivered"
	}
	return result
}

func (dispatcher *Dispatcher) botReceiver(name, receiverType string) (receiver, error) {
	if dispatcher == nil {
		return nil, ErrUnknownBotReceiver
	}
	for _, configured := range dispatcher.receivers {
		if configured.Name() == name && configured.Type() == receiverType {
			return configured, nil
		}
	}
	return nil, ErrUnknownBotReceiver
}

func (receiver *telegramReceiver) reply(ctx context.Context, client *http.Client, message BotMessage, text string) (int, string) {
	chunks := splitBotMessage(sanitizeBotText(text), maximumTelegramRunes)
	lastStatus := 0
	for _, chunk := range chunks {
		payload := map[string]any{"chat_id": message.ConversationID, "text": chunk}
		if message.ThreadID > 0 {
			payload["message_thread_id"] = message.ThreadID
		}
		if replyID, err := strconv.ParseInt(message.ReplyToID, 10, 64); err == nil && replyID > 0 {
			payload["reply_parameters"] = map[string]any{"message_id": replyID, "allow_sending_without_reply": true}
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return lastStatus, "encode_payload"
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, receiver.baseURL+"/bot"+receiver.botToken+"/sendMessage", bytes.NewReader(body))
		if err != nil {
			return lastStatus, "create_request"
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("User-Agent", "Akritas/telegram-chat")
		lastStatus, errorCode := performRequest(client, request, true)
		if errorCode != "" {
			return lastStatus, errorCode
		}
	}
	return lastStatus, ""
}

func (receiver *mattermostReceiver) reply(ctx context.Context, client *http.Client, message BotMessage, text string) (int, string) {
	chunks := splitBotMessage(escapeMattermostMarkdown(sanitizeBotText(text)), maximumMattermostRunes)
	lastStatus := 0
	for _, chunk := range chunks {
		payload := map[string]any{"channel_id": message.ConversationID, "message": chunk}
		if message.ReplyToID != "" {
			payload["root_id"] = message.ReplyToID
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return lastStatus, "encode_payload"
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, receiver.baseURL+"/api/v4/posts", bytes.NewReader(body))
		if err != nil {
			return lastStatus, "create_request"
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+receiver.botToken)
		request.Header.Set("User-Agent", "Akritas/mattermost-chat")
		lastStatus, errorCode := performRequest(client, request, false)
		if errorCode != "" {
			return lastStatus, errorCode
		}
	}
	return lastStatus, ""
}

func sanitizeBotText(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(item rune) rune {
		if item == '@' {
			return '＠'
		}
		if item == '\r' {
			return '\n'
		}
		if item != '\n' && item != '\t' && unicode.IsControl(item) {
			return -1
		}
		return item
	}, value)
	if value == "" {
		return "Akritas returned an empty response."
	}
	return value
}

func splitBotMessage(value string, maximum int) []string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return []string{value}
	}
	chunkSize := maximum - 16
	count := (len(runes) + chunkSize - 1) / chunkSize
	chunks := make([]string, 0, count)
	for index, start := 0, 0; start < len(runes); index, start = index+1, start+chunkSize {
		end := start + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, fmt.Sprintf("(%d/%d)\n%s", index+1, count, string(runes[start:end])))
	}
	return chunks
}
