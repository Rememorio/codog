package acpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/workspaceops"
)

type Options struct {
	Version   string
	Workspace string
}

type Handlers struct {
	NewSession      func(context.Context) (SessionInfo, error)
	OpenSession     func(context.Context, SessionOpenRequest) (SessionDetail, error)
	ListSessions    func(context.Context) (SessionList, error)
	GetSession      func(context.Context, SessionLookupRequest) (SessionDetail, error)
	History         func(context.Context, SessionHistoryRequest) (SessionHistory, error)
	AppendMessage   func(context.Context, SessionAppendMessageRequest) (SessionMutationResult, error)
	AppendInput     func(context.Context, SessionAppendInputRequest) (SessionMutationResult, error)
	RewindSession   func(context.Context, SessionRewindRequest) (SessionRewindResult, error)
	ForkSession     func(context.Context, SessionForkRequest) (SessionMutationResult, error)
	RenameSession   func(context.Context, SessionRenameRequest) (SessionMutationResult, error)
	DeleteSession   func(context.Context, SessionLookupRequest) (SessionMutationResult, error)
	PruneSessions   func(context.Context, SessionPruneRequest) (any, error)
	Prompt          func(context.Context, PromptRequest) (PromptResult, error)
	Status          func(context.Context) (any, error)
	WorkspaceInfo   func(context.Context) (workspaceops.InfoResult, error)
	WorkspaceFiles  func(context.Context, workspaceops.FilesOptions) (workspaceops.FilesResult, error)
	WorkspaceSearch func(context.Context, workspaceops.SearchOptions) (workspaceops.SearchResult, error)
	FileRead        func(context.Context, workspaceops.ReadOptions) (workspaceops.ReadResult, error)
	FileWrite       func(context.Context, workspaceops.WriteOptions) (workspaceops.WriteResult, error)
	FileEdit        func(context.Context, workspaceops.EditOptions) (workspaceops.EditResult, error)
	FileDiff        func(context.Context, workspaceops.DiffOptions) (workspaceops.DiffResult, error)
	DiagnosticsGo   func(context.Context, DiagnosticsRequest) (any, error)
	CodeSymbols     func(context.Context, CodeSymbolsRequest) (any, error)
	CodeReferences  func(context.Context, CodeReferencesRequest) (any, error)
	CodeDefinition  func(context.Context, CodeDefinitionRequest) (any, error)
	CodeHover       func(context.Context, CodeHoverRequest) (any, error)
	CodeCompletion  func(context.Context, CodeCompletionRequest) (any, error)
	CodeFormat      func(context.Context, CodeFormatRequest) (any, error)
	NotebookRead    func(context.Context, NotebookReadRequest) (any, error)
	NotebookEdit    func(context.Context, NotebookEditRequest) (any, error)
}

type SessionInfo struct {
	SessionID string `json:"session_id"`
	Workspace string `json:"workspace,omitempty"`
}

type SessionSummary struct {
	SessionID           string           `json:"session_id"`
	Workspace           string           `json:"workspace,omitempty"`
	Path                string           `json:"path,omitempty"`
	MessageCount        int              `json:"message_count"`
	CreatedAtMS         int64            `json:"created_at_ms,omitempty"`
	UpdatedAtMS         int64            `json:"updated_at_ms,omitempty"`
	ModifiedEpochMillis int64            `json:"modified_epoch_millis,omitempty"`
	ParentSessionID     string           `json:"parent_session_id,omitempty"`
	BranchName          string           `json:"branch_name,omitempty"`
	Lifecycle           SessionLifecycle `json:"lifecycle"`
}

type SessionLifecycle struct {
	Kind      string `json:"kind"`
	Signal    string `json:"signal"`
	Saved     bool   `json:"saved"`
	Abandoned bool   `json:"abandoned"`
}

type SessionList struct {
	Kind      string           `json:"kind"`
	Count     int              `json:"count"`
	Sessions  []SessionSummary `json:"sessions"`
	Workspace string           `json:"workspace,omitempty"`
}

type SessionLookupRequest struct {
	SessionID string `json:"session_id,omitempty"`
}

type SessionOpenRequest struct {
	SessionID string `json:"session_id,omitempty"`
}

