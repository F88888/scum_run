package jobprotocol

import "time"

const (
	ProtocolVersion = "execution-agent-job-protocol.v1"

	AckAccepted           = "accepted"
	AckDuplicate          = "duplicate"
	AckQueued             = "queued"
	AckRejected           = "rejected"
	AckBusy               = "busy"
	AckUnsupported        = "unsupported"
	AckGenerationMismatch = "generation_mismatch"
	AckInvalid            = "invalid"

	JobStateAccepted  = "accepted"
	JobStateRunning   = "running"
	JobStateSucceeded = "succeeded"
	JobStateFailed    = "failed"
	JobStateCancelled = "cancelled"

	CancelAccepted        = "accepted"
	CancelUnsupported     = "unsupported"
	CancelAlreadyFinished = "already_finished"
	CancelNotFound        = "not_found"
	CancelTimeout         = "timeout"
	CancelFailed          = "failed"

	ReconcileRunning            = "running"
	ReconcileTerminal           = "terminal"
	ReconcileUnknown            = "unknown"
	ReconcileExpired            = "expired"
	ReconcileGenerationMismatch = "generation_mismatch"
	ReconcileUnsupported        = "unsupported"
)

// Envelope is the execution job request accepted from the authenticated control channel.
// JSON fields carry job identity, endpoint generation, limits and payload, and the value is returned with validation errors when required identity is missing.
type Envelope struct {
	// JobID 是 execution job 的唯一标识，由 scum_server 生成。
	JobID string `json:"jobId"`
	// CapabilityKey 是本次 job 调用的能力键，例如 scum.db.query.readonly。
	CapabilityKey string `json:"capabilityKey"`
	// ActionKey 是能力内的动作键，例如 query、start 或 restart。
	ActionKey string `json:"actionKey,omitempty"`
	// OperationID 是关联的控制面 operation ID；非 operation 调用可为空。
	OperationID string `json:"operationId,omitempty"`
	// StepKey 是关联 operation step 的稳定键；非 step 调用可为空。
	StepKey string `json:"stepKey,omitempty"`
	// IdempotencyKey 是重复请求检测使用的幂等键。
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// EndpointID 是目标执行端 ID，用于防止 job 被错误路由。
	EndpointID string `json:"endpointId,omitempty"`
	// EndpointGeneration 是目标执行端代次，用于识别重启或重连后的陈旧 job。
	EndpointGeneration uint64 `json:"endpointGeneration,omitempty"`
	// RequestID 是外部请求 ID，用于控制面和执行端日志关联。
	RequestID string `json:"requestId,omitempty"`
	// TraceID 是链路追踪 ID，用于诊断视图串联事件。
	TraceID string `json:"traceId,omitempty"`
	// TimeoutMS 是执行端允许本 job 运行的超时时间毫秒数。
	TimeoutMS int64 `json:"timeoutMs,omitempty"`
	// Limits 是结果行数、字节数等有界执行限制。
	Limits map[string]any `json:"limits,omitempty"`
	// CancellationPolicy 是取消策略摘要，例如 unsupported 或 best_effort。
	CancellationPolicy string `json:"cancellationPolicy,omitempty"`
	// PurposeSummary 是脱敏后的操作目的摘要，用于审计和 journal。
	PurposeSummary string `json:"purposeSummary,omitempty"`
	// Payload 是能力专属输入，只在本地执行边界内使用。
	Payload map[string]any `json:"payload,omitempty"`
}

// Ack is the structured acceptance response for one execution job.
// It includes the endpoint generation and ack state, and callers use State to decide whether execution may continue.
type Ack struct {
	// JobID 是被确认的 execution job ID。
	JobID string `json:"jobId"`
	// EndpointID 是返回 ack 的执行端 ID。
	EndpointID string `json:"endpointId"`
	// EndpointGeneration 是返回 ack 时的执行端代次。
	EndpointGeneration uint64 `json:"endpointGeneration"`
	// State 是 ack 状态，例如 accepted、duplicate、busy 或 generation_mismatch。
	State string `json:"state"`
	// AcceptedAt 是 job 被接受的时间；未接受时为空。
	AcceptedAt *time.Time `json:"acceptedAt,omitempty"`
	// RetryAfterMS 是繁忙或队列满时建议重试的毫秒数。
	RetryAfterMS int64 `json:"retryAfterMs,omitempty"`
	// Reason 是脱敏原因摘要，用于展示和审计。
	Reason string `json:"reason,omitempty"`
	// CurrentState 是 duplicate 场景下 journal 已知的 job 状态。
	CurrentState string `json:"currentState,omitempty"`
	// Result 是 duplicate 且已终态时可重放的脱敏结果。
	Result *Result `json:"result,omitempty"`
}

