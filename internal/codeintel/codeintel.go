package codeintel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	goformat "go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Symbol describes a Go declaration discovered in the workspace.
type Symbol struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

// Reference describes one textual symbol reference in a Go source file.
type Reference struct {
	Symbol string `json:"symbol"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Text   string `json:"text"`
}

// DocumentHighlight describes one static symbol occurrence in a source file.
type DocumentHighlight struct {
	Path  string   `json:"path"`
	Range LSPRange `json:"range"`
	Kind  int      `json:"kind,omitempty"`
	Text  string   `json:"text,omitempty"`
}

// FoldingRange describes one static foldable range in a source file.
type FoldingRange struct {
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Kind      string `json:"kind,omitempty"`
}

// SelectionRange describes one static AST range containing a document position.
type SelectionRange struct {
	Path  string   `json:"path"`
	Range LSPRange `json:"range"`
	Kind  string   `json:"kind,omitempty"`
}

// Hover contains static hover context for a discovered symbol.
type Hover struct {
	Symbol  string   `json:"symbol"`
	Found   bool     `json:"found"`
	Kind    string   `json:"kind,omitempty"`
	Path    string   `json:"path,omitempty"`
	Line    int      `json:"line,omitempty"`
	Snippet []string `json:"snippet,omitempty"`
}

// Completion describes a static completion candidate.
type Completion struct {
	Label  string `json:"label"`
	Kind   string `json:"kind"`
	Path   string `json:"path,omitempty"`
	Line   int    `json:"line,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// ResolvedSymbol combines a workspace symbol match with hover context.
type ResolvedSymbol struct {
	Symbol Symbol `json:"symbol"`
	Hover  Hover  `json:"hover,omitempty"`
}

// FormatResult reports the result of formatting a source file.
type FormatResult struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Changed bool   `json:"changed"`
	Bytes   int    `json:"bytes"`
	Content string `json:"content,omitempty"`
}

// MapEntry describes one file or directory in a workspace code map.
type MapEntry struct {
	Path  string `json:"path"`
	Type  string `json:"type"`
	Depth int    `json:"depth"`
}

// Diagnostic describes a build, test, or parser issue in a source file.
type Diagnostic struct {
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Package string `json:"package,omitempty"`
	Test    string `json:"test,omitempty"`
	Action  string `json:"action,omitempty"`
	Message string `json:"message"`
}

