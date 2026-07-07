package codeintel

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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

func TestWorkspaceSymbolsFiltersByQueryAndLimit(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "pkg"), 0o755))
	source := strings.Join([]string{
		"package pkg",
		"",
		"type Runner struct{}",
		"type Helper struct{}",
		"func RunFast() Runner { return Runner{} }",
		"func RunSlow() Runner { return Runner{} }",
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "runner.go"), []byte(source), 0o644))

	symbols, err := WorkspaceSymbols(workspace, "run", 2)
	require.NoError(t, err)
	require.Len(t, symbols, 2)
	require.Equal(t, "Runner", symbols[0].Name)
	require.Equal(t, "pkg/runner.go", symbols[0].Path)
	require.Equal(t, "RunFast", symbols[1].Name)

	all, err := WorkspaceSymbols(workspace, "", 0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(all), 4)

	resolved, found, err := ResolveWorkspaceSymbol(workspace, "runfast")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "RunFast", resolved.Symbol.Name)
	require.Equal(t, "function", resolved.Symbol.Kind)
	require.Equal(t, "pkg/runner.go", resolved.Symbol.Path)
	require.True(t, resolved.Hover.Found)
	require.Equal(t, "RunFast", resolved.Hover.Symbol)

	_, found, err = ResolveWorkspaceSymbol(workspace, "MissingSymbol")
	require.NoError(t, err)
	require.False(t, found)
}

