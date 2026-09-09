package alerts

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	storeVersion            = 1
	maximumStoreRecordBytes = 4 * 1024 * 1024
	maximumErrorDetailRunes = 2048
)

var sensitiveErrorValues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(bearer|basic)\s+[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|apikey|access[_-]?token|refresh[_-]?token|token|password|secret)["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^,\s;&}]+)`),
	regexp.MustCompile(`(?i)(https?://[^:/\s]+:)[^@\s]+@`),
}

type DeliveryStatus string

const (
	DeliveryAccepted  DeliveryStatus = "accepted"
	DeliveryDuplicate DeliveryStatus = "duplicate"
)

type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
)

type Delivery struct {
	ID          string          `json:"id"`
	Source      string          `json:"source"`
	ReceivedAt  time.Time       `json:"received_at"`
	PayloadHash string          `json:"payload_hash"`
	Events      []DeliveryEvent `json:"events"`
}

type DeliveryEvent struct {
	EventID    string         `json:"event_id"`
	IncidentID string         `json:"incident_id"`
	JobID      string         `json:"job_id,omitempty"`
	Status     DeliveryStatus `json:"status"`
}

type StoredEvent struct {
	Event
	IncidentID string `json:"incident_id"`
	DeliveryID string `json:"delivery_id"`
}

type Incident struct {
	ID             string     `json:"id"`
	CorrelationKey string     `json:"correlation_key"`
	Source         Source     `json:"source"`
	State          State      `json:"state"`
	Name           string     `json:"name"`
	Severity       Severity   `json:"severity"`
	Entity         Entity     `json:"entity"`
	OpenedAt       time.Time  `json:"opened_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	EventIDs       []string   `json:"event_ids"`
	RunIDs         []string   `json:"run_ids,omitempty"`
	LastRunID      string     `json:"last_run_id,omitempty"`
}

type Job struct {
	ID          string     `json:"id"`
	IncidentID  string     `json:"incident_id"`
	EventID     string     `json:"event_id"`
	Status      JobStatus  `json:"status"`
	Attempts    int        `json:"attempts"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	RunID       string     `json:"run_id,omitempty"`
	Error       string     `json:"error,omitempty"`
	ErrorDetail string     `json:"error_detail,omitempty"`
}

type storeRecord struct {
	Version   int           `json:"version"`
	Kind      string        `json:"kind"`
	Delivery  *Delivery     `json:"delivery,omitempty"`
	Events    []StoredEvent `json:"events,omitempty"`
	Incidents []Incident    `json:"incidents,omitempty"`
	Jobs      []Job         `json:"jobs,omitempty"`
}

type Store struct {
	mu           sync.Mutex
	file         *os.File
	deliveries   map[string]Delivery
	events       map[string]StoredEvent
	incidents    map[string]Incident
	jobs         map[string]Job
	dedupe       map[string]string
	openIncident map[string]string
	wake         chan struct{}
	waiters      map[string][]chan Job
}

