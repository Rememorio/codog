package codeintel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Symbol struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

type Reference struct {
	Symbol string `json:"symbol"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Text   string `json:"text"`
}

type Hover struct {
	Symbol  string   `json:"symbol"`
	Found   bool     `json:"found"`
	Kind    string   `json:"kind,omitempty"`
	Path    string   `json:"path,omitempty"`
	Line    int      `json:"line,omitempty"`
	Snippet []string `json:"snippet,omitempty"`
}

type MapEntry struct {
	Path  string `json:"path"`
	Type  string `json:"type"`
	Depth int    `json:"depth"`
}

type Diagnostic struct {
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Package string `json:"package,omitempty"`
	Test    string `json:"test,omitempty"`
	Action  string `json:"action,omitempty"`
	Message string `json:"message"`
}

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

type Notebook struct {
	Cells []NotebookCell `json:"cells"`
}

type NotebookCell struct {
	CellType string   `json:"cell_type"`
	Source   []string `json:"source"`
}

type NotebookEditOptions struct {
	Index    int
	CellType string
	Source   string
	Mode     string
}

type NotebookEditResult struct {
	Path        string `json:"path"`
	Mode        string `json:"mode"`
	Index       int    `json:"index"`
	CellType    string `json:"cell_type,omitempty"`
	CellCount   int    `json:"cell_count"`
	SourceLines int    `json:"source_lines,omitempty"`
}

func EditNotebookCell(path string, index int, cellType string, source string) error {
	_, err := EditNotebook(path, NotebookEditOptions{Index: index, CellType: cellType, Source: source, Mode: "replace"})
	return err
}

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
	switch mode {
	case "replace":
		for len(cells) <= options.Index {
			cells = append(cells, newNotebookCell("code", nil))
		}
		cells[options.Index] = applyNotebookCell(cells[options.Index], cellType, sourceLines)
	case "insert":
		cell := newNotebookCell(cellType, sourceLines)
		if options.Index >= len(cells) {
			cells = append(cells, cell)
		} else {
			cells = append(cells[:options.Index], append([]map[string]any{cell}, cells[options.Index:]...)...)
		}
	case "delete":
		if options.Index < 0 || options.Index >= len(cells) {
			return NotebookEditResult{}, errors.New("cell index out of range")
		}
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
		Path:        path,
		Mode:        mode,
		Index:       options.Index,
		CellType:    cellType,
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
		return []any{}
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

func newNotebookCell(cellType string, source []any) map[string]any {
	cell := map[string]any{
		"cell_type": cellType,
		"metadata":  map[string]any{},
		"source":    source,
	}
	if cellType == "code" {
		cell["execution_count"] = nil
		cell["outputs"] = []any{}
	}
	return cell
}

func applyNotebookCell(cell map[string]any, cellType string, source []any) map[string]any {
	if cell == nil {
		cell = map[string]any{}
	}
	cell["cell_type"] = cellType
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

func mapsToAny(cells []map[string]any) []any {
	out := make([]any, 0, len(cells))
	for _, cell := range cells {
		out = append(out, cell)
	}
	return out
}
