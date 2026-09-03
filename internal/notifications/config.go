package notifications

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	ConfigVersion          = 1
	maximumConfigBytes     = 1024 * 1024
	maximumReceivers       = 16
	maximumSecretBytes     = 8 * 1024
	defaultDeliveryTimeout = 10 * time.Second
)

var (
	receiverNamePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	environmentPattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	telegramSecretPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
)

type Config struct {
	Version            int              `json:"version"`
	Timeout            string           `json:"timeout,omitempty"`
	BotQueueSize       int              `json:"bot_queue_size,omitempty"`
	BotHistoryMessages int              `json:"bot_history_messages,omitempty"`
	BotHistoryBytes    int              `json:"bot_history_bytes,omitempty"`
	BotSessionTTL      string           `json:"bot_session_ttl,omitempty"`
	BotMaxSessions     int              `json:"bot_max_sessions,omitempty"`
	Receivers          []ReceiverConfig `json:"receivers"`
}

type ReceiverConfig struct {
	Name                    string   `json:"name"`
	Type                    string   `json:"type"`
	URL                     string   `json:"url,omitempty"`
	URLEnvironment          string   `json:"url_env,omitempty"`
	BaseURL                 string   `json:"base_url,omitempty"`
	BaseURLEnvironment      string   `json:"base_url_env,omitempty"`
	BearerTokenEnv          string   `json:"bearer_token_env,omitempty"`
	HMACSecretEnv           string   `json:"hmac_secret_env,omitempty"`
	BotTokenEnv             string   `json:"bot_token_env,omitempty"`
	ChatID                  string   `json:"chat_id,omitempty"`
	ChatIDEnvironment       string   `json:"chat_id_env,omitempty"`
	MessageThreadID         int64    `json:"message_thread_id,omitempty"`
	MessageThreadIDEnv      string   `json:"message_thread_id_env,omitempty"`
	ChannelID               string   `json:"channel_id,omitempty"`
	ChannelIDEnvironment    string   `json:"channel_id_env,omitempty"`
	InboundMode             string   `json:"inbound_mode,omitempty"`
	InboundSecretEnv        string   `json:"inbound_secret_env,omitempty"`
	AllowedConversations    []string `json:"allowed_conversation_ids,omitempty"`
	AllowedConversationsEnv string   `json:"allowed_conversation_ids_env,omitempty"`
	AllowedUsers            []string `json:"allowed_user_ids,omitempty"`
	AllowedUsersEnv         string   `json:"allowed_user_ids_env,omitempty"`
}

type resolvedConfig struct {
	timeout   time.Duration
	receivers []receiver
	bot       BotSettings
}

func Load(path string, lookup func(string) (string, bool)) (*Dispatcher, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	config, err := loadConfig(path)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveConfig(config, lookup)
	if err != nil {
		return nil, err
	}
	return newDispatcher(resolved, nil), nil
}

func loadConfig(path string) (Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Config{}, fmt.Errorf("stat notification config %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return Config{}, fmt.Errorf("notification config %q is not a regular file", path)
	}
	if info.Size() <= 0 || info.Size() > maximumConfigBytes {
		return Config{}, fmt.Errorf("notification config %q must contain 1..%d bytes", path, maximumConfigBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read notification config %q: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode notification config %q: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("notification config %q contains multiple JSON values", path)
		}
		return Config{}, fmt.Errorf("decode trailing notification config %q: %w", path, err)
	}
	return config, nil
}

func resolveConfig(config Config, lookup func(string) (string, bool)) (resolvedConfig, error) {
	if config.Version != ConfigVersion {
		return resolvedConfig{}, fmt.Errorf("unsupported notification config version %d", config.Version)
	}
	if len(config.Receivers) == 0 || len(config.Receivers) > maximumReceivers {
		return resolvedConfig{}, fmt.Errorf("notification config requires 1..%d receivers", maximumReceivers)
	}
	timeout := defaultDeliveryTimeout
	if strings.TrimSpace(config.Timeout) != "" {
		parsed, err := time.ParseDuration(strings.TrimSpace(config.Timeout))
		if err != nil || parsed <= 0 || parsed > time.Minute {
			return resolvedConfig{}, fmt.Errorf("notification timeout must be a duration between 1ns and 1m")
		}
		timeout = parsed
	}
	bot, err := resolveBotSettings(config)
	if err != nil {
		return resolvedConfig{}, err
	}
	resolved := resolvedConfig{timeout: timeout, receivers: make([]receiver, 0, len(config.Receivers)), bot: bot}
	names := make(map[string]bool, len(config.Receivers))
	for index, item := range config.Receivers {
		if !receiverNamePattern.MatchString(item.Name) || names[item.Name] {
			return resolvedConfig{}, fmt.Errorf("receivers[%d] has invalid or duplicate name %q", index, item.Name)
		}
		names[item.Name] = true
		configured, err := resolveReceiver(item, lookup)
		if err != nil {
			return resolvedConfig{}, fmt.Errorf("receiver %q: %w", item.Name, err)
		}
		resolved.receivers = append(resolved.receivers, configured)
	}
	return resolved, nil
}

