package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"scum_run/internal/logger"
)

const (
	DefaultQueryTimeout = 10 * time.Second
	DefaultMaxRows      = 500
	DefaultMaxBytes     = 1 << 20
)

const (
	SQLActionRead        = "read"
	SQLActionWrite       = "write"
	SQLActionSchema      = "schema"
	SQLActionTransaction = "transaction"
	SQLActionPragma      = "pragma"
	SQLActionUnsafe      = "unsafe"
)

// Client represents a SQLite database client
type Client struct {
	dbPath string
	logger *logger.Logger
	db     *sql.DB // 添加数据库连接字段
}

// QueryOptions 数据库能力执行选项。
type QueryOptions struct {
	// QueryID 是调用方传入的查询标识，用于把请求和响应对应起来。
	QueryID string
	// Args 是 SQL 参数列表，用于绑定问号占位符。
	Args []interface{}
	// Timeout 是本次 SQL 执行超时时间，空值使用默认值。
	Timeout time.Duration
	// MaxRows 是读取类 SQL 最多返回的行数，0 使用默认值。
	MaxRows int
	// MaxBytes 是读取类 SQL 最多返回的 JSON 序列化字节数，0 使用默认值。
	MaxBytes int
}

// QueryResult 数据库能力执行结果。
type QueryResult struct {
	// QueryID 是调用方传入的查询标识。
	QueryID string `json:"query_id,omitempty"`
	// Action 是 SQL 动作分类，例如 read、write、schema 或 transaction。
	Action string `json:"action"`
	// Columns 是读取类 SQL 返回的列名列表。
	Columns []string `json:"columns,omitempty"`
	// Rows 是读取类 SQL 返回的结果行。
	Rows []map[string]interface{} `json:"rows,omitempty"`
	// RowsAffected 是写入类 SQL 影响的行数。
	RowsAffected int64 `json:"rows_affected,omitempty"`
	// Truncated 表示读取结果是否被限制截断。
	Truncated bool `json:"truncated"`
	// TruncatedBy 是触发截断的限制类型，例如 rows 或 bytes。
	TruncatedBy string `json:"truncated_by,omitempty"`
	// DurationMS 是 SQL 执行耗时毫秒数。
	DurationMS int64 `json:"duration_ms"`
}

// New creates a new database client
func New(dbPath string, logger *logger.Logger) *Client {
	return &Client{
		dbPath: dbPath,
		logger: logger,
	}
}

// IsAvailable checks if the database file exists and is accessible
func (c *Client) IsAvailable() bool {
	if _, err := os.Stat(c.dbPath); os.IsNotExist(err) {
		return false
	}
	return true
}

// Initialize initializes the database connection and sets WAL mode
func (c *Client) Initialize() error {
	// If already initialized, just check the connection
	if c.db != nil {
		if err := c.db.Ping(); err == nil {
			c.logger.Debug("Database connection already active")
			return nil
		}
		// Connection failed, close and reinitialize
		c.logger.Warn("Existing database connection failed, reinitializing...")
		c.db.Close()
		c.db = nil
	}

	c.logger.Info("Initializing database connection: %s", c.dbPath)

	// Check if database file exists, if not, provide helpful warning message
	if _, err := os.Stat(c.dbPath); os.IsNotExist(err) {
		c.logger.Warn("Database file does not exist: %s. This usually means SCUM server has not been started yet or the server is not installed", c.dbPath)
		return fmt.Errorf("database file does not exist: %s. This usually means SCUM server has not been started yet or the server is not installed", c.dbPath)
	}

	// Open database connection
	db, err := sql.Open("sqlite3", c.dbPath)
	if err != nil {
		// Provide more specific error messages for common issues
		if strings.Contains(err.Error(), "CGO_ENABLED=0") {
			return fmt.Errorf("SQLite driver requires CGO to be enabled. Please rebuild with CGO_ENABLED=1: %w", err)
		}
		return fmt.Errorf("failed to open database: %w", err)
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		db.Close()
		// Provide more specific error messages
		if strings.Contains(err.Error(), "database is locked") {
			return fmt.Errorf("database is locked, another process may be using it: %w", err)
		}
		return fmt.Errorf("failed to ping database: %w", err)
	}

	// Set WAL mode
	c.logger.Info("Setting database journal mode to WAL")
	_, err = db.Exec("PRAGMA journal_mode=WAL;")
	if err != nil {
		db.Close()
		return fmt.Errorf("failed to set WAL mode: %w", err)
	}

	// Verify WAL mode is set
	var journalMode string
	err = db.QueryRow("PRAGMA journal_mode;").Scan(&journalMode)
	if err != nil {
		db.Close()
		return fmt.Errorf("failed to verify journal mode: %w", err)
	}

	c.logger.Info("Database journal mode set to: %s", journalMode)

	c.db = db
	return nil
}