func OpenStore(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("alert store path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve alert store path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return nil, fmt.Errorf("create alert store directory: %w", err)
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open alert store: %w", err)
	}
	store := &Store{
		file: file, deliveries: make(map[string]Delivery), events: make(map[string]StoredEvent),
		incidents: make(map[string]Incident), jobs: make(map[string]Job), dedupe: make(map[string]string),
		openIncident: make(map[string]string), wake: make(chan struct{}, 1), waiters: make(map[string][]chan Job),
	}
	if err := store.load(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return store, nil
}

func (store *Store) Ingest(source string, body []byte, events []Event, receivedAt time.Time) (Delivery, error) {
	if store == nil {
		return Delivery{}, fmt.Errorf("alert store is nil")
	}
	if len(events) == 0 || len(events) > MaximumEventsPerBatch {
		return Delivery{}, fmt.Errorf("ingestion requires 1..%d events", MaximumEventsPerBatch)
	}
	hash := sha256.Sum256(body)
	delivery := Delivery{
		ID: newID("delivery"), Source: bounded(source, 64), ReceivedAt: receivedAt.UTC(),
		PayloadHash: hex.EncodeToString(hash[:]), Events: make([]DeliveryEvent, 0, len(events)),
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	workingDedupe := make(map[string]string, len(events))
	workingOpen := make(map[string]string, len(store.openIncident))
	for key, value := range store.openIncident {
		workingOpen[key] = value
	}
	changedIncidents := make(map[string]Incident)
	newEventsByID := make(map[string]StoredEvent)
	storedEvents := make([]StoredEvent, 0, len(events))
	jobs := make([]Job, 0, len(events))
	for _, event := range events {
		if err := event.Validate(); err != nil {
			return Delivery{}, err
		}
		dedupeKey := event.Source.Name + "\x00" + event.DeduplicationKey
		if existingID := firstNonEmpty(workingDedupe[dedupeKey], store.dedupe[dedupeKey]); existingID != "" {
			existing, exists := newEventsByID[existingID]
			if !exists {
				existing = store.events[existingID]
			}
			delivery.Events = append(delivery.Events, DeliveryEvent{
				EventID: existingID, IncidentID: existing.IncidentID, Status: DeliveryDuplicate,
			})
			continue
		}
		event.ID = newID("evt")
		correlationIndex := event.Source.Name + "\x00" + event.CorrelationKey
		incidentID := workingOpen[correlationIndex]
		incident, exists := changedIncidents[incidentID]
		if !exists && incidentID != "" {
			incident, exists = store.incidents[incidentID]
		}
		newIncident := !exists || incidentID == ""
		if newIncident {
			incident = Incident{
				ID: newID("inc"), CorrelationKey: event.CorrelationKey, Source: event.Source,
				State: event.State, Name: event.Name, Severity: event.Severity, Entity: event.Entity,
				OpenedAt: event.ObservedAt, UpdatedAt: event.ObservedAt,
			}
			incidentID = incident.ID
		}
		incident.State = event.State
		incident.Name, incident.Severity, incident.Entity = event.Name, event.Severity, event.Entity
		incident.UpdatedAt = event.ObservedAt
		incident.EventIDs = append(append([]string(nil), incident.EventIDs...), event.ID)
		jobID := ""
		if event.State == StateFiring {
			incident.ResolvedAt = nil
			workingOpen[correlationIndex] = incident.ID
			if newIncident {
				job := Job{
					ID: newID("job"), IncidentID: incident.ID, EventID: event.ID,
					Status: JobPending, CreatedAt: receivedAt.UTC(),
				}
				jobID = job.ID
				jobs = append(jobs, job)
			}
		} else {
			resolved := event.ObservedAt
			incident.ResolvedAt = &resolved
			delete(workingOpen, correlationIndex)
		}
		changedIncidents[incident.ID] = incident
		workingDedupe[dedupeKey] = event.ID
		stored := StoredEvent{Event: event, IncidentID: incident.ID, DeliveryID: delivery.ID}
		storedEvents = append(storedEvents, stored)
		newEventsByID[event.ID] = stored
		delivery.Events = append(delivery.Events, DeliveryEvent{
			EventID: event.ID, IncidentID: incident.ID, JobID: jobID, Status: DeliveryAccepted,
		})
	}
	incidents := make([]Incident, 0, len(changedIncidents))
	for _, incident := range changedIncidents {
		incidents = append(incidents, incident)
	}
	sort.Slice(incidents, func(left, right int) bool { return incidents[left].ID < incidents[right].ID })
	record := storeRecord{Version: storeVersion, Kind: "ingestion", Delivery: &delivery, Events: storedEvents, Incidents: incidents, Jobs: jobs}
	if err := store.append(record); err != nil {
		return Delivery{}, err
	}
	store.deliveries[delivery.ID] = cloneDelivery(delivery)
	for _, event := range storedEvents {
		store.events[event.ID] = cloneStoredEvent(event)
		store.dedupe[event.Source.Name+"\x00"+event.DeduplicationKey] = event.ID
	}
	for _, incident := range incidents {
		store.incidents[incident.ID] = cloneIncident(incident)
	}
	store.openIncident = workingOpen
	for _, job := range jobs {
		store.jobs[job.ID] = cloneJob(job)
	}
	if len(jobs) > 0 {
		store.signal()
	}
	return cloneDelivery(delivery), nil
}

func (store *Store) ClaimNext() (Job, bool, error) {
	if store == nil {
		return Job{}, false, fmt.Errorf("alert store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	pending := make([]Job, 0)
	for _, job := range store.jobs {
		if job.Status == JobPending {
			pending = append(pending, job)
		}
	}
	if len(pending) == 0 {
		return Job{}, false, nil
	}
	sort.Slice(pending, func(left, right int) bool { return pending[left].CreatedAt.Before(pending[right].CreatedAt) })
	job := pending[0]
	now := time.Now().UTC()
	job.Status, job.StartedAt, job.CompletedAt, job.Error, job.ErrorDetail = JobRunning, &now, nil, "", ""
	job.Attempts++
	if err := store.append(storeRecord{Version: storeVersion, Kind: "job_updated", Jobs: []Job{job}}); err != nil {
		return Job{}, false, err
	}
	store.jobs[job.ID] = cloneJob(job)
	return cloneJob(job), true, nil
}

func (store *Store) CompleteJob(jobID string, status JobStatus, runID, jobError, errorDetail string) error {
	if store == nil {
		return fmt.Errorf("alert store is nil")
	}
	if status != JobSucceeded && status != JobFailed {
		return fmt.Errorf("invalid final job status %q", status)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	job, exists := store.jobs[jobID]
	if !exists || job.Status != JobRunning {
		return fmt.Errorf("investigation job %q is not running", jobID)
	}
	now := time.Now().UTC()
	job.Status, job.CompletedAt, job.RunID = status, &now, bounded(runID, 256)
	job.Error = bounded(jobError, 1024)
	job.ErrorDetail = SanitizeErrorDetail(errorDetail)
	incident := store.incidents[job.IncidentID]
	incidents := []Incident(nil)
	if job.RunID != "" && incident.ID != "" {
		incident.LastRunID = job.RunID
		incident.RunIDs = appendUnique(incident.RunIDs, job.RunID)
		incidents = append(incidents, incident)
	}
	if err := store.append(storeRecord{Version: storeVersion, Kind: "job_updated", Jobs: []Job{job}, Incidents: incidents}); err != nil {
		return err
	}
	store.jobs[job.ID] = cloneJob(job)
	if len(incidents) > 0 {
		store.incidents[incident.ID] = cloneIncident(incident)
	}
	store.notifyWaiters(job)
	return nil
}

func (store *Store) RecoverRunning() (int, error) {
	if store == nil {
		return 0, nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	recovered := make([]Job, 0)
	for _, job := range store.jobs {
		if job.Status == JobRunning {
			job.Status, job.StartedAt, job.CompletedAt = JobPending, nil, nil
			job.Error, job.ErrorDetail, job.RunID = "", "", ""
			recovered = append(recovered, job)
		}
	}
	if len(recovered) == 0 {
		return 0, nil
	}
	if err := store.append(storeRecord{Version: storeVersion, Kind: "jobs_recovered", Jobs: recovered}); err != nil {
		return 0, err
	}
	for _, job := range recovered {
		store.jobs[job.ID] = cloneJob(job)
	}
	store.signal()
	return len(recovered), nil
}

// SanitizeErrorDetail makes an internal failure safe to persist and expose
// through the authenticated investigation-job API. It is deliberately
// conservative: common credential forms are redacted and whitespace and size
// are bounded before the value reaches the append-only store.
func SanitizeErrorDetail(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = sensitiveErrorValues[0].ReplaceAllString(value, "$1 [REDACTED]")
	for _, pattern := range sensitiveErrorValues[1:] {
		value = pattern.ReplaceAllString(value, "${1}[REDACTED]")
	}
	return bounded(value, maximumErrorDetailRunes)
}

func (store *Store) WaitJob(ctx context.Context, jobID string) (Job, error) {
	store.mu.Lock()
	job, exists := store.jobs[jobID]
	if !exists {
		store.mu.Unlock()
		return Job{}, fmt.Errorf("unknown investigation job %q", jobID)
	}
	if job.Status == JobSucceeded || job.Status == JobFailed {
		store.mu.Unlock()
		return cloneJob(job), nil
	}
	waiter := make(chan Job, 1)
	store.waiters[jobID] = append(store.waiters[jobID], waiter)
	store.mu.Unlock()
	select {
	case result := <-waiter:
		return result, nil
	case <-ctx.Done():
		store.mu.Lock()
		waiting := store.waiters[jobID]
		for index, candidate := range waiting {
			if candidate == waiter {
				store.waiters[jobID] = append(waiting[:index], waiting[index+1:]...)
				break
			}
		}
		if len(store.waiters[jobID]) == 0 {
			delete(store.waiters, jobID)
		}
		store.mu.Unlock()
		return Job{}, ctx.Err()
	}
}

func (store *Store) Wake() <-chan struct{} {
	if store == nil {
		return nil
	}
	return store.wake
}

func (store *Store) GetEvent(id string) (StoredEvent, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, exists := store.events[id]
	return cloneStoredEvent(value), exists
}

func (store *Store) GetIncident(id string) (Incident, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, exists := store.incidents[id]
	return cloneIncident(value), exists
}

func (store *Store) GetJob(id string) (Job, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, exists := store.jobs[id]
	return cloneJob(value), exists
}

func (store *Store) GetDelivery(id string) (Delivery, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, exists := store.deliveries[id]
	return cloneDelivery(value), exists
}

func (store *Store) EventsForIncident(incidentID string) []StoredEvent {
	store.mu.Lock()
	defer store.mu.Unlock()
	incident, exists := store.incidents[incidentID]
	if !exists {
		return nil
	}
	values := make([]StoredEvent, 0, len(incident.EventIDs))
	for _, id := range incident.EventIDs {
		if event, exists := store.events[id]; exists {
			values = append(values, cloneStoredEvent(event))
		}
	}
	return values
}

func (store *Store) JobsForIncident(incidentID string) []Job {
	store.mu.Lock()
	defer store.mu.Unlock()
	values := make([]Job, 0)
	for _, job := range store.jobs {
		if job.IncidentID == incidentID {
			values = append(values, cloneJob(job))
		}
	}
	sort.Slice(values, func(left, right int) bool { return values[left].CreatedAt.Before(values[right].CreatedAt) })
	return values
}

func (store *Store) ListIncidents(limit int) []Incident {
	store.mu.Lock()
	defer store.mu.Unlock()
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	values := make([]Incident, 0, len(store.incidents))
	for _, value := range store.incidents {
		values = append(values, cloneIncident(value))
	}
	sort.Slice(values, func(left, right int) bool { return values[left].UpdatedAt.After(values[right].UpdatedAt) })
	if len(values) > limit {
		values = values[:limit]
	}
	return values
}

func (store *Store) Close() error {
	if store == nil || store.file == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.file.Close()
}

func (store *Store) load() error {
	if _, err := store.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek alert store: %w", err)
	}
	scanner := bufio.NewScanner(store.file)
	scanner.Buffer(make([]byte, 64*1024), maximumStoreRecordBytes)
	line := 0
	for scanner.Scan() {
		line++
		var record storeRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return fmt.Errorf("decode alert store line %d: %w", line, err)
		}
		if record.Version != storeVersion {
			return fmt.Errorf("alert store line %d has unsupported version %d", line, record.Version)
		}
		switch record.Kind {
		case "ingestion":
			if record.Delivery == nil || record.Delivery.ID == "" {
				return fmt.Errorf("alert store line %d has invalid delivery", line)
			}
			store.deliveries[record.Delivery.ID] = cloneDelivery(*record.Delivery)
			for _, event := range record.Events {
				if event.ID == "" || event.IncidentID == "" {
					return fmt.Errorf("alert store line %d has invalid event", line)
				}
				store.events[event.ID] = cloneStoredEvent(event)
			}
		case "job_updated", "jobs_recovered":
		default:
			return fmt.Errorf("alert store line %d has unknown kind %q", line, record.Kind)
		}
		for _, incident := range record.Incidents {
			store.incidents[incident.ID] = cloneIncident(incident)
		}
		for _, job := range record.Jobs {
			store.jobs[job.ID] = cloneJob(job)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan alert store: %w", err)
	}
	for _, event := range store.events {
		store.dedupe[event.Source.Name+"\x00"+event.DeduplicationKey] = event.ID
	}
	for _, incident := range store.incidents {
		if incident.State == StateFiring {
			store.openIncident[incident.Source.Name+"\x00"+incident.CorrelationKey] = incident.ID
		}
	}
	_, err := store.file.Seek(0, io.SeekEnd)
	return err
}

func (store *Store) append(record storeRecord) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode alert store record: %w", err)
	}
	if len(encoded)+1 > maximumStoreRecordBytes {
		return fmt.Errorf("alert store record exceeds %d bytes", maximumStoreRecordBytes)
	}
	encoded = append(encoded, '\n')
	written, err := store.file.Write(encoded)
	if err != nil {
		return fmt.Errorf("append alert store record: %w", err)
	}
	if written != len(encoded) {
		return fmt.Errorf("append alert store record: short write")
	}
	if err := store.file.Sync(); err != nil {
		return fmt.Errorf("sync alert store record: %w", err)
	}
	return nil
}

func (store *Store) signal() {
	select {
	case store.wake <- struct{}{}:
	default:
	}
}

func (store *Store) notifyWaiters(job Job) {
	for _, waiter := range store.waiters[job.ID] {
		waiter <- cloneJob(job)
		close(waiter)
	}
	delete(store.waiters, job.ID)
}

func newID(prefix string) string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(buffer)
}

func cloneDelivery(value Delivery) Delivery {
	value.Events = append([]DeliveryEvent(nil), value.Events...)
	return value
}

func cloneStoredEvent(value StoredEvent) StoredEvent {
	value.Labels = cloneMap(value.Labels)
	value.Annotations = cloneMap(value.Annotations)
	value.RelatedEntities = append([]Entity(nil), value.RelatedEntities...)
	value.Links = append([]Link(nil), value.Links...)
	if value.EndedAt != nil {
		ended := *value.EndedAt
		value.EndedAt = &ended
	}
	return value
}

func cloneIncident(value Incident) Incident {
	value.EventIDs = append([]string(nil), value.EventIDs...)
	value.RunIDs = append([]string(nil), value.RunIDs...)
	if value.ResolvedAt != nil {
		resolved := *value.ResolvedAt
		value.ResolvedAt = &resolved
	}
	return value
}

func cloneJob(value Job) Job {
	if value.StartedAt != nil {
		started := *value.StartedAt
		value.StartedAt = &started
	}
	if value.CompletedAt != nil {
		completed := *value.CompletedAt
		value.CompletedAt = &completed
	}
	return value
}

func cloneMap(value map[string]string) map[string]string {
	if len(value) == 0 {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
