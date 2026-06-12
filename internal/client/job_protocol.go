package client

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"scum_run/config"
	"scum_run/internal/database"
	"scum_run/internal/jobprotocol"
)

const (
	MsgTypeExecutionJob          = "execution_job"
	MsgTypeExecutionJobAck       = "execution_job_ack"
	MsgTypeExecutionJobProgress  = "execution_job_progress"
	MsgTypeExecutionJobResult    = "execution_job_result"
	MsgTypeExecutionJobCancel    = "execution_job_cancel"
	MsgTypeExecutionJobReconcile = "execution_job_reconcile"
	MsgTypeExecutionJobReadiness = "execution_job_readiness"
)

var jobCapableCapabilities = []string{
	"process.restart",
	"process.start",
	"process.status",
	"process.stop",
	"scum.db.query",
	"scum.db.query.readonly",
}

// handleExecutionJob validates and executes one job-protocol request from scum_server.
// data is the decoded WebSocket payload, and the method returns no values while sending ack, progress and terminal result messages back to the control channel.
func (c *Client) handleExecutionJob(data interface{}) {
	envelope, err := decodeJobEnvelope(data)
	if err != nil {
		c.sendResponse(MsgTypeExecutionJobAck, jobprotocol.Ack{EndpointID: c.jobEndpointID, EndpointGeneration: c.jobGeneration, State: jobprotocol.AckInvalid, Reason: jobprotocol.SafeError(err)}, jobprotocol.SafeError(err))
		return
	}
	if !c.jobControlChannelAuthorized() {
		c.sendResponse(MsgTypeExecutionJobAck, jobprotocol.Ack{JobID: envelope.JobID, EndpointID: c.jobEndpointID, EndpointGeneration: c.jobGeneration, State: jobprotocol.AckRejected, Reason: "control channel is not authenticated"}, "control channel is not authenticated")
		return
	}
	ack := c.jobJournal.Accept(envelope, c.jobEndpointID, c.jobGeneration, c.supportsJobCapability(envelope.CapabilityKey))
	c.sendResponse(MsgTypeExecutionJobAck, ack, ackError(ack))
	if ack.State != jobprotocol.AckAccepted {
		return
	}

	started := time.Now()
	c.sendJobProgress(envelope, "running", 5, "job accepted for local execution", 1)
	result := c.executeJobEnvelope(envelope, started)
	if _, err := c.jobJournal.MarkResult(result); err != nil {
		result.Status = jobprotocol.JobStateFailed
		result.ResultCode = "journal_result_failed"
		result.ErrorSummary = jobprotocol.SafeError(err)
	}
	c.sendResponse(MsgTypeExecutionJobResult, result, "")
}

// handleExecutionJobCancel records a structured best-effort cancellation outcome.
// data is the decoded WebSocket payload, and the method returns no values while sending the cancellation outcome response.
func (c *Client) handleExecutionJobCancel(data interface{}) {
	request, err := decodeJobCancelRequest(data)
	if err != nil {
		c.sendResponse(MsgTypeExecutionJobCancel, jobprotocol.CancelOutcome{EndpointID: c.jobEndpointID, EndpointGeneration: c.jobGeneration, Outcome: jobprotocol.CancelFailed, Reason: jobprotocol.SafeError(err), Timestamp: time.Now().UTC()}, jobprotocol.SafeError(err))
		return
	}
	outcome := c.jobJournal.Cancel(request, c.jobEndpointID, c.jobGeneration)
	c.sendResponse(MsgTypeExecutionJobCancel, outcome, "")
}

// handleExecutionJobReconcile resolves a previous job from the local journal.
// data is the decoded WebSocket payload, and the method returns no values while sending a reconciliation response to scum_server.
func (c *Client) handleExecutionJobReconcile(data interface{}) {
	request, err := decodeJobReconcileRequest(data)
	if err != nil {
		c.sendResponse(MsgTypeExecutionJobReconcile, jobprotocol.ReconcileResponse{EndpointID: c.jobEndpointID, EndpointGeneration: c.jobGeneration, State: jobprotocol.ReconcileUnknown, Reason: jobprotocol.SafeError(err), Timestamp: time.Now().UTC()}, jobprotocol.SafeError(err))
		return
	}
	response := c.jobJournal.Reconcile(request, c.jobEndpointID, c.jobGeneration)
	c.sendResponse(MsgTypeExecutionJobReconcile, response, "")
}

// handleExecutionJobReadiness returns endpoint capacity and protocol readiness.
// It takes no parameters and sends a readiness response with journal, cancellation and reconciliation support summaries.
func (c *Client) handleExecutionJobReadiness() {
	capabilities := append([]string(nil), jobCapableCapabilities...)
	sort.Strings(capabilities)
	c.sendResponse(MsgTypeExecutionJobReadiness, c.jobJournal.Readiness(c.jobEndpointID, c.jobGeneration, capabilities), "")
}

