package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestRemoteLogDirectoryCreation tests that the remotelog directory is created correctly
func TestRemoteLogDirectoryCreation(t *testing.T) {
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

	// Verify remotelog directory was created
	remoteLogDir := filepath.Join(tempDir, RemoteLogDirectory)
	info, err := os.Stat(remoteLogDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "Remote log directory should be created")

	// Verify a log file was created
	assert.NotEmpty(t, writer.currentPath, "Current log file path should be set")
	_, err = os.Stat(writer.currentPath)
	require.NoError(t, err, "Current log file should exist")
}