func resolveReceiver(config ReceiverConfig, lookup func(string) (string, bool)) (receiver, error) {
	switch config.Type {
	case "webhook":
		if hasAny(config.BaseURL, config.BaseURLEnvironment, config.BotTokenEnv, config.ChatID, config.ChatIDEnvironment, config.MessageThreadIDEnv, config.ChannelID, config.ChannelIDEnvironment, config.InboundMode, config.InboundSecretEnv, config.AllowedConversationsEnv, config.AllowedUsersEnv) || config.MessageThreadID != 0 || len(config.AllowedConversations) > 0 || len(config.AllowedUsers) > 0 {
			return nil, fmt.Errorf("contains fields that are not valid for webhook")
		}
		endpoint, err := resolveLiteralOrEnvironment("url", config.URL, config.URLEnvironment, lookup, true)
		if err != nil {
			return nil, err
		}
		if err := validateHTTPURL(endpoint, false); err != nil {
			return nil, fmt.Errorf("url: %w", err)
		}
		bearer, err := resolveOptionalSecret(config.BearerTokenEnv, lookup)
		if err != nil {
			return nil, fmt.Errorf("bearer_token_env: %w", err)
		}
		secret, err := resolveOptionalSecret(config.HMACSecretEnv, lookup)
		if err != nil {
			return nil, fmt.Errorf("hmac_secret_env: %w", err)
		}
		return &webhookReceiver{name: config.Name, endpoint: endpoint, bearerToken: bearer, hmacSecret: secret}, nil
	case "telegram":
		if hasAny(config.URL, config.URLEnvironment, config.BearerTokenEnv, config.HMACSecretEnv, config.ChannelID, config.ChannelIDEnvironment) {
			return nil, fmt.Errorf("contains fields that are not valid for telegram")
		}
		token, err := resolveRequiredSecret(config.BotTokenEnv, lookup)
		if err != nil {
			return nil, fmt.Errorf("bot_token_env: %w", err)
		}
		chatID, err := resolveLiteralOrEnvironment("chat_id", config.ChatID, config.ChatIDEnvironment, lookup, true)
		if err != nil {
			return nil, err
		}
		baseURL, err := resolveLiteralOrEnvironment("base_url", config.BaseURL, config.BaseURLEnvironment, lookup, false)
		if err != nil {
			return nil, err
		}
		if baseURL == "" {
			baseURL = "https://api.telegram.org"
		}
		if err := validateHTTPURL(baseURL, true); err != nil {
			return nil, fmt.Errorf("base_url: %w", err)
		}
		threadID, err := resolvePositiveInteger("message_thread_id", config.MessageThreadID, config.MessageThreadIDEnv, lookup)
		if err != nil {
			return nil, err
		}
		access, err := resolveBotAccess(config, lookup, true, chatID)
		if err != nil {
			return nil, err
		}
		return &telegramReceiver{name: config.Name, baseURL: strings.TrimRight(baseURL, "/"), botToken: token, chatID: chatID, messageThreadID: threadID, access: access}, nil
	case "mattermost":
		if hasAny(config.URL, config.URLEnvironment, config.BearerTokenEnv, config.HMACSecretEnv, config.ChatID, config.ChatIDEnvironment, config.MessageThreadIDEnv) || config.MessageThreadID != 0 {
			return nil, fmt.Errorf("contains fields that are not valid for mattermost")
		}
		baseURL, err := resolveLiteralOrEnvironment("base_url", config.BaseURL, config.BaseURLEnvironment, lookup, true)
		if err != nil {
			return nil, err
		}
		if err := validateHTTPURL(baseURL, true); err != nil {
			return nil, fmt.Errorf("base_url: %w", err)
		}
		token, err := resolveRequiredSecret(config.BotTokenEnv, lookup)
		if err != nil {
			return nil, fmt.Errorf("bot_token_env: %w", err)
		}
		channelID, err := resolveLiteralOrEnvironment("channel_id", config.ChannelID, config.ChannelIDEnvironment, lookup, true)
		if err != nil {
			return nil, err
		}
		access, err := resolveBotAccess(config, lookup, false, "")
		if err != nil {
			return nil, err
		}
		return &mattermostReceiver{name: config.Name, baseURL: strings.TrimRight(baseURL, "/"), botToken: token, channelID: channelID, access: access}, nil
	default:
		return nil, fmt.Errorf("unsupported type %q", config.Type)
	}
}

