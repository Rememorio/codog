package anthropic

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/stretchr/testify/require"
)

func TestClientStreamsText(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "hello from mock"}.Handler())
	defer server.Close()

	client := New(server.URL, "test", "")
	var streamed strings.Builder
	msg, err := client.Stream(context.Background(), Request{
		Model:     "mock",
		MaxTokens: 64,
		Messages:  []Message{TextMessage("user", "hi")},
	}, func(delta string) {
		streamed.WriteString(delta)
	})
	require.NoError(t, err)
	require.Contains(t, streamed.String(), "hello from mock")
	require.Len(t, msg.Blocks, 1)
	require.Contains(t, msg.Blocks[0].Text, "hello from mock")
	require.Equal(t, 10, msg.Usage.InputTokens)
}
