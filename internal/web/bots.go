package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"akritas/internal/audit"
	"akritas/internal/notifications"
)

const maximumRememberedBotEvents = 4096

var errBotQueueFull = errors.New("bot message queue is full")

type opsBotSession struct {
	messages  []openAIChatMessage
	updatedAt time.Time
}

type opsBotGateway struct {
	server     *opsServer
	dispatcher *notifications.Dispatcher
	settings   notifications.BotSettings
	queue      chan notifications.BotMessage
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	closeOnce  sync.Once
	pollers    sync.WaitGroup

	mu        sync.Mutex
	sessions  map[string]opsBotSession
	seen      map[string]bool
	seenOrder []string
}

func newOpsBotGateway(server *opsServer, dispatcher *notifications.Dispatcher) *opsBotGateway {
	if server == nil || dispatcher == nil || dispatcher.BotReceiverCount() == 0 {
		return nil
	}
	settings := dispatcher.BotSettings()
	ctx, cancel := context.WithCancel(context.Background())
	gateway := &opsBotGateway{
		server: server, dispatcher: dispatcher, settings: settings,
		queue: make(chan notifications.BotMessage, settings.QueueSize),
		ctx:   ctx, cancel: cancel, done: make(chan struct{}),
		sessions: make(map[string]opsBotSession), seen: make(map[string]bool),
	}
	go gateway.run()
	for _, receiverName := range dispatcher.TelegramPollingReceiverNames() {
		gateway.pollers.Add(1)
		go gateway.pollTelegram(receiverName)
	}
	return gateway
}

func (gateway *opsBotGateway) close() {
	if gateway == nil {
		return
	}
	gateway.closeOnce.Do(func() {
		gateway.cancel()
		<-gateway.done
		gateway.pollers.Wait()
	})
}

func (gateway *opsBotGateway) pollTelegram(receiverName string) {
	defer gateway.pollers.Done()
	err := gateway.dispatcher.PollTelegram(gateway.ctx, receiverName, func(message notifications.BotMessage) error {
		_, err := gateway.enqueue(message)
		return err
	}, func(err error) {
		log.Printf("level=warn component=akritas_bot type=telegram receiver=%q event=poll_failed error=%q", receiverName, err)
	})
	if err != nil && gateway.ctx.Err() == nil {
		log.Printf("level=error component=akritas_bot type=telegram receiver=%q event=poll_stopped error=%q", receiverName, err)
	}
}