// Close closes the database connection
func (c *Client) Close() error {
	if c.db != nil {
		c.logger.Info("Closing database connection")
		err := c.db.Close()
		c.db = nil
		return err
	}
	return nil
}

// Query executes a SQL query and returns all rows.
// query is the SQL text, args are optional positional parameters for ? placeholders, and the method returns result rows or an error when opening, executing, or scanning fails.
func (c *Client) Query(query string, args ...interface{}) ([]map[string]interface{}, error) {
	c.logger.Debug("Executing query: %s", query)

	// Use existing connection if available, otherwise create a temporary one
	var db *sql.DB
	var shouldClose bool

	if c.db != nil {
		db = c.db
	} else {
		c.logger.Debug("Opening temporary database connection: %s", c.dbPath)
		var err error
		db, err = sql.Open("sqlite3", c.dbPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open database: %w", err)
		}
		shouldClose = true
	}

	if shouldClose {
		defer db.Close()
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to get columns: %w", err)
	}

	var results []map[string]interface{}

	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))

		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		row := make(map[string]interface{})
		for i, col := range columns {
			val := values[i]
			if val != nil {
				if b, ok := val.([]byte); ok {
					row[col] = string(b)
				} else {
					row[col] = val
				}
			} else {
				row[col] = nil
			}
		}

		results = append(results, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	c.logger.Debug("Query returned %d rows", len(results))
	return results, nil
}