func resolveBotSettings(config Config) (BotSettings, error) {
	settings := BotSettings{
		QueueSize: 64, HistoryMessages: 20, HistoryBytes: 64 * 1024,
		SessionTTL: 24 * time.Hour, MaxSessions: 256,
	}
	if config.BotQueueSize != 0 {
		settings.QueueSize = config.BotQueueSize
	}
	if config.BotHistoryMessages != 0 {
		settings.HistoryMessages = config.BotHistoryMessages
	}
	if config.BotHistoryBytes != 0 {
		settings.HistoryBytes = config.BotHistoryBytes
	}
	if config.BotMaxSessions != 0 {
		settings.MaxSessions = config.BotMaxSessions
	}
	if strings.TrimSpace(config.BotSessionTTL) != "" {
		parsed, err := time.ParseDuration(strings.TrimSpace(config.BotSessionTTL))
		if err != nil {
			return BotSettings{}, fmt.Errorf("bot_session_ttl must be a valid duration")
		}
		settings.SessionTTL = parsed
	}
	if settings.QueueSize < 1 || settings.QueueSize > 1024 {
		return BotSettings{}, fmt.Errorf("bot_queue_size must be between 1 and 1024")
	}
	if settings.HistoryMessages < 2 || settings.HistoryMessages > 64 {
		return BotSettings{}, fmt.Errorf("bot_history_messages must be between 2 and 64")
	}
	if settings.HistoryBytes < 1024 || settings.HistoryBytes > 256*1024 {
		return BotSettings{}, fmt.Errorf("bot_history_bytes must be between 1024 and 262144")
	}
	if settings.SessionTTL < time.Minute || settings.SessionTTL > 30*24*time.Hour {
		return BotSettings{}, fmt.Errorf("bot_session_ttl must be between 1m and 720h")
	}
	if settings.MaxSessions < 1 || settings.MaxSessions > 4096 {
		return BotSettings{}, fmt.Errorf("bot_max_sessions must be between 1 and 4096")
	}
	return settings, nil
}

func resolveBotAccess(config ReceiverConfig, lookup func(string) (string, bool), telegram bool, defaultConversation string) (botAccess, error) {
	mode := strings.ToLower(strings.TrimSpace(config.InboundMode))
	secretConfigured := strings.TrimSpace(config.InboundSecretEnv) != ""
	allowlistConfigured := len(config.AllowedConversations) > 0 || strings.TrimSpace(config.AllowedConversationsEnv) != "" ||
		len(config.AllowedUsers) > 0 || strings.TrimSpace(config.AllowedUsersEnv) != ""
	if mode == "" {
		if secretConfigured {
			mode = botInboundWebhook
		} else if allowlistConfigured {
			return botAccess{}, fmt.Errorf("allowed inbound IDs require inbound_mode or inbound_secret_env")
		} else {
			return botAccess{}, nil
		}
	}
	if mode != botInboundWebhook && mode != botInboundPolling {
		return botAccess{}, fmt.Errorf("inbound_mode must be %q or %q", botInboundWebhook, botInboundPolling)
	}
	if !telegram && mode != botInboundWebhook {
		return botAccess{}, fmt.Errorf("Mattermost inbound_mode must be %q", botInboundWebhook)
	}
	if mode == botInboundPolling && secretConfigured {
		return botAccess{}, fmt.Errorf("inbound_secret_env is not valid for Telegram polling")
	}
	secret := ""
	if mode == botInboundWebhook {
		var err error
		secret, err = resolveRequiredSecret(config.InboundSecretEnv, lookup)
		if err != nil {
			return botAccess{}, fmt.Errorf("inbound_secret_env: %w", err)
		}
		if telegram && !telegramSecretPattern.MatchString(secret) {
			return botAccess{}, fmt.Errorf("Telegram inbound secret must contain 1..256 letters, digits, underscores, or hyphens")
		}
	}
	conversations, err := resolveStringSet("allowed_conversation_ids", config.AllowedConversations, config.AllowedConversationsEnv, lookup)
	if err != nil {
		return botAccess{}, err
	}
	users, err := resolveStringSet("allowed_user_ids", config.AllowedUsers, config.AllowedUsersEnv, lookup)
	if err != nil {
		return botAccess{}, err
	}
	if telegram && mode == botInboundPolling && len(conversations) == 0 && len(users) == 0 && defaultConversation != "" {
		conversations[defaultConversation] = true
	}
	if len(conversations) == 0 && len(users) == 0 {
		return botAccess{}, fmt.Errorf("inbound bots require allowed_conversation_ids or allowed_user_ids")
	}
	if !telegram && len(users) == 0 {
		return botAccess{}, fmt.Errorf("Mattermost inbound requires allowed_user_ids to prevent bot reply loops")
	}
	return botAccess{mode: mode, secret: secret, conversations: conversations, users: users}, nil
}

