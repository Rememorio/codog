package anthropic

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/mockanthropic"
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
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(streamed.String(), "hello from mock") {
		t.Fatalf("missing streamed text: %q", streamed.String())
	}
	if len(msg.Blocks) != 1 || !strings.Contains(msg.Blocks[0].Text, "hello from mock") {
		t.Fatalf("unexpected blocks: %#v", msg.Blocks)
	}
	if msg.Usage.InputTokens != 10 {
		t.Fatalf("usage not parsed: %#v", msg.Usage)
	}
}
