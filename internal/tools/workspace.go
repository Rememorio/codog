package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	// Register image decoders for read_image and notebook image outputs.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/gitignore"
	"github.com/Rememorio/codog/internal/undo"
	"github.com/Rememorio/codog/internal/webaccess"
)

func (MultiEditTool) Permission() Permission { return PermissionWorkspace }

func (t MultiEditTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
		Edits    []struct {
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		} `json:"edits"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if len(payload.Edits) == 0 {
		return "", errors.New("edits are required")
	}
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, firstNonEmpty(payload.Path, payload.FilePath), false)
	if err != nil {
		return "", err
	}
	data, truncated, err := readFileLimited(path, maxFileToolBytes)
	if err != nil {
		return "", err
	}
	if truncated {
		return "", fmt.Errorf("file exceeds maximum editable size of %d bytes", maxFileToolBytes)
	}
	content := string(data)
	total := 0
	for index, edit := range payload.Edits {
		if edit.OldString == "" {
			return "", fmt.Errorf("edits[%d].old_string is required", index)
		}
		count := strings.Count(content, edit.OldString)
		if count == 0 {
			return "", fmt.Errorf("edits[%d].old_string not found", index)
		}
		if !edit.ReplaceAll && count > 1 {
			return "", fmt.Errorf("edits[%d].old_string appears %d times; set replace_all to true or provide more context", index, count)
		}
		replacements := 1
		if edit.ReplaceAll {
			replacements = count
			content = strings.ReplaceAll(content, edit.OldString, edit.NewString)
		} else {
			content = strings.Replace(content, edit.OldString, edit.NewString, 1)
		}
		total += replacements
	}
	record, err := undo.Push(t.Workspace, "multi_edit", path, true, data)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"path":            path,
		"filePath":        path,
		"originalFile":    string(data),
		"structuredPatch": makeStructuredPatch(string(data), content),
		"gitDiff":         nil,
		"edits":           len(payload.Edits),
		"replacements":    total,
		"undo_available":  true,
		"undo_id":         record.ID,
	}), nil
}

func fileUndoSnapshot(path string) (bool, []byte, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil, true, nil
		}
		return false, nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileToolBytes {
		return true, nil, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, nil, false, err
	}
	return true, data, true, nil
}

type structuredPatchHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

func makeStructuredPatch(original string, updated string) []structuredPatchHunk {
	oldLines := structuredPatchContentLines(original)
	newLines := structuredPatchContentLines(updated)
	lines := make([]string, 0, len(oldLines)+len(newLines))
	for _, line := range oldLines {
		lines = append(lines, "-"+line)
	}
	for _, line := range newLines {
		lines = append(lines, "+"+line)
	}
	return []structuredPatchHunk{{
		OldStart: 1,
		OldLines: len(oldLines),
		NewStart: 1,
		NewLines: len(newLines),
		Lines:    lines,
	}}
}

func structuredPatchContentLines(content string) []string {
	if content == "" {
		return nil
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.TrimSuffix(content, "\n")
	lines := strings.Split(content, "\n")
	for index := range lines {
		lines[index] = strings.TrimSuffix(lines[index], "\r")
	}
	return lines
}

func addUndoFields(result map[string]any, available bool, id string) {
	result["undo_available"] = available
	if id != "" {
		result["undo_id"] = id
	}
}

type ApplyPatchTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (ApplyPatchTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "apply_patch",
		Description: "Apply a unified diff patch to one or more text files inside the workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"patch": map[string]any{
					"type":        "string",
					"description": "Unified diff text with ---/+++ file headers and @@ hunks.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Alias for patch.",
				},
			},
			"anyOf": []map[string]any{
				{"required": []string{"patch"}},
				{"required": []string{"content"}},
			},
			"additionalProperties": false,
		},
	}
}

func (ApplyPatchTool) Permission() Permission { return PermissionWorkspace }

func (t ApplyPatchTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Patch   string `json:"patch"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	patch := firstNonEmpty(payload.Patch, payload.Content)
	if strings.TrimSpace(patch) == "" {
		return "", errors.New("patch is required")
	}
	if int64(len(patch)) > maxFileToolBytes {
		return "", fmt.Errorf("patch exceeds maximum file tool size of %d bytes", maxFileToolBytes)
	}
	filePatches, err := parseUnifiedPatch(patch)
	if err != nil {
		return "", err
	}
	changes := make([]applyPatchChange, 0, len(filePatches))
	for _, filePatch := range filePatches {
		change, err := t.planApplyPatchChange(filePatch)
		if err != nil {
			return "", err
		}
		changes = append(changes, change)
	}
	results := []map[string]any{}
	for _, change := range changes {
		undoID := ""
		if change.UndoAvailable {
			record, err := undo.Push(t.Workspace, "apply_patch", change.Path, change.Existed, change.Original)
			if err != nil {
				return "", err
			}
			undoID = record.ID
		}
		switch change.Operation {
		case "delete":
			if err := os.Remove(change.Path); err != nil {
				return "", err
			}
		default:
			if err := os.MkdirAll(filepath.Dir(change.Path), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(change.Path, []byte(change.Next), 0o644); err != nil {
				return "", err
			}
		}
		result := map[string]any{
			"path":      displayPath(t.Workspace, change.Path),
			"operation": change.Operation,
			"hunks":     len(change.FilePatch.Hunks),
			"added":     change.Added,
			"removed":   change.Removed,
			"bytes":     len([]byte(change.Next)),
		}
		addUndoFields(result, change.UndoAvailable, undoID)
		results = append(results, result)
	}
	return pretty(map[string]any{
		"kind":          "apply_patch",
		"files_changed": len(results),
		"files":         results,
	}), nil
}

func (t ApplyPatchTool) planApplyPatchChange(filePatch unifiedFilePatch) (applyPatchChange, error) {
	operation := "update"
	target := filePatch.NewPath
	allowMissing := false
	switch {
	case filePatch.IsCreate():
		operation = "create"
		allowMissing = true
	case filePatch.IsDelete():
		operation = "delete"
		target = filePatch.OldPath
	}
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, target, allowMissing)
	if err != nil {
		return applyPatchChange{}, err
	}
	existed, original, undoAvailable, err := fileUndoSnapshot(path)
	if err != nil {
		return applyPatchChange{}, err
	}
	if operation == "create" && existed {
		return applyPatchChange{}, fmt.Errorf("cannot create existing file %s", displayPath(t.Workspace, path))
	}
	if operation != "create" && !existed {
		return applyPatchChange{}, fmt.Errorf("file does not exist: %s", displayPath(t.Workspace, path))
	}
	current := ""
	if operation != "create" {
		data, truncated, err := readFileLimited(path, maxFileToolBytes)
		if err != nil {
			return applyPatchChange{}, err
		}
		if truncated {
			return applyPatchChange{}, fmt.Errorf("file exceeds maximum editable size of %d bytes", maxFileToolBytes)
		}
		if bytes.Contains(data[:min(len(data), 8192)], []byte{0}) {
			return applyPatchChange{}, errors.New("file appears to be binary")
		}
		current = string(data)
	}
	next, added, removed, err := applyUnifiedFilePatch(current, filePatch)
	if err != nil {
		return applyPatchChange{}, err
	}
	if operation == "delete" && strings.TrimSpace(next) != "" {
		return applyPatchChange{}, fmt.Errorf("delete patch for %s leaves content behind", displayPath(t.Workspace, path))
	}
	return applyPatchChange{
		Path:          path,
		Operation:     operation,
		Existed:       existed,
		Original:      original,
		Next:          next,
		UndoAvailable: undoAvailable,
		Added:         added,
		Removed:       removed,
		FilePatch:     filePatch,
	}, nil
}

