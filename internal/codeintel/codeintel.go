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
	re := regexp.MustCompile(`^\s*func\s+(\([^)]+\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
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
			match := re.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			rel, _ := filepath.Rel(workspace, path)
			symbols = append(symbols, Symbol{Name: match[2], Kind: "function", Path: rel, Line: i + 1})
		}
		return nil
	})
	return symbols, err
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

type Notebook struct {
	Cells []NotebookCell `json:"cells"`
}

type NotebookCell struct {
	CellType string   `json:"cell_type"`
	Source   []string `json:"source"`
}

func EditNotebookCell(path string, index int, cellType string, source string) error {
	if index < 0 {
		return errors.New("cell index must be non-negative")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var notebook Notebook
	if err := json.Unmarshal(data, &notebook); err != nil {
		return err
	}
	for len(notebook.Cells) <= index {
		notebook.Cells = append(notebook.Cells, NotebookCell{CellType: "code", Source: []string{}})
	}
	if cellType == "" {
		cellType = notebook.Cells[index].CellType
	}
	if cellType == "" {
		cellType = "code"
	}
	notebook.Cells[index].CellType = cellType
	notebook.Cells[index].Source = []string{source}
	next, err := json.MarshalIndent(notebook, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(next, '\n'), 0o644)
}
