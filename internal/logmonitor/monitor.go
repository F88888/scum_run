package logmonitor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/fsnotify/fsnotify"

	"scum_run/internal/logger"
)

// LogUpdateCallback is called when new log lines are detected
type LogUpdateCallback func(filename string, lines []string)

// Monitor monitors log files for changes
type Monitor struct {
	logsPath   string
	logger     *logger.Logger
	callback   LogUpdateCallback
	watcher    *fsnotify.Watcher
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	fileStates map[string]*fileState
	mutex      sync.Mutex
}

// fileState tracks the state of a monitored file
type fileState struct {
	filename    string
	lastSize    int64
	lastModTime time.Time
}

// New creates a new log monitor
func New(logsPath string, logger *logger.Logger, callback LogUpdateCallback) *Monitor {
	ctx, cancel := context.WithCancel(context.Background())

	return &Monitor{
		logsPath:   logsPath,
		logger:     logger,
		callback:   callback,
		ctx:        ctx,
		cancel:     cancel,
		fileStates: make(map[string]*fileState),
	}
}

// Start starts monitoring log files
func (m *Monitor) Start() error {
	var err error
	m.watcher, err = fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create file watcher: %w", err)
	}

	// Check if logs directory exists
	if _, err := os.Stat(m.logsPath); os.IsNotExist(err) {
		m.logger.Warn("Logs directory does not exist: %s", m.logsPath)
		return nil
	}

	// Add the logs directory to the watcher
	if err := m.watcher.Add(m.logsPath); err != nil {
		return fmt.Errorf("failed to add logs directory to watcher: %w", err)
	}

	m.logger.Info("Started monitoring logs directory: %s", m.logsPath)

	// Scan existing log files
	if err := m.scanExistingFiles(); err != nil {
		m.logger.Warn("Failed to scan existing files: %v", err)
	}

	// Start the monitoring goroutine
	m.wg.Add(1)
	go m.monitorLoop()

	return nil
}

// Stop stops monitoring log files
func (m *Monitor) Stop() {
	if m.cancel != nil {
		m.cancel()
	}

	if m.watcher != nil {
		m.watcher.Close()
	}

	m.wg.Wait()
	m.logger.Info("Log monitor stopped")
}

// scanExistingFiles scans for existing log files and initializes their states
func (m *Monitor) scanExistingFiles() error {
	entries, err := os.ReadDir(m.logsPath)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filename := entry.Name()
		if !m.isLogFile(filename) {
			continue
		}

		filepath := filepath.Join(m.logsPath, filename)
		fileInfo, err := os.Stat(filepath)
		if err != nil {
			m.logger.Warn("Failed to stat file %s: %v", filepath, err)
			continue
		}

		m.mutex.Lock()
		m.fileStates[filename] = &fileState{
			filename:    filename,
			lastSize:    fileInfo.Size(),
			lastModTime: fileInfo.ModTime(),
		}
		m.mutex.Unlock()

		m.logger.Debug("Initialized state for log file: %s", filename)
	}

	return nil
}

// monitorLoop is the main monitoring loop
func (m *Monitor) monitorLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case event := <-m.watcher.Events:
			m.handleFileEvent(event)
		case err := <-m.watcher.Errors:
			m.logger.Error("File watcher error: %v", err)
		case <-ticker.C:
			// Periodic check for file changes
			m.checkFileChanges()
		}
	}
}

// handleFileEvent handles file system events
func (m *Monitor) handleFileEvent(event fsnotify.Event) {
	filename := filepath.Base(event.Name)

	if !m.isLogFile(filename) {
		return
	}

	m.logger.Debug("File event: %s %s", event.Op.String(), filename)

	switch {
	case event.Op&fsnotify.Create == fsnotify.Create:
		m.handleFileCreated(filename)
	case event.Op&fsnotify.Write == fsnotify.Write:
		m.handleFileModified(filename)
	case event.Op&fsnotify.Remove == fsnotify.Remove:
		m.handleFileRemoved(filename)
	}
}

// handleFileCreated handles file creation events
func (m *Monitor) handleFileCreated(filename string) {
	filepath := filepath.Join(m.logsPath, filename)
	fileInfo, err := os.Stat(filepath)
	if err != nil {
		m.logger.Warn("Failed to stat newly created file %s: %v", filepath, err)
		return
	}

	m.mutex.Lock()
	m.fileStates[filename] = &fileState{
		filename:    filename,
		lastSize:    0, // Start from beginning for new files
		lastModTime: fileInfo.ModTime(),
	}
	m.mutex.Unlock()

	m.logger.Info("Started monitoring new log file: %s", filename)
	m.checkFileForNewLines(filename)
}

// handleFileModified handles file modification events
func (m *Monitor) handleFileModified(filename string) {
	m.checkFileForNewLines(filename)
}

