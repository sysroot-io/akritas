package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

const (
	telegramLongPollSeconds     = 30
	telegramLongPollHTTPTimeout = 40 * time.Second
	maximumPollingResponseBytes = 1024 * 1024
	minimumEmptyPollDelay       = 100 * time.Millisecond
	minimumPollingRetryDelay    = time.Second
	maximumPollingRetryDelay    = 30 * time.Second
)

// TelegramPollingReceiverNames returns configured Telegram receivers that use
// getUpdates instead of a webhook.
func (dispatcher *Dispatcher) TelegramPollingReceiverNames() []string {
	if dispatcher == nil {
		return nil
	}
	names := make([]string, 0)
	for _, configured := range dispatcher.receivers {
		if receiver, ok := configured.(*telegramReceiver); ok && receiver.access.polling() {
			names = append(names, receiver.name)
		}
	}
	sort.Strings(names)
	return names
}

// PollTelegram receives Telegram updates until ctx is canceled. Accepted
// updates are only acknowledged after accept succeeds, which gives queueing
// failures at-least-once delivery semantics within a running process.
func (dispatcher *Dispatcher) PollTelegram(ctx context.Context, receiverName string, accept func(BotMessage) error, report func(err error)) error {
	if accept == nil {
		return fmt.Errorf("Telegram polling requires an update handler")
	}
	configured, err := dispatcher.botReceiver(receiverName, "telegram")
	if err != nil {
		return err
	}
	receiver := configured.(*telegramReceiver)
	if !receiver.access.polling() {
		return ErrUnknownBotReceiver
	}
	offset := int64(0)
	retryDelay := minimumPollingRetryDelay
	for {
		updates, err := receiver.getUpdates(ctx, dispatcher.client, offset)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if report != nil {
				report(err)
			}
			if !waitForPolling(ctx, retryDelay) {
				return nil
			}
			retryDelay *= 2
			if retryDelay > maximumPollingRetryDelay {
				retryDelay = maximumPollingRetryDelay
			}
			continue
		}
		retryDelay = minimumPollingRetryDelay
		if len(updates) == 0 {
			if !waitForPolling(ctx, minimumEmptyPollDelay) {
				return nil
			}
			continue
		}
		for _, raw := range updates {
			message, updateID, decodeErr := decodeTelegramUpdate(receiverName, raw)
			if updateID < offset {
				continue
			}
			if decodeErr == nil {
				decodeErr = receiver.access.authorizedActor(message.ConversationID, message.UserID)
			}
			if decodeErr != nil {
				if updateID < 0 {
					return fmt.Errorf("decode Telegram polling update: %w", decodeErr)
				}
				if !errors.Is(decodeErr, ErrBotUpdateIgnored) && !errors.Is(decodeErr, ErrBotForbidden) && report != nil {
					report(decodeErr)
				}
				offset = updateID + 1
				continue
			}
			reportedAcceptError := false
			for {
				if err := accept(message); err == nil {
					break
				} else if report != nil && !reportedAcceptError {
					report(err)
					reportedAcceptError = true
				}
				if !waitForPolling(ctx, minimumPollingRetryDelay) {
					return nil
				}
			}
			offset = updateID + 1
		}
	}
}

func (receiver *telegramReceiver) getUpdates(ctx context.Context, client *http.Client, offset int64) ([]json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{
		"offset": offset, "timeout": telegramLongPollSeconds, "allowed_updates": []string{"message"},
	})
	if err != nil {
		return nil, fmt.Errorf("encode Telegram polling request: %w", err)
	}
	requestContext, cancel := context.WithTimeout(ctx, telegramLongPollHTTPTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestContext, http.MethodPost, receiver.baseURL+"/bot"+receiver.botToken+"/getUpdates", bytes.NewReader(body),
	)
	if err != nil {
		return nil, errors.New("create Telegram polling request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Akritas/telegram-polling")
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if requestContext.Err() != nil {
			return nil, errors.New("Telegram getUpdates timed out")
		}
		return nil, errors.New("Telegram getUpdates request failed")
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maximumPollingResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Telegram polling response: %w", err)
	}
	if len(responseBody) > maximumPollingResponseBytes {
		return nil, fmt.Errorf("Telegram polling response exceeds %d bytes", maximumPollingResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Telegram getUpdates returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		OK     bool              `json:"ok"`
		Result []json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return nil, fmt.Errorf("decode Telegram polling response: %w", err)
	}
	if !payload.OK {
		return nil, errors.New("Telegram rejected getUpdates")
	}
	return payload.Result, nil
}

func waitForPolling(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
