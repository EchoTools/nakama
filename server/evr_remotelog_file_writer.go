package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"go.uber.org/zap"
)

const (
	// RemoteLogDirectory is the subdirectory under datadir where remote logs are stored
	RemoteLogDirectory = "remotelogs"
	// RemoteLogMaxFileSize is the maximum size of a log file before rotation (10MB)
	RemoteLogMaxFileSize = 10 * 1024 * 1024
	// RemoteLogRetentionDays is how long to keep remote log files
	RemoteLogRetentionDays = 7
	// RemoteLogFlushInterval is how often to flush logs to disk
	RemoteLogFlushInterval = 10 * time.Second
	// RemoteLogEntryOverheadBytes is the estimated overhead per log entry (JSON structure + metadata)
	RemoteLogEntryOverheadBytes = 100
	// RemoteLogRotationInterval is how often to rotate log files by time
	RemoteLogRotationInterval = 24 * time.Hour
)

// RemoteLogFileEntry represents a single log entry written to disk
type RemoteLogFileEntry struct {
	UserID    string    `json:"user_id"`
	SessionID string    `json:"session_id"`
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
}

// RemoteLogFileWriter manages writing remote logs to disk
type RemoteLogFileWriter struct {
	logger  *zap.Logger
	baseDir string

	mu          sync.RWMutex
	currentFile *os.File
	currentPath string
	currentSize int64
	lastRotate  time.Time
}

// NewRemoteLogFileWriter creates a new file-based remote log writer
func NewRemoteLogFileWriter(ctx context.Context, logger *zap.Logger, dataDir string) (*RemoteLogFileWriter, error) {
	baseDir := filepath.Join(dataDir, RemoteLogDirectory)

	// Create the remote log directory if it doesn't exist
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create remote log directory: %w", err)
	}

	writer := &RemoteLogFileWriter{
		logger:     logger,
		baseDir:    baseDir,
		lastRotate: time.Now(),
	}

	// Open initial log file
	if err := writer.rotate(); err != nil {
		return nil, fmt.Errorf("failed to open initial log file: %w", err)
	}

	// Start background cleanup routine
	go writer.cleanupRoutine(ctx)

	// Start background flush routine
	go writer.flushRoutine(ctx)

	return writer, nil
}

// Write writes log entries to disk in JSON Lines format
func (w *RemoteLogFileWriter) Write(userID, sessionID uuid.UUID, logs []string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now().UTC()

	// Check if we need to rotate the file
	if w.currentSize >= RemoteLogMaxFileSize || time.Since(w.lastRotate) >= RemoteLogRotationInterval {
		if err := w.rotate(); err != nil {
			return fmt.Errorf("failed to rotate log file: %w", err)
		}
	}

	encoder := json.NewEncoder(w.currentFile)

	for _, msg := range logs {
		entry := RemoteLogFileEntry{
			UserID:    userID.String(),
			SessionID: sessionID.String(),
			Timestamp: now,
			Message:   msg,
		}

		// Encode to get actual size
		entryJSON, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("failed to marshal log entry: %w", err)
		}

		if err := encoder.Encode(entry); err != nil {
			return fmt.Errorf("failed to write log entry: %w", err)
		}

		// Track actual size including newline
		w.currentSize += int64(len(entryJSON) + 1)
	}

	return nil
}

