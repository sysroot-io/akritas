package openai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"akritas/internal/mcp"
	"akritas/internal/runbudget"
)

const maxOpenAIToolResponseBytes = 4 * 1024 * 1024

type openAITokenLimitParameter uint32

const (
	openAITokenLimitMaxTokens openAITokenLimitParameter = iota + 1
	openAITokenLimitMaxCompletionTokens
)

type openAIToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Arguments   string          `json:"arguments,omitempty"`
}

type openAIToolSpec struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolMessage struct {
	Role                  string           `json:"role"`
	Content               *string          `json:"content"`
	Name                  string           `json:"name,omitempty"`
	ToolCallID            string           `json:"tool_call_id,omitempty"`
	ToolCalls             []openAIToolCall `json:"tool_calls,omitempty"`
	FinishReason          string           `json:"-"`
	CompletionTokens      int              `json:"-"`
	CompletionTokensKnown bool             `json:"-"`
}

type openAIToolRequest struct {
	Model               string              `json:"model"`
	Messages            []openAIToolMessage `json:"messages"`
	Tools               []openAIToolSpec    `json:"tools,omitempty"`
	ToolChoice          string              `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool               `json:"parallel_tool_calls,omitempty"`
	MaxTokens           *int                `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                `json:"max_completion_tokens,omitempty"`
	Temperature         float64             `json:"temperature"`
	Stream              bool                `json:"stream"`
}