type applyPatchChange struct {
	Path          string
	Operation     string
	Existed       bool
	Original      []byte
	Next          string
	UndoAvailable bool
	Added         int
	Removed       int
	FilePatch     unifiedFilePatch
}

type unifiedFilePatch struct {
	OldPath string
	NewPath string
	Hunks   []unifiedHunk
}

func (p unifiedFilePatch) IsCreate() bool {
	return p.OldPath == "/dev/null"
}

func (p unifiedFilePatch) IsDelete() bool {
	return p.NewPath == "/dev/null"
}

type unifiedHunk struct {
	OldStart int
	OldCount int
	NewStart int
	NewCount int
	Lines    []string
}

var unifiedHunkHeaderPattern = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@`)

func parseUnifiedPatch(patch string) ([]unifiedFilePatch, error) {
	lines := splitPatchText(patch)
	patches := []unifiedFilePatch{}
	for index := 0; index < len(lines); {
		line := lines[index]
		if !strings.HasPrefix(line, "--- ") {
			index++
			continue
		}
		if index+1 >= len(lines) || !strings.HasPrefix(lines[index+1], "+++ ") {
			return nil, fmt.Errorf("patch file header at line %d is missing +++ header", index+1)
		}
		filePatch := unifiedFilePatch{
			OldPath: parseUnifiedPathHeader(strings.TrimPrefix(line, "--- ")),
			NewPath: parseUnifiedPathHeader(strings.TrimPrefix(lines[index+1], "+++ ")),
		}
		if filePatch.OldPath == "" || filePatch.NewPath == "" {
			return nil, fmt.Errorf("patch file header at line %d has empty path", index+1)
		}
		index += 2
		for index < len(lines) {
			if strings.HasPrefix(lines[index], "--- ") {
				break
			}
			if !strings.HasPrefix(lines[index], "@@ ") {
				if strings.TrimSpace(lines[index]) == "" || strings.HasPrefix(lines[index], "diff --git ") || strings.HasPrefix(lines[index], "index ") {
					index++
					continue
				}
				return nil, fmt.Errorf("unexpected patch line %d: %s", index+1, lines[index])
			}
			hunk, nextIndex, err := parseUnifiedHunk(lines, index)
			if err != nil {
				return nil, err
			}
			filePatch.Hunks = append(filePatch.Hunks, hunk)
			index = nextIndex
		}
		if len(filePatch.Hunks) == 0 {
			return nil, fmt.Errorf("patch for %s has no hunks", filePatch.TargetPath())
		}
		patches = append(patches, filePatch)
	}
	if len(patches) == 0 {
		return nil, errors.New("patch contains no unified diff file sections")
	}
	return patches, nil
}

func (p unifiedFilePatch) TargetPath() string {
	if p.IsDelete() {
		return p.OldPath
	}
	return p.NewPath
}

func splitPatchText(patch string) []string {
	patch = strings.ReplaceAll(patch, "\r\n", "\n")
	patch = strings.ReplaceAll(patch, "\r", "\n")
	lines := strings.Split(patch, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func parseUnifiedPathHeader(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if tab := strings.IndexByte(value, '\t'); tab >= 0 {
		value = value[:tab]
	}
	value = strings.Trim(value, `"`)
	if value == "/dev/null" {
		return value
	}
	if strings.HasPrefix(value, "a/") || strings.HasPrefix(value, "b/") {
		value = value[2:]
	}
	return filepath.Clean(filepath.FromSlash(value))
}

