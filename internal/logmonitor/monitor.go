package logmonitor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"scum_run/internal/logger"
	"scum_run/internal/utils"
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
	encoding    utils.EncodingType
	lastUsed    time.Time
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
}

// scanExistingFiles scans for existing log files and initializes their states
func (m *Monitor) scanExistingFiles() error {

	entries, err := os.ReadDir(m.logsPath)
	if err != nil {
		return err
	}

	scannedCount := 0
	acceptedCount := 0

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filename := entry.Name()
		scannedCount++

		if !m.isLogFile(filename) {
			continue
		}

		filepath := filepath.Join(m.logsPath, filename)
		fileInfo, err := os.Stat(filepath)
		if err != nil {
			continue
		}

		// Detect encoding for this file (only if file has enough content)
		encoding := m.detectFileEncoding(filepath, fileInfo.Size())

		m.mutex.Lock()
		m.fileStates[filename] = &fileState{
			filename:    filename,
			lastSize:    fileInfo.Size(),
			lastModTime: fileInfo.ModTime(),
			encoding:    encoding,
			lastUsed:    time.Now(),
		}
		m.mutex.Unlock()

		acceptedCount++
	}
	return nil
}

// monitorLoop is the main monitoring loop
func (m *Monitor) monitorLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Cleanup ticker for unused encoding cache (every 2 hours)
	cleanupTicker := time.NewTicker(2 * time.Hour)
	defer cleanupTicker.Stop()

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
		case <-cleanupTicker.C:
			// Cleanup unused encoding cache
			m.cleanupUnusedEncodings()
		}
	}
}

// handleFileEvent handles file system events
func (m *Monitor) handleFileEvent(event fsnotify.Event) {
	filename := filepath.Base(event.Name)

	if !m.isLogFile(filename) {
		return
	}

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
		return
	}

	// Detect encoding for this new file (only if file has enough content)
	encoding := m.detectFileEncoding(filepath, fileInfo.Size())

	m.mutex.Lock()
	m.fileStates[filename] = &fileState{
		filename:    filename,
		lastSize:    0, // Start from beginning for new files
		lastModTime: fileInfo.ModTime(),
		encoding:    encoding,
		lastUsed:    time.Now(),
	}
	m.mutex.Unlock()

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
		m.logger.Debug("🗑️ File %s no longer exists, removed from monitoring", filename)
		return
	}

	m.mutex.Lock()
	state, exists := m.fileStates[filename]
	if !exists {
		// Initialize state for this file
		encoding := m.detectFileEncoding(filepath, fileInfo.Size())
		state = &fileState{
			filename:    filename,
			lastSize:    0,
			lastModTime: fileInfo.ModTime(),
			encoding:    encoding,
			lastUsed:    time.Now(),
		}
		m.fileStates[filename] = state
	} else {
		// Update last used time
		state.lastUsed = time.Now()
	}
	m.mutex.Unlock()

	// Check if file has grown
	if fileInfo.Size() <= state.lastSize {
		return
	}

	// Read new lines using cached encoding
	newLines, err := m.readNewLinesWithEncoding(filepath, state.lastSize, fileInfo.Size(), state.encoding)
	if err != nil {
		m.logger.Error("Failed to read new lines from %s: %v", filename, err)
		return
	}

	if len(newLines) > 0 {
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

	// Detect encoding and decode content using the improved encoding utility
	text, _, err := utils.ConvertToUTF8(string(content))
	if err != nil {
		// If conversion fails, try to use the content as-is
		text = string(content)
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

// detectFileEncoding detects the encoding of a file by reading a sample
// Only detects encoding if file has enough content (at least 10 lines worth)
func (m *Monitor) detectFileEncoding(filepath string, fileSize int64) utils.EncodingType {
	// If file is too small (less than 1KB), default to UTF-8
	if fileSize < 1024 {
		return utils.EncodingUTF8
	}

	file, err := os.Open(filepath)
	if err != nil {
		return utils.EncodingUTF8 // Default to UTF-8 if can't read
	}
	defer file.Close()

	// Read first 4KB to detect encoding (enough for multiple lines)
	sample := make([]byte, 4096)
	n, err := file.Read(sample)
	if err != nil && err != io.EOF {
		return utils.EncodingUTF8 // Default to UTF-8 if can't read
	}
	sample = sample[:n]

	// Use the encoding utility to detect encoding
	_, encoding, _ := utils.ConvertToUTF8(string(sample))
	return encoding
}

// readNewLinesWithEncoding reads new lines from a file using the specified encoding
func (m *Monitor) readNewLinesWithEncoding(filepath string, startOffset, endOffset int64, encoding utils.EncodingType) ([]string, error) {
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

	// Convert content to UTF-8 using the cached encoding
	text, err := m.convertToUTF8WithEncoding(string(content), encoding)
	if err != nil {
		// If conversion fails, try to use the content as-is
		text = string(content)
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

// convertToUTF8WithEncoding converts content to UTF-8 using the specified encoding
func (m *Monitor) convertToUTF8WithEncoding(content string, encoding utils.EncodingType) (string, error) {
	switch encoding {
	case utils.EncodingUTF8:
		return content, nil
	case utils.EncodingUTF16LE, utils.EncodingUTF16BE:
		// Use the encoding utility for UTF-16 conversion
		text, _, err := utils.ConvertToUTF8(content)
		return text, err
	default:
		// For unknown encodings, try the general conversion
		text, _, err := utils.ConvertToUTF8(content)
		return text, err
	}
}

// cleanupUnusedEncodings removes encoding cache for files not used in the last 2 hours
func (m *Monitor) cleanupUnusedEncodings() {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	cutoff := time.Now().Add(-2 * time.Hour)
	for _, state := range m.fileStates {
		if state.lastUsed.Before(cutoff) {
			// Reset encoding to force re-detection next time
			state.encoding = utils.EncodingUnknown
		}
	}
}

// isLogFile determines if a file is a log file we should monitor
// Only monitor specific SCUM log files: admin_, chat_, economy_, event_kill_, famepoints_, gameplay_, kill_, login_, violations_, vehicle_destruction_
func (m *Monitor) isLogFile(filename string) bool {
	// Convert filename to lowercase for case-insensitive matching
	lowerFilename := strings.ToLower(filename)

	// Check if file has .log extension
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != ".log" {
		return false
	}

	// Check if filename starts with one of the specific SCUM log prefixes
	scumLogPrefixes := []string{
		"admin", "chat", "economy", "event_kill", "famepoints",
		"gameplay", "kill", "login", "violations", "vehicle_destruction",
	}
	for _, prefix := range scumLogPrefixes {
		if strings.HasPrefix(lowerFilename, prefix) {
			return true
		}
	}

	return false
}
