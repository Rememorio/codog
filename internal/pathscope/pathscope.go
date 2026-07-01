package pathscope

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

const FileName = "additional-dirs.json"

type Entry struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Exists bool   `json:"exists"`
}

type State struct {
	Kind      string    `json:"kind"`
	Workspace string    `json:"workspace"`
	UpdatedAt time.Time `json:"updated_at"`
	Dirs      []string  `json:"dirs"`
}

type Report struct {
	Kind      string  `json:"kind"`
	Action    string  `json:"action"`
	Workspace string  `json:"workspace"`
	Total     int     `json:"total"`
	Entries   []Entry `json:"entries"`
}

type ValidationEntry struct {
	Input          string `json:"input,omitempty"`
	Path           string `json:"path,omitempty"`
	Source         string `json:"source,omitempty"`
	Valid          bool   `json:"valid"`
	Exists         bool   `json:"exists"`
	IsDir          bool   `json:"is_dir"`
	AlreadyAllowed bool   `json:"already_allowed"`
	Error          string `json:"error,omitempty"`
}

type ValidationReport struct {
	Kind         string            `json:"kind"`
	Action       string            `json:"action"`
	Status       string            `json:"status"`
	Workspace    string            `json:"workspace"`
	Total        int               `json:"total"`
	ValidCount   int               `json:"valid_count"`
	InvalidCount int               `json:"invalid_count"`
	Entries      []ValidationEntry `json:"entries"`
}

func Path(workspace string) string {
	return filepath.Join(cleanWorkspace(workspace), ".codog", FileName)
}

func Load(workspace string) (State, error) {
	workspace = cleanWorkspace(workspace)
	data, err := os.ReadFile(Path(workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return State{Kind: "additional_dirs", Workspace: workspace, Dirs: []string{}}, nil
		}
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	if state.Kind != "additional_dirs" {
		return State{}, errors.New("additional dirs state kind is invalid")
	}
	state.Workspace = workspace
	state.Dirs = normalizeStoredDirs(state.Dirs)
	return state, nil
}

func Save(workspace string, state State) error {
	state.Kind = "additional_dirs"
	state.Workspace = cleanWorkspace(workspace)
	state.UpdatedAt = time.Now().UTC()
	state.Dirs = normalizeStoredDirs(state.Dirs)
	path := Path(state.Workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".additional-dirs-*.tmp")
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

func Add(workspace string, paths []string) (Report, error) {
	if len(paths) == 0 {
		return Report{}, errors.New("additional directory is required")
	}
	state, err := Load(workspace)
	if err != nil {
		return Report{}, err
	}
	index := map[string]struct{}{}
	for _, dir := range state.Dirs {
		index[dir] = struct{}{}
	}
	for _, requested := range paths {
		dir, err := NormalizeDir(state.Workspace, requested)
		if err != nil {
			return Report{}, err
		}
		index[dir] = struct{}{}
	}
	state.Dirs = sortedKeys(index)
	if err := Save(state.Workspace, state); err != nil {
		return Report{}, err
	}
	return BuildReport(state.Workspace, nil, "add")
}

func Remove(workspace string, paths []string) (Report, error) {
	if len(paths) == 0 {
		return Report{}, errors.New("additional directory is required")
	}
	state, err := Load(workspace)
	if err != nil {
		return Report{}, err
	}
	remove := map[string]struct{}{}
	for _, requested := range paths {
		dir, err := NormalizeDir(state.Workspace, requested)
		if err != nil {
			return Report{}, err
		}
		remove[dir] = struct{}{}
	}
	var dirs []string
	for _, dir := range state.Dirs {
		if _, ok := remove[dir]; ok {
			continue
		}
		dirs = append(dirs, dir)
	}
	state.Dirs = dirs
	if err := Save(state.Workspace, state); err != nil {
		return Report{}, err
	}
	return BuildReport(state.Workspace, nil, "remove")
}

func Clear(workspace string) (Report, error) {
	state := State{Kind: "additional_dirs", Workspace: cleanWorkspace(workspace), Dirs: []string{}}
	if err := Save(state.Workspace, state); err != nil {
		return Report{}, err
	}
	return BuildReport(state.Workspace, nil, "clear")
}

func BuildReport(workspace string, configDirs []string, action string) (Report, error) {
	workspace = cleanWorkspace(workspace)
	entries, err := effectiveEntries(workspace, configDirs)
	if err != nil {
		return Report{}, err
	}
	if action == "" {
		action = "list"
	}
	return Report{
		Kind:      "additional_dirs",
		Action:    action,
		Workspace: workspace,
		Total:     len(entries),
		Entries:   entries,
	}, nil
}

func EffectiveDirs(workspace string, configDirs []string) ([]string, error) {
	entries, err := effectiveEntries(cleanWorkspace(workspace), configDirs)
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.Exists {
			continue
		}
		dirs = append(dirs, entry.Path)
	}
	return dirs, nil
}

func Validate(workspace string, configDirs []string, paths []string) (ValidationReport, error) {
	workspace = cleanWorkspace(workspace)
	effective, err := effectiveEntries(workspace, configDirs)
	if err != nil {
		return ValidationReport{}, err
	}
	allowed := []string{workspace}
	for _, entry := range effective {
		if entry.Exists {
			allowed = append(allowed, entry.Path)
		}
	}
	report := ValidationReport{
		Kind:      "validation",
		Action:    "add_dir",
		Status:    "ok",
		Workspace: workspace,
	}
	if len(paths) == 0 {
		for _, entry := range effective {
			report.Entries = append(report.Entries, validateResolvedDir(entry.Path, entry.Path, entry.Source, allowed))
		}
	} else {
		for _, requested := range paths {
			report.Entries = append(report.Entries, validateRequestedDir(workspace, requested, allowed))
		}
	}
	report.Total = len(report.Entries)
	for _, entry := range report.Entries {
		if entry.Valid {
			report.ValidCount++
		} else {
			report.InvalidCount++
		}
	}
	if report.InvalidCount != 0 {
		report.Status = "error"
	}
	return report, nil
}

func NormalizeDir(workspace, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", errors.New("directory path is required")
	}
	workspace = cleanWorkspace(workspace)
	candidate := expandPath(requested)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workspace, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("additional path is not a directory: %s", requested)
	}
	return filepath.Clean(resolved), nil
}

