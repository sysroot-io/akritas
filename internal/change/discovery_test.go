package change

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverChangeSimulationSnapshotUsesForcedPlanner(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"inventory/backends.yaml":  "orders-v2: 10.20.4.15\n",
		"pillar/prod/nftables.sls": "backend_allowlist:\n  - orders-v1\n",
		"unrelated.txt":            "nothing useful\n",
		".env":                     "orders-v2=secret\n",
		".git/config":              "orders-v2\n",
	}
	for path, content := range files {
		target := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input openAIToolRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if input.ToolChoice != "required" || len(input.Tools) != 1 || input.Tools[0].Function.Name != changeDiscoveryToolName {
			t.Errorf("unexpected discovery request: %+v", input)
		}
		writeJSON(writer, http.StatusOK, map[string]any{"choices": []any{map[string]any{"message": map[string]any{
			"role": "assistant", "tool_calls": []any{map[string]any{"id": "search", "type": "function", "function": map[string]any{
				"name": changeDiscoveryToolName, "arguments": `{"queries":["orders-v2","nftables","backend_allowlist"]}`,
			}}},
		}}}})
	}))
	defer upstream.Close()
	client, err := newOpenAIToolClient(upstream.URL+"/v1", "qwen-test", "", upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := discoverChangeSimulationSnapshot(
		context.Background(), client, root, "<inline>", "Allow orders-v2 in nftables", 512, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	found := make(map[string]bool)
	for _, file := range snapshot.Files {
		found[file.Path] = true
	}
	if !found["inventory/backends.yaml"] || !found["pillar/prod/nftables.sls"] || found[".env"] || found[".git/config"] {
		t.Fatalf("unexpected discovery candidates: %+v", found)
	}
}
