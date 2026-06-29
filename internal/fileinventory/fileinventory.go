package fileinventory

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Options struct {
	Path          string
	Glob          string
	Limit         int
	IncludeHidden bool
}

type Entry struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Ext   string `json:"ext,omitempty"`
	Depth int    `json:"depth"`
}

type Report struct {
	Kind      string  `json:"kind"`
	Action    string  `json:"action"`
	Root      string  `json:"root"`
	Path      string  `json:"path,omitempty"`
	Glob      string  `json:"glob,omitempty"`
	Total     int     `json:"total"`
	Limit     int     `json:"limit"`
	Truncated bool    `json:"truncated"`
	Bytes     int64   `json:"bytes"`
	Files     []Entry `json:"files"`
}

func Build(workspace string, opts Options) (Report, error) {
	if workspace == "" {
		workspace = "."
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return Report{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Report{}, err
	}
	start := root
	if strings.TrimSpace(opts.Path) != "" {
		start, err = scopedPath(root, opts.Path)
		if err != nil {
			return Report{}, err
		}
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}
	report := Report{
		Kind:   "files",
		Action: "list",
		Root:   root,
		Path:   displayPath(root, start),
		Glob:   opts.Glob,
		Limit:  limit,
		Files:  []Entry{},
	}
	err = filepath.WalkDir(start, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == start {
			return nil
		}
		if entry.IsDir() {
			if skipDir(entry.Name(), opts.IncludeHidden) {
				return filepath.SkipDir
			}
			return nil
		}
		if !opts.IncludeHidden && strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		rel := displayPath(root, path)
		if opts.Glob != "" && !matchesGlob(opts.Glob, rel, filepath.Base(path)) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		report.Total++
		report.Bytes += info.Size()
		if len(report.Files) >= limit {
			report.Truncated = true
			return nil
		}
		report.Files = append(report.Files, Entry{
			Path:  rel,
			Size:  info.Size(),
			Ext:   strings.TrimPrefix(filepath.Ext(path), "."),
			Depth: depth(rel),
		})
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	sort.Slice(report.Files, func(i, j int) bool { return report.Files[i].Path < report.Files[j].Path })
	if report.Total > len(report.Files) {
		report.Truncated = true
	}
	return report, nil
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Files")
	fmt.Fprintf(w, "  Root             %s\n", report.Root)
	if report.Path != "" && report.Path != "." {
		fmt.Fprintf(w, "  Path             %s\n", report.Path)
	}
	if report.Glob != "" {
		fmt.Fprintf(w, "  Glob             %s\n", report.Glob)
	}
	fmt.Fprintf(w, "  Total            %d\n", report.Total)
	fmt.Fprintf(w, "  Listed           %d\n", len(report.Files))
	fmt.Fprintf(w, "  Bytes            %d\n", report.Bytes)
	fmt.Fprintf(w, "  Truncated        %t\n", report.Truncated)
	if len(report.Files) == 0 {
		return
	}
	fmt.Fprintln(w)
	for _, file := range report.Files {
		fmt.Fprintf(w, "  %s\t%d", file.Path, file.Size)
		if file.Ext != "" {
			fmt.Fprintf(w, "\t%s", file.Ext)
		}
		fmt.Fprintln(w)
	}
}

func scopedPath(root, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", errors.New("path is required")
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if !within(root, resolved) {
		return "", fmt.Errorf("path escapes workspace: %s", requested)
	}
	return resolved, nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func displayPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func matchesGlob(pattern, rel, base string) bool {
	if ok, _ := filepath.Match(pattern, rel); ok {
		return true
	}
	ok, _ := filepath.Match(pattern, base)
	return ok
}

func skipDir(name string, includeHidden bool) bool {
	switch name {
	case ".git", "node_modules", "target", "dist", "coverage", ".next", ".cache":
		return true
	}
	return !includeHidden && strings.HasPrefix(name, ".")
}

func depth(rel string) int {
	rel = strings.Trim(rel, "/")
	if rel == "" || rel == "." {
		return 0
	}
	return strings.Count(rel, "/") + 1
}
