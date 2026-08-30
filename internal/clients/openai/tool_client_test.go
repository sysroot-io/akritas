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
		registry, policy, 2, 64, 0,
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