func (gateway *opsBotGateway) enqueue(message notifications.BotMessage) (bool, error) {
	if gateway == nil {
		return false, notifications.ErrUnknownBotReceiver
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.seen[message.EventID] {
		return true, nil
	}
	select {
	case gateway.queue <- message:
		gateway.seen[message.EventID] = true
		gateway.seenOrder = append(gateway.seenOrder, message.EventID)
		if len(gateway.seenOrder) > maximumRememberedBotEvents {
			oldest := gateway.seenOrder[0]
			gateway.seenOrder = gateway.seenOrder[1:]
			delete(gateway.seen, oldest)
		}
		return false, nil
	default:
		return false, errBotQueueFull
	}
}

func (gateway *opsBotGateway) run() {
	defer close(gateway.done)
	for {
		select {
		case <-gateway.ctx.Done():
			return
		case message := <-gateway.queue:
			gateway.process(message)
		}
	}
}

func (gateway *opsBotGateway) process(message notifications.BotMessage) {
	command, prompt := normalizeBotPrompt(message.Text)
	switch command {
	case "help":
		gateway.sendFixedReply(message, "Send a question or incident description. Commands: /new resets this conversation, /status shows retained history, /help shows this message.")
		return
	case "new":
		gateway.resetSession(message)
		gateway.sendFixedReply(message, "Conversation reset. The next message starts with empty history.")
		return
	case "status":
		count := gateway.sessionMessageCount(message)
		gateway.sendFixedReply(message, fmt.Sprintf("Conversation is active. Retained messages: %d of %d.", count, gateway.settings.HistoryMessages))
		return
	case "unsupported":
		gateway.sendFixedReply(message, "Unsupported command. Use /help, /new, /status, or /ask <question>.")
		return
	}
	if strings.TrimSpace(prompt) == "" {
		gateway.sendFixedReply(message, "Send a question after /ask, or use /help.")
		return
	}

	history := gateway.sessionHistory(message)
	history = append(history, openAIChatMessage{Role: "user", Content: prompt})
	history = trimBotRequestHistory(history)
	ctx, cancel := context.WithTimeout(gateway.ctx, gateway.server.requestTimeout)
	defer cancel()
	auditRun := gateway.server.beginAuditRun(message.Type+"_bot", "", map[string]string{"receiver": message.Receiver})
	defer auditRun.failIfRunning("request_incomplete")
	result, err := gateway.server.completeChat(ctx, history, gateway.server.defaultMaxTokens, gateway.server.defaultTemp)
	if err != nil {
		delivery := gateway.dispatcher.Reply(gateway.ctx, message, "Akritas could not complete this request. Try again later.")
		addBotReplyAuditEvent(auditRun, delivery)
		auditRun.finish(audit.RunFailed, "bot_chat_failed", nil)
		log.Printf("level=warn component=akritas_bot type=%q receiver=%q event=chat_failed", message.Type, message.Receiver)
		return
	}
	auditRun.addToolEvents(result)
	delivery := gateway.dispatcher.Reply(gateway.ctx, message, result.Answer)
	addBotReplyAuditEvent(auditRun, delivery)
	if delivery.Status == "delivered" {
		history = append(history, openAIChatMessage{Role: "assistant", Content: result.Answer})
		gateway.storeSession(message, history)
	} else {
		log.Printf(
			"level=warn component=akritas_bot type=%q receiver=%q event=reply_failed status_code=%d error_code=%q",
			message.Type, message.Receiver, delivery.StatusCode, delivery.ErrorCode,
		)
	}
	auditRun.succeed(opsRunUsageMetadata(result))
}

func (gateway *opsBotGateway) sendFixedReply(message notifications.BotMessage, text string) {
	delivery := gateway.dispatcher.Reply(gateway.ctx, message, text)
	if delivery.Status != "delivered" {
		log.Printf(
			"level=warn component=akritas_bot type=%q receiver=%q event=command_reply_failed status_code=%d error_code=%q",
			message.Type, message.Receiver, delivery.StatusCode, delivery.ErrorCode,
		)
	}
}

func addBotReplyAuditEvent(run *opsAuditRun, delivery notifications.DeliveryResult) {
	metadata := map[string]string{
		"receiver": delivery.Receiver, "type": delivery.Type, "delivery_id": delivery.DeliveryID,
	}
	if delivery.StatusCode != 0 {
		metadata["status_code"] = fmt.Sprintf("%d", delivery.StatusCode)
	}
	if delivery.ErrorCode != "" {
		metadata["error_code"] = delivery.ErrorCode
	}
	run.addEvent("bot_reply", delivery.Status, metadata)
}

func normalizeBotPrompt(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "help", ""
	}
	fields := strings.Fields(value)
	first := strings.ToLower(fields[0])
	if strings.HasPrefix(first, "/") {
		first = strings.SplitN(first, "@", 2)[0]
		switch first {
		case "/start", "/help":
			return "help", ""
		case "/new", "/reset":
			return "new", ""
		case "/status":
			return "status", ""
		case "/ask":
			return "chat", strings.TrimSpace(strings.TrimPrefix(value, fields[0]))
		default:
			return "unsupported", ""
		}
	}
	return "chat", value
}

func (gateway *opsBotGateway) sessionKey(message notifications.BotMessage) string {
	return message.Type + "\x00" + message.Receiver + "\x00" + message.ConversationID + "\x00" + fmt.Sprintf("%d", message.ThreadID)
}

func (gateway *opsBotGateway) sessionHistory(message notifications.BotMessage) []openAIChatMessage {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.pruneSessionsLocked(time.Now())
	session := gateway.sessions[gateway.sessionKey(message)]
	return append([]openAIChatMessage(nil), session.messages...)
}

func (gateway *opsBotGateway) sessionMessageCount(message notifications.BotMessage) int {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.pruneSessionsLocked(time.Now())
	return len(gateway.sessions[gateway.sessionKey(message)].messages)
}

func (gateway *opsBotGateway) resetSession(message notifications.BotMessage) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	delete(gateway.sessions, gateway.sessionKey(message))
}