// Execute executes a SQL command and returns affected rows.
// query is the SQL command text, and the method returns affected row count or an error when opening, executing, or reading the result fails.
func (c *Client) Execute(query string) (int64, error) {
	c.logger.Debug("Executing command: %s", query)

	// Use existing connection if available, otherwise create a temporary one
	var db *sql.DB
	var shouldClose bool

	if c.db != nil {
		db = c.db
	} else {
		c.logger.Debug("Opening temporary database connection: %s", c.dbPath)
		var err error
		db, err = sql.Open("sqlite3", c.dbPath)
		if err != nil {
			return 0, fmt.Errorf("failed to open database: %w", err)
		}
		shouldClose = true
	}

	if shouldClose {
		defer db.Close()
	}

	result, err := db.Exec(query)
	if err != nil {
		return 0, fmt.Errorf("failed to execute command: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	c.logger.Debug("Command affected %d rows", rowsAffected)
	return rowsAffected, nil
}

// ExecuteCapability executes one bounded SCUM database statement for control-plane forwarding.
// query is a single SQL statement, options contains query ID, arguments and limits, and the method returns a shaped result or an error when validation or execution fails.
func (c *Client) ExecuteCapability(query string, options QueryOptions) (QueryResult, error) {
	action, cleaned, err := ClassifySQL(query)
	if err != nil {
		return QueryResult{}, err
	}
	options = normalizeQueryOptions(options)
	ctx, cancel := context.WithTimeout(context.Background(), options.Timeout)
	defer cancel()

	started := time.Now()
	switch action {
	case SQLActionRead, SQLActionPragma:
		result, err := c.queryBounded(ctx, cleaned, options)
		if err != nil {
			return QueryResult{}, err
		}
		result.Action = action
		result.DurationMS = time.Since(started).Milliseconds()
		return result, nil
	case SQLActionWrite, SQLActionSchema, SQLActionTransaction:
		result, err := c.execBounded(ctx, cleaned, options)
		if err != nil {
			return QueryResult{}, err
		}
		result.Action = action
		result.DurationMS = time.Since(started).Milliseconds()
		return result, nil
	default:
		return QueryResult{}, fmt.Errorf("unsupported SQL action: %s", action)
	}
}

// ExecuteReadOnlyCapability executes one bounded read-only SCUM database statement for plugin forwarding.
// query is a single SQL statement, options contains query ID, arguments and limits, and the method returns rows or an error when the statement is not read-only or execution fails.
func (c *Client) ExecuteReadOnlyCapability(query string, options QueryOptions) (QueryResult, error) {
	action, cleaned, err := ClassifySQL(query)
	if err != nil {
		return QueryResult{}, err
	}
	if action != SQLActionRead {
		return QueryResult{}, fmt.Errorf("read-only database capability rejected %s statement", action)
	}
	options = normalizeQueryOptions(options)
	ctx, cancel := context.WithTimeout(context.Background(), options.Timeout)
	defer cancel()
	started := time.Now()
	result, err := c.queryBounded(ctx, cleaned, options)
	if err != nil {
		return QueryResult{}, err
	}
	result.Action = action
	result.DurationMS = time.Since(started).Milliseconds()
	return result, nil
}

// ClassifySQL classifies a single SQL statement for safe forwarding metadata.
// query is raw SQL text, and the function returns an action class plus cleaned SQL or an error for empty or multi-statement payloads.
func ClassifySQL(query string) (string, string, error) {
	cleaned := stripSQLComments(strings.TrimSpace(query))
	if cleaned == "" {
		return "", "", fmt.Errorf("query is required")
	}
	if hasMultipleStatements(cleaned) {
		return SQLActionUnsafe, "", fmt.Errorf("multiple SQL statements are not allowed")
	}
	normalized := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(cleaned, ";")))
	switch firstSQLKeyword(normalized) {
	case "select", "with":
		return SQLActionRead, cleaned, nil
	case "pragma":
		return SQLActionPragma, cleaned, nil
	case "insert", "update", "delete", "replace":
		return SQLActionWrite, cleaned, nil
	case "create", "alter", "drop", "reindex", "vacuum":
		return SQLActionSchema, cleaned, nil
	case "begin", "commit", "rollback", "savepoint", "release":
		return SQLActionTransaction, cleaned, nil
	default:
		return SQLActionUnsafe, "", fmt.Errorf("unsupported SQL statement type")
	}
}

// SanitizeError removes host paths and obvious secret markers from database errors.
// err is an execution error, and the function returns an empty string for nil or a redacted message for responses.
func SanitizeError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	for _, field := range []string{"token", "password", "passwd", "secret"} {
		re := regexp.MustCompile(`(?i)` + field + `\s*[:=]\s*[^,\s]+`)
		message = re.ReplaceAllString(message, field+"=***")
	}
	parts := strings.Fields(message)
	for _, part := range parts {
		if strings.HasPrefix(part, "/") || strings.Contains(part, ":\\") {
			message = strings.Replace(message, part, "***", 1)
		}
	}
	return message
}