type SessionDetail struct {
	SessionID           string           `json:"session_id"`
	Workspace           string           `json:"workspace,omitempty"`
	Path                string           `json:"path,omitempty"`
	MessageCount        int              `json:"message_count"`
	CreatedAtMS         int64            `json:"created_at_ms,omitempty"`
	UpdatedAtMS         int64            `json:"updated_at_ms,omitempty"`
	ModifiedEpochMillis int64            `json:"modified_epoch_millis,omitempty"`
	ParentSessionID     string           `json:"parent_session_id,omitempty"`
	BranchName          string           `json:"branch_name,omitempty"`
	Lifecycle           SessionLifecycle `json:"lifecycle"`
	Messages            any              `json:"messages,omitempty"`
}

type SessionHistoryRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type SessionHistory struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
	Count     int    `json:"count"`
	Entries   any    `json:"entries"`
}

type SessionAppendMessageRequest struct {
	SessionID string             `json:"session_id"`
	Message   *anthropic.Message `json:"message,omitempty"`
	Role      string             `json:"role,omitempty"`
	Text      string             `json:"text,omitempty"`
}

type SessionAppendInputRequest struct {
	SessionID string `json:"session_id"`
	Input     string `json:"input"`
}

type SessionRewindRequest struct {
	SessionID      string `json:"session_id"`
	RemoveMessages int    `json:"remove_messages"`
}

type SessionRewindResult struct {
	Kind              string `json:"kind"`
	Action            string `json:"action"`
	Status            string `json:"status"`
	SessionID         string `json:"session_id"`
	Path              string `json:"path,omitempty"`
	OriginalMessages  int    `json:"original_messages"`
	RemainingMessages int    `json:"remaining_messages"`
	RemovedMessages   int    `json:"removed_messages"`
}

type SessionForkRequest struct {
	SessionID  string `json:"session_id"`
	BranchName string `json:"branch_name,omitempty"`
}

type SessionRenameRequest struct {
	SessionID    string `json:"session_id"`
	NewSessionID string `json:"new_session_id"`
}

type SessionMutationResult struct {
	Kind         string `json:"kind"`
	Action       string `json:"action"`
	Status       string `json:"status"`
	SessionID    string `json:"session_id,omitempty"`
	NewSessionID string `json:"new_session_id,omitempty"`
	Path         string `json:"path,omitempty"`
	MessageCount int    `json:"message_count,omitempty"`
}

type SessionPruneRequest struct {
	Keep      int    `json:"keep,omitempty"`
	EmptyOnly *bool  `json:"empty_only,omitempty"`
	Confirm   bool   `json:"confirm,omitempty"`
	ExcludeID string `json:"exclude_id,omitempty"`
}

type PromptRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Prompt    string `json:"prompt"`
}

type PromptResult struct {
	SessionID string `json:"session_id"`
	Output    string `json:"output"`
}

type DiagnosticsRequest struct {
	Patterns []string `json:"patterns,omitempty"`
}

type CodeSymbolsRequest struct {
	Path string `json:"path,omitempty"`
}

type CodeReferencesRequest struct {
	Symbol string `json:"symbol"`
	Limit  int    `json:"limit,omitempty"`
}

type CodeDefinitionRequest struct {
	Symbol string `json:"symbol"`
}

type CodeHoverRequest struct {
	Symbol       string `json:"symbol"`
	ContextLines int    `json:"context_lines,omitempty"`
}

type CodeCompletionRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type CodeFormatRequest struct {
	Path  string `json:"path"`
	Write bool   `json:"write,omitempty"`
}