// rotate closes the current file and opens a new one
// Must be called with w.mu held (write lock)
func (w *RemoteLogFileWriter) rotate() error {
	// Close current file if open
	if w.currentFile != nil {
		if err := w.currentFile.Sync(); err != nil {
			w.logger.Warn("Failed to sync log file before rotation", zap.Error(err))
		}
		if err := w.currentFile.Close(); err != nil {
			w.logger.Warn("Failed to close log file", zap.Error(err))
		}
	}

	// Generate new filename with timestamp
	timestamp := time.Now().UTC().Format("2006-01-02T15-04-05")
	filename := fmt.Sprintf("remotelog-%s.jsonl", timestamp)
	w.currentPath = filepath.Join(w.baseDir, filename)

	// Open new file with restrictive permissions (owner read/write only)
	file, err := os.OpenFile(w.currentPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	w.currentFile = file
	w.currentSize = 0
	w.lastRotate = time.Now()

	w.logger.Info("Rotated remote log file", zap.String("path", w.currentPath))

	return nil
}

// flush syncs the current file to disk
func (w *RemoteLogFileWriter) flush() error {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if w.currentFile != nil {
		return w.currentFile.Sync()
	}
	return nil
}

// flushRoutine periodically flushes logs to disk
func (w *RemoteLogFileWriter) flushRoutine(ctx context.Context) {
	ticker := time.NewTicker(RemoteLogFlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Final flush before shutdown
			if err := w.flush(); err != nil {
				w.logger.Warn("Failed to flush logs on shutdown", zap.Error(err))
			}
			return
		case <-ticker.C:
			if err := w.flush(); err != nil {
				w.logger.Warn("Failed to flush logs", zap.Error(err))
			}
		}
	}
}

// cleanupRoutine removes old log files
func (w *RemoteLogFileWriter) cleanupRoutine(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	// Run cleanup immediately on start
	w.cleanup()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.cleanup()
		}
	}
}

// cleanup removes log files older than the retention period
func (w *RemoteLogFileWriter) cleanup() {
	w.mu.RLock()
	currentPath := w.currentPath
	w.mu.RUnlock()

	entries, err := os.ReadDir(w.baseDir)
	if err != nil {
		w.logger.Warn("Failed to read remote log directory", zap.Error(err))
		return
	}

	cutoff := time.Now().Add(-RemoteLogRetentionDays * 24 * time.Hour)
	removed := 0

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// Only process .jsonl files
		if filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}

		path := filepath.Join(w.baseDir, entry.Name())

		// Never remove the current log file
		if path == currentPath {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			w.logger.Warn("Failed to get file info", zap.String("file", entry.Name()), zap.Error(err))
			continue
		}

		// Remove files older than retention period
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err != nil {
				w.logger.Warn("Failed to remove old log file", zap.String("path", path), zap.Error(err))
			} else {
				removed++
			}
		}
	}

	if removed > 0 {
		w.logger.Info("Cleaned up old remote log files", zap.Int("count", removed))
	}
}

// Close closes the file writer
func (w *RemoteLogFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.currentFile != nil {
		if err := w.currentFile.Sync(); err != nil {
			w.logger.Warn("Failed to sync log file on close", zap.Error(err))
		}
		return w.currentFile.Close()
	}
	return nil
}

// Read reads remote log entries for a specific user
// This is provided for debugging access but is not optimized for high-frequency reads
func (w *RemoteLogFileWriter) Read(userID uuid.UUID, since time.Time) ([]RemoteLogFileEntry, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	entries, err := os.ReadDir(w.baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read remote log directory: %w", err)
	}

	var results []RemoteLogFileEntry
	userIDStr := userID.String()

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}

		path := filepath.Join(w.baseDir, entry.Name())
		file, err := os.Open(path)
		if err != nil {
			w.logger.Warn("Failed to open log file for reading", zap.String("path", path), zap.Error(err))
			continue
		}

		decoder := json.NewDecoder(file)
		for decoder.More() {
			var logEntry RemoteLogFileEntry
			if err := decoder.Decode(&logEntry); err != nil {
				w.logger.Warn("Failed to decode log entry", zap.Error(err))
				continue
			}

			// Filter by user ID and timestamp
			if logEntry.UserID == userIDStr && logEntry.Timestamp.After(since) {
				results = append(results, logEntry)
			}
		}

		// Close file immediately after processing to avoid resource leak
		if err := file.Close(); err != nil {
			w.logger.Warn("Failed to close log file after reading", zap.String("path", path), zap.Error(err))
		}
	}

	return results, nil
}
