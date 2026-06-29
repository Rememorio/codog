package codeintel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Symbol struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
	Line int    `json:"line"`
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