// GoSymbols returns function and type declarations found in Go files.
func GoSymbols(workspace string) ([]Symbol, error) {
	var symbols []Symbol
	funcRe := regexp.MustCompile(`^\s*func\s+(\([^)]+\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	typeRe := regexp.MustCompile(`^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\s+`)
	err := filepath.WalkDir(workspace, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if match := funcRe.FindStringSubmatch(line); match != nil {
				rel, _ := filepath.Rel(workspace, path)
				symbols = append(symbols, Symbol{Name: match[2], Kind: "function", Path: rel, Line: i + 1})
				continue
			}
			if match := typeRe.FindStringSubmatch(line); match != nil {
				rel, _ := filepath.Rel(workspace, path)
				symbols = append(symbols, Symbol{Name: match[1], Kind: "type", Path: rel, Line: i + 1})
			}
		}
		return nil
	})
	return symbols, err
}

// WorkspaceSymbols returns symbols matching a workspace-wide query.
func WorkspaceSymbols(workspace string, query string, limit int) ([]Symbol, error) {
	symbols, err := GoSymbols(workspace)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	query = strings.ToLower(strings.TrimSpace(query))
	matches := make([]Symbol, 0, min(limit, len(symbols)))
	for _, symbol := range symbols {
		if query != "" && !strings.Contains(strings.ToLower(symbol.Name), query) {
			continue
		}
		symbol.Path = filepath.ToSlash(symbol.Path)
		matches = append(matches, symbol)
		if len(matches) >= limit {
			break
		}
	}
	return matches, nil
}

// ResolveWorkspaceSymbol returns the best static match for a workspace symbol
// query and includes nearby source context.
func ResolveWorkspaceSymbol(workspace string, query string) (ResolvedSymbol, bool, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return ResolvedSymbol{}, false, errors.New("symbol is required")
	}
	symbols, err := GoSymbols(workspace)
	if err != nil {
		return ResolvedSymbol{}, false, err
	}
	queryLower := strings.ToLower(query)
	var selected Symbol
	found := false
	for _, symbol := range symbols {
		if symbol.Name == query {
			selected = symbol
			found = true
			break
		}
	}
	if !found {
		for _, symbol := range symbols {
			if strings.ToLower(symbol.Name) == queryLower {
				selected = symbol
				found = true
				break
			}
		}
	}
	if !found {
		for _, symbol := range symbols {
			if strings.Contains(strings.ToLower(symbol.Name), queryLower) {
				selected = symbol
				found = true
				break
			}
		}
	}
	if !found {
		return ResolvedSymbol{}, false, nil
	}
	selected.Path = filepath.ToSlash(selected.Path)
	hover, err := HoverInfo(workspace, selected.Name, 2)
	if err != nil {
		return ResolvedSymbol{}, false, err
	}
	return ResolvedSymbol{Symbol: selected, Hover: hover}, true, nil
}

// Definition returns the first discovered definition for a Go symbol.
func Definition(workspace string, symbol string) (Symbol, bool, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return Symbol{}, false, errors.New("symbol is required")
	}
	symbols, err := GoSymbols(workspace)
	if err != nil {
		return Symbol{}, false, err
	}
	for _, candidate := range symbols {
		if candidate.Name == symbol {
			return candidate, true, nil
		}
	}
	return Symbol{}, false, nil
}

// References returns textual Go references for a symbol.
func References(workspace string, symbol string, limit int) ([]Reference, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return nil, errors.New("symbol is required")
	}
	if limit <= 0 {
		limit = 100
	}
	re, err := regexp.Compile(`\b` + regexp.QuoteMeta(symbol) + `\b`)
	if err != nil {
		return nil, err
	}
	var refs []Reference
	err = filepath.WalkDir(workspace, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(refs) >= limit {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if ignoredDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if re.MatchString(line) {
				rel, _ := filepath.Rel(workspace, path)
				refs = append(refs, Reference{Symbol: symbol, Path: rel, Line: i + 1, Text: strings.TrimSpace(line)})
				if len(refs) >= limit {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	return refs, err
}

// DocumentHighlights returns static ranges for textual symbol occurrences.
func DocumentHighlights(workspace string, symbol string, relPath string, limit int) ([]DocumentHighlight, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return nil, errors.New("symbol is required")
	}
	if limit <= 0 {
		limit = 100
	}
	relPath = filepath.ToSlash(strings.TrimSpace(relPath))
	re, err := regexp.Compile(`\b` + regexp.QuoteMeta(symbol) + `\b`)
	if err != nil {
		return nil, err
	}
	var highlights []DocumentHighlight
	err = filepath.WalkDir(workspace, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(highlights) >= limit {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if ignoredDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(workspace, path)
		rel = filepath.ToSlash(rel)
		if relPath != "" && rel != relPath {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			for _, bounds := range re.FindAllStringIndex(line, -1) {
				highlights = append(highlights, DocumentHighlight{
					Path: rel,
					Range: LSPRange{
						Start: LSPPosition{Line: i, Character: bounds[0]},
						End:   LSPPosition{Line: i, Character: bounds[1]},
					},
					Kind: 1,
					Text: strings.TrimSpace(line),
				})
				if len(highlights) >= limit {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	return highlights, err
}

// FoldingRanges returns static foldable ranges for Go source files.
func FoldingRanges(workspace string, relPath string, limit int) ([]FoldingRange, error) {
	if limit <= 0 {
		limit = 100
	}
	relPath = filepath.ToSlash(strings.TrimSpace(relPath))
	fset := token.NewFileSet()
	var ranges []FoldingRange
	err := filepath.WalkDir(workspace, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(ranges) >= limit {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if ignoredDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(workspace, path)
		rel = filepath.ToSlash(rel)
		if relPath != "" && rel != relPath {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		addRange := func(start token.Pos, end token.Pos, kind string) {
			if len(ranges) >= limit || !start.IsValid() || !end.IsValid() {
				return
			}
			startLine := fset.Position(start).Line - 1
			endLine := fset.Position(end).Line - 1
			if endLine <= startLine {
				return
			}
			ranges = append(ranges, FoldingRange{Path: rel, StartLine: startLine, EndLine: endLine, Kind: kind})
		}
		for _, comment := range file.Comments {
			addRange(comment.Pos(), comment.End(), "comment")
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if len(ranges) >= limit {
				return false
			}
			switch n := node.(type) {
			case *ast.FuncDecl:
				if n.Body != nil {
					addRange(n.Body.Lbrace, n.Body.Rbrace, "region")
				}
			case *ast.GenDecl:
				if n.Lparen.IsValid() && n.Rparen.IsValid() {
					addRange(n.Lparen, n.Rparen, "region")
				}
			case *ast.TypeSpec:
				switch typ := n.Type.(type) {
				case *ast.StructType:
					if typ.Fields != nil {
						addRange(typ.Struct, typ.Fields.Closing, "region")
					}
				case *ast.InterfaceType:
					if typ.Methods != nil {
						addRange(typ.Interface, typ.Methods.Closing, "region")
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(ranges) > limit {
		ranges = ranges[:limit]
	}
	return ranges, nil
}

// SelectionRanges returns nested static AST ranges containing a document position.
func SelectionRanges(workspace string, relPath string, line int, character int, limit int) ([]SelectionRange, error) {
	if strings.TrimSpace(relPath) == "" {
		return nil, errors.New("path is required")
	}
	if line < 0 || character < 0 {
		return nil, errors.New("line and character must be non-negative")
	}
	if limit <= 0 {
		limit = 16
	}
	relPath = filepath.ToSlash(strings.TrimSpace(relPath))
	path := filepath.Join(workspace, filepath.FromSlash(relPath))
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	tokenFile := fset.File(file.Pos())
	if tokenFile == nil || line+1 > tokenFile.LineCount() {
		return nil, nil
	}
	target := tokenFile.LineStart(line+1) + token.Pos(character)
	var ranges []SelectionRange
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil || !node.Pos().IsValid() || !node.End().IsValid() {
			return true
		}
		if node.Pos() <= target && target <= node.End() {
			start := fset.Position(node.Pos())
			end := fset.Position(node.End())
			if start.IsValid() && end.IsValid() && (end.Line > start.Line || end.Column > start.Column) {
				ranges = append(ranges, SelectionRange{
					Path: relPath,
					Range: LSPRange{
						Start: LSPPosition{Line: start.Line - 1, Character: start.Column - 1},
						End:   LSPPosition{Line: end.Line - 1, Character: end.Column - 1},
					},
					Kind: astNodeKind(node),
				})
			}
			return true
		}
		return false
	})
	sort.SliceStable(ranges, func(i, j int) bool {
		iStart := ranges[i].Range.Start
		iEnd := ranges[i].Range.End
		jStart := ranges[j].Range.Start
		jEnd := ranges[j].Range.End
		iLines := iEnd.Line - iStart.Line
		jLines := jEnd.Line - jStart.Line
		if iLines != jLines {
			return iLines < jLines
		}
		iChars := iEnd.Character - iStart.Character
		jChars := jEnd.Character - jStart.Character
		return iChars < jChars
	})
	if len(ranges) > limit {
		ranges = ranges[:limit]
	}
	return ranges, nil
}

func astNodeKind(node ast.Node) string {
	name := fmt.Sprintf("%T", node)
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		name = name[dot+1:]
	}
	return strings.TrimPrefix(name, "*")
}

// HoverInfo returns static hover context around a symbol definition.
func HoverInfo(workspace string, symbol string, contextLines int) (Hover, error) {
	if contextLines <= 0 {
		contextLines = 2
	}
	definition, ok, err := Definition(workspace, symbol)
	if err != nil {
		return Hover{}, err
	}
	if !ok {
		return Hover{Symbol: symbol, Found: false}, nil
	}
	data, err := os.ReadFile(filepath.Join(workspace, definition.Path))
	if err != nil {
		return Hover{}, err
	}
	lines := strings.Split(string(data), "\n")
	start := max(1, definition.Line-contextLines)
	end := min(len(lines), definition.Line+contextLines)
	snippet := make([]string, 0, end-start+1)
	for line := start; line <= end; line++ {
		snippet = append(snippet, fmt.Sprintf("%d: %s", line, lines[line-1]))
	}
	return Hover{
		Symbol:  symbol,
		Found:   true,
		Kind:    definition.Kind,
		Path:    definition.Path,
		Line:    definition.Line,
		Snippet: snippet,
	}, nil
}

// Completions returns static Go symbol and keyword completions for a prefix.
func Completions(workspace string, prefix string, limit int) ([]Completion, error) {
	prefix = strings.TrimSpace(prefix)
	if limit <= 0 {
		limit = 50
	}
	seen := map[string]bool{}
	var completions []Completion
	symbols, err := GoSymbols(workspace)
	if err != nil {
		return nil, err
	}
	for _, symbol := range symbols {
		if !matchesCompletionPrefix(symbol.Name, prefix) {
			continue
		}
		key := symbol.Kind + ":" + symbol.Name + ":" + symbol.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		completions = append(completions, Completion{
			Label:  symbol.Name,
			Kind:   symbol.Kind,
			Path:   filepath.ToSlash(symbol.Path),
			Line:   symbol.Line,
			Detail: symbol.Path,
		})
	}
	for _, keyword := range goCompletionKeywords {
		if !matchesCompletionPrefix(keyword.label, prefix) {
			continue
		}
		key := keyword.kind + ":" + keyword.label
		if seen[key] {
			continue
		}
		seen[key] = true
		completions = append(completions, Completion{Label: keyword.label, Kind: keyword.kind, Detail: keyword.detail})
	}
	sort.Slice(completions, func(i, j int) bool {
		leftExact := strings.EqualFold(completions[i].Label, prefix)
		rightExact := strings.EqualFold(completions[j].Label, prefix)
		if leftExact != rightExact {
			return leftExact
		}
		if completions[i].Label == completions[j].Label {
			if completions[i].Kind == completions[j].Kind {
				return completions[i].Path < completions[j].Path
			}
			return completions[i].Kind < completions[j].Kind
		}
		return strings.ToLower(completions[i].Label) < strings.ToLower(completions[j].Label)
	})
	if len(completions) > limit {
		completions = completions[:limit]
	}
	return completions, nil
}

// FormatGoFile formats a Go file and optionally writes the formatted content.
func FormatGoFile(workspace string, requested string, write bool) (FormatResult, error) {
	if strings.TrimSpace(requested) == "" {
		return FormatResult{}, errors.New("path is required")
	}
	path, rel, err := resolveWorkspaceFile(workspace, requested)
	if err != nil {
		return FormatResult{}, err
	}
	if !strings.HasSuffix(strings.ToLower(path), ".go") {
		return FormatResult{}, errors.New("path must point to a .go file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return FormatResult{}, err
	}
	formatted, err := goformat.Source(data)
	if err != nil {
		return FormatResult{}, err
	}
	changed := !bytes.Equal(data, formatted)
	if write && changed {
		info, statErr := os.Stat(path)
		mode := os.FileMode(0o644)
		if statErr == nil {
			mode = info.Mode()
		}
		if err := os.WriteFile(path, formatted, mode); err != nil {
			return FormatResult{}, err
		}
	}
	return FormatResult{
		Kind:    "format",
		Path:    rel,
		Changed: changed,
		Bytes:   len(formatted),
		Content: string(formatted),
	}, nil
}

// CodeMap returns a bounded map of files and directories in the workspace.
func CodeMap(workspace string, depthLimit int, limit int) ([]MapEntry, error) {
	if depthLimit <= 0 {
		depthLimit = 3
	}
	if limit <= 0 {
		limit = 200
	}
	workspace = filepath.Clean(workspace)
	var entries []MapEntry
	err := filepath.WalkDir(workspace, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == workspace {
			return nil
		}
		rel, err := filepath.Rel(workspace, path)
		if err != nil {
			return err
		}
		depth := pathDepth(rel)
		if entry.IsDir() {
			if ignoredDir(entry.Name()) {
				return filepath.SkipDir
			}
			if depth > depthLimit {
				return filepath.SkipDir
			}
		} else if depth > depthLimit {
			return nil
		}
		if len(entries) >= limit {
			return filepath.SkipAll
		}
		kind := "file"
		if entry.IsDir() {
			kind = "dir"
		}
		entries = append(entries, MapEntry{Path: filepath.ToSlash(rel), Type: kind, Depth: depth})
		return nil
	})
	return entries, err
}

// GoDiagnostics returns diagnostics from `go test -json` for the workspace.
func GoDiagnostics(ctx context.Context, workspace string, patterns []string) ([]Diagnostic, error) {
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	args := append([]string{"test", "-json"}, patterns...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = workspace
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	diagnostics, parseErr := ParseGoTestJSON(workspace, stdout.Bytes())
	if parseErr != nil {
		return nil, parseErr
	}
	if stderr.Len() != 0 {
		diagnostics = append(diagnostics, diagnosticsFromText(workspace, "", "", "stderr", stderr.String())...)
	}
	if ctx.Err() == context.DeadlineExceeded {
		diagnostics = append(diagnostics, Diagnostic{Action: "timeout", Message: "go test diagnostics timed out"})
		return diagnostics, nil
	}
	if err != nil && len(diagnostics) == 0 {
		diagnostics = append(diagnostics, Diagnostic{Action: "fail", Message: err.Error()})
	}
	return diagnostics, nil
}

// ParseGoTestJSON converts `go test -json` output into diagnostics.
func ParseGoTestJSON(workspace string, data []byte) ([]Diagnostic, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	diagnostics := []Diagnostic{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event struct {
			Action  string `json:"Action"`
			Package string `json:"Package"`
			Test    string `json:"Test"`
			Output  string `json:"Output"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, err
		}
		if event.Action == "fail" && event.Test == "" {
			diagnostics = append(diagnostics, Diagnostic{Package: event.Package, Action: event.Action, Message: fmt.Sprintf("package %s failed", event.Package)})
		}
		if strings.TrimSpace(event.Output) == "" {
			continue
		}
		diagnostics = append(diagnostics, diagnosticsFromText(workspace, event.Package, event.Test, event.Action, event.Output)...)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return diagnostics, nil
}