func TestDefinitionReferencesHoverAndCodeMap(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/codeintel\n\ngo 1.26\n"), 0o644))
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

	highlights, err := DocumentHighlights(workspace, "Runner", "pkg/runner.go", 10)
	require.NoError(t, err)
	require.Len(t, highlights, 3)
	require.Equal(t, "pkg/runner.go", highlights[0].Path)
	require.Equal(t, LSPPosition{Line: 2, Character: 5}, highlights[0].Range.Start)
	require.Equal(t, LSPPosition{Line: 2, Character: 11}, highlights[0].Range.End)
	require.Equal(t, 1, highlights[0].Kind)

	foldSource := "package pkg\n\nfunc FoldOnly() {\n\tprintln(\"fold\")\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "fold.go"), []byte(foldSource), 0o644))
	folds, err := FoldingRanges(workspace, "pkg/fold.go", 10)
	require.NoError(t, err)
	require.Len(t, folds, 1)
	require.Equal(t, "pkg/fold.go", folds[0].Path)
	require.Equal(t, 2, folds[0].StartLine)
	require.Equal(t, 4, folds[0].EndLine)
	require.Equal(t, "region", folds[0].Kind)

	selections, err := SelectionRanges(workspace, "pkg/runner.go", 4, 6, 10)
	require.NoError(t, err)
	require.NotEmpty(t, selections)
	require.Equal(t, "pkg/runner.go", selections[0].Path)
	require.Equal(t, LSPPosition{Line: 4, Character: 5}, selections[0].Range.Start)
	require.Equal(t, LSPPosition{Line: 4, Character: 8}, selections[0].Range.End)
	require.Equal(t, "Ident", selections[0].Kind)

	monikers, err := Monikers(workspace, "Runner")
	require.NoError(t, err)
	require.Len(t, monikers, 1)
	require.Equal(t, "gomod", monikers[0].Scheme)
	require.Equal(t, "example.test/codeintel/pkg.Runner", monikers[0].Identifier)
	require.Equal(t, "export", monikers[0].Kind)
	require.Equal(t, "project", monikers[0].Unique)

	linked, err := LinkedEditingRanges(workspace, "Runner", "pkg/runner.go", 10)
	require.NoError(t, err)
	require.Equal(t, "pkg/runner.go", linked.Path)
	require.Len(t, linked.Ranges, 3)
	require.Equal(t, LSPPosition{Line: 2, Character: 5}, linked.Ranges[0].Start)
	require.Equal(t, `[A-Za-z_][A-Za-z0-9_]*`, linked.WordPattern)

	linkSource := "package pkg\n\n// Docs: https://example.test/docs.\nconst Link = \"https://example.test/api\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "links.go"), []byte(linkSource), 0o644))
	links, err := DocumentLinks(workspace, "pkg/links.go", 10)
	require.NoError(t, err)
	require.Len(t, links, 2)
	require.Equal(t, "pkg/links.go", links[0].Path)
	require.Equal(t, "https://example.test/docs", links[0].Target)
	require.Equal(t, LSPPosition{Line: 2, Character: 9}, links[0].Range.Start)
	require.Equal(t, LSPPosition{Line: 2, Character: 34}, links[0].Range.End)
	require.Equal(t, "https://example.test/docs", links[0].Tooltip)

	resolvedLink, found, err := DocumentLinkAtPosition(workspace, "pkg/links.go", 2, 12)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, links[0], resolvedLink)

	_, found, err = DocumentLinkAtPosition(workspace, "pkg/links.go", 2, 34)
	require.NoError(t, err)
	require.False(t, found)

	colorSource := "package pkg\n\nconst Accent = \"#336699\"\nconst Short = \"#abc\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "colors.go"), []byte(colorSource), 0o644))
	colors, err := DocumentColors(workspace, "pkg/colors.go", 10)
	require.NoError(t, err)
	require.Len(t, colors, 2)
	require.Equal(t, "pkg/colors.go", colors[0].Path)
	require.Equal(t, "#336699", colors[0].Text)
	require.Equal(t, LSPPosition{Line: 2, Character: 16}, colors[0].Range.Start)
	require.Equal(t, LSPPosition{Line: 2, Character: 23}, colors[0].Range.End)
	require.InDelta(t, 0.2, colors[0].Color.Red, 0.001)
	require.InDelta(t, 0.4, colors[0].Color.Green, 0.001)
	require.InDelta(t, 0.6, colors[0].Color.Blue, 0.001)
	require.Equal(t, 1.0, colors[0].Color.Alpha)

	presentations, found, err := ColorPresentations(workspace, "pkg/colors.go", 2, 18)
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, presentations, 2)
	require.Equal(t, "#336699", presentations[0].Label)
	require.Equal(t, "rgb(51, 102, 153)", presentations[1].Label)

	_, found, err = ColorPresentations(workspace, "pkg/colors.go", 2, 23)
	require.NoError(t, err)
	require.False(t, found)

	hintSource := "package pkg\n\nfunc Build(name string, count int) int { return count }\nfunc UseBuild() { _ = Build(\"codog\", 2) }\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "hints.go"), []byte(hintSource), 0o644))
	hierarchySource := strings.Join([]string{
		"package pkg",
		"",
		"type RunnerBase struct{}",
		"type RunnerChild struct{ RunnerBase }",
		"type RunnerContract interface {",
		"\tExecute()",
		"}",
		"func (RunnerChild) Execute() {}",
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "hierarchy.go"), []byte(hierarchySource), 0o644))
	hints, err := InlayHints(workspace, "pkg/hints.go", 10)
	require.NoError(t, err)
	require.Len(t, hints, 2)
	hintArgChar := strings.Index(strings.Split(hintSource, "\n")[3], `"codog"`)
	require.Equal(t, "pkg/hints.go", hints[0].Path)
	require.Equal(t, LSPPosition{Line: 3, Character: hintArgChar}, hints[0].Position)
	require.Equal(t, "name:", hints[0].Label)
	require.Equal(t, "parameter", hints[0].Kind)
	require.Equal(t, "Build parameter 1", hints[0].Tooltip)
	require.True(t, hints[0].PaddingRight)

	resolvedHint, found, err := InlayHintAtPosition(workspace, "pkg/hints.go", 3, hintArgChar)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, hints[0], resolvedHint)

	inlineSource := "package pkg\n\nconst InlineAnswer = 42\n\nfunc InlineValuesDemo() {\n\tlocal := \"codog\"\n\t_ = local\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "inline.go"), []byte(inlineSource), 0o644))
	inlineValues, err := InlineValues(workspace, "pkg/inline.go", 0, 0, 10)
	require.NoError(t, err)
	require.Contains(t, inlineValues, InlineValue{
		Path:  "pkg/inline.go",
		Name:  "InlineAnswer",
		Value: "42",
		Text:  "InlineAnswer = 42",
		Kind:  "const",
		Range: LSPRange{Start: LSPPosition{Line: 2, Character: 6}, End: LSPPosition{Line: 2, Character: 18}},
	})
	require.Contains(t, inlineValues, InlineValue{
		Path:  "pkg/inline.go",
		Name:  "local",
		Value: "\"codog\"",
		Text:  "local = \"codog\"",
		Kind:  "assignment",
		Range: LSPRange{Start: LSPPosition{Line: 5, Character: 1}, End: LSPPosition{Line: 5, Character: 6}},
	})

	_, found, err = InlayHintAtPosition(workspace, "pkg/hints.go", 3, hintArgChar+1)
	require.NoError(t, err)
	require.False(t, found)

	signature, err := SignatureHelpAtPosition(workspace, "pkg/hints.go", 3, hintArgChar)
	require.NoError(t, err)
	require.True(t, signature.Found)
	require.Equal(t, "pkg/hints.go", signature.Path)
	require.Equal(t, "Build", signature.Function)
	require.Equal(t, 0, signature.ActiveSignature)
	require.Equal(t, 0, signature.ActiveParameter)
	require.Len(t, signature.Signatures, 1)
	require.Equal(t, "Build(name string, count int) int", signature.Signatures[0].Label)
	require.Equal(t, "func Build(name string, count int) int", signature.Signatures[0].Documentation)
	require.Equal(t, []SignatureArgument{{Label: "name string", Name: "name", Type: "string"}, {Label: "count int", Name: "count", Type: "int"}}, signature.Parameters)

	secondArgChar := strings.Index(strings.Split(hintSource, "\n")[3], "2)")
	signature, err = SignatureHelpAtPosition(workspace, "pkg/hints.go", 3, secondArgChar)
	require.NoError(t, err)
	require.True(t, signature.Found)
	require.Equal(t, 1, signature.ActiveParameter)

	lenses, err := CodeLenses(workspace, "pkg/runner.go", 10)
	require.NoError(t, err)
	require.Len(t, lenses, 2)
	require.Equal(t, "pkg/runner.go", lenses[0].Path)
	require.Equal(t, "Runner", lenses[0].Symbol)
	require.Equal(t, "type", lenses[0].Kind)
	require.Equal(t, "codog.references", lenses[0].Command.Command)
	require.Contains(t, lenses[0].Command.Title, "references")
	require.Equal(t, []any{"Runner", "pkg/runner.go"}, lenses[0].Command.Arguments)
	require.Equal(t, LSPPosition{Line: 2, Character: 5}, lenses[0].Range.Start)

	resolvedLens, found, err := CodeLensAtPosition(workspace, "pkg/runner.go", 2, 6)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, lenses[0], resolvedLens)

	_, found, err = CodeLensAtPosition(workspace, "pkg/runner.go", 1, 0)
	require.NoError(t, err)
	require.False(t, found)

	semanticTokens, err := SemanticTokensForDocument(workspace, "pkg/runner.go", 100)
	require.NoError(t, err)
	require.Equal(t, "pkg/runner.go", semanticTokens.Path)
	require.NotEmpty(t, semanticTokens.ResultID)
	require.Contains(t, semanticTokens.Legend, "type")
	require.Contains(t, semanticTokens.Legend, "function")
	require.NotEmpty(t, semanticTokens.Data)
	require.Zero(t, len(semanticTokens.Data)%5)
	require.Contains(t, semanticTokens.Tokens, SemanticToken{
		Path:      "pkg/runner.go",
		Range:     LSPRange{Start: LSPPosition{Line: 2, Character: 5}, End: LSPPosition{Line: 2, Character: 11}},
		Text:      "Runner",
		Type:      "type",
		TokenType: 1,
	})
	require.Contains(t, semanticTokens.Tokens, SemanticToken{
		Path:      "pkg/runner.go",
		Range:     LSPRange{Start: LSPPosition{Line: 4, Character: 5}, End: LSPPosition{Line: 4, Character: 8}},
		Text:      "Run",
		Type:      "function",
		TokenType: 2,
	})

	lineTokens, err := SemanticTokensForLine(workspace, "pkg/runner.go", 2, 100)
	require.NoError(t, err)
	require.NotEmpty(t, lineTokens.Tokens)
	for _, token := range lineTokens.Tokens {
		require.Equal(t, 2, token.Range.Start.Line)
	}

	delta, err := SemanticTokensDeltaForDocument(workspace, "pkg/runner.go", semanticTokens.ResultID, 100)
	require.NoError(t, err)
	require.Equal(t, semanticTokens.ResultID, delta.PreviousResultID)
	require.Equal(t, "pkg/runner.go", delta.Path)
	require.Empty(t, delta.Edits)
	require.Equal(t, semanticTokens.Data, delta.Tokens.Data)

	preparedRename, err := PrepareRenameAtPosition(workspace, "pkg/runner.go", 2, 6)
	require.NoError(t, err)
	require.True(t, preparedRename.Found)
	require.Equal(t, "Runner", preparedRename.Symbol)
	require.Equal(t, "Runner", preparedRename.Placeholder)
	require.Equal(t, LSPRange{Start: LSPPosition{Line: 2, Character: 5}, End: LSPPosition{Line: 2, Character: 11}}, preparedRename.Range)

	rename, err := RenameSymbol(workspace, "Runner", "RunnerRenamed", 100)
	require.NoError(t, err)
	require.True(t, rename.Found)
	require.Equal(t, "Runner", rename.Symbol)
	require.Equal(t, "RunnerRenamed", rename.NewName)
	require.GreaterOrEqual(t, rename.TextEdits, 3)
	require.GreaterOrEqual(t, rename.FileEdits, 1)
	require.Equal(t, "pkg/runner.go", rename.Edits[0].Path)
	require.Contains(t, rename.Edits[0].Content, "type RunnerRenamed struct{}")

	_, err = RenameSymbol(workspace, "Runner", "1bad", 100)
	require.Error(t, err)
	require.Contains(t, err.Error(), "valid Go identifier")

	callItems, err := PrepareCallHierarchy(workspace, "Build", "", 0, 0)
	require.NoError(t, err)
	require.Len(t, callItems, 1)
	require.Equal(t, "Build", callItems[0].Name)
	require.Equal(t, "function", callItems[0].Kind)
	require.Equal(t, "pkg/hints.go", callItems[0].Path)

	incoming, err := IncomingCalls(workspace, "Build", 10)
	require.NoError(t, err)
	require.Len(t, incoming, 1)
	require.Equal(t, "UseBuild", incoming[0].From.Name)
	require.Equal(t, "Build", incoming[0].To.Name)
	require.NotEmpty(t, incoming[0].FromRanges)

	outgoing, err := OutgoingCalls(workspace, "UseBuild", 10)
	require.NoError(t, err)
	require.Len(t, outgoing, 1)
	require.Equal(t, "UseBuild", outgoing[0].From.Name)
	require.Equal(t, "Build", outgoing[0].To.Name)
	require.NotEmpty(t, outgoing[0].FromRanges)

	typeItems, err := PrepareTypeHierarchy(workspace, "RunnerBase", "", 0, 0)
	require.NoError(t, err)
	require.Len(t, typeItems, 1)
	require.Equal(t, "RunnerBase", typeItems[0].Name)
	require.Equal(t, "struct", typeItems[0].Kind)
	require.Equal(t, "pkg/hierarchy.go", typeItems[0].Path)

	supertypes, err := TypeHierarchySupertypes(workspace, "RunnerChild", 10)
	require.NoError(t, err)
	require.Len(t, supertypes, 1)
	require.Equal(t, "RunnerBase", supertypes[0].Name)

	subtypes, err := TypeHierarchySubtypes(workspace, "RunnerBase", 10)
	require.NoError(t, err)
	require.Len(t, subtypes, 1)
	require.Equal(t, "RunnerChild", subtypes[0].Name)

	implementations, err := TypeHierarchySubtypes(workspace, "RunnerContract", 10)
	require.NoError(t, err)
	implementationNames := make([]string, 0, len(implementations))
	for _, item := range implementations {
		implementationNames = append(implementationNames, item.Name)
	}
	require.Contains(t, implementationNames, "RunnerChild")

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

	importsSource := "package main\n\nimport (\n\t\"strings\"\n\t\"fmt\"\n\t\"bytes\"\n\t\"fmt\"\n\t_ \"net/http/pprof\"\n)\n\nfunc main(){ fmt.Println(strings.TrimSpace(\" hi \")) }\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "imports.go"), []byte(importsSource), 0o644))
	organized, err := OrganizeGoImports(workspace, "imports.go", false)
	require.NoError(t, err)
	require.Equal(t, "organize_imports", organized.Kind)
	require.Equal(t, "imports.go", organized.Path)
	require.True(t, organized.Changed)
	require.Contains(t, organized.RemovedImports, "bytes")
	require.Contains(t, organized.DuplicateImports, "fmt")
	require.False(t, organized.MissingImportInference)
	require.Contains(t, organized.Content, `"fmt"`)
	require.Contains(t, organized.Content, `"strings"`)
	require.Contains(t, organized.Content, `_ "net/http/pprof"`)
	require.NotContains(t, organized.Content, `"bytes"`)
	data, err = os.ReadFile(filepath.Join(workspace, "imports.go"))
	require.NoError(t, err)
	require.Equal(t, importsSource, string(data))

	organized, err = OrganizeGoImports(workspace, "imports.go", true)
	require.NoError(t, err)
	require.True(t, organized.Changed)
	data, err = os.ReadFile(filepath.Join(workspace, "imports.go"))
	require.NoError(t, err)
	require.Equal(t, organized.Content, string(data))
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
	source := "package main\n\nfunc main(){ }\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte(source), 0o644))
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

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "completion_resolve", Path: "main.go", Line: 2, Character: 5, Query: "BuildWidget"})
	require.NoError(t, err)
	require.Equal(t, "completion-item-resolve", result.Action)
	require.Equal(t, "completionItem/resolve", result.Method)
	var resolvedCompletion struct {
		Selected struct {
			Label string `json:"label"`
		} `json:"selected"`
		Resolved struct {
			Label         string `json:"label"`
			Detail        string `json:"detail"`
			Documentation any    `json:"documentation"`
		} `json:"resolved"`
	}
	encodedResolvedCompletion, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedResolvedCompletion, &resolvedCompletion))
	require.Equal(t, "BuildWidget", resolvedCompletion.Selected.Label)
	require.Equal(t, "BuildWidget", resolvedCompletion.Resolved.Label)
	require.Equal(t, "func BuildWidget() Widget", resolvedCompletion.Resolved.Detail)

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

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "document_link_resolve", Path: "main.go", Line: 2, Character: 3})
	require.NoError(t, err)
	require.Equal(t, "document-link-resolve", result.Action)
	require.Equal(t, "documentLink/resolve", result.Method)
	var resolvedLink struct {
		Resolved struct {
			Target string `json:"target"`
		} `json:"resolved"`
	}
	encodedResolvedLink, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedResolvedLink, &resolvedLink))
	require.Equal(t, "https://example.test/resolved", resolvedLink.Resolved.Target)

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

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "inlay_hint_resolve", Path: "main.go", Line: 2, Character: 10})
	require.NoError(t, err)
	require.Equal(t, "inlay-hint-resolve", result.Action)
	require.Equal(t, "inlayHint/resolve", result.Method)
	var resolvedHint struct {
		Resolved struct {
			Tooltip string `json:"tooltip"`
		} `json:"resolved"`
	}
	encodedResolvedHint, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedResolvedHint, &resolvedHint))
	require.Equal(t, "resolved inlay hint", resolvedHint.Resolved.Tooltip)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "inline_value", Path: "main.go", Line: 2, Character: 10, Query: "frame-1"})
	require.NoError(t, err)
	require.Equal(t, "inline-value", result.Action)
	require.Equal(t, "textDocument/inlineValue", result.Method)
	var inlineValues []struct {
		Text  string   `json:"text"`
		Range lspRange `json:"range"`
	}
	encodedInlineValues, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedInlineValues, &inlineValues))
	require.Len(t, inlineValues, 1)
	require.Equal(t, "count = 1", inlineValues[0].Text)
	require.Equal(t, LSPPosition{Line: 2, Character: 5}, inlineValues[0].Range.Start)

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

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "semantic_tokens_delta", Path: "main.go", Query: "full-1"})
	require.NoError(t, err)
	require.Equal(t, "semantic-tokens-delta", result.Action)
	require.Equal(t, "textDocument/semanticTokens/full/delta", result.Method)
	var semanticTokenDelta struct {
		ResultID string `json:"resultId"`
		Edits    []struct {
			Start       int   `json:"start"`
			DeleteCount int   `json:"deleteCount"`
			Data        []int `json:"data"`
		} `json:"edits"`
	}
	encodedSemanticTokenDelta, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedSemanticTokenDelta, &semanticTokenDelta))
	require.Equal(t, "full-2", semanticTokenDelta.ResultID)
	require.Len(t, semanticTokenDelta.Edits, 1)
	require.Equal(t, []int{0, 1, 2, 3, 4}, semanticTokenDelta.Edits[0].Data)

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

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "workspace_symbol_resolve", Query: "Build"})
	require.NoError(t, err)
	require.Equal(t, "workspace-symbol-resolve", result.Action)
	require.Equal(t, "workspaceSymbol/resolve", result.Method)
	require.Empty(t, result.Path)
	var resolvedWorkspaceSymbol struct {
		Selected struct {
			Name string `json:"name"`
		} `json:"selected"`
		Resolved struct {
			Name      string         `json:"name"`
			Container string         `json:"containerName"`
			Location  map[string]any `json:"location"`
		} `json:"resolved"`
	}
	encodedResolvedWorkspaceSymbol, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedResolvedWorkspaceSymbol, &resolvedWorkspaceSymbol))
	require.Equal(t, "BuildWidget", resolvedWorkspaceSymbol.Selected.Name)
	require.Equal(t, "BuildWidget", resolvedWorkspaceSymbol.Resolved.Name)
	require.Equal(t, "demo", resolvedWorkspaceSymbol.Resolved.Container)
	require.NotNil(t, resolvedWorkspaceSymbol.Resolved.Location)

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "execute_command", Query: "demo.run", Arguments: []any{"main.go", map[string]any{"line": 2}}})
	require.NoError(t, err)
	require.Equal(t, "execute-command", result.Action)
	require.Equal(t, "workspace/executeCommand", result.Method)
	require.Empty(t, result.Path)
	var executedCommand struct {
		Status               string `json:"status"`
		Command              string `json:"command"`
		Arguments            []any  `json:"arguments"`
		ConfigurationHandled bool   `json:"configurationHandled"`
		FoldersHandled       bool   `json:"foldersHandled"`
		RegisterHandled      bool   `json:"registerHandled"`
		UnregisterHandled    bool   `json:"unregisterHandled"`
		ProgressHandled      bool   `json:"progressHandled"`
		ShowDocumentHandled  bool   `json:"showDocumentHandled"`
		RefreshHandled       bool   `json:"refreshHandled"`
	}
	encodedExecutedCommand, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedExecutedCommand, &executedCommand))
	require.Equal(t, "ok", executedCommand.Status)
	require.Equal(t, "demo.run", executedCommand.Command)
	require.Len(t, executedCommand.Arguments, 2)
	require.Equal(t, "main.go", executedCommand.Arguments[0])
	require.True(t, executedCommand.ConfigurationHandled)
	require.True(t, executedCommand.FoldersHandled)
	require.True(t, executedCommand.RegisterHandled)
	require.True(t, executedCommand.UnregisterHandled)
	require.True(t, executedCommand.ProgressHandled)
	require.True(t, executedCommand.ShowDocumentHandled)
	require.True(t, executedCommand.RefreshHandled)
	notificationMethods := map[string]bool{}
	for _, notification := range result.Notifications {
		notificationMethods[notification.Method] = true
	}
	require.True(t, notificationMethods["window/showMessage"])
	require.True(t, notificationMethods["window/logMessage"])
	require.True(t, notificationMethods["telemetry/event"])
	require.True(t, notificationMethods["$/progress"])
	require.Equal(t, 1, result.FileEdits)
	require.Equal(t, 1, result.TextEdits)
	require.True(t, result.Changed)
	require.False(t, result.Applied)
	require.Len(t, result.Edits, 1)
	require.Contains(t, result.Edits[0].Content, "func ExecutePreview()")
	data, err := os.ReadFile(filepath.Join(workspace, "main.go"))
	require.NoError(t, err)
	require.Contains(t, string(data), "func main()")

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "execute_command", Query: "demo.apply", Apply: true})
	require.NoError(t, err)
	require.Equal(t, "execute-command", result.Action)
	require.Equal(t, 1, result.FileEdits)
	require.Equal(t, 1, result.TextEdits)
	require.True(t, result.Changed)
	require.True(t, result.Applied)
	data, err = os.ReadFile(filepath.Join(workspace, "main.go"))
	require.NoError(t, err)
	require.Contains(t, string(data), "func ExecutePreview()")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte(source), 0o644))

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
	data, err = os.ReadFile(filepath.Join(workspace, "main.go"))
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
	require.Len(t, codeActions, 2)
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

	result, err = store.Query(context.Background(), "go", LSPQueryRequest{Action: "code_action_resolve", Path: "main.go", Line: 2, Character: 5, Query: "Apply lazy fix"})
	require.NoError(t, err)
	require.Equal(t, "code-action-resolve", result.Action)
	require.Equal(t, "codeAction/resolve", result.Method)
	var resolvedCodeAction struct {
		Selected struct {
			Title string `json:"title"`
		} `json:"selected"`
		Resolved struct {
			Title string `json:"title"`
			Edit  any    `json:"edit"`
		} `json:"resolved"`
	}
	encodedResolvedCodeAction, err := json.Marshal(result.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encodedResolvedCodeAction, &resolvedCodeAction))
	require.Equal(t, "Apply lazy fix", resolvedCodeAction.Selected.Title)
	require.Equal(t, "Apply lazy fix", resolvedCodeAction.Resolved.Title)
	require.NotNil(t, resolvedCodeAction.Resolved.Edit)

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
	require.Equal(t, "fake-code", result.Diagnostics[0].Code)
	require.NotNil(t, result.Diagnostics[0].CodeDescription)
	require.Equal(t, "https://example.test/diagnostics/fake-code", result.Diagnostics[0].CodeDescription.Href)
	require.Equal(t, []int{1}, result.Diagnostics[0].Tags)
	require.Len(t, result.Diagnostics[0].RelatedInformation, 1)
	require.Equal(t, "related fake diagnostic", result.Diagnostics[0].RelatedInformation[0].Message)
	require.True(t, strings.HasPrefix(result.Diagnostics[0].RelatedInformation[0].Location.URI, "file://"))
	require.True(t, strings.HasSuffix(result.Diagnostics[0].RelatedInformation[0].Location.URI, "/main.go"))
	require.IsType(t, map[string]any{}, result.Diagnostics[0].Data)
	require.Equal(t, "fake-rule", result.Diagnostics[0].Data.(map[string]any)["rule"])
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
		"code_action_resolve":       "code-action-resolve",
		"codeActionResolve":         "code-action-resolve",
		"resolve_code_action":       "code-action-resolve",
		"code_lens":                 "code-lens",
		"codeLens":                  "code-lens",
		"code_lens_resolve":         "code-lens-resolve",
		"codeLensResolve":           "code-lens-resolve",
		"completion_resolve":        "completion-item-resolve",
		"completionItemResolve":     "completion-item-resolve",
		"resolve_completion":        "completion-item-resolve",
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
		"document_link_resolve":     "document-link-resolve",
		"documentLinkResolve":       "document-link-resolve",
		"resolve_document_link":     "document-link-resolve",
		"document_color":            "document-color",
		"documentColor":             "document-color",
		"document-colors":           "document-color",
		"color_presentation":        "color-presentation",
		"colorPresentation":         "color-presentation",
		"color-presentations":       "color-presentation",
		"inlay_hint":                "inlay-hint",
		"inlayHint":                 "inlay-hint",
		"inlay-hints":               "inlay-hint",
		"inlay_hint_resolve":        "inlay-hint-resolve",
		"inlayHintResolve":          "inlay-hint-resolve",
		"resolve_inlay_hint":        "inlay-hint-resolve",
		"inline_value":              "inline-value",
		"inlineValue":               "inline-value",
		"inline-values":             "inline-value",
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
		"semantic_tokens_delta":     "semantic-tokens-delta",
		"semanticTokensDelta":       "semantic-tokens-delta",
		"semantic_delta":            "semantic-tokens-delta",
		"workspace_symbol":          "workspace-symbol",
		"workspace_symbols":         "workspace-symbol",
		"workspaceSymbol":           "workspace-symbol",
		"symbol-search":             "workspace-symbol",
		"workspace_symbol_resolve":  "workspace-symbol-resolve",
		"workspaceSymbolResolve":    "workspace-symbol-resolve",
		"resolve_workspace_symbol":  "workspace-symbol-resolve",
		"execute_command":           "execute-command",
		"executeCommand":            "execute-command",
		"workspace_execute_command": "execute-command",
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
	rootURI := ""
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
						"code":     "fake-code",
						"codeDescription": map[string]any{
							"href": "https://example.test/diagnostics/fake-code",
						},
						"source":  "fake-lsp",
						"message": "fake diagnostic",
						"tags":    []int{1},
						"relatedInformation": []map[string]any{{
							"location": map[string]any{
								"uri": params.TextDocument.URI,
								"range": map[string]any{
									"start": map[string]any{"line": 1, "character": 0},
									"end":   map[string]any{"line": 1, "character": 4},
								},
							},
							"message": "related fake diagnostic",
						}},
						"data": map[string]any{"rule": "fake-rule"},
					}},
				}})
			}
			continue
		}
		switch msg.Method {
		case "initialize":
			var params struct {
				RootURI string `json:"rootUri"`
			}
			_ = decodeLSPParams(msg.Params, &params)
			rootURI = strings.TrimRight(params.RootURI, "/")
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
		case "textDocument/completion":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{
				"isIncomplete": false,
				"items": []map[string]any{
					{"label": "BuildWidget", "kind": 3, "data": map[string]any{"id": "completion-1"}},
					{"label": "BuildOther", "kind": 3, "data": map[string]any{"id": "completion-2"}},
				},
			})})
		case "completionItem/resolve":
			var item map[string]any
			_ = decodeLSPParams(msg.Params, &item)
			item["detail"] = "func BuildWidget() Widget"
			item["documentation"] = map[string]any{"kind": "markdown", "value": "Constructs a widget."}
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(item)})
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
		case "documentLink/resolve":
			var link map[string]any
			_ = decodeLSPParams(msg.Params, &link)
			link["target"] = "https://example.test/resolved"
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(link)})
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
		case "inlayHint/resolve":
			var hint map[string]any
			_ = decodeLSPParams(msg.Params, &hint)
			hint["tooltip"] = "resolved inlay hint"
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(hint)})
		case "textDocument/inlineValue":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 5},
					"end":   map[string]any{"line": 2, "character": 10},
				},
				"text": "count = 1",
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
		case "textDocument/semanticTokens/full/delta":
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{
				"resultId": "full-2",
				"edits": []map[string]any{{
					"start":       0,
					"deleteCount": 5,
					"data":        []int{0, 1, 2, 3, 4},
				}},
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
		case "workspaceSymbol/resolve":
			var symbol map[string]any
			_ = decodeLSPParams(msg.Params, &symbol)
			symbol["containerName"] = "demo"
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(symbol)})
		case "workspace/executeCommand":
			var params struct {
				Command   string `json:"command"`
				Arguments []any  `json:"arguments"`
			}
			_ = decodeLSPParams(msg.Params, &params)
			if currentURI == "" && rootURI != "" {
				currentURI = rootURI + "/main.go"
			}
			configurationHandled := fakeLSPServerRequestHandled(reader, "configuration-check", "workspace/configuration", map[string]any{
				"items": []map[string]any{{"section": "gopls"}},
			})
			foldersHandled := fakeLSPServerRequestHandled(reader, "folders-check", "workspace/workspaceFolders", nil)
			registerHandled := fakeLSPServerRequestHandled(reader, "register-check", "client/registerCapability", map[string]any{
				"registrations": []map[string]any{{"id": "watch", "method": "workspace/didChangeWatchedFiles"}},
			})
			unregisterHandled := fakeLSPServerRequestHandled(reader, "unregister-check", "client/unregisterCapability", map[string]any{
				"unregisterations": []map[string]any{{"id": "watch", "method": "workspace/didChangeWatchedFiles"}},
			})
			progressHandled := fakeLSPServerRequestHandled(reader, "progress-check", "window/workDoneProgress/create", map[string]any{
				"token": "demo-progress",
			})
			showDocumentHandled := fakeLSPServerRequestHandled(reader, "show-document-check", "window/showDocument", map[string]any{
				"uri":       currentURI,
				"takeFocus": false,
			})
			refreshHandled := fakeLSPServerRequestHandled(reader, "semantic-refresh-check", "workspace/semanticTokens/refresh", nil) &&
				fakeLSPServerRequestHandled(reader, "inlay-refresh-check", "workspace/inlayHint/refresh", nil) &&
				fakeLSPServerRequestHandled(reader, "code-lens-refresh-check", "workspace/codeLens/refresh", nil) &&
				fakeLSPServerRequestHandled(reader, "diagnostic-refresh-check", "workspace/diagnostic/refresh", nil)
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", Method: "window/showMessage", Params: map[string]any{
				"type":    3,
				"message": "execute notice",
			}})
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", Method: "window/logMessage", Params: map[string]any{
				"type":    3,
				"message": "execute log",
			}})
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", Method: "telemetry/event", Params: map[string]any{
				"name": "execute",
			}})
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", Method: "$/progress", Params: map[string]any{
				"token": "demo-progress",
				"value": map[string]any{"kind": "report", "message": "running"},
			}})
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: "apply-execute", Method: "workspace/applyEdit", Params: map[string]any{
				"label": "execute preview",
				"edit": map[string]any{
					"changes": map[string]any{
						currentURI: []map[string]any{{
							"range": map[string]any{
								"start": map[string]any{"line": 2, "character": 0},
								"end":   map[string]any{"line": 2, "character": 13},
							},
							"newText": "func ExecutePreview() {}",
						}},
					},
				},
			}})
			_, _ = readLSPMessage(reader)
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{
				"status":               "ok",
				"command":              params.Command,
				"arguments":            params.Arguments,
				"configurationHandled": configurationHandled,
				"foldersHandled":       foldersHandled,
				"registerHandled":      registerHandled,
				"unregisterHandled":    unregisterHandled,
				"progressHandled":      progressHandled,
				"showDocumentHandled":  showDocumentHandled,
				"refreshHandled":       refreshHandled,
			})})
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
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]map[string]any{
				{
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
				},
				{"title": "Apply lazy fix", "kind": "quickfix", "data": map[string]any{"id": "lazy-fix"}},
			})})
		case "codeAction/resolve":
			var action map[string]any
			_ = decodeLSPParams(msg.Params, &action)
			action["edit"] = map[string]any{
				"changes": map[string]any{
					currentURI: []map[string]any{{
						"range": map[string]any{
							"start": map[string]any{"line": 2, "character": 5},
							"end":   map[string]any{"line": 2, "character": 10},
						},
						"newText": "Lazy",
					}},
				},
			}
			_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(action)})
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

func fakeLSPServerRequestHandled(reader *bufio.Reader, id string, method string, params any) bool {
	_ = writeLSPMessage(os.Stdout, lspRPCMessage{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	raw, err := readLSPMessage(reader)
	if err != nil {
		return false
	}
	var response lspRPCMessage
	if err := json.Unmarshal(raw, &response); err != nil {
		return false
	}
	return fmt.Sprint(response.ID) == id && response.Error == nil
}

func mustRawJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
