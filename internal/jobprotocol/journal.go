package jobprotocol

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrJournalUnavailable = errors.New("job journal unavailable")

// JournalOptions controls local journal retention and capacity.
type JournalOptions struct {
	// Path 是 journal JSON 文件路径；为空时 journal 仅保存在内存。
	Path string
	// Retention 是 journal 记录保留时长；空值使用默认保留时长。
	Retention time.Duration
	// MaxRecords 是 journal 最多保留记录数；空值使用默认容量。
	MaxRecords int
	// MaxActive 是可同时接受的非终态 job 数量；空值使用默认并发容量。
	MaxActive int
}

// Journal stores accepted job metadata and terminal results for reconciliation.
type Journal struct {
	// mu 保护 journal 记录、最近错误和持久化写入。
	mu sync.Mutex
	// path 是 journal JSON 文件路径；为空时仅使用内存记录。
	path string
	// retention 是终态记录允许保留的最长时间。
	retention time.Duration
	// maxRecords 是 journal 最多保留的记录数量。
	maxRecords int
	// maxActive 是允许同时处于非终态的 job 数量。
	maxActive int
	// records 保存按 job ID 索引的本地 journal 记录。
	records map[string]Record
	// recentErrs 保存最近的脱敏 journal 错误摘要。
	recentErrs []string
}

// NewJournal loads or creates a bounded local job journal.
// options defines storage path, retention and capacity, and the function returns the journal plus any load error that leaves it in memory-only mode.
func NewJournal(options JournalOptions) (*Journal, error) {
	j := &Journal{
		path:       strings.TrimSpace(options.Path),
		retention:  firstDuration(options.Retention, 24*time.Hour),
		maxRecords: firstInt(options.MaxRecords, 256),
		maxActive:  firstInt(options.MaxActive, 1),
		records:    map[string]Record{},
	}
	err := j.load()
	j.cleanupLocked(time.Now().UTC())
	if saveErr := j.saveLocked(); saveErr != nil && err == nil {
		err = saveErr
	}
	if err != nil {
		j.rememberError(err.Error())
	}
	return j, err
}

