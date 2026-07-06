// Package acpserver implements Codog's line-delimited JSON-RPC ACP bridge.
package acpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/workspaceops"
)

// Options configures server metadata returned by initialize and status calls.
type Options struct {
	Version   string
	Workspace string
}

// Handlers contains application callbacks for each ACP method.
type Handlers struct {
	NewSession           func(context.Context) (SessionInfo, error)
	OpenSession          func(context.Context, SessionOpenRequest) (SessionDetail, error)
	ListSessions         func(context.Context) (SessionList, error)
	GetSession           func(context.Context, SessionLookupRequest) (SessionDetail, error)
	History              func(context.Context, SessionHistoryRequest) (SessionHistory, error)
	AppendMessage        func(context.Context, SessionAppendMessageRequest) (SessionMutationResult, error)
	AppendInput          func(context.Context, SessionAppendInputRequest) (SessionMutationResult, error)
	RewindSession        func(context.Context, SessionRewindRequest) (SessionRewindResult, error)
	ForkSession          func(context.Context, SessionForkRequest) (SessionMutationResult, error)
	RenameSession        func(context.Context, SessionRenameRequest) (SessionMutationResult, error)
	DeleteSession        func(context.Context, SessionLookupRequest) (SessionMutationResult, error)
	PruneSessions        func(context.Context, SessionPruneRequest) (any, error)
	Prompt               func(context.Context, PromptRequest) (PromptResult, error)
	Status               func(context.Context) (any, error)
	WorkspaceInfo        func(context.Context) (workspaceops.InfoResult, error)
	WorkspaceFiles       func(context.Context, workspaceops.FilesOptions) (workspaceops.FilesResult, error)
	WorkspaceSearch      func(context.Context, workspaceops.SearchOptions) (workspaceops.SearchResult, error)
	FileRead             func(context.Context, workspaceops.ReadOptions) (workspaceops.ReadResult, error)
	FileWrite            func(context.Context, workspaceops.WriteOptions) (workspaceops.WriteResult, error)
	FileEdit             func(context.Context, workspaceops.EditOptions) (workspaceops.EditResult, error)
	FileDiff             func(context.Context, workspaceops.DiffOptions) (workspaceops.DiffResult, error)
	DiagnosticsGo        func(context.Context, DiagnosticsRequest) (any, error)
	CodeSymbols          func(context.Context, CodeSymbolsRequest) (any, error)
	CodeReferences       func(context.Context, CodeReferencesRequest) (any, error)
	CodeDefinition       func(context.Context, CodeDefinitionRequest) (any, error)
	CodeHover            func(context.Context, CodeHoverRequest) (any, error)
	CodeCompletion       func(context.Context, CodeCompletionRequest) (any, error)
	CodeFormat           func(context.Context, CodeFormatRequest) (any, error)
	NotebookRead         func(context.Context, NotebookReadRequest) (any, error)
	NotebookEdit         func(context.Context, NotebookEditRequest) (any, error)
	LSPActions           func(context.Context) (any, error)
	LSPDiscover          func(context.Context) (any, error)
	LSPList              func(context.Context) (any, error)
	LSPStart             func(context.Context, LSPStartRequest) (any, error)
	LSPStatus            func(context.Context, LSPStatusRequest) (any, error)
	LSPStop              func(context.Context, LSPStopRequest) (any, error)
	LSPQuery             func(context.Context, LSPQueryRequest) (any, error)
	BackgroundList       func(context.Context, BackgroundListRequest) (any, error)
	BackgroundRun        func(context.Context, BackgroundRunRequest) (any, error)
	BackgroundGet        func(context.Context, BackgroundIDRequest) (any, error)
	BackgroundLogs       func(context.Context, BackgroundLogsRequest) (any, error)
	BackgroundBoard      func(context.Context, BackgroundBoardRequest) (any, error)
	BackgroundHeartbeat  func(context.Context, BackgroundHeartbeatRequest) (any, error)
	BackgroundStop       func(context.Context, BackgroundIDRequest) (any, error)
	BackgroundRestart    func(context.Context, BackgroundIDRequest) (any, error)
	BackgroundPrune      func(context.Context, BackgroundPruneRequest) (any, error)
	BackgroundSupervise  func(context.Context, BackgroundSuperviseRequest) (any, error)
	BackgroundWatch      func(context.Context, BackgroundWatchRequest, func(background.WatchEvent) error) (any, error)
	AgentRunsList        func(context.Context, AgentRunsListRequest) (any, error)
	AgentRunsGet         func(context.Context, AgentRunIDRequest) (any, error)
	AgentRunsLogs        func(context.Context, AgentRunLogsRequest) (any, error)
	AgentRunsBoard       func(context.Context, AgentRunsBoardRequest) (any, error)
	AgentRunsHeartbeat   func(context.Context, AgentRunHeartbeatRequest) (any, error)
	AgentRunsStop        func(context.Context, AgentRunIDRequest) (any, error)
	AgentRunsPrune       func(context.Context, AgentRunsPruneRequest) (any, error)
	MCPList              func(context.Context, MCPListRequest) (any, error)
	MCPShow              func(context.Context, MCPServerRequest) (any, error)
	MCPAuth              func(context.Context, MCPServerRequest) (any, error)
	MCPTools             func(context.Context, MCPServerRequest) (any, error)
	MCPCall              func(context.Context, MCPCallRequest) (any, error)
	MCPResources         func(context.Context, MCPServerRequest) (any, error)
	MCPResourceTemplates func(context.Context, MCPServerRequest) (any, error)
	MCPRead              func(context.Context, MCPReadRequest) (any, error)
	MCPPrompts           func(context.Context, MCPServerRequest) (any, error)
	MCPPrompt            func(context.Context, MCPPromptRequest) (any, error)
	EditorIdentify       func(context.Context, EditorIdentifyRequest) (any, error)
	EditorState          func(context.Context) (any, error)
	EditorOpen           func(context.Context, EditorOpenRequest) (any, error)
	EditorSelection      func(context.Context, EditorSelectionRequest) (any, error)
	BridgeFaultsList     func(context.Context) (any, error)
	BridgeFaultsRecord   func(context.Context, BridgeFaultRecordRequest) (any, error)
	BridgeFaultsClear    func(context.Context) (any, error)
}

