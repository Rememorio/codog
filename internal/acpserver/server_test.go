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
		`{"jsonrpc":"2.0","id":5,"method":"shutdown","params":{}}`,
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
	}, Options{Version: "test", Workspace: "/workspace"})
	require.NoError(t, err)

	responses := decodeACPResponses(t, out.String())
	require.Len(t, responses, 5)
	require.Equal(t, "test", responses[0]["result"].(map[string]any)["serverInfo"].(map[string]any)["version"])
	require.Equal(t, "session-1", responses[1]["result"].(map[string]any)["session_id"])
	promptResult := responses[2]["result"].(map[string]any)
	require.Equal(t, "world", promptResult["text"])
	require.Equal(t, "ok", responses[3]["result"].(map[string]any)["status"])
	require.NotNil(t, responses[4]["result"])
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