type NotebookReadRequest struct {
	Path           string `json:"path,omitempty"`
	NotebookPath   string `json:"notebook_path,omitempty"`
	CellIndex      *int   `json:"cell_index,omitempty"`
	Index          *int   `json:"index,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	IncludeOutputs bool   `json:"include_outputs,omitempty"`
	Outputs        *bool  `json:"outputs,omitempty"`
}

type NotebookEditRequest struct {
	Path         string  `json:"path,omitempty"`
	NotebookPath string  `json:"notebook_path,omitempty"`
	Mode         string  `json:"mode,omitempty"`
	EditMode     string  `json:"edit_mode,omitempty"`
	CellIndex    *int    `json:"cell_index,omitempty"`
	Index        *int    `json:"index,omitempty"`
	CellID       string  `json:"cell_id,omitempty"`
	CellType     string  `json:"cell_type,omitempty"`
	Type         string  `json:"type,omitempty"`
	Source       *string `json:"source,omitempty"`
	NewSource    *string `json:"new_source,omitempty"`
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *responseError  `json:"error,omitempty"`
}

type responseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func Serve(ctx context.Context, in io.Reader, out io.Writer, handlers Handlers, opts Options) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			if err := writeError(out, nil, -32700, err.Error()); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(req.Method, "notifications/") || len(req.ID) == 0 {
			continue
		}
		stop, err := handle(ctx, out, handlers, opts, req)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return scanner.Err()
}

func handle(ctx context.Context, out io.Writer, handlers Handlers, opts Options, req request) (bool, error) {
	switch req.Method {
	case "initialize":
		return false, writeResult(out, req.ID, initializeResult(opts))
	case "shutdown":
		return true, writeResult(out, req.ID, map[string]any{})
	case "status":
		return false, handleStatus(ctx, out, handlers, opts, req)
	case "workspace/info":
		return false, handleWorkspaceInfo(ctx, out, handlers, opts, req)
	case "workspace/files":
		return false, handleWorkspaceFiles(ctx, out, handlers, req)
	case "workspace/search":
		return false, handleWorkspaceSearch(ctx, out, handlers, req)
	case "file/read":
		return false, handleFileRead(ctx, out, handlers, req)
	case "file/write":
		return false, handleFileWrite(ctx, out, handlers, req)
	case "file/edit":
		return false, handleFileEdit(ctx, out, handlers, req)
	case "file/diff":
		return false, handleFileDiff(ctx, out, handlers, req)
	case "diagnostics/go":
		return false, handleDiagnosticsGo(ctx, out, handlers, req)
	case "code/symbols":
		return false, handleCodeSymbols(ctx, out, handlers, req)
	case "code/references":
		return false, handleCodeReferences(ctx, out, handlers, req)
	case "code/definition":
		return false, handleCodeDefinition(ctx, out, handlers, req)
	case "code/hover":
		return false, handleCodeHover(ctx, out, handlers, req)
	case "code/completion":
		return false, handleCodeCompletion(ctx, out, handlers, req)
	case "code/format":
		return false, handleCodeFormat(ctx, out, handlers, req)
	case "notebook/read":
		return false, handleNotebookRead(ctx, out, handlers, req)
	case "notebook/edit":
		return false, handleNotebookEdit(ctx, out, handlers, req)
	case "session/new", "session/create", "sessions/new":
		return false, handleNewSession(ctx, out, handlers, opts, req)
	case "session/open", "sessions/open":
		return false, handleOpenSession(ctx, out, handlers, opts, req)
	case "session/list", "sessions/list":
		return false, handleListSessions(ctx, out, handlers, opts, req)
	case "session/get", "sessions/get", "session/read":
		return false, handleGetSession(ctx, out, handlers, opts, req)
	case "session/history", "sessions/history", "history":
		return false, handleHistory(ctx, out, handlers, req)
	case "session/append_message", "sessions/append_message":
		return false, handleAppendMessage(ctx, out, handlers, req)
	case "session/append_input", "sessions/append_input":
		return false, handleAppendInput(ctx, out, handlers, req)
	case "session/rewind", "sessions/rewind":
		return false, handleRewindSession(ctx, out, handlers, req)
	case "session/fork", "sessions/fork":
		return false, handleForkSession(ctx, out, handlers, req)
	case "session/rename", "sessions/rename":
		return false, handleRenameSession(ctx, out, handlers, req)
	case "session/delete", "sessions/delete":
		return false, handleDeleteSession(ctx, out, handlers, req)
	case "session/prune", "sessions/prune":
		return false, handlePruneSessions(ctx, out, handlers, req)
	case "prompt", "session/prompt":
		return false, handlePrompt(ctx, out, handlers, req)
	default:
		return false, writeError(out, req.ID, -32601, "method not found: "+req.Method)
	}
}

func initializeResult(opts Options) map[string]any {
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = "0.1.0"
	}
	return map[string]any{
		"protocolVersion": "codog-acp-0.1",
		"serverInfo":      map[string]any{"name": "codog", "version": version},
		"capabilities": map[string]any{
			"sessions": map[string]any{
				"new":     true,
				"open":    true,
				"list":    true,
				"get":     true,
				"history": true,
				"append":  true,
				"rewind":  true,
				"fork":    true,
				"rename":  true,
				"delete":  true,
				"prune":   true,
			},
			"workspace": map[string]any{
				"info":   true,
				"files":  true,
				"search": true,
			},
			"file": map[string]any{
				"read":  true,
				"write": true,
				"edit":  true,
				"diff":  true,
			},
			"diagnostics": map[string]any{
				"go": true,
			},
			"code": map[string]any{
				"symbols":    true,
				"references": true,
				"definition": true,
				"hover":      true,
				"completion": true,
				"format":     true,
			},
			"notebook": map[string]any{
				"read": true,
				"edit": true,
			},
			"prompt": true,
			"status": true,
		},
	}
}

func handleStatus(ctx context.Context, out io.Writer, handlers Handlers, opts Options, req request) error {
	if handlers.Status != nil {
		status, err := handlers.Status(ctx)
		if err != nil {
			return writeError(out, req.ID, -32603, err.Error())
		}
		return writeResult(out, req.ID, status)
	}
	return writeResult(out, req.ID, map[string]any{
		"kind":      "acp",
		"status":    "ok",
		"workspace": opts.Workspace,
	})
}

func handleWorkspaceInfo(ctx context.Context, out io.Writer, handlers Handlers, opts Options, req request) error {
	if handlers.WorkspaceInfo != nil {
		info, err := handlers.WorkspaceInfo(ctx)
		if err != nil {
			return writeError(out, req.ID, -32603, err.Error())
		}
		return writeResult(out, req.ID, info)
	}
	return writeResult(out, req.ID, workspaceops.InfoResult{Path: opts.Workspace, Name: workspaceName(opts.Workspace)})
}

func handleWorkspaceFiles(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.WorkspaceFiles == nil {
		return writeError(out, req.ID, -32603, "workspace files handler is not configured")
	}
	var options workspaceops.FilesOptions
	if len(req.Params) != 0 {
		if err := json.Unmarshal(req.Params, &options); err != nil {
			return writeError(out, req.ID, -32602, err.Error())
		}
	}
	result, err := handlers.WorkspaceFiles(ctx, options)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleWorkspaceSearch(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.WorkspaceSearch == nil {
		return writeError(out, req.ID, -32603, "workspace search handler is not configured")
	}
	var options workspaceops.SearchOptions
	if len(req.Params) != 0 {
		if err := json.Unmarshal(req.Params, &options); err != nil {
			return writeError(out, req.ID, -32602, err.Error())
		}
	}
	result, err := handlers.WorkspaceSearch(ctx, options)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleFileRead(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.FileRead == nil {
		return writeError(out, req.ID, -32603, "file read handler is not configured")
	}
	var options workspaceops.ReadOptions
	if err := unmarshalParams(req.Params, &options); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.FileRead(ctx, options)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleFileWrite(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.FileWrite == nil {
		return writeError(out, req.ID, -32603, "file write handler is not configured")
	}
	var options workspaceops.WriteOptions
	if err := unmarshalParams(req.Params, &options); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.FileWrite(ctx, options)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleFileEdit(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.FileEdit == nil {
		return writeError(out, req.ID, -32603, "file edit handler is not configured")
	}
	var options workspaceops.EditOptions
	if err := unmarshalParams(req.Params, &options); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.FileEdit(ctx, options)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleFileDiff(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.FileDiff == nil {
		return writeError(out, req.ID, -32603, "file diff handler is not configured")
	}
	var options workspaceops.DiffOptions
	if err := unmarshalParams(req.Params, &options); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.FileDiff(ctx, options)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleDiagnosticsGo(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.DiagnosticsGo == nil {
		return writeError(out, req.ID, -32603, "go diagnostics handler is not configured")
	}
	var request DiagnosticsRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.DiagnosticsGo(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleCodeSymbols(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.CodeSymbols == nil {
		return writeError(out, req.ID, -32603, "code symbols handler is not configured")
	}
	var request CodeSymbolsRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.CodeSymbols(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleCodeReferences(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.CodeReferences == nil {
		return writeError(out, req.ID, -32603, "code references handler is not configured")
	}
	var request CodeReferencesRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.CodeReferences(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleCodeDefinition(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.CodeDefinition == nil {
		return writeError(out, req.ID, -32603, "code definition handler is not configured")
	}
	var request CodeDefinitionRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.CodeDefinition(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleCodeHover(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.CodeHover == nil {
		return writeError(out, req.ID, -32603, "code hover handler is not configured")
	}
	var request CodeHoverRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.CodeHover(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleCodeCompletion(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.CodeCompletion == nil {
		return writeError(out, req.ID, -32603, "code completion handler is not configured")
	}
	var request CodeCompletionRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.CodeCompletion(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleCodeFormat(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.CodeFormat == nil {
		return writeError(out, req.ID, -32603, "code format handler is not configured")
	}
	var request CodeFormatRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.CodeFormat(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleNotebookRead(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.NotebookRead == nil {
		return writeError(out, req.ID, -32603, "notebook read handler is not configured")
	}
	var request NotebookReadRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.NotebookRead(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleNotebookEdit(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.NotebookEdit == nil {
		return writeError(out, req.ID, -32603, "notebook edit handler is not configured")
	}
	var request NotebookEditRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.NotebookEdit(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleNewSession(ctx context.Context, out io.Writer, handlers Handlers, opts Options, req request) error {
	if handlers.NewSession == nil {
		return writeError(out, req.ID, -32603, "session handler is not configured")
	}
	info, err := handlers.NewSession(ctx)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	if strings.TrimSpace(info.Workspace) == "" {
		info.Workspace = opts.Workspace
	}
	return writeResult(out, req.ID, info)
}

func handleOpenSession(ctx context.Context, out io.Writer, handlers Handlers, opts Options, req request) error {
	if handlers.OpenSession == nil {
		return writeError(out, req.ID, -32603, "session open handler is not configured")
	}
	openReq, err := parseSessionOpenRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	detail, err := handlers.OpenSession(ctx, openReq)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	if strings.TrimSpace(detail.Workspace) == "" {
		detail.Workspace = opts.Workspace
	}
	return writeResult(out, req.ID, detail)
}

func handleListSessions(ctx context.Context, out io.Writer, handlers Handlers, opts Options, req request) error {
	if handlers.ListSessions == nil {
		return writeError(out, req.ID, -32603, "session list handler is not configured")
	}
	list, err := handlers.ListSessions(ctx)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	if strings.TrimSpace(list.Kind) == "" {
		list.Kind = "session_list"
	}
	if strings.TrimSpace(list.Workspace) == "" {
		list.Workspace = opts.Workspace
	}
	list.Count = len(list.Sessions)
	return writeResult(out, req.ID, list)
}

func handleGetSession(ctx context.Context, out io.Writer, handlers Handlers, opts Options, req request) error {
	if handlers.GetSession == nil {
		return writeError(out, req.ID, -32603, "session get handler is not configured")
	}
	lookup, err := parseSessionLookupRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	detail, err := handlers.GetSession(ctx, lookup)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	if strings.TrimSpace(detail.Workspace) == "" {
		detail.Workspace = opts.Workspace
	}
	return writeResult(out, req.ID, detail)
}

func handleHistory(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.History == nil {
		return writeError(out, req.ID, -32603, "session history handler is not configured")
	}
	historyReq, err := parseSessionHistoryRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	history, err := handlers.History(ctx, historyReq)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	if strings.TrimSpace(history.Kind) == "" {
		history.Kind = "session_history"
	}
	return writeResult(out, req.ID, history)
}

func parseSessionOpenRequest(params json.RawMessage) (SessionOpenRequest, error) {
	var raw struct {
		SessionID      string `json:"session_id"`
		SessionIDCamel string `json:"sessionId"`
		ID             string `json:"id"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &raw); err != nil {
			return SessionOpenRequest{}, err
		}
	}
	return SessionOpenRequest{SessionID: firstNonEmpty(raw.SessionID, raw.SessionIDCamel, raw.ID)}, nil
}