// executeJobEnvelope runs one supported capability and returns a terminal result.
// envelope contains the accepted job payload and startedAt is used for latency reporting, and the function returns a bounded sanitized terminal result.
func (c *Client) executeJobEnvelope(envelope jobprotocol.Envelope, startedAt time.Time) jobprotocol.Result {
	result := jobprotocol.Result{
		JobID:              envelope.JobID,
		EndpointID:         c.jobEndpointID,
		EndpointGeneration: c.jobGeneration,
		Status:             jobprotocol.JobStateSucceeded,
		ResultCode:         "ok",
		FinishedAt:         time.Now().UTC(),
	}
	data, summary, err := c.executeJobCapability(envelope)
	result.DurationMS = time.Since(startedAt).Milliseconds()
	if err != nil {
		result.Status = jobprotocol.JobStateFailed
		result.ResultCode = "execution_failed"
		result.Retryable = true
		result.ErrorSummary = jobprotocol.SafeError(err)
		result.Summary = "execution job failed"
		return result
	}
	result.Data = data
	result.Summary = summary
	if truncated, ok := data["truncated"].(bool); ok {
		result.Truncated = truncated
	}
	if truncatedBy, ok := data["truncatedBy"].(string); ok {
		result.TruncatedBy = truncatedBy
	}
	return result
}

// executeJobCapability dispatches an accepted job to the local SCUM capability implementation.
// envelope contains the capability key and payload, and the function returns bounded data, a safe summary, or an execution error.
func (c *Client) executeJobCapability(envelope jobprotocol.Envelope) (map[string]any, string, error) {
	switch strings.TrimSpace(envelope.CapabilityKey) {
	case "scum.db.query.readonly":
		return c.executeDatabaseJob(envelope, true)
	case "scum.db.query":
		return c.executeDatabaseJob(envelope, boolFromJobPayload(envelope.Payload, "readOnly", "read_only"))
	case "process.start":
		if err := c.process.Start(); err != nil {
			return nil, "", err
		}
		return map[string]any{"status": c.process.GetStatus()}, "process started", nil
	case "process.stop":
		if err := c.process.Stop(); err != nil {
			return nil, "", err
		}
		return map[string]any{"status": c.process.GetStatus()}, "process stopped", nil
	case "process.restart":
		if err := c.process.Restart(); err != nil {
			return nil, "", err
		}
		return map[string]any{"status": c.process.GetStatus()}, "process restarted", nil
	case "process.status":
		return map[string]any{"status": c.process.GetStatus()}, "process status returned", nil
	default:
		return nil, "", fmt.Errorf("unsupported execution job capability: %s", jobprotocol.RedactText(envelope.CapabilityKey))
	}
}

// executeDatabaseJob runs a bounded SCUM.db query through the local database client.
// envelope contains SQL payload and limits while readOnly forces read-only execution, and the function returns bounded result data or a sanitized error.
func (c *Client) executeDatabaseJob(envelope jobprotocol.Envelope, readOnly bool) (map[string]any, string, error) {
	query := stringFromJobPayload(envelope.Payload, "query", "sql")
	if strings.TrimSpace(query) == "" {
		return nil, "", fmt.Errorf("missing database query")
	}
	options := database.QueryOptions{
		QueryID:  firstNonEmptyJobString(stringFromJobPayload(envelope.Payload, "queryId", "query_id"), envelope.JobID),
		Args:     databaseArgsFromMessage(envelope.Payload["args"]),
		Timeout:  durationFromMilliseconds(firstJobValue(envelope.Payload, "timeoutMs", "timeout_ms")),
		MaxRows:  intFromJobValue(firstLimitValue(envelope, "maxRows", "max_rows")),
		MaxBytes: intFromJobValue(firstLimitValue(envelope, "maxBytes", "max_bytes")),
	}
	var (
		result database.QueryResult
		err    error
	)
	if readOnly {
		result, err = c.db.ExecuteReadOnlyCapability(query, options)
	} else {
		result, err = c.db.ExecuteCapability(query, options)
	}
	if err != nil {
		return nil, "", err
	}
	data := map[string]any{
		"queryId":      result.QueryID,
		"action":       result.Action,
		"columns":      result.Columns,
		"rows":         result.Rows,
		"rowCount":     len(result.Rows),
		"rowsAffected": result.RowsAffected,
		"truncated":    result.Truncated,
		"truncatedBy":  result.TruncatedBy,
		"durationMs":   result.DurationMS,
	}
	return data, "database query completed", nil
}

// sendJobProgress records and emits one bounded progress update.
// envelope identifies the job, phase and percent describe progress, and the method returns no values while best-effort sending the response.
func (c *Client) sendJobProgress(envelope jobprotocol.Envelope, phase string, percent uint8, summary string, sequence uint64) {
	progress := jobprotocol.Progress{
		JobID:              envelope.JobID,
		EndpointID:         c.jobEndpointID,
		EndpointGeneration: c.jobGeneration,
		Phase:              phase,
		Percent:            percent,
		DetailSummary:      summary,
		Sequence:           sequence,
		Timestamp:          time.Now().UTC(),
	}
	stored, err := c.jobJournal.MarkProgress(progress)
	if err != nil {
		c.logger.Warn("Failed to record execution job progress: %s", jobprotocol.SafeError(err))
		stored = progress
	}
	c.sendResponse(MsgTypeExecutionJobProgress, stored, "")
}