type openAIToolResponse struct {
	Choices []struct {
		Message      openAIToolMessage `json:"message"`
		FinishReason string            `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
	Error *openAIError `json:"error,omitempty"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Param   string `json:"param,omitempty"`
	Code    string `json:"code,omitempty"`
}

type openAIErrorEnvelope struct {
	Error openAIError `json:"error"`
}

type openAIModelList struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

type openAIToolClient struct {
	BaseURL             string
	Model               string
	APIKey              string
	HTTPClient          *http.Client
	systemInstructions  string
	tokenLimitParameter atomic.Uint32
}

type openAIToolLoopResult struct {
	Answer  string
	Calls   []mcp.ToolCall
	Results []mcp.ToolResult
	History []openAIToolMessage
	Skills  []string
	Tracker *runbudget.Tracker
}

type Client = openAIToolClient
type ToolCall = openAIToolCall
type ToolFunction = openAIToolFunction
type ToolMessage = openAIToolMessage
type ToolRequest = openAIToolRequest
type ToolSpec = openAIToolSpec
type ToolLoopResult = openAIToolLoopResult

func NewToolClient(baseURL, model, apiKey string, client *http.Client) (*Client, error) {
	return newOpenAIToolClient(baseURL, model, apiKey, client)
}

// SetSystemInstructions configures host-owned instructions that are prepended
// to the first system message of every model request.
func (client *openAIToolClient) SetSystemInstructions(value string) error {
	if client == nil {
		return fmt.Errorf("OpenAI client is nil")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("system instructions are empty")
	}
	client.systemInstructions = value
	return nil
}

func newOpenAIToolClient(baseURL, model, apiKey string, client *http.Client) (*openAIToolClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("OpenAI base URL must be an absolute http(s) URL")
	}
	if !strings.HasSuffix(parsed.Path, "/v1") {
		return nil, fmt.Errorf("OpenAI base URL must end with /v1")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &openAIToolClient{
		BaseURL: baseURL, Model: strings.TrimSpace(model), APIKey: apiKey,
		HTTPClient: client,
	}, nil
}

func (client *openAIToolClient) discoverModel(ctx context.Context) error {
	if client.Model != "" {
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.BaseURL+"/models", nil)
	if err != nil {
		return err
	}
	client.authorize(request)
	response, err := client.HTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("list OpenAI models: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxOpenAIToolResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read OpenAI models response: %w", err)
	}
	if len(body) > maxOpenAIToolResponseBytes {
		return fmt.Errorf("OpenAI models response exceeds %d bytes", maxOpenAIToolResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("list OpenAI models: HTTP %d: %s", response.StatusCode, compactHTTPError(body))
	}
	var models openAIModelList
	if err := json.Unmarshal(body, &models); err != nil {
		return fmt.Errorf("decode OpenAI models response: %w", err)
	}
	if len(models.Data) == 0 || strings.TrimSpace(models.Data[0].ID) == "" {
		return fmt.Errorf("OpenAI endpoint returned no models")
	}
	client.Model = models.Data[0].ID
	return nil
}

func (client *openAIToolClient) DiscoverModel(ctx context.Context) error {
	return client.discoverModel(ctx)
}

func (client *openAIToolClient) complete(
	ctx context.Context,
	messages []openAIToolMessage,
	tools []openAIToolSpec,
	maxTokens int,
	temperature float64,
) (openAIToolMessage, error) {
	toolChoice := ""
	if len(tools) > 0 {
		toolChoice = "auto"
	}
	return client.completeWithToolChoice(
		ctx, messages, tools, toolChoice, maxTokens, temperature,
	)
}

func (client *openAIToolClient) completeWithToolChoice(
	ctx context.Context,
	messages []openAIToolMessage,
	tools []openAIToolSpec,
	toolChoice string,
	maxTokens int,
	temperature float64,
) (openAIToolMessage, error) {
	messages = client.withSystemInstructions(messages)
	return client.completeWithToolChoicePrepared(
		ctx, messages, tools, toolChoice, maxTokens, temperature,
	)
}

func (client *openAIToolClient) completeWithToolChoicePrepared(
	ctx context.Context,
	messages []openAIToolMessage,
	tools []openAIToolSpec,
	toolChoice string,
	maxTokens int,
	temperature float64,
) (openAIToolMessage, error) {
	parallel := false
	input := openAIToolRequest{
		Model: client.Model, Messages: messages, Tools: tools,
		Temperature: temperature, Stream: false,
	}
	if len(tools) > 0 {
		input.ToolChoice = toolChoice
		input.ParallelToolCalls = &parallel
	}
	parameter := client.preferredTokenLimitParameter()
	for attempt := 0; attempt < 2; attempt++ {
		input.setTokenLimit(parameter, maxTokens)
		status, responseBody, err := client.sendChatCompletion(ctx, input)
		if err != nil {
			return openAIToolMessage{}, err
		}
		if status < 200 || status >= 300 {
			if attempt == 0 && rejectsOpenAITokenLimitParameter(status, responseBody, parameter) {
				parameter = parameter.alternate()
				client.tokenLimitParameter.Store(uint32(parameter))
				continue
			}
			return openAIToolMessage{}, fmt.Errorf(
				"OpenAI chat completions: HTTP %d: %s", status, compactHTTPError(responseBody),
			)
		}
		var output openAIToolResponse
		if err := json.Unmarshal(responseBody, &output); err != nil {
			return openAIToolMessage{}, fmt.Errorf("decode OpenAI chat response: %w", err)
		}
		if len(output.Choices) != 1 {
			return openAIToolMessage{}, fmt.Errorf("OpenAI endpoint returned %d choices, want 1", len(output.Choices))
		}
		message := output.Choices[0].Message
		message.FinishReason = output.Choices[0].FinishReason
		if output.Usage != nil {
			message.CompletionTokens = output.Usage.CompletionTokens
			message.CompletionTokensKnown = true
		}
		return message, nil
	}
	return openAIToolMessage{}, fmt.Errorf("OpenAI token-limit parameter negotiation failed")
}

func (client *openAIToolClient) withSystemInstructions(messages []openAIToolMessage) []openAIToolMessage {
	if client == nil || client.systemInstructions == "" {
		return messages
	}
	prepared := make([]openAIToolMessage, 0, len(messages)+1)
	if len(messages) > 0 && messages[0].Role == "system" {
		prepared = append(prepared, messages...)
		content := client.systemInstructions
		if messages[0].Content != nil && strings.TrimSpace(*messages[0].Content) != "" {
			content += "\n\n" + strings.TrimSpace(*messages[0].Content)
		}
		prepared[0].Content = &content
		return prepared
	}
	content := client.systemInstructions
	prepared = append(prepared, openAIToolMessage{Role: "system", Content: &content})
	prepared = append(prepared, messages...)
	return prepared
}

func (request *openAIToolRequest) setTokenLimit(parameter openAITokenLimitParameter, value int) {
	request.MaxTokens = nil
	request.MaxCompletionTokens = nil
	if parameter == openAITokenLimitMaxCompletionTokens {
		request.MaxCompletionTokens = &value
		return
	}
	request.MaxTokens = &value
}

func (client *openAIToolClient) preferredTokenLimitParameter() openAITokenLimitParameter {
	parameter := openAITokenLimitParameter(client.tokenLimitParameter.Load())
	if parameter == openAITokenLimitMaxCompletionTokens {
		return parameter
	}
	return openAITokenLimitMaxTokens
}

func (parameter openAITokenLimitParameter) alternate() openAITokenLimitParameter {
	if parameter == openAITokenLimitMaxCompletionTokens {
		return openAITokenLimitMaxTokens
	}
	return openAITokenLimitMaxCompletionTokens
}

func (parameter openAITokenLimitParameter) field() string {
	if parameter == openAITokenLimitMaxCompletionTokens {
		return "max_completion_tokens"
	}
	return "max_tokens"
}

func (client *openAIToolClient) sendChatCompletion(
	ctx context.Context,
	input openAIToolRequest,
) (int, []byte, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return 0, nil, err
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, client.BaseURL+"/chat/completions", bytes.NewReader(body),
	)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	client.authorize(request)
	response, err := client.HTTPClient.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("call OpenAI chat completions: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxOpenAIToolResponseBytes+1))
	if err != nil {
		return 0, nil, fmt.Errorf("read OpenAI chat response: %w", err)
	}
	if len(responseBody) > maxOpenAIToolResponseBytes {
		return 0, nil, fmt.Errorf("OpenAI chat response exceeds %d bytes", maxOpenAIToolResponseBytes)
	}
	return response.StatusCode, responseBody, nil
}

func rejectsOpenAITokenLimitParameter(
	status int,
	body []byte,
	parameter openAITokenLimitParameter,
) bool {
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return false
	}
	var envelope openAIErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return false
	}
	field := parameter.field()
	message := strings.ToLower(envelope.Error.Message)
	if envelope.Error.Param != "" && envelope.Error.Param != field {
		return false
	}
	if envelope.Error.Param != field && !strings.Contains(message, field) {
		return false
	}
	if envelope.Error.Code == "unsupported_parameter" {
		return true
	}
	for _, marker := range []string{
		"unsupported parameter", "not supported", "unknown parameter", "unrecognized parameter",
		"extra fields not permitted", "extra inputs are not permitted",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (client *openAIToolClient) completeWithToolChoiceBudgeted(
	ctx context.Context,
	messages []openAIToolMessage,
	tools []openAIToolSpec,
	toolChoice string,
	maxTokens int,
	temperature float64,
	tracker *runbudget.Tracker,
) (openAIToolMessage, error) {
	if tracker == nil {
		return client.completeWithToolChoice(ctx, messages, tools, toolChoice, maxTokens, temperature)
	}
	messages = client.withSystemInstructions(messages)
	contextPayload, err := json.Marshal(struct {
		Messages []openAIToolMessage `json:"messages"`
		Tools    []openAIToolSpec    `json:"tools,omitempty"`
	}{Messages: messages, Tools: tools})
	if err != nil {
		return openAIToolMessage{}, fmt.Errorf("encode budgeted model context: %w", err)
	}
	if err := tracker.RecordContextBytes(len(contextPayload)); err != nil {
		return openAIToolMessage{}, err
	}
	reservation, allowed, err := tracker.BeginModelCall(maxTokens)
	if err != nil {
		return openAIToolMessage{}, err
	}
	message, callErr := client.completeWithToolChoicePrepared(ctx, messages, tools, toolChoice, allowed, temperature)
	reported := -1
	if callErr == nil && message.CompletionTokensKnown {
		reported = message.CompletionTokens
	}
	if finishErr := tracker.FinishModelCall(reservation, reported); finishErr != nil {
		return openAIToolMessage{}, finishErr
	}
	return message, callErr
}

func (client *openAIToolClient) CompleteWithToolChoice(
	ctx context.Context,
	messages []ToolMessage,
	tools []ToolSpec,
	toolChoice string,
	maxTokens int,
	temperature float64,
) (ToolMessage, error) {
	return client.completeWithToolChoice(ctx, messages, tools, toolChoice, maxTokens, temperature)
}

func (client *openAIToolClient) CompleteWithToolChoiceBudgeted(
	ctx context.Context,
	messages []ToolMessage,
	tools []ToolSpec,
	toolChoice string,
	maxTokens int,
	temperature float64,
	tracker *runbudget.Tracker,
) (ToolMessage, error) {
	return client.completeWithToolChoiceBudgeted(
		ctx, messages, tools, toolChoice, maxTokens, temperature, tracker,
	)
}

func (client *openAIToolClient) authorize(request *http.Request) {
	if client.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+client.APIKey)
	}
}

