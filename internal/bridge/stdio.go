package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/session"
)

type Server struct {
	Sessions   *session.Store
	Version    string
	Workspace  string
	ConfigHome string
	TrustToken string
}

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s Server) Serve(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			_ = encoder.Encode(Response{JSONRPC: "2.0", Error: &Error{Code: -32700, Message: err.Error()}})
			continue
		}
		var result any
		var rpcErr *Error
		if req.Method == "background/watch" {
			result, rpcErr = s.backgroundWatch(req.Params, encoder)
		} else {
			result, rpcErr = s.handle(req)
		}
		resp := Response{JSONRPC: "2.0", ID: json.RawMessage(req.ID), Result: result, Error: rpcErr}
		_ = encoder.Encode(resp)
	}
	return scanner.Err()
}

func (s Server) handle(req Request) (any, *Error) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"name":    "codog",
			"version": s.Version,
			"capabilities": []string{
				"sessions/list",
				"sessions/open",
				"sessions/get",
				"sessions/append_message",
				"sessions/append_input",
				"sessions/rewind",
				"workspace/info",
				"workspace/files",
				"workspace/search",
				"file/read",
				"file/write",
				"file/edit",
				"file/diff",
				"editor/identify",
				"editor/state",
				"editor/open",
				"editor/selection",
				"diagnostics/go",
				"background/watch",
			},
		}, nil
	case "workspace/info":
		workspace, err := s.workspace()
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return map[string]any{"path": workspace, "name": filepath.Base(workspace)}, nil
	case "workspace/files":
		result, err := s.workspaceFiles(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "workspace/search":
		result, err := s.workspaceSearch(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "sessions/list":
		sessions, err := s.Sessions.List()
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return sessions, nil
	case "sessions/open":
		result, err := s.sessionOpen(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "sessions/get":
		result, err := s.sessionGet(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "sessions/append_message":
		result, err := s.sessionAppendMessage(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "sessions/append_input":
		result, err := s.sessionAppendInput(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "sessions/rewind":
		result, err := s.sessionRewind(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "file/read":
		result, err := s.readFile(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "file/write":
		result, err := s.writeFile(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "file/edit":
		result, err := s.editFile(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "file/diff":
		result, err := s.diffFile(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "editor/identify":
		result, err := s.editorIdentify(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "editor/state":
		result, err := s.editorState()
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "editor/open":
		result, err := s.editorOpen(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "editor/selection":
		result, err := s.editorSelection(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "diagnostics/go":
		result, err := s.goDiagnostics(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	default:
		return nil, &Error{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)}
	}
}

type fileEntry struct {
	Path    string    `json:"path"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size,omitempty"`
	ModTime time.Time `json:"mod_time,omitempty"`
}

type searchMatch struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Text   string `json:"text"`
	Column int    `json:"column,omitempty"`
}

func (s Server) goDiagnostics(params json.RawMessage) (any, error) {
	var payload struct {
		Patterns []string `json:"patterns"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &payload); err != nil {
			return nil, err
		}
	}
	workspace, err := s.workspace()
	if err != nil {
		return nil, err
	}
	return codeintel.GoDiagnostics(context.Background(), workspace, payload.Patterns)
}

func (s Server) backgroundWatch(params json.RawMessage, encoder *json.Encoder) (any, *Error) {
	var payload struct {
		ID         string `json:"id"`
		Offset     int64  `json:"offset"`
		IntervalMS int    `json:"interval_ms"`
		MaxEvents  int    `json:"max_events"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, &Error{Code: -32000, Message: err.Error()}
	}
	if strings.TrimSpace(payload.ID) == "" {
		return nil, &Error{Code: -32000, Message: "id is required"}
	}
	if strings.TrimSpace(s.ConfigHome) == "" {
		return nil, &Error{Code: -32000, Message: "config home is required"}
	}
	options := background.WatchOptions{Offset: payload.Offset, MaxEvents: payload.MaxEvents}
	if payload.IntervalMS > 0 {
		options.Interval = time.Duration(payload.IntervalMS) * time.Millisecond
	}
	count := 0
	err := background.NewStore(s.ConfigHome).Watch(context.Background(), payload.ID, options, func(event background.WatchEvent) error {
		count++
		return encoder.Encode(Notification{JSONRPC: "2.0", Method: "background/event", Params: event})
	})
	if err != nil {
		return nil, &Error{Code: -32000, Message: err.Error()}
	}
	return map[string]any{"id": payload.ID, "events": count}, nil
}

func (s Server) sessionOpen(params json.RawMessage) (any, error) {
	var payload struct {
		ID string `json:"id"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &payload); err != nil {
			return nil, err
		}
	}
	return s.Sessions.Open(strings.TrimSpace(payload.ID))
}

func (s Server) sessionGet(params json.RawMessage) (any, error) {
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.ID) == "" {
		return nil, errors.New("id is required")
	}
	return s.Sessions.Open(strings.TrimSpace(payload.ID))
}

func (s Server) sessionAppendMessage(params json.RawMessage) (any, error) {
	var payload struct {
		ID      string             `json:"id"`
		Message *anthropic.Message `json:"message"`
		Role    string             `json:"role"`
		Text    string             `json:"text"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(payload.ID)
	if id == "" {
		return nil, errors.New("id is required")
	}
	var msg anthropic.Message
	if payload.Message != nil {
		msg = *payload.Message
	} else {
		role := strings.TrimSpace(payload.Role)
		text := strings.TrimSpace(payload.Text)
		if role == "" {
			role = "user"
		}
		if text == "" {
			return nil, errors.New("text is required")
		}
		msg = anthropic.TextMessage(role, text)
	}
	if msg.Role == "" || len(msg.Content) == 0 {
		return nil, errors.New("message role and content are required")
	}
	if err := s.Sessions.Append(id, msg); err != nil {
		return nil, err
	}
	return s.Sessions.Open(id)
}

func (s Server) sessionAppendInput(params json.RawMessage) (any, error) {
	var payload struct {
		ID    string `json:"id"`
		Input string `json:"input"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(payload.ID)
	if id == "" {
		return nil, errors.New("id is required")
	}
	if strings.TrimSpace(payload.Input) == "" {
		return nil, errors.New("input is required")
	}
	if err := s.Sessions.AppendInput(id, payload.Input); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "input": payload.Input}, nil
}

func (s Server) sessionRewind(params json.RawMessage) (any, error) {
	var payload struct {
		ID             string `json:"id"`
		RemoveMessages int    `json:"remove_messages"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(payload.ID)
	if id == "" {
		return nil, errors.New("id is required")
	}
	return s.Sessions.Rewind(id, payload.RemoveMessages)
}

func (s Server) workspaceFiles(params json.RawMessage) (any, error) {
	var payload struct {
		Path          string `json:"path"`
		Pattern       string `json:"pattern"`
		Limit         int    `json:"limit"`
		IncludeHidden bool   `json:"include_hidden"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &payload); err != nil {
			return nil, err
		}
	}
	root, relRoot, err := s.resolveWorkspacePath(payload.Path)
	if err != nil {
		return nil, err
	}
	pattern := payload.Pattern
	if pattern == "" {
		pattern = "*"
	}
	limit := boundedLimit(payload.Limit, 500, 5000)
	entries := []fileEntry{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := s.rel(path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if shouldSkipBridgePath(rel, entry, payload.IncludeHidden) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		ok, err := bridgePatternMatch(pattern, rel, entry.Name())
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		entries = append(entries, fileEntry{Path: rel, IsDir: entry.IsDir(), Size: info.Size(), ModTime: info.ModTime().UTC()})
		if len(entries) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return map[string]any{"root": relRoot, "files": entries, "truncated": len(entries) >= limit}, nil
}

func (s Server) workspaceSearch(params json.RawMessage) (any, error) {
	var payload struct {
		Query         string `json:"query"`
		Path          string `json:"path"`
		Glob          string `json:"glob"`
		Regex         bool   `json:"regex"`
		Limit         int    `json:"limit"`
		IncludeHidden bool   `json:"include_hidden"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	if payload.Query == "" {
		return nil, errors.New("query is required")
	}
	root, _, err := s.resolveWorkspacePath(payload.Path)
	if err != nil {
		return nil, err
	}
	limit := boundedLimit(payload.Limit, 100, 1000)
	var expr *regexp.Regexp
	if payload.Regex {
		expr, err = regexp.Compile(payload.Query)
		if err != nil {
			return nil, err
		}
	}
	matches := []searchMatch{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := s.rel(path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if shouldSkipBridgePath(rel, entry, payload.IncludeHidden) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if payload.Glob != "" {
			ok, err := bridgePatternMatch(payload.Glob, rel, entry.Name())
			if err != nil || !ok {
				return err
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(data) > 1024*1024 || bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			column := 0
			found := false
			if expr != nil {
				loc := expr.FindStringIndex(line)
				if loc != nil {
					found = true
					column = loc[0] + 1
				}
			} else if idx := strings.Index(line, payload.Query); idx >= 0 {
				found = true
				column = idx + 1
			}
			if !found {
				continue
			}
			matches = append(matches, searchMatch{Path: rel, Line: i + 1, Text: line, Column: column})
			if len(matches) >= limit {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"matches": matches, "truncated": len(matches) >= limit}, nil
}

func (s Server) readFile(params json.RawMessage) (any, error) {
	var payload struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	path, rel, err := s.resolve(payload.Path, false)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if payload.Offset < 0 {
		payload.Offset = 0
	}
	if payload.Offset > len(data) {
		payload.Offset = len(data)
	}
	limit := payload.Limit
	if limit <= 0 {
		limit = 64 * 1024
	}
	end := payload.Offset + limit
	if end > len(data) {
		end = len(data)
	}
	return map[string]any{
		"path":      rel,
		"content":   string(data[payload.Offset:end]),
		"bytes":     len(data),
		"truncated": end < len(data),
	}, nil
}

func (s Server) writeFile(params json.RawMessage) (any, error) {
	var payload struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	path, rel, err := s.resolve(payload.Path, true)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(payload.Content), 0o644); err != nil {
		return nil, err
	}
	return map[string]any{"path": rel, "bytes": len(payload.Content)}, nil
}

func (s Server) editFile(params json.RawMessage) (any, error) {
	var payload struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	if payload.OldString == "" {
		return nil, errors.New("old_string is required")
	}
	path, rel, err := s.resolve(payload.Path, false)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content := string(data)
	count := strings.Count(content, payload.OldString)
	if count == 0 {
		return nil, errors.New("old_string was not found")
	}
	if count > 1 && !payload.ReplaceAll {
		return nil, fmt.Errorf("old_string appears %d times; set replace_all to true", count)
	}
	limit := 1
	if payload.ReplaceAll {
		limit = -1
	}
	updated := strings.Replace(content, payload.OldString, payload.NewString, limit)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return nil, err
	}
	replacements := 1
	if payload.ReplaceAll {
		replacements = count
	}
	return map[string]any{"path": rel, "replacements": replacements}, nil
}

func (s Server) diffFile(params json.RawMessage) (any, error) {
	var payload struct {
		Path      string `json:"path"`
		Original  string `json:"original"`
		Updated   string `json:"updated"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	path, rel, err := s.resolve(payload.Path, false)
	if err != nil {
		return nil, err
	}
	original := payload.Original
	if original == "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		original = string(data)
	}
	updated := payload.Updated
	if updated == "" {
		if payload.OldString == "" {
			return nil, errors.New("updated or old_string is required")
		}
		if !strings.Contains(original, payload.OldString) {
			return nil, errors.New("old_string was not found")
		}
		updated = strings.Replace(original, payload.OldString, payload.NewString, 1)
	}
	return map[string]any{"path": rel, "diff": unifiedDiff(rel, original, updated)}, nil
}

func (s Server) resolve(requested string, allowMissing bool) (string, string, error) {
	if strings.TrimSpace(requested) == "" {
		return "", "", errors.New("path is required")
	}
	workspace, err := s.workspace()
	if err != nil {
		return "", "", err
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", "", err
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate = filepath.Clean(candidate)
	resolved := candidate
	if allowMissing {
		parent, err := filepath.EvalSymlinks(filepath.Dir(candidate))
		if err != nil {
			return "", "", err
		}
		resolved = filepath.Join(parent, filepath.Base(candidate))
	} else {
		resolved, err = filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", "", err
		}
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("path escapes workspace: %s", requested)
	}
	return resolved, rel, nil
}

func (s Server) resolveWorkspacePath(requested string) (string, string, error) {
	if strings.TrimSpace(requested) == "" {
		workspace, err := s.workspace()
		if err != nil {
			return "", "", err
		}
		root, err := filepath.EvalSymlinks(workspace)
		if err != nil {
			return "", "", err
		}
		return root, ".", nil
	}
	return s.resolve(requested, false)
}

func (s Server) rel(path string) (string, error) {
	workspace, err := s.workspace()
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	return rel, nil
}

func (s Server) workspace() (string, error) {
	if s.Workspace != "" {
		return filepath.Abs(s.Workspace)
	}
	return os.Getwd()
}

func boundedLimit(value, defaultValue, maxValue int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func shouldSkipBridgePath(rel string, entry os.DirEntry, includeHidden bool) bool {
	base := entry.Name()
	if entry.IsDir() && (base == ".git" || base == ".codog" || base == "node_modules") {
		return true
	}
	return !includeHidden && strings.HasPrefix(base, ".")
}

func bridgePatternMatch(pattern string, rel string, base string) (bool, error) {
	target := base
	if strings.Contains(pattern, "/") {
		target = filepath.ToSlash(rel)
		pattern = filepath.ToSlash(pattern)
	}
	return filepath.Match(pattern, target)
}

func unifiedDiff(path string, original string, updated string) string {
	if original == updated {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("--- a/" + path + "\n")
	builder.WriteString("+++ b/" + path + "\n")
	builder.WriteString("@@\n")
	oldLines := splitDiffLines(original)
	newLines := splitDiffLines(updated)
	for _, line := range oldLines {
		builder.WriteString("-" + line + "\n")
	}
	for _, line := range newLines {
		builder.WriteString("+" + line + "\n")
	}
	return builder.String()
}

func splitDiffLines(value string) []string {
	value = strings.TrimSuffix(value, "\n")
	if value == "" {
		return nil
	}
	return strings.Split(value, "\n")
}
