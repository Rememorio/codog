package codeintel

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGoSymbols(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n\nfunc Run() {}\n"), 0o644))

	symbols, err := GoSymbols(workspace)
	require.NoError(t, err)
	require.Len(t, symbols, 1)
	require.Equal(t, "Run", symbols[0].Name)
	require.Equal(t, 3, symbols[0].Line)
}

func TestEditNotebookCell(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nb.ipynb")
	require.NoError(t, os.WriteFile(path, []byte(`{"cells":[]}`), 0o644))

	require.NoError(t, EditNotebookCell(path, 0, "markdown", "# Title"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"cell_type": "markdown"`)
	require.Contains(t, string(data), "# Title")
}

func TestParseGoTestJSONDiagnostics(t *testing.T) {
	workspace := t.TempDir()
	data := []byte(`{"Action":"output","Package":"example","Output":"main.go:7:13: undefined: Missing\n"}` + "\n" +
		`{"Action":"fail","Package":"example"}` + "\n")

	diagnostics, err := ParseGoTestJSON(workspace, data)
	require.NoError(t, err)
	require.Len(t, diagnostics, 2)
	require.Equal(t, "main.go", diagnostics[0].Path)
	require.Equal(t, 7, diagnostics[0].Line)
	require.Equal(t, 13, diagnostics[0].Column)
	require.Contains(t, diagnostics[0].Message, "undefined")
	require.Equal(t, "fail", diagnostics[1].Action)
}

func TestParseGoTestJSONReturnsEmptySlice(t *testing.T) {
	diagnostics, err := ParseGoTestJSON(t.TempDir(), nil)
	require.NoError(t, err)
	require.NotNil(t, diagnostics)
	require.Empty(t, diagnostics)
}

func TestGoDiagnosticsReportsBuildError(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/diag\n\ngo 1.22\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package diag\n\nfunc Broken() { Missing() }\n"), 0o644))

	diagnostics, err := GoDiagnostics(context.Background(), workspace, []string{"./..."})
	require.NoError(t, err)
	require.NotEmpty(t, diagnostics)
	var found bool
	for _, diagnostic := range diagnostics {
		if diagnostic.Path == "main.go" && diagnostic.Line == 3 && strings.Contains(diagnostic.Message, "undefined") {
			found = true
		}
	}
	require.True(t, found, "expected undefined symbol diagnostic: %#v", diagnostics)
}

func TestDefaultLSPCandidatesIncludesGo(t *testing.T) {
	candidates := DefaultLSPCandidates()
	require.NotEmpty(t, candidates)
	var found bool
	for _, candidate := range candidates {
		if candidate.Language == "go" && candidate.Command == "gopls" {
			found = true
		}
	}
	require.True(t, found)
}

func TestLSPStoreLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := NewLSPStore(configHome, workspace)
	status, err := store.Start("go", []string{"sh", "-c", "sleep 30"})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = store.Stop("go") })
	require.Equal(t, "go", status.Language)
	require.Equal(t, "running", status.Task.Status)
	require.Contains(t, status.Command, "sleep 30")

	list, err := store.List()
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "go", list[0].Language)

	current, err := store.Status("go")
	require.NoError(t, err)
	require.Equal(t, status.TaskID, current.TaskID)
	require.Equal(t, "running", current.Task.Status)

	stopped, err := store.Stop("go")
	require.NoError(t, err)
	require.Equal(t, "stopped", stopped.Task.Status)
	require.NotNil(t, stopped.Task.CompletedAt)
	require.Eventually(t, func() bool {
		current, err := store.Status("go")
		return err == nil && current.Task.Status != "running"
	}, 2*time.Second, 50*time.Millisecond)
}

func TestLSPStoreRejectsUnsafeLanguage(t *testing.T) {
	store := NewLSPStore(t.TempDir(), t.TempDir())
	_, err := store.Start("../go", []string{"gopls"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "safe name")
}