func parseUnifiedHunk(lines []string, start int) (unifiedHunk, int, error) {
	match := unifiedHunkHeaderPattern.FindStringSubmatch(lines[start])
	if match == nil {
		return unifiedHunk{}, start, fmt.Errorf("invalid hunk header at line %d", start+1)
	}
	oldStart, _ := strconv.Atoi(match[1])
	oldCount := parseUnifiedHunkCount(match[2])
	newStart, _ := strconv.Atoi(match[3])
	newCount := parseUnifiedHunkCount(match[4])
	hunk := unifiedHunk{OldStart: oldStart, OldCount: oldCount, NewStart: newStart, NewCount: newCount}
	index := start + 1
	oldSeen := 0
	newSeen := 0
	for index < len(lines) {
		line := lines[index]
		if oldSeen >= oldCount && newSeen >= newCount {
			if strings.HasPrefix(line, `\`) {
				hunk.Lines = append(hunk.Lines, line)
				index++
				continue
			}
			break
		}
		if line == "" {
			return unifiedHunk{}, index, fmt.Errorf("invalid empty hunk line at line %d", index+1)
		}
		switch line[0] {
		case ' ':
			if oldSeen >= oldCount || newSeen >= newCount {
				return unifiedHunk{}, index, fmt.Errorf("hunk line count exceeded at line %d", index+1)
			}
			oldSeen++
			newSeen++
			hunk.Lines = append(hunk.Lines, line)
		case '-':
			if oldSeen >= oldCount {
				return unifiedHunk{}, index, fmt.Errorf("hunk old line count exceeded at line %d", index+1)
			}
			oldSeen++
			hunk.Lines = append(hunk.Lines, line)
		case '+':
			if newSeen >= newCount {
				return unifiedHunk{}, index, fmt.Errorf("hunk new line count exceeded at line %d", index+1)
			}
			newSeen++
			hunk.Lines = append(hunk.Lines, line)
		case '\\':
			hunk.Lines = append(hunk.Lines, line)
		default:
			return unifiedHunk{}, index, fmt.Errorf("invalid hunk line prefix at line %d", index+1)
		}
		index++
	}
	if len(hunk.Lines) == 0 {
		return unifiedHunk{}, index, fmt.Errorf("hunk at line %d has no lines", start+1)
	}
	if oldSeen != oldCount || newSeen != newCount {
		return unifiedHunk{}, index, fmt.Errorf("hunk at line %d ended before declared line counts", start+1)
	}
	return hunk, index, nil
}

func parseUnifiedHunkCount(value string) int {
	if value == "" {
		return 1
	}
	count, _ := strconv.Atoi(value)
	return count
}

func applyUnifiedFilePatch(content string, filePatch unifiedFilePatch) (string, int, int, error) {
	lines := splitLinesKeepEnd(content)
	added := 0
	removed := 0
	offset := 0
	for _, hunk := range filePatch.Hunks {
		oldLines, newLines, hunkAdded, hunkRemoved := hunkLineSets(hunk)
		index := hunk.OldStart - 1 + offset
		if hunk.OldStart == 0 {
			index = 0
		}
		if index < 0 {
			index = 0
		}
		if !lineWindowMatches(lines, index, oldLines) {
			found := findUniqueLineWindow(lines, oldLines)
			if found < 0 {
				return "", 0, 0, fmt.Errorf("hunk starting at original line %d did not match", hunk.OldStart)
			}
			index = found
		}
		next := make([]string, 0, len(lines)-len(oldLines)+len(newLines))
		next = append(next, lines[:index]...)
		next = append(next, newLines...)
		next = append(next, lines[index+len(oldLines):]...)
		lines = next
		offset += len(newLines) - len(oldLines)
		added += hunkAdded
		removed += hunkRemoved
	}
	return strings.Join(lines, ""), added, removed, nil
}

func splitLinesKeepEnd(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.SplitAfter(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func hunkLineSets(hunk unifiedHunk) ([]string, []string, int, int) {
	oldLines := []string{}
	newLines := []string{}
	added := 0
	removed := 0
	lastOld := -1
	lastNew := -1
	lastPrefix := byte(0)
	for _, line := range hunk.Lines {
		if strings.HasPrefix(line, `\`) {
			if (lastPrefix == ' ' || lastPrefix == '-') && lastOld >= 0 {
				oldLines[lastOld] = strings.TrimSuffix(oldLines[lastOld], "\n")
			}
			if (lastPrefix == ' ' || lastPrefix == '+') && lastNew >= 0 {
				newLines[lastNew] = strings.TrimSuffix(newLines[lastNew], "\n")
			}
			continue
		}
		text := line[1:] + "\n"
		lastPrefix = line[0]
		switch line[0] {
		case ' ':
			oldLines = append(oldLines, text)
			newLines = append(newLines, text)
			lastOld = len(oldLines) - 1
			lastNew = len(newLines) - 1
		case '-':
			oldLines = append(oldLines, text)
			lastOld = len(oldLines) - 1
			removed++
		case '+':
			newLines = append(newLines, text)
			lastNew = len(newLines) - 1
			added++
		}
	}
	return oldLines, newLines, added, removed
}

func lineWindowMatches(lines []string, start int, window []string) bool {
	if start < 0 || start+len(window) > len(lines) {
		return false
	}
	for index := range window {
		if lines[start+index] != window[index] {
			return false
		}
	}
	return true
}

func findUniqueLineWindow(lines []string, window []string) int {
	if len(window) == 0 {
		return len(lines)
	}
	found := -1
	for index := 0; index+len(window) <= len(lines); index++ {
		if !lineWindowMatches(lines, index, window) {
			continue
		}
		if found >= 0 {
			return -1
		}
		found = index
	}
	return found
}

func pathOrFilePathRequirement() []map[string]any {
	return []map[string]any{
		{"required": []string{"path"}},
		{"required": []string{"file_path"}},
	}
}

type GrepTool struct {
	Workspace        string
	AdditionalDirs   []string
	RespectGitignore bool
}

var grepOutputModeNames = []string{"content", "matches", "lines", "files_with_matches", "files", "paths", "filenames", "names", "count", "counts"}

func (GrepTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "grep",
		Description: "Search file contents with a regular expression.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":     map[string]any{"type": "string"},
				"path":        map[string]any{"type": "string"},
				"glob":        map[string]any{"type": "string"},
				"output_mode": map[string]any{"type": "string", "enum": append([]string(nil), grepOutputModeNames...)},
				"-B":          map[string]any{"type": "integer", "minimum": 0},
				"-A":          map[string]any{"type": "integer", "minimum": 0},
				"-C":          map[string]any{"type": "integer", "minimum": 0},
				"context":     map[string]any{"type": "integer", "minimum": 0},
				"-n":          map[string]any{"type": "boolean"},
				"-i":          map[string]any{"type": "boolean"},
				"ignore_case": map[string]any{"type": "boolean"},
				"type":        map[string]any{"type": "string"},
				"limit":       map[string]any{"type": "integer", "minimum": 1},
				"head_limit":  map[string]any{"type": "integer", "minimum": 0},
				"offset":      map[string]any{"type": "integer", "minimum": 0},
				"multiline":   map[string]any{"type": "boolean"},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
	}
}

func (GrepTool) Permission() Permission { return PermissionReadOnly }

type grepInput struct {
	Pattern        string `json:"pattern"`
	Path           string `json:"path"`
	Glob           string `json:"glob"`
	OutputMode     string `json:"output_mode"`
	Before         int    `json:"-B"`
	After          int    `json:"-A"`
	ContextShort   int    `json:"-C"`
	Context        int    `json:"context"`
	LineNumbers    *bool  `json:"-n"`
	DashIgnoreCase bool   `json:"-i"`
	IgnoreCase     bool   `json:"ignore_case"`
	Type           string `json:"type"`
	Limit          int    `json:"limit"`
	HeadLimit      *int   `json:"head_limit"`
	Offset         int    `json:"offset"`
	Multiline      bool   `json:"multiline"`
}

type grepOptions struct {
	root          string
	walkRoot      string
	glob          string
	fileType      string
	mode          string
	limit         int
	offset        int
	beforeLines   int
	afterLines    int
	unlimited     bool
	lineNumbers   bool
	multiline     bool
	respectIgnore bool
	pattern       *regexp.Regexp
}

type grepSearch struct {
	workspace        string
	options          grepOptions
	ignoreMatcher    *gitignore.Matcher
	seenFiles        map[string]bool
	counts           map[string]int
	files            []string
	contentFiles     map[string]bool
	contentFilenames []string
	contentLines     []string
	matches          []map[string]any
	seen             int
	filesTruncated   bool
	countTruncated   bool
	contentTruncated bool
}

func (t GrepTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	started := time.Now()
	var payload grepInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	search, err := newGrepSearch(t, payload)
	if err != nil {
		return "", err
	}
	if err := search.run(); err != nil {
		return "", err
	}
	return search.render(time.Since(started).Milliseconds())
}

func newGrepSearch(tool GrepTool, payload grepInput) (*grepSearch, error) {
	pattern, err := compileGrepPattern(payload)
	if err != nil {
		return nil, err
	}
	root := tool.Workspace
	if payload.Path != "" {
		root, err = safePathInScope(tool.Workspace, tool.AdditionalDirs, payload.Path, false)
		if err != nil {
			return nil, err
		}
	}
	mode := normalizeGrepOutputMode(payload.OutputMode)
	if mode == "" {
		return nil, suggestedValueError("unsupported grep output_mode", payload.OutputMode, grepOutputModeNames)
	}
	limit, unlimited := grepLimit(payload.HeadLimit, payload.Limit)
	beforeLines, afterLines := grepContextLimits(payload)
	lineNumbers := payload.LineNumbers == nil || *payload.LineNumbers
	walkRoot := root
	if payload.Glob != "" {
		walkRoot = deriveGlobWalkRoot(root, payload.Glob)
	}
	return &grepSearch{
		workspace: tool.Workspace,
		options: grepOptions{
			root: root, walkRoot: walkRoot, glob: payload.Glob, fileType: payload.Type,
			mode: mode, limit: limit, offset: max(payload.Offset, 0), beforeLines: beforeLines,
			afterLines: afterLines, unlimited: unlimited, lineNumbers: lineNumbers,
			multiline: payload.Multiline, respectIgnore: tool.RespectGitignore, pattern: pattern,
		},
		seenFiles:    map[string]bool{},
		counts:       map[string]int{},
		contentFiles: map[string]bool{},
	}, nil
}

func compileGrepPattern(payload grepInput) (*regexp.Regexp, error) {
	if payload.Pattern == "" {
		return nil, errors.New("pattern is required")
	}
	flags := ""
	if payload.IgnoreCase || payload.DashIgnoreCase {
		flags += "i"
	}
	if payload.Multiline {
		flags += "s"
	}
	pattern := payload.Pattern
	if flags != "" {
		pattern = "(?" + flags + ")" + pattern
	}
	return regexp.Compile(pattern)
}

func grepContextLimits(payload grepInput) (int, int) {
	contextLines := max(payload.Context, 0)
	if contextLines == 0 {
		contextLines = max(payload.ContextShort, 0)
	}
	beforeLines := max(payload.Before, 0)
	if beforeLines == 0 {
		beforeLines = contextLines
	}
	afterLines := max(payload.After, 0)
	if afterLines == 0 {
		afterLines = contextLines
	}
	return beforeLines, afterLines
}

func (s *grepSearch) run() error {
	if s.options.respectIgnore {
		matcher, err := gitignore.New(s.workspace)
		if err != nil {
			return err
		}
		s.ignoreMatcher = matcher
	}
	return filepath.WalkDir(s.options.walkRoot, s.visit)
}

func (s *grepSearch) visit(path string, entry os.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if entry.IsDir() {
		return s.visitDirectory(path, entry)
	}
	if !s.includesFile(path) {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.Contains(data[:min(len(data), 4096)], []byte{0}) {
		return nil
	}
	if s.options.multiline {
		return s.matchMultiline(path, string(data))
	}
	return s.matchLines(path, strings.Split(string(data), "\n"))
}

func (s *grepSearch) visitDirectory(path string, entry os.DirEntry) error {
	if ignoredDir(entry.Name()) && path != s.options.root {
		return filepath.SkipDir
	}
	if s.ignoreMatcher != nil && s.ignoreMatcher.Ignored(path, true) {
		return filepath.SkipDir
	}
	return nil
}

func (s *grepSearch) includesFile(path string) bool {
	if s.ignoreMatcher != nil && s.ignoreMatcher.Ignored(path, false) {
		return false
	}
	if s.options.glob != "" {
		rel, _ := filepath.Rel(s.options.root, path)
		if !globPatternMatches(s.options.glob, rel, filepath.Base(path)) {
			return false
		}
	}
	return s.options.fileType == "" || matchesGrepType(path, s.options.fileType)
}

func (s *grepSearch) matchMultiline(path string, text string) error {
	locations := s.options.pattern.FindAllStringIndex(text, -1)
	if len(locations) == 0 {
		return nil
	}
	display := displayPath(s.workspace, path)
	switch s.options.mode {
	case "files_with_matches":
		return s.recordFile(display)
	case "count":
		return s.recordCount(display, len(locations))
	}
	lines := strings.Split(text, "\n")
	lineStarts := grepLineStartOffsets(text)
	for _, location := range locations {
		startLine := grepLineForOffset(lineStarts, location[0])
		endLine := grepLineForOffset(lineStarts, max(location[1]-1, location[0]))
		if err := s.recordContent(display, lines, startLine, endLine, text[location[0]:location[1]]); err != nil {
			return err
		}
	}
	return nil
}

func (s *grepSearch) matchLines(path string, lines []string) error {
	display := displayPath(s.workspace, path)
	for index, line := range lines {
		if !s.options.pattern.MatchString(line) {
			continue
		}
		switch s.options.mode {
		case "files_with_matches":
			return s.recordFile(display)
		case "count":
			if err := s.recordCount(display, 1); err != nil {
				return err
			}
		default:
			if err := s.recordContent(display, lines, index, index, line); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *grepSearch) recordFile(path string) error {
	if s.seenFiles[path] {
		return nil
	}
	s.seenFiles[path] = true
	defer func() { s.seen++ }()
	if s.seen < s.options.offset {
		return nil
	}
	if !s.options.unlimited && len(s.files) >= s.options.limit {
		s.filesTruncated = true
		return filepath.SkipAll
	}
	s.files = append(s.files, path)
	return nil
}

func (s *grepSearch) recordCount(path string, count int) error {
	if _, exists := s.counts[path]; !exists && !s.options.unlimited && len(s.counts) >= s.options.offset+s.options.limit {
		s.countTruncated = true
		return filepath.SkipAll
	}
	s.counts[path] += count
	return nil
}

func (s *grepSearch) recordContent(path string, lines []string, startLine int, endLine int, text string) error {
	defer func() { s.seen++ }()
	if s.seen < s.options.offset {
		return nil
	}
	if !s.options.unlimited && len(s.matches) >= s.options.limit {
		s.contentTruncated = true
		return filepath.SkipAll
	}
	s.recordContentFile(path)
	match := map[string]any{"path": path, "line": startLine + 1, "text": text}
	if s.options.multiline {
		match["end_line"] = endLine + 1
	}
	if s.options.beforeLines > 0 {
		before := grepContextLines(lines, startLine-s.options.beforeLines, startLine)
		s.appendMatchContext(match, "before", path, before)
	}
	s.appendContentLines(path, grepContextLines(lines, startLine, endLine+1))
	if s.options.afterLines > 0 {
		afterEnd := endLine + s.options.afterLines + 1
		if s.options.multiline {
			afterEnd++
		}
		after := grepContextLines(lines, endLine+1, afterEnd)
		s.appendMatchContext(match, "after", path, after)
	}
	s.matches = append(s.matches, match)
	return nil
}

func (s *grepSearch) recordContentFile(path string) {
	if s.contentFiles[path] {
		return
	}
	s.contentFiles[path] = true
	s.contentFilenames = append(s.contentFilenames, path)
}

func (s *grepSearch) appendMatchContext(match map[string]any, key string, path string, lines []grepContextLine) {
	match[key] = lines
	s.appendContentLines(path, lines)
}

func (s *grepSearch) appendContentLines(path string, lines []grepContextLine) {
	for _, entry := range lines {
		s.contentLines = append(s.contentLines, formatGrepContentLine(path, entry.Line, entry.Text, s.options.lineNumbers))
	}
}

func (s *grepSearch) render(durationMS int64) (string, error) {
	switch s.options.mode {
	case "files_with_matches":
		sort.Strings(s.files)
		return pretty(map[string]any{
			"output_mode":   s.options.mode,
			"mode":          s.options.mode,
			"files":         s.files,
			"filenames":     s.files,
			"num_files":     len(s.files),
			"numFiles":      len(s.files),
			"content":       nil,
			"numLines":      nil,
			"numMatches":    nil,
			"appliedLimit":  grepAppliedLimit(s.options.limit, s.options.unlimited),
			"appliedOffset": s.options.offset,
			"durationMs":    durationMS,
			"duration_ms":   durationMS,
			"truncated":     s.filesTruncated,
			"offset":        s.options.offset,
		}), nil
	case "count":
		entries := grepCountEntries(s.counts, s.options.offset, s.options.limit)
		filenames := grepCountFilenames(entries)
		totalMatches := grepCountTotal(s.counts)
		return pretty(map[string]any{
			"output_mode":   s.options.mode,
			"mode":          s.options.mode,
			"counts":        entries,
			"filenames":     filenames,
			"numFiles":      len(filenames),
			"content":       nil,
			"numLines":      nil,
			"numMatches":    totalMatches,
			"appliedLimit":  grepAppliedLimit(s.options.limit, s.options.unlimited),
			"appliedOffset": s.options.offset,
			"durationMs":    durationMS,
			"duration_ms":   durationMS,
			"truncated":     s.countTruncated,
			"offset":        s.options.offset,
		}), nil
	default:
		sort.Strings(s.contentFilenames)
		return pretty(map[string]any{
			"output_mode":   s.options.mode,
			"mode":          s.options.mode,
			"matches":       s.matches,
			"filenames":     s.contentFilenames,
			"numFiles":      len(s.contentFilenames),
			"content":       strings.Join(s.contentLines, "\n"),
			"numLines":      len(s.contentLines),
			"appliedLimit":  grepAppliedLimit(s.options.limit, s.options.unlimited),
			"appliedOffset": s.options.offset,
			"durationMs":    durationMS,
			"duration_ms":   durationMS,
			"truncated":     s.contentTruncated,
			"offset":        s.options.offset,
		}), nil
	}
}

func grepLimit(headLimit *int, legacyLimit int) (int, bool) {
	if headLimit != nil {
		if *headLimit <= 0 {
			return 0, true
		}
		return *headLimit, false
	}
	if legacyLimit > 0 {
		return legacyLimit, false
	}
	return 250, false
}

func normalizeGrepOutputMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "files_with_matches", "files-with-matches", "files with matches", "files", "file", "paths", "path", "filenames", "filename", "names", "name", "files_only", "files-only", "file_paths", "file-paths":
		return "files_with_matches"
	case "content", "contents", "matches", "match", "lines", "line":
		return "content"
	case "count", "counts", "count_matches", "count-matches":
		return "count"
	default:
		return ""
	}
}

func grepAppliedLimit(limit int, unlimited bool) any {
	if unlimited {
		return nil
	}
	return limit
}

type grepContextLine struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

func grepContextLines(lines []string, start int, end int) []grepContextLine {
	start = max(start, 0)
	end = min(max(end, 0), len(lines))
	if start >= end {
		return []grepContextLine{}
	}
	out := make([]grepContextLine, 0, end-start)
	for index := start; index < end; index++ {
		out = append(out, grepContextLine{Line: index + 1, Text: lines[index]})
	}
	return out
}

func grepLineStartOffsets(text string) []int {
	offsets := []int{0}
	for index, r := range text {
		if r == '\n' {
			offsets = append(offsets, index+1)
		}
	}
	return offsets
}

func grepLineForOffset(lineStarts []int, offset int) int {
	if len(lineStarts) == 0 {
		return 0
	}
	offset = max(offset, 0)
	index := sort.Search(len(lineStarts), func(i int) bool {
		return lineStarts[i] > offset
	}) - 1
	if index < 0 {
		return 0
	}
	return min(index, len(lineStarts)-1)
}

func formatGrepContentLine(path string, line int, text string, lineNumbers bool) string {
	if lineNumbers {
		return fmt.Sprintf("%s:%d:%s", path, line, text)
	}
	return fmt.Sprintf("%s:%s", path, text)
}

func matchesGrepType(path string, fileType string) bool {
	typ := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fileType)), ".")
	if typ == "" {
		return true
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	aliases := map[string][]string{
		"c":          {"c", "h"},
		"cpp":        {"cc", "cpp", "cxx", "hpp", "hh", "hxx"},
		"go":         {"go"},
		"java":       {"java"},
		"js":         {"js", "mjs", "cjs"},
		"json":       {"json"},
		"jsx":        {"jsx"},
		"markdown":   {"md", "markdown"},
		"md":         {"md", "markdown"},
		"py":         {"py"},
		"python":     {"py"},
		"rs":         {"rs"},
		"rust":       {"rs"},
		"sh":         {"sh", "bash", "zsh"},
		"shell":      {"sh", "bash", "zsh"},
		"swift":      {"swift"},
		"toml":       {"toml"},
		"ts":         {"ts", "mts", "cts"},
		"tsx":        {"tsx"},
		"typescript": {"ts", "tsx", "mts", "cts"},
		"yaml":       {"yaml", "yml"},
		"yml":        {"yaml", "yml"},
	}
	if values := aliases[typ]; len(values) != 0 {
		for _, value := range values {
			if ext == value {
				return true
			}
		}
		return false
	}
	return ext == typ
}

func grepCountEntries(counts map[string]int, offset int, limit int) []map[string]any {
	paths := make([]string, 0, len(counts))
	for path := range counts {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	start := min(max(offset, 0), len(paths))
	end := len(paths)
	if limit > 0 {
		end = min(start+limit, len(paths))
	}
	entries := make([]map[string]any, 0, end-start)
	for _, path := range paths[start:end] {
		entries = append(entries, map[string]any{"path": path, "count": counts[path]})
	}
	return entries
}

func grepCountFilenames(entries []map[string]any) []string {
	filenames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if path, ok := entry["path"].(string); ok {
			filenames = append(filenames, path)
		}
	}
	return filenames
}

func grepCountTotal(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

type GlobTool struct {
	Workspace        string
	AdditionalDirs   []string
	RespectGitignore bool
}

func (GlobTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "glob",
		Description: "Find workspace files by glob pattern.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string"},
				"path":    map[string]any{"type": "string"},
				"limit":   map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
	}
}

func (GlobTool) Permission() Permission { return PermissionReadOnly }

func (t GlobTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.Pattern == "" {
		return "", errors.New("pattern is required")
	}
	root := t.Workspace
	var err error
	if payload.Path != "" {
		root, err = safePathInScope(t.Workspace, t.AdditionalDirs, payload.Path, false)
		if err != nil {
			return "", err
		}
	}
	limit := payload.Limit
	if limit <= 0 {
		limit = 200
	}
	var files []string
	started := time.Now()
	walkRoot := deriveGlobWalkRoot(root, payload.Pattern)
	collectLimit := limit + 1
	var ignoreMatcher *gitignore.Matcher
	if t.RespectGitignore {
		ignoreMatcher, err = gitignore.New(t.Workspace)
		if err != nil {
			return "", err
		}
	}
	err = filepath.WalkDir(walkRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(files) >= collectLimit {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if ignoredDir(entry.Name()) && path != root {
				return filepath.SkipDir
			}
			if ignoreMatcher != nil && ignoreMatcher.Ignored(path, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if ignoreMatcher != nil && ignoreMatcher.Ignored(path, false) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if globPatternMatches(payload.Pattern, rel, filepath.Base(path)) {
			files = append(files, displayPath(t.Workspace, path))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	truncated := len(files) > limit
	if truncated {
		files = files[:limit]
	}
	durationMS := time.Since(started).Milliseconds()
	return pretty(map[string]any{
		"files":       files,
		"filenames":   files,
		"numFiles":    len(files),
		"durationMs":  durationMS,
		"duration_ms": durationMS,
		"truncated":   truncated,
	}), nil
}

func globPatternMatches(pattern string, rel string, base string) bool {
	for _, expanded := range expandBracePatterns(pattern, 64) {
		if globPatternMatchesSingle(expanded, rel, base) {
			return true
		}
	}
	return false
}

func globPatternMatchesSingle(pattern string, rel string, base string) bool {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	rel = filepath.ToSlash(strings.TrimPrefix(rel, "./"))
	base = filepath.ToSlash(base)
	if pattern == "" {
		return true
	}
	if ok, _ := pathMatch(pattern, rel); ok {
		return true
	}
	if !strings.Contains(pattern, "/") {
		if ok, _ := pathMatch(pattern, base); ok {
			return true
		}
	}
	re, err := regexp.Compile(globPatternRegexp(pattern))
	if err != nil {
		return false
	}
	return re.MatchString(rel)
}

func expandBracePatterns(pattern string, limit int) []string {
	if limit <= 0 {
		return []string{pattern}
	}
	start := strings.Index(pattern, "{")
	if start < 0 {
		return []string{pattern}
	}
	end := strings.Index(pattern[start+1:], "}")
	if end < 0 {
		return []string{pattern}
	}
	end += start + 1
	parts := strings.Split(pattern[start+1:end], ",")
	if len(parts) <= 1 {
		return []string{pattern}
	}
	out := []string{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		next := pattern[:start] + part + pattern[end+1:]
		for _, expanded := range expandBracePatterns(next, limit-len(out)) {
			out = append(out, expanded)
			if len(out) >= limit {
				return out
			}
		}
	}
	if len(out) == 0 {
		return []string{pattern}
	}
	return out
}

func deriveGlobWalkRoot(root string, pattern string) string {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	if pattern == "" || filepath.IsAbs(pattern) {
		return root
	}
	parts := strings.Split(pattern, "/")
	fixed := []string{}
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." || strings.ContainsAny(part, "*?[{") {
			break
		}
		fixed = append(fixed, part)
	}
	if len(fixed) == 0 {
		return root
	}
	candidate := filepath.Join(append([]string{root}, fixed...)...)
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return root
	}
	return candidate
}

func pathMatch(pattern string, value string) (bool, error) {
	pattern = filepath.ToSlash(pattern)
	value = filepath.ToSlash(value)
	return path.Match(pattern, value)
}

func globPatternRegexp(pattern string) string {
	var builder strings.Builder
	builder.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch ch {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					builder.WriteString("(?:.*/)?")
				} else {
					builder.WriteString(".*")
				}
				continue
			}
			builder.WriteString("[^/]*")
		case '?':
			builder.WriteString("[^/]")
		default:
			builder.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	builder.WriteString("$")
	return builder.String()
}

type LSTool struct {
	Workspace      string
	AdditionalDirs []string
}

type lsEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Type   string `json:"type"`
	Size   int64  `json:"size"`
	Hidden bool   `json:"hidden,omitempty"`
}

func (LSTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "ls",
		Description: "List files and directories in a workspace-scoped directory.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string"},
				"ignore": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"hidden": map[string]any{"type": "boolean"},
				"limit":  map[string]any{"type": "integer", "minimum": 1},
			},
			"additionalProperties": false,
		},
	}
}

func (LSTool) Permission() Permission { return PermissionReadOnly }

func (t LSTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	started := time.Now()
	var payload struct {
		Path   string   `json:"path"`
		Ignore []string `json:"ignore"`
		Hidden bool     `json:"hidden"`
		Limit  int      `json:"limit"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	requested := strings.TrimSpace(payload.Path)
	if requested == "" {
		requested = "."
	}
	dir, err := safePathInScope(t.Workspace, t.AdditionalDirs, requested, false)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path must be a directory")
	}
	limit := payload.Limit
	if limit <= 0 {
		limit = 200
	}
	children, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	fileIgnorePatterns := loadLSIgnorePatterns(t.Workspace, dir)
	sort.Slice(children, func(i, j int) bool {
		left, right := children[i], children[j]
		if left.IsDir() != right.IsDir() {
			return left.IsDir()
		}
		return strings.ToLower(left.Name()) < strings.ToLower(right.Name())
	})
	entries := make([]lsEntry, 0, min(len(children), limit))
	entryPaths := make([]string, 0, min(len(children), limit))
	truncated := false
	for _, child := range children {
		name := child.Name()
		hidden := strings.HasPrefix(name, ".")
		if hidden && !payload.Hidden {
			continue
		}
		childPath := filepath.Join(dir, name)
		if ignoredLSEntry(t.Workspace, childPath, name, child.IsDir(), payload.Ignore) {
			continue
		}
		if ignoredByLSIgnoreFiles(childPath, name, child.IsDir(), fileIgnorePatterns) {
			continue
		}
		if len(entries) >= limit {
			truncated = true
			break
		}
		childInfo, err := child.Info()
		if err != nil {
			return "", err
		}
		kind := "file"
		switch {
		case childInfo.IsDir():
			kind = "directory"
		case childInfo.Mode()&os.ModeSymlink != 0:
			kind = "symlink"
		}
		display := displayPath(t.Workspace, childPath)
		entries = append(entries, lsEntry{
			Name:   name,
			Path:   display,
			Type:   kind,
			Size:   childInfo.Size(),
			Hidden: hidden,
		})
		entryPaths = append(entryPaths, display)
	}
	durationMS := time.Since(started).Milliseconds()
	return pretty(map[string]any{
		"kind":        "ls",
		"path":        displayPath(t.Workspace, dir),
		"entries":     entries,
		"files":       entryPaths,
		"filenames":   entryPaths,
		"numFiles":    len(entryPaths),
		"num_files":   len(entryPaths),
		"numEntries":  len(entries),
		"num_entries": len(entries),
		"durationMs":  durationMS,
		"duration_ms": durationMS,
		"limit":       limit,
		"truncated":   truncated,
	}), nil
}

func ignoredLSEntry(workspace string, fullPath string, name string, isDir bool, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	display := filepath.ToSlash(displayPath(workspace, fullPath))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		directoryOnly := strings.HasSuffix(pattern, "/")
		pattern = strings.TrimSuffix(pattern, "/")
		if directoryOnly && !isDir {
			continue
		}
		if ok, _ := filepath.Match(pattern, name); ok {
			return true
		}
		if ok, _ := filepath.Match(filepath.FromSlash(pattern), filepath.FromSlash(display)); ok {
			return true
		}
	}
	return false
}

type lsIgnorePattern struct {
	Base          string
	Pattern       string
	DirectoryOnly bool
}

func loadLSIgnorePatterns(workspace string, dir string) []lsIgnorePattern {
	patterns := []lsIgnorePattern{}
	for _, base := range lsIgnoreBases(workspace, dir) {
		for _, filename := range []string{".gitignore", ".clawignore", ".claudeignore", ".codogignore"} {
			data, err := os.ReadFile(filepath.Join(base, filename))
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
					continue
				}
				line = strings.TrimPrefix(filepath.ToSlash(line), "/")
				directoryOnly := strings.HasSuffix(line, "/")
				line = strings.TrimSuffix(line, "/")
				if line == "" {
					continue
				}
				patterns = append(patterns, lsIgnorePattern{
					Base:          base,
					Pattern:       line,
					DirectoryOnly: directoryOnly,
				})
			}
		}
	}
	return patterns
}

