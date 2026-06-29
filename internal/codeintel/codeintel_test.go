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
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n\ntype Runner struct{}\n\nfunc Run() {}\n"), 0o644))

	symbols, err := GoSymbols(workspace)
	require.NoError(t, err)
	require.Len(t, symbols, 2)
	require.Equal(t, "Runner", symbols[0].Name)
	require.Equal(t, "type", symbols[0].Kind)
	require.Equal(t, 3, symbols[0].Line)
	require.Equal(t, "Run", symbols[1].Name)
	require.Equal(t, "function", symbols[1].Kind)
	require.Equal(t, 5, symbols[1].Line)
}

func TestDefinitionReferencesHoverAndCodeMap(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "pkg"), 0o755))
	source := "package pkg\n\ntype Runner struct{}\n\nfunc Run() Runner { return Runner{} }\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "runner.go"), []byte(source), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "ignored.txt"), []byte("Run\n"), 0o644))

	definition, ok, err := Definition(workspace, "Run")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "pkg/runner.go", definition.Path)
	require.Equal(t, 5, definition.Line)

	refs, err := References(workspace, "Runner", 10)
	require.NoError(t, err)
	require.Len(t, refs, 2)
	require.Equal(t, "pkg/runner.go", refs[0].Path)
	require.Contains(t, refs[0].Text, "type Runner")

	hover, err := HoverInfo(workspace, "Run", 1)
	require.NoError(t, err)
	require.True(t, hover.Found)
	require.Equal(t, "function", hover.Kind)
	require.Equal(t, "pkg/runner.go", hover.Path)
	require.NotEmpty(t, hover.Snippet)

	entries, err := CodeMap(workspace, 2, 10)
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	require.Contains(t, entries, MapEntry{Path: "pkg", Type: "dir", Depth: 1})
	require.Contains(t, entries, MapEntry{Path: "pkg/runner.go", Type: "file", Depth: 2})
}

func TestEditNotebookCell(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nb.ipynb")
	require.NoError(t, os.WriteFile(path, []byte(`{"metadata":{"kernelspec":{"language":"python"}},"cells":[]}`), 0o644))

	require.NoError(t, EditNotebookCell(path, 0, "markdown", "# Title"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"cell_type": "markdown"`)
	require.Contains(t, string(data), "# Title")
	require.Contains(t, string(data), `"kernelspec"`)

	result, err := EditNotebook(path, NotebookEditOptions{Index: 0, Mode: "insert", CellType: "code", Source: "print('hello')\n"})
	require.NoError(t, err)
	require.Equal(t, "insert", result.Mode)
	require.Equal(t, 2, result.CellCount)
	require.Equal(t, 1, result.SourceLines)

	result, err = EditNotebook(path, NotebookEditOptions{Index: 1, Mode: "delete"})
	require.NoError(t, err)
	require.Equal(t, "delete", result.Mode)
	require.Equal(t, 1, result.CellCount)
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
