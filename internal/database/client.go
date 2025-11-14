package database

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"scum_run/internal/logger"
)

// Client represents a SQLite database client
type Client struct {
	dbPath string
	logger *logger.Logger
	db     *sql.DB // 添加数据库连接字段
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

// Query executes a SQL query and returns the results
// Supports parameterized queries using ? placeholders
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

// Execute executes a SQL command (INSERT, UPDATE, DELETE)
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