func lsIgnoreBases(workspace string, dir string) []string {
	workspace = filepath.Clean(workspace)
	dir = filepath.Clean(dir)
	rel, err := filepath.Rel(workspace, dir)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return []string{dir}
	}
	bases := []string{workspace}
	if rel == "." {
		return bases
	}
	current := workspace
	for _, component := range strings.Split(filepath.ToSlash(rel), "/") {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		bases = append(bases, current)
	}
	return bases
}

func ignoredByLSIgnoreFiles(fullPath string, name string, isDir bool, patterns []lsIgnorePattern) bool {
	for _, pattern := range patterns {
		if pattern.DirectoryOnly && !isDir {
			continue
		}
		rel, err := filepath.Rel(pattern.Base, fullPath)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			continue
		}
		rel = filepath.ToSlash(rel)
		if lsIgnorePatternMatches(pattern.Pattern, rel, name) {
			return true
		}
	}
	return false
}

func lsIgnorePatternMatches(pattern string, rel string, name string) bool {
	if strings.Contains(pattern, "/") {
		if ok, _ := path.Match(pattern, rel); ok {
			return true
		}
		return rel == pattern || strings.HasPrefix(rel, pattern+"/")
	}
	if ok, _ := path.Match(pattern, name); ok {
		return true
	}
	return name == pattern
}