// Accept validates and records one new execution job envelope.
// envelope is the requested job and endpoint identity describes this scum_run instance; it returns an ack that decides whether execution may continue.
func (j *Journal) Accept(envelope Envelope, endpointID string, endpointGeneration uint64, supported bool) Ack {
	now := time.Now().UTC()
	endpointID = strings.TrimSpace(endpointID)
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cleanupLocked(now)
	if strings.TrimSpace(envelope.JobID) == "" {
		return Ack{EndpointID: endpointID, EndpointGeneration: endpointGeneration, State: AckInvalid, Reason: "missing jobId"}
	}
	if !supported {
		return Ack{JobID: envelope.JobID, EndpointID: endpointID, EndpointGeneration: endpointGeneration, State: AckUnsupported, Reason: "capability is not job-protocol ready"}
	}
	if envelope.EndpointGeneration > 0 && envelope.EndpointGeneration != endpointGeneration {
		return Ack{JobID: envelope.JobID, EndpointID: endpointID, EndpointGeneration: endpointGeneration, State: AckGenerationMismatch, Reason: "endpoint generation mismatch"}
	}
	if record, ok := j.findLocked(envelope.JobID, envelope.IdempotencyKey); ok {
		return Ack{
			JobID:              record.JobID,
			EndpointID:         endpointID,
			EndpointGeneration: endpointGeneration,
			State:              AckDuplicate,
			Reason:             "duplicate idempotency key or job id",
			CurrentState:       record.JobState,
			Result:             record.Result,
		}
	}
	if j.activeCountLocked() >= j.maxActive {
		return Ack{
			JobID:              envelope.JobID,
			EndpointID:         endpointID,
			EndpointGeneration: endpointGeneration,
			State:              AckBusy,
			RetryAfterMS:       1000,
			Reason:             "execution job capacity is full",
		}
	}
	record := Record{
		JobID:              strings.TrimSpace(envelope.JobID),
		IdempotencyKey:     strings.TrimSpace(envelope.IdempotencyKey),
		EndpointGeneration: endpointGeneration,
		CapabilityKey:      strings.TrimSpace(envelope.CapabilityKey),
		ActionKey:          strings.TrimSpace(envelope.ActionKey),
		PayloadSummary:     PayloadSummary(envelope.Payload),
		PurposeSummary:     RedactText(envelope.PurposeSummary),
		AckState:           AckAccepted,
		JobState:           JobStateAccepted,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	j.records[record.JobID] = record
	if err := j.saveLocked(); err != nil {
		j.rememberErrorLocked(err.Error())
	}
	acceptedAt := now
	return Ack{JobID: record.JobID, EndpointID: endpointID, EndpointGeneration: endpointGeneration, State: AckAccepted, AcceptedAt: &acceptedAt}
}

// MarkProgress records a bounded progress checkpoint for a running job.
// progress contains the latest execution phase and sequence, and the function returns the stored progress or an error when the job is unknown.
func (j *Journal) MarkProgress(progress Progress) (Progress, error) {
	now := time.Now().UTC()
	if progress.Timestamp.IsZero() {
		progress.Timestamp = now
	}
	progress.DetailSummary = RedactText(progress.DetailSummary)
	j.mu.Lock()
	defer j.mu.Unlock()
	record, ok := j.records[progress.JobID]
	if !ok {
		return Progress{}, ErrJournalUnavailable
	}
	record.JobState = JobStateRunning
	record.Progress = &progress
	record.UpdatedAt = now
	j.records[record.JobID] = record
	if err := j.saveLocked(); err != nil {
		j.rememberErrorLocked(err.Error())
	}
	return progress, nil
}

// MarkResult records and persists one terminal execution result.
// result contains bounded job output or sanitized failure data, and the function returns the stored result or an error when the job is unknown.
func (j *Journal) MarkResult(result Result) (Result, error) {
	now := time.Now().UTC()
	if result.FinishedAt.IsZero() {
		result.FinishedAt = now
	}
	result.Summary = RedactText(result.Summary)
	result.ErrorSummary = RedactText(result.ErrorSummary)
	result.Data = RedactMap(result.Data)
	j.mu.Lock()
	defer j.mu.Unlock()
	record, ok := j.records[result.JobID]
	if !ok {
		return Result{}, ErrJournalUnavailable
	}
	if strings.TrimSpace(result.Status) == "" {
		result.Status = JobStateSucceeded
	}
	record.JobState = result.Status
	record.Result = &result
	record.UpdatedAt = now
	j.records[record.JobID] = record
	if err := j.saveLocked(); err != nil {
		j.rememberErrorLocked(err.Error())
	}
	return result, nil
}

// Cancel records a structured best-effort cancellation outcome for a known job.
// request identifies the target job, endpoint identity describes this scum_run instance, and the function returns the protocol outcome.
func (j *Journal) Cancel(request CancelRequest, endpointID string, endpointGeneration uint64) CancelOutcome {
	now := time.Now().UTC()
	j.mu.Lock()
	defer j.mu.Unlock()
	record, ok := j.findLocked(request.JobID, request.IdempotencyKey)
	outcome := CancelOutcome{EndpointID: endpointID, EndpointGeneration: endpointGeneration, Timestamp: now}
	if !ok {
		outcome.Outcome = CancelNotFound
		outcome.Reason = "job not found in local journal"
		return outcome
	}
	outcome.JobID = record.JobID
	if terminalState(record.JobState) {
		outcome.Outcome = CancelAlreadyFinished
		outcome.Reason = "job already reached terminal state"
	} else {
		outcome.Outcome = CancelUnsupported
		outcome.Reason = "job cancellation is not supported for active scum_run handlers"
	}
	record.Cancellation = &outcome
	record.UpdatedAt = now
	j.records[record.JobID] = record
	if err := j.saveLocked(); err != nil {
		j.rememberErrorLocked(err.Error())
	}
	return outcome
}

// Reconcile returns the current local journal state for a previous job.
// request identifies the target and expected generation, endpoint identity describes this scum_run instance, and the function returns a safe reconciliation response.
func (j *Journal) Reconcile(request ReconcileRequest, endpointID string, endpointGeneration uint64) ReconcileResponse {
	now := time.Now().UTC()
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cleanupLocked(now)
	response := ReconcileResponse{EndpointID: endpointID, EndpointGeneration: endpointGeneration, Timestamp: now}
	record, ok := j.findLocked(request.JobID, request.IdempotencyKey)
	if !ok {
		response.State = ReconcileUnknown
		response.Reason = "job not found in local journal"
		return response
	}
	response.JobID = record.JobID
	response.CurrentJobState = record.JobState
	if request.EndpointGeneration > 0 && request.EndpointGeneration != record.EndpointGeneration {
		response.State = ReconcileGenerationMismatch
		response.Reason = "requested endpoint generation does not match journal record"
		return response
	}
	if now.Sub(record.UpdatedAt) > j.retention {
		response.State = ReconcileExpired
		response.Reason = "journal record expired"
		return response
	}
	if terminalState(record.JobState) {
		response.State = ReconcileTerminal
		response.Result = record.Result
		response.Reason = "terminal result replayed from local journal"
		return response
	}
	response.State = ReconcileRunning
	response.Reason = "job is still active or accepted locally"
	return response
}

// Readiness returns a safe protocol readiness summary for this endpoint.
// endpoint identity and capabilities describe this scum_run instance, and the function returns capacity, journal and recent error state.
func (j *Journal) Readiness(endpointID string, endpointGeneration uint64, capabilities []string) Readiness {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cleanupLocked(time.Now().UTC())
	return Readiness{
		ProtocolVersion:         ProtocolVersion,
		EndpointID:              endpointID,
		EndpointGeneration:      endpointGeneration,
		SupportedCapabilities:   append([]string(nil), capabilities...),
		QueueCapacity:           j.maxActive,
		ActiveJobs:              j.activeCountLocked(),
		JournalAvailable:        true,
		CancellationSupported:   true,
		ReconciliationSupported: true,
		RecentErrors:            append([]string(nil), j.recentErrs...),
	}
}

// RecentErrors returns the current redacted journal error summaries.
// It takes no parameters and returns a copy so callers cannot mutate journal state.
func (j *Journal) RecentErrors() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.recentErrs...)
}