// handleFileRemoved handles file removal events
func (m *Monitor) handleFileRemoved(filename string) {
	m.mutex.Lock()
	delete(m.fileStates, filename)
	m.mutex.Unlock()

	m.logger.Info("Stopped monitoring removed log file: %s", filename)
}

// checkFileChanges periodically checks all monitored files for changes
func (m *Monitor) checkFileChanges() {
	m.mutex.Lock()
	filenames := make([]string, 0, len(m.fileStates))
	for filename := range m.fileStates {
		filenames = append(filenames, filename)
	}
	m.mutex.Unlock()

	for _, filename := range filenames {
		m.checkFileForNewLines(filename)
	}
}

// checkFileForNewLines checks a specific file for new lines
func (m *Monitor) checkFileForNewLines(filename string) {
	filepath := filepath.Join(m.logsPath, filename)

	fileInfo, err := os.Stat(filepath)
	if err != nil {
		// File might have been deleted
		m.mutex.Lock()
		delete(m.fileStates, filename)
		m.mutex.Unlock()
		return
	}

	m.mutex.Lock()
	state, exists := m.fileStates[filename]
	if !exists {
		// Initialize state for this file
		state = &fileState{
			filename:    filename,
			lastSize:    0,
			lastModTime: fileInfo.ModTime(),
		}
		m.fileStates[filename] = state
	}
	m.mutex.Unlock()

	// Check if file has grown
	if fileInfo.Size() <= state.lastSize {
		return
	}

	// Read new lines
	newLines, err := m.readNewLines(filepath, state.lastSize, fileInfo.Size())
	if err != nil {
		m.logger.Error("Failed to read new lines from %s: %v", filename, err)
		return
	}

	if len(newLines) > 0 {
		m.logger.Debug("Found %d new lines in %s", len(newLines), filename)

		// Update state
		m.mutex.Lock()
		state.lastSize = fileInfo.Size()
		state.lastModTime = fileInfo.ModTime()
		m.mutex.Unlock()

		// Call callback with new lines
		if m.callback != nil {
			m.callback(filename, newLines)
		}
	}
}

// readNewLines reads new lines from a file starting from the given offset
// Supports both UTF-8 and UTF-16LE encoding
func (m *Monitor) readNewLines(filepath string, startOffset, endOffset int64) ([]string, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Seek to the start offset
	if _, err := file.Seek(startOffset, io.SeekStart); err != nil {
		return nil, err
	}

	// Read the new content
	contentSize := endOffset - startOffset
	content := make([]byte, contentSize)
	n, err := io.ReadFull(file, content)
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	content = content[:n]

	// Detect encoding and decode content
	text, err := m.decodeContent(content)
	if err != nil {
		return nil, fmt.Errorf("failed to decode content: %w", err)
	}

	// Split into lines
	lines := strings.Split(text, "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}

	return result, nil
}

// decodeContent detects encoding and decodes the content
func (m *Monitor) decodeContent(content []byte) (string, error) {
	if len(content) == 0 {
		return "", nil
	}

	// Check for UTF-16LE BOM (Byte Order Mark)
	if len(content) >= 2 && content[0] == 0xFF && content[1] == 0xFE {
		// UTF-16LE with BOM
		return m.decodeUTF16LE(content[2:])
	}

	// Check if content is valid UTF-8
	if utf8.Valid(content) {
		return string(content), nil
	}

	// Try to decode as UTF-16LE without BOM
	// Check if content length is even (UTF-16LE requires even number of bytes)
	if len(content)%2 == 0 {
		// Try UTF-16LE decoding
		if text, err := m.decodeUTF16LE(content); err == nil {
			return text, nil
		}
	}

	// Fallback to UTF-8 with replacement characters
	return string(content), nil
}

// decodeUTF16LE decodes UTF-16LE encoded content
func (m *Monitor) decodeUTF16LE(content []byte) (string, error) {
	if len(content)%2 != 0 {
		return "", fmt.Errorf("UTF-16LE content must have even number of bytes")
	}

	// Convert bytes to UTF-16 code units
	codeUnits := make([]uint16, len(content)/2)
	for i := 0; i < len(content); i += 2 {
		codeUnits[i/2] = uint16(content[i]) | (uint16(content[i+1]) << 8)
	}

	// Convert UTF-16 to UTF-8
	return string(utf16.Decode(codeUnits)), nil
}

// isLogFile determines if a file is a log file we should monitor
// Only monitor specific SCUM log files: chat, login, kill, economy, gameplay, vehicle_destruction
func (m *Monitor) isLogFile(filename string) bool {
	// Convert filename to lowercase for case-insensitive matching
	lowerFilename := strings.ToLower(filename)

	// Check if file has .log extension
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != ".log" {
		return false
	}

	// Check if filename starts with one of the specific SCUM log prefixes
	scumLogPrefixes := []string{"chat", "login", "kill", "economy", "gameplay", "vehicle_destruction"}
	for _, prefix := range scumLogPrefixes {
		if strings.HasPrefix(lowerFilename, prefix) {
			return true
		}
	}

	return false
}