func diagnosticsFromText(workspace, pkg, test, action, text string) []Diagnostic {
	var diagnostics []Diagnostic
	re := regexp.MustCompile(`(?m)([A-Za-z0-9_./\\-]+\.go):([0-9]+)(?::([0-9]+))?:\s*(.+)`)
	for _, match := range re.FindAllStringSubmatch(text, -1) {
		line := atoi(match[2])
		column := atoi(match[3])
		path := normalizeDiagnosticPath(workspace, match[1])
		diagnostics = append(diagnostics, Diagnostic{
			Path:    path,
			Line:    line,
			Column:  column,
			Package: pkg,
			Test:    test,
			Action:  action,
			Message: strings.TrimSpace(match[4]),
		})
	}
	if len(diagnostics) == 0 && strings.TrimSpace(text) != "" && strings.Contains(action, "fail") {
		diagnostics = append(diagnostics, Diagnostic{Package: pkg, Test: test, Action: action, Message: strings.TrimSpace(text)})
	}
	return diagnostics
}

func normalizeDiagnosticPath(workspace, path string) string {
	path = filepath.Clean(path)
	if filepath.IsAbs(path) {
		if rel, err := filepath.Rel(workspace, path); err == nil {
			return rel
		}
	}
	return path
}

func resolveWorkspaceFile(workspace string, requested string) (string, string, error) {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", "", err
	}
	if resolvedCandidate, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = resolvedCandidate
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("path escapes workspace: %s", requested)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", "", err
	}
	rel, err = filepath.Rel(root, resolved)
	if err != nil {
		return "", "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("path escapes workspace: %s", requested)
	}
	return resolved, filepath.ToSlash(rel), nil
}

