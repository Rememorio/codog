package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/workspaceops"
)

type Server struct {
	Sessions   *session.Store
	Version    string
	Workspace  string
	ConfigHome string
	TrustToken string
	Executable string
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
				"sessions/prompt",
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
				"code/symbols",
				"code/references",
				"code/definition",
				"code/hover",
				"background/list",
				"background/run",
				"background/get",
				"background/logs",
				"background/stop",
				"background/restart",
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
	case "sessions/prompt":
		result, err := s.sessionPrompt(req.Params)
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
	case "code/symbols":
		result, err := s.codeSymbols(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "code/references":
		result, err := s.codeReferences(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "code/definition":
		result, err := s.codeDefinition(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "code/hover":
		result, err := s.codeHover(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "background/list":
		result, err := s.backgroundList(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "background/run":
		result, err := s.backgroundRun(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "background/get":
		result, err := s.backgroundGet(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "background/logs":
		result, err := s.backgroundLogs(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "background/stop":
		result, err := s.backgroundStop(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "background/restart":
		result, err := s.backgroundRestart(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	default:
		return nil, &Error{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)}
	}
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

func (s Server) codeSymbols(params json.RawMessage) (any, error) {
	var payload struct {
		Path string `json:"path"`
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
	symbols, err := codeintel.GoSymbols(workspace)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.Path) != "" {
		_, rel, err := s.resolve(payload.Path, false)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		filtered := symbols[:0]
		for _, symbol := range symbols {
			if filepath.ToSlash(symbol.Path) == rel {
				filtered = append(filtered, symbol)
			}
		}
		symbols = filtered
	}
	return map[string]any{"kind": "symbols", "total": len(symbols), "symbols": symbols}, nil
}

func (s Server) codeReferences(params json.RawMessage) (any, error) {
	var payload struct {
		Symbol string `json:"symbol"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	workspace, err := s.workspace()
	if err != nil {
		return nil, err
	}
	refs, err := codeintel.References(workspace, payload.Symbol, payload.Limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"kind": "references", "symbol": strings.TrimSpace(payload.Symbol), "total": len(refs), "references": refs}, nil
}

func (s Server) codeDefinition(params json.RawMessage) (any, error) {
	var payload struct {
		Symbol string `json:"symbol"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	workspace, err := s.workspace()
	if err != nil {
		return nil, err
	}
	definition, found, err := codeintel.Definition(workspace, payload.Symbol)
	if err != nil {
		return nil, err
	}
	return map[string]any{"kind": "definition", "symbol": strings.TrimSpace(payload.Symbol), "found": found, "definition": definition}, nil
}

func (s Server) codeHover(params json.RawMessage) (any, error) {
	var payload struct {
		Symbol       string `json:"symbol"`
		ContextLines int    `json:"context_lines"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	workspace, err := s.workspace()
	if err != nil {
		return nil, err
	}
	hover, err := codeintel.HoverInfo(workspace, payload.Symbol, payload.ContextLines)
	if err != nil {
		return nil, err
	}
	return map[string]any{"kind": "hover", "symbol": strings.TrimSpace(payload.Symbol), "hover": hover}, nil
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

func (s Server) backgroundList(params json.RawMessage) (any, error) {
	var payload struct {
		SessionID string `json:"session_id"`
		Kind      string `json:"kind"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &payload); err != nil {
			return nil, err
		}
	}
	store, err := s.backgroundStore()
	if err != nil {
		return nil, err
	}
	tasks, err := store.List()
	if err != nil {
		return nil, err
	}
	tasks = background.FilterBySession(tasks, payload.SessionID)
	tasks = background.FilterByKind(tasks, payload.Kind)
	return tasks, nil
}

func (s Server) backgroundRun(params json.RawMessage) (any, error) {
	var payload struct {
		Command       string                    `json:"command"`
		Kind          string                    `json:"kind"`
		SessionID     string                    `json:"session_id"`
		RestartPolicy *background.RestartPolicy `json:"restart_policy"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.Command) == "" {
		return nil, errors.New("command is required")
	}
	store, err := s.backgroundStore()
	if err != nil {
		return nil, err
	}
	return store.RunWithOptions(payload.Command, s.Workspace, background.RunOptions{
		Kind:          payload.Kind,
		SessionID:     payload.SessionID,
		RestartPolicy: payload.RestartPolicy,
	})
}

func (s Server) backgroundGet(params json.RawMessage) (any, error) {
	id, err := parseBridgeBackgroundID(params)
	if err != nil {
		return nil, err
	}
	store, err := s.backgroundStore()
	if err != nil {
		return nil, err
	}
	return store.Status(id)
}

func (s Server) backgroundLogs(params json.RawMessage) (any, error) {
	var payload struct {
		ID    string `json:"id"`
		Limit int64  `json:"limit"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(payload.ID)
	if id == "" {
		return nil, errors.New("id is required")
	}
	limit := payload.Limit
	if limit <= 0 {
		limit = 64 * 1024
	}
	store, err := s.backgroundStore()
	if err != nil {
		return nil, err
	}
	logs, err := store.Logs(id, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "logs": logs}, nil
}

func (s Server) backgroundStop(params json.RawMessage) (any, error) {
	id, err := parseBridgeBackgroundID(params)
	if err != nil {
		return nil, err
	}
	store, err := s.backgroundStore()
	if err != nil {
		return nil, err
	}
	return store.Stop(id)
}

func (s Server) backgroundRestart(params json.RawMessage) (any, error) {
	id, err := parseBridgeBackgroundID(params)
	if err != nil {
		return nil, err
	}
	store, err := s.backgroundStore()
	if err != nil {
		return nil, err
	}
	return store.Restart(id, s.Workspace)
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

func (s Server) sessionPrompt(params json.RawMessage) (any, error) {
	var payload struct {
		ID     string `json:"id"`
		Prompt string `json:"prompt"`
		Kind   string `json:"kind"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(payload.ID)
	if id == "" {
		return nil, errors.New("id is required")
	}
	prompt := strings.TrimSpace(payload.Prompt)
	if prompt == "" {
		return nil, errors.New("prompt is required")
	}
	if strings.TrimSpace(s.ConfigHome) == "" {
		return nil, errors.New("config home is required")
	}
	executable := strings.TrimSpace(s.Executable)
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return nil, err
		}
	}
	kind := strings.TrimSpace(payload.Kind)
	if kind == "" {
		kind = "prompt"
	}
	command := strings.Join([]string{
		bridgeShellQuote(executable),
		"--resume",
		bridgeShellQuote(id),
		"prompt",
		bridgeShellQuote(prompt),
	}, " ")
	return background.NewStore(s.ConfigHome).RunWithOptions(command, s.Workspace, background.RunOptions{
		Kind:      kind,
		SessionID: id,
	})
}

func (s Server) workspaceFiles(params json.RawMessage) (any, error) {
	var payload workspaceops.FilesOptions
	if len(params) != 0 {
		if err := json.Unmarshal(params, &payload); err != nil {
			return nil, err
		}
	}
	return s.workspaceOps().Files(payload)
}

func (s Server) workspaceSearch(params json.RawMessage) (any, error) {
	var payload workspaceops.SearchOptions
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	return s.workspaceOps().Search(payload)
}

func (s Server) readFile(params json.RawMessage) (any, error) {
	var payload workspaceops.ReadOptions
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	return s.workspaceOps().Read(payload)
}

func (s Server) writeFile(params json.RawMessage) (any, error) {
	var payload workspaceops.WriteOptions
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	return s.workspaceOps().Write(payload)
}

func (s Server) editFile(params json.RawMessage) (any, error) {
	var payload workspaceops.EditOptions
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	return s.workspaceOps().Edit(payload)
}

func (s Server) diffFile(params json.RawMessage) (any, error) {
	var payload workspaceops.DiffOptions
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	return s.workspaceOps().Diff(payload)
}

func (s Server) resolve(requested string, allowMissing bool) (string, string, error) {
	return s.workspaceOps().Resolve(requested, allowMissing)
}

func (s Server) resolveWorkspacePath(requested string) (string, string, error) {
	return s.workspaceOps().ResolveWorkspacePath(requested)
}

func (s Server) rel(path string) (string, error) {
	return s.workspaceOps().Rel(path)
}

func (s Server) workspace() (string, error) {
	return s.workspaceOps().WorkspacePath()
}

func (s Server) workspaceOps() workspaceops.Service {
	return workspaceops.Service{Workspace: s.Workspace}
}

func (s Server) backgroundStore() (background.Store, error) {
	if strings.TrimSpace(s.ConfigHome) == "" {
		return background.Store{}, errors.New("config home is required")
	}
	return background.NewStore(s.ConfigHome), nil
}

func parseBridgeBackgroundID(params json.RawMessage) (string, error) {
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return "", err
	}
	id := strings.TrimSpace(payload.ID)
	if id == "" {
		return "", errors.New("id is required")
	}
	return id, nil
}

func bridgeShellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$`!*?[]{}()<>|&;") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
