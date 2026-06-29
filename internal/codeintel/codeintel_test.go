package codeintel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
