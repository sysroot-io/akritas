package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

const (
	mcpProtocolVersion = "2025-11-25"
	maxMCPMessageBytes = 4 * 1024 * 1024
)

type MCPStdioConfig struct {
	Name             string
	Command          string
	Arguments        []string
	Environment      []string
	WorkingDirectory string
	ClientName       string
	ClientVersion    string
}

type mcpRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (rpcError *mcpRPCError) Error() string {
	if rpcError == nil {
		return ""
	}
	return fmt.Sprintf("MCP JSON-RPC error %d: %s", rpcError.Code, rpcError.Message)
}

type mcpRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}

type mcpRPCResponse struct {
	result json.RawMessage
	err    error
}

type MCPInitializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	Capabilities    struct {
		Tools *struct {
			ListChanged bool `json:"listChanged,omitempty"`
		} `json:"tools,omitempty"`
	} `json:"capabilities"`
	ServerInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
	Instructions string `json:"instructions,omitempty"`
}

// MCPStdioClient owns exactly one MCP subprocess and one implicit stdio
// session. Requests may be made concurrently; responses are dispatched by ID.
type MCPStdioClient struct {
	config MCPStdioConfig
	cmd    *exec.Cmd
	stdin  io.WriteCloser

	writeMu sync.Mutex
	stateMu sync.Mutex
	pending map[string]chan mcpRPCResponse
	closed  bool
	readErr error

	nextID atomic.Int64
	done   chan struct{}

	initialize MCPInitializeResult
}

func StartMCPStdioClient(
	ctx context.Context,
	config MCPStdioConfig,
) (*MCPStdioClient, error) {
	if config.Name == "" || !toolNamePattern.MatchString(config.Name) {
		return nil, fmt.Errorf("invalid MCP server name %q", config.Name)
	}
	if config.Command == "" {
		return nil, fmt.Errorf("MCP server command is empty")
	}
	if config.ClientName == "" {
		config.ClientName = "Akritas"
	}
	if config.ClientVersion == "" {
		config.ClientVersion = "0.0.1"
	}

	// Command and arguments are passed directly to exec. They are never
	// interpreted by a shell.
	command := exec.Command(config.Command, config.Arguments...)
	command.Dir = config.WorkingDirectory
	command.Env = append(os.Environ(), config.Environment...)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open MCP stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open MCP stdout: %w", err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start MCP server %q: %w", config.Name, err)
	}

	client := &MCPStdioClient{
		config:  config,
		cmd:     command,
		stdin:   stdin,
		pending: make(map[string]chan mcpRPCResponse),
		done:    make(chan struct{}),
	}
	go client.readLoop(stdout)
	if err := client.initializeSession(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func (client *MCPStdioClient) InitializeResult() MCPInitializeResult {
	return client.initialize
}

func (client *MCPStdioClient) initializeSession(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    client.config.ClientName,
			"version": client.config.ClientVersion,
		},
	}
	if err := client.request(ctx, "initialize", params, &client.initialize); err != nil {
		return fmt.Errorf("initialize MCP server %q: %w", client.config.Name, err)
	}
	if client.initialize.ProtocolVersion != mcpProtocolVersion {
		return fmt.Errorf(
			"MCP server %q selected unsupported protocol %q",
			client.config.Name,
			client.initialize.ProtocolVersion,
		)
	}
	if client.initialize.ServerInfo.Name == "" {
		return fmt.Errorf("MCP server %q returned no serverInfo.name", client.config.Name)
	}
	if err := client.notify("notifications/initialized", map[string]any{}); err != nil {
		return fmt.Errorf("finish MCP initialization: %w", err)
	}
	return nil
}

