package audit

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	storeVersion       = 1
	maximumRecordBytes = 256 * 1024
	maximumMetadata    = 32
	maximumValueRunes  = 512
)

type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
)

type Run struct {
	ID          string            `json:"id"`
	Source      string            `json:"source"`
	Actor       string            `json:"actor"`
	Workspace   string            `json:"workspace,omitempty"`
	Model       string            `json:"model,omitempty"`
	Status      RunStatus         `json:"status"`
	StartedAt   time.Time         `json:"started_at"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
	Error       string            `json:"error,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Events      []Event           `json:"events,omitempty"`
}

type Event struct {
	Sequence int               `json:"sequence"`
	Time     time.Time         `json:"time"`
	Type     string            `json:"type"`
	Status   string            `json:"status,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type record struct {
	Version int               `json:"version"`
	Kind    string            `json:"kind"`
	Run     *Run              `json:"run,omitempty"`
	RunID   string            `json:"run_id,omitempty"`
	Event   *Event            `json:"event,omitempty"`
	Status  RunStatus         `json:"status,omitempty"`
	Time    *time.Time        `json:"time,omitempty"`
	Error   string            `json:"error,omitempty"`
	Meta    map[string]string `json:"metadata,omitempty"`
}

type Store struct {
	mu   sync.Mutex
	file *os.File
	runs map[string]*Run
}

func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("audit path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve audit path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return nil, fmt.Errorf("create audit directory: %w", err)
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit store: %w", err)
	}
	store := &Store{file: file, runs: make(map[string]*Run)}
	if err := store.load(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return store, nil
}

func (store *Store) load() error {
	if _, err := store.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek audit store: %w", err)
	}
	scanner := bufio.NewScanner(store.file)
	scanner.Buffer(make([]byte, 64*1024), maximumRecordBytes)
	line := 0
	for scanner.Scan() {
		line++
		var item record
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return fmt.Errorf("decode audit record line %d: %w", line, err)
		}
		if item.Version != storeVersion {
			return fmt.Errorf("audit record line %d has version %d, want %d", line, item.Version, storeVersion)
		}
		if err := store.applyRecord(item); err != nil {
			return fmt.Errorf("apply audit record line %d: %w", line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan audit store: %w", err)
	}
	_, err := store.file.Seek(0, io.SeekEnd)
	return err
}

func (store *Store) applyRecord(item record) error {
	switch item.Kind {
	case "run_started":
		if item.Run == nil || item.Run.ID == "" || item.Run.Status != RunRunning {
			return fmt.Errorf("invalid run_started record")
		}
		if _, exists := store.runs[item.Run.ID]; exists {
			return fmt.Errorf("duplicate run %q", item.Run.ID)
		}
		store.runs[item.Run.ID] = cloneRun(item.Run)
	case "event":
		run := store.runs[item.RunID]
		if run == nil || item.Event == nil || item.Event.Sequence != len(run.Events)+1 {
			return fmt.Errorf("invalid event for run %q", item.RunID)
		}
		run.Events = append(run.Events, cloneEvent(*item.Event))
	case "run_finished":
		run := store.runs[item.RunID]
		if run == nil || item.Time == nil || run.Status != RunRunning ||
			(item.Status != RunSucceeded && item.Status != RunFailed) {
			return fmt.Errorf("invalid run_finished record for %q", item.RunID)
		}
		run.Status, run.CompletedAt = item.Status, item.Time
		run.Error = boundedValue(item.Error)
		run.Metadata = mergeMetadata(run.Metadata, item.Meta)
	default:
		return fmt.Errorf("unknown audit record kind %q", item.Kind)
	}
	return nil
}

func (store *Store) StartRun(source, actor, workspace, model string, metadata map[string]string) (Run, error) {
	if store == nil {
		return Run{}, fmt.Errorf("audit store is nil")
	}
	source, actor = strings.TrimSpace(source), strings.TrimSpace(actor)
	if source == "" || actor == "" {
		return Run{}, fmt.Errorf("audit run requires source and actor")
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return Run{}, fmt.Errorf("generate audit run ID: %w", err)
	}
	run := Run{
		ID: "run_" + hex.EncodeToString(idBytes), Source: boundedValue(source),
		Actor: boundedValue(actor), Workspace: boundedValue(workspace), Model: boundedValue(model),
		Status: RunRunning, StartedAt: time.Now().UTC(), Metadata: sanitizedMetadata(metadata),
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.append(record{Version: storeVersion, Kind: "run_started", Run: &run}); err != nil {
		return Run{}, err
	}
	store.runs[run.ID] = cloneRun(&run)
	return *cloneRun(&run), nil
}

func (store *Store) AddEvent(runID, eventType, status string, metadata map[string]string) error {
	if store == nil {
		return fmt.Errorf("audit store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	run := store.runs[runID]
	if run == nil || run.Status != RunRunning {
		return fmt.Errorf("audit run %q is not running", runID)
	}
	event := Event{
		Sequence: len(run.Events) + 1, Time: time.Now().UTC(),
		Type: boundedValue(strings.TrimSpace(eventType)), Status: boundedValue(status),
		Metadata: sanitizedMetadata(metadata),
	}
	if event.Type == "" {
		return fmt.Errorf("audit event type is empty")
	}
	if err := store.append(record{Version: storeVersion, Kind: "event", RunID: runID, Event: &event}); err != nil {
		return err
	}
	run.Events = append(run.Events, cloneEvent(event))
	return nil
}

func (store *Store) FinishRun(runID string, status RunStatus, runError string, metadata map[string]string) error {
	if store == nil {
		return fmt.Errorf("audit store is nil")
	}
	if status != RunSucceeded && status != RunFailed {
		return fmt.Errorf("invalid final audit status %q", status)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	run := store.runs[runID]
	if run == nil || run.Status != RunRunning {
		return fmt.Errorf("audit run %q is not running", runID)
	}
	now := time.Now().UTC()
	item := record{
		Version: storeVersion, Kind: "run_finished", RunID: runID,
		Status: status, Time: &now, Error: boundedValue(runError), Meta: sanitizedMetadata(metadata),
	}
	if err := store.append(item); err != nil {
		return err
	}
	run.Status, run.CompletedAt, run.Error = status, &now, item.Error
	run.Metadata = mergeMetadata(run.Metadata, item.Meta)
	return nil
}

func (store *Store) Get(runID string) (Run, bool) {
	if store == nil {
		return Run{}, false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	run := store.runs[runID]
	if run == nil {
		return Run{}, false
	}
	return *cloneRun(run), true
}

func (store *Store) List(limit int) []Run {
	if store == nil {
		return nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	runs := make([]Run, 0, len(store.runs))
	for _, run := range store.runs {
		runs = append(runs, *cloneRun(run))
	}
	sort.Slice(runs, func(left, right int) bool { return runs[left].StartedAt.After(runs[right].StartedAt) })
	if len(runs) > limit {
		runs = runs[:limit]
	}
	return runs
}

func (store *Store) append(item record) error {
	encoded, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("encode audit record: %w", err)
	}
	if len(encoded)+1 > maximumRecordBytes {
		return fmt.Errorf("audit record exceeds %d bytes", maximumRecordBytes)
	}
	encoded = append(encoded, '\n')
	written, err := store.file.Write(encoded)
	if err != nil {
		return fmt.Errorf("append audit record: %w", err)
	}
	if written != len(encoded) {
		return fmt.Errorf("append audit record: short write %d of %d bytes", written, len(encoded))
	}
	if err := store.file.Sync(); err != nil {
		return fmt.Errorf("sync audit record: %w", err)
	}
	return nil
}

func (store *Store) Close() error {
	if store == nil || store.file == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.file.Close()
}

func sanitizedMetadata(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > maximumMetadata {
		keys = keys[:maximumMetadata]
	}
	output := make(map[string]string, len(keys))
	for _, key := range keys {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if normalized == "" || strings.Contains(normalized, "authorization") ||
			strings.Contains(normalized, "api_key") || strings.Contains(normalized, "apikey") ||
			strings.Contains(normalized, "password") || strings.Contains(normalized, "secret") ||
			strings.Contains(normalized, "token") {
			continue
		}
		output[boundedValue(normalized)] = boundedValue(input[key])
	}
	if len(output) == 0 {
		return nil
	}
	return output
}

func mergeMetadata(groups ...map[string]string) map[string]string {
	merged := make(map[string]string)
	for _, group := range groups {
		for key, value := range sanitizedMetadata(group) {
			merged[key] = value
		}
	}
	return sanitizedMetadata(merged)
}

func boundedValue(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > maximumValueRunes {
		runes = runes[:maximumValueRunes]
	}
	return string(runes)
}

func cloneEvent(event Event) Event {
	event.Metadata = sanitizedMetadata(event.Metadata)
	return event
}

func cloneRun(run *Run) *Run {
	clone := *run
	clone.Metadata = sanitizedMetadata(run.Metadata)
	clone.Events = make([]Event, len(run.Events))
	for index := range run.Events {
		clone.Events[index] = cloneEvent(run.Events[index])
	}
	if run.CompletedAt != nil {
		completed := *run.CompletedAt
		clone.CompletedAt = &completed
	}
	return &clone
}
