package gitignore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatcherAppliesNestedGitignoreRules(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "ignored-dir"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "pkg", "cache"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".gitignore"), []byte("*.log\nignored-dir/\n!important.log\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", ".gitignore"), []byte("cache/\n*.tmp\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "trace.log"), []byte("ignored\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "important.log"), []byte("kept\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "draft.tmp"), []byte("ignored\n"), 0o644))

	matcher, err := New(workspace)
	require.NoError(t, err)

	require.True(t, matcher.Ignored(filepath.Join(workspace, "trace.log"), false))
	require.False(t, matcher.Ignored(filepath.Join(workspace, "important.log"), false))
	require.True(t, matcher.Ignored(filepath.Join(workspace, "ignored-dir"), true))
	require.True(t, matcher.Ignored(filepath.Join(workspace, "pkg", "draft.tmp"), false))
	require.True(t, matcher.Ignored(filepath.Join(workspace, "pkg", "cache"), true))
	require.False(t, matcher.Ignored(filepath.Join(workspace, "pkg", "main.go"), false))
}
