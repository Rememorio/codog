package codeintel

import (
	"bufio"
	"context"
	"encoding/json"
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

func TestCompletionsAndFormatGoFile(t *testing.T) {
	workspace := t.TempDir()
	source := "package main\n\ntype Runner struct{}\n\nfunc RunFast() Runner { return Runner{} }\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte(source), 0o644))

	completions, err := Completions(workspace, "Run", 10)
	require.NoError(t, err)
	require.Contains(t, completions, Completion{Label: "RunFast", Kind: "function", Path: "main.go", Line: 5, Detail: "main.go"})

	completions, err = Completions(workspace, "ret", 10)
	require.NoError(t, err)
	require.Contains(t, completions, Completion{Label: "return", Kind: "keyword", Detail: "Go keyword"})

	unformatted := "package main\n\nfunc main(){println(\"hi\")}\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "messy.go"), []byte(unformatted), 0o644))
	result, err := FormatGoFile(workspace, "messy.go", false)
	require.NoError(t, err)
	require.Equal(t, "format", result.Kind)
	require.Equal(t, "messy.go", result.Path)
	require.True(t, result.Changed)
	require.Contains(t, result.Content, "func main()")
	data, err := os.ReadFile(filepath.Join(workspace, "messy.go"))
	require.NoError(t, err)
	require.Equal(t, unformatted, string(data))

	result, err = FormatGoFile(workspace, "messy.go", true)
	require.NoError(t, err)
	require.True(t, result.Changed)
	data, err = os.ReadFile(filepath.Join(workspace, "messy.go"))
	require.NoError(t, err)
	require.Equal(t, result.Content, string(data))

	_, err = FormatGoFile(workspace, "../escape.go", false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workspace")
}

func TestEditNotebookCell(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nb.ipynb")
	require.NoError(t, os.WriteFile(path, []byte(`{"metadata":{"kernelspec":{"language":"python"}},"cells":[]}`), 0o644))

	result, err := EditNotebook(path, NotebookEditOptions{Index: 0, Mode: "insert", CellType: "markdown", Source: "# Title"})
	require.NoError(t, err)
	require.Equal(t, "cell-1", result.CellID)
	require.Equal(t, "python", result.Language)
	require.NoError(t, EditNotebookCell(path, 0, "markdown", "# Renamed"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"cell_type": "markdown"`)
	require.Contains(t, string(data), `"id": "cell-1"`)
	require.Contains(t, string(data), "# Renamed")
	require.Contains(t, string(data), `"kernelspec"`)

	result, err = EditNotebook(path, NotebookEditOptions{Index: 0, Mode: "insert", CellType: "code", Source: "print('hello')\n"})
	require.NoError(t, err)
	require.Equal(t, "insert", result.Mode)
	require.Equal(t, 2, result.CellCount)
	require.Equal(t, "cell-2", result.CellID)
	require.Equal(t, 1, result.SourceLines)

	result, err = EditNotebook(path, NotebookEditOptions{Index: 1, Mode: "delete"})
	require.NoError(t, err)
	require.Equal(t, "delete", result.Mode)
	require.Equal(t, 1, result.CellCount)

	_, err = EditNotebook(path, NotebookEditOptions{Index: 10, Mode: "replace", CellType: "markdown", Source: "missing"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cell index out of range")

	read, err := ReadNotebook(path, NotebookReadOptions{Limit: 1})
	require.NoError(t, err)
	require.Equal(t, "notebook_read", read.Kind)
	require.Equal(t, "python", read.Language)
	require.Equal(t, 1, read.CellCount)
	require.Len(t, read.Cells, 1)
	require.Equal(t, "cell-2", read.Cells[0].CellID)
	require.Equal(t, "print('hello')\n", read.Cells[0].Source)
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
		t.Skip("uses POSIX sleep")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := NewLSPStore(configHome, workspace)
	status, err := store.Start("go", []string{"sleep", "30"})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = store.Stop("go") })
	require.Equal(t, "go", status.Language)
	require.Equal(t, "running", status.Task.Status)
	require.Contains(t, status.Command, "sleep")
	require.Contains(t, status.Command, "30")

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

func TestLSPStoreQueryUsesStdioProtocol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell command")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n\nfunc main(){ }\n"), 0o644))
	store := NewLSPStore(configHome, workspace)
	command := "CODOG_FAKE_LSP=1 " + shellCommand([]string{os.Args[0], "-test.run", "TestFakeLSPServer"})
	require.NoError(t, store.save(LSPServer{Language: "go", Command: command, Workspace: workspace, StartedAt: time.Now()}))

	result, err := store.Query(context.Background(), "go", LSPQueryRequest{Action: "hover", Path: "main.go", Line: 2, Character: 5})
	require.NoError(t, err)
	require.Equal(t, "lsp_query", result.Kind)
	require.Equal(t, "go", result.Language)
	require.Equal(t, "hover", result.Action)
	require.Equal(t, "textDocument/hover", result.Method)
	require.Equal(t, "main.go", result.Path)
	require.NotNil(t, result.Result)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "format", Path: "main.go"})
	require.NoError(t, err)
	require.Equal(t, "format", result.Action)
	require.Equal(t, "textDocument/formatting", result.Method)
	require.Equal(t, 1, result.TextEdits)
	require.True(t, result.Changed)
	require.Contains(t, result.Content, "func main() {}")

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "diagnostics", Path: "main.go"})
	require.NoError(t, err)
	require.Equal(t, "diagnostics", result.Action)
	require.Equal(t, "textDocument/publishDiagnostics", result.Method)
	require.Len(t, result.Diagnostics, 1)
	require.Equal(t, "fake diagnostic", result.Diagnostics[0].Message)
}

func TestApplyLSPTextEdits(t *testing.T) {
	source := "alpha\nbeta\n"
	var edit lspTextEdit
	edit.Range.Start = LSPPosition{Line: 1, Character: 0}
	edit.Range.End = LSPPosition{Line: 1, Character: 4}
	edit.NewText = "gamma"

	out, err := applyLSPTextEdits(source, []lspTextEdit{edit})
	require.NoError(t, err)
	require.Equal(t, "alpha\ngamma\n", out)
}

func TestNormalizeLSPActionAliases(t *testing.T) {
	cases := map[string]string{
		"goto_definition":  "definition",
		"find_references":  "references",
		"completions":      "completion",
		"document_symbols": "symbols",
		"formatting":       "format",
	}
	for input, expected := range cases {
		actual, err := NormalizeLSPAction(input)
		require.NoError(t, err)
		require.Equal(t, expected, actual)
	}
	_, err := NormalizeLSPAction("unknown")
	require.Error(t, err)
}

func TestFakeLSPServer(t *testing.T) {
	if os.Getenv("CODOG_FAKE_LSP") != "1" {
		return
	}
	defer os.Exit(0)
	reader := bufio.NewReader(os.Stdin)
	for {
		raw, err := readLSPMessage(reader)
		if err != nil {
			return
		}
		var msg lspRPCMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			return
		}
		if msg.Method == "exit" {
			return
		}
		if msg.ID == nil {
			if msg.Method == "textDocument/didOpen" {
				var params struct {
					TextDocument struct {
						URI string `json:"uri"`
					} `json:"textDocument"`
				}
				_ = decodeLSPParams(msg.Params, &params)
				_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", Method: "textDocument/publishDiagnostics", Params: map[string]any{
					"uri": params.TextDocument.URI,
					"diagnostics": []map[string]any{{
						"range": map[string]any{
							"start": map[string]any{"line": 2, "character": 5},
							"end":   map[string]any{"line": 2, "character": 9},
						},
						"severity": 1,
						"source":   "fake-lsp",
						"message":  "fake diagnostic",
					}},
				}})
			}
			continue
		}
		switch msg.Method {
		case "initialize":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{"capabilities": map[string]any{}})})
		case "textDocument/hover":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{"contents": map[string]any{"kind": "markdown", "value": "fake hover"}})})
		case "textDocument/formatting":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 14},
				},
				"newText": "func main() {}\n",
			}})})
		case "shutdown":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(nil)})
		default:
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(nil)})
		}
	}
}

func mustRawJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
