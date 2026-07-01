package mocklimits

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandlerRateLimitsThenStreams(t *testing.T) {
	server := httptest.NewServer(Handler(Options{Failures: 2, RetryAfterMS: 1200, Text: "eventual success"}))
	defer server.Close()

	for i := 0; i < 2; i++ {
		resp, err := http.Post(server.URL+"/v1/messages", "application/json", strings.NewReader(`{}`))
		require.NoError(t, err)
		require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
		require.Equal(t, "2", resp.Header.Get("retry-after"))
		require.NoError(t, resp.Body.Close())
	}

	resp, err := http.Post(server.URL+"/v1/messages", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body bytes.Buffer
	_, err = body.ReadFrom(resp.Body)
	require.NoError(t, err)
	require.Contains(t, body.String(), "eventual")
}

func TestBuildReportAndRenderText(t *testing.T) {
	report := BuildReport("show", Options{Addr: ":9999", Failures: 3, RetryAfterMS: 500, Text: "ok"})
	require.Equal(t, "mock_limits", report.Kind)
	require.Equal(t, "ready", report.Status)
	require.Equal(t, "http://127.0.0.1:9999", report.BaseURL)
	require.Equal(t, 3, report.Failures)

	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "Mock Limits")
	require.Contains(t, out.String(), "127.0.0.1:9999")
}
