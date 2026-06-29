package focus

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	FileName       = "focus.json"
	MaxFileBytes   = 16 * 1024
	MaxDirChildren = 100
)

type Entry struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Exists    bool   `json:"exists"`
	Lines     int    `json:"lines,omitempty"`
	Chars     int    `json:"chars,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type State struct {
	Kind      string    `json:"kind"`
	Workspace string    `json:"workspace"`
	UpdatedAt time.Time `json:"updated_at"`
	Entries   []Entry   `json:"entries"`
}

type Report struct {
	Kind    string  `json:"kind"`
	Action  string  `json:"action"`
	Total   int     `json:"total"`
	Entries []Entry `json:"entries"`
}

func Path(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	return filepath.Join(workspace, ".codog", FileName)
}

func Load(workspace string) (State, error) {
	path := Path(workspace)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{Kind: "focus", Workspace: cleanWorkspace(workspace), Entries: []Entry{}}, nil
		}
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	if state.Kind != "focus" {
		return State{}, errors.New("focus state kind is invalid")
	}
	state.Workspace = cleanWorkspace(workspace)
	state.Entries = refreshEntries(state.Workspace, state.Entries)
	return state, nil
}

func Add(workspace string, paths []string) (Report, error) {
	if len(paths) == 0 {
		return Report{}, errors.New("focus path is required")
	}
	state, err := Load(workspace)
	if err != nil {
		return Report{}, err
	}
	index := map[string]Entry{}
	for _, entry := range state.Entries {
		index[entry.Path] = entry
	}
	for _, requested := range paths {
		entry, err := entryFor(state.Workspace, requested)
		if err != nil {
			return Report{}, err
		}
		index[entry.Path] = entry
	}
	state.Entries = sortedEntries(index)
	if err := Save(state.Workspace, state); err != nil {
		return Report{}, err
	}
	return report("add", state.Entries), nil
}

func Remove(workspace string, paths []string) (Report, error) {
	if len(paths) == 0 {
		return Report{}, errors.New("focus path is required")
	}
	state, err := Load(workspace)
	if err != nil {
		return Report{}, err
	}
	remove := map[string]struct{}{}
	for _, requested := range paths {
		rel, err := normalizePath(state.Workspace, requested)
		if err != nil {
			return Report{}, err
		}
		remove[rel] = struct{}{}
	}
	var entries []Entry
	for _, entry := range state.Entries {
		if _, ok := remove[entry.Path]; ok {
			continue
		}
		entries = append(entries, entry)
	}
	state.Entries = entries
	if err := Save(state.Workspace, state); err != nil {
		return Report{}, err
	}
	return report("remove", state.Entries), nil
}

func Clear(workspace string) (Report, error) {
	state := State{Kind: "focus", Workspace: cleanWorkspace(workspace), Entries: []Entry{}}
	if err := Save(state.Workspace, state); err != nil {
		return Report{}, err
	}
	return report("clear", state.Entries), nil
}

func BuildReport(workspace string) (Report, error) {
	state, err := Load(workspace)
	if err != nil {
		return Report{}, err
	}
	return report("list", state.Entries), nil
}

func Save(workspace string, state State) error {
	state.Kind = "focus"
	state.Workspace = cleanWorkspace(workspace)
	state.UpdatedAt = time.Now().UTC()
	path := Path(state.Workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".focus-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Focus")
	fmt.Fprintf(w, "  Entries          %d\n", report.Total)
	if report.Total == 0 {
		fmt.Fprintln(w, "  Result           no focused paths")
		return
	}
	for _, entry := range report.Entries {
		exists := "missing"
		if entry.Exists {
			exists = "ok"
		}
		detail := ""
		if entry.Kind == "file" {
			detail = fmt.Sprintf(" lines=%d chars=%d", entry.Lines, entry.Chars)
			if entry.Truncated {
				detail += " truncated=true"
			}
		}
		fmt.Fprintf(w, "  %s\t%s\t%s%s\n", entry.Kind, exists, entry.Path, detail)
	}
}

func RenderPrompt(workspace string) string {
	state, err := Load(workspace)
	if err != nil || len(state.Entries) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("<focused_context>\n")
	for _, entry := range state.Entries {
		builder.WriteString("<path value=\"")
		builder.WriteString(escapeAttr(entry.Path))
		builder.WriteString("\" kind=\"")
		builder.WriteString(entry.Kind)
		builder.WriteString("\">\n")
		switch entry.Kind {
		case "file":
			body, truncated, err := readFocusedFile(state.Workspace, entry.Path)
			if err != nil {
				builder.WriteString("unavailable: ")
				builder.WriteString(err.Error())
				builder.WriteByte('\n')
			} else {
				builder.WriteString(strings.TrimSpace(body))
				if truncated {
					builder.WriteString("\n[truncated]")
				}
				builder.WriteByte('\n')
			}
		case "dir":
			for _, child := range listFocusedDir(state.Workspace, entry.Path) {
				builder.WriteString("- ")
				builder.WriteString(child)
				builder.WriteByte('\n')
			}
		default:
			builder.WriteString("missing\n")
		}
		builder.WriteString("</path>\n")
	}
	builder.WriteString("</focused_context>")
	return builder.String()
}

func entryFor(workspace string, requested string) (Entry, error) {
	rel, err := normalizePath(workspace, requested)
	if err != nil {
		return Entry{}, err
	}
	abs := filepath.Join(workspace, filepath.FromSlash(rel))
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return Entry{}, fmt.Errorf("focused path does not exist: %s", rel)
		}
		return Entry{}, err
	}
	entry := Entry{Path: rel, Exists: true}
	if info.IsDir() {
		entry.Kind = "dir"
		return entry, nil
	}
	entry.Kind = "file"
	data, err := os.ReadFile(abs)
	if err != nil {
		return Entry{}, err
	}
	if len(data) > MaxFileBytes {
		data = data[:MaxFileBytes]
		entry.Truncated = true
	}
	body := string(data)
	entry.Lines = countLines(body)
	entry.Chars = len([]rune(body))
	return entry, nil
}

func refreshEntries(workspace string, entries []Entry) []Entry {
	refreshed := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		next, err := entryFor(workspace, entry.Path)
		if err != nil {
			refreshed = append(refreshed, Entry{Path: entry.Path, Kind: entry.Kind, Exists: false})
			continue
		}
		refreshed = append(refreshed, next)
	}
	sort.Slice(refreshed, func(i, j int) bool { return refreshed[i].Path < refreshed[j].Path })
	return refreshed
}

func normalizePath(workspace string, requested string) (string, error) {
	workspace = cleanWorkspace(workspace)
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", errors.New("path is required")
	}
	var abs string
	if filepath.IsAbs(requested) {
		abs = filepath.Clean(requested)
	} else {
		abs = filepath.Join(workspace, requested)
	}
	rel, err := filepath.Rel(workspace, abs)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "", errors.New("workspace root cannot be focused directly")
	}
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes workspace: %s", requested)
	}
	return filepath.ToSlash(filepath.Clean(rel)), nil
}

func cleanWorkspace(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}
	return filepath.Clean(workspace)
}

func sortedEntries(index map[string]Entry) []Entry {
	entries := make([]Entry, 0, len(index))
	for _, entry := range index {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

func report(action string, entries []Entry) Report {
	return Report{Kind: "focus", Action: action, Total: len(entries), Entries: append([]Entry(nil), entries...)}
}

func readFocusedFile(workspace string, rel string) (string, bool, error) {
	data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(rel)))
	if err != nil {
		return "", false, err
	}
	truncated := false
	if len(data) > MaxFileBytes {
		data = data[:MaxFileBytes]
		truncated = true
	}
	return string(data), truncated, nil
}

func listFocusedDir(workspace string, rel string) []string {
	root := filepath.Join(workspace, filepath.FromSlash(rel))
	var paths []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || path == root {
			return nil
		}
		if len(paths) >= MaxDirChildren {
			return filepath.SkipAll
		}
		if entry.IsDir() && ignoredDir(entry.Name()) {
			return filepath.SkipDir
		}
		childRel, err := filepath.Rel(workspace, path)
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			paths = append(paths, filepath.ToSlash(childRel)+"/")
		} else {
			paths = append(paths, filepath.ToSlash(childRel))
		}
		return nil
	})
	sort.Strings(paths)
	return paths
}

func ignoredDir(name string) bool {
	switch name {
	case ".git", ".codog", "node_modules", "vendor":
		return true
	default:
		return false
	}
}

func countLines(body string) int {
	if body == "" {
		return 0
	}
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return 1
	}
	return strings.Count(body, "\n") + 1
}

func escapeAttr(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "\"", "&quot;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}