func handleAppendMessage(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.AppendMessage == nil {
		return writeError(out, req.ID, -32603, "session append_message handler is not configured")
	}
	appendReq, err := parseSessionAppendMessageRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.AppendMessage(ctx, appendReq)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	if strings.TrimSpace(result.Kind) == "" {
		result.Kind = "session_mutation"
	}
	if strings.TrimSpace(result.Action) == "" {
		result.Action = "append_message"
	}
	if strings.TrimSpace(result.Status) == "" {
		result.Status = "ok"
	}
	return writeResult(out, req.ID, result)
}

func handleAppendInput(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.AppendInput == nil {
		return writeError(out, req.ID, -32603, "session append_input handler is not configured")
	}
	appendReq, err := parseSessionAppendInputRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.AppendInput(ctx, appendReq)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	if strings.TrimSpace(result.Kind) == "" {
		result.Kind = "session_mutation"
	}
	if strings.TrimSpace(result.Action) == "" {
		result.Action = "append_input"
	}
	if strings.TrimSpace(result.Status) == "" {
		result.Status = "ok"
	}
	return writeResult(out, req.ID, result)
}

func handleRewindSession(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.RewindSession == nil {
		return writeError(out, req.ID, -32603, "session rewind handler is not configured")
	}
	rewindReq, err := parseSessionRewindRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.RewindSession(ctx, rewindReq)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	if strings.TrimSpace(result.Kind) == "" {
		result.Kind = "session_mutation"
	}
	if strings.TrimSpace(result.Action) == "" {
		result.Action = "rewind"
	}
	if strings.TrimSpace(result.Status) == "" {
		result.Status = "ok"
	}
	return writeResult(out, req.ID, result)
}

