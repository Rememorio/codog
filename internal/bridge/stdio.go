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
				"sessions/history",
				"sessions/rename",
				"sessions/delete",
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
				"bridge/faults/list",
				"bridge/faults/record",
				"bridge/faults/clear",
				"diagnostics/go",
				"code/symbols",
				"code/references",
				"code/definition",
				"code/hover",
				"code/completion",
				"code/format",
				"notebook/read",
				"notebook/edit",
				"background/list",
				"background/run",
				"background/get",
				"background/logs",
				"background/board",
				"background/heartbeat",
				"background/stop",
				"background/restart",
				"background/watch",
				"background/prune",
				"background/supervise",
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
	case "sessions/history":
		result, err := s.sessionHistory(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "sessions/rename":
		result, err := s.sessionRename(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "sessions/delete":
		result, err := s.sessionDelete(req.Params)
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
	case "bridge/faults/list":
		result, err := s.bridgeFaultsList()
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "bridge/faults/record":
		result, err := s.bridgeFaultsRecord(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "bridge/faults/clear":
		result, err := s.bridgeFaultsClear()
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
	case "code/completion":
		result, err := s.codeCompletion(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "code/format":
		result, err := s.codeFormat(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "notebook/read":
		result, err := s.notebookRead(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "notebook/edit":
		result, err := s.notebookEdit(req.Params)
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
	case "background/board":
		result, err := s.backgroundBoard(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "background/heartbeat":
		result, err := s.backgroundHeartbeat(req.Params)
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
	case "background/prune":
		result, err := s.backgroundPrune(req.Params)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "background/supervise":
		result, err := s.backgroundSupervise(req.Params)
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

func (s Server) codeCompletion(params json.RawMessage) (any, error) {
	var payload struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	workspace, err := s.workspace()
	if err != nil {
		return nil, err
	}
	completions, err := codeintel.Completions(workspace, payload.Query, payload.Limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"kind": "completion", "query": strings.TrimSpace(payload.Query), "total": len(completions), "completions": completions}, nil
}

func (s Server) codeFormat(params json.RawMessage) (any, error) {
	var payload struct {
		Path  string `json:"path"`
		Write bool   `json:"write"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	workspace, err := s.workspace()
	if err != nil {
		return nil, err
	}
	result, err := codeintel.FormatGoFile(workspace, payload.Path, payload.Write)
	if err != nil {
		return nil, err
	}
	return map[string]any{"kind": "format", "write": payload.Write, "result": result}, nil
}

func (s Server) notebookRead(params json.RawMessage) (any, error) {
	var payload struct {
		Path           string `json:"path"`
		NotebookPath   string `json:"notebook_path"`
		CellIndex      *int   `json:"cell_index"`
		Index          *int   `json:"index"`
		Limit          int    `json:"limit"`
		IncludeOutputs bool   `json:"include_outputs"`
		Outputs        *bool  `json:"outputs"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	cellIndex := payload.CellIndex
	if cellIndex == nil {
		cellIndex = payload.Index
	}
	if cellIndex != nil && *cellIndex < 0 {
		return nil, errors.New("cell_index must be non-negative")
	}
	includeOutputs := payload.IncludeOutputs
	if payload.Outputs != nil {
		includeOutputs = *payload.Outputs
	}
	absPath, relPath, err := s.resolveNotebookPath(firstNonEmpty(payload.NotebookPath, payload.Path))
	if err != nil {
		return nil, err
	}
	result, err := codeintel.ReadNotebook(absPath, codeintel.NotebookReadOptions{
		CellIndex:      cellIndex,
		Limit:          payload.Limit,
		IncludeOutputs: includeOutputs,
	})
	if err != nil {
		return nil, err
	}
	result.Path = relPath
	return result, nil
}

func (s Server) notebookEdit(params json.RawMessage) (any, error) {
	var payload struct {
		Path         string  `json:"path"`
		NotebookPath string  `json:"notebook_path"`
		Mode         string  `json:"mode"`
		EditMode     string  `json:"edit_mode"`
		CellIndex    *int    `json:"cell_index"`
		Index        *int    `json:"index"`
		CellID       string  `json:"cell_id"`
		CellType     string  `json:"cell_type"`
		Type         string  `json:"type"`
		Source       *string `json:"source"`
		NewSource    *string `json:"new_source"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	cellIndex := payload.CellIndex
	if cellIndex == nil {
		cellIndex = payload.Index
	}
	if cellIndex != nil && *cellIndex < 0 {
		return nil, errors.New("cell_index must be non-negative")
	}
	if cellIndex != nil && strings.TrimSpace(payload.CellID) != "" {
		return nil, errors.New("notebook/edit accepts either cell_index or cell_id, not both")
	}
	mode := strings.ToLower(firstNonEmpty(payload.Mode, payload.EditMode))
	if mode == "" {
		mode = "replace"
	}
	switch mode {
	case "replace", "insert", "delete":
	default:
		return nil, fmt.Errorf("unknown notebook edit mode %q", mode)
	}
	source, sourceSet := "", false
	if payload.Source != nil {
		source = *payload.Source
		sourceSet = true
	}
	if payload.NewSource != nil {
		source = *payload.NewSource
		sourceSet = true
	}
	if (mode == "replace" || mode == "insert") && !sourceSet {
		return nil, errors.New("new_source is required for insert and replace edits")
	}
	absPath, relPath, err := s.resolveNotebookPath(firstNonEmpty(payload.NotebookPath, payload.Path))
	if err != nil {
		return nil, err
	}
	index, err := codeintel.ResolveNotebookEditIndex(absPath, cellIndex, payload.CellID, mode)
	if err != nil {
		return nil, err
	}
	result, err := codeintel.EditNotebook(absPath, codeintel.NotebookEditOptions{
		Index:    index,
		CellType: firstNonEmpty(payload.CellType, payload.Type),
		Source:   source,
		Mode:     mode,
	})
	if err != nil {
		return nil, err
	}
	result.Path = relPath
	return result, nil
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

func (s Server) backgroundBoard(params json.RawMessage) (any, error) {
	var payload struct {
		StalledAfterSeconds int `json:"stalled_after_seconds"`
		StalledAfterMS      int `json:"stalled_after_ms"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &payload); err != nil {
			return nil, err
		}
	}
	stalledAfter := 30 * time.Second
	switch {
	case payload.StalledAfterMS < 0:
		return nil, errors.New("stalled_after_ms must be non-negative")
	case payload.StalledAfterSeconds < 0:
		return nil, errors.New("stalled_after_seconds must be non-negative")
	case payload.StalledAfterMS > 0:
		stalledAfter = time.Duration(payload.StalledAfterMS) * time.Millisecond
	case payload.StalledAfterSeconds > 0:
		stalledAfter = time.Duration(payload.StalledAfterSeconds) * time.Second
	}
	store, err := s.backgroundStore()
	if err != nil {
		return nil, err
	}
	return store.LaneBoard(stalledAfter)
}

func (s Server) backgroundHeartbeat(params json.RawMessage) (any, error) {
	var payload struct {
		ID             string     `json:"id"`
		Status         string     `json:"status"`
		TransportAlive *bool      `json:"transport_alive"`
		ObservedAt     *time.Time `json:"observed_at"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(payload.ID)
	if id == "" {
		return nil, errors.New("id is required")
	}
	transportAlive := true
	if payload.TransportAlive != nil {
		transportAlive = *payload.TransportAlive
	}
	heartbeat := background.LaneHeartbeat{
		TransportAlive: transportAlive,
		Status:         payload.Status,
	}
	if payload.ObservedAt != nil {
		heartbeat.ObservedAt = *payload.ObservedAt
	}
	store, err := s.backgroundStore()
	if err != nil {
		return nil, err
	}
	return store.UpdateHeartbeat(id, heartbeat)
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

func (s Server) backgroundPrune(params json.RawMessage) (any, error) {
	options := background.DefaultPruneOptions()
	if len(params) != 0 {
		var payload struct {
			OlderThanSeconds int  `json:"older_than_seconds"`
			OlderThanDays    int  `json:"older_than_days"`
			Keep             *int `json:"keep"`
		}
		if err := json.Unmarshal(params, &payload); err != nil {
			return nil, err
		}
		switch {
		case payload.OlderThanSeconds < 0:
			return nil, errors.New("older_than_seconds must be non-negative")
		case payload.OlderThanDays < 0:
			return nil, errors.New("older_than_days must be non-negative")
		case payload.OlderThanSeconds > 0:
			options.OlderThan = time.Duration(payload.OlderThanSeconds) * time.Second
		case payload.OlderThanDays > 0:
			options.OlderThan = time.Duration(payload.OlderThanDays) * 24 * time.Hour
		}
		if payload.Keep != nil {
			if *payload.Keep < 0 {
				return nil, errors.New("keep must be non-negative")
			}
			options.Keep = *payload.Keep
		}
	}
	store, err := s.backgroundStore()
	if err != nil {
		return nil, err
	}
	return store.Prune(options)
}

func (s Server) backgroundSupervise(params json.RawMessage) (any, error) {
	now := time.Now().UTC()
	if len(params) != 0 {
		var payload struct {
			Now *time.Time `json:"now"`
		}
		if err := json.Unmarshal(params, &payload); err != nil {
			return nil, err
		}
		if payload.Now != nil {
			now = payload.Now.UTC()
		}
	}
	store, err := s.backgroundStore()
	if err != nil {
		return nil, err
	}
	return store.SuperviseOnce(now)
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

func (s Server) sessionHistory(params json.RawMessage) (any, error) {
	var payload struct {
		ID        string `json:"id"`
		SessionID string `json:"session_id"`
		Limit     int    `json:"limit"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	id := firstNonEmpty(payload.ID, payload.SessionID)
	if id == "" {
		return nil, errors.New("id is required")
	}
	entries, err := s.Sessions.PromptHistory(id)
	if err != nil {
		return nil, err
	}
	if payload.Limit > 0 && len(entries) > payload.Limit {
		entries = entries[len(entries)-payload.Limit:]
	}
	return map[string]any{
		"kind":    "session_history",
		"id":      id,
		"count":   len(entries),
		"entries": entries,
	}, nil
}

func (s Server) sessionRename(params json.RawMessage) (any, error) {
	var payload struct {
		ID           string `json:"id"`
		SessionID    string `json:"session_id"`
		NewID        string `json:"new_id"`
		NewSessionID string `json:"new_session_id"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	id := firstNonEmpty(payload.ID, payload.SessionID)
	if id == "" {
		return nil, errors.New("id is required")
	}
	newID := firstNonEmpty(payload.NewID, payload.NewSessionID)
	if newID == "" {
		return nil, errors.New("new_id is required")
	}
	result, err := s.Sessions.Rename(id, newID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"kind":          "session_mutation",
		"action":        "rename",
		"status":        "ok",
		"old_id":        result.OldID,
		"new_id":        result.NewID,
		"old_path":      result.OldPath,
		"new_path":      result.NewPath,
		"message_count": result.MessageCount,
	}, nil
}

func (s Server) sessionDelete(params json.RawMessage) (any, error) {
	var payload struct {
		ID        string `json:"id"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	id := firstNonEmpty(payload.ID, payload.SessionID)
	if id == "" {
		return nil, errors.New("id is required")
	}
	sess, err := s.Sessions.Open(id)
	if err != nil {
		return nil, err
	}
	if err := s.Sessions.Delete(id); err != nil {
		return nil, err
	}
	return map[string]any{
		"kind":          "session_mutation",
		"action":        "delete",
		"status":        "ok",
		"id":            sess.ID,
		"path":          sess.Path,
		"message_count": len(sess.Messages),
	}, nil
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

func (s Server) resolveNotebookPath(requested string) (string, string, error) {
	absPath, relPath, err := s.resolve(requested, false)
	if err != nil {
		return "", "", err
	}
	if !strings.EqualFold(filepath.Ext(absPath), ".ipynb") {
		return "", "", errors.New("notebook path must point to a .ipynb file")
	}
	return absPath, relPath, nil
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
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
