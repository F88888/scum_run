package localruntime

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"scum_run/internal/database"
)

const (
	// CapabilityDatabaseQuery 表示读写皆可的本地数据库能力键。
	CapabilityDatabaseQuery = "scum.db.query"
	// CapabilityDatabaseQueryReadOnly 表示只读数据库能力键。
	CapabilityDatabaseQueryReadOnly = "scum.db.query.readonly"
	// CapabilityProcessStart 表示本地进程启动能力键。
	CapabilityProcessStart = "process.start"
	// CapabilityProcessStop 表示本地进程停止能力键。
	CapabilityProcessStop = "process.stop"
	// CapabilityProcessRestart 表示本地进程重启能力键。
	CapabilityProcessRestart = "process.restart"
	// CapabilityProcessStatus 表示本地进程状态查询能力键。
	CapabilityProcessStatus = "process.status"
)

// CapabilityExecutionResult 描述一次共享本地能力执行结果。
type CapabilityExecutionResult struct {
	// Data 是返回给控制面的安全结构化结果数据。
	Data map[string]any
	// Summary 是给调用方展示的脱敏执行摘要。
	Summary string
}

// CapabilityExecutionError 描述一次共享本地能力执行失败的稳定错误。
type CapabilityExecutionError struct {
	// ReasonCode 是结构化稳定错误码。
	ReasonCode string
	// Summary 是给调用方展示的脱敏错误摘要。
	Summary string
	// Retryable 表示当前失败是否适合稍后重试。
	Retryable bool
	// Data 是附带的安全结构化上下文，例如当前状态快照。
	Data map[string]any
}

// Error returns the display-safe summary for one capability execution failure.
// It takes no parameters and returns the structured summary string for error wrapping and comparisons.
func (e *CapabilityExecutionError) Error() string {
	if e == nil {
		return ""
	}
	return firstNonEmpty(strings.TrimSpace(e.Summary), strings.TrimSpace(e.ReasonCode), "capability execution failed")
}

// SupportsCapability reports whether the shared runtime knows how to execute one capability key.
// capability is the requested local capability name, and the method returns true for supported database and process lifecycle operations.
func (r *LocalRuntime) SupportsCapability(capability string) bool {
	switch strings.TrimSpace(capability) {
	case CapabilityDatabaseQuery,
		CapabilityDatabaseQueryReadOnly,
		CapabilityProcessStart,
		CapabilityProcessStop,
		CapabilityProcessRestart,
		CapabilityProcessStatus:
		return true
	default:
		return false
	}
}

// ExecuteCapability runs one supported database or process capability through the shared local runtime.
// capability identifies the requested action and payload carries bounded execution parameters, and the method returns a structured result or a stable execution error when validation or local execution fails.
func (r *LocalRuntime) ExecuteCapability(capability string, payload map[string]any) (CapabilityExecutionResult, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	switch strings.TrimSpace(capability) {
	case CapabilityDatabaseQueryReadOnly:
		return r.executeDatabaseCapability(payload, true)
	case CapabilityDatabaseQuery:
		return r.executeDatabaseCapability(payload, boolFromPayload(payload, "readOnly", "read_only"))
	case CapabilityProcessStart:
		return r.executeProcessCapability(CapabilityProcessStart)
	case CapabilityProcessStop:
		return r.executeProcessCapability(CapabilityProcessStop)
	case CapabilityProcessRestart:
		return r.executeProcessCapability(CapabilityProcessRestart)
	case CapabilityProcessStatus:
		return r.processStatusCapabilityResult(), nil
	default:
		return CapabilityExecutionResult{}, &CapabilityExecutionError{
			ReasonCode: "capability.unsupported",
			Summary:    fmt.Sprintf("unsupported capability: %s", strings.TrimSpace(capability)),
			Retryable:  false,
		}
	}
}

// executeDatabaseCapability runs one bounded SCUM.db capability through the shared database client.
// payload contains SQL text, args and optional limits while readOnly controls mutation safety, and the method returns query output or a stable execution error.
func (r *LocalRuntime) executeDatabaseCapability(payload map[string]any, readOnly bool) (CapabilityExecutionResult, error) {
	if r == nil || r.db == nil {
		return CapabilityExecutionResult{}, &CapabilityExecutionError{
			ReasonCode: "database.unavailable",
			Summary:    "本地数据库 runtime 未初始化。",
			Retryable:  true,
		}
	}
	query := stringFromPayload(payload, "query", "sql")
	if strings.TrimSpace(query) == "" {
		return CapabilityExecutionResult{}, &CapabilityExecutionError{
			ReasonCode: "database.query_missing",
			Summary:    "缺少数据库查询语句。",
			Retryable:  false,
		}
	}
	options := database.QueryOptions{
		QueryID:  firstNonEmpty(stringFromPayload(payload, "queryId", "query_id"), stringFromPayload(payload, "jobId", "job_id")),
		Args:     databaseArgsFromPayload(payload["args"]),
		Timeout:  durationFromMilliseconds(firstPayloadValue(payload, "timeoutMs", "timeout_ms")),
		MaxRows:  intFromPayloadValue(firstPayloadValue(payload, "maxRows", "max_rows")),
		MaxBytes: intFromPayloadValue(firstPayloadValue(payload, "maxBytes", "max_bytes")),
	}
	var (
		result database.QueryResult
		err    error
	)
	if readOnly {
		result, err = r.db.ExecuteReadOnlyCapability(query, options)
	} else {
		result, err = r.db.ExecuteCapability(query, options)
	}
	if err != nil {
		return CapabilityExecutionResult{}, &CapabilityExecutionError{
			ReasonCode: "database.execution_failed",
			Summary:    err.Error(),
			Retryable:  true,
		}
	}
	return CapabilityExecutionResult{
		Data: map[string]any{
			"queryId":      result.QueryID,
			"action":       result.Action,
			"columns":      result.Columns,
			"rows":         result.Rows,
			"rowCount":     len(result.Rows),
			"rowsAffected": result.RowsAffected,
			"truncated":    result.Truncated,
			"truncatedBy":  result.TruncatedBy,
			"durationMs":   result.DurationMS,
		},
		Summary: "database query completed",
	}, nil
}