type WebFetchTool struct{}

func (WebFetchTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "web_fetch",
		Description: "Fetch an HTTP or HTTPS URL and return extracted text, metadata, and a bounded summary.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":        map[string]any{"type": "string"},
				"prompt":     map[string]any{"type": "string"},
				"timeout_ms": map[string]any{"type": "integer", "minimum": 1},
				"max_bytes":  map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{"url", "prompt"},
			"additionalProperties": false,
		},
	}
}

func (WebFetchTool) Permission() Permission { return PermissionReadOnly }

func (WebFetchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		URL       string  `json:"url"`
		Prompt    *string `json:"prompt"`
		TimeoutMS int     `json:"timeout_ms,omitempty"`
		MaxBytes  int64   `json:"max_bytes,omitempty"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.Prompt == nil {
		return "", errors.New("prompt is required")
	}
	result, err := webaccess.Fetch(ctx, webaccess.FetchInput{
		URL:       payload.URL,
		Prompt:    *payload.Prompt,
		TimeoutMS: payload.TimeoutMS,
		MaxBytes:  payload.MaxBytes,
	})
	if err != nil {
		return "", err
	}
	return pretty(result), nil
}

type WebSearchTool struct{}

func (WebSearchTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "web_search",
		Description: "Search the web using the configured search endpoint and return result titles, URLs, and snippets.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":           map[string]any{"type": "string", "minLength": 2},
				"max_results":     map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
				"allowed_domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"blocked_domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"timeout_ms":      map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
	}
}

func (WebSearchTool) Permission() Permission { return PermissionReadOnly }

func (WebSearchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload webaccess.SearchInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	result, err := webaccess.Search(ctx, payload)
	if err != nil {
		return "", err
	}
	return pretty(result), nil
}

// RetrieveContextTool queries an external workspace RAG service.
type RetrieveContextTool struct {
	BaseURL string
	Timeout time.Duration
	TopKMax int
}

func (RetrieveContextTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "retrieve_context",
		Description: "Semantic search over the configured workspace RAG index. Returns matching paths, scores, and snippets.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Natural-language search query.",
				},
				"top_k": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Maximum number of hits to return. Defaults to 8 and is capped locally.",
				},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
	}
}

func (RetrieveContextTool) Permission() Permission { return PermissionReadOnly }

func (t RetrieveContextTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k,omitempty"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	query := strings.TrimSpace(payload.Query)
	if query == "" {
		return "", errors.New("query is required")
	}
	if utf8.RuneCountInString(query) > maxRAGQueryChars {
		return "", fmt.Errorf("query too long: max %d characters", maxRAGQueryChars)
	}
	endpoint, err := ragQueryEndpoint(t.BaseURL)
	if err != nil {
		return "", err
	}
	topKMax := t.TopKMax
	if topKMax <= 0 {
		topKMax = 32
	}
	topK := payload.TopK
	if topK <= 0 {
		topK = 8
	}
	topK = min(max(topK, 1), topKMax)
	requestBody, err := json.Marshal(map[string]any{"query": query, "top_k": topK})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: t.Timeout}
	if client.Timeout <= 0 {
		client.Timeout = 30 * time.Second
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("RAG request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRAGBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("RAG response body: %w", err)
	}
	if int64(len(body)) > maxRAGBodyBytes {
		return "", fmt.Errorf("RAG response exceeded %d bytes", maxRAGBodyBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("RAG HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	formatted, err := formatRAGQueryJSONForModel(body)
	if err != nil {
		return "", fmt.Errorf("%w\nraw: %s", err, strings.TrimSpace(string(body)))
	}
	return formatted, nil
}

func ragQueryEndpoint(baseURL string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "", errors.New("RAG base URL is not configured")
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("RAG base URL must use http or https, got %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", errors.New("RAG base URL must include a host")
	}
	return base + "/v1/query", nil
}

func formatRAGQueryJSONForModel(body []byte) (string, error) {
	var payload struct {
		Phase any `json:"phase"`
		Hits  []struct {
			Path    string   `json:"path"`
			Snippet string   `json:"snippet"`
			Score   *float64 `json:"score"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("invalid JSON: %w", err)
	}
	phase, ok := payload.Phase.(string)
	if !ok || phase == "" {
		return "", unknownRAGPhaseError(payload.Phase, "RAG response is missing a string phase")
	}
	if !knownRAGPhase(phase) {
		return "", unknownRAGPhaseError(phase, "RAG response phase is not recognized")
	}
	var out strings.Builder
	fmt.Fprintf(&out, "phase: %s\n", phase)
	if payload.Hits == nil {
		return "", errors.New("missing hits array")
	}
	if len(payload.Hits) == 0 {
		out.WriteString("(no hits)\n")
		return out.String(), nil
	}
	for index, hit := range payload.Hits {
		fmt.Fprintf(&out, "%d. ", index+1)
		if hit.Score != nil {
			fmt.Fprintf(&out, "score=%.4f ", *hit.Score)
		}
		fmt.Fprintf(&out, "path=%s\n", hit.Path)
		lines := strings.Split(hit.Snippet, "\n")
		for lineIndex, line := range lines {
			if lineIndex >= 32 {
				out.WriteString("    ...\n")
				break
			}
			fmt.Fprintf(&out, "    %s\n", line)
		}
		out.WriteString("\n")
	}
	return out.String(), nil
}