func runOpenAIToolLoop(
	ctx context.Context,
	client *openAIToolClient,
	history []openAIToolMessage,
	registry *mcp.ToolRegistry,
	policy mcp.ToolAuthorizationPolicy,
	maxCalls int,
	maxTokens int,
	temperature float64,
	tracker *runbudget.Tracker,
	observeToolResult func([]openAIToolMessage, mcp.ToolCall, mcp.ToolResult) ([]openAIToolMessage, error),
) (openAIToolLoopResult, error) {
	if client == nil || registry == nil || policy == nil {
		return openAIToolLoopResult{}, fmt.Errorf("OpenAI tool loop requires client, registry and policy")
	}
	if len(history) == 0 || maxCalls < 0 || maxTokens <= 0 || temperature < 0 {
		return openAIToolLoopResult{}, fmt.Errorf("OpenAI tool loop has invalid limits or empty history")
	}
	tools, aliases := buildOpenAITools(registry.Definitions())
	if maxCalls == 0 {
		tools = nil
	}
	result := openAIToolLoopResult{History: append([]openAIToolMessage(nil), history...), Tracker: tracker}
	for {
		message, err := client.completeWithToolChoiceBudgeted(
			ctx, result.History, tools, toolChoiceForTools(tools), maxTokens, temperature, tracker,
		)
		if err != nil {
			return openAIToolLoopResult{}, err
		}
		if len(message.ToolCalls) == 0 {
			if message.Content == nil || strings.TrimSpace(*message.Content) == "" {
				return openAIToolLoopResult{}, fmt.Errorf("OpenAI model returned neither content nor tool calls")
			}
			result.Answer = strings.TrimSpace(*message.Content)
			result.History = append(result.History, message)
			return result, nil
		}
		if len(result.Calls)+len(message.ToolCalls) > maxCalls {
			return openAIToolLoopResult{}, fmt.Errorf("tool loop exceeded %d calls", maxCalls)
		}
		for index := range message.ToolCalls {
			if !mcp.IsValidToolName(message.ToolCalls[index].ID) {
				message.ToolCalls[index].ID = fmt.Sprintf(
					"call_%d", len(result.Calls)+index+1,
				)
			}
		}
		result.History = append(result.History, message)
		for _, externalCall := range message.ToolCalls {
			if externalCall.Type != "function" {
				return openAIToolLoopResult{}, fmt.Errorf("unsupported OpenAI tool call type %q", externalCall.Type)
			}
			internalName, exists := aliases[externalCall.Function.Name]
			if !exists {
				return openAIToolLoopResult{}, fmt.Errorf("model requested unknown tool %q", externalCall.Function.Name)
			}
			call := mcp.ToolCall{
				ID: externalCall.ID, Name: internalName,
				Arguments: json.RawMessage(externalCall.Function.Arguments),
			}
			if tracker != nil {
				if err := tracker.RecordToolCall(); err != nil {
					return openAIToolLoopResult{}, err
				}
			}
			toolResult := registry.Execute(ctx, call, policy)
			serialized, err := json.Marshal(toolResult)
			if err != nil {
				return openAIToolLoopResult{}, fmt.Errorf("encode tool result: %w", err)
			}
			if tracker != nil {
				if err := tracker.RecordToolResult(len(serialized), false); err != nil {
					return openAIToolLoopResult{}, err
				}
			}
			content := string(serialized)
			result.History = append(result.History, openAIToolMessage{
				Role: "tool", Content: &content, Name: externalCall.Function.Name,
				ToolCallID: externalCall.ID,
			})
			result.Calls = append(result.Calls, call)
			result.Results = append(result.Results, toolResult)
			if observeToolResult != nil {
				result.History, err = observeToolResult(result.History, call, toolResult)
				if err != nil {
					return openAIToolLoopResult{}, fmt.Errorf("observe tool result %q: %w", call.Name, err)
				}
			}
		}
	}
}

