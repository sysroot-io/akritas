package victoriametrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"akritas/internal/mcp"
)

const (
	maximumQueryBytes   = 16 * 1024
	maximumMatchers     = 10
	maximumMatcherBytes = 4 * 1024
	maximumDiscovery    = 1000
)

var labelNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

type queryArguments struct {
	Query string `json:"query"`
	Time  string `json:"time,omitempty"`
	Step  string `json:"step,omitempty"`
}

type rangeQueryArguments struct {
	Query string `json:"query"`
	Start string `json:"start"`
	End   string `json:"end"`
	Step  string `json:"step"`
}

type discoveryArguments struct {
	Match []string `json:"match,omitempty"`
	Start string   `json:"start,omitempty"`
	End   string   `json:"end,omitempty"`
	Limit int      `json:"limit,omitempty"`
}

type labelValuesArguments struct {
	Label string   `json:"label"`
	Match []string `json:"match,omitempty"`
	Start string   `json:"start,omitempty"`
	End   string   `json:"end,omitempty"`
	Limit int      `json:"limit,omitempty"`
}

func RegisterTools(registry *mcp.ToolRegistry, client *Client) error {
	if registry == nil || client == nil {
		return fmt.Errorf("VictoriaMetrics tool registration requires registry and client")
	}
	definitions := []mcp.ToolDefinition{
		{
			Name: "query", Description: "Run a read-only instant MetricsQL or PromQL query against the operator-configured VictoriaMetrics endpoint.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"time":{"type":"string"},"step":{"type":"string"}},"required":["query"],"additionalProperties":false}`),
			Permission:  mcp.ToolPermissionRead,
			ValidateArguments: func(raw json.RawMessage) error {
				var arguments queryArguments
				if err := mcp.DecodeStrictJSONObject(raw, &arguments); err != nil {
					return err
				}
				return validateQuery(arguments.Query, arguments.Time, arguments.Step)
			},
			Handler: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
				var arguments queryArguments
				if err := json.Unmarshal(raw, &arguments); err != nil {
					return nil, err
				}
				values := url.Values{"query": {arguments.Query}}
				addOptional(values, "time", arguments.Time)
				addOptional(values, "step", arguments.Step)
				return client.Query(ctx, values)
			},
		},
		{
			Name: "query_range", Description: "Run a read-only MetricsQL or PromQL range query with an explicit start, end, and step.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"start":{"type":"string"},"end":{"type":"string"},"step":{"type":"string"}},"required":["query","start","end","step"],"additionalProperties":false}`),
			Permission:  mcp.ToolPermissionRead,
			ValidateArguments: func(raw json.RawMessage) error {
				var arguments rangeQueryArguments
				if err := mcp.DecodeStrictJSONObject(raw, &arguments); err != nil {
					return err
				}
				if err := validateQuery(arguments.Query, arguments.Start, arguments.End, arguments.Step); err != nil {
					return err
				}
				if arguments.Start == "" || arguments.End == "" || arguments.Step == "" {
					return fmt.Errorf("start, end and step must not be empty")
				}
				return nil
			},
			Handler: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
				var arguments rangeQueryArguments
				if err := json.Unmarshal(raw, &arguments); err != nil {
					return nil, err
				}
				return client.QueryRange(ctx, url.Values{
					"query": {arguments.Query}, "start": {arguments.Start},
					"end": {arguments.End}, "step": {arguments.Step},
				})
			},
		},
		newDiscoveryTool("series", "List bounded time series and their labels for one or more selectors.", true, client.Series),
		newDiscoveryTool("labels", "List bounded label names in an optional time range and selector scope.", false, client.Labels),
		{
			Name: "label_values", Description: "List bounded values for a label in an optional time range and selector scope.",
			InputSchema: discoverySchema(true, false), Permission: mcp.ToolPermissionRead,
			ValidateArguments: func(raw json.RawMessage) error {
				var arguments labelValuesArguments
				if err := mcp.DecodeStrictJSONObject(raw, &arguments); err != nil {
					return err
				}
				if !labelNamePattern.MatchString(arguments.Label) {
					return fmt.Errorf("label has invalid syntax")
				}
				return validateDiscovery(arguments.Match, arguments.Start, arguments.End, arguments.Limit, false)
			},
			Handler: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
				var arguments labelValuesArguments
				if err := json.Unmarshal(raw, &arguments); err != nil {
					return nil, err
				}
				return client.LabelValues(ctx, arguments.Label, discoveryValues(
					arguments.Match, arguments.Start, arguments.End, arguments.Limit,
				))
			},
		},
	}
	for index := range definitions {
		if client.toolTimeout > 0 {
			definitions[index].Timeout = client.toolTimeout
		}
		definition := definitions[index]
		if err := registry.Register(definition); err != nil {
			return err
		}
	}
	return nil
}