func validateRequestedDir(workspace, requested string, allowed []string) ValidationEntry {
	entry := ValidationEntry{Input: requested, Source: "requested"}
	trimmed := strings.TrimSpace(requested)
	if trimmed == "" {
		entry.Error = "directory path is required"
		return entry
	}
	candidate := expandPath(trimmed)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workspace, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	return validateResolvedDir(filepath.Clean(abs), requested, "requested", allowed)
}

func validateResolvedDir(path, input, source string, allowed []string) ValidationEntry {
	entry := ValidationEntry{Input: input, Path: filepath.Clean(path), Source: source}
	info, err := os.Stat(entry.Path)
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	entry.Exists = true
	entry.IsDir = info.IsDir()
	if !entry.IsDir {
		entry.Error = "path is not a directory"
		return entry
	}
	if resolved, err := filepath.EvalSymlinks(entry.Path); err == nil {
		entry.Path = filepath.Clean(resolved)
	}
	entry.AlreadyAllowed = pathWithinAny(entry.Path, allowed)
	entry.Valid = true
	return entry
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Additional Directories")
	fmt.Fprintf(w, "  Entries          %d\n", report.Total)
	if report.Total == 0 {
		fmt.Fprintln(w, "  Result           no additional directories")
		return
	}
	for _, entry := range report.Entries {
		status := "missing"
		if entry.Exists {
			status = "ok"
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\n", entry.Source, status, entry.Path)
	}
}

func RenderValidationText(w io.Writer, report ValidationReport) {
	fmt.Fprintln(w, "Add-dir Validation")
	fmt.Fprintf(w, "  Status           %s\n", report.Status)
	fmt.Fprintf(w, "  Workspace        %s\n", report.Workspace)
	fmt.Fprintf(w, "  Entries          %d\n", report.Total)
	fmt.Fprintf(w, "  Valid            %d\n", report.ValidCount)
	fmt.Fprintf(w, "  Invalid          %d\n", report.InvalidCount)
	if report.Total == 0 {
		fmt.Fprintln(w, "  Result           no additional directories")
		return
	}
	for _, entry := range report.Entries {
		status := "invalid"
		if entry.Valid {
			status = "ok"
		}
		allowed := "new"
		if entry.AlreadyAllowed {
			allowed = "already-allowed"
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", entry.Source, status, allowed, entry.Path)
		if entry.Error != "" {
			fmt.Fprintf(w, "    Error          %s\n", entry.Error)
		}
	}
}

func RenderPrompt(workspace string, configDirs []string) string {
	report, err := BuildReport(workspace, configDirs, "list")
	if err != nil || report.Total == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("<additional_directories>\n")
	for _, entry := range report.Entries {
		if !entry.Exists {
			continue
		}
		builder.WriteString("<directory path=\"")
		builder.WriteString(escapeAttr(entry.Path))
		builder.WriteString("\" source=\"")
		builder.WriteString(escapeAttr(entry.Source))
		builder.WriteString("\" />\n")
	}
	builder.WriteString("</additional_directories>")
	return builder.String()
}

func effectiveEntries(workspace string, configDirs []string) ([]Entry, error) {
	state, err := Load(workspace)
	if err != nil {
		return nil, err
	}
	index := map[string]sourceEntry{}
	for _, requested := range configDirs {
		dir, err := NormalizeDir(workspace, requested)
		if err != nil {
			return nil, err
		}
		index[dir] = sourceEntry{source: "config"}
	}
	for _, dir := range state.Dirs {
		if _, ok := index[dir]; ok {
			continue
		}
		index[dir] = sourceEntry{source: "workspace"}
	}
	paths := sortedKeysFromEntries(index)
	entries := make([]Entry, 0, len(paths))
	for _, path := range paths {
		entries = append(entries, Entry{
			Path:   path,
			Source: index[path].source,
			Exists: dirExists(path),
		})
	}
	return entries, nil
}

func normalizeStoredDirs(dirs []string) []string {
	index := map[string]struct{}{}
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		index[filepath.Clean(dir)] = struct{}{}
	}
	return sortedKeys(index)
}

func cleanWorkspace(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	workspace = expandPath(workspace)
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return filepath.Clean(workspace)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs)
}

func expandPath(path string) string {
	path = os.ExpandEnv(path)
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func sortedKeys(index map[string]struct{}) []string {
	keys := make([]string, 0, len(index))
	for key := range index {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysFromEntries(index map[string]sourceEntry) []string {
	keys := make([]string, 0, len(index))
	for key := range index {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type sourceEntry struct {
	source string
}

func pathWithinAny(path string, bases []string) bool {
	for _, base := range bases {
		if pathWithin(path, base) {
			return true
		}
	}
	return false
}

func pathWithin(path, base string) bool {
	path = filepath.Clean(path)
	base = filepath.Clean(base)
	if path == base {
		return true
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != "" && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".."
}

func escapeAttr(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}