func matchesCompletionPrefix(label string, prefix string) bool {
	if prefix == "" {
		return true
	}
	return strings.HasPrefix(strings.ToLower(label), strings.ToLower(prefix))
}

type completionKeyword struct {
	label  string
	kind   string
	detail string
}

var goCompletionKeywords = []completionKeyword{
	{label: "append", kind: "builtin", detail: "built-in function"},
	{label: "bool", kind: "builtin", detail: "built-in type"},
	{label: "break", kind: "keyword", detail: "Go keyword"},
	{label: "byte", kind: "builtin", detail: "alias for uint8"},
	{label: "cap", kind: "builtin", detail: "built-in function"},
	{label: "case", kind: "keyword", detail: "Go keyword"},
	{label: "chan", kind: "keyword", detail: "Go keyword"},
	{label: "close", kind: "builtin", detail: "built-in function"},
	{label: "complex", kind: "builtin", detail: "built-in function"},
	{label: "const", kind: "keyword", detail: "Go keyword"},
	{label: "continue", kind: "keyword", detail: "Go keyword"},
	{label: "copy", kind: "builtin", detail: "built-in function"},
	{label: "default", kind: "keyword", detail: "Go keyword"},
	{label: "defer", kind: "keyword", detail: "Go keyword"},
	{label: "delete", kind: "builtin", detail: "built-in function"},
	{label: "else", kind: "keyword", detail: "Go keyword"},
	{label: "error", kind: "builtin", detail: "built-in interface"},
	{label: "fallthrough", kind: "keyword", detail: "Go keyword"},
	{label: "false", kind: "builtin", detail: "predeclared identifier"},
	{label: "float64", kind: "builtin", detail: "built-in type"},
	{label: "for", kind: "keyword", detail: "Go keyword"},
	{label: "func", kind: "keyword", detail: "Go keyword"},
	{label: "go", kind: "keyword", detail: "Go keyword"},
	{label: "goto", kind: "keyword", detail: "Go keyword"},
	{label: "if", kind: "keyword", detail: "Go keyword"},
	{label: "imag", kind: "builtin", detail: "built-in function"},
	{label: "import", kind: "keyword", detail: "Go keyword"},
	{label: "int", kind: "builtin", detail: "built-in type"},
	{label: "interface", kind: "keyword", detail: "Go keyword"},
	{label: "len", kind: "builtin", detail: "built-in function"},
	{label: "make", kind: "builtin", detail: "built-in function"},
	{label: "map", kind: "keyword", detail: "Go keyword"},
	{label: "new", kind: "builtin", detail: "built-in function"},
	{label: "nil", kind: "builtin", detail: "predeclared identifier"},
	{label: "package", kind: "keyword", detail: "Go keyword"},
	{label: "panic", kind: "builtin", detail: "built-in function"},
	{label: "range", kind: "keyword", detail: "Go keyword"},
	{label: "real", kind: "builtin", detail: "built-in function"},
	{label: "recover", kind: "builtin", detail: "built-in function"},
	{label: "return", kind: "keyword", detail: "Go keyword"},
	{label: "rune", kind: "builtin", detail: "alias for int32"},
	{label: "select", kind: "keyword", detail: "Go keyword"},
	{label: "string", kind: "builtin", detail: "built-in type"},
	{label: "struct", kind: "keyword", detail: "Go keyword"},
	{label: "switch", kind: "keyword", detail: "Go keyword"},
	{label: "true", kind: "builtin", detail: "predeclared identifier"},
	{label: "type", kind: "keyword", detail: "Go keyword"},
	{label: "var", kind: "keyword", detail: "Go keyword"},
}

