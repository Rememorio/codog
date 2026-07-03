package acpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

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
