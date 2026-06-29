package workspaceops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileOperationsEnforceSizeLimits(t *testing.T) {
	workspace := t.TempDir()
	service := Service{Workspace: workspace}
	largeContent := strings.Repeat("a", int(MaxFileBytes)+1)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "large.txt"), []byte(largeContent), 0o644))

	read, err := service.Read(ReadOptions{Path: "large.txt", Limit: int(MaxFileBytes) + 100})
	require.NoError(t, err)
	require.Equal(t, int(MaxFileBytes)+1, read.Bytes)
	require.True(t, read.Truncated)
	require.Len(t, read.Content, int(MaxFileBytes))

	_, err = service.Write(WriteOptions{Path: "too-large.txt", Content: largeContent})
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum workspace file size")

	_, err = service.Edit(EditOptions{Path: "large.txt", OldString: "a", NewString: "b"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum editable size")

	_, err = service.Diff(DiffOptions{Path: "large.txt", OldString: "a", NewString: "b"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum editable size")
}