// SessionInfo describes a newly opened or created session.
type SessionInfo struct {
	SessionID string `json:"session_id"`
	Workspace string `json:"workspace,omitempty"`
}

// SessionSummary is a compact session record for list responses.
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

// SessionLifecycle describes the persistence state of a session.
type SessionLifecycle struct {
	Kind      string `json:"kind"`
	Signal    string `json:"signal"`
	Saved     bool   `json:"saved"`
	Abandoned bool   `json:"abandoned"`
}

// SessionList contains sessions visible to the ACP client.
type SessionList struct {
	Kind      string           `json:"kind"`
	Count     int              `json:"count"`
	Sessions  []SessionSummary `json:"sessions"`
	Workspace string           `json:"workspace,omitempty"`
}

// SessionLookupRequest identifies one session.
type SessionLookupRequest struct {
	SessionID string `json:"session_id,omitempty"`
}

// SessionOpenRequest identifies a session to open or resume.
type SessionOpenRequest struct {
	SessionID string `json:"session_id,omitempty"`
}

// SessionDetail contains session metadata and optional message history.
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

// SessionHistoryRequest controls a bounded history lookup.
type SessionHistoryRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// SessionHistory contains normalized history entries for a session.
type SessionHistory struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
	Count     int    `json:"count"`
	Entries   any    `json:"entries"`
}

// SessionAppendMessageRequest appends a structured or text message.
type SessionAppendMessageRequest struct {
	SessionID string             `json:"session_id"`
	Message   *anthropic.Message `json:"message,omitempty"`
	Role      string             `json:"role,omitempty"`
	Text      string             `json:"text,omitempty"`
}

// SessionAppendInputRequest appends user input to a session.
type SessionAppendInputRequest struct {
	SessionID string `json:"session_id"`
	Input     string `json:"input"`
}

// SessionRewindRequest removes recent messages from a session.
type SessionRewindRequest struct {
	SessionID      string `json:"session_id"`
	RemoveMessages int    `json:"remove_messages"`
}

// SessionRewindResult reports a completed rewind mutation.
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

// SessionForkRequest creates a session branch from an existing session.
type SessionForkRequest struct {
	SessionID  string `json:"session_id"`
	BranchName string `json:"branch_name,omitempty"`
}

// SessionRenameRequest renames an existing session.
type SessionRenameRequest struct {
	SessionID    string `json:"session_id"`
	NewSessionID string `json:"new_session_id"`
}

// SessionMutationResult reports a session mutation.
type SessionMutationResult struct {
	Kind         string `json:"kind"`
	Action       string `json:"action"`
	Status       string `json:"status"`
	SessionID    string `json:"session_id,omitempty"`
	NewSessionID string `json:"new_session_id,omitempty"`
	Path         string `json:"path,omitempty"`
	MessageCount int    `json:"message_count,omitempty"`
}

// SessionPruneRequest controls session retention cleanup.
type SessionPruneRequest struct {
	Keep      int    `json:"keep,omitempty"`
	EmptyOnly *bool  `json:"empty_only,omitempty"`
	Confirm   bool   `json:"confirm,omitempty"`
	ExcludeID string `json:"exclude_id,omitempty"`
}

// PromptRequest sends one prompt to the agent runtime.
type PromptRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Prompt    string `json:"prompt"`
}

// PromptResult contains the assistant output for a prompt call.
type PromptResult struct {
	SessionID string `json:"session_id"`
	Output    string `json:"output"`
}

// DiagnosticsRequest controls Go diagnostics.
type DiagnosticsRequest struct {
	Patterns []string `json:"patterns,omitempty"`
}

// CodeSymbolsRequest controls symbol discovery.
type CodeSymbolsRequest struct {
	Path string `json:"path,omitempty"`
}

