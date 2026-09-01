package runbudget

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrExhausted = errors.New("run budget exhausted")

type Limits struct {
	MaxDuration              time.Duration `json:"-"`
	MaxIterations            int           `json:"max_iterations"`
	MaxToolCalls             int           `json:"max_tool_calls"`
	MaxToolResultBytes       int           `json:"max_tool_result_bytes"`
	MaxRetrievedContextBytes int           `json:"max_retrieved_context_bytes"`
	MaxContextTokens         int           `json:"max_context_tokens"`
	MaxModelTokens           int           `json:"max_model_tokens"`
}

type PublicLimits struct {
	MaxDurationMilliseconds  int64 `json:"max_duration_ms"`
	MaxIterations            int   `json:"max_iterations"`
	MaxToolCalls             int   `json:"max_tool_calls"`
	MaxToolResultBytes       int   `json:"max_tool_result_bytes"`
	MaxRetrievedContextBytes int   `json:"max_retrieved_context_bytes"`
	MaxContextTokens         int   `json:"max_context_tokens"`
	MaxModelTokens           int   `json:"max_model_tokens"`
}

type Usage struct {
	DurationMilliseconds  int64 `json:"duration_ms"`
	Iterations            int   `json:"iterations"`
	ToolCalls             int   `json:"tool_calls"`
	ToolResultBytes       int   `json:"tool_result_bytes"`
	RetrievedContextBytes int   `json:"retrieved_context_bytes"`
	ContextTokens         int   `json:"context_tokens"`
	ModelTokens           int   `json:"model_tokens"`
}

type Snapshot struct {
	Limits UsageLimits `json:"limits"`
	Usage  Usage       `json:"usage"`
}

type UsageLimits = PublicLimits

type Tracker struct {
	mu       sync.Mutex
	limits   Limits
	usage    Usage
	started  time.Time
	reserved map[int]int
	nextID   int
}

func New(limits Limits) (*Tracker, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	return &Tracker{
		limits: limits, started: time.Now(), reserved: make(map[int]int),
	}, nil
}

func (limits Limits) Validate() error {
	if limits.MaxDuration <= 0 || limits.MaxIterations <= 0 || limits.MaxToolCalls < 0 ||
		limits.MaxToolResultBytes <= 0 || limits.MaxRetrievedContextBytes <= 0 ||
		limits.MaxContextTokens <= 0 || limits.MaxModelTokens <= 0 {
		return fmt.Errorf("run budget limits must be positive; max tool calls may be zero")
	}
	if limits.MaxRetrievedContextBytes > limits.MaxToolResultBytes {
		return fmt.Errorf("retrieved context limit cannot exceed tool result limit")
	}
	return nil
}

func (tracker *Tracker) Context(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, tracker.limits.MaxDuration)
}

// BeginModelCall reserves a bounded output allowance before an upstream call.
// If the upstream does not report usage, the entire reservation remains charged.
func (tracker *Tracker) BeginModelCall(requestedTokens int) (reservationID, allowedTokens int, err error) {
	if tracker == nil || requestedTokens <= 0 {
		return 0, 0, fmt.Errorf("invalid model token request")
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.usage.Iterations >= tracker.limits.MaxIterations {
		return 0, 0, exhausted("iterations")
	}
	remaining := tracker.limits.MaxModelTokens - tracker.usage.ModelTokens
	if remaining <= 0 {
		return 0, 0, exhausted("model_tokens")
	}
	allowed := requestedTokens
	if allowed > remaining {
		allowed = remaining
	}
	tracker.nextID++
	tracker.reserved[tracker.nextID] = allowed
	tracker.usage.Iterations++
	tracker.usage.ModelTokens += allowed
	return tracker.nextID, allowed, nil
}

// FinishModelCall replaces a conservative reservation with reported usage.
// reportedTokens < 0 means the upstream omitted trustworthy usage information.
func (tracker *Tracker) FinishModelCall(reservationID, reportedTokens int) error {
	if tracker == nil {
		return fmt.Errorf("run budget tracker is nil")
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	reserved, exists := tracker.reserved[reservationID]
	if !exists {
		return fmt.Errorf("unknown model reservation %d", reservationID)
	}
	delete(tracker.reserved, reservationID)
	if reportedTokens < 0 {
		return nil
	}
	if reportedTokens > reserved {
		return exhausted("model_tokens")
	}
	tracker.usage.ModelTokens -= reserved - reportedTokens
	return nil
}

func (tracker *Tracker) RecordToolCall() error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.usage.ToolCalls >= tracker.limits.MaxToolCalls {
		return exhausted("tool_calls")
	}
	tracker.usage.ToolCalls++
	return nil
}

func (tracker *Tracker) RecordToolResult(size int, retrievedContext bool) error {
	if size < 0 {
		return fmt.Errorf("tool result size cannot be negative")
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.usage.ToolResultBytes+size > tracker.limits.MaxToolResultBytes {
		return exhausted("tool_result_bytes")
	}
	if retrievedContext && tracker.usage.RetrievedContextBytes+size > tracker.limits.MaxRetrievedContextBytes {
		return exhausted("retrieved_context_bytes")
	}
	tracker.usage.ToolResultBytes += size
	if retrievedContext {
		tracker.usage.RetrievedContextBytes += size
	}
	return nil
}

// RecordContextBytes applies a conservative host-derived upper bound of one
// token per input byte. This does not depend on model-reported tokenization.
func (tracker *Tracker) RecordContextBytes(size int) error {
	if size < 0 {
		return fmt.Errorf("context size cannot be negative")
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.usage.ContextTokens+size > tracker.limits.MaxContextTokens {
		return exhausted("context_tokens")
	}
	tracker.usage.ContextTokens += size
	return nil
}

func (tracker *Tracker) Snapshot() Snapshot {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	usage := tracker.usage
	usage.DurationMilliseconds = time.Since(tracker.started).Milliseconds()
	return Snapshot{Limits: tracker.limits.Public(), Usage: usage}
}

func (limits Limits) Public() PublicLimits {
	return PublicLimits{
		MaxDurationMilliseconds:  limits.MaxDuration.Milliseconds(),
		MaxIterations:            limits.MaxIterations,
		MaxToolCalls:             limits.MaxToolCalls,
		MaxToolResultBytes:       limits.MaxToolResultBytes,
		MaxRetrievedContextBytes: limits.MaxRetrievedContextBytes,
		MaxContextTokens:         limits.MaxContextTokens,
		MaxModelTokens:           limits.MaxModelTokens,
	}
}

func exhausted(dimension string) error {
	return fmt.Errorf("%w: %s", ErrExhausted, dimension)
}