// executeProcessCapability runs one process lifecycle action through the shared process manager.
// capability identifies start, stop, or restart, and the method returns the updated process snapshot or a stable blocked error when local readiness is missing.
func (r *LocalRuntime) executeProcessCapability(capability string) (CapabilityExecutionResult, error) {
	readiness := r.ProcessReadiness()
	if !readiness.Ready {
		return CapabilityExecutionResult{}, &CapabilityExecutionError{
			ReasonCode: firstNonEmpty(readiness.ReasonCode, ProcessReasonPathUnresolved),
			Summary:    firstNonEmpty(readiness.Summary, "本地进程 runtime 尚未就绪。"),
			Retryable:  true,
			Data: map[string]any{
				"status":           readiness.Status,
				"processReady":     readiness.Ready,
				"processSupported": readiness.Supported,
				"reasonCode":       readiness.ReasonCode,
				"summary":          readiness.Summary,
			},
		}
	}
	if r == nil || r.process == nil {
		return CapabilityExecutionResult{}, &CapabilityExecutionError{
			ReasonCode: ProcessReasonPathUnresolved,
			Summary:    "本地进程 runtime 未初始化。",
			Retryable:  true,
		}
	}
	var err error
	summary := "process status returned"
	switch strings.TrimSpace(capability) {
	case CapabilityProcessStart:
		err = r.process.Start()
		summary = "process started"
	case CapabilityProcessStop:
		err = r.process.Stop()
		summary = "process stopped"
	case CapabilityProcessRestart:
		err = r.process.Restart()
		summary = "process restarted"
	default:
		return CapabilityExecutionResult{}, &CapabilityExecutionError{
			ReasonCode: "capability.unsupported",
			Summary:    fmt.Sprintf("unsupported process capability: %s", strings.TrimSpace(capability)),
			Retryable:  false,
		}
	}
	if err != nil {
		return CapabilityExecutionResult{}, &CapabilityExecutionError{
			ReasonCode: "process.execution_failed",
			Summary:    err.Error(),
			Retryable:  true,
			Data: map[string]any{
				"status": r.process.GetStatus(),
			},
		}
	}
	return CapabilityExecutionResult{
		Data: map[string]any{
			"status": r.process.GetStatus(),
		},
		Summary: summary,
	}, nil
}

// processStatusCapabilityResult returns the current process status plus readiness metadata.
// It takes no additional parameters and returns a structured status snapshot even when the local runtime is blocked or unsupported.
func (r *LocalRuntime) processStatusCapabilityResult() CapabilityExecutionResult {
	readiness := r.ProcessReadiness()
	return CapabilityExecutionResult{
		Data: map[string]any{
			"status":           readiness.Status,
			"processReady":     readiness.Ready,
			"processSupported": readiness.Supported,
			"reasonCode":       readiness.ReasonCode,
			"summary":          readiness.Summary,
		},
		Summary: firstNonEmpty(readiness.Summary, "process status returned"),
	}
}

// stringFromPayload returns the first non-empty string value from the payload for the provided keys.
// payload contains decoded JSON-like fields and keys lists candidate names, and the function returns an empty string when no string value exists.
func stringFromPayload(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// boolFromPayload returns the first boolean value from the payload for the provided keys.
// payload contains decoded JSON-like fields and keys lists candidate names, and the function returns false when no boolean value exists.
func boolFromPayload(payload map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := payload[key].(bool); ok {
			return value
		}
	}
	return false
}

// firstPayloadValue returns the first present value for the provided payload keys.
// payload contains decoded JSON-like fields and keys lists candidate names, and the function returns nil when none of the keys are present.
func firstPayloadValue(payload map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := payload[key]; ok {
			return value
		}
	}
	return nil
}

// intFromPayloadValue converts one decoded JSON scalar into an int.
// value is a decoded JSON-like scalar, and the function returns zero when conversion is not possible.
func intFromPayloadValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		return 0
	}
}

// durationFromMilliseconds converts a decoded millisecond value into a bounded duration.
// value is a decoded JSON-like scalar, and the function returns zero when no valid timeout is provided.
func durationFromMilliseconds(value any) time.Duration {
	milliseconds := intFromPayloadValue(value)
	if milliseconds <= 0 {
		return 0
	}
	return time.Duration(milliseconds) * time.Millisecond
}

// databaseArgsFromPayload normalizes one decoded JSON args field into a SQLite argument slice.
// raw is the decoded args payload, and the function returns a positional argument list suitable for database capability execution.
func databaseArgsFromPayload(raw any) []interface{} {
	switch typed := raw.(type) {
	case []any:
		return typed
	default:
		return nil
	}
}
