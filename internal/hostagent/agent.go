package hostagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"scum_run/internal/database"
	"scum_run/internal/logger"
	"scum_run/internal/steam"
)

const (
	hostAgentDatabaseCapability       = "scum.db.query.readonly"
	hostAgentExecutorUpdateCapability = "executor.update.push"
	hostAgentSelfUpdateCapability     = "executor.self_update"
	hostAgentCapabilityStatus         = "active"
	hostAgentCapabilityVersion        = "1"
	hostAgentCapabilityRisk           = "medium"
	hostAgentCapabilityDir            = "request"
)

// Agent 表示 scum_run 在新控制面下的 host-agent 执行端。
type Agent struct {
	// cfg 是 host-agent 运行配置。
	cfg Config
	// logger 是结构化文本日志输出器。
	logger *logger.Logger
	// client 是访问 scum_server 控制面的 HTTP 客户端。
	client *http.Client
	// databasePath 是解析后的本地 SCUM.db 路径。
	databasePath string
	// db 是本地 SQLite 访问客户端。
	db *database.Client
	// sessionMu 保护当前 session token 的读写。
	sessionMu sync.RWMutex
	// sessionToken 是 hello 成功后返回的 host agent 会话令牌。
	sessionToken string
}

// helloRequest 表示 host agent 启动时向 scum_server 提交的注册请求。
type helloRequest struct {
	// RegistrationToken 是创建 host agent 会话前使用的注册令牌。
	RegistrationToken string `json:"registrationToken"`
	// AgentID 是 host agent 的稳定唯一标识。
	AgentID string `json:"agentId"`
	// DisplayName 是 host agent 的展示名称。
	DisplayName string `json:"displayName"`
	// Version 是 host agent 的版本号。
	Version string `json:"version"`
	// Address 是 host agent 的地址或节点标识。
	Address string `json:"address"`
	// Capabilities 是 host agent 当前声明的能力列表。
	Capabilities []hostAgentCapability `json:"capabilities"`
}

// helloResponse 表示 scum_server 对 host agent hello 的响应。
type helloResponse struct {
	// SessionToken 是后续 heartbeat 和轮询接口使用的会话令牌。
	SessionToken string `json:"sessionToken"`
}

// heartbeatRequest 表示 host agent 对 scum_server 的心跳请求。
type heartbeatRequest struct {
	// AgentID 是用于一致性校验的 host agent ID。
	AgentID string `json:"agentId"`
	// Version 是当前 host agent 版本号。
	Version string `json:"version"`
}

// hostAgentCapability 表示 hello 时上报给 scum_server 的能力声明。
type hostAgentCapability struct {
	// Capability 是能力键，例如 scum.db.query.readonly。
	Capability string `json:"capability"`
	// Version 是能力实现版本号。
	Version string `json:"version"`
	// Direction 是能力调用方向。
	Direction string `json:"direction"`
	// RiskLevel 是能力风险级别。
	RiskLevel string `json:"riskLevel"`
	// Status 是能力当前状态。
	Status string `json:"status"`
	// Metadata 是能力的脱敏补充元数据。
	Metadata map[string]any `json:"metadata,omitempty"`
}

// databaseOperation 表示从 scum_server 拉取的一条待执行数据库操作。
type databaseOperation struct {
	// ID 是数据库操作记录 ID。
	ID string `json:"id"`
	// QueryID 是执行端需要回传的查询 ID。
	QueryID string `json:"queryId"`
	// SQLText 是只供执行边界使用的 SQL 文本。
	SQLText string `json:"sqlText"`
	// Args 是 SQL 位置参数列表。
	Args []any `json:"args"`
	// ReadOnly 表示该操作必须以只读模式执行。
	ReadOnly bool `json:"readOnly"`
	// TimeoutMS 是本次执行允许的超时时间毫秒数。
	TimeoutMS int `json:"timeoutMs"`
	// MaxRows 是本次读取允许返回的最大行数。
	MaxRows int `json:"maxRows"`
	// MaxBytes 是本次读取允许返回的最大字节数。
	MaxBytes int `json:"maxBytes"`
	// SQLSummary 是脱敏 SQL 摘要。
	SQLSummary string `json:"sqlSummary"`
}

