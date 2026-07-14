package mockanthropic

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandlerStreamsTextAndToolUse(t *testing.T) {
	var request json.RawMessage
	server := httptest.NewServer(Server{
		Turns: []Turn{{
			Text: "hello world",
			ToolUses: []ToolUse{{
				ID:          "toolu_1",
				Name:        "read_file",
				InputDeltas: []string{`{"path":`, `"README.md"}`},
			}},
		}},
		OnRequest: func(data json.RawMessage) {
			request = data
		},
	}.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/messages", "application/json", strings.NewReader(`{"messages":[]}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("content-type"))
	require.JSONEq(t, `{"messages":[]}`, string(request))

	stream := string(body)
	require.Contains(t, stream, "event: message_start")
	require.Contains(t, stream, `"text":"hello ","type":"text_delta"`)
	require.Contains(t, stream, `"id":"toolu_1","input":null,"name":"read_file","type":"tool_use"`)
	require.Contains(t, stream, `"partial_json":"{\"path\":","type":"input_json_delta"`)
	require.Contains(t, stream, "event: message_stop")
}

func TestHandlerRateLimitsBeforeStreaming(t *testing.T) {
	server := httptest.NewServer(Server{
		RateLimitFailures: 1,
		RetryAfter:        "2",
		Text:              "ok",
	}.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/messages", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.Equal(t, "2", resp.Header.Get("retry-after"))
	_ = resp.Body.Close()

	resp, err = http.Post(server.URL+"/v1/messages", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `"text":"ok ","type":"text_delta"`)
}

func TestHandlerRejectsNonPost(t *testing.T) {
	server := httptest.NewServer(Server{}.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1/messages")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}