func toolChoiceForTools(tools []openAIToolSpec) string {
	if len(tools) == 0 {
		return ""
	}
	return "auto"
}

func buildOpenAITools(definitions []mcp.ToolDefinition) ([]openAIToolSpec, map[string]string) {
	tools := make([]openAIToolSpec, 0, len(definitions))
	aliases := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		alias := openAIToolAlias(definition.Name)
		tools = append(tools, openAIToolSpec{
			Type: "function",
			Function: openAIToolFunction{
				Name:        alias,
				Description: fmt.Sprintf("%s (Akritas tool: %s)", definition.Description, definition.Name),
				Parameters:  append(json.RawMessage(nil), definition.InputSchema...),
			},
		})
		aliases[alias] = definition.Name
	}
	return tools, aliases
}

func RunToolLoop(
	ctx context.Context,
	client *Client,
	history []ToolMessage,
	registry *mcp.ToolRegistry,
	policy mcp.ToolAuthorizationPolicy,
	maxCalls int,
	maxTokens int,
	temperature float64,
) (ToolLoopResult, error) {
	return runOpenAIToolLoop(ctx, client, history, registry, policy, maxCalls, maxTokens, temperature, nil, nil)
}

func RunToolLoopBudgeted(
	ctx context.Context,
	client *Client,
	history []ToolMessage,
	registry *mcp.ToolRegistry,
	policy mcp.ToolAuthorizationPolicy,
	maxCalls int,
	maxTokens int,
	temperature float64,
	tracker *runbudget.Tracker,
) (ToolLoopResult, error) {
	return runOpenAIToolLoop(
		ctx, client, history, registry, policy, maxCalls, maxTokens, temperature, tracker, nil,
	)
}