// databaseOperationResultRequest 表示 host agent 向 scum_server 回报数据库操作结果的请求体。
type databaseOperationResultRequest struct {
	// Status 是数据库操作最终状态。
	Status string `json:"status"`
	// QueryID 是执行端回传的查询 ID。
	QueryID string `json:"queryId,omitempty"`
	// Action 是 SQL 动作分类，例如 read。
	Action string `json:"action"`
	// Columns 是读取类 SQL 返回的列名列表。
	Columns []string `json:"columns,omitempty"`
	// Rows 是读取类 SQL 返回的结果行。
	Rows []map[string]any `json:"rows,omitempty"`
	// RowCount 是本次返回的结果行数。
	RowCount int `json:"rowCount"`
	// Truncated 表示结果是否被限制截断。
	Truncated bool `json:"truncated"`
	// TruncatedBy 是触发截断的限制类型。
	TruncatedBy string `json:"truncatedBy,omitempty"`
	// DurationMS 是本次数据库执行耗时毫秒数。
	DurationMS int64 `json:"durationMs"`
	// ErrorCode 是结构化错误码。
	ErrorCode string `json:"errorCode,omitempty"`
	// ErrorMessage 是脱敏错误摘要。
	ErrorMessage string `json:"errorMessage,omitempty"`
	// Summary 是用于审计的脱敏 SQL 摘要。
	Summary string `json:"summary,omitempty"`
}

// errorResponse 表示 scum_server 返回的标准错误体。
type errorResponse struct {
	// Error 是返回给调用方的错误摘要。
	Error string `json:"error"`
}

// New builds one host-agent runtime from environment-derived configuration.
// cfg contains server URL, credentials and database path hints, logger writes progress output, and the function returns a configured Agent or an error when the database path cannot be resolved.
func New(cfg Config, logger *logger.Logger) (*Agent, error) {
	databasePath, err := cfg.ResolveDatabasePath()
	if err != nil {
		steamDir := steam.NewDetector(logger).DetectSteamDirectory()
		if strings.TrimSpace(steamDir) == "" {
			return nil, err
		}
		databasePath = databasePathFromSteamDir(steamDir)
	}
	return &Agent{
		cfg:          cfg,
		logger:       logger,
		client:       &http.Client{Timeout: cfg.RequestTimeout},
		databasePath: databasePath,
		db:           database.New(databasePath, logger),
	}, nil
}

// Run starts host-agent registration, heartbeat, and database polling loops.
// ctx controls the runtime lifetime, and the method returns nil on clean shutdown or an error when startup registration fails.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.register(ctx); err != nil {
		return err
	}
	a.logger.Info("Host agent registered: id=%s database=%s", a.cfg.AgentID, a.redactPath(a.databasePath))
	heartbeatErrCh := make(chan error, 1)
	go a.heartbeatLoop(ctx, heartbeatErrCh)
	if a.db.IsAvailable() {
		if err := a.db.Initialize(); err != nil {
			a.logger.Warn("Initial database initialization failed: %s", database.SanitizeError(err))
		}
	}
	pollErr := a.pollLoop(ctx)
	select {
	case err := <-heartbeatErrCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	default:
	}
	return pollErr
}

// heartbeatLoop sends periodic heartbeat messages and re-registers when the session expires.
// ctx controls cancellation, errCh receives terminal background errors, and the method returns no values.
func (a *Agent) heartbeatLoop(ctx context.Context, errCh chan<- error) {
	ticker := time.NewTicker(a.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			errCh <- ctx.Err()
			return
		case <-ticker.C:
			if err := a.sendHeartbeat(ctx); err != nil {
				if a.isUnauthorized(err) {
					a.logger.Warn("Host agent session expired during heartbeat, re-registering")
					if err := a.register(ctx); err != nil {
						errCh <- err
						return
					}
					continue
				}
				a.logger.Warn("Host agent heartbeat failed: %s", err.Error())
			}
		}
	}
}

// pollLoop continuously claims database operations and reports execution results.
// ctx controls cancellation, and the method returns nil on shutdown or an error when re-registration fails.
func (a *Agent) pollLoop(ctx context.Context) error {
	ticker := time.NewTicker(a.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		operation, found, err := a.nextDatabaseOperation(ctx)
		if err != nil {
			if a.isUnauthorized(err) {
				a.logger.Warn("Host agent session expired during polling, re-registering")
				if err := a.register(ctx); err != nil {
					return err
				}
				continue
			}
			a.logger.Warn("Database polling failed: %s", err.Error())
			continue
		}
		if !found {
			continue
		}
		if err := a.handleDatabaseOperation(ctx, operation); err != nil {
			a.logger.Warn("Database operation %s handling failed: %s", operation.ID, err.Error())
		}
	}
}