func newDiscoveryTool(
	name string,
	description string,
	requireMatcher bool,
	handler func(context.Context, url.Values) (json.RawMessage, error),
) mcp.ToolDefinition {
	return mcp.ToolDefinition{
		Name: name, Description: description,
		InputSchema: discoverySchema(false, requireMatcher), Permission: mcp.ToolPermissionRead,
		ValidateArguments: func(raw json.RawMessage) error {
			var arguments discoveryArguments
			if err := mcp.DecodeStrictJSONObject(raw, &arguments); err != nil {
				return err
			}
			return validateDiscovery(arguments.Match, arguments.Start, arguments.End, arguments.Limit, requireMatcher)
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var arguments discoveryArguments
			if err := json.Unmarshal(raw, &arguments); err != nil {
				return nil, err
			}
			return handler(ctx, discoveryValues(arguments.Match, arguments.Start, arguments.End, arguments.Limit))
		},
	}
}

func discoverySchema(includeLabel bool, requireMatcher bool) json.RawMessage {
	properties := `"match":{"type":"array","items":{"type":"string"}},"start":{"type":"string"},"end":{"type":"string"},"limit":{"type":"integer"}`
	required := ""
	if includeLabel {
		properties = `"label":{"type":"string"},` + properties
		required = `,"required":["label"]`
	} else if requireMatcher {
		required = `,"required":["match"]`
	}
	return json.RawMessage(`{"type":"object","properties":{` + properties + `}` + required + `,"additionalProperties":false}`)
}

func validateQuery(query string, timeValues ...string) error {
	if strings.TrimSpace(query) == "" || len(query) > maximumQueryBytes {
		return fmt.Errorf("query must contain 1..%d bytes", maximumQueryBytes)
	}
	for _, value := range timeValues {
		if len(value) > 64 || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("time and step values must contain at most 64 bytes")
		}
	}
	return nil
}

func validateDiscovery(matchers []string, start, end string, limit int, requireMatcher bool) error {
	if requireMatcher && len(matchers) == 0 {
		return fmt.Errorf("at least one match selector is required")
	}
	if len(matchers) > maximumMatchers {
		return fmt.Errorf("at most %d match selectors are allowed", maximumMatchers)
	}
	for _, matcher := range matchers {
		if strings.TrimSpace(matcher) == "" || len(matcher) > maximumMatcherBytes {
			return fmt.Errorf("each match selector must contain 1..%d bytes", maximumMatcherBytes)
		}
	}
	if err := validateQuery("discovery", start, end); err != nil {
		return err
	}
	if limit < 0 || limit > maximumDiscovery {
		return fmt.Errorf("limit must be between 0 and %d", maximumDiscovery)
	}
	return nil
}

func discoveryValues(matchers []string, start, end string, limit int) url.Values {
	values := make(url.Values)
	for _, matcher := range matchers {
		values.Add("match[]", matcher)
	}
	addOptional(values, "start", start)
	addOptional(values, "end", end)
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	return values
}

func addOptional(values url.Values, key, value string) {
	if value != "" {
		values.Set(key, value)
	}
}