func handleForkSession(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.ForkSession == nil {
		return writeError(out, req.ID, -32603, "session fork handler is not configured")
	}
	forkReq, err := parseSessionForkRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.ForkSession(ctx, forkReq)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	if strings.TrimSpace(result.Kind) == "" {
		result.Kind = "session_mutation"
	}
	if strings.TrimSpace(result.Action) == "" {
		result.Action = "fork"
	}
	if strings.TrimSpace(result.Status) == "" {
		result.Status = "ok"
	}
	return writeResult(out, req.ID, result)
}

func handleRenameSession(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.RenameSession == nil {
		return writeError(out, req.ID, -32603, "session rename handler is not configured")
	}
	renameReq, err := parseSessionRenameRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.RenameSession(ctx, renameReq)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	if strings.TrimSpace(result.Kind) == "" {
		result.Kind = "session_mutation"
	}
	if strings.TrimSpace(result.Action) == "" {
		result.Action = "rename"
	}
	if strings.TrimSpace(result.Status) == "" {
		result.Status = "ok"
	}
	return writeResult(out, req.ID, result)
}

func handleDeleteSession(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.DeleteSession == nil {
		return writeError(out, req.ID, -32603, "session delete handler is not configured")
	}
	lookup, err := parseSessionLookupRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.DeleteSession(ctx, lookup)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	if strings.TrimSpace(result.Kind) == "" {
		result.Kind = "session_mutation"
	}
	if strings.TrimSpace(result.Action) == "" {
		result.Action = "delete"
	}
	if strings.TrimSpace(result.Status) == "" {
		result.Status = "ok"
	}
	return writeResult(out, req.ID, result)
}