// Progress is a bounded progress report for an accepted execution job.
// It carries phase, percentage and redacted details, and callers can order reports by Sequence.
type Progress struct {
	// JobID 是 progress 所属的 execution job ID。
	JobID string `json:"jobId"`
	// EndpointID 是发送 progress 的执行端 ID。
	EndpointID string `json:"endpointId"`
	// EndpointGeneration 是发送 progress 时的执行端代次。
	EndpointGeneration uint64 `json:"endpointGeneration"`
	// Phase 是当前阶段，例如 accepted、running、query 或 finished。
	Phase string `json:"phase,omitempty"`
	// Percent 是当前进度百分比，未知时为 0。
	Percent uint8 `json:"percent,omitempty"`
	// DetailSummary 是脱敏且有界的进度摘要。
	DetailSummary string `json:"detailSummary,omitempty"`
	// Sequence 是同一 job 内单调递增的 progress 序号。
	Sequence uint64 `json:"sequence"`
	// Timestamp 是 progress 产生时间。
	Timestamp time.Time `json:"timestamp"`
}

// Result is the terminal execution outcome persisted in the local journal.
// It carries bounded data and sanitized errors, and callers use Status and ResultCode for state transitions.
type Result struct {
	// JobID 是 terminal result 所属的 execution job ID。
	JobID string `json:"jobId"`
	// EndpointID 是发送 result 的执行端 ID。
	EndpointID string `json:"endpointId"`
	// EndpointGeneration 是发送 result 时的执行端代次。
	EndpointGeneration uint64 `json:"endpointGeneration"`
	// Status 是终态状态，例如 succeeded、failed 或 cancelled。
	Status string `json:"status"`
	// ResultCode 是稳定结果码，例如 ok、unsupported_capability 或 execution_failed。
	ResultCode string `json:"resultCode"`
	// DurationMS 是本次 job 执行耗时毫秒数。
	DurationMS int64 `json:"durationMs"`
	// Retryable 表示失败是否允许控制面重试。
	Retryable bool `json:"retryable"`
	// Truncated 表示结果数据是否被执行端限制截断。
	Truncated bool `json:"truncated"`
	// TruncatedBy 是触发截断的限制类型，例如 rows 或 bytes。
	TruncatedBy string `json:"truncatedBy,omitempty"`
	// Summary 是脱敏后的成功结果摘要。
	Summary string `json:"summary,omitempty"`
	// ErrorSummary 是脱敏后的失败摘要。
	ErrorSummary string `json:"errorSummary,omitempty"`
	// Data 是有界且脱敏的能力结果数据。
	Data map[string]any `json:"data,omitempty"`
	// FinishedAt 是终态结果产生时间。
	FinishedAt time.Time `json:"finishedAt"`
}

// CancelRequest asks the endpoint to best-effort cancel one job.
// It identifies the job by job ID or idempotency key, and the response reports a structured cancellation outcome.
type CancelRequest struct {
	// JobID 是要取消的 execution job ID。
	JobID string `json:"jobId,omitempty"`
	// IdempotencyKey 是要取消的 job 幂等键；JobID 为空时可用于查找。
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// Reason 是调用方给出的脱敏取消原因摘要。
	Reason string `json:"reason,omitempty"`
}

// CancelOutcome is the structured result of one cancellation request.
// It reports whether cancellation was accepted, unsupported, already finished or unavailable, and callers must not infer stronger guarantees.
type CancelOutcome struct {
	// JobID 是取消请求匹配到的 execution job ID。
	JobID string `json:"jobId,omitempty"`
	// EndpointID 是返回取消结果的执行端 ID。
	EndpointID string `json:"endpointId"`
	// EndpointGeneration 是返回取消结果时的执行端代次。
	EndpointGeneration uint64 `json:"endpointGeneration"`
	// Outcome 是取消结果，例如 unsupported、already_finished 或 not_found。
	Outcome string `json:"outcome"`
	// Reason 是脱敏结果摘要。
	Reason string `json:"reason,omitempty"`
	// Timestamp 是取消结果产生时间。
	Timestamp time.Time `json:"timestamp"`
}

