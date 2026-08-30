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

	"akritas/internal/mcp"
)

const maxOpenAIToolResponseBytes = 4 * 1024 * 1024

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
	Role             string           `json:"role"`
	Content          *string          `json:"content"`
	Name             string           `json:"name,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
	FinishReason     string           `json:"-"`
	CompletionTokens int              `json:"-"`
}

type openAIToolRequest struct {
	Model             string              `json:"model"`
	Messages          []openAIToolMessage `json:"messages"`
	Tools             []openAIToolSpec    `json:"tools,omitempty"`
	ToolChoice        string              `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool               `json:"parallel_tool_calls,omitempty"`
	MaxTokens         int                 `json:"max_tokens"`
	Temperature       float64             `json:"temperature"`
	Stream            bool                `json:"stream"`
}

type openAIToolResponse struct {
	Choices []struct {
		Message      openAIToolMessage `json:"message"`
		FinishReason string            `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
	Error *openAIError `json:"error,omitempty"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
}

type openAIModelList struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

type openAIToolClient struct {
	BaseURL    string
	Model      string
	APIKey     string
	HTTPClient *http.Client
}

type openAIToolLoopResult struct {
	Answer  string
	Calls   []mcp.ToolCall
	Results []mcp.ToolResult
	History []openAIToolMessage
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
	parallel := false
	input := openAIToolRequest{
		Model: client.Model, Messages: messages, Tools: tools,
		MaxTokens: maxTokens, Temperature: temperature, Stream: false,
	}
	if len(tools) > 0 {
		input.ToolChoice = toolChoice
		input.ParallelToolCalls = &parallel
	}
	body, err := json.Marshal(input)
	if err != nil {
		return openAIToolMessage{}, err
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, client.BaseURL+"/chat/completions", bytes.NewReader(body),
	)
	if err != nil {
		return openAIToolMessage{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	client.authorize(request)
	response, err := client.HTTPClient.Do(request)
	if err != nil {
		return openAIToolMessage{}, fmt.Errorf("call OpenAI chat completions: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxOpenAIToolResponseBytes+1))
	if err != nil {
		return openAIToolMessage{}, fmt.Errorf("read OpenAI chat response: %w", err)
	}
	if len(responseBody) > maxOpenAIToolResponseBytes {
		return openAIToolMessage{}, fmt.Errorf("OpenAI chat response exceeds %d bytes", maxOpenAIToolResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openAIToolMessage{}, fmt.Errorf(
			"OpenAI chat completions: HTTP %d: %s", response.StatusCode, compactHTTPError(responseBody),
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
	message.CompletionTokens = output.Usage.CompletionTokens
	return message, nil
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
	result := openAIToolLoopResult{History: append([]openAIToolMessage(nil), history...)}
	for {
		message, err := client.complete(ctx, result.History, tools, maxTokens, temperature)
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
			toolResult := registry.Execute(ctx, call, policy)
			serialized, err := json.Marshal(toolResult)
			if err != nil {
				return openAIToolLoopResult{}, fmt.Errorf("encode tool result: %w", err)
			}
			content := string(serialized)
			result.History = append(result.History, openAIToolMessage{
				Role: "tool", Content: &content, Name: externalCall.Function.Name,
				ToolCallID: externalCall.ID,
			})
			result.Calls = append(result.Calls, call)
			result.Results = append(result.Results, toolResult)
		}
	}
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
	return runOpenAIToolLoop(ctx, client, history, registry, policy, maxCalls, maxTokens, temperature)
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