// queryBounded executes a bounded read statement.
// ctx controls timeout, query is cleaned SQL, options contains params and limits, and the method returns rows, columns and truncation metadata.
func (c *Client) queryBounded(ctx context.Context, query string, options QueryOptions) (QueryResult, error) {
	db, shouldClose, err := c.databaseHandle()
	if err != nil {
		return QueryResult{}, err
	}
	if shouldClose {
		defer db.Close()
	}
	rows, err := db.QueryContext(ctx, query, options.Args...)
	if err != nil {
		return QueryResult{}, fmt.Errorf("execute database query: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return QueryResult{}, fmt.Errorf("read database columns: %w", err)
	}
	result := QueryResult{QueryID: options.QueryID, Columns: columns, Rows: make([]map[string]interface{}, 0)}
	for rows.Next() {
		if len(result.Rows) >= options.MaxRows {
			result.Truncated = true
			result.TruncatedBy = "rows"
			break
		}
		row, err := scanRow(rows, columns)
		if err != nil {
			return QueryResult{}, err
		}
		nextRows := append(result.Rows, row)
		if encoded, err := json.Marshal(nextRows); err == nil && len(encoded) > options.MaxBytes {
			result.Truncated = true
			result.TruncatedBy = "bytes"
			break
		}
		result.Rows = nextRows
	}
	if err := rows.Err(); err != nil {
		return QueryResult{}, fmt.Errorf("iterate database rows: %w", err)
	}
	return result, nil
}

// execBounded executes a write, schema, or transaction statement.
// ctx controls timeout, query is cleaned SQL, options contains params and query ID, and the method returns affected row metadata.
func (c *Client) execBounded(ctx context.Context, query string, options QueryOptions) (QueryResult, error) {
	db, shouldClose, err := c.databaseHandle()
	if err != nil {
		return QueryResult{}, err
	}
	if shouldClose {
		defer db.Close()
	}
	execResult, err := db.ExecContext(ctx, query, options.Args...)
	if err != nil {
		return QueryResult{}, fmt.Errorf("execute database command: %w", err)
	}
	rowsAffected, err := execResult.RowsAffected()
	if err != nil {
		rowsAffected = 0
	}
	return QueryResult{QueryID: options.QueryID, RowsAffected: rowsAffected}, nil
}

// databaseHandle returns the active or temporary SQLite handle.
// It takes no parameters and returns the database handle, whether it must be closed by the caller, and an error when opening fails.
func (c *Client) databaseHandle() (*sql.DB, bool, error) {
	if c.db != nil {
		return c.db, false, nil
	}
	db, err := sql.Open("sqlite3", c.dbPath)
	if err != nil {
		return nil, false, fmt.Errorf("open database: %w", err)
	}
	return db, true, nil
}

// normalizeQueryOptions fills default timeout and result limits.
// options contains caller-supplied limits, and the function returns concrete execution options.
func normalizeQueryOptions(options QueryOptions) QueryOptions {
	if options.Timeout <= 0 {
		options.Timeout = DefaultQueryTimeout
	}
	if options.MaxRows <= 0 {
		options.MaxRows = DefaultMaxRows
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	return options
}

// scanRow scans one SQL row into a map keyed by column name.
// rows is positioned on a row, columns are the column names, and the function returns a row map or a scan error.
func scanRow(rows *sql.Rows, columns []string) (map[string]interface{}, error) {
	values := make([]interface{}, len(columns))
	valuePtrs := make([]interface{}, len(columns))
	for i := range values {
		valuePtrs[i] = &values[i]
	}
	if err := rows.Scan(valuePtrs...); err != nil {
		return nil, fmt.Errorf("scan database row: %w", err)
	}
	row := make(map[string]interface{}, len(columns))
	for i, col := range columns {
		if bytes, ok := values[i].([]byte); ok {
			row[col] = string(bytes)
			continue
		}
		row[col] = values[i]
	}
	return row, nil
}

// stripSQLComments removes simple SQL comments before classification.
// query is raw SQL text, and the function returns SQL text without line or block comments.
func stripSQLComments(query string) string {
	lineComment := regexp.MustCompile(`(?m)--.*$`)
	blockComment := regexp.MustCompile(`(?s)/\*.*?\*/`)
	return strings.TrimSpace(lineComment.ReplaceAllString(blockComment.ReplaceAllString(query, " "), " "))
}

// hasMultipleStatements reports whether SQL contains more than one statement.
// query is comment-free SQL, and the function returns true when non-empty SQL appears after a semicolon.
func hasMultipleStatements(query string) bool {
	trimmed := strings.TrimSpace(query)
	if strings.Count(trimmed, ";") == 0 {
		return false
	}
	withoutTrailing := strings.TrimSpace(strings.TrimSuffix(trimmed, ";"))
	return strings.Contains(withoutTrailing, ";")
}

// firstSQLKeyword extracts the first SQL keyword.
// normalized is lower-case SQL text, and the function returns the first whitespace-delimited token.
func firstSQLKeyword(normalized string) string {
	fields := strings.Fields(normalized)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
