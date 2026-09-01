package victoriametrics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultMaximumResponseBytes = 192 * 1024
	maximumDebugPreviewBytes    = 2 * 1024
)

type ClientOptions struct {
	BaseURL              string
	BearerToken          string
	Username             string
	Password             string
	AccountID            string
	ProjectID            string
	MaximumResponseBytes int64
	HTTPClient           *http.Client
	Debug                bool
	Logger               *log.Logger
}

type Client struct {
	baseURL              *url.URL
	bearerToken          string
	username             string
	password             string
	accountID            string
	projectID            string
	maximumResponseBytes int64
	httpClient           *http.Client
	toolTimeout          time.Duration
	debug                bool
	logger               *log.Logger
}

func NewClient(options ClientOptions) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimSpace(options.BaseURL))
	if err != nil || baseURL == nil ||
		(baseURL.Scheme != "http" && baseURL.Scheme != "https") ||
		baseURL.Host == "" || baseURL.User != nil || baseURL.Fragment != "" {
		return nil, fmt.Errorf("VictoriaMetrics base URL must be an HTTP(S) URL without userinfo or fragment")
	}
	if options.BearerToken != "" && (options.Username != "" || options.Password != "") {
		return nil, fmt.Errorf("VictoriaMetrics bearer and basic authentication are mutually exclusive")
	}
	if (options.Username == "") != (options.Password == "") {
		return nil, fmt.Errorf("VictoriaMetrics basic authentication requires both username and password")
	}
	if (options.ProjectID != "" && options.AccountID == "") ||
		len(options.AccountID) > 128 || len(options.ProjectID) > 128 ||
		strings.ContainsAny(options.AccountID+options.ProjectID, "\r\n") {
		return nil, fmt.Errorf("invalid VictoriaMetrics tenant headers")
	}
	maximumBytes := options.MaximumResponseBytes
	if maximumBytes == 0 {
		maximumBytes = defaultMaximumResponseBytes
	}
	if maximumBytes < 1024 || maximumBytes > 240*1024 {
		return nil, fmt.Errorf("VictoriaMetrics maximum response bytes must be between 1024 and 245760")
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	logger := options.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &Client{
		baseURL: baseURL, bearerToken: options.BearerToken,
		username: options.Username, password: options.Password,
		accountID: options.AccountID, projectID: options.ProjectID,
		maximumResponseBytes: maximumBytes, httpClient: httpClient,
		toolTimeout: httpClient.Timeout,
		debug:       options.Debug, logger: logger,
	}, nil
}

func (client *Client) Query(ctx context.Context, values url.Values) (json.RawMessage, error) {
	return client.post(ctx, "/api/v1/query", values)
}

func (client *Client) QueryRange(ctx context.Context, values url.Values) (json.RawMessage, error) {
	return client.post(ctx, "/api/v1/query_range", values)
}

func (client *Client) Series(ctx context.Context, values url.Values) (json.RawMessage, error) {
	return client.post(ctx, "/api/v1/series", values)
}

func (client *Client) Labels(ctx context.Context, values url.Values) (json.RawMessage, error) {
	return client.post(ctx, "/api/v1/labels", values)
}

func (client *Client) LabelValues(
	ctx context.Context,
	label string,
	values url.Values,
) (json.RawMessage, error) {
	return client.post(ctx, "/api/v1/label/"+url.PathEscape(label)+"/values", values)
}

func (client *Client) post(
	ctx context.Context,
	endpoint string,
	values url.Values,
) (json.RawMessage, error) {
	target := *client.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + endpoint
	query := target.Query()
	for key, entries := range values {
		for _, value := range entries {
			query.Add(key, value)
		}
	}
	target.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, target.String(), bytes.NewReader(nil),
	)
	if err != nil {
		return nil, fmt.Errorf("build VictoriaMetrics request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Akritas-VictoriaMetrics-MCP/0.1.0")
	if client.bearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+client.bearerToken)
	} else if client.username != "" {
		request.SetBasicAuth(client.username, client.password)
	}
	if client.accountID != "" {
		request.Header.Set("AccountID", client.accountID)
	}
	if client.projectID != "" {
		request.Header.Set("ProjectID", client.projectID)
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("query VictoriaMetrics: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, client.maximumResponseBytes+1))
	if err != nil {
		client.logErrorResponse("read_error", endpoint, response, body)
		return nil, fmt.Errorf("read VictoriaMetrics response: %w", err)
	}
	if int64(len(body)) > client.maximumResponseBytes {
		client.logErrorResponse("response_too_large", endpoint, response, body)
		return nil, fmt.Errorf("VictoriaMetrics response exceeds %d bytes", client.maximumResponseBytes)
	}
	var envelope struct {
		Status    string `json:"status"`
		ErrorType string `json:"errorType"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		client.logErrorResponse("invalid_json", endpoint, response, body)
		return nil, fmt.Errorf("VictoriaMetrics returned invalid JSON")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.Status != "success" {
		client.logErrorResponse("query_failed", endpoint, response, body)
		message := strings.TrimSpace(strings.Join([]string{envelope.ErrorType, envelope.Error}, ": "))
		message = strings.Trim(message, ": ")
		if message == "" {
			message = response.Status
		}
		if len(message) > 512 {
			message = message[:512]
		}
		return nil, fmt.Errorf("VictoriaMetrics query failed: %s", message)
	}
	return append(json.RawMessage(nil), body...), nil
}

func (client *Client) logErrorResponse(
	reason string,
	endpoint string,
	response *http.Response,
	body []byte,
) {
	if !client.debug || client.logger == nil || response == nil {
		return
	}
	contentType := response.Header.Get("Content-Type")
	if len(contentType) > 256 {
		contentType = contentType[:256]
	}
	preview := body
	truncated := false
	if len(preview) > maximumDebugPreviewBytes {
		preview = preview[:maximumDebugPreviewBytes]
		truncated = true
	}
	previewText := strings.ToValidUTF8(string(preview), "\uFFFD")
	if truncated {
		previewText += "..."
	}
	client.logger.Printf(
		"level=debug component=victoriametrics event=response_error reason=%q endpoint=%q status_code=%d content_type=%q body_preview=%q",
		reason, endpoint, response.StatusCode, contentType, previewText,
	)
}
