package codeintel

import (
	"os"
	"path/filepath"
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