// register performs the host-agent hello flow and stores the returned session token.
// ctx controls the HTTP request lifetime, and the method returns nil on success or an error when registration fails.
func (a *Agent) register(ctx context.Context) error {
	request := helloRequest{
		RegistrationToken: a.cfg.RegistrationToken,
		AgentID:           a.cfg.AgentID,
		DisplayName:       a.cfg.DisplayName,
		Version:           a.cfg.Version,
		Address:           a.cfg.Address,
		Capabilities: []hostAgentCapability{
			{
				Capability: hostAgentDatabaseCapability,
				Version:    hostAgentCapabilityVersion,
				Direction:  hostAgentCapabilityDir,
				RiskLevel:  hostAgentCapabilityRisk,
				Status:     hostAgentCapabilityStatus,
				Metadata: map[string]any{
					"databasePath": a.redactPath(a.databasePath),
					"databaseName": filepath.Base(a.databasePath),
				},
			},
			{
				Capability: hostAgentExecutorUpdateCapability,
				Version:    hostAgentCapabilityVersion,
				Direction:  hostAgentCapabilityDir,
				RiskLevel:  "high",
				Status:     hostAgentCapabilityStatus,
				Metadata: map[string]any{
					"supportsPlatformPush": true,
					"supportsRollbackHint": true,
				},
			},
			{
				Capability: hostAgentSelfUpdateCapability,
				Version:    hostAgentCapabilityVersion,
				Direction:  hostAgentCapabilityDir,
				RiskLevel:  "high",
				Status:     hostAgentCapabilityStatus,
				Metadata: map[string]any{
					"supportsManualRecovery": true,
				},
			},
		},
	}
	var response helloResponse
	if err := a.requestJSON(ctx, http.MethodPost, "/api/v1/host-agents/hello", "", request, &response); err != nil {
		return fmt.Errorf("register host agent: %w", err)
	}
	if strings.TrimSpace(response.SessionToken) == "" {
		return errors.New("register host agent: empty session token")
	}
	a.setSessionToken(response.SessionToken)
	return nil
}

// sendHeartbeat refreshes host-agent liveness with the current session token.
// ctx controls the HTTP request lifetime, and the method returns nil when the heartbeat succeeds or an error when it fails.
func (a *Agent) sendHeartbeat(ctx context.Context) error {
	return a.requestJSON(ctx, http.MethodPost, "/api/v1/host-agents/heartbeat", a.session(), heartbeatRequest{
		AgentID: a.cfg.AgentID,
		Version: a.cfg.Version,
	}, nil)
}

// nextDatabaseOperation claims the next pending database operation for this host agent.
// ctx controls the HTTP request lifetime, and the method returns the reserved operation, whether one existed, or an error.
func (a *Agent) nextDatabaseOperation(ctx context.Context) (databaseOperation, bool, error) {
	var operation databaseOperation
	err := a.requestJSON(ctx, http.MethodGet, "/api/v1/host-agents/database-operations/next", a.session(), nil, &operation)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return databaseOperation{}, false, nil
		}
		return databaseOperation{}, false, err
	}
	return operation, true, nil
}

// handleDatabaseOperation executes one claimed database operation and reports the shaped result back to scum_server.
// ctx controls the report request lifetime, operation is the reserved database task, and the method returns nil when report succeeds or an error when local execution/reporting fails.
func (a *Agent) handleDatabaseOperation(ctx context.Context, operation databaseOperation) error {
	result := a.executeDatabaseOperation(operation)
	return a.requestJSON(
		ctx,
		http.MethodPost,
		fmt.Sprintf("/api/v1/host-agents/database-operations/%s/result", url.PathEscape(operation.ID)),
		a.session(),
		result,
		nil,
	)
}