func (gateway *opsBotGateway) storeSession(message notifications.BotMessage, history []openAIChatMessage) {
	for len(history) > gateway.settings.HistoryMessages || botHistoryBytes(history) > gateway.settings.HistoryBytes {
		if len(history) < 2 {
			history = nil
			break
		}
		history = history[2:]
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	now := time.Now()
	gateway.pruneSessionsLocked(now)
	key := gateway.sessionKey(message)
	if _, exists := gateway.sessions[key]; !exists && len(gateway.sessions) >= gateway.settings.MaxSessions {
		oldestKey := ""
		var oldest time.Time
		for candidate, session := range gateway.sessions {
			if oldestKey == "" || session.updatedAt.Before(oldest) {
				oldestKey, oldest = candidate, session.updatedAt
			}
		}
		delete(gateway.sessions, oldestKey)
	}
	gateway.sessions[key] = opsBotSession{messages: append([]openAIChatMessage(nil), history...), updatedAt: now}
}

func trimBotRequestHistory(history []openAIChatMessage) []openAIChatMessage {
	for (len(history) > maximumOpsChatMessages || botHistoryBytes(history) > maximumOpsChatBytes) && len(history) > 2 {
		history = history[2:]
	}
	return history
}

func (gateway *opsBotGateway) pruneSessionsLocked(now time.Time) {
	for key, session := range gateway.sessions {
		if now.Sub(session.updatedAt) > gateway.settings.SessionTTL {
			delete(gateway.sessions, key)
		}
	}
}

func botHistoryBytes(messages []openAIChatMessage) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content)
	}
	return total
}

func (server *opsServer) handleTelegramBotWebhook(writer http.ResponseWriter, request *http.Request) {
	if server.botGateway == nil || server.notifications == nil {
		http.NotFound(writer, request)
		return
	}
	body, err := readBotWebhookBody(writer, request)
	if err != nil {
		writeOpsError(writer, http.StatusBadRequest, err)
		return
	}
	message, err := server.notifications.DecodeTelegram(
		strings.TrimSpace(request.PathValue("receiver")),
		request.Header.Get("X-Telegram-Bot-Api-Secret-Token"), body,
	)
	server.acceptBotMessage(writer, message, err)
}

func (server *opsServer) handleMattermostBotWebhook(writer http.ResponseWriter, request *http.Request) {
	if server.botGateway == nil || server.notifications == nil {
		http.NotFound(writer, request)
		return
	}
	body, err := readBotWebhookBody(writer, request)
	if err != nil {
		writeOpsError(writer, http.StatusBadRequest, err)
		return
	}
	message, err := server.notifications.DecodeMattermost(
		strings.TrimSpace(request.PathValue("receiver")), request.Header.Get("Content-Type"), body,
	)
	server.acceptBotMessage(writer, message, err)
}

func readBotWebhookBody(writer http.ResponseWriter, request *http.Request) ([]byte, error) {
	request.Body = http.MaxBytesReader(writer, request.Body, 64*1024)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, fmt.Errorf("read bot webhook: %w", err)
	}
	return body, nil
}

func (server *opsServer) acceptBotMessage(writer http.ResponseWriter, message notifications.BotMessage, decodeErr error) {
	if decodeErr != nil {
		switch {
		case errors.Is(decodeErr, notifications.ErrUnknownBotReceiver):
			http.Error(writer, "404 page not found", http.StatusNotFound)
		case errors.Is(decodeErr, notifications.ErrBotUpdateIgnored), errors.Is(decodeErr, notifications.ErrBotForbidden):
			if errors.Is(decodeErr, notifications.ErrBotForbidden) {
				log.Printf("level=warn component=akritas_bot event=ingress_forbidden")
			}
			writeJSON(writer, http.StatusOK, map[string]any{"accepted": false})
		case errors.Is(decodeErr, notifications.ErrBotUnauthorized):
			writeOpsError(writer, http.StatusUnauthorized, decodeErr)
		default:
			writeOpsError(writer, http.StatusBadRequest, decodeErr)
		}
		return
	}
	duplicate, err := server.botGateway.enqueue(message)
	if err != nil {
		writeOpsError(writer, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"accepted": true, "duplicate": duplicate})
}