// supportsJobCapability reports whether scum_run can execute a capability through the job protocol.
// capability is the requested capability key, and the function returns true for the supported local DB and process capabilities.
func (c *Client) supportsJobCapability(capability string) bool {
	for _, supported := range jobCapableCapabilities {
		if strings.EqualFold(strings.TrimSpace(capability), supported) {
			return true
		}
	}
	return false
}

// jobControlChannelAuthorized reports whether the current WebSocket channel can accept control-plane jobs.
// It takes no parameters and returns true when the authenticated client connection is currently established.
func (c *Client) jobControlChannelAuthorized() bool {
	return c.wsClient != nil && c.wsClient.IsConnected()
}

// decodeJobEnvelope converts a WebSocket payload into a job envelope.
// data is the decoded JSON payload and the function returns an envelope or an error when the shape is invalid.
func decodeJobEnvelope(data interface{}) (jobprotocol.Envelope, error) {
	var envelope jobprotocol.Envelope
	if err := decodeJobPayload(data, &envelope); err != nil {
		return envelope, err
	}
	if envelope.Payload == nil {
		envelope.Payload = map[string]any{}
	}
	return envelope, nil
}

// decodeJobCancelRequest converts a WebSocket payload into a cancellation request.
// data is the decoded JSON payload and the function returns a request or an error when the shape is invalid.
func decodeJobCancelRequest(data interface{}) (jobprotocol.CancelRequest, error) {
	var request jobprotocol.CancelRequest
	return request, decodeJobPayload(data, &request)
}

// decodeJobReconcileRequest converts a WebSocket payload into a reconciliation request.
// data is the decoded JSON payload and the function returns a request or an error when the shape is invalid.
func decodeJobReconcileRequest(data interface{}) (jobprotocol.ReconcileRequest, error) {
	var request jobprotocol.ReconcileRequest
	return request, decodeJobPayload(data, &request)
}

// decodeJobPayload re-decodes a generic WebSocket payload into a typed protocol DTO.
// data is the decoded JSON payload and target is the destination DTO pointer, and the function returns any marshal or unmarshal error.
func decodeJobPayload(data interface{}, target interface{}) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, target)
}

// ackError maps protocol ack states into the legacy WebSocket success flag.
// ack is the structured acknowledgement, and the function returns an error message only for rejected ack states.
func ackError(ack jobprotocol.Ack) string {
	switch ack.State {
	case jobprotocol.AckAccepted, jobprotocol.AckDuplicate, jobprotocol.AckQueued:
		return ""
	default:
		return ack.Reason
	}
}

// defaultJobJournalPath returns the local file path used for job journal persistence.
// It takes no parameters and returns a user-cache path with a temp-directory fallback.
func defaultJobJournalPath() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(cacheDir) == "" {
		cacheDir = os.TempDir()
	}
	return filepath.Join(cacheDir, "scum_run", "execution_job_journal.json")
}

// newExecutionEndpointID derives a stable endpoint ID summary for this scum_run process.
// cfg contains token and server address identity hints, and the function returns a redacted deterministic ID.
func newExecutionEndpointID(cfg *config.Config) string {
	hostname, _ := os.Hostname()
	seed := hostname
	if cfg != nil {
		seed = strings.Join([]string{hostname, cfg.ServerAddr, cfg.Token}, "|")
	}
	sum := sha256.Sum256([]byte(seed))
	return "scum-run-" + hex.EncodeToString(sum[:])[:16]
}

// stringFromJobPayload reads the first non-empty string value from a job payload.
// payload contains decoded JSON fields and keys are candidate names, and the function returns an empty string when no string value exists.
func stringFromJobPayload(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// boolFromJobPayload reads the first boolean value from a job payload.
// payload contains decoded JSON fields and keys are candidate names, and the function returns false when no boolean value exists.
func boolFromJobPayload(payload map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := payload[key].(bool); ok {
			return value
		}
	}
	return false
}

// firstJobValue returns the first present payload value for a set of keys.
// payload contains decoded JSON fields and keys are candidate names, and the function returns nil when none exists.
func firstJobValue(payload map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := payload[key]; ok {
			return value
		}
	}
	return nil
}

// firstLimitValue returns a limit from payload first and envelope limits second.
// envelope contains payload and limit maps, key names are candidate names, and the function returns nil when no value exists.
func firstLimitValue(envelope jobprotocol.Envelope, keys ...string) any {
	if value := firstJobValue(envelope.Payload, keys...); value != nil {
		return value
	}
	for _, key := range keys {
		if value, ok := envelope.Limits[key]; ok {
			return value
		}
	}
	return nil
}

// intFromJobValue converts JSON numeric values into an int.
// value is a decoded JSON scalar, and the function returns zero when conversion is not possible.
func intFromJobValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		return 0
	}
}

// firstNonEmptyJobString returns the first non-empty string from two candidates.
// first and second are candidate values, and the function returns the first non-empty value or an empty string.
func firstNonEmptyJobString(first string, second string) string {
	if strings.TrimSpace(first) != "" {
		return strings.TrimSpace(first)
	}
	return strings.TrimSpace(second)
}
