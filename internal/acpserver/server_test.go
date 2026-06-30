package acpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServeHandlesACPRequests(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"prompt","params":{"session_id":"session-1","prompt":"hello"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"status","params":{}}`,
		`{"jsonrpc":"2.0","id":5,"method":"session/list","params":{}}`,
		`{"jsonrpc":"2.0","id":6,"method":"session/get","params":{"sessionId":"session-1"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"session/history","params":{"session_id":"session-1","limit":1}}`,
		`{"jsonrpc":"2.0","id":8,"method":"session/rename","params":{"session_id":"session-1","newSessionId":"session-2"}}`,
		`{"jsonrpc":"2.0","id":9,"method":"session/delete","params":{"session_id":"session-2"}}`,
		`{"jsonrpc":"2.0","id":10,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	err := Serve(context.Background(), strings.NewReader(input), &out, Handlers{
		NewSession: func(context.Context) (SessionInfo, error) {
			return SessionInfo{SessionID: "session-1"}, nil
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
		RenameSession: func(_ context.Context, req SessionRenameRequest) (SessionMutationResult, error) {
			require.Equal(t, "session-1", req.SessionID)
			require.Equal(t, "session-2", req.NewSessionID)
			return SessionMutationResult{SessionID: req.SessionID, NewSessionID: req.NewSessionID}, nil
		},
		DeleteSession: func(_ context.Context, req SessionLookupRequest) (SessionMutationResult, error) {
			require.Equal(t, "session-2", req.SessionID)
			return SessionMutationResult{SessionID: req.SessionID}, nil
		},
	}, Options{Version: "test", Workspace: "/workspace"})
	require.NoError(t, err)

	responses := decodeACPResponses(t, out.String())
	require.Len(t, responses, 10)
	require.Equal(t, "test", responses[0]["result"].(map[string]any)["serverInfo"].(map[string]any)["version"])
	capabilities := responses[0]["result"].(map[string]any)["capabilities"].(map[string]any)
	require.Equal(t, true, capabilities["prompt"])
	sessionCaps := capabilities["sessions"].(map[string]any)
	require.Equal(t, true, sessionCaps["history"])
	require.Equal(t, true, sessionCaps["rename"])
	require.Equal(t, true, sessionCaps["delete"])
	require.Equal(t, "session-1", responses[1]["result"].(map[string]any)["session_id"])
	promptResult := responses[2]["result"].(map[string]any)
	require.Equal(t, "world", promptResult["text"])
	require.Equal(t, "ok", responses[3]["result"].(map[string]any)["status"])
	listResult := responses[4]["result"].(map[string]any)
	require.Equal(t, "session_list", listResult["kind"])
	require.EqualValues(t, 1, listResult["count"])
	getResult := responses[5]["result"].(map[string]any)
	require.Equal(t, "session-1", getResult["session_id"])
	require.EqualValues(t, 2, getResult["message_count"])
	historyResult := responses[6]["result"].(map[string]any)
	require.Equal(t, "session_history", historyResult["kind"])
	require.Equal(t, "session-1", historyResult["session_id"])
	renameResult := responses[7]["result"].(map[string]any)
	require.Equal(t, "session_mutation", renameResult["kind"])
	require.Equal(t, "rename", renameResult["action"])
	require.Equal(t, "session-2", renameResult["new_session_id"])
	deleteResult := responses[8]["result"].(map[string]any)
	require.Equal(t, "session_mutation", deleteResult["kind"])
	require.Equal(t, "delete", deleteResult["action"])
	require.Equal(t, "session-2", deleteResult["session_id"])
	require.NotNil(t, responses[9]["result"])
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