func knownRAGPhase(phase string) bool {
	switch phase {
	case "1-sqlite-no-db", "1-sqlite-empty", "1-sqlite", "2-qdrant":
		return true
	default:
		return false
	}
}

func unknownRAGPhaseError(value any, message string) error {
	return fmt.Errorf("%s", pretty(map[string]any{
		"kind":           "unknown_bootstrap_phase",
		"field":          "phase",
		"received":       value,
		"allowed_values": []string{"1-sqlite-no-db", "1-sqlite-empty", "1-sqlite", "2-qdrant"},
		"message":        message,
	}))
}

type RemoteTriggerTool struct{}

func (RemoteTriggerTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "remote_trigger",
		Description: "Trigger a remote HTTP action or webhook endpoint.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":     map[string]any{"type": "string"},
				"method":  map[string]any{"type": "string", "enum": []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD"}},
				"headers": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
				"body":    map[string]any{"type": "string"},
				"timeout_ms": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Optional request timeout in milliseconds. Defaults to 30000.",
				},
				"max_bytes": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Maximum response body bytes to return, capped at 2000000.",
				},
			},
			"required":             []string{"url"},
			"additionalProperties": false,
		},
	}
}

func (RemoteTriggerTool) Permission() Permission { return PermissionDanger }

func (RemoteTriggerTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		URL       string            `json:"url"`
		Method    string            `json:"method"`
		Headers   map[string]string `json:"headers"`
		Body      string            `json:"body"`
		TimeoutMS int               `json:"timeout_ms"`
		MaxBytes  int64             `json:"max_bytes"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	requestURL, err := validateRemoteTriggerURL(payload.URL)
	if err != nil {
		return "", err
	}
	method := strings.ToUpper(strings.TrimSpace(payload.Method))
	if method == "" {
		method = http.MethodGet
	}
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead:
	default:
		return "", fmt.Errorf("unsupported HTTP method: %s", method)
	}
	timeout := 30 * time.Second
	if payload.TimeoutMS > 0 {
		timeout = time.Duration(payload.TimeoutMS) * time.Millisecond
	}
	limit := payload.MaxBytes
	if limit <= 0 {
		limit = 1024 * 1024
	}
	if limit > maxRemoteBodyBytes {
		limit = maxRemoteBodyBytes
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, method, requestURL.String(), strings.NewReader(payload.Body))
	if err != nil {
		return "", err
	}
	for key, value := range payload.Headers {
		if strings.TrimSpace(key) == "" {
			continue
		}
		req.Header.Set(key, value)
	}
	if payload.Body != "" && req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "text/plain; charset=utf-8")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", err
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	return pretty(map[string]any{
		"url":         requestURL.String(),
		"final_url":   resp.Request.URL.String(),
		"method":      method,
		"status_code": resp.StatusCode,
		"status":      resp.Status,
		"headers":     resp.Header,
		"bytes":       len(data),
		"truncated":   truncated,
		"body":        string(data),
		"duration_ms": time.Since(started).Milliseconds(),
	}), nil
}

func validateRemoteTriggerURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("url must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("url host is required")
	}
	return parsed, nil
}

type PermissionCheckTool struct{}

var permissionValueNames = []string{
	string(PermissionReadOnly),
	string(PermissionWorkspace),
	string(PermissionDanger),
	string(PermissionPrompt),
	string(PermissionAllow),
}

func (PermissionCheckTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "permission_check",
		Description: "Dry-run the current permission policy for a target tool without executing that tool.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"target_tool": map[string]any{"type": "string"},
				"tool":        map[string]any{"type": "string"},
				"required_permission": map[string]any{
					"type": "string",
					"enum": append([]string(nil), permissionValueNames...),
				},
				"input":  map[string]any{"type": "object", "additionalProperties": true},
				"action": map[string]any{"type": "string", "description": "Deprecated compatibility alias used as the target label when target_tool is omitted."},
			},
		},
	}
}