// ReconcileRequest asks the endpoint to report known state for one previous job.
// It can use either job ID or idempotency key, and endpoint generation is checked when provided.
type ReconcileRequest struct {
	// JobID 是要对账的 execution job ID。
	JobID string `json:"jobId,omitempty"`
	// IdempotencyKey 是要对账的 job 幂等键；JobID 为空时可用于查找。
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// EndpointGeneration 是控制面认为 job 所属的执行端代次。
	EndpointGeneration uint64 `json:"endpointGeneration,omitempty"`
}

// ReconcileResponse is the bounded status returned for a previous job.
// It distinguishes running, terminal, expired, unknown and generation mismatch states, and may include a replayed terminal result.
type ReconcileResponse struct {
	// JobID 是对账命中的 execution job ID。
	JobID string `json:"jobId,omitempty"`
	// EndpointID 是返回对账结果的执行端 ID。
	EndpointID string `json:"endpointId"`
	// EndpointGeneration 是返回对账结果时的执行端代次。
	EndpointGeneration uint64 `json:"endpointGeneration"`
	// State 是对账状态，例如 running、terminal、expired 或 unknown。
	State string `json:"state"`
	// CurrentJobState 是 journal 中记录的 job 状态。
	CurrentJobState string `json:"currentJobState,omitempty"`
	// Result 是 terminal 状态下重放的脱敏终态结果。
	Result *Result `json:"result,omitempty"`
	// Reason 是脱敏对账摘要。
	Reason string `json:"reason,omitempty"`
	// Timestamp 是对账响应产生时间。
	Timestamp time.Time `json:"timestamp"`
}

// Readiness describes endpoint support for the execution job protocol.
// It exposes protocol version, generation, capacity and journal health, and callers use it for serviceability decisions.
type Readiness struct {
	// ProtocolVersion 是当前执行端支持的 job protocol 版本。
	ProtocolVersion string `json:"protocolVersion"`
	// EndpointID 是当前执行端 ID。
	EndpointID string `json:"endpointId"`
	// EndpointGeneration 是当前执行端代次。
	EndpointGeneration uint64 `json:"endpointGeneration"`
	// SupportedCapabilities 是当前可通过 job 协议执行的能力键。
	SupportedCapabilities []string `json:"supportedCapabilities"`
	// QueueCapacity 是本地 job 并发或队列容量摘要。
	QueueCapacity int `json:"queueCapacity"`
	// ActiveJobs 是当前 journal 中仍处于非终态的 job 数量。
	ActiveJobs int `json:"activeJobs"`
	// JournalAvailable 表示本地 journal 是否可用。
	JournalAvailable bool `json:"journalAvailable"`
	// CancellationSupported 表示执行端是否支持结构化取消结果。
	CancellationSupported bool `json:"cancellationSupported"`
	// ReconciliationSupported 表示执行端是否支持 job 状态对账。
	ReconciliationSupported bool `json:"reconciliationSupported"`
	// RecentErrors 是最近脱敏错误摘要列表。
	RecentErrors []string `json:"recentErrors,omitempty"`
}

// Record is one persisted local journal entry for an accepted job.
// It stores only redacted metadata, progress checkpoints and terminal summaries, and callers must not use it for raw payload replay.
type Record struct {
	// JobID 是 journal 记录对应的 execution job ID。
	JobID string `json:"jobId"`
	// IdempotencyKey 是 journal 记录对应的幂等键。
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// EndpointGeneration 是接受 job 时的执行端代次。
	EndpointGeneration uint64 `json:"endpointGeneration"`
	// CapabilityKey 是 job 的能力键。
	CapabilityKey string `json:"capabilityKey"`
	// ActionKey 是 job 的动作键。
	ActionKey string `json:"actionKey,omitempty"`
	// PayloadSummary 是脱敏后的 payload 摘要。
	PayloadSummary map[string]any `json:"payloadSummary,omitempty"`
	// PurposeSummary 是脱敏后的操作目的摘要。
	PurposeSummary string `json:"purposeSummary,omitempty"`
	// AckState 是最近一次 ack 状态。
	AckState string `json:"ackState"`
	// JobState 是 journal 当前已知 job 状态。
	JobState string `json:"jobState"`
	// Progress 是最近一次 progress checkpoint。
	Progress *Progress `json:"progress,omitempty"`
	// Result 是终态结果摘要。
	Result *Result `json:"result,omitempty"`
	// Cancellation 是最近一次取消结果。
	Cancellation *CancelOutcome `json:"cancellation,omitempty"`
	// CreatedAt 是 journal 记录创建时间。
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt 是 journal 记录最后更新时间。
	UpdatedAt time.Time `json:"updatedAt"`
}
