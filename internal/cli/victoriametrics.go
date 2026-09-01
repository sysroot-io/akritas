package cli

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"akritas/internal/mcp"
	"akritas/internal/victoriametrics"
)

func runVictoriaMetricsMCP(arguments []string) {
	flags := flag.NewFlagSet("mcp-victoriametrics", flag.ExitOnError)
	baseURL := flags.String("base-url", "http://127.0.0.1:8428/prometheus", "VictoriaMetrics read API root before /api/v1")
	bearerTokenEnvironment := flags.String("bearer-token-env", "", "environment variable containing a bearer token")
	usernameEnvironment := flags.String("username-env", "", "environment variable containing a basic-auth username")
	passwordEnvironment := flags.String("password-env", "", "environment variable containing a basic-auth password")
	accountID := flags.String("account-id", "", "optional VictoriaMetrics AccountID header")
	projectID := flags.String("project-id", "", "optional VictoriaMetrics ProjectID header")
	requestTimeout := flags.Duration("request-timeout", 15*time.Second, "timeout for each VictoriaMetrics HTTP request")
	maximumResponseBytes := flags.Int64("max-response-bytes", 192*1024, "maximum decompressed VictoriaMetrics JSON response size")
	debug := flags.Bool("debug", false, "log bounded VictoriaMetrics error response details to stderr")
	_ = flags.Parse(arguments)
	if *requestTimeout <= 0 {
		panic("mcp-victoriametrics request-timeout must be positive")
	}

	bearerToken, err := optionalSecretFromEnvironment(*bearerTokenEnvironment)
	if err != nil {
		panic(err)
	}
	username, err := optionalSecretFromEnvironment(*usernameEnvironment)
	if err != nil {
		panic(err)
	}
	password, err := optionalSecretFromEnvironment(*passwordEnvironment)
	if err != nil {
		panic(err)
	}
	client, err := victoriametrics.NewClient(victoriametrics.ClientOptions{
		BaseURL: *baseURL, BearerToken: bearerToken,
		Username: username, Password: password,
		AccountID: *accountID, ProjectID: *projectID,
		MaximumResponseBytes: *maximumResponseBytes,
		HTTPClient:           &http.Client{Timeout: *requestTimeout},
		Debug:                *debug,
	})
	if err != nil {
		panic(err)
	}
	registry := mcp.NewToolRegistry()
	if err := victoriametrics.RegisterTools(registry, client); err != nil {
		panic(err)
	}
	if err := mcp.ServeStdio(
		context.Background(), os.Stdin, os.Stdout,
		"akritas-victoriametrics", "0.1.0", registry,
	); err != nil {
		panic(fmt.Errorf("serve VictoriaMetrics MCP: %w", err))
	}
}

func optionalSecretFromEnvironment(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	value, exists := os.LookupEnv(name)
	if !exists || value == "" {
		return "", fmt.Errorf("environment variable %s is not set or is empty", name)
	}
	return value, nil
}
