package runloop

import (
	"context"
	"encoding/json"
	"errors"
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

type errorClient struct {
	err error
}

func (c errorClient) Stream(context.Context, anthropic.Request, func(string)) (anthropic.AssistantMessage, error) {
	if c.err != nil {
		return anthropic.AssistantMessage{}, c.err
	}
	return anthropic.AssistantMessage{}, errors.New("model failed")
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

func TestRunnerExecutesPreCompactHook(t *testing.T) {
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
	previous := []anthropic.Message{
		anthropic.TextMessage("user", "one"),
		anthropic.TextMessage("assistant", "two"),
		anthropic.TextMessage("user", "three"),
	}
	_, err := Runner{
		Config: config.Config{
			Model:               "mock",
			MaxTokens:           128,
			MaxTurns:            2,
			AutoCompactMessages: 1,
			Hooks: config.HookConfig{
				PreCompactCommands:  []config.HookCommand{{Command: "cat > compact.json"}},
				PostCompactCommands: []config.HookCommand{{Command: "cat > post-compact.json"}},
			},
		},
		Client:    client,
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
	}.Run(context.Background(), previous, "four")
	require.NoError(t, err)

	payload, err := os.ReadFile(workspace + "/compact.json")
	require.NoError(t, err)
	var hookPayload struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(payload, &hookPayload))
	require.Equal(t, "pre_compact", hookPayload.Event)
	require.Contains(t, hookPayload.Input, `"messages":4`)
	require.Contains(t, hookPayload.Input, `"keep":1`)
	postPayload, err := os.ReadFile(workspace + "/post-compact.json")
	require.NoError(t, err)
	var postHookPayload struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(postPayload, &postHookPayload))
	require.Equal(t, "post_compact", postHookPayload.Event)
	require.Contains(t, postHookPayload.Input, `"messages":4`)
	require.Contains(t, postHookPayload.Input, `"keep":1`)
}

func TestRunnerExecutesFileChangedHookAfterWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	client := &scriptedClient{
		responses: []anthropic.AssistantMessage{
			{
				Blocks: []anthropic.ContentBlock{{
					Type:  "tool_use",
					ID:    "tool-1",
					Name:  "write_file",
					Input: []byte(`{"path":"notes.txt","content":"hello"}`),
				}},
			},
			{
				Blocks: []anthropic.ContentBlock{{
					Type: "text",
					Text: "done",
				}},
			},
		},
	}
	result, err := Runner{
		Config: config.Config{
			Model:     "mock",
			MaxTokens: 128,
			MaxTurns:  2,
			Hooks: config.HookConfig{
				FileChangedCommands: []config.HookCommand{{Matcher: "write_file", Command: "cat > file-changed.json"}},
			},
		},
		Client:    client,
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
	}.Run(context.Background(), nil, "write notes")
	require.NoError(t, err)
	require.Len(t, result.ToolCalls, 1)
	require.False(t, result.ToolCalls[0].IsError)
	require.FileExists(t, workspace+"/notes.txt")

	payload, err := os.ReadFile(workspace + "/file-changed.json")
	require.NoError(t, err)
	var hookPayload struct {
		Event     string `json:"event"`
		Tool      string `json:"tool"`
		ToolName  string `json:"tool_name"`
		Input     string `json:"input"`
		FilePath  string `json:"file_path"`
		Operation string `json:"operation"`
	}
	require.NoError(t, json.Unmarshal(payload, &hookPayload))
	require.Equal(t, "file_changed", hookPayload.Event)
	require.Equal(t, "write_file", hookPayload.Tool)
	require.Equal(t, "write_file", hookPayload.ToolName)
	require.Equal(t, "notes.txt", hookPayload.FilePath)
	require.Equal(t, "write_file", hookPayload.Operation)
	require.Contains(t, hookPayload.Input, `"path":"notes.txt"`)
}

func TestRunnerExecutesPostToolUseFailureHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	client := &scriptedClient{
		responses: []anthropic.AssistantMessage{
			{
				Blocks: []anthropic.ContentBlock{{
					Type:  "tool_use",
					ID:    "tool-1",
					Name:  "missing_tool",
					Input: []byte(`{"value":true}`),
				}},
			},
			{
				Blocks: []anthropic.ContentBlock{{
					Type: "text",
					Text: "done",
				}},
			},
		},
	}
	result, err := Runner{
		Config: config.Config{
			Model:     "mock",
			MaxTokens: 128,
			MaxTurns:  2,
			Hooks: config.HookConfig{
				PostToolUseFailureCommands: []config.HookCommand{{Command: "cat > failure.json"}},
			},
		},
		Client:    client,
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
	}.Run(context.Background(), nil, "run missing tool")
	require.NoError(t, err)
	require.Len(t, result.ToolCalls, 1)
	require.True(t, result.ToolCalls[0].IsError)

	payload, err := os.ReadFile(workspace + "/failure.json")
	require.NoError(t, err)
	var hookPayload struct {
		Event   string `json:"event"`
		Tool    string `json:"tool"`
		IsError bool   `json:"is_error"`
	}
	require.NoError(t, json.Unmarshal(payload, &hookPayload))
	require.Equal(t, "post_tool_use_failure", hookPayload.Event)
	require.Equal(t, "missing_tool", hookPayload.Tool)
	require.True(t, hookPayload.IsError)
}

func TestRunnerExecutesStopFailureHookOnModelError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	_, err := Runner{
		Config: config.Config{
			Model:     "mock",
			MaxTokens: 128,
			MaxTurns:  1,
			Hooks: config.HookConfig{
				StopFailureCommands: []config.HookCommand{{Command: "cat > stop-failure.json"}},
			},
		},
		Client:    errorClient{err: errors.New("rate limited")},
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
	}.Run(context.Background(), nil, "hello")
	require.Error(t, err)
	require.Contains(t, err.Error(), "rate limited")

	payload, err := os.ReadFile(workspace + "/stop-failure.json")
	require.NoError(t, err)
	var hookPayload struct {
		Event   string `json:"event"`
		Output  string `json:"output"`
		IsError bool   `json:"is_error"`
		Reason  string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(payload, &hookPayload))
	require.Equal(t, "stop_failure", hookPayload.Event)
	require.Equal(t, "rate limited", hookPayload.Output)
	require.True(t, hookPayload.IsError)
	require.Equal(t, "model_error", hookPayload.Reason)
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