func (client *MCPStdioClient) request(
	ctx context.Context,
	method string,
	params any,
	destination any,
) error {
	id := client.nextID.Add(1)
	idKey := fmt.Sprintf("%d", id)
	responseChannel := make(chan mcpRPCResponse, 1)

	client.stateMu.Lock()
	if client.closed {
		err := client.readErr
		if err == nil {
			err = errors.New("MCP client is closed")
		}
		client.stateMu.Unlock()
		return err
	}
	client.pending[idKey] = responseChannel
	client.stateMu.Unlock()

	message := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := client.writeMessage(message); err != nil {
		client.removePending(idKey)
		return err
	}

	select {
	case response := <-responseChannel:
		if response.err != nil {
			return response.err
		}
		if destination == nil {
			return nil
		}
		if err := json.Unmarshal(response.result, destination); err != nil {
			return fmt.Errorf("decode MCP %s result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		client.removePending(idKey)
		_ = client.notify("notifications/cancelled", map[string]any{
			"requestId": id,
			"reason":    ctx.Err().Error(),
		})
		return ctx.Err()
	case <-client.done:
		client.removePending(idKey)
		client.stateMu.Lock()
		err := client.readErr
		client.stateMu.Unlock()
		if err == nil {
			err = errors.New("MCP connection closed")
		}
		return err
	}
}

func (client *MCPStdioClient) notify(method string, params any) error {
	return client.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (client *MCPStdioClient) writeMessage(message any) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode MCP message: %w", err)
	}
	encoded = append(encoded, '\n')
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	if _, err := client.stdin.Write(encoded); err != nil {
		return fmt.Errorf("write MCP message: %w", err)
	}
	return nil
}

func (client *MCPStdioClient) readLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxMCPMessageBytes)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var message mcpRPCMessage
		if err := json.Unmarshal(line, &message); err != nil {
			client.fail(fmt.Errorf("decode MCP message: %w", err))
			return
		}
		if message.JSONRPC != "2.0" {
			client.fail(fmt.Errorf("MCP message has invalid jsonrpc version"))
			return
		}
		if len(message.ID) == 0 {
			// Server notifications do not require a response. Logging and
			// list-changed notifications are deliberately transport-only in
			// this first client slice.
			continue
		}
		if message.Method != "" {
			client.replyMethodNotFound(message.ID, message.Method)
			continue
		}
		idKey, err := normalizeMCPID(message.ID)
		if err != nil {
			client.fail(err)
			return
		}
		client.stateMu.Lock()
		responseChannel := client.pending[idKey]
		delete(client.pending, idKey)
		client.stateMu.Unlock()
		if responseChannel == nil {
			continue
		}
		if message.Error != nil {
			responseChannel <- mcpRPCResponse{err: message.Error}
		} else if len(message.Result) == 0 {
			responseChannel <- mcpRPCResponse{
				err: errors.New("MCP response has neither result nor error"),
			}
		} else {
			responseChannel <- mcpRPCResponse{result: message.Result}
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	client.fail(fmt.Errorf("MCP stdout closed: %w", err))
}

func normalizeMCPID(raw json.RawMessage) (string, error) {
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err == nil {
		return number.String(), nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	return "", fmt.Errorf("unsupported MCP response ID %s", raw)
}

func (client *MCPStdioClient) replyMethodNotFound(
	id json.RawMessage,
	method string,
) {
	_ = client.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    -32601,
			"message": fmt.Sprintf("unsupported server request %q", method),
		},
	})
}

func (client *MCPStdioClient) removePending(id string) {
	client.stateMu.Lock()
	delete(client.pending, id)
	client.stateMu.Unlock()
}

func (client *MCPStdioClient) fail(err error) {
	client.stateMu.Lock()
	if client.closed {
		client.stateMu.Unlock()
		return
	}
	client.closed = true
	client.readErr = err
	pending := client.pending
	client.pending = make(map[string]chan mcpRPCResponse)
	close(client.done)
	client.stateMu.Unlock()
	for _, responseChannel := range pending {
		responseChannel <- mcpRPCResponse{err: err}
	}
}

func (client *MCPStdioClient) Close() error {
	if client == nil {
		return nil
	}
	client.stateMu.Lock()
	alreadyClosed := client.closed
	client.closed = true
	if !alreadyClosed {
		close(client.done)
	}
	client.stateMu.Unlock()
	_ = client.stdin.Close()

	wait := make(chan error, 1)
	go func() { wait <- client.cmd.Wait() }()
	select {
	case err := <-wait:
		if err != nil && !alreadyClosed {
			return fmt.Errorf("wait for MCP server: %w", err)
		}
		return nil
	case <-time.After(2 * time.Second):
		if err := client.cmd.Process.Kill(); err != nil &&
			!errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("kill MCP server: %w", err)
		}
		<-wait
		return nil
	}
}