// load reads persisted records from disk when a journal path is configured.
// It takes no parameters and returns an error when persisted JSON cannot be loaded.
func (j *Journal) load() error {
	if j.path == "" {
		return nil
	}
	data, err := os.ReadFile(j.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var records []Record
	if err := json.Unmarshal(data, &records); err != nil {
		return err
	}
	for _, record := range records {
		if strings.TrimSpace(record.JobID) != "" {
			j.records[record.JobID] = record
		}
	}
	return nil
}

// saveLocked persists the current journal snapshot to disk.
// The caller must hold j.mu, and the function returns any filesystem or JSON serialization error.
func (j *Journal) saveLocked() error {
	if j.path == "" {
		return nil
	}
	records := j.sortedRecordsLocked()
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0755); err != nil {
		return err
	}
	tmpPath := j.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, j.path)
}

// cleanupLocked removes expired and over-capacity records from the journal.
// now is the current time used for retention checks, and the method mutates in-memory records without returning a value.
func (j *Journal) cleanupLocked(now time.Time) {
	for key, record := range j.records {
		if terminalState(record.JobState) && now.Sub(record.UpdatedAt) > j.retention {
			delete(j.records, key)
		}
	}
	records := j.sortedRecordsLocked()
	for len(records) > j.maxRecords {
		delete(j.records, records[0].JobID)
		records = records[1:]
	}
}

// sortedRecordsLocked returns journal records ordered by update time.
// The caller must hold j.mu, and the function returns a new slice for persistence and cleanup decisions.
func (j *Journal) sortedRecordsLocked() []Record {
	records := make([]Record, 0, len(j.records))
	for _, record := range j.records {
		records = append(records, record)
	}
	sort.Slice(records, func(i, k int) bool {
		return records[i].UpdatedAt.Before(records[k].UpdatedAt)
	})
	return records
}

// findLocked locates one record by job ID or idempotency key.
// The caller must hold j.mu, and the function returns the record and true when a match exists.
func (j *Journal) findLocked(jobID string, idempotencyKey string) (Record, bool) {
	jobID = strings.TrimSpace(jobID)
	if jobID != "" {
		if record, ok := j.records[jobID]; ok {
			return record, true
		}
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return Record{}, false
	}
	for _, record := range j.records {
		if record.IdempotencyKey == idempotencyKey {
			return record, true
		}
	}
	return Record{}, false
}

// activeCountLocked counts non-terminal records in the journal.
// The caller must hold j.mu, and the function returns the active job count used for backpressure.
func (j *Journal) activeCountLocked() int {
	active := 0
	for _, record := range j.records {
		if !terminalState(record.JobState) {
			active++
		}
	}
	return active
}

// rememberError stores one redacted journal error summary.
// summary is the raw error text, and the method keeps only the most recent five safe summaries.
func (j *Journal) rememberError(summary string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.rememberErrorLocked(summary)
}

// rememberErrorLocked stores one redacted journal error summary while locked.
// summary is the raw error text, and the method mutates recentErrs without returning a value.
func (j *Journal) rememberErrorLocked(summary string) {
	summary = RedactText(summary)
	if summary == "" {
		return
	}
	j.recentErrs = append(j.recentErrs, summary)
	if len(j.recentErrs) > 5 {
		j.recentErrs = j.recentErrs[len(j.recentErrs)-5:]
	}
}

// terminalState reports whether a job state can no longer progress.
// state is the current journal state, and the function returns true for succeeded, failed and cancelled.
func terminalState(state string) bool {
	switch strings.TrimSpace(state) {
	case JobStateSucceeded, JobStateFailed, JobStateCancelled:
		return true
	default:
		return false
	}
}

// firstDuration returns a configured duration or a fallback.
// value is the configured duration and fallback is used when value is zero, and the function returns the effective duration.
func firstDuration(value time.Duration, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

// firstInt returns a configured positive integer or a fallback.
// value is the configured integer and fallback is used when value is not positive, and the function returns the effective integer.
func firstInt(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
