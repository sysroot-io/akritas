package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"akritas/internal/mcp"
)

func TestOpenAIToolLoopDiscoversModelAndExecutesAuthorizedTool(t *testing.T) {
	registry := mcp.NewToolRegistry()
	if err := registerEchoTool(registry); err != nil {
		t.Fatal(err)
	}
	policy := mcp.StaticToolPolicy{Allowed: map[mcp.ToolPermission]bool{
		mcp.ToolPermissionRead: true,
	}}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization=%q", request.Header.Get("Authorization"))
		}
		switch request.URL.Path {
		case "/v1/models":
			writeTestJSON(writer, http.StatusOK, map[string]any{
				"object": "list", "data": []map[string]string{{"id": "qwen-local"}},
			})
		case "/v1/chat/completions":
			requests++
			var input openAIToolRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Errorf("decode request: %v", err)
			}
			if input.Model != "qwen-local" || len(input.Tools) != 1 {
				t.Errorf("unexpected request model/tools: %+v", input)
			}
			alias := openAIToolAlias("local.echo")
			if input.Tools[0].Function.Name != alias || strings.Contains(alias, ".") {
				t.Errorf("unsafe or unexpected alias %q", input.Tools[0].Function.Name)
			}
			if requests == 1 {
				writeTestJSON(writer, http.StatusOK, map[string]any{
					"choices": []any{map[string]any{"message": map[string]any{
						"role": "assistant", "content": nil,
						"tool_calls": []any{map[string]any{
							"id": "call_1", "type": "function",
							"function": map[string]any{
								"name": alias, "arguments": `{"text":"hello"}`,
							},
						}},
					}}},
				})
				return
			}
			last := input.Messages[len(input.Messages)-1]
			if last.Role != "tool" || last.ToolCallID != "call_1" ||
				last.Content == nil || !strings.Contains(*last.Content, `"hello"`) {
				t.Errorf("tool continuation=%+v", last)
			}
			writeTestJSON(writer, http.StatusOK, map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{
					"role": "assistant", "content": "Echo says hello.",
				}}},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := newOpenAIToolClient(server.URL+"/v1", "", "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.discoverModel(context.Background()); err != nil {
		t.Fatal(err)
	}
	system := "Use tools."
	user := "Echo hello."
	result, err := runOpenAIToolLoop(
		context.Background(), client,
		[]openAIToolMessage{{Role: "system", Content: &system}, {Role: "user", Content: &user}},
		registry, policy, 2, 64, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer != "Echo says hello." || len(result.Calls) != 1 || len(result.Results) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Calls[0].Name != "local.echo" || result.Results[0].Error != nil {
		t.Fatalf("unexpected tool execution: call=%+v result=%+v", result.Calls[0], result.Results[0])
	}
	if requests != 2 {
		t.Fatalf("chat requests=%d, want 2", requests)
	}
}

func TestOpenAIClientNegotiatesAndCachesTokenLimitParameter(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		var input openAIToolRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		switch requests {
		case 1:
			if input.MaxTokens == nil || *input.MaxTokens != 64 || input.MaxCompletionTokens != nil {
				t.Errorf("first request did not use max_tokens: %+v", input)
			}
			writeTestJSON(writer, http.StatusBadRequest, map[string]any{"error": map[string]any{
				"message": "Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.",
				"type":    "invalid_request_error", "param": "max_tokens", "code": "unsupported_parameter",
			}})
			return
		case 2, 3:
			if input.MaxTokens != nil || input.MaxCompletionTokens == nil || *input.MaxCompletionTokens != 64 {
				t.Errorf("request %d did not use max_completion_tokens: %+v", requests, input)
			}
		default:
			t.Errorf("unexpected request %d", requests)
		}
		writeTestJSON(writer, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "compatible",
			}}},
		})
	}))
	defer server.Close()

	client, err := newOpenAIToolClient(server.URL+"/v1", "modern-model", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	user := "Test compatibility."
	for call := 0; call < 2; call++ {
		message, err := client.complete(
			context.Background(), []openAIToolMessage{{Role: "user", Content: &user}}, nil, 64, 0,
		)
		if err != nil {
			t.Fatal(err)
		}
		if message.Content == nil || *message.Content != "compatible" {
			t.Fatalf("unexpected message: %+v", message)
		}
	}
	if requests != 3 {
		t.Fatalf("requests=%d, want 3", requests)
	}
}

func TestOpenAIClientPrependsConfiguredSystemInstructions(t *testing.T) {
	var received []openAIToolMessage
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input openAIToolRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode request: %v", err)
		}
		received = input.Messages
		writeTestJSON(writer, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "ok",
			}}},
		})
	}))
	defer server.Close()

	client, err := newOpenAIToolClient(server.URL+"/v1", "test-model", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetSystemInstructions("global instructions"); err != nil {
		t.Fatal(err)
	}
	task, user := "task instructions", "request"
	messages := []openAIToolMessage{
		{Role: "system", Content: &task},
		{Role: "user", Content: &user},
	}
	if _, err := client.complete(context.Background(), messages, nil, 64, 0); err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 || received[0].Role != "system" || received[0].Content == nil {
		t.Fatalf("unexpected messages: %+v", received)
	}
	want := "global instructions\n\ntask instructions"
	if *received[0].Content != want {
		t.Fatalf("system message = %q, want %q", *received[0].Content, want)
	}
	if messages[0].Content == nil || *messages[0].Content != task {
		t.Fatalf("caller messages were mutated: %+v", messages)
	}
}

func TestOpenAIClientDoesNotRetryUnrelatedBadRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		writeTestJSON(writer, http.StatusBadRequest, map[string]any{"error": map[string]any{
			"message": "Invalid tool schema.", "type": "invalid_request_error",
			"param": "tools", "code": "invalid_value",
		}})
	}))
	defer server.Close()

	client, err := newOpenAIToolClient(server.URL+"/v1", "test-model", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	user := "Test error handling."
	_, err = client.complete(
		context.Background(), []openAIToolMessage{{Role: "user", Content: &user}}, nil, 64, 0,
	)
	if err == nil || !strings.Contains(err.Error(), "Invalid tool schema") {
		t.Fatalf("unexpected error: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests=%d, want 1", requests)
	}
}

func TestOpenAIToolAliasIsDistinctAndBounded(t *testing.T) {
	left := openAIToolAlias("mcp.demo.echo")
	right := openAIToolAlias("mcp-demo-echo")
	if left == right {
		t.Fatalf("aliases collided: %q", left)
	}
	if len(openAIToolAlias(strings.Repeat("very.long-name.", 20))) > 64 {
		t.Fatal("alias exceeds OpenAI function-name limit")
	}
}

func registerEchoTool(registry *mcp.ToolRegistry) error {
	type arguments struct {
		Text string `json:"text"`
	}
	return registry.Register(mcp.ToolDefinition{
		Name: "local.echo", Description: "Returns the provided text as JSON.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
		Permission:  mcp.ToolPermissionRead,
		ValidateArguments: func(raw json.RawMessage) error {
			var value arguments
			if err := mcp.DecodeStrictJSONObject(raw, &value); err != nil {
				return err
			}
			if strings.TrimSpace(value.Text) == "" {
				return fmt.Errorf("text must not be empty")
			}
			return nil
		},
		Handler: func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var value arguments
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]string{"text": value.Text})
		},
	})
}

func writeTestJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
