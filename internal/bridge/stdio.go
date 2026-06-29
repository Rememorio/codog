package bridge

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Rememorio/codog/internal/future"
	"github.com/Rememorio/codog/internal/session"
)

type Server struct {
	Sessions  *session.Store
	Version   string
	Workspace string
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
		result, rpcErr := s.handle(req)
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
				"capabilities/list",
				"workspace/info",
				"file/read",
				"file/write",
				"file/edit",
			},
		}, nil
	case "capabilities/list":
		return future.NewReport(s.Version), nil
	case "workspace/info":
		workspace, err := s.workspace()
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return map[string]any{"path": workspace, "name": filepath.Base(workspace)}, nil
	case "sessions/list":
		sessions, err := s.Sessions.List()
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return sessions, nil
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
	default:
		return nil, &Error{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)}
	}
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

func (s Server) workspace() (string, error) {
	if s.Workspace != "" {
		return filepath.Abs(s.Workspace)
	}
	return os.Getwd()
}