func atoi(value string) int {
	var n int
	for _, r := range value {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func ignoredDir(name string) bool {
	switch name {
	case ".git", "vendor", "node_modules", ".codog":
		return true
	default:
		return false
	}
}

func pathDepth(path string) int {
	path = filepath.ToSlash(filepath.Clean(path))
	if path == "." || path == "" {
		return 0
	}
	return strings.Count(path, "/") + 1
}

// Notebook contains the cells from a Jupyter notebook document.
type Notebook struct {
	Cells []NotebookCell `json:"cells"`
}

// NotebookCell contains the source for one notebook cell.
type NotebookCell struct {
	CellType string   `json:"cell_type"`
	Source   []string `json:"source"`
}

// NotebookReadOptions controls notebook read filtering and output expansion.
type NotebookReadOptions struct {
	CellIndex      *int
	Limit          int
	IncludeOutputs bool
}

// NotebookReadCell is a JSON-safe view of one notebook cell.
type NotebookReadCell struct {
	Index          int    `json:"index"`
	CellID         string `json:"cell_id,omitempty"`
	CellType       string `json:"cell_type"`
	Source         string `json:"source"`
	ExecutionCount any    `json:"execution_count,omitempty"`
	OutputCount    int    `json:"output_count,omitempty"`
	Outputs        any    `json:"outputs,omitempty"`
}

// NotebookReadResult reports notebook metadata and selected cells.
type NotebookReadResult struct {
	Kind      string             `json:"kind"`
	Path      string             `json:"path"`
	Language  string             `json:"language,omitempty"`
	CellCount int                `json:"cell_count"`
	Cells     []NotebookReadCell `json:"cells"`
	Truncated bool               `json:"truncated"`
}

// NotebookEditOptions describes a notebook cell edit operation.
type NotebookEditOptions struct {
	Index    int
	CellType string
	Source   string
	Mode     string
}

// NotebookEditResult reports the applied notebook cell edit.
type NotebookEditResult struct {
	Kind        string `json:"kind"`
	Path        string `json:"path"`
	Mode        string `json:"mode"`
	Index       int    `json:"index"`
	CellID      string `json:"cell_id,omitempty"`
	CellType    string `json:"cell_type,omitempty"`
	Language    string `json:"language,omitempty"`
	CellCount   int    `json:"cell_count"`
	SourceLines int    `json:"source_lines,omitempty"`
}

// EditNotebookCell replaces one notebook cell with source and type.
func EditNotebookCell(path string, index int, cellType string, source string) error {
	_, err := EditNotebook(path, NotebookEditOptions{Index: index, CellType: cellType, Source: source, Mode: "replace"})
	return err
}

// ReadNotebook reads selected cells from a Jupyter notebook file.
func ReadNotebook(path string, options NotebookReadOptions) (NotebookReadResult, error) {
	if options.CellIndex != nil && *options.CellIndex < 0 {
		return NotebookReadResult{}, errors.New("cell index must be non-negative")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return NotebookReadResult{}, err
	}
	var notebook map[string]any
	if err := json.Unmarshal(data, &notebook); err != nil {
		return NotebookReadResult{}, err
	}
	cells, err := notebookCells(notebook)
	if err != nil {
		return NotebookReadResult{}, err
	}
	limit := options.Limit
	if limit <= 0 {
		limit = 100
	}
	start, end := 0, len(cells)
	if options.CellIndex != nil {
		if *options.CellIndex >= len(cells) {
			return NotebookReadResult{}, errors.New("cell index out of range")
		}
		start = *options.CellIndex
		end = start + 1
	}
	outCells := make([]NotebookReadCell, 0, min(end-start, limit))
	truncated := false
	for index := start; index < end; index++ {
		if len(outCells) >= limit {
			truncated = true
			break
		}
		cell := cells[index]
		entry := NotebookReadCell{
			Index:          index,
			CellID:         notebookCellID(cell),
			CellType:       notebookStringValue(cell["cell_type"]),
			Source:         notebookSourceText(cell["source"]),
			ExecutionCount: cell["execution_count"],
		}
		if outputs, ok := cell["outputs"].([]any); ok {
			entry.OutputCount = len(outputs)
			if options.IncludeOutputs {
				entry.Outputs = outputs
			}
		}
		outCells = append(outCells, entry)
	}
	return NotebookReadResult{
		Kind:      "notebook_read",
		Path:      path,
		Language:  notebookLanguage(notebook),
		CellCount: len(cells),
		Cells:     outCells,
		Truncated: truncated,
	}, nil
}

// ResolveNotebookEditIndex resolves an index for a notebook edit request.
func ResolveNotebookEditIndex(path string, cellIndex *int, cellID string, mode string) (int, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "replace"
	}
	if cellIndex != nil {
		if *cellIndex < 0 {
			return 0, errors.New("cell_index must be non-negative")
		}
		return *cellIndex, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var notebook map[string]any
	if err := json.Unmarshal(data, &notebook); err != nil {
		return 0, err
	}
	cells, err := notebookCells(notebook)
	if err != nil {
		return 0, err
	}
	cellID = strings.TrimSpace(cellID)
	if cellID == "" {
		if mode == "insert" {
			return len(cells), nil
		}
		if len(cells) == 0 {
			return 0, errors.New("Notebook has no cells to edit")
		}
		return len(cells) - 1, nil
	}
	if index, err := strconv.Atoi(cellID); err == nil && index >= 0 {
		if mode == "insert" {
			return index + 1, nil
		}
		return index, nil
	}
	for index, cell := range cells {
		if notebookCellID(cell) == cellID {
			if mode == "insert" {
				return index + 1, nil
			}
			return index, nil
		}
	}
	return 0, fmt.Errorf("cell_id %q not found", cellID)
}

// EditNotebook applies a replace, insert, or delete operation to a notebook.
func EditNotebook(path string, options NotebookEditOptions) (NotebookEditResult, error) {
	if options.Index < 0 {
		return NotebookEditResult{}, errors.New("cell index must be non-negative")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return NotebookEditResult{}, err
	}
	var notebook map[string]any
	if err := json.Unmarshal(data, &notebook); err != nil {
		return NotebookEditResult{}, err
	}
	language := notebookLanguage(notebook)
	cells, err := notebookCells(notebook)
	if err != nil {
		return NotebookEditResult{}, err
	}
	mode := strings.ToLower(strings.TrimSpace(options.Mode))
	if mode == "" {
		mode = "replace"
	}
	cellType := strings.ToLower(strings.TrimSpace(options.CellType))
	if cellType == "" && mode != "delete" && options.Index < len(cells) {
		cellType, _ = cells[options.Index]["cell_type"].(string)
	}
	if cellType == "" && mode != "delete" {
		cellType = "code"
	}
	if mode != "delete" && !validNotebookCellType(cellType) {
		return NotebookEditResult{}, fmt.Errorf("unsupported cell type %q", cellType)
	}
	sourceLines := notebookSourceLines(options.Source)
	cellID := ""
	switch mode {
	case "replace":
		if options.Index >= len(cells) {
			return NotebookEditResult{}, errors.New("cell index out of range")
		}
		cells[options.Index] = applyNotebookCell(cells[options.Index], cellType, sourceLines, makeNotebookCellIDForIndex(cells, options.Index))
		cellID = notebookCellID(cells[options.Index])
	case "insert":
		cell := newNotebookCell(cellType, sourceLines, makeNotebookCellID(cells))
		cellID = notebookCellID(cell)
		if options.Index >= len(cells) {
			cells = append(cells, cell)
		} else {
			cells = append(cells[:options.Index], append([]map[string]any{cell}, cells[options.Index:]...)...)
		}
	case "delete":
		if options.Index < 0 || options.Index >= len(cells) {
			return NotebookEditResult{}, errors.New("cell index out of range")
		}
		cellID = notebookCellID(cells[options.Index])
		cells = append(cells[:options.Index], cells[options.Index+1:]...)
	default:
		return NotebookEditResult{}, fmt.Errorf("unknown notebook edit mode %q", options.Mode)
	}
	notebook["cells"] = mapsToAny(cells)
	next, err := json.MarshalIndent(notebook, "", "  ")
	if err != nil {
		return NotebookEditResult{}, err
	}
	if err := os.WriteFile(path, append(next, '\n'), 0o644); err != nil {
		return NotebookEditResult{}, err
	}
	return NotebookEditResult{
		Kind:        "notebook_edit",
		Path:        path,
		Mode:        mode,
		Index:       options.Index,
		CellID:      cellID,
		CellType:    cellType,
		Language:    language,
		CellCount:   len(cells),
		SourceLines: len(sourceLines),
	}, nil
}

func notebookCells(notebook map[string]any) ([]map[string]any, error) {
	raw, ok := notebook["cells"]
	if !ok {
		return nil, errors.New("notebook cells array not found")
	}
	rawCells, ok := raw.([]any)
	if !ok {
		return nil, errors.New("notebook cells array not found")
	}
	cells := make([]map[string]any, 0, len(rawCells))
	for _, rawCell := range rawCells {
		cell, ok := rawCell.(map[string]any)
		if !ok {
			return nil, errors.New("notebook cell is not an object")
		}
		cells = append(cells, cell)
	}
	return cells, nil
}

func validNotebookCellType(cellType string) bool {
	switch cellType {
	case "code", "markdown", "raw":
		return true
	default:
		return false
	}
}

func notebookSourceLines(source string) []any {
	if source == "" {
		return []any{""}
	}
	lines := strings.SplitAfter(source, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	out := make([]any, 0, len(lines))
	for _, line := range lines {
		out = append(out, line)
	}
	return out
}

func newNotebookCell(cellType string, source []any, id string) map[string]any {
	cell := map[string]any{
		"cell_type": cellType,
		"id":        id,
		"metadata":  map[string]any{},
		"source":    source,
	}
	if cellType == "code" {
		cell["execution_count"] = nil
		cell["outputs"] = []any{}
	}
	return cell
}

func applyNotebookCell(cell map[string]any, cellType string, source []any, fallbackID string) map[string]any {
	if cell == nil {
		cell = map[string]any{}
	}
	cell["cell_type"] = cellType
	if strings.TrimSpace(notebookCellID(cell)) == "" {
		cell["id"] = fallbackID
	}
	if _, ok := cell["metadata"]; !ok {
		cell["metadata"] = map[string]any{}
	}
	cell["source"] = source
	if cellType == "code" {
		if _, ok := cell["execution_count"]; !ok {
			cell["execution_count"] = nil
		}
		if _, ok := cell["outputs"]; !ok {
			cell["outputs"] = []any{}
		}
	} else {
		delete(cell, "execution_count")
		delete(cell, "outputs")
	}
	return cell
}

func notebookLanguage(notebook map[string]any) string {
	metadata, ok := notebook["metadata"].(map[string]any)
	if !ok {
		return ""
	}
	kernelspec, ok := metadata["kernelspec"].(map[string]any)
	if !ok {
		return ""
	}
	language, _ := kernelspec["language"].(string)
	return strings.TrimSpace(language)
}

func notebookCellID(cell map[string]any) string {
	if cell == nil {
		return ""
	}
	id, _ := cell["id"].(string)
	return strings.TrimSpace(id)
}

func makeNotebookCellID(cells []map[string]any) string {
	return makeNotebookCellIDForIndex(cells, len(cells))
}

func makeNotebookCellIDForIndex(cells []map[string]any, index int) string {
	used := map[string]bool{}
	for _, cell := range cells {
		if id := notebookCellID(cell); id != "" {
			used[id] = true
		}
	}
	for next := index + 1; ; next++ {
		id := fmt.Sprintf("cell-%d", next)
		if !used[id] {
			return id
		}
	}
}

func notebookSourceText(value any) string {
	switch source := value.(type) {
	case string:
		return source
	case []any:
		var builder strings.Builder
		for _, line := range source {
			builder.WriteString(notebookStringValue(line))
		}
		return builder.String()
	default:
		return ""
	}
}

func notebookStringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func mapsToAny(cells []map[string]any) []any {
	out := make([]any, 0, len(cells))
	for _, cell := range cells {
		out = append(out, cell)
	}
	return out
}
