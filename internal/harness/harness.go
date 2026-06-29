package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/tools"
)

type Report struct {
	OK           bool   `json:"ok"`
	Workspace    string `json:"workspace"`
	Output       string `json:"output"`
	Iterations   int    `json:"iterations"`
	MessageCount int    `json:"message_count"`
	ToolCalls    int    `json:"tool_calls"`
}

func Run(ctx context.Context) (Report, error) {
	server := httptest.NewServer(mockanthropic.Server{Turns: []mockanthropic.Turn{
		{ToolUses: []mockanthropic.ToolUse{{
			ID:    "tool-1",
			Name:  "read_file",
			Input: json.RawMessage(`{"path":"README.md"}`),
		}}},
		{Text: "codog harness ok"},
	}}.Handler())
	defer server.Close()

	workspace, err := os.MkdirTemp("", "codog-harness-*")
	if err != nil {
		return Report{}, err
	}
	defer os.RemoveAll(workspace)
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Harness\n"), 0o644); err != nil {
		return Report{}, err
	}

	var out bytes.Buffer
	client := anthropic.New(server.URL, "mock-key", "")
	result, err := runloop.Runner{
		Config: config.Config{
			Model:               "mock",
			MaxTokens:           128,
			MaxTurns:            3,
			AutoCompactMessages: 20,
		},
		Client:    client,
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Out:       &out,
	}.Run(ctx, nil, "say hello")
	if err != nil {
		return Report{}, err
	}
	return Report{
		OK:           true,
		Workspace:    workspace,
		Output:       out.String(),
		Iterations:   result.Iterations,
		MessageCount: len(result.Messages),
		ToolCalls:    len(result.ToolCalls),
	}, nil
}
