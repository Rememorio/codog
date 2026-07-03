package acpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/workspaceops"
	"github.com/stretchr/testify/require"
)

func TestServeHandlesACPRequests(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/open","params":{"id":"session-opened"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"prompt","params":{"session_id":"session-1","prompt":"hello"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"status","params":{}}`,
		`{"jsonrpc":"2.0","id":6,"method":"session/list","params":{}}`,
		`{"jsonrpc":"2.0","id":7,"method":"session/get","params":{"sessionId":"session-1"}}`,
		`{"jsonrpc":"2.0","id":8,"method":"session/history","params":{"session_id":"session-1","limit":1}}`,
		`{"jsonrpc":"2.0","id":9,"method":"session/append_input","params":{"session_id":"session-1","input":"next prompt"}}`,
		`{"jsonrpc":"2.0","id":10,"method":"session/append_message","params":{"session_id":"session-1","role":"assistant","text":"saved answer"}}`,
		`{"jsonrpc":"2.0","id":11,"method":"session/rewind","params":{"session_id":"session-1","removeMessages":1}}`,
		`{"jsonrpc":"2.0","id":12,"method":"session/fork","params":{"session_id":"session-1","branchName":"scratch"}}`,
		`{"jsonrpc":"2.0","id":13,"method":"session/rename","params":{"session_id":"session-1","newSessionId":"session-2"}}`,
		`{"jsonrpc":"2.0","id":14,"method":"session/delete","params":{"session_id":"session-2"}}`,
		`{"jsonrpc":"2.0","id":15,"method":"session/prune","params":{"keep":3,"confirm":true,"excludeSessionId":"session-2"}}`,
		`{"jsonrpc":"2.0","id":16,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	err := Serve(context.Background(), strings.NewReader(input), &out, Handlers{
		NewSession: func(context.Context) (SessionInfo, error) {
			return SessionInfo{SessionID: "session-1"}, nil
		},
		OpenSession: func(_ context.Context, req SessionOpenRequest) (SessionDetail, error) {
			require.Equal(t, "session-opened", req.SessionID)
			return SessionDetail{SessionID: req.SessionID, MessageCount: 0}, nil
		},
		Prompt: func(_ context.Context, req PromptRequest) (PromptResult, error) {
			require.Equal(t, "session-1", req.SessionID)
			require.Equal(t, "hello", req.Prompt)
			return PromptResult{SessionID: req.SessionID, Output: "world"}, nil
		},
		Status: func(context.Context) (any, error) {
			return map[string]any{"kind": "acp", "status": "ok"}, nil
		},
		ListSessions: func(context.Context) (SessionList, error) {
			return SessionList{Sessions: []SessionSummary{{SessionID: "session-1", Workspace: "/workspace", MessageCount: 2}}}, nil
		},
		GetSession: func(_ context.Context, req SessionLookupRequest) (SessionDetail, error) {
			require.Equal(t, "session-1", req.SessionID)
			return SessionDetail{SessionID: "session-1", MessageCount: 2, Messages: []map[string]string{{"role": "user"}}}, nil
		},
		History: func(_ context.Context, req SessionHistoryRequest) (SessionHistory, error) {
			require.Equal(t, "session-1", req.SessionID)
			require.Equal(t, 1, req.Limit)
			return SessionHistory{SessionID: "session-1", Entries: []map[string]any{{"text": "hello"}}}, nil
		},
		AppendInput: func(_ context.Context, req SessionAppendInputRequest) (SessionMutationResult, error) {
			require.Equal(t, "session-1", req.SessionID)
			require.Equal(t, "next prompt", req.Input)
			return SessionMutationResult{SessionID: req.SessionID, MessageCount: 2}, nil
		},
		AppendMessage: func(_ context.Context, req SessionAppendMessageRequest) (SessionMutationResult, error) {
			require.Equal(t, "session-1", req.SessionID)
			require.Nil(t, req.Message)
			require.Equal(t, "assistant", req.Role)
			require.Equal(t, "saved answer", req.Text)
			return SessionMutationResult{SessionID: req.SessionID, MessageCount: 3}, nil
		},
		RewindSession: func(_ context.Context, req SessionRewindRequest) (SessionRewindResult, error) {
			require.Equal(t, "session-1", req.SessionID)
			require.Equal(t, 1, req.RemoveMessages)
			return SessionRewindResult{SessionID: req.SessionID, RemovedMessages: 1}, nil
		},
		ForkSession: func(_ context.Context, req SessionForkRequest) (SessionMutationResult, error) {
			require.Equal(t, "session-1", req.SessionID)
			require.Equal(t, "scratch", req.BranchName)
			return SessionMutationResult{SessionID: "session-fork"}, nil
		},
		RenameSession: func(_ context.Context, req SessionRenameRequest) (SessionMutationResult, error) {
			require.Equal(t, "session-1", req.SessionID)
			require.Equal(t, "session-2", req.NewSessionID)
			return SessionMutationResult{SessionID: req.SessionID, NewSessionID: req.NewSessionID}, nil
		},
		DeleteSession: func(_ context.Context, req SessionLookupRequest) (SessionMutationResult, error) {
			require.Equal(t, "session-2", req.SessionID)
			return SessionMutationResult{SessionID: req.SessionID}, nil
		},
		PruneSessions: func(_ context.Context, req SessionPruneRequest) (any, error) {
			require.Equal(t, 3, req.Keep)
			require.True(t, req.Confirm)
			require.Equal(t, "session-2", req.ExcludeID)
			return map[string]any{"kind": "session_prune", "status": "ok", "deleted_count": 1}, nil
		},
	}, Options{Version: "test", Workspace: "/workspace"})
	require.NoError(t, err)

	responses := decodeACPResponses(t, out.String())
	require.Len(t, responses, 16)
	require.Equal(t, "test", responses[0]["result"].(map[string]any)["serverInfo"].(map[string]any)["version"])
	capabilities := responses[0]["result"].(map[string]any)["capabilities"].(map[string]any)
	require.Equal(t, true, capabilities["prompt"])
	workspaceCaps := capabilities["workspace"].(map[string]any)
	require.Equal(t, true, workspaceCaps["info"])
	require.Equal(t, true, workspaceCaps["files"])
	require.Equal(t, true, workspaceCaps["search"])
	fileCaps := capabilities["file"].(map[string]any)
	require.Equal(t, true, fileCaps["read"])
	require.Equal(t, true, fileCaps["write"])
	require.Equal(t, true, fileCaps["edit"])
	require.Equal(t, true, fileCaps["diff"])
	diagnosticsCaps := capabilities["diagnostics"].(map[string]any)
	require.Equal(t, true, diagnosticsCaps["go"])
	codeCaps := capabilities["code"].(map[string]any)
	require.Equal(t, true, codeCaps["symbols"])
	require.Equal(t, true, codeCaps["references"])
	require.Equal(t, true, codeCaps["definition"])
	require.Equal(t, true, codeCaps["hover"])
	require.Equal(t, true, codeCaps["completion"])
	require.Equal(t, true, codeCaps["format"])
	notebookCaps := capabilities["notebook"].(map[string]any)
	require.Equal(t, true, notebookCaps["read"])
	require.Equal(t, true, notebookCaps["edit"])
	lspCaps := capabilities["lsp"].(map[string]any)
	require.Equal(t, true, lspCaps["actions"])
	require.Equal(t, true, lspCaps["discover"])
	require.Equal(t, true, lspCaps["list"])
	require.Equal(t, true, lspCaps["start"])
	require.Equal(t, true, lspCaps["status"])
	require.Equal(t, true, lspCaps["stop"])
	require.Equal(t, true, lspCaps["query"])
	backgroundCaps := capabilities["background"].(map[string]any)
	require.Equal(t, true, backgroundCaps["list"])
	require.Equal(t, true, backgroundCaps["run"])
	require.Equal(t, true, backgroundCaps["get"])
	require.Equal(t, true, backgroundCaps["logs"])
	require.Equal(t, true, backgroundCaps["board"])
	require.Equal(t, true, backgroundCaps["heartbeat"])
	require.Equal(t, true, backgroundCaps["stop"])
	require.Equal(t, true, backgroundCaps["restart"])
	require.Equal(t, true, backgroundCaps["prune"])
	require.Equal(t, true, backgroundCaps["supervise"])
	require.Equal(t, true, backgroundCaps["watch"])
	agentRunCaps := capabilities["agent_runs"].(map[string]any)
	require.Equal(t, true, agentRunCaps["list"])
	require.Equal(t, true, agentRunCaps["get"])
	require.Equal(t, true, agentRunCaps["logs"])
	require.Equal(t, true, agentRunCaps["board"])
	require.Equal(t, true, agentRunCaps["heartbeat"])
	require.Equal(t, true, agentRunCaps["stop"])
	require.Equal(t, true, agentRunCaps["prune"])
	mcpCaps := capabilities["mcp"].(map[string]any)
	require.Equal(t, true, mcpCaps["list"])
	require.Equal(t, true, mcpCaps["show"])
	require.Equal(t, true, mcpCaps["auth"])
	require.Equal(t, true, mcpCaps["tools"])
	require.Equal(t, true, mcpCaps["call"])
	require.Equal(t, true, mcpCaps["resources"])
	require.Equal(t, true, mcpCaps["resource_templates"])
	require.Equal(t, true, mcpCaps["read"])
	require.Equal(t, true, mcpCaps["prompts"])
	require.Equal(t, true, mcpCaps["prompt"])
	sessionCaps := capabilities["sessions"].(map[string]any)
	require.Equal(t, true, sessionCaps["open"])
	require.Equal(t, true, sessionCaps["history"])
	require.Equal(t, true, sessionCaps["append"])
	require.Equal(t, true, sessionCaps["rewind"])
	require.Equal(t, true, sessionCaps["fork"])
	require.Equal(t, true, sessionCaps["rename"])
	require.Equal(t, true, sessionCaps["delete"])
	require.Equal(t, true, sessionCaps["prune"])
	require.Equal(t, "session-1", responses[1]["result"].(map[string]any)["session_id"])
	openResult := responses[2]["result"].(map[string]any)
	require.Equal(t, "session-opened", openResult["session_id"])
	require.EqualValues(t, 0, openResult["message_count"])
	promptResult := responses[3]["result"].(map[string]any)
	require.Equal(t, "world", promptResult["text"])
	require.Equal(t, "ok", responses[4]["result"].(map[string]any)["status"])
	listResult := responses[5]["result"].(map[string]any)
	require.Equal(t, "session_list", listResult["kind"])
	require.EqualValues(t, 1, listResult["count"])
	getResult := responses[6]["result"].(map[string]any)
	require.Equal(t, "session-1", getResult["session_id"])
	require.EqualValues(t, 2, getResult["message_count"])
	historyResult := responses[7]["result"].(map[string]any)
	require.Equal(t, "session_history", historyResult["kind"])
	require.Equal(t, "session-1", historyResult["session_id"])
	appendInputResult := responses[8]["result"].(map[string]any)
	require.Equal(t, "session_mutation", appendInputResult["kind"])
	require.Equal(t, "append_input", appendInputResult["action"])
	require.Equal(t, "session-1", appendInputResult["session_id"])
	appendMessageResult := responses[9]["result"].(map[string]any)
	require.Equal(t, "session_mutation", appendMessageResult["kind"])
	require.Equal(t, "append_message", appendMessageResult["action"])
	require.Equal(t, "session-1", appendMessageResult["session_id"])
	rewindResult := responses[10]["result"].(map[string]any)
	require.Equal(t, "session_mutation", rewindResult["kind"])
	require.Equal(t, "rewind", rewindResult["action"])
	require.EqualValues(t, 1, rewindResult["removed_messages"])
	forkResult := responses[11]["result"].(map[string]any)
	require.Equal(t, "session_mutation", forkResult["kind"])
	require.Equal(t, "fork", forkResult["action"])
	require.Equal(t, "session-fork", forkResult["session_id"])
	renameResult := responses[12]["result"].(map[string]any)
	require.Equal(t, "session_mutation", renameResult["kind"])
	require.Equal(t, "rename", renameResult["action"])
	require.Equal(t, "session-2", renameResult["new_session_id"])
	deleteResult := responses[13]["result"].(map[string]any)
	require.Equal(t, "session_mutation", deleteResult["kind"])
	require.Equal(t, "delete", deleteResult["action"])
	require.Equal(t, "session-2", deleteResult["session_id"])
	pruneResult := responses[14]["result"].(map[string]any)
	require.Equal(t, "session_prune", pruneResult["kind"])
	require.EqualValues(t, 1, pruneResult["deleted_count"])
	require.NotNil(t, responses[15]["result"])
}

func TestServeHandlesWorkspaceRequests(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"workspace/info","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"workspace/files","params":{"path":"src","pattern":"*.go","limit":3}}`,
		`{"jsonrpc":"2.0","id":3,"method":"workspace/search","params":{"query":"needle","glob":"*.go","limit":2}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	err := Serve(context.Background(), strings.NewReader(input), &out, Handlers{
		WorkspaceInfo: func(context.Context) (workspaceops.InfoResult, error) {
			return workspaceops.InfoResult{Path: "/workspace", Name: "workspace"}, nil
		},
		WorkspaceFiles: func(_ context.Context, options workspaceops.FilesOptions) (workspaceops.FilesResult, error) {
			require.Equal(t, "src", options.Path)
			require.Equal(t, "*.go", options.Pattern)
			require.Equal(t, 3, options.Limit)
			return workspaceops.FilesResult{Root: "src", Files: []workspaceops.FileEntry{{Path: "src/main.go"}}}, nil
		},
		WorkspaceSearch: func(_ context.Context, options workspaceops.SearchOptions) (workspaceops.SearchResult, error) {
			require.Equal(t, "needle", options.Query)
			require.Equal(t, "*.go", options.Glob)
			require.Equal(t, 2, options.Limit)
			return workspaceops.SearchResult{Matches: []workspaceops.SearchMatch{{Path: "src/main.go", Line: 1, Text: "needle"}}}, nil
		},
	}, Options{})
	require.NoError(t, err)

	responses := decodeACPResponses(t, out.String())
	require.Len(t, responses, 3)
	info := responses[0]["result"].(map[string]any)
	require.Equal(t, "/workspace", info["path"])
	require.Equal(t, "workspace", info["name"])
	files := responses[1]["result"].(map[string]any)
	require.Equal(t, "src", files["root"])
	require.Len(t, files["files"].([]any), 1)
	search := responses[2]["result"].(map[string]any)
	require.Len(t, search["matches"].([]any), 1)
}

func TestServeHandlesFileRequests(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"file/read","params":{"path":"src/main.go","offset":2,"limit":5}}`,
		`{"jsonrpc":"2.0","id":2,"method":"file/write","params":{"path":"src/new.go","content":"package main\n"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"file/edit","params":{"path":"src/new.go","old_string":"main","new_string":"codog","replace_all":true}}`,
		`{"jsonrpc":"2.0","id":4,"method":"file/diff","params":{"path":"src/new.go","old_string":"codog","new_string":"main"}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	err := Serve(context.Background(), strings.NewReader(input), &out, Handlers{
		FileRead: func(_ context.Context, options workspaceops.ReadOptions) (workspaceops.ReadResult, error) {
			require.Equal(t, "src/main.go", options.Path)
			require.Equal(t, 2, options.Offset)
			require.Equal(t, 5, options.Limit)
			return workspaceops.ReadResult{Path: options.Path, Content: "hello", Bytes: 12, Truncated: true}, nil
		},
		FileWrite: func(_ context.Context, options workspaceops.WriteOptions) (workspaceops.WriteResult, error) {
			require.Equal(t, "src/new.go", options.Path)
			require.Equal(t, "package main\n", options.Content)
			return workspaceops.WriteResult{Path: options.Path, Bytes: len(options.Content)}, nil
		},
		FileEdit: func(_ context.Context, options workspaceops.EditOptions) (workspaceops.EditResult, error) {
			require.Equal(t, "src/new.go", options.Path)
			require.Equal(t, "main", options.OldString)
			require.Equal(t, "codog", options.NewString)
			require.True(t, options.ReplaceAll)
			return workspaceops.EditResult{Path: options.Path, Replacements: 1}, nil
		},
		FileDiff: func(_ context.Context, options workspaceops.DiffOptions) (workspaceops.DiffResult, error) {
			require.Equal(t, "src/new.go", options.Path)
			require.Equal(t, "codog", options.OldString)
			require.Equal(t, "main", options.NewString)
			return workspaceops.DiffResult{Path: options.Path, Diff: "--- src/new.go\n+++ src/new.go\n"}, nil
		},
	}, Options{})
	require.NoError(t, err)

	responses := decodeACPResponses(t, out.String())
	require.Len(t, responses, 4)
	readResult := responses[0]["result"].(map[string]any)
	require.Equal(t, "src/main.go", readResult["path"])
	require.Equal(t, "hello", readResult["content"])
	require.Equal(t, true, readResult["truncated"])
	writeResult := responses[1]["result"].(map[string]any)
	require.Equal(t, "src/new.go", writeResult["path"])
	require.EqualValues(t, len("package main\n"), writeResult["bytes"])
	editResult := responses[2]["result"].(map[string]any)
	require.EqualValues(t, 1, editResult["replacements"])
	diffResult := responses[3]["result"].(map[string]any)
	require.Contains(t, diffResult["diff"], "--- src/new.go")
}

func TestServeHandlesCodeIntelRequests(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"diagnostics/go","params":{"patterns":["./..."]}}`,
		`{"jsonrpc":"2.0","id":2,"method":"code/symbols","params":{"path":"main.go"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"code/references","params":{"symbol":"Run","limit":2}}`,
		`{"jsonrpc":"2.0","id":4,"method":"code/definition","params":{"symbol":"Run"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"code/hover","params":{"symbol":"Run","context_lines":1}}`,
		`{"jsonrpc":"2.0","id":6,"method":"code/completion","params":{"query":"Ru","limit":3}}`,
		`{"jsonrpc":"2.0","id":7,"method":"code/format","params":{"path":"main.go","write":true}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	err := Serve(context.Background(), strings.NewReader(input), &out, Handlers{
		DiagnosticsGo: func(_ context.Context, req DiagnosticsRequest) (any, error) {
			require.Equal(t, []string{"./..."}, req.Patterns)
			return map[string]any{"kind": "diagnostics", "total": 0}, nil
		},
		CodeSymbols: func(_ context.Context, req CodeSymbolsRequest) (any, error) {
			require.Equal(t, "main.go", req.Path)
			return map[string]any{"kind": "symbols", "total": 1, "symbols": []map[string]any{{"name": "Run"}}}, nil
		},
		CodeReferences: func(_ context.Context, req CodeReferencesRequest) (any, error) {
			require.Equal(t, "Run", req.Symbol)
			require.Equal(t, 2, req.Limit)
			return map[string]any{"kind": "references", "symbol": req.Symbol, "total": 1}, nil
		},
		CodeDefinition: func(_ context.Context, req CodeDefinitionRequest) (any, error) {
			require.Equal(t, "Run", req.Symbol)
			return map[string]any{"kind": "definition", "symbol": req.Symbol, "found": true}, nil
		},
		CodeHover: func(_ context.Context, req CodeHoverRequest) (any, error) {
			require.Equal(t, "Run", req.Symbol)
			require.Equal(t, 1, req.ContextLines)
			return map[string]any{"kind": "hover", "symbol": req.Symbol}, nil
		},
		CodeCompletion: func(_ context.Context, req CodeCompletionRequest) (any, error) {
			require.Equal(t, "Ru", req.Query)
			require.Equal(t, 3, req.Limit)
			return map[string]any{"kind": "completion", "query": req.Query, "total": 1}, nil
		},
		CodeFormat: func(_ context.Context, req CodeFormatRequest) (any, error) {
			require.Equal(t, "main.go", req.Path)
			require.True(t, req.Write)
			return map[string]any{"kind": "format", "write": req.Write, "result": map[string]any{"path": req.Path, "changed": true}}, nil
		},
	}, Options{})
	require.NoError(t, err)

	responses := decodeACPResponses(t, out.String())
	require.Len(t, responses, 7)
	require.Equal(t, "diagnostics", responses[0]["result"].(map[string]any)["kind"])
	require.Equal(t, "symbols", responses[1]["result"].(map[string]any)["kind"])
	require.Equal(t, "references", responses[2]["result"].(map[string]any)["kind"])
	require.Equal(t, "definition", responses[3]["result"].(map[string]any)["kind"])
	require.Equal(t, "hover", responses[4]["result"].(map[string]any)["kind"])
	require.Equal(t, "completion", responses[5]["result"].(map[string]any)["kind"])
	require.Equal(t, "format", responses[6]["result"].(map[string]any)["kind"])
}

func TestServeHandlesNotebookRequests(t *testing.T) {
	source := "print('hello')\n"
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"notebook/read","params":{"notebook_path":"nb.ipynb","index":0,"limit":1,"outputs":true}}`,
		`{"jsonrpc":"2.0","id":2,"method":"notebook/edit","params":{"path":"nb.ipynb","mode":"insert","cell_index":0,"cell_type":"code","new_source":` + strconv.Quote(source) + `}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	err := Serve(context.Background(), strings.NewReader(input), &out, Handlers{
		NotebookRead: func(_ context.Context, req NotebookReadRequest) (any, error) {
			require.Equal(t, "nb.ipynb", req.NotebookPath)
			require.NotNil(t, req.Index)
			require.Equal(t, 0, *req.Index)
			require.Equal(t, 1, req.Limit)
			require.NotNil(t, req.Outputs)
			require.True(t, *req.Outputs)
			return map[string]any{"kind": "notebook_read", "path": req.NotebookPath, "cell_count": 1}, nil
		},
		NotebookEdit: func(_ context.Context, req NotebookEditRequest) (any, error) {
			require.Equal(t, "nb.ipynb", req.Path)
			require.Equal(t, "insert", req.Mode)
			require.NotNil(t, req.CellIndex)
			require.Equal(t, 0, *req.CellIndex)
			require.Equal(t, "code", req.CellType)
			require.NotNil(t, req.NewSource)
			require.Equal(t, source, *req.NewSource)
			return map[string]any{"kind": "notebook_edit", "result": map[string]any{"path": req.Path, "mode": req.Mode}}, nil
		},
	}, Options{})
	require.NoError(t, err)

	responses := decodeACPResponses(t, out.String())
	require.Len(t, responses, 2)
	require.Equal(t, "notebook_read", responses[0]["result"].(map[string]any)["kind"])
	require.Equal(t, "notebook_edit", responses[1]["result"].(map[string]any)["kind"])
}

func TestServeHandlesLSPRequests(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"lsp/actions","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"lsp/discover","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"lsp/list","params":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"lsp/start","params":{"language":"go","command":"gopls -remote=auto"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"lsp/query","params":{"language":"go","action":"hover","path":"main.go","line":2,"character":5,"timeout_ms":1000}}`,
		`{"jsonrpc":"2.0","id":6,"method":"lsp/status","params":{"language":"go"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"lsp/stop","params":{"language":"go"}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	err := Serve(context.Background(), strings.NewReader(input), &out, Handlers{
		LSPActions: func(context.Context) (any, error) {
			return map[string]any{"kind": "lsp_actions", "count": 1}, nil
		},
		LSPDiscover: func(context.Context) (any, error) {
			return map[string]any{"kind": "lsp_discover", "count": 1}, nil
		},
		LSPList: func(context.Context) (any, error) {
			return map[string]any{"kind": "lsp_list", "count": 0, "servers": []any{}}, nil
		},
		LSPStart: func(_ context.Context, req LSPStartRequest) (any, error) {
			require.Equal(t, "go", req.Language)
			require.Equal(t, []string{"sh", "-lc", "gopls -remote=auto"}, req.CommandArgs)
			return map[string]any{"kind": "lsp_start", "status": "ok"}, nil
		},
		LSPQuery: func(_ context.Context, req LSPQueryRequest) (any, error) {
			require.Equal(t, "go", req.Language)
			require.Equal(t, "hover", req.Action)
			require.Equal(t, "main.go", req.Path)
			require.Equal(t, 2, req.Line)
			require.Equal(t, 5, req.Character)
			require.Equal(t, 1000, req.TimeoutMS)
			return map[string]any{"kind": "lsp_query", "action": req.Action}, nil
		},
		LSPStatus: func(_ context.Context, req LSPStatusRequest) (any, error) {
			require.Equal(t, "go", req.Language)
			return map[string]any{"kind": "lsp_status"}, nil
		},
		LSPStop: func(_ context.Context, req LSPStopRequest) (any, error) {
			require.Equal(t, "go", req.Language)
			return map[string]any{"kind": "lsp_stop", "status": "ok"}, nil
		},
	}, Options{})
	require.NoError(t, err)

	responses := decodeACPResponses(t, out.String())
	require.Len(t, responses, 7)
	require.Equal(t, "lsp_actions", responses[0]["result"].(map[string]any)["kind"])
	require.Equal(t, "lsp_discover", responses[1]["result"].(map[string]any)["kind"])
	require.Equal(t, "lsp_list", responses[2]["result"].(map[string]any)["kind"])
	require.Empty(t, responses[2]["result"].(map[string]any)["servers"].([]any))
	require.Equal(t, "lsp_start", responses[3]["result"].(map[string]any)["kind"])
	require.Equal(t, "lsp_query", responses[4]["result"].(map[string]any)["kind"])
	require.Equal(t, "lsp_status", responses[5]["result"].(map[string]any)["kind"])
	require.Equal(t, "lsp_stop", responses[6]["result"].(map[string]any)["kind"])
}

func TestServeHandlesBackgroundRequests(t *testing.T) {
	observedAt := time.Date(2026, 7, 3, 1, 0, 0, 0, time.UTC)
	keep := 2
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"background/run","params":{"command":"printf acp","kind":"terminal","session_id":"session-1","restart_policy":{"enabled":true,"max_attempts":1}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"background/list","params":{"session_id":"session-1","kind":"terminal"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"background/get","params":{"id":"task-1"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"background/logs","params":{"id":"task-1","limit":4096}}`,
		`{"jsonrpc":"2.0","id":5,"method":"background/board","params":{"stalled_after_ms":500}}`,
		`{"jsonrpc":"2.0","id":6,"method":"background/heartbeat","params":{"id":"task-1","status":"working","transport_alive":false,"observed_at":"` + observedAt.Format(time.RFC3339) + `"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"background/stop","params":"task-1"}`,
		`{"jsonrpc":"2.0","id":8,"method":"background/restart","params":{"id":"task-1"}}`,
		`{"jsonrpc":"2.0","id":9,"method":"background/prune","params":{"older_than_seconds":60,"keep":2}}`,
		`{"jsonrpc":"2.0","id":10,"method":"background/supervise","params":{"now":"` + observedAt.Format(time.RFC3339) + `"}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	err := Serve(context.Background(), strings.NewReader(input), &out, Handlers{
		BackgroundRun: func(_ context.Context, req BackgroundRunRequest) (any, error) {
			require.Equal(t, "printf acp", req.Command)
			require.Equal(t, "terminal", req.Kind)
			require.Equal(t, "session-1", req.SessionID)
			require.NotNil(t, req.RestartPolicy)
			require.True(t, req.RestartPolicy.Enabled)
			require.Equal(t, 1, req.RestartPolicy.MaxAttempts)
			return map[string]any{"kind": "background_run", "id": "task-1"}, nil
		},
		BackgroundList: func(_ context.Context, req BackgroundListRequest) (any, error) {
			require.Equal(t, "session-1", req.SessionID)
			require.Equal(t, "terminal", req.Kind)
			return []map[string]any{{"id": "task-1"}}, nil
		},
		BackgroundGet: func(_ context.Context, req BackgroundIDRequest) (any, error) {
			require.Equal(t, "task-1", req.ID)
			return map[string]any{"kind": "background_get", "id": req.ID}, nil
		},
		BackgroundLogs: func(_ context.Context, req BackgroundLogsRequest) (any, error) {
			require.Equal(t, "task-1", req.ID)
			require.EqualValues(t, 4096, req.Limit)
			return map[string]any{"kind": "background_logs", "id": req.ID, "logs": "acp"}, nil
		},
		BackgroundBoard: func(_ context.Context, req BackgroundBoardRequest) (any, error) {
			require.Equal(t, 500, req.StalledAfterMS)
			return map[string]any{"kind": "background_board"}, nil
		},
		BackgroundHeartbeat: func(_ context.Context, req BackgroundHeartbeatRequest) (any, error) {
			require.Equal(t, "task-1", req.ID)
			require.Equal(t, "working", req.Status)
			require.NotNil(t, req.TransportAlive)
			require.False(t, *req.TransportAlive)
			require.NotNil(t, req.ObservedAt)
			require.Equal(t, observedAt, req.ObservedAt.UTC())
			return map[string]any{"kind": "background_heartbeat", "id": req.ID}, nil
		},
		BackgroundStop: func(_ context.Context, req BackgroundIDRequest) (any, error) {
			require.Equal(t, "task-1", req.ID)
			return map[string]any{"kind": "background_stop", "id": req.ID}, nil
		},
		BackgroundRestart: func(_ context.Context, req BackgroundIDRequest) (any, error) {
			require.Equal(t, "task-1", req.ID)
			return map[string]any{"kind": "background_restart", "id": "task-2"}, nil
		},
		BackgroundPrune: func(_ context.Context, req BackgroundPruneRequest) (any, error) {
			require.Equal(t, 60, req.OlderThanSeconds)
			require.NotNil(t, req.Keep)
			require.Equal(t, keep, *req.Keep)
			return map[string]any{"kind": "background_prune", "removed_count": 1}, nil
		},
		BackgroundSupervise: func(_ context.Context, req BackgroundSuperviseRequest) (any, error) {
			require.NotNil(t, req.Now)
			require.Equal(t, observedAt, req.Now.UTC())
			return map[string]any{"kind": "background_supervise"}, nil
		},
	}, Options{})
	require.NoError(t, err)

	responses := decodeACPResponses(t, out.String())
	require.Len(t, responses, 10)
	require.Equal(t, "background_run", responses[0]["result"].(map[string]any)["kind"])
	require.Len(t, responses[1]["result"].([]any), 1)
	require.Equal(t, "background_get", responses[2]["result"].(map[string]any)["kind"])
	require.Equal(t, "background_logs", responses[3]["result"].(map[string]any)["kind"])
	require.Equal(t, "background_board", responses[4]["result"].(map[string]any)["kind"])
	require.Equal(t, "background_heartbeat", responses[5]["result"].(map[string]any)["kind"])
	require.Equal(t, "background_stop", responses[6]["result"].(map[string]any)["kind"])
	require.Equal(t, "background_restart", responses[7]["result"].(map[string]any)["kind"])
	require.Equal(t, "background_prune", responses[8]["result"].(map[string]any)["kind"])
	require.Equal(t, "background_supervise", responses[9]["result"].(map[string]any)["kind"])
}

func TestServeStreamsBackgroundWatchNotifications(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"background/watch","params":{"id":"task-1","offset":2,"interval_ms":25,"max_events":2}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	err := Serve(context.Background(), strings.NewReader(input), &out, Handlers{
		BackgroundWatch: func(_ context.Context, req BackgroundWatchRequest, emit func(background.WatchEvent) error) (any, error) {
			require.Equal(t, "task-1", req.ID)
			require.EqualValues(t, 2, req.Offset)
			require.Equal(t, 25, req.IntervalMS)
			require.Equal(t, 2, req.MaxEvents)
			require.NoError(t, emit(background.WatchEvent{Type: "status", ID: req.ID, Status: "running"}))
			require.NoError(t, emit(background.WatchEvent{Type: "log", ID: req.ID, Offset: 12, Data: "hello"}))
			return map[string]any{"id": req.ID, "events": 2}, nil
		},
	}, Options{})
	require.NoError(t, err)

	responses := decodeACPResponses(t, out.String())
	require.Len(t, responses, 3)
	require.Equal(t, "background/event", responses[0]["method"])
	firstEvent := responses[0]["params"].(map[string]any)
	require.Equal(t, "status", firstEvent["type"])
	require.Equal(t, "running", firstEvent["status"])
	require.Equal(t, "background/event", responses[1]["method"])
	secondEvent := responses[1]["params"].(map[string]any)
	require.Equal(t, "log", secondEvent["type"])
	require.Equal(t, "hello", secondEvent["data"])
	result := responses[2]["result"].(map[string]any)
	require.Equal(t, "task-1", result["id"])
	require.EqualValues(t, 2, result["events"])
}

func TestServeHandlesAgentRunsRequests(t *testing.T) {
	observedAt := time.Date(2026, 7, 3, 1, 0, 0, 0, time.UTC)
	keep := 1
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"agent-runs/list","params":{"agent":"reviewer","session_id":"session-1"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"agent-runs/get","params":{"id":"run-1"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"agent-runs/logs","params":{"id":"run-1","limit":4096}}`,
		`{"jsonrpc":"2.0","id":4,"method":"agent-runs/board","params":{"agent":"reviewer","session_id":"session-1","stalled_after_ms":500}}`,
		`{"jsonrpc":"2.0","id":5,"method":"agent-runs/heartbeat","params":{"id":"run-1","status":"working","transport_alive":false,"observed_at":"` + observedAt.Format(time.RFC3339) + `"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"agent-runs/stop","params":"run-1"}`,
		`{"jsonrpc":"2.0","id":7,"method":"agent-runs/prune","params":{"older_than_days":2,"keep":1}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	err := Serve(context.Background(), strings.NewReader(input), &out, Handlers{
		AgentRunsList: func(_ context.Context, req AgentRunsListRequest) (any, error) {
			require.Equal(t, "reviewer", req.Agent)
			require.Equal(t, "session-1", req.SessionID)
			return []map[string]any{{"id": "run-1", "kind": "agent_run"}}, nil
		},
		AgentRunsGet: func(_ context.Context, req AgentRunIDRequest) (any, error) {
			require.Equal(t, "run-1", req.ID)
			return map[string]any{"id": req.ID, "kind": "agent_run_status"}, nil
		},
		AgentRunsLogs: func(_ context.Context, req AgentRunLogsRequest) (any, error) {
			require.Equal(t, "run-1", req.ID)
			require.EqualValues(t, 4096, req.Limit)
			return map[string]any{"id": req.ID, "kind": "agent_run_logs"}, nil
		},
		AgentRunsBoard: func(_ context.Context, req AgentRunsBoardRequest) (any, error) {
			require.Equal(t, "reviewer", req.Agent)
			require.Equal(t, "session-1", req.SessionID)
			require.Equal(t, 500, req.StalledAfterMS)
			return map[string]any{"kind": "agent_run_board"}, nil
		},
		AgentRunsHeartbeat: func(_ context.Context, req AgentRunHeartbeatRequest) (any, error) {
			require.Equal(t, "run-1", req.ID)
			require.Equal(t, "working", req.Status)
			require.NotNil(t, req.TransportAlive)
			require.False(t, *req.TransportAlive)
			require.NotNil(t, req.ObservedAt)
			require.Equal(t, observedAt, req.ObservedAt.UTC())
			return map[string]any{"id": req.ID, "kind": "agent_run_heartbeat"}, nil
		},
		AgentRunsStop: func(_ context.Context, req AgentRunIDRequest) (any, error) {
			require.Equal(t, "run-1", req.ID)
			return map[string]any{"id": req.ID, "kind": "agent_run_stop"}, nil
		},
		AgentRunsPrune: func(_ context.Context, req AgentRunsPruneRequest) (any, error) {
			require.Equal(t, 2, req.OlderThanDays)
			require.NotNil(t, req.Keep)
			require.Equal(t, keep, *req.Keep)
			return map[string]any{"kind": "agent_run_prune"}, nil
		},
	}, Options{})
	require.NoError(t, err)

	responses := decodeACPResponses(t, out.String())
	require.Len(t, responses, 7)
	require.Len(t, responses[0]["result"].([]any), 1)
	require.Equal(t, "agent_run_status", responses[1]["result"].(map[string]any)["kind"])
	require.Equal(t, "agent_run_logs", responses[2]["result"].(map[string]any)["kind"])
	require.Equal(t, "agent_run_board", responses[3]["result"].(map[string]any)["kind"])
	require.Equal(t, "agent_run_heartbeat", responses[4]["result"].(map[string]any)["kind"])
	require.Equal(t, "agent_run_stop", responses[5]["result"].(map[string]any)["kind"])
	require.Equal(t, "agent_run_prune", responses[6]["result"].(map[string]any)["kind"])
}

func TestServeHandlesMCPRequests(t *testing.T) {
	inspect := false
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"mcp/list","params":{"inspect":false}}`,
		`{"jsonrpc":"2.0","id":2,"method":"mcp/show","params":"test"}`,
		`{"jsonrpc":"2.0","id":3,"method":"mcp/auth","params":{"name":"test"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"mcp/tools","params":{"server":"test"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"mcp/call","params":{"server":"test","tool":"echo","arguments":{"text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"mcp/resources","params":{"server":"test"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"mcp/resource-templates","params":{"server":"test"}}`,
		`{"jsonrpc":"2.0","id":8,"method":"mcp/read","params":{"server":"test","uri":"codog://note"}}`,
		`{"jsonrpc":"2.0","id":9,"method":"mcp/prompts","params":{"server":"test"}}`,
		`{"jsonrpc":"2.0","id":10,"method":"mcp/prompt","params":{"server":"test","name":"review","arguments":{"topic":"hooks"}}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	err := Serve(context.Background(), strings.NewReader(input), &out, Handlers{
		MCPList: func(_ context.Context, req MCPListRequest) (any, error) {
			require.NotNil(t, req.Inspect)
			require.Equal(t, inspect, *req.Inspect)
			return map[string]any{"kind": "mcp_list"}, nil
		},
		MCPShow: func(_ context.Context, req MCPServerRequest) (any, error) {
			require.Equal(t, "test", req.Server)
			return map[string]any{"kind": "mcp_show"}, nil
		},
		MCPAuth: func(_ context.Context, req MCPServerRequest) (any, error) {
			require.Equal(t, "test", req.Name)
			return map[string]any{"kind": "mcp_auth"}, nil
		},
		MCPTools: func(_ context.Context, req MCPServerRequest) (any, error) {
			require.Equal(t, "test", req.Server)
			return map[string]any{"kind": "mcp_tools"}, nil
		},
		MCPCall: func(_ context.Context, req MCPCallRequest) (any, error) {
			require.Equal(t, "test", req.Server)
			require.Equal(t, "echo", req.Tool)
			require.JSONEq(t, `{"text":"hi"}`, string(req.Arguments))
			return map[string]any{"kind": "mcp_call"}, nil
		},
		MCPResources: func(_ context.Context, req MCPServerRequest) (any, error) {
			require.Equal(t, "test", req.Server)
			return map[string]any{"kind": "mcp_resources"}, nil
		},
		MCPResourceTemplates: func(_ context.Context, req MCPServerRequest) (any, error) {
			require.Equal(t, "test", req.Server)
			return map[string]any{"kind": "mcp_resource_templates"}, nil
		},
		MCPRead: func(_ context.Context, req MCPReadRequest) (any, error) {
			require.Equal(t, "test", req.Server)
			require.Equal(t, "codog://note", req.URI)
			return map[string]any{"kind": "mcp_read"}, nil
		},
		MCPPrompts: func(_ context.Context, req MCPServerRequest) (any, error) {
			require.Equal(t, "test", req.Server)
			return map[string]any{"kind": "mcp_prompts"}, nil
		},
		MCPPrompt: func(_ context.Context, req MCPPromptRequest) (any, error) {
			require.Equal(t, "test", req.Server)
			require.Equal(t, "review", req.Name)
			require.JSONEq(t, `{"topic":"hooks"}`, string(req.Arguments))
			return map[string]any{"kind": "mcp_prompt"}, nil
		},
	}, Options{})
	require.NoError(t, err)

	responses := decodeACPResponses(t, out.String())
	require.Len(t, responses, 10)
	require.Equal(t, "mcp_list", responses[0]["result"].(map[string]any)["kind"])
	require.Equal(t, "mcp_show", responses[1]["result"].(map[string]any)["kind"])
	require.Equal(t, "mcp_auth", responses[2]["result"].(map[string]any)["kind"])
	require.Equal(t, "mcp_tools", responses[3]["result"].(map[string]any)["kind"])
	require.Equal(t, "mcp_call", responses[4]["result"].(map[string]any)["kind"])
	require.Equal(t, "mcp_resources", responses[5]["result"].(map[string]any)["kind"])
	require.Equal(t, "mcp_resource_templates", responses[6]["result"].(map[string]any)["kind"])
	require.Equal(t, "mcp_read", responses[7]["result"].(map[string]any)["kind"])
	require.Equal(t, "mcp_prompts", responses[8]["result"].(map[string]any)["kind"])
	require.Equal(t, "mcp_prompt", responses[9]["result"].(map[string]any)["kind"])
}

func TestServeReportsWorkspaceValidationErrors(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"workspace/files","params":{"limit":"bad"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"workspace/search","params":{"query":"missing handler"}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	err := Serve(context.Background(), strings.NewReader(input), &out, Handlers{}, Options{})
	require.NoError(t, err)

	responses := decodeACPResponses(t, out.String())
	require.Len(t, responses, 2)
	require.EqualValues(t, -32603, responses[0]["error"].(map[string]any)["code"])
	require.Contains(t, responses[0]["error"].(map[string]any)["message"], "workspace files handler")
	require.EqualValues(t, -32603, responses[1]["error"].(map[string]any)["code"])
	require.Contains(t, responses[1]["error"].(map[string]any)["message"], "workspace search handler")
}

func TestServeReportsPromptValidationErrors(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"prompt","params":{"sessionId":"session-1"}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	err := Serve(context.Background(), strings.NewReader(input), &out, Handlers{
		Prompt: func(context.Context, PromptRequest) (PromptResult, error) {
			t.Fatal("prompt handler should not be called")
			return PromptResult{}, nil
		},
	}, Options{})
	require.NoError(t, err)

	responses := decodeACPResponses(t, out.String())
	require.Len(t, responses, 1)
	errPayload := responses[0]["error"].(map[string]any)
	require.EqualValues(t, -32602, errPayload["code"])
	require.Contains(t, errPayload["message"], "prompt is required")
}

func TestServeParsesStructuredAppendMessageAndRewindAliases(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"sessions/append_message","params":{"id":"session-1","message":{"role":"assistant","content":[{"type":"text","text":"structured"}]}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"sessions/rewind","params":{"id":"session-1","count":2}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	err := Serve(context.Background(), strings.NewReader(input), &out, Handlers{
		AppendMessage: func(_ context.Context, req SessionAppendMessageRequest) (SessionMutationResult, error) {
			require.Equal(t, "session-1", req.SessionID)
			require.NotNil(t, req.Message)
			require.Equal(t, "assistant", req.Message.Role)
			require.Equal(t, "structured", req.Message.Content[0].Text)
			return SessionMutationResult{SessionID: req.SessionID}, nil
		},
		RewindSession: func(_ context.Context, req SessionRewindRequest) (SessionRewindResult, error) {
			require.Equal(t, "session-1", req.SessionID)
			require.Equal(t, 2, req.RemoveMessages)
			return SessionRewindResult{}, nil
		},
	}, Options{})
	require.NoError(t, err)

	responses := decodeACPResponses(t, out.String())
	require.Len(t, responses, 2)
	require.Equal(t, "append_message", responses[0]["result"].(map[string]any)["action"])
	require.Equal(t, "rewind", responses[1]["result"].(map[string]any)["action"])
}

func TestServeReportsSessionMutationValidationErrors(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"sessions/append_message","params":{"id":"session-1"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"sessions/append_input","params":{"id":"session-1"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"sessions/rewind","params":{"id":"session-1","remove_messages":0}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	err := Serve(context.Background(), strings.NewReader(input), &out, Handlers{
		AppendMessage: func(context.Context, SessionAppendMessageRequest) (SessionMutationResult, error) {
			t.Fatal("append message handler should not be called")
			return SessionMutationResult{}, nil
		},
		AppendInput: func(context.Context, SessionAppendInputRequest) (SessionMutationResult, error) {
			t.Fatal("append input handler should not be called")
			return SessionMutationResult{}, nil
		},
		RewindSession: func(context.Context, SessionRewindRequest) (SessionRewindResult, error) {
			t.Fatal("rewind handler should not be called")
			return SessionRewindResult{}, nil
		},
	}, Options{})
	require.NoError(t, err)

	responses := decodeACPResponses(t, out.String())
	require.Len(t, responses, 3)
	require.Contains(t, responses[0]["error"].(map[string]any)["message"], "text is required")
	require.Contains(t, responses[1]["error"].(map[string]any)["message"], "input is required")
	require.Contains(t, responses[2]["error"].(map[string]any)["message"], "remove_messages must be positive")
}

func decodeACPResponses(t *testing.T, output string) []map[string]any {
	t.Helper()
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var response map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &response))
		responses = append(responses, response)
	}
	return responses
}
