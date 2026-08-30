package change

import (
	"encoding/json"
	"net/http"

	"akritas/internal/clients/openai"
	"akritas/internal/mcp"
)

type openAIToolClient = openai.Client
type openAIToolFunction = openai.ToolFunction
type openAIToolMessage = openai.ToolMessage
type openAIToolRequest = openai.ToolRequest
type openAIToolSpec = openai.ToolSpec

func newOpenAIToolClient(baseURL, model, apiKey string, client *http.Client) (*openAIToolClient, error) {
	return openai.NewToolClient(baseURL, model, apiKey, client)
}

func decodeStrictJSONObject(payload []byte, destination any) error {
	return mcp.DecodeStrictJSONObject(json.RawMessage(payload), destination)
}

type Workspace struct {
	Name              string
	Root              string
	ValidatorProfiles []string
	ReplaceValidators bool
}

type opsWorkspace = Workspace
