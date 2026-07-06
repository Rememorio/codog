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
	require.Equal(t, "notebook_edit", result.Kind)
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

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "range_format", Path: "main.go", Line: 2, Character: 14})
	require.NoError(t, err)
	require.Equal(t, "range-format", result.Action)
	require.Equal(t, "textDocument/rangeFormatting", result.Method)
	require.Equal(t, 1, result.TextEdits)
	require.True(t, result.Changed)
	require.Contains(t, result.Content, "func rangeFormatted()")

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "on_type_format", Path: "main.go", Line: 2, Character: 14, Query: "}"})
	require.NoError(t, err)
	require.Equal(t, "on-type-format", result.Action)
	require.Equal(t, "textDocument/onTypeFormatting", result.Method)
	require.Equal(t, 1, result.TextEdits)
	require.True(t, result.Changed)
	require.Contains(t, result.Content, "func onTypeFormatted()")

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "will_save", Path: "main.go"})
	require.NoError(t, err)
	require.Equal(t, "will-save", result.Action)
	require.Equal(t, "textDocument/willSaveWaitUntil", result.Method)
	require.Equal(t, 1, result.TextEdits)
	require.True(t, result.Changed)
	require.Contains(t, result.Content, "func saved()")

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "implementation", Path: "main.go", Line: 2, Character: 5})
	require.NoError(t, err)
	require.Equal(t, "implementation", result.Action)
	require.Equal(t, "textDocument/implementation", result.Method)
	require.NotNil(t, result.Result)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "typeDefinition", Path: "main.go", Line: 2, Character: 5})
	require.NoError(t, err)
	require.Equal(t, "type-definition", result.Action)
	require.Equal(t, "textDocument/typeDefinition", result.Method)
	require.NotNil(t, result.Result)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "signature_help", Path: "main.go", Line: 2, Character: 5})
	require.NoError(t, err)
	require.Equal(t, "signature-help", result.Action)
	require.Equal(t, "textDocument/signatureHelp", result.Method)
	require.NotNil(t, result.Result)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "selection_range", Path: "main.go", Line: 2, Character: 5})
	require.NoError(t, err)
	require.Equal(t, "selection-range", result.Action)
	require.Equal(t, "textDocument/selectionRange", result.Method)
	var selectionRanges []struct {
		Range struct {
			Start LSPPosition `json:"start"`
			End   LSPPosition `json:"end"`
		} `json:"range"`
		Parent *struct {
			Range struct {
				Start LSPPosition `json:"start"`
				End   LSPPosition `json:"end"`
			} `json:"range"`
		} `json:"parent"`
	}
	encodedSelection, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedSelection, &selectionRanges))
	require.Len(t, selectionRanges, 1)
	require.Equal(t, LSPPosition{Line: 2, Character: 5}, selectionRanges[0].Range.Start)
	require.NotNil(t, selectionRanges[0].Parent)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "folding_range", Path: "main.go"})
	require.NoError(t, err)
	require.Equal(t, "folding-range", result.Action)
	require.Equal(t, "textDocument/foldingRange", result.Method)
	var foldingRanges []struct {
		StartLine int    `json:"startLine"`
		EndLine   int    `json:"endLine"`
		Kind      string `json:"kind"`
	}
	encodedFolding, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedFolding, &foldingRanges))
	require.Len(t, foldingRanges, 1)
	require.Equal(t, 2, foldingRanges[0].StartLine)
	require.Equal(t, 2, foldingRanges[0].EndLine)
	require.Equal(t, "region", foldingRanges[0].Kind)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "document_link", Path: "main.go"})
	require.NoError(t, err)
	require.Equal(t, "document-link", result.Action)
	require.Equal(t, "textDocument/documentLink", result.Method)
	var documentLinks []struct {
		Target string `json:"target"`
		Range  struct {
			Start LSPPosition `json:"start"`
			End   LSPPosition `json:"end"`
		} `json:"range"`
	}
	encodedLinks, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedLinks, &documentLinks))
	require.Len(t, documentLinks, 1)
	require.Equal(t, "https://example.test/docs", documentLinks[0].Target)
	require.Equal(t, LSPPosition{Line: 2, Character: 0}, documentLinks[0].Range.Start)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "document_color", Path: "main.go"})
	require.NoError(t, err)
	require.Equal(t, "document-color", result.Action)
	require.Equal(t, "textDocument/documentColor", result.Method)
	var documentColors []struct {
		Color struct {
			Red   float64 `json:"red"`
			Green float64 `json:"green"`
			Blue  float64 `json:"blue"`
			Alpha float64 `json:"alpha"`
		} `json:"color"`
		Range struct {
			Start LSPPosition `json:"start"`
			End   LSPPosition `json:"end"`
		} `json:"range"`
	}
	encodedColors, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedColors, &documentColors))
	require.Len(t, documentColors, 1)
	require.Equal(t, 1.0, documentColors[0].Color.Red)
	require.Equal(t, 0.5, documentColors[0].Color.Green)
	require.Equal(t, LSPPosition{Line: 2, Character: 1}, documentColors[0].Range.Start)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "color_presentation", Path: "main.go", Line: 2, Character: 3})
	require.NoError(t, err)
	require.Equal(t, "color-presentation", result.Action)
	require.Equal(t, "textDocument/colorPresentation", result.Method)
	var colorPresentation struct {
		Selected      lspColorInformation `json:"selected"`
		Presentations []struct {
			Label string `json:"label"`
		} `json:"presentations"`
	}
	encodedColorPresentation, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedColorPresentation, &colorPresentation))
	require.Equal(t, LSPPosition{Line: 2, Character: 1}, colorPresentation.Selected.Range.Start)
	require.Len(t, colorPresentation.Presentations, 1)
	require.Equal(t, "#ff8040", colorPresentation.Presentations[0].Label)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "code_lens", Path: "main.go"})
	require.NoError(t, err)
	require.Equal(t, "code-lens", result.Action)
	require.Equal(t, "textDocument/codeLens", result.Method)
	var codeLenses []struct {
		Command struct {
			Title   string `json:"title"`
			Command string `json:"command"`
		} `json:"command"`
		Range lspRange `json:"range"`
	}
	encodedCodeLenses, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedCodeLenses, &codeLenses))
	require.Len(t, codeLenses, 1)
	require.Equal(t, "Run test", codeLenses[0].Command.Title)
	require.Equal(t, LSPPosition{Line: 2, Character: 0}, codeLenses[0].Range.Start)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "code_lens_resolve", Path: "main.go", Line: 2, Character: 3})
	require.NoError(t, err)
	require.Equal(t, "code-lens-resolve", result.Action)
	require.Equal(t, "codeLens/resolve", result.Method)
	var resolvedLens struct {
		Resolved struct {
			Command struct {
				Title string `json:"title"`
			} `json:"command"`
		} `json:"resolved"`
	}
	encodedResolvedLens, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedResolvedLens, &resolvedLens))
	require.Equal(t, "Run test (resolved)", resolvedLens.Resolved.Command.Title)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "inlay_hint", Path: "main.go", Line: 2, Character: 12})
	require.NoError(t, err)
	require.Equal(t, "inlay-hint", result.Action)
	require.Equal(t, "textDocument/inlayHint", result.Method)
	var inlayHints []struct {
		Label    any         `json:"label"`
		Kind     int         `json:"kind"`
		Position LSPPosition `json:"position"`
	}
	encodedHints, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedHints, &inlayHints))
	require.Len(t, inlayHints, 1)
	require.Equal(t, ": int", inlayHints[0].Label)
	require.Equal(t, 1, inlayHints[0].Kind)
	require.Equal(t, LSPPosition{Line: 2, Character: 10}, inlayHints[0].Position)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "linked_editing_range", Path: "main.go", Line: 2, Character: 5})
	require.NoError(t, err)
	require.Equal(t, "linked-editing-range", result.Action)
	require.Equal(t, "textDocument/linkedEditingRange", result.Method)
	var linkedEditing struct {
		WordPattern string `json:"wordPattern"`
		Ranges      []struct {
			Start LSPPosition `json:"start"`
			End   LSPPosition `json:"end"`
		} `json:"ranges"`
	}
	encodedLinked, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedLinked, &linkedEditing))
	require.Equal(t, "[A-Za-z_]+", linkedEditing.WordPattern)
	require.Len(t, linkedEditing.Ranges, 2)
	require.Equal(t, LSPPosition{Line: 2, Character: 5}, linkedEditing.Ranges[0].Start)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "moniker", Path: "main.go", Line: 2, Character: 5})
	require.NoError(t, err)
	require.Equal(t, "moniker", result.Action)
	require.Equal(t, "textDocument/moniker", result.Method)
	var monikers []struct {
		Scheme     string `json:"scheme"`
		Identifier string `json:"identifier"`
		Kind       string `json:"kind"`
	}
	encodedMonikers, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedMonikers, &monikers))
	require.Len(t, monikers, 1)
	require.Equal(t, "gomod", monikers[0].Scheme)
	require.Equal(t, "example.test/demo.BuildWidget", monikers[0].Identifier)
	require.Equal(t, "export", monikers[0].Kind)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "semantic_tokens", Path: "main.go"})
	require.NoError(t, err)
	require.Equal(t, "semantic-tokens", result.Action)
	require.Equal(t, "textDocument/semanticTokens/full", result.Method)
	var semanticTokens struct {
		ResultID string `json:"resultId"`
		Data     []int  `json:"data"`
	}
	encodedSemanticTokens, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedSemanticTokens, &semanticTokens))
	require.Equal(t, "full-1", semanticTokens.ResultID)
	require.Equal(t, []int{2, 5, 4, 12, 0}, semanticTokens.Data)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "semantic_tokens_range", Path: "main.go", Line: 2, Character: 12})
	require.NoError(t, err)
	require.Equal(t, "semantic-tokens-range", result.Action)
	require.Equal(t, "textDocument/semanticTokens/range", result.Method)
	var semanticTokenRange struct {
		Data []int `json:"data"`
	}
	encodedSemanticTokenRange, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedSemanticTokenRange, &semanticTokenRange))
	require.Equal(t, []int{0, 5, 6, 12, 0}, semanticTokenRange.Data)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "workspace_symbol", Query: "Build"})
	require.NoError(t, err)
	require.Equal(t, "workspace-symbol", result.Action)
	require.Equal(t, "workspace/symbol", result.Method)
	require.Empty(t, result.Path)
	var workspaceSymbols []struct {
		Name     string `json:"name"`
		Kind     int    `json:"kind"`
		Location struct {
			URI   string `json:"uri"`
			Range struct {
				Start LSPPosition `json:"start"`
				End   LSPPosition `json:"end"`
			} `json:"range"`
		} `json:"location"`
	}
	encodedWorkspaceSymbols, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedWorkspaceSymbols, &workspaceSymbols))
	require.Len(t, workspaceSymbols, 1)
	require.Equal(t, "BuildWidget", workspaceSymbols[0].Name)
	require.Equal(t, 12, workspaceSymbols[0].Kind)
	require.Equal(t, "file:///workspace/main.go", workspaceSymbols[0].Location.URI)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "rename", Path: "main.go", Line: 2, Character: 5, NewName: "Start"})
	require.NoError(t, err)
	require.Equal(t, "rename", result.Action)
	require.Equal(t, "textDocument/rename", result.Method)
	require.Equal(t, 1, result.FileEdits)
	require.Equal(t, 1, result.TextEdits)
	require.True(t, result.Changed)
	require.Len(t, result.Edits, 1)
	require.Equal(t, "main.go", result.Edits[0].Path)
	require.Contains(t, result.Edits[0].Content, "func Start()")
	data, err := os.ReadFile(filepath.Join(workspace, "main.go"))
	require.NoError(t, err)
	require.Contains(t, string(data), "func main()")

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "rename", Path: "main.go", Line: 2, Character: 5, NewName: "Start", Apply: true})
	require.NoError(t, err)
	require.Equal(t, "rename", result.Action)
	require.True(t, result.Applied)
	require.True(t, result.Changed)
	data, err = os.ReadFile(filepath.Join(workspace, "main.go"))
	require.NoError(t, err)
	require.Contains(t, string(data), "func Start()")

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "prepare_rename", Path: "main.go", Line: 2, Character: 5})
	require.NoError(t, err)
	require.Equal(t, "prepare-rename", result.Action)
	require.Equal(t, "textDocument/prepareRename", result.Method)
	var prepareRename struct {
		Placeholder string `json:"placeholder"`
		Range       struct {
			Start LSPPosition `json:"start"`
			End   LSPPosition `json:"end"`
		} `json:"range"`
	}
	encodedPrepare, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedPrepare, &prepareRename))
	require.Equal(t, "Start", prepareRename.Placeholder)
	require.Equal(t, LSPPosition{Line: 2, Character: 5}, prepareRename.Range.Start)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "code_action", Path: "main.go", Line: 2, Character: 5})
	require.NoError(t, err)
	require.Equal(t, "code-action", result.Action)
	require.Equal(t, "textDocument/codeAction", result.Method)
	require.NotNil(t, result.Result)
	var codeActions []struct {
		Title string `json:"title"`
		Kind  string `json:"kind"`
	}
	encoded, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encoded, &codeActions))
	require.Len(t, codeActions, 1)
	require.Equal(t, "Apply fake fix", codeActions[0].Title)
	require.Equal(t, "quickfix", codeActions[0].Kind)
	require.Equal(t, 1, result.FileEdits)
	require.Equal(t, 1, result.TextEdits)
	require.True(t, result.Changed)
	require.Len(t, result.Edits, 1)
	require.Equal(t, "Apply fake fix", result.Edits[0].ActionTitle)
	require.Equal(t, "quickfix", result.Edits[0].ActionKind)
	require.Equal(t, "main.go", result.Edits[0].Path)
	require.Contains(t, result.Edits[0].Content, "func Launch()")
	data, err = os.ReadFile(filepath.Join(workspace, "main.go"))
	require.NoError(t, err)
	require.Contains(t, string(data), "func Start()")

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "code_action", Path: "main.go", Line: 2, Character: 5, CodeActionTitle: "Apply fake fix", Apply: true})
	require.NoError(t, err)
	require.Equal(t, "code-action", result.Action)
	require.True(t, result.Applied)
	require.Equal(t, 1, result.FileEdits)
	data, err = os.ReadFile(filepath.Join(workspace, "main.go"))
	require.NoError(t, err)
	require.Contains(t, string(data), "func Launch()")

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "prepare_call_hierarchy", Path: "main.go", Line: 2, Character: 5})
	require.NoError(t, err)
	require.Equal(t, "prepare-call-hierarchy", result.Action)
	require.Equal(t, "textDocument/prepareCallHierarchy", result.Method)
	var callItems []struct {
		Name string `json:"name"`
		Kind int    `json:"kind"`
		URI  string `json:"uri"`
	}
	encodedCallItems, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedCallItems, &callItems))
	require.Len(t, callItems, 1)
	require.Equal(t, "BuildWidget", callItems[0].Name)
	require.Equal(t, 12, callItems[0].Kind)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "incoming_calls", Path: "main.go", Line: 2, Character: 5})
	require.NoError(t, err)
	require.Equal(t, "call-hierarchy-incoming", result.Action)
	require.Equal(t, "callHierarchy/incomingCalls", result.Method)
	var incomingCalls struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
		Calls []struct {
			From struct {
				Name string `json:"name"`
			} `json:"from"`
		} `json:"calls"`
	}
	encodedIncoming, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedIncoming, &incomingCalls))
	require.Len(t, incomingCalls.Items, 1)
	require.Len(t, incomingCalls.Calls, 1)
	require.Equal(t, "TestCaller", incomingCalls.Calls[0].From.Name)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "outgoing_calls", Path: "main.go", Line: 2, Character: 5})
	require.NoError(t, err)
	require.Equal(t, "call-hierarchy-outgoing", result.Action)
	require.Equal(t, "callHierarchy/outgoingCalls", result.Method)
	var outgoingCalls struct {
		Calls []struct {
			To struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"calls"`
	}
	encodedOutgoing, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedOutgoing, &outgoingCalls))
	require.Len(t, outgoingCalls.Calls, 1)
	require.Equal(t, "fmt.Println", outgoingCalls.Calls[0].To.Name)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "prepare_type_hierarchy", Path: "main.go", Line: 2, Character: 5})
	require.NoError(t, err)
	require.Equal(t, "prepare-type-hierarchy", result.Action)
	require.Equal(t, "textDocument/prepareTypeHierarchy", result.Method)
	var typeItems []struct {
		Name string `json:"name"`
		Kind int    `json:"kind"`
		URI  string `json:"uri"`
	}
	encodedTypeItems, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedTypeItems, &typeItems))
	require.Len(t, typeItems, 1)
	require.Equal(t, "Widget", typeItems[0].Name)
	require.Equal(t, 23, typeItems[0].Kind)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "supertypes", Path: "main.go", Line: 2, Character: 5})
	require.NoError(t, err)
	require.Equal(t, "type-hierarchy-supertypes", result.Action)
	require.Equal(t, "typeHierarchy/supertypes", result.Method)
	var supertypes struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
		Types []struct {
			Name string `json:"name"`
		} `json:"types"`
	}
	encodedSupertypes, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedSupertypes, &supertypes))
	require.Len(t, supertypes.Items, 1)
	require.Len(t, supertypes.Types, 1)
	require.Equal(t, "BaseWidget", supertypes.Types[0].Name)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "subtypes", Path: "main.go", Line: 2, Character: 5})
	require.NoError(t, err)
	require.Equal(t, "type-hierarchy-subtypes", result.Action)
	require.Equal(t, "typeHierarchy/subtypes", result.Method)
	var subtypes struct {
		Types []struct {
			Name string `json:"name"`
		} `json:"types"`
	}
	encodedSubtypes, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedSubtypes, &subtypes))
	require.Len(t, subtypes.Types, 1)
	require.Equal(t, "SpecialWidget", subtypes.Types[0].Name)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "document_diagnostic", Path: "main.go"})
	require.NoError(t, err)
	require.Equal(t, "document-diagnostic", result.Action)
	require.Equal(t, "textDocument/diagnostic", result.Method)
	var documentDiagnostic struct {
		Kind  string `json:"kind"`
		Items []struct {
			Message string `json:"message"`
		} `json:"items"`
	}
	encodedDocumentDiagnostic, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedDocumentDiagnostic, &documentDiagnostic))
	require.Equal(t, "full", documentDiagnostic.Kind)
	require.Len(t, documentDiagnostic.Items, 1)
	require.Equal(t, "pulled document diagnostic", documentDiagnostic.Items[0].Message)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "workspace_diagnostic"})
	require.NoError(t, err)
	require.Equal(t, "workspace-diagnostic", result.Action)
	require.Equal(t, "workspace/diagnostic", result.Method)
	require.Empty(t, result.Path)
	var workspaceDiagnostic struct {
		Items []struct {
			URI   string `json:"uri"`
			Items []struct {
				Message string `json:"message"`
			} `json:"items"`
		} `json:"items"`
	}
	encodedWorkspaceDiagnostic, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedWorkspaceDiagnostic, &workspaceDiagnostic))
	require.Len(t, workspaceDiagnostic.Items, 1)
	require.Equal(t, "pulled workspace diagnostic", workspaceDiagnostic.Items[0].Items[0].Message)

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
		"document_diagnostic":       "document-diagnostic",
		"documentDiagnostic":        "document-diagnostic",
		"pull_diagnostic":           "document-diagnostic",
		"workspace_diagnostic":      "workspace-diagnostic",
		"workspaceDiagnostic":       "workspace-diagnostic",
		"workspace_diagnostics":     "workspace-diagnostic",
		"goto_definition":           "definition",
		"goto-definition":           "definition",
		"gotoDefinition":            "definition",
		"goto_declaration":          "declaration",
		"gotoDeclaration":           "declaration",
		"goto_implementation":       "implementation",
		"gotoImplementation":        "implementation",
		"type_definition":           "type-definition",
		"typeDefinition":            "type-definition",
		"gotoTypeDefinition":        "type-definition",
		"find_references":           "references",
		"find-references":           "references",
		"findReferences":            "references",
		"rename_symbol":             "rename",
		"renameSymbol":              "rename",
		"symbol-rename":             "rename",
		"prepare_rename":            "prepare-rename",
		"prepareRename":             "prepare-rename",
		"rename-prepare":            "prepare-rename",
		"code_action":               "code-action",
		"codeAction":                "code-action",
		"quickfix":                  "code-action",
		"quick-fix":                 "code-action",
		"code_lens":                 "code-lens",
		"codeLens":                  "code-lens",
		"code_lens_resolve":         "code-lens-resolve",
		"codeLensResolve":           "code-lens-resolve",
		"prepare_call_hierarchy":    "prepare-call-hierarchy",
		"prepareCallHierarchy":      "prepare-call-hierarchy",
		"call_hierarchy":            "prepare-call-hierarchy",
		"incoming_calls":            "call-hierarchy-incoming",
		"incomingCalls":             "call-hierarchy-incoming",
		"call_hierarchy_incoming":   "call-hierarchy-incoming",
		"outgoing_calls":            "call-hierarchy-outgoing",
		"outgoingCalls":             "call-hierarchy-outgoing",
		"call_hierarchy_outgoing":   "call-hierarchy-outgoing",
		"prepare_type_hierarchy":    "prepare-type-hierarchy",
		"prepareTypeHierarchy":      "prepare-type-hierarchy",
		"type_hierarchy":            "prepare-type-hierarchy",
		"supertypes":                "type-hierarchy-supertypes",
		"super_types":               "type-hierarchy-supertypes",
		"type_hierarchy_supertypes": "type-hierarchy-supertypes",
		"subtypes":                  "type-hierarchy-subtypes",
		"sub_types":                 "type-hierarchy-subtypes",
		"type_hierarchy_subtypes":   "type-hierarchy-subtypes",
		"completions":               "completion",
		"document_highlight":        "document-highlight",
		"documentHighlight":         "document-highlight",
		"selection_range":           "selection-range",
		"selectionRange":            "selection-range",
		"expand_selection":          "selection-range",
		"folding_range":             "folding-range",
		"foldingRange":              "folding-range",
		"folds":                     "folding-range",
		"document_link":             "document-link",
		"documentLink":              "document-link",
		"document-links":            "document-link",
		"document_color":            "document-color",
		"documentColor":             "document-color",
		"document-colors":           "document-color",
		"color_presentation":        "color-presentation",
		"colorPresentation":         "color-presentation",
		"color-presentations":       "color-presentation",
		"inlay_hint":                "inlay-hint",
		"inlayHint":                 "inlay-hint",
		"inlay-hints":               "inlay-hint",
		"linked_editing_range":      "linked-editing-range",
		"linkedEditingRange":        "linked-editing-range",
		"linked-editing":            "linked-editing-range",
		"monikers":                  "moniker",
		"symbol_moniker":            "moniker",
		"semantic_tokens":           "semantic-tokens",
		"semanticTokens":            "semantic-tokens",
		"semantic_tokens_full":      "semantic-tokens",
		"semanticTokensFull":        "semantic-tokens",
		"semantic_tokens_range":     "semantic-tokens-range",
		"semanticTokensRange":       "semantic-tokens-range",
		"semantic-range":            "semantic-tokens-range",
		"workspace_symbol":          "workspace-symbol",
		"workspace_symbols":         "workspace-symbol",
		"workspaceSymbol":           "workspace-symbol",
		"symbol-search":             "workspace-symbol",
		"signature_help":            "signature-help",
		"signatureHelp":             "signature-help",
		"signature":                 "signature-help",
		"document_symbols":          "symbols",
		"document-symbols":          "symbols",
		"documentSymbols":           "symbols",
		"document-formatting":       "format",
		"documentFormatting":        "format",
		"formatting":                "format",
		"range_format":              "range-format",
		"rangeFormatting":           "range-format",
		"format_range":              "range-format",
		"on_type_format":            "on-type-format",
		"onTypeFormatting":          "on-type-format",
		"format_on_type":            "on-type-format",
		"will_save":                 "will-save",
		"willSave":                  "will-save",
		"will_save_wait_until":      "will-save",
		"willSaveWaitUntil":         "will-save",
		"save_edits":                "will-save",
	}
	for input, expected := range cases {
		actual, err := NormalizeLSPAction(input)
		require.NoError(t, err)
		require.Equal(t, expected, actual)
	}
	_, err := NormalizeLSPAction("unknown")
	require.Error(t, err)
	require.Contains(t, err.Error(), "supported actions")
	require.NotEmpty(t, SupportedLSPActions())
}

func TestFakeLSPServer(t *testing.T) {
	if os.Getenv("CODOG_FAKE_LSP") != "1" {
		return
	}
	defer os.Exit(0)
	reader := bufio.NewReader(os.Stdin)
	currentURI := ""
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
				currentURI = params.TextDocument.URI
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
		case "textDocument/diagnostic":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{
				"kind": "full",
				"items": []map[string]any{{
					"range": map[string]any{
						"start": map[string]any{"line": 2, "character": 5},
						"end":   map[string]any{"line": 2, "character": 9},
					},
					"severity": 2,
					"source":   "fake-lsp",
					"message":  "pulled document diagnostic",
				}},
			})})
		case "workspace/diagnostic":
			if currentURI == "" {
				currentURI = "file:///workspace/main.go"
			}
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{
				"items": []map[string]any{{
					"uri":     currentURI,
					"version": 1,
					"kind":    "full",
					"items": []map[string]any{{
						"range": map[string]any{
							"start": map[string]any{"line": 3, "character": 1},
							"end":   map[string]any{"line": 3, "character": 5},
						},
						"severity": 2,
						"source":   "fake-lsp",
						"message":  "pulled workspace diagnostic",
					}},
				}},
			})})
		case "textDocument/hover":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{"contents": map[string]any{"kind": "markdown", "value": "fake hover"}})})
		case "textDocument/declaration", "textDocument/implementation", "textDocument/typeDefinition":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"uri":   "file:///workspace/main.go",
				"range": map[string]any{"start": map[string]any{"line": 2, "character": 5}, "end": map[string]any{"line": 2, "character": 9}},
			}})})
		case "textDocument/documentHighlight":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"kind":  1,
				"range": map[string]any{"start": map[string]any{"line": 2, "character": 5}, "end": map[string]any{"line": 2, "character": 9}},
			}})})
		case "textDocument/selectionRange":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 5},
					"end":   map[string]any{"line": 2, "character": 10},
				},
				"parent": map[string]any{
					"range": map[string]any{
						"start": map[string]any{"line": 2, "character": 0},
						"end":   map[string]any{"line": 2, "character": 15},
					},
				},
			}})})
		case "textDocument/foldingRange":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"startLine": 2,
				"endLine":   2,
				"kind":      "region",
			}})})
		case "textDocument/documentLink":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 12},
				},
				"target": "https://example.test/docs",
			}})})
		case "textDocument/documentColor":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 1},
					"end":   map[string]any{"line": 2, "character": 8},
				},
				"color": map[string]any{"red": 1.0, "green": 0.5, "blue": 0.25, "alpha": 1.0},
			}})})
		case "textDocument/colorPresentation":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"label": "#ff8040",
				"textEdit": map[string]any{
					"range": map[string]any{
						"start": map[string]any{"line": 2, "character": 1},
						"end":   map[string]any{"line": 2, "character": 8},
					},
					"newText": "#ff8040",
				},
			}})})
		case "textDocument/codeLens":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 20},
				},
				"command": map[string]any{"title": "Run test", "command": "go.test"},
				"data":    map[string]any{"id": "lens-1"},
			}})})
		case "codeLens/resolve":
			var lens map[string]any
			_ = decodeLSPParams(msg.Params, &lens)
			lens["command"] = map[string]any{"title": "Run test (resolved)", "command": "go.test", "arguments": []string{"./..."}}
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(lens)})
		case "textDocument/inlayHint":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"position": map[string]any{"line": 2, "character": 10},
				"label":    ": int",
				"kind":     1,
			}})})
		case "textDocument/linkedEditingRange":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{
				"ranges": []map[string]any{
					{"start": map[string]any{"line": 2, "character": 5}, "end": map[string]any{"line": 2, "character": 10}},
					{"start": map[string]any{"line": 4, "character": 5}, "end": map[string]any{"line": 4, "character": 10}},
				},
				"wordPattern": "[A-Za-z_]+",
			})})
		case "textDocument/moniker":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"scheme":     "gomod",
				"identifier": "example.test/demo.BuildWidget",
				"kind":       "export",
			}})})
		case "textDocument/semanticTokens/full":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{
				"resultId": "full-1",
				"data":     []int{2, 5, 4, 12, 0},
			})})
		case "textDocument/semanticTokens/range":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{
				"data": []int{0, 5, 6, 12, 0},
			})})
		case "workspace/symbol":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"name": "BuildWidget",
				"kind": 12,
				"location": map[string]any{
					"uri": "file:///workspace/main.go",
					"range": map[string]any{
						"start": map[string]any{"line": 2, "character": 5},
						"end":   map[string]any{"line": 2, "character": 16},
					},
				},
			}})})
		case "textDocument/signatureHelp":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{
				"activeSignature": 0,
				"signatures": []map[string]any{{
					"label": "main()",
				}},
			})})
		case "textDocument/rename":
			if currentURI == "" {
				currentURI = "file:///workspace/main.go"
			}
			var params struct {
				NewName string `json:"newName"`
			}
			_ = decodeLSPParams(msg.Params, &params)
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{
				"changes": map[string]any{
					currentURI: []map[string]any{{
						"range": map[string]any{
							"start": map[string]any{"line": 2, "character": 5},
							"end":   map[string]any{"line": 2, "character": 9},
						},
						"newText": params.NewName,
					}},
				},
			})})
		case "textDocument/prepareRename":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 5},
					"end":   map[string]any{"line": 2, "character": 10},
				},
				"placeholder": "Start",
			})})
		case "textDocument/codeAction":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"title": "Apply fake fix",
				"kind":  "quickfix",
				"edit": map[string]any{
					"changes": map[string]any{
						currentURI: []map[string]any{{
							"range": map[string]any{
								"start": map[string]any{"line": 2, "character": 5},
								"end":   map[string]any{"line": 2, "character": 10},
							},
							"newText": "Launch",
						}},
					},
				},
			}})})
		case "textDocument/prepareCallHierarchy":
			if currentURI == "" {
				currentURI = "file:///workspace/main.go"
			}
			callItem := map[string]any{
				"name": "BuildWidget",
				"kind": 12,
				"uri":  currentURI,
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 20},
				},
				"selectionRange": map[string]any{
					"start": map[string]any{"line": 2, "character": 5},
					"end":   map[string]any{"line": 2, "character": 16},
				},
			}
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{callItem})})
		case "callHierarchy/incomingCalls":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"from": map[string]any{
					"name": "TestCaller",
					"kind": 12,
					"uri":  currentURI,
					"range": map[string]any{
						"start": map[string]any{"line": 8, "character": 0},
						"end":   map[string]any{"line": 10, "character": 1},
					},
					"selectionRange": map[string]any{
						"start": map[string]any{"line": 8, "character": 5},
						"end":   map[string]any{"line": 8, "character": 15},
					},
				},
				"fromRanges": []map[string]any{{
					"start": map[string]any{"line": 9, "character": 1},
					"end":   map[string]any{"line": 9, "character": 12},
				}},
			}})})
		case "callHierarchy/outgoingCalls":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"to": map[string]any{
					"name": "fmt.Println",
					"kind": 12,
					"uri":  currentURI,
					"range": map[string]any{
						"start": map[string]any{"line": 4, "character": 0},
						"end":   map[string]any{"line": 4, "character": 20},
					},
					"selectionRange": map[string]any{
						"start": map[string]any{"line": 4, "character": 1},
						"end":   map[string]any{"line": 4, "character": 12},
					},
				},
				"fromRanges": []map[string]any{{
					"start": map[string]any{"line": 3, "character": 1},
					"end":   map[string]any{"line": 3, "character": 12},
				}},
			}})})
		case "textDocument/prepareTypeHierarchy":
			if currentURI == "" {
				currentURI = "file:///workspace/main.go"
			}
			typeItem := map[string]any{
				"name": "Widget",
				"kind": 23,
				"uri":  currentURI,
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 20},
				},
				"selectionRange": map[string]any{
					"start": map[string]any{"line": 2, "character": 5},
					"end":   map[string]any{"line": 2, "character": 11},
				},
			}
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{typeItem})})
		case "typeHierarchy/supertypes":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"name": "BaseWidget",
				"kind": 23,
				"uri":  currentURI,
				"range": map[string]any{
					"start": map[string]any{"line": 1, "character": 0},
					"end":   map[string]any{"line": 1, "character": 20},
				},
				"selectionRange": map[string]any{
					"start": map[string]any{"line": 1, "character": 5},
					"end":   map[string]any{"line": 1, "character": 15},
				},
			}})})
		case "typeHierarchy/subtypes":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"name": "SpecialWidget",
				"kind": 23,
				"uri":  currentURI,
				"range": map[string]any{
					"start": map[string]any{"line": 5, "character": 0},
					"end":   map[string]any{"line": 5, "character": 20},
				},
				"selectionRange": map[string]any{
					"start": map[string]any{"line": 5, "character": 5},
					"end":   map[string]any{"line": 5, "character": 18},
				},
			}})})
		case "textDocument/formatting":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 14},
				},
				"newText": "func main() {}\n",
			}})})
		case "textDocument/rangeFormatting":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 14},
				},
				"newText": "func rangeFormatted() {}\n",
			}})})
		case "textDocument/onTypeFormatting":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 14},
				},
				"newText": "func onTypeFormatted() {}\n",
			}})})
		case "textDocument/willSaveWaitUntil":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 14},
				},
				"newText": "func saved() {}\n",
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