// CodeReferencesRequest controls reference lookup.
type CodeReferencesRequest struct {
	Symbol string `json:"symbol"`
	Limit  int    `json:"limit,omitempty"`
}

// CodeDefinitionRequest controls definition lookup.
type CodeDefinitionRequest struct {
	Symbol string `json:"symbol"`
}

// CodeHoverRequest controls hover information lookup.
type CodeHoverRequest struct {
	Symbol       string `json:"symbol"`
	ContextLines int    `json:"context_lines,omitempty"`
}

// CodeCompletionRequest controls code completion suggestions.
type CodeCompletionRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// CodeFormatRequest controls code formatting.
type CodeFormatRequest struct {
	Path  string `json:"path"`
	Write bool   `json:"write,omitempty"`
}

// NotebookReadRequest controls notebook cell reads.
type NotebookReadRequest struct {
	Path           string `json:"path,omitempty"`
	NotebookPath   string `json:"notebook_path,omitempty"`
	CellIndex      *int   `json:"cell_index,omitempty"`
	Index          *int   `json:"index,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	IncludeOutputs bool   `json:"include_outputs,omitempty"`
	Outputs        *bool  `json:"outputs,omitempty"`
}

// NotebookEditRequest controls notebook cell mutations.
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

// LSPStartRequest starts a language server process.
type LSPStartRequest struct {
	Language    string   `json:"language"`
	CommandArgs []string `json:"command_args,omitempty"`
}

// LSPStatusRequest identifies a language server status query.
type LSPStatusRequest struct {
	Language string `json:"language"`
}

// LSPStopRequest identifies a language server stop request.
type LSPStopRequest struct {
	Language string `json:"language"`
}

// LSPQueryRequest sends a typed request to a language server.
type LSPQueryRequest struct {
	Language        string `json:"language"`
	Action          string `json:"action"`
	Path            string `json:"path,omitempty"`
	FilePath        string `json:"file_path,omitempty"`
	Line            int    `json:"line,omitempty"`
	Character       int    `json:"character,omitempty"`
	NewName         string `json:"new_name,omitempty"`
	CodeActionTitle string `json:"code_action_title,omitempty"`
	Apply           bool   `json:"apply,omitempty"`
	Write           bool   `json:"write,omitempty"`
	TimeoutMS       int    `json:"timeout_ms,omitempty"`
}

// BackgroundListRequest filters background task listing.
type BackgroundListRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Kind      string `json:"kind,omitempty"`
}

// BackgroundRunRequest starts a background task.
type BackgroundRunRequest struct {
	Command       string                    `json:"command"`
	Kind          string                    `json:"kind,omitempty"`
	SessionID     string                    `json:"session_id,omitempty"`
	RestartPolicy *background.RestartPolicy `json:"restart_policy,omitempty"`
}

// BackgroundIDRequest identifies a background task.
type BackgroundIDRequest struct {
	ID string `json:"id"`
}

// BackgroundLogsRequest reads bounded task logs.
type BackgroundLogsRequest struct {
	ID    string `json:"id"`
	Limit int64  `json:"limit,omitempty"`
}

// BackgroundBoardRequest controls background lane board rendering.
type BackgroundBoardRequest struct {
	StalledAfterSeconds int `json:"stalled_after_seconds,omitempty"`
	StalledAfterMS      int `json:"stalled_after_ms,omitempty"`
}

// BackgroundHeartbeatRequest updates task heartbeat state.
type BackgroundHeartbeatRequest struct {
	ID             string     `json:"id"`
	Status         string     `json:"status,omitempty"`
	TransportAlive *bool      `json:"transport_alive,omitempty"`
	ObservedAt     *time.Time `json:"observed_at,omitempty"`
}

// BackgroundPruneRequest controls background task cleanup.
type BackgroundPruneRequest struct {
	OlderThanSeconds int  `json:"older_than_seconds,omitempty"`
	OlderThanDays    int  `json:"older_than_days,omitempty"`
	Keep             *int `json:"keep,omitempty"`
}

// BackgroundSuperviseRequest runs one background supervision pass.
type BackgroundSuperviseRequest struct {
	Now *time.Time `json:"now,omitempty"`
}

// BackgroundWatchRequest streams background task events.
type BackgroundWatchRequest struct {
	ID         string `json:"id"`
	Offset     int64  `json:"offset,omitempty"`
	IntervalMS int    `json:"interval_ms,omitempty"`
	MaxEvents  int    `json:"max_events,omitempty"`
}

// AgentRunsListRequest filters agent run listing.
type AgentRunsListRequest struct {
	Agent     string `json:"agent,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// AgentRunIDRequest identifies one agent run.
type AgentRunIDRequest struct {
	ID string `json:"id"`
}

// AgentRunLogsRequest reads logs for an agent run.
type AgentRunLogsRequest struct {
	ID    string `json:"id"`
	Limit int64  `json:"limit,omitempty"`
}

// AgentRunsBoardRequest controls agent run lane board rendering.
type AgentRunsBoardRequest struct {
	Agent               string `json:"agent,omitempty"`
	SessionID           string `json:"session_id,omitempty"`
	StalledAfterSeconds int    `json:"stalled_after_seconds,omitempty"`
	StalledAfterMS      int    `json:"stalled_after_ms,omitempty"`
}

// AgentRunHeartbeatRequest updates an agent run heartbeat.
type AgentRunHeartbeatRequest struct {
	ID             string     `json:"id"`
	Status         string     `json:"status,omitempty"`
	TransportAlive *bool      `json:"transport_alive,omitempty"`
	ObservedAt     *time.Time `json:"observed_at,omitempty"`
}

// AgentRunsPruneRequest controls agent run cleanup.
type AgentRunsPruneRequest struct {
	OlderThanSeconds int  `json:"older_than_seconds,omitempty"`
	OlderThanDays    int  `json:"older_than_days,omitempty"`
	Keep             *int `json:"keep,omitempty"`
}

// MCPListRequest controls MCP server listing.
type MCPListRequest struct {
	Inspect *bool `json:"inspect,omitempty"`
}

// MCPServerRequest identifies an MCP server by server or name.
type MCPServerRequest struct {
	Server string `json:"server,omitempty"`
	Name   string `json:"name,omitempty"`
}

// MCPCallRequest invokes an MCP tool.
type MCPCallRequest struct {
	Server    string          `json:"server,omitempty"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// MCPReadRequest reads an MCP resource.
type MCPReadRequest struct {
	Server string `json:"server,omitempty"`
	URI    string `json:"uri"`
}

// MCPPromptRequest renders an MCP prompt.
type MCPPromptRequest struct {
	Server    string          `json:"server,omitempty"`
	Prompt    string          `json:"prompt,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// EditorIdentifyRequest establishes a trusted editor bridge identity.
type EditorIdentifyRequest struct {
	Editor    string `json:"editor"`
	Version   string `json:"version,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Token     string `json:"token,omitempty"`
}

// EditorOpenRequest records the active editor file.
type EditorOpenRequest struct {
	Path string `json:"path"`
}

// EditorSelectionRequest records an editor selection.
type EditorSelectionRequest struct {
	Path        string `json:"path,omitempty"`
	StartLine   int    `json:"start_line"`
	StartColumn int    `json:"start_column,omitempty"`
	EndLine     int    `json:"end_line,omitempty"`
	EndColumn   int    `json:"end_column,omitempty"`
	Text        string `json:"text,omitempty"`
}

// BridgeFaultRecordRequest records an editor bridge diagnostic fault.
type BridgeFaultRecordRequest struct {
	Action string   `json:"action"`
	Args   []string `json:"args,omitempty"`
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

type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// Serve processes line-delimited JSON-RPC ACP requests from in and writes responses to out.
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
	case "editor/identify":
		return false, handleEditorIdentify(ctx, out, handlers, req)
	case "editor/state":
		return false, handleEditorState(ctx, out, handlers, req)
	case "editor/open":
		return false, handleEditorOpen(ctx, out, handlers, req)
	case "editor/selection":
		return false, handleEditorSelection(ctx, out, handlers, req)
	case "bridge/faults/list":
		return false, handleBridgeFaultsList(ctx, out, handlers, req)
	case "bridge/faults/record":
		return false, handleBridgeFaultsRecord(ctx, out, handlers, req)
	case "bridge/faults/clear":
		return false, handleBridgeFaultsClear(ctx, out, handlers, req)
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
	case "lsp/actions":
		return false, handleLSPActions(ctx, out, handlers, req)
	case "lsp/discover":
		return false, handleLSPDiscover(ctx, out, handlers, req)
	case "lsp/list":
		return false, handleLSPList(ctx, out, handlers, req)
	case "lsp/start":
		return false, handleLSPStart(ctx, out, handlers, req)
	case "lsp/status":
		return false, handleLSPStatus(ctx, out, handlers, req)
	case "lsp/stop":
		return false, handleLSPStop(ctx, out, handlers, req)
	case "lsp/query", "lsp/request":
		return false, handleLSPQuery(ctx, out, handlers, req)
	case "background/list":
		return false, handleBackgroundList(ctx, out, handlers, req)
	case "background/run":
		return false, handleBackgroundRun(ctx, out, handlers, req)
	case "background/get", "background/status":
		return false, handleBackgroundGet(ctx, out, handlers, req)
	case "background/logs":
		return false, handleBackgroundLogs(ctx, out, handlers, req)
	case "background/board":
		return false, handleBackgroundBoard(ctx, out, handlers, req)
	case "background/heartbeat":
		return false, handleBackgroundHeartbeat(ctx, out, handlers, req)
	case "background/stop":
		return false, handleBackgroundStop(ctx, out, handlers, req)
	case "background/restart":
		return false, handleBackgroundRestart(ctx, out, handlers, req)
	case "background/prune":
		return false, handleBackgroundPrune(ctx, out, handlers, req)
	case "background/supervise":
		return false, handleBackgroundSupervise(ctx, out, handlers, req)
	case "background/watch":
		return false, handleBackgroundWatch(ctx, out, handlers, req)
	case "agent-runs/list":
		return false, handleAgentRunsList(ctx, out, handlers, req)
	case "agent-runs/get", "agent-runs/status":
		return false, handleAgentRunsGet(ctx, out, handlers, req)
	case "agent-runs/logs":
		return false, handleAgentRunsLogs(ctx, out, handlers, req)
	case "agent-runs/board":
		return false, handleAgentRunsBoard(ctx, out, handlers, req)
	case "agent-runs/heartbeat":
		return false, handleAgentRunsHeartbeat(ctx, out, handlers, req)
	case "agent-runs/stop":
		return false, handleAgentRunsStop(ctx, out, handlers, req)
	case "agent-runs/prune":
		return false, handleAgentRunsPrune(ctx, out, handlers, req)
	case "mcp/list":
		return false, handleMCPList(ctx, out, handlers, req)
	case "mcp/show":
		return false, handleMCPShow(ctx, out, handlers, req)
	case "mcp/auth":
		return false, handleMCPAuth(ctx, out, handlers, req)
	case "mcp/tools":
		return false, handleMCPTools(ctx, out, handlers, req)
	case "mcp/call":
		return false, handleMCPCall(ctx, out, handlers, req)
	case "mcp/resources":
		return false, handleMCPResources(ctx, out, handlers, req)
	case "mcp/resource-templates", "mcp/resources-templates":
		return false, handleMCPResourceTemplates(ctx, out, handlers, req)
	case "mcp/read":
		return false, handleMCPRead(ctx, out, handlers, req)
	case "mcp/prompts":
		return false, handleMCPPrompts(ctx, out, handlers, req)
	case "mcp/prompt":
		return false, handleMCPPrompt(ctx, out, handlers, req)
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
			"editor": map[string]any{
				"identify":  true,
				"state":     true,
				"open":      true,
				"selection": true,
			},
			"bridge_faults": map[string]any{
				"list":   true,
				"record": true,
				"clear":  true,
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
			"lsp": map[string]any{
				"actions":  true,
				"discover": true,
				"list":     true,
				"start":    true,
				"status":   true,
				"stop":     true,
				"query":    true,
			},
			"background": map[string]any{
				"list":      true,
				"run":       true,
				"get":       true,
				"logs":      true,
				"board":     true,
				"heartbeat": true,
				"stop":      true,
				"restart":   true,
				"prune":     true,
				"supervise": true,
				"watch":     true,
			},
			"agent_runs": map[string]any{
				"list":      true,
				"get":       true,
				"logs":      true,
				"board":     true,
				"heartbeat": true,
				"stop":      true,
				"prune":     true,
			},
			"mcp": map[string]any{
				"list":               true,
				"show":               true,
				"auth":               true,
				"tools":              true,
				"call":               true,
				"resources":          true,
				"resource_templates": true,
				"read":               true,
				"prompts":            true,
				"prompt":             true,
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

func handleEditorIdentify(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.EditorIdentify == nil {
		return writeError(out, req.ID, -32603, "editor identify handler is not configured")
	}
	var request EditorIdentifyRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if strings.TrimSpace(request.Editor) == "" {
		return writeError(out, req.ID, -32602, "editor is required")
	}
	result, err := handlers.EditorIdentify(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleEditorState(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.EditorState == nil {
		return writeError(out, req.ID, -32603, "editor state handler is not configured")
	}
	result, err := handlers.EditorState(ctx)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleEditorOpen(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.EditorOpen == nil {
		return writeError(out, req.ID, -32603, "editor open handler is not configured")
	}
	var request EditorOpenRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if strings.TrimSpace(request.Path) == "" {
		return writeError(out, req.ID, -32602, "path is required")
	}
	result, err := handlers.EditorOpen(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleEditorSelection(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.EditorSelection == nil {
		return writeError(out, req.ID, -32603, "editor selection handler is not configured")
	}
	var request EditorSelectionRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if request.StartLine <= 0 {
		return writeError(out, req.ID, -32602, "start_line is required")
	}
	if request.EndLine < 0 {
		return writeError(out, req.ID, -32602, "end_line must be non-negative")
	}
	if request.StartColumn < 0 {
		return writeError(out, req.ID, -32602, "start_column must be non-negative")
	}
	if request.EndColumn < 0 {
		return writeError(out, req.ID, -32602, "end_column must be non-negative")
	}
	result, err := handlers.EditorSelection(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleBridgeFaultsList(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.BridgeFaultsList == nil {
		return writeError(out, req.ID, -32603, "bridge faults list handler is not configured")
	}
	result, err := handlers.BridgeFaultsList(ctx)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleBridgeFaultsRecord(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.BridgeFaultsRecord == nil {
		return writeError(out, req.ID, -32603, "bridge faults record handler is not configured")
	}
	var request BridgeFaultRecordRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if strings.TrimSpace(request.Action) == "" {
		return writeError(out, req.ID, -32602, "action is required")
	}
	result, err := handlers.BridgeFaultsRecord(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleBridgeFaultsClear(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.BridgeFaultsClear == nil {
		return writeError(out, req.ID, -32603, "bridge faults clear handler is not configured")
	}
	result, err := handlers.BridgeFaultsClear(ctx)
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

func handleLSPActions(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.LSPActions == nil {
		return writeError(out, req.ID, -32603, "lsp actions handler is not configured")
	}
	result, err := handlers.LSPActions(ctx)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleLSPDiscover(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.LSPDiscover == nil {
		return writeError(out, req.ID, -32603, "lsp discover handler is not configured")
	}
	result, err := handlers.LSPDiscover(ctx)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleLSPList(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.LSPList == nil {
		return writeError(out, req.ID, -32603, "lsp list handler is not configured")
	}
	result, err := handlers.LSPList(ctx)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleLSPStart(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.LSPStart == nil {
		return writeError(out, req.ID, -32603, "lsp start handler is not configured")
	}
	request, err := parseLSPStartRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.LSPStart(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleLSPStatus(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.LSPStatus == nil {
		return writeError(out, req.ID, -32603, "lsp status handler is not configured")
	}
	var request LSPStatusRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.LSPStatus(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleLSPStop(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.LSPStop == nil {
		return writeError(out, req.ID, -32603, "lsp stop handler is not configured")
	}
	var request LSPStopRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.LSPStop(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleLSPQuery(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.LSPQuery == nil {
		return writeError(out, req.ID, -32603, "lsp query handler is not configured")
	}
	var request LSPQueryRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if request.Line < 0 {
		return writeError(out, req.ID, -32602, "line must be non-negative")
	}
	if request.Character < 0 {
		return writeError(out, req.ID, -32602, "character must be non-negative")
	}
	if request.TimeoutMS < 0 {
		return writeError(out, req.ID, -32602, "timeout_ms must be non-negative")
	}
	result, err := handlers.LSPQuery(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleBackgroundList(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.BackgroundList == nil {
		return writeError(out, req.ID, -32603, "background list handler is not configured")
	}
	var request BackgroundListRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.BackgroundList(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleBackgroundRun(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.BackgroundRun == nil {
		return writeError(out, req.ID, -32603, "background run handler is not configured")
	}
	var request BackgroundRunRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if strings.TrimSpace(request.Command) == "" {
		return writeError(out, req.ID, -32602, "command is required")
	}
	result, err := handlers.BackgroundRun(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleBackgroundGet(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.BackgroundGet == nil {
		return writeError(out, req.ID, -32603, "background get handler is not configured")
	}
	request, err := parseBackgroundIDRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.BackgroundGet(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleBackgroundLogs(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.BackgroundLogs == nil {
		return writeError(out, req.ID, -32603, "background logs handler is not configured")
	}
	var request BackgroundLogsRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if strings.TrimSpace(request.ID) == "" {
		return writeError(out, req.ID, -32602, "id is required")
	}
	if request.Limit < 0 {
		return writeError(out, req.ID, -32602, "limit must be non-negative")
	}
	result, err := handlers.BackgroundLogs(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleBackgroundBoard(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.BackgroundBoard == nil {
		return writeError(out, req.ID, -32603, "background board handler is not configured")
	}
	var request BackgroundBoardRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if request.StalledAfterMS < 0 {
		return writeError(out, req.ID, -32602, "stalled_after_ms must be non-negative")
	}
	if request.StalledAfterSeconds < 0 {
		return writeError(out, req.ID, -32602, "stalled_after_seconds must be non-negative")
	}
	result, err := handlers.BackgroundBoard(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleBackgroundHeartbeat(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.BackgroundHeartbeat == nil {
		return writeError(out, req.ID, -32603, "background heartbeat handler is not configured")
	}
	var request BackgroundHeartbeatRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if strings.TrimSpace(request.ID) == "" {
		return writeError(out, req.ID, -32602, "id is required")
	}
	result, err := handlers.BackgroundHeartbeat(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleBackgroundStop(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.BackgroundStop == nil {
		return writeError(out, req.ID, -32603, "background stop handler is not configured")
	}
	request, err := parseBackgroundIDRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.BackgroundStop(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleBackgroundRestart(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.BackgroundRestart == nil {
		return writeError(out, req.ID, -32603, "background restart handler is not configured")
	}
	request, err := parseBackgroundIDRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.BackgroundRestart(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleBackgroundPrune(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.BackgroundPrune == nil {
		return writeError(out, req.ID, -32603, "background prune handler is not configured")
	}
	var request BackgroundPruneRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if request.OlderThanSeconds < 0 {
		return writeError(out, req.ID, -32602, "older_than_seconds must be non-negative")
	}
	if request.OlderThanDays < 0 {
		return writeError(out, req.ID, -32602, "older_than_days must be non-negative")
	}
	if request.Keep != nil && *request.Keep < 0 {
		return writeError(out, req.ID, -32602, "keep must be non-negative")
	}
	result, err := handlers.BackgroundPrune(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleBackgroundSupervise(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.BackgroundSupervise == nil {
		return writeError(out, req.ID, -32603, "background supervise handler is not configured")
	}
	var request BackgroundSuperviseRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.BackgroundSupervise(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleBackgroundWatch(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.BackgroundWatch == nil {
		return writeError(out, req.ID, -32603, "background watch handler is not configured")
	}
	var request BackgroundWatchRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if strings.TrimSpace(request.ID) == "" {
		return writeError(out, req.ID, -32602, "id is required")
	}
	if request.Offset < 0 {
		return writeError(out, req.ID, -32602, "offset must be non-negative")
	}
	if request.IntervalMS < 0 {
		return writeError(out, req.ID, -32602, "interval_ms must be non-negative")
	}
	if request.MaxEvents < 0 {
		return writeError(out, req.ID, -32602, "max_events must be non-negative")
	}
	emit := func(event background.WatchEvent) error {
		return writeNotification(out, "background/event", event)
	}
	result, err := handlers.BackgroundWatch(ctx, request, emit)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleAgentRunsList(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.AgentRunsList == nil {
		return writeError(out, req.ID, -32603, "agent runs list handler is not configured")
	}
	var request AgentRunsListRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.AgentRunsList(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleAgentRunsGet(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.AgentRunsGet == nil {
		return writeError(out, req.ID, -32603, "agent runs get handler is not configured")
	}
	request, err := parseAgentRunIDRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.AgentRunsGet(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleAgentRunsLogs(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.AgentRunsLogs == nil {
		return writeError(out, req.ID, -32603, "agent runs logs handler is not configured")
	}
	var request AgentRunLogsRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if strings.TrimSpace(request.ID) == "" {
		return writeError(out, req.ID, -32602, "id is required")
	}
	if request.Limit < 0 {
		return writeError(out, req.ID, -32602, "limit must be non-negative")
	}
	result, err := handlers.AgentRunsLogs(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleAgentRunsBoard(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.AgentRunsBoard == nil {
		return writeError(out, req.ID, -32603, "agent runs board handler is not configured")
	}
	var request AgentRunsBoardRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if request.StalledAfterMS < 0 {
		return writeError(out, req.ID, -32602, "stalled_after_ms must be non-negative")
	}
	if request.StalledAfterSeconds < 0 {
		return writeError(out, req.ID, -32602, "stalled_after_seconds must be non-negative")
	}
	result, err := handlers.AgentRunsBoard(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleAgentRunsHeartbeat(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.AgentRunsHeartbeat == nil {
		return writeError(out, req.ID, -32603, "agent runs heartbeat handler is not configured")
	}
	var request AgentRunHeartbeatRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if strings.TrimSpace(request.ID) == "" {
		return writeError(out, req.ID, -32602, "id is required")
	}
	result, err := handlers.AgentRunsHeartbeat(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleAgentRunsStop(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.AgentRunsStop == nil {
		return writeError(out, req.ID, -32603, "agent runs stop handler is not configured")
	}
	request, err := parseAgentRunIDRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.AgentRunsStop(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleAgentRunsPrune(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.AgentRunsPrune == nil {
		return writeError(out, req.ID, -32603, "agent runs prune handler is not configured")
	}
	var request AgentRunsPruneRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if request.OlderThanSeconds < 0 {
		return writeError(out, req.ID, -32602, "older_than_seconds must be non-negative")
	}
	if request.OlderThanDays < 0 {
		return writeError(out, req.ID, -32602, "older_than_days must be non-negative")
	}
	if request.Keep != nil && *request.Keep < 0 {
		return writeError(out, req.ID, -32602, "keep must be non-negative")
	}
	result, err := handlers.AgentRunsPrune(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleMCPList(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.MCPList == nil {
		return writeError(out, req.ID, -32603, "mcp list handler is not configured")
	}
	var request MCPListRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.MCPList(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleMCPShow(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.MCPShow == nil {
		return writeError(out, req.ID, -32603, "mcp show handler is not configured")
	}
	request, err := parseMCPServerRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if firstNonEmpty(request.Server, request.Name) == "" {
		return writeError(out, req.ID, -32602, "server is required")
	}
	result, err := handlers.MCPShow(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleMCPAuth(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.MCPAuth == nil {
		return writeError(out, req.ID, -32603, "mcp auth handler is not configured")
	}
	request, err := parseMCPServerRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.MCPAuth(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleMCPTools(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.MCPTools == nil {
		return writeError(out, req.ID, -32603, "mcp tools handler is not configured")
	}
	request, err := parseMCPServerRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.MCPTools(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleMCPCall(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.MCPCall == nil {
		return writeError(out, req.ID, -32603, "mcp call handler is not configured")
	}
	var request MCPCallRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if strings.TrimSpace(request.Tool) == "" {
		return writeError(out, req.ID, -32602, "tool is required")
	}
	result, err := handlers.MCPCall(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleMCPResources(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.MCPResources == nil {
		return writeError(out, req.ID, -32603, "mcp resources handler is not configured")
	}
	request, err := parseMCPServerRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.MCPResources(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleMCPResourceTemplates(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.MCPResourceTemplates == nil {
		return writeError(out, req.ID, -32603, "mcp resource templates handler is not configured")
	}
	request, err := parseMCPServerRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.MCPResourceTemplates(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleMCPRead(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.MCPRead == nil {
		return writeError(out, req.ID, -32603, "mcp read handler is not configured")
	}
	var request MCPReadRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if strings.TrimSpace(request.URI) == "" {
		return writeError(out, req.ID, -32602, "uri is required")
	}
	result, err := handlers.MCPRead(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleMCPPrompts(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.MCPPrompts == nil {
		return writeError(out, req.ID, -32603, "mcp prompts handler is not configured")
	}
	request, err := parseMCPServerRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.MCPPrompts(ctx, request)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, result)
}

func handleMCPPrompt(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.MCPPrompt == nil {
		return writeError(out, req.ID, -32603, "mcp prompt handler is not configured")
	}
	var request MCPPromptRequest
	if err := unmarshalParams(req.Params, &request); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if firstNonEmpty(request.Prompt, request.Name) == "" {
		return writeError(out, req.ID, -32602, "prompt is required")
	}
	result, err := handlers.MCPPrompt(ctx, request)
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

func parseLSPStartRequest(params json.RawMessage) (LSPStartRequest, error) {
	var payload struct {
		Language    string          `json:"language"`
		Command     json.RawMessage `json:"command"`
		CommandArgs []string        `json:"command_args"`
		Args        []string        `json:"args"`
	}
	if err := unmarshalParams(params, &payload); err != nil {
		return LSPStartRequest{}, err
	}
	commandArgs, err := parseLSPCommandArgs(payload.Command, payload.CommandArgs, payload.Args)
	if err != nil {
		return LSPStartRequest{}, err
	}
	return LSPStartRequest{
		Language:    payload.Language,
		CommandArgs: commandArgs,
	}, nil
}

func parseLSPCommandArgs(command json.RawMessage, commandArgs []string, args []string) ([]string, error) {
	if len(commandArgs) > 0 {
		return append([]string(nil), commandArgs...), nil
	}
	if len(args) > 0 {
		return append([]string(nil), args...), nil
	}
	if len(command) == 0 || string(command) == "null" {
		return nil, nil
	}
	var list []string
	if err := json.Unmarshal(command, &list); err == nil {
		return list, nil
	}
	var raw string
	if err := json.Unmarshal(command, &raw); err != nil {
		return nil, fmt.Errorf("command must be a string or string array")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	return []string{"sh", "-lc", raw}, nil
}

func parseBackgroundIDRequest(params json.RawMessage) (BackgroundIDRequest, error) {
	var raw BackgroundIDRequest
	if len(params) != 0 {
		if err := json.Unmarshal(params, &raw); err == nil && strings.TrimSpace(raw.ID) != "" {
			return raw, nil
		}
		var id string
		if err := json.Unmarshal(params, &id); err == nil && strings.TrimSpace(id) != "" {
			return BackgroundIDRequest{ID: id}, nil
		}
		if err := json.Unmarshal(params, &raw); err != nil {
			return BackgroundIDRequest{}, err
		}
	}
	if strings.TrimSpace(raw.ID) == "" {
		return BackgroundIDRequest{}, fmt.Errorf("id is required")
	}
	return raw, nil
}

func parseAgentRunIDRequest(params json.RawMessage) (AgentRunIDRequest, error) {
	var raw AgentRunIDRequest
	if len(params) != 0 {
		if err := json.Unmarshal(params, &raw); err == nil && strings.TrimSpace(raw.ID) != "" {
			return raw, nil
		}
		var id string
		if err := json.Unmarshal(params, &id); err == nil && strings.TrimSpace(id) != "" {
			return AgentRunIDRequest{ID: id}, nil
		}
		if err := json.Unmarshal(params, &raw); err != nil {
			return AgentRunIDRequest{}, err
		}
	}
	if strings.TrimSpace(raw.ID) == "" {
		return AgentRunIDRequest{}, fmt.Errorf("id is required")
	}
	return raw, nil
}

func parseMCPServerRequest(params json.RawMessage) (MCPServerRequest, error) {
	var raw MCPServerRequest
	if len(params) != 0 {
		if err := json.Unmarshal(params, &raw); err == nil && firstNonEmpty(raw.Server, raw.Name) != "" {
			return raw, nil
		}
		var server string
		if err := json.Unmarshal(params, &server); err == nil && strings.TrimSpace(server) != "" {
			return MCPServerRequest{Server: server}, nil
		}
		if err := json.Unmarshal(params, &raw); err != nil {
			return MCPServerRequest{}, err
		}
	}
	return raw, nil
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

func writeNotification(out io.Writer, method string, params any) error {
	data, err := json.Marshal(notification{JSONRPC: "2.0", Method: method, Params: params})
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
