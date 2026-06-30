package runloop

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/stretchr/testify/require"
)

type scriptedClient struct {
	responses []anthropic.AssistantMessage
	requests  []anthropic.Request
}

func (c *scriptedClient) Stream(_ context.Context, req anthropic.Request, onText func(string)) (anthropic.AssistantMessage, error) {
	c.requests = append(c.requests, req)
	next := c.responses[0]
	c.responses = c.responses[1:]
	for _, block := range next.Blocks {
		if block.Type == "text" && onText != nil {
			onText(block.Text)
		}
	}
	return next, nil
}

func TestRunnerExecutesToolLoop(t *testing.T) {
	workspace := t.TempDir()
	client := &scriptedClient{
		responses: []anthropic.AssistantMessage{
			{
				Blocks: []anthropic.ContentBlock{{
					Type:  "tool_use",
					ID:    "tool-1",
					Name:  "glob",
					Input: []byte(`{"pattern":"*.txt"}`),
				}},
				Usage: anthropic.Usage{InputTokens: 12, OutputTokens: 3},
			},
			{
				Blocks: []anthropic.ContentBlock{{
					Type: "text",
					Text: "done",
				}},
				Usage: anthropic.Usage{InputTokens: 15, OutputTokens: 2},
			},
		},
	}
	var out strings.Builder
	result, err := Runner{
		Config: config.Config{
			Model:               "mock",
			MaxTokens:           128,
			MaxTurns:            4,
			AutoCompactMessages: 20,
		},
		Client:    client,
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Out:       &out,
	}.Run(context.Background(), nil, "list files")
	require.NoError(t, err)
	require.Equal(t, 2, result.Iterations)
	require.Len(t, result.ToolCalls, 1)
	require.Equal(t, "glob", result.ToolCalls[0].Name)
	require.Contains(t, out.String(), "done")
	require.Len(t, client.requests, 2)
	require.Equal(t, []MessageUsage{
		{MessageIndex: 1, Usage: anthropic.Usage{InputTokens: 12, OutputTokens: 3}},
		{MessageIndex: 3, Usage: anthropic.Usage{InputTokens: 15, OutputTokens: 2}},
	}, result.MessageUsages)
}

func TestRunnerExecutesPromptSubmitAndStopHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	client := &scriptedClient{
		responses: []anthropic.AssistantMessage{{
			Blocks: []anthropic.ContentBlock{{
				Type: "text",
				Text: "done",
			}},
		}},
	}
	result, err := Runner{
		Config: config.Config{
			Model:     "mock",
			MaxTokens: 128,
			MaxTurns:  2,
			Hooks: config.HookConfig{
				UserPromptSubmitCommands: []config.HookCommand{{Command: "cat > prompt.json"}},
				StopCommands:             []config.HookCommand{{Command: "cat > stop.json"}},
			},
		},
		Client:    client,
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
	}.Run(context.Background(), nil, "hello")
	require.NoError(t, err)
	require.Equal(t, 1, result.Iterations)

	promptPayload, err := os.ReadFile(workspace + "/prompt.json")
	require.NoError(t, err)
	require.Contains(t, string(promptPayload), `"event":"user_prompt_submit"`)
	require.Contains(t, string(promptPayload), `"input":"hello"`)

	stopPayload, err := os.ReadFile(workspace + "/stop.json")
	require.NoError(t, err)
	require.Contains(t, string(stopPayload), `"event":"stop"`)
	require.Contains(t, string(stopPayload), `"output":"done"`)
}

func TestCompactMessagesKeepsRecentContext(t *testing.T) {
	messages := []anthropic.Message{
		anthropic.TextMessage("user", "one"),
		anthropic.TextMessage("assistant", "two"),
		anthropic.TextMessage("user", "three"),
	}
	compacted := CompactMessages(messages, 1)
	require.Len(t, compacted, 2)
	require.Equal(t, "three", compacted[1].Content[0].Text)
	require.Contains(t, compacted[0].Content[0].Text, "auto-compacted")
}