// executeDatabaseOperation runs one read-only SQL request against the local SCUM database.
// operation contains SQL, args and result limits, and the method returns a shaped success or failure payload for scum_server.
func (a *Agent) executeDatabaseOperation(operation databaseOperation) databaseOperationResultRequest {
	if !operation.ReadOnly {
		return databaseOperationResultRequest{
			Status:       "failed",
			QueryID:      operation.QueryID,
			Action:       database.SQLActionUnsafe,
			ErrorCode:    "readonly_required",
			ErrorMessage: "host agent only accepts read-only database operations",
			Summary:      operation.SQLSummary,
		}
	}
	if a.db.IsAvailable() {
		if err := a.db.Initialize(); err != nil {
			a.logger.Warn("Database initialization before query failed: %s", database.SanitizeError(err))
		}
	}
	result, err := a.db.ExecuteReadOnlyCapability(operation.SQLText, database.QueryOptions{
		QueryID:  operation.QueryID,
		Args:     operation.Args,
		Timeout:  time.Duration(operation.TimeoutMS) * time.Millisecond,
		MaxRows:  operation.MaxRows,
		MaxBytes: operation.MaxBytes,
	})
	if err != nil {
		return databaseOperationResultRequest{
			Status:       "failed",
			QueryID:      operation.QueryID,
			Action:       database.SQLActionRead,
			ErrorCode:    databaseErrorCode(err),
			ErrorMessage: database.SanitizeError(err),
			Summary:      operation.SQLSummary,
		}
	}
	return databaseOperationResultRequest{
		Status:      "succeeded",
		QueryID:     result.QueryID,
		Action:      result.Action,
		Columns:     result.Columns,
		Rows:        result.Rows,
		RowCount:    len(result.Rows),
		Truncated:   result.Truncated,
		TruncatedBy: result.TruncatedBy,
		DurationMS:  result.DurationMS,
		Summary:     operation.SQLSummary,
	}
}

// requestJSON performs one authenticated JSON HTTP request against scum_server.
// ctx controls request lifetime, method/path identify the endpoint, sessionToken is optional bearer auth, body is marshaled when non-nil, into receives the decoded success response, and the method returns a shaped transport or server error.
func (a *Agent) requestJSON(ctx context.Context, method string, path string, sessionToken string, body any, into any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, a.cfg.ServerURL+path, payload)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(sessionToken) != "" {
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(sessionToken))
	}
	response, err := a.client.Do(request)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNoContent {
		return nil
	}
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if response.StatusCode >= http.StatusBadRequest {
		serverErr := decodeErrorSummary(responseBody)
		switch response.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %s", errNotFound, serverErr)
		case http.StatusUnauthorized:
			return fmt.Errorf("%w: %s", errUnauthorized, serverErr)
		default:
			return fmt.Errorf("request failed with status %d: %s", response.StatusCode, serverErr)
		}
	}
	if into != nil && len(responseBody) > 0 {
		if err := json.Unmarshal(responseBody, into); err != nil {
			return fmt.Errorf("decode response body: %w", err)
		}
	}
	return nil
}

// session returns the current bearer token used for authenticated host-agent requests.
// It takes no parameters and returns the latest stored session token.
func (a *Agent) session() string {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	return a.sessionToken
}

// setSessionToken replaces the current bearer token after a successful hello call.
// sessionToken is the new token value, and the method returns no values.
func (a *Agent) setSessionToken(sessionToken string) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	a.sessionToken = strings.TrimSpace(sessionToken)
}

// redactPath converts an absolute database path into a safe basename-style summary.
// path is a potentially sensitive filesystem path, and the method returns a bounded redacted representation.
func (a *Agent) redactPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	return filepath.Base(trimmed)
}

// isUnauthorized reports whether an error came from an expired or invalid host-agent session.
// err is the request error returned by requestJSON, and the method returns true when re-registration should be attempted.
func (a *Agent) isUnauthorized(err error) bool {
	return errors.Is(err, errUnauthorized)
}

var (
	errNotFound     = errors.New("resource not found")
	errUnauthorized = errors.New("unauthorized")
)

// decodeErrorSummary extracts a safe text summary from a JSON or plain-text error response.
// body is the raw HTTP response body, and the function returns the parsed error string or a bounded fallback summary.
func decodeErrorSummary(body []byte) string {
	var response errorResponse
	if err := json.Unmarshal(body, &response); err == nil && strings.TrimSpace(response.Error) != "" {
		return strings.TrimSpace(response.Error)
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "request failed"
	}
	return text
}

// databaseErrorCode classifies a local database execution failure into a stable structured code.
// err is the local database execution error, and the function returns a safe machine-readable error code.
func databaseErrorCode(err error) string {
	message := strings.ToLower(database.SanitizeError(err))
	switch {
	case strings.Contains(message, "multiple sql statements"):
		return "multiple_statements"
	case strings.Contains(message, "read-only database capability rejected"):
		return "readonly_rejected"
	case strings.Contains(message, "unsupported sql statement type"):
		return "unsupported_statement"
	case strings.Contains(message, "database is locked"):
		return "database_locked"
	case strings.Contains(message, "no such file"), strings.Contains(message, "does not exist"):
		return "database_unavailable"
	case strings.Contains(message, "deadline exceeded"), strings.Contains(message, "timeout"):
		return "timeout"
	default:
		return "execution_failed"
	}
}
