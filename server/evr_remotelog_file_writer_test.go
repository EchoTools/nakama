package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestRemoteLogFileWriter_WriteAndRead(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	
	logger := zap.NewNop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create the file writer
	writer, err := NewRemoteLogFileWriter(ctx, logger, tempDir)
	require.NoError(t, err)
	require.NotNil(t, writer)
	defer writer.Close()

	// Test data
	userID := uuid.Must(uuid.NewV4())
	sessionID := uuid.Must(uuid.NewV4())
	testLogs := []string{
		`{"message": "test log 1"}`,
		`{"message": "test log 2"}`,
		`{"message": "test log 3"}`,
	}

	// Write logs
	err = writer.Write(userID, sessionID, testLogs)
	require.NoError(t, err)

	// Force flush
	err = writer.flush()
	require.NoError(t, err)

	// Read logs back
	entries, err := writer.Read(userID, time.Now().Add(-1*time.Hour))
	require.NoError(t, err)
	assert.Len(t, entries, 3)

	// Verify log contents
	for i, entry := range entries {
		assert.Equal(t, userID.String(), entry.UserID)
		assert.Equal(t, sessionID.String(), entry.SessionID)
		assert.Equal(t, testLogs[i], entry.Message)
	}
}

func TestRemoteLogFileWriter_Rotation(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	
	logger := zap.NewNop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create the file writer
	writer, err := NewRemoteLogFileWriter(ctx, logger, tempDir)
	require.NoError(t, err)
	require.NotNil(t, writer)
	defer writer.Close()

	// Get initial file path
	initialPath := writer.currentPath

	// Sleep for a moment to ensure timestamp difference
	time.Sleep(1 * time.Second)

	// Force rotation
	err = writer.rotate()
	require.NoError(t, err)

	// Verify new file was created
	assert.NotEqual(t, initialPath, writer.currentPath)
	
	// Verify both files exist
	_, err = os.Stat(initialPath)
	assert.NoError(t, err)
	_, err = os.Stat(writer.currentPath)
	assert.NoError(t, err)
}

func TestRemoteLogFileWriter_Cleanup(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	
	logger := zap.NewNop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create the file writer
	writer, err := NewRemoteLogFileWriter(ctx, logger, tempDir)
	require.NoError(t, err)
	require.NotNil(t, writer)
	defer writer.Close()

	// Create an old file that should be cleaned up
	oldFileName := "remotelog-2020-01-01T00-00-00.jsonl"
	oldFilePath := filepath.Join(writer.baseDir, oldFileName)
	oldFile, err := os.Create(oldFilePath)
	require.NoError(t, err)
	oldFile.Close()

	// Set the file's modification time to be older than retention period
	const retentionMultiplier = 2 // Make file twice as old as retention period
	oldTime := time.Now().Add(-RemoteLogRetentionDays * 24 * time.Hour * retentionMultiplier)
	err = os.Chtimes(oldFilePath, oldTime, oldTime)
	require.NoError(t, err)

	// Run cleanup
	writer.cleanup()

	// Verify old file was removed
	_, err = os.Stat(oldFilePath)
	assert.True(t, os.IsNotExist(err), "Old log file should have been removed")

	// Verify current file still exists
	_, err = os.Stat(writer.currentPath)
	assert.NoError(t, err, "Current log file should still exist")
}

func TestRemoteLogFileWriter_JSONLinesFormat(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	
	logger := zap.NewNop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create the file writer
	writer, err := NewRemoteLogFileWriter(ctx, logger, tempDir)
	require.NoError(t, err)
	require.NotNil(t, writer)
	defer writer.Close()

	// Write logs
	userID := uuid.Must(uuid.NewV4())
	sessionID := uuid.Must(uuid.NewV4())
	testLogs := []string{
		`{"test": "log1"}`,
		`{"test": "log2"}`,
	}

	err = writer.Write(userID, sessionID, testLogs)
	require.NoError(t, err)
	
	// Force flush
	err = writer.flush()
	require.NoError(t, err)

	// Read the file directly and verify JSONL format
	file, err := os.Open(writer.currentPath)
	require.NoError(t, err)
	defer file.Close()

	decoder := json.NewDecoder(file)
	lineCount := 0
	for decoder.More() {
		var entry RemoteLogFileEntry
		err := decoder.Decode(&entry)
		require.NoError(t, err)
		assert.Equal(t, userID.String(), entry.UserID)
		assert.Equal(t, sessionID.String(), entry.SessionID)
		lineCount++
	}

	assert.Equal(t, 2, lineCount, "Should have 2 lines in JSONL file")
}

func TestRemoteLogFileWriter_DirectoryCreation(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	
	// Remove the directory to test creation
	os.RemoveAll(tempDir)

	logger := zap.NewNop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create the file writer - should create directory
	writer, err := NewRemoteLogFileWriter(ctx, logger, tempDir)
	require.NoError(t, err)
	require.NotNil(t, writer)
	defer writer.Close()

	// Verify directory was created
	remoteLogDir := filepath.Join(tempDir, RemoteLogDirectory)
	info, err := os.Stat(remoteLogDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestRemoteLogFileWriter_FilterByTimestamp(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	
	logger := zap.NewNop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create the file writer
	writer, err := NewRemoteLogFileWriter(ctx, logger, tempDir)
	require.NoError(t, err)
	require.NotNil(t, writer)
	defer writer.Close()

	// Write logs
	userID := uuid.Must(uuid.NewV4())
	sessionID := uuid.Must(uuid.NewV4())
	testLogs := []string{`{"message": "test"}`}

	err = writer.Write(userID, sessionID, testLogs)
	require.NoError(t, err)
	
	// Force flush
	err = writer.flush()
	require.NoError(t, err)

	// Read with a future timestamp - should return nothing
	futureTime := time.Now().Add(1 * time.Hour)
	entries, err := writer.Read(userID, futureTime)
	require.NoError(t, err)
	assert.Len(t, entries, 0, "Should not return logs from before the filter time")

	// Read with a past timestamp - should return logs
	pastTime := time.Now().Add(-1 * time.Hour)
	entries, err = writer.Read(userID, pastTime)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "Should return logs from after the filter time")
}