func handlePruneSessions(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.PruneSessions == nil {
		return writeError(out, req.ID, -32603, "session prune handler is not configured")
	}
	pruneReq, err := parseSessionPruneRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.PruneSessions(ctx, pruneReq)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handlePrompt(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.Prompt == nil {
		return writeError(out, req.ID, -32603, "prompt handler is not configured")
	}
	promptReq, err := parsePromptRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.Prompt(ctx, promptReq)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, map[string]any{
		"session_id": result.SessionID,
		"text":       result.Output,
		"content":    []map[string]string{{"type": "text", "text": result.Output}},
	})
}

func parseSessionLookupRequest(params json.RawMessage) (SessionLookupRequest, error) {
	var raw struct {
		SessionID      string `json:"session_id"`
		SessionIDCamel string `json:"sessionId"`
		ID             string `json:"id"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &raw); err != nil {
			return SessionLookupRequest{}, err
		}
	}
	sessionID := firstNonEmpty(raw.SessionID, raw.SessionIDCamel, raw.ID)
	if strings.TrimSpace(sessionID) == "" {
		sessionID = "latest"
	}
	return SessionLookupRequest{SessionID: sessionID}, nil
}

func parseSessionHistoryRequest(params json.RawMessage) (SessionHistoryRequest, error) {
	var raw struct {
		SessionID      string `json:"session_id"`
		SessionIDCamel string `json:"sessionId"`
		ID             string `json:"id"`
		Limit          int    `json:"limit"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &raw); err != nil {
			return SessionHistoryRequest{}, err
		}
	}
	sessionID := firstNonEmpty(raw.SessionID, raw.SessionIDCamel, raw.ID)
	if strings.TrimSpace(sessionID) == "" {
		sessionID = "latest"
	}
	return SessionHistoryRequest{SessionID: sessionID, Limit: raw.Limit}, nil
}