func RunToolLoopBudgetedObserved(
	ctx context.Context,
	client *Client,
	history []ToolMessage,
	registry *mcp.ToolRegistry,
	policy mcp.ToolAuthorizationPolicy,
	maxCalls int,
	maxTokens int,
	temperature float64,
	tracker *runbudget.Tracker,
	observeToolResult func([]ToolMessage, mcp.ToolCall, mcp.ToolResult) ([]ToolMessage, error),
) (ToolLoopResult, error) {
	return runOpenAIToolLoop(
		ctx, client, history, registry, policy, maxCalls, maxTokens, temperature, tracker,
		observeToolResult,
	)
}

func BuildTools(definitions []mcp.ToolDefinition) ([]ToolSpec, map[string]string) {
	return buildOpenAITools(definitions)
}

func openAIToolAlias(name string) string {
	var builder strings.Builder
	builder.WriteString("akritas_")
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('_')
		}
	}
	digest := sha256.Sum256([]byte(name))
	alias := builder.String()
	if len(alias) > 55 {
		alias = alias[:55]
	}
	return alias + "_" + hex.EncodeToString(digest[:4])
}

func ToolAlias(name string) string { return openAIToolAlias(name) }

func compactHTTPError(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 512 {
		text = text[:512] + "..."
	}
	if text == "" {
		return "empty response"
	}
	return text
}