func resolveStringSet(name string, literal []string, environment string, lookup func(string) (string, bool)) (map[string]bool, error) {
	if len(literal) > 0 && strings.TrimSpace(environment) != "" {
		return nil, fmt.Errorf("%s and %s_env are mutually exclusive", name, name)
	}
	values := append([]string(nil), literal...)
	if strings.TrimSpace(environment) != "" {
		if !environmentPattern.MatchString(strings.TrimSpace(environment)) {
			return nil, fmt.Errorf("%s_env has invalid environment variable name %q", name, environment)
		}
		raw, exists := lookup(strings.TrimSpace(environment))
		if !exists || strings.TrimSpace(raw) == "" {
			return nil, fmt.Errorf("environment variable %s is empty or missing", strings.TrimSpace(environment))
		}
		if strings.HasPrefix(strings.TrimSpace(raw), "[") {
			if err := json.Unmarshal([]byte(raw), &values); err != nil {
				return nil, fmt.Errorf("environment variable %s must be a JSON array or comma-separated list", strings.TrimSpace(environment))
			}
		} else {
			values = strings.Split(raw, ",")
		}
	}
	if len(values) > 256 {
		return nil, fmt.Errorf("%s exceeds 256 entries", name)
	}
	result := make(map[string]bool, len(values))
	for index, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 256 || result[value] {
			return nil, fmt.Errorf("%s[%d] is empty, too long, or duplicated", name, index)
		}
		result[value] = true
	}
	return result, nil
}

func resolveLiteralOrEnvironment(name, literal, environment string, lookup func(string) (string, bool), required bool) (string, error) {
	literal = strings.TrimSpace(literal)
	environment = strings.TrimSpace(environment)
	if literal != "" && environment != "" {
		return "", fmt.Errorf("%s and %s_env are mutually exclusive", name, name)
	}
	value := literal
	if environment != "" {
		if !environmentPattern.MatchString(environment) {
			return "", fmt.Errorf("%s_env has invalid environment variable name %q", name, environment)
		}
		resolved, exists := lookup(environment)
		if !exists || strings.TrimSpace(resolved) == "" {
			return "", fmt.Errorf("environment variable %s is empty or missing", environment)
		}
		value = strings.TrimSpace(resolved)
	}
	if required && value == "" {
		return "", fmt.Errorf("%s or %s_env is required", name, name)
	}
	if len(value) > maximumSecretBytes {
		return "", fmt.Errorf("%s exceeds %d bytes", name, maximumSecretBytes)
	}
	return value, nil
}

func resolveRequiredSecret(environment string, lookup func(string) (string, bool)) (string, error) {
	environment = strings.TrimSpace(environment)
	if !environmentPattern.MatchString(environment) {
		return "", fmt.Errorf("a valid environment variable name is required")
	}
	value, exists := lookup(environment)
	value = strings.TrimSpace(value)
	if !exists || value == "" {
		return "", fmt.Errorf("environment variable %s is empty or missing", environment)
	}
	if len(value) > maximumSecretBytes {
		return "", fmt.Errorf("environment variable %s exceeds %d bytes", environment, maximumSecretBytes)
	}
	return value, nil
}

func resolveOptionalSecret(environment string, lookup func(string) (string, bool)) (string, error) {
	if strings.TrimSpace(environment) == "" {
		return "", nil
	}
	return resolveRequiredSecret(environment, lookup)
}

func resolvePositiveInteger(name string, literal int64, environment string, lookup func(string) (string, bool)) (int64, error) {
	environment = strings.TrimSpace(environment)
	if literal != 0 && environment != "" {
		return 0, fmt.Errorf("%s and %s_env are mutually exclusive", name, name)
	}
	if environment == "" {
		if literal < 0 {
			return 0, fmt.Errorf("%s must be positive", name)
		}
		return literal, nil
	}
	if !environmentPattern.MatchString(environment) {
		return 0, fmt.Errorf("%s_env has invalid environment variable name %q", name, environment)
	}
	value, exists := lookup(environment)
	if !exists || strings.TrimSpace(value) == "" {
		return 0, fmt.Errorf("environment variable %s is empty or missing", environment)
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("environment variable %s must be a positive integer", environment)
	}
	return parsed, nil
}

func validateHTTPURL(value string, base bool) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("must not contain user information or a fragment")
	}
	if base && (parsed.RawQuery != "" || parsed.RawFragment != "") {
		return fmt.Errorf("base URL must not contain a query or fragment")
	}
	return nil
}

func hasAny(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}