func parseSessionAppendMessageRequest(params json.RawMessage) (SessionAppendMessageRequest, error) {
	var raw struct {
		SessionID      string             `json:"session_id"`
		SessionIDCamel string             `json:"sessionId"`
		ID             string             `json:"id"`
		Message        *anthropic.Message `json:"message"`
		Role           string             `json:"role"`
		Text           string             `json:"text"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &raw); err != nil {
			return SessionAppendMessageRequest{}, err
		}
	}
	sessionID := firstNonEmpty(raw.SessionID, raw.SessionIDCamel, raw.ID)
	if strings.TrimSpace(sessionID) == "" {
		return SessionAppendMessageRequest{}, fmt.Errorf("session_id is required")
	}
	if raw.Message != nil {
		if strings.TrimSpace(raw.Message.Role) == "" || len(raw.Message.Content) == 0 {
			return SessionAppendMessageRequest{}, fmt.Errorf("message role and content are required")
		}
		return SessionAppendMessageRequest{SessionID: sessionID, Message: raw.Message}, nil
	}
	role := strings.TrimSpace(raw.Role)
	if role == "" {
		role = "user"
	}
	text := strings.TrimSpace(raw.Text)
	if text == "" {
		return SessionAppendMessageRequest{}, fmt.Errorf("text is required")
	}
	return SessionAppendMessageRequest{SessionID: sessionID, Role: role, Text: text}, nil
}

func parseSessionAppendInputRequest(params json.RawMessage) (SessionAppendInputRequest, error) {
	var raw struct {
		SessionID      string `json:"session_id"`
		SessionIDCamel string `json:"sessionId"`
		ID             string `json:"id"`
		Input          string `json:"input"`
		Prompt         string `json:"prompt"`
		Text           string `json:"text"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &raw); err != nil {
			return SessionAppendInputRequest{}, err
		}
	}
	sessionID := firstNonEmpty(raw.SessionID, raw.SessionIDCamel, raw.ID)
	if strings.TrimSpace(sessionID) == "" {
		return SessionAppendInputRequest{}, fmt.Errorf("session_id is required")
	}
	input := firstNonEmpty(raw.Input, raw.Prompt, raw.Text)
	if strings.TrimSpace(input) == "" {
		return SessionAppendInputRequest{}, fmt.Errorf("input is required")
	}
	return SessionAppendInputRequest{SessionID: sessionID, Input: input}, nil
}

func parseSessionRewindRequest(params json.RawMessage) (SessionRewindRequest, error) {
	var raw struct {
		SessionID           string `json:"session_id"`
		SessionIDCamel      string `json:"sessionId"`
		ID                  string `json:"id"`
		RemoveMessages      int    `json:"remove_messages"`
		RemoveMessagesCamel int    `json:"removeMessages"`
		Count               int    `json:"count"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &raw); err != nil {
			return SessionRewindRequest{}, err
		}
	}
	sessionID := firstNonEmpty(raw.SessionID, raw.SessionIDCamel, raw.ID)
	if strings.TrimSpace(sessionID) == "" {
		return SessionRewindRequest{}, fmt.Errorf("session_id is required")
	}
	removeMessages := firstPositive(raw.RemoveMessages, raw.RemoveMessagesCamel, raw.Count)
	if removeMessages <= 0 {
		return SessionRewindRequest{}, fmt.Errorf("remove_messages must be positive")
	}
	return SessionRewindRequest{SessionID: sessionID, RemoveMessages: removeMessages}, nil
}

func parseSessionForkRequest(params json.RawMessage) (SessionForkRequest, error) {
	var raw struct {
		SessionID       string `json:"session_id"`
		SessionIDCamel  string `json:"sessionId"`
		ID              string `json:"id"`
		Branch          string `json:"branch"`
		BranchName      string `json:"branch_name"`
		BranchNameCamel string `json:"branchName"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &raw); err != nil {
			return SessionForkRequest{}, err
		}
	}
	sessionID := firstNonEmpty(raw.SessionID, raw.SessionIDCamel, raw.ID)
	if strings.TrimSpace(sessionID) == "" {
		return SessionForkRequest{}, fmt.Errorf("session_id is required")
	}
	return SessionForkRequest{
		SessionID:  sessionID,
		BranchName: firstNonEmpty(raw.BranchName, raw.BranchNameCamel, raw.Branch),
	}, nil
}

