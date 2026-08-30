package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMCPHostConfigStrictAndExplicit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{
		"version":1,
		"servers":[{
			"name":"demo",
			"command":"example-mcp-server",
			"args":["mcp-stdio-echo-server"],
			"allow":["read"],
			"timeout_ms":5000
		}]
	}`)
	config, err := LoadMCPHostConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Servers) != 1 ||
		config.Servers[0].Allow[0] != ToolPermissionRead {
		t.Fatalf("unexpected config: %+v", config)
	}

	write(`{"version":1,"servers":[{
		"name":"demo","command":"example-mcp-server","allow":["read"],"unknown":true
	}]}`)
	if _, err := LoadMCPHostConfig(path); err == nil {
		t.Fatal("unknown config field was accepted")
	}
	write(`{"version":1,"servers":[{
		"name":"demo","command":"example-mcp-server","allow":[]
	}]}`)
	if _, err := LoadMCPHostConfig(path); err == nil {
		t.Fatal("server without explicit permissions was accepted")
	}
	write(`{"version":1,"servers":[{
		"name":"demo","command":"example-mcp-server","allow":["read"]
	}]} trailing`)
	if _, err := LoadMCPHostConfig(path); err == nil {
		t.Fatal("trailing config data was accepted")
	}
}

func TestStartMCPHostAppliesPerServerPermission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	host, err := StartMCPHost(ctx, MCPHostConfig{
		Version: 1,
		Servers: []MCPServerConfig{{
			Name:        "configured",
			Command:     os.Args[0],
			Arguments:   []string{"-test.run=^TestMCPStdioHelperProcess$"},
			Environment: map[string]string{"AKRITAS_MCP_HELPER": "1"},
			Allow:       []ToolPermission{ToolPermissionRead},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if !host.Policy.Allowed["mcp.configured.echo"] {
		t.Fatalf("read tool was not allowed: %+v", host.Policy.Allowed)
	}
	catalog := host.ToolCatalogPrompt()
	if catalog == "" {
		t.Fatal("tool catalog is empty")
	}
}