func parseSessionRenameRequest(params json.RawMessage) (SessionRenameRequest, error) {
	var raw struct {
		SessionID         string `json:"session_id"`
		SessionIDCamel    string `json:"sessionId"`
		ID                string `json:"id"`
		NewSessionID      string `json:"new_session_id"`
		NewSessionIDCamel string `json:"newSessionId"`
		NewID             string `json:"new_id"`
		Name              string `json:"name"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &raw); err != nil {
			return SessionRenameRequest{}, err
		}
	}
	sessionID := firstNonEmpty(raw.SessionID, raw.SessionIDCamel, raw.ID)
	newSessionID := firstNonEmpty(raw.NewSessionID, raw.NewSessionIDCamel, raw.NewID, raw.Name)
	if strings.TrimSpace(sessionID) == "" {
		return SessionRenameRequest{}, fmt.Errorf("session_id is required")
	}
	if strings.TrimSpace(newSessionID) == "" {
		return SessionRenameRequest{}, fmt.Errorf("new_session_id is required")
	}
	return SessionRenameRequest{SessionID: sessionID, NewSessionID: newSessionID}, nil
}

func parseSessionPruneRequest(params json.RawMessage) (SessionPruneRequest, error) {
	var raw struct {
		Keep                  int    `json:"keep"`
		EmptyOnly             *bool  `json:"empty_only"`
		Confirm               bool   `json:"confirm"`
		ExcludeID             string `json:"exclude_id"`
		ExcludeSessionID      string `json:"exclude_session_id"`
		ExcludeSessionIDCamel string `json:"excludeSessionId"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &raw); err != nil {
			return SessionPruneRequest{}, err
		}
	}
	if raw.Keep < 0 {
		return SessionPruneRequest{}, fmt.Errorf("keep must be non-negative")
	}
	if raw.Keep == 0 && raw.EmptyOnly != nil && !*raw.EmptyOnly {
		return SessionPruneRequest{}, fmt.Errorf("empty_only=false requires keep")
	}
	return SessionPruneRequest{
		Keep:      raw.Keep,
		EmptyOnly: raw.EmptyOnly,
		Confirm:   raw.Confirm,
		ExcludeID: firstNonEmpty(raw.ExcludeID, raw.ExcludeSessionID, raw.ExcludeSessionIDCamel),
	}, nil
}

func parsePromptRequest(params json.RawMessage) (PromptRequest, error) {
	var raw struct {
		SessionID      string `json:"session_id"`
		SessionIDCamel string `json:"sessionId"`
		Prompt         string `json:"prompt"`
		Input          string `json:"input"`
		Text           string `json:"text"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &raw); err != nil {
			return PromptRequest{}, err
		}
	}
	prompt := firstNonEmpty(raw.Prompt, raw.Input, raw.Text)
	if strings.TrimSpace(prompt) == "" {
		return PromptRequest{}, fmt.Errorf("prompt is required")
	}
	return PromptRequest{
		SessionID: firstNonEmpty(raw.SessionID, raw.SessionIDCamel),
		Prompt:    prompt,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func workspaceName(path string) string {
	path = strings.TrimRight(strings.TrimSpace(path), `/\`)
	if path == "" {
		return ""
	}
	index := strings.LastIndexAny(path, `/\`)
	if index >= 0 {
		return path[index+1:]
	}
	return path
}

func unmarshalParams(params json.RawMessage, target any) error {
	if len(params) == 0 {
		return nil
	}
	return json.Unmarshal(params, target)
}

func writeResult(out io.Writer, id json.RawMessage, result any) error {
	data, err := json.Marshal(response{JSONRPC: "2.0", ID: id, Result: result})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", data)
	return err
}

func writeError(out io.Writer, id json.RawMessage, code int, message string) error {
	data, err := json.Marshal(response{JSONRPC: "2.0", ID: id, Error: &responseError{Code: code, Message: message}})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", data)
	return err
}
