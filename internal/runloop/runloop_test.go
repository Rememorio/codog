package runloop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/hooks"
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

type controlledReadTool struct {
	name    string
	safe    bool
	started chan<- string
	release <-chan struct{}
	ready   <-chan struct{}
	fail    string
}

func (t controlledReadTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name: t.name,
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"label": map[string]any{"type": "string"}},
			"required":   []string{"label"},
		},
	}
}

func (controlledReadTool) Permission() tools.Permission { return tools.PermissionReadOnly }

func (t controlledReadTool) ConcurrencySafe(json.RawMessage) bool { return t.safe }

func (t controlledReadTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Label string `json:"label"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	select {
	case t.started <- payload.Label:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if t.ready != nil {
		select {
		case <-t.ready:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if payload.Label == t.fail {
		return "", errors.New("controlled read failed")
	}
	select {
	case <-t.release:
		return payload.Label, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (c errorClient) Stream(context.Context, anthropic.Request, func(string)) (anthropic.AssistantMessage, error) {
	if c.err != nil {
		return anthropic.AssistantMessage{}, c.err
	}
	return anthropic.AssistantMessage{}, errors.New("model failed")
}

func TestRunPreservesAssistantMessageID(t *testing.T) {
	client := &scriptedClient{
		responses: []anthropic.AssistantMessage{{
			ID:     "msg_123",
			Blocks: []anthropic.ContentBlock{{Type: "text", Text: "done"}},
		}},
	}
	runner := Runner{
		Config: config.Config{
			Model:     "mock",
			MaxTokens: 64,
			MaxTurns:  1,
		},
		Client: client,
		Tools:  tools.NewRegistry(t.TempDir()),
	}

	result, err := runner.Run(context.Background(), nil, "hello")

	require.NoError(t, err)
	require.Len(t, result.Messages, 2)
	require.Equal(t, "msg_123", result.Messages[1].ID)
	require.Equal(t, "assistant", result.Messages[1].Role)
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
	events := []string{}
	temperature := 0.3
	result, err := Runner{
		Config: config.Config{
			Model:               "mock",
			MaxTokens:           128,
			Temperature:         &temperature,
			ReasoningEffort:     "high",
			ExtraBody:           map[string]any{"parallel_tool_calls": false},
			MaxTurns:            4,
			AutoCompactMessages: 20,
		},
		Client:    client,
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Out:       &out,
		OnToolStart: func(call ToolCall) {
			events = append(events, "start:"+call.Name)
		},
		OnToolUse: func(call ToolCall) {
			events = append(events, "done:"+call.Name)
		},
	}.Run(context.Background(), nil, "list files")
	require.NoError(t, err)
	require.Equal(t, 2, result.Iterations)
	require.Len(t, result.ToolCalls, 1)
	require.Equal(t, "glob", result.ToolCalls[0].Name)
	require.Equal(t, []string{"start:glob", "done:glob"}, events)
	require.Contains(t, out.String(), "done")
	require.Len(t, client.requests, 2)
	require.Equal(t, []MessageUsage{
		{MessageIndex: 1, Usage: anthropic.Usage{InputTokens: 12, OutputTokens: 3}},
		{MessageIndex: 3, Usage: anthropic.Usage{InputTokens: 15, OutputTokens: 2}},
	}, result.MessageUsages)
	require.NoError(t, ValidateTurnResult(result))
	require.NotNil(t, client.requests[0].Temperature)
	require.InDelta(t, 0.3, *client.requests[0].Temperature, 0.0001)
	require.Equal(t, "high", client.requests[0].ReasoningEffort)
	require.Equal(t, false, client.requests[0].ExtraBody["parallel_tool_calls"])
}

func TestRunnerRefreshesRuntimeToolsBeforeEveryModelRequest(t *testing.T) {
	workspace := t.TempDir()
	client := &scriptedClient{responses: []anthropic.AssistantMessage{
		{Blocks: []anthropic.ContentBlock{{Type: "tool_use", ID: "tool-1", Name: "glob", Input: []byte(`{"pattern":"*.txt"}`)}}},
		{Blocks: []anthropic.ContentBlock{{Type: "text", Text: "done"}}},
	}}
	refreshes := 0
	_, err := Runner{
		Config: config.Config{Model: "mock", MaxTokens: 64, MaxTurns: 2},
		Client: client, Tools: tools.NewRegistry(workspace), Workspace: workspace,
		BeforeRequest: func(context.Context) error {
			refreshes++
			return nil
		},
	}.Run(context.Background(), nil, "list files")
	require.NoError(t, err)
	require.Equal(t, 2, refreshes)
}

func TestRunnerStopsWhenRuntimeToolRefreshFails(t *testing.T) {
	client := &scriptedClient{responses: []anthropic.AssistantMessage{{Blocks: []anthropic.ContentBlock{{Type: "text", Text: "unused"}}}}}
	_, err := Runner{
		Config: config.Config{Model: "mock", MaxTokens: 64, MaxTurns: 1},
		Client: client, Tools: tools.NewRegistry(t.TempDir()),
		BeforeRequest: func(context.Context) error { return errors.New("refresh failed") },
	}.Run(context.Background(), nil, "hello")
	require.EqualError(t, err, "refresh failed")
	require.Empty(t, client.requests)
}

func TestRunnerReturnsReadMediaAsStructuredModelContent(t *testing.T) {
	for _, test := range []struct {
		name      string
		file      string
		data      []byte
		blockType string
		mediaType string
	}{
		{
			name: "image", file: "pixel.png", blockType: "image", mediaType: "image/png",
			data: mustDecodeBase64(t, "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="),
		},
		{name: "pdf", file: "sample.pdf", blockType: "document", mediaType: "application/pdf", data: []byte("%PDF-1.4\n%%EOF\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(workspace, test.file), test.data, 0o644))
			input, err := json.Marshal(map[string]string{"path": test.file})
			require.NoError(t, err)
			client := &scriptedClient{responses: []anthropic.AssistantMessage{
				{Blocks: []anthropic.ContentBlock{{Type: "tool_use", ID: "read-media", Name: "read_file", Input: input}}},
				{Blocks: []anthropic.ContentBlock{{Type: "text", Text: "done"}}},
			}}
			result, err := (Runner{
				Config: config.Config{Model: "mock", MaxTokens: 64, MaxTurns: 2},
				Client: client, Tools: tools.NewRegistry(workspace), Workspace: workspace,
			}).Run(context.Background(), nil, "inspect media")
			require.NoError(t, err)
			require.Len(t, client.requests, 2)
			toolResult := client.requests[1].Messages[2]
			require.Len(t, toolResult.Content, 2)
			require.Equal(t, "tool_result", toolResult.Content[0].Type)
			require.NotContains(t, toolResult.Content[0].Content, base64.StdEncoding.EncodeToString(test.data))
			require.Equal(t, test.blockType, toolResult.Content[1].Type)
			require.Equal(t, test.mediaType, toolResult.Content[1].Source.MediaType)
			require.Equal(t, base64.StdEncoding.EncodeToString(test.data), toolResult.Content[1].Source.Data)
			require.NotContains(t, result.ToolCalls[0].Output, base64.StdEncoding.EncodeToString(test.data))
			require.NoError(t, ValidateTurnResult(result))
		})
	}
}

func mustDecodeBase64(t *testing.T, value string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(value)
	require.NoError(t, err)
	return data
}

func TestRunnerExecutesSafeToolBatchConcurrentlyInTranscriptOrder(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{}, 2)
	registry := tools.NewRegistry(t.TempDir())
	registry.Register(controlledReadTool{name: "controlled_read", safe: true, started: started, release: release})
	client := &scriptedClient{responses: []anthropic.AssistantMessage{
		{Blocks: []anthropic.ContentBlock{
			{Type: "tool_use", ID: "first", Name: "controlled_read", Input: json.RawMessage(`{"label":"first"}`)},
			{Type: "tool_use", ID: "second", Name: "controlled_read", Input: json.RawMessage(`{"label":"second"}`)},
		}},
		{Blocks: []anthropic.ContentBlock{{Type: "text", Text: "done"}}},
	}}
	events := []string{}
	resultCh := make(chan TurnResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := (Runner{
			Config: config.Config{Model: "mock", MaxTokens: 64, MaxTurns: 2},
			Client: client, Tools: registry,
			OnToolStart: func(call ToolCall) { events = append(events, "start:"+call.ID) },
			OnToolUse:   func(call ToolCall) { events = append(events, "done:"+call.ID) },
		}).Run(context.Background(), nil, "read both")
		resultCh <- result
		errCh <- err
	}()

	seen := map[string]bool{}
	for range 2 {
		select {
		case label := <-started:
			seen[label] = true
		case <-time.After(time.Second):
			t.Fatal("safe tool batch did not start concurrently")
		}
	}
	require.Equal(t, map[string]bool{"first": true, "second": true}, seen)
	release <- struct{}{}
	release <- struct{}{}
	require.NoError(t, <-errCh)
	result := <-resultCh
	require.Equal(t, []string{"first", "second"}, []string{result.ToolCalls[0].Output, result.ToolCalls[1].Output})
	require.Equal(t, []string{"start:first", "start:second", "done:first", "done:second"}, events)
	require.Equal(t, "first", result.Messages[2].Content[0].Content)
	require.Equal(t, "second", result.Messages[3].Content[0].Content)
	require.NoError(t, ValidateTurnResult(result))
}

func TestRunnerKeepsUnsafeToolCallsSerial(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{}, 2)
	registry := tools.NewRegistry(t.TempDir())
	registry.Register(controlledReadTool{name: "controlled_serial", safe: false, started: started, release: release})
	client := &scriptedClient{responses: []anthropic.AssistantMessage{
		{Blocks: []anthropic.ContentBlock{
			{Type: "tool_use", ID: "first", Name: "controlled_serial", Input: json.RawMessage(`{"label":"first"}`)},
			{Type: "tool_use", ID: "second", Name: "controlled_serial", Input: json.RawMessage(`{"label":"second"}`)},
		}},
		{Blocks: []anthropic.ContentBlock{{Type: "text", Text: "done"}}},
	}}
	done := make(chan error, 1)
	go func() {
		_, err := (Runner{Config: config.Config{Model: "mock", MaxTokens: 64, MaxTurns: 2}, Client: client, Tools: registry}).Run(context.Background(), nil, "read serially")
		done <- err
	}()
	require.Equal(t, "first", <-started)
	select {
	case label := <-started:
		t.Fatalf("second unsafe tool started before first completed: %s", label)
	case <-time.After(50 * time.Millisecond):
	}
	release <- struct{}{}
	require.Equal(t, "second", <-started)
	release <- struct{}{}
	require.NoError(t, <-done)
}

func TestToolHooksDisableConcurrentBatching(t *testing.T) {
	registry := tools.NewRegistry(t.TempDir())
	execution := turnExecution{
		runner:     Runner{Tools: registry},
		hookRunner: hooks.Runner{Config: config.HookConfig{PreToolUse: []string{"configured"}}},
	}
	require.False(t, execution.toolConcurrencySafe(anthropic.ContentBlock{
		Type: "tool_use", Name: "read_file", Input: json.RawMessage(`{"path":"README.md"}`),
	}))
}

func TestRunnerCancelsSiblingAfterConcurrentToolFailure(t *testing.T) {
	started := make(chan string, 2)
	ready := make(chan struct{})
	release := make(chan struct{})
	registry := tools.NewRegistry(t.TempDir())
	registry.Register(controlledReadTool{
		name: "failing_read", safe: true, started: started, release: release, ready: ready, fail: "first",
	})
	client := &scriptedClient{responses: []anthropic.AssistantMessage{
		{Blocks: []anthropic.ContentBlock{
			{Type: "tool_use", ID: "first", Name: "failing_read", Input: json.RawMessage(`{"label":"first"}`)},
			{Type: "tool_use", ID: "second", Name: "failing_read", Input: json.RawMessage(`{"label":"second"}`)},
		}},
		{Blocks: []anthropic.ContentBlock{{Type: "text", Text: "done"}}},
	}}
	resultCh := make(chan TurnResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := (Runner{Config: config.Config{Model: "mock", MaxTokens: 64, MaxTurns: 2}, Client: client, Tools: registry}).Run(context.Background(), nil, "read")
		resultCh <- result
		errCh <- err
	}()
	<-started
	<-started
	close(ready)
	require.NoError(t, <-errCh)
	result := <-resultCh
	require.Len(t, result.ToolCalls, 2)
	require.True(t, result.ToolCalls[0].IsError)
	require.Contains(t, result.ToolCalls[0].Output, "controlled read failed")
	require.True(t, result.ToolCalls[1].IsError)
	require.Contains(t, result.ToolCalls[1].Output, "context canceled")
}

func TestRunnerStopsWhenMaxBudgetExceeded(t *testing.T) {
	client := &scriptedClient{
		responses: []anthropic.AssistantMessage{{
			ID:     "msg-budget",
			Blocks: []anthropic.ContentBlock{{Type: "text", Text: "done"}},
			Usage:  anthropic.Usage{InputTokens: 10, OutputTokens: 10},
		}},
	}

	result, err := Runner{
		Config: config.Config{
			Model:     "mock",
			MaxTokens: 64,
			MaxTurns:  2,
		},
		Client:       client,
		Tools:        tools.NewRegistry(t.TempDir()),
		MaxBudgetUSD: 0.00001,
	}.Run(context.Background(), nil, "hello")

	require.Error(t, err)
	var budgetErr BudgetExceededError
	require.ErrorAs(t, err, &budgetErr)
	require.Equal(t, 0.00001, budgetErr.LimitUSD)
	require.GreaterOrEqual(t, budgetErr.CostUSD, budgetErr.LimitUSD)
	require.Len(t, result.Messages, 2)
	require.Equal(t, "msg-budget", result.Messages[1].ID)
	require.Equal(t, []MessageUsage{{MessageIndex: 1, Usage: anthropic.Usage{InputTokens: 10, OutputTokens: 10}}}, result.MessageUsages)
}

func TestRunnerPlanModeFiltersToolsAndEnforcesReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	client := &scriptedClient{
		responses: []anthropic.AssistantMessage{
			{
				Blocks: []anthropic.ContentBlock{
					{
						Type:  "tool_use",
						ID:    "tool-1",
						Name:  "bash",
						Input: []byte(`{"command":"printf ok"}`),
					},
					{
						Type:  "tool_use",
						ID:    "tool-2",
						Name:  "bash",
						Input: []byte(`{"command":"touch blocked-by-bash.txt"}`),
					},
					{
						Type:  "tool_use",
						ID:    "tool-3",
						Name:  "write_file",
						Input: []byte(`{"path":"blocked.txt","content":"nope"}`),
					},
				},
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
			PlanMode:  true,
		},
		Client:    client,
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
	}.Run(context.Background(), nil, "plan only")
	require.NoError(t, err)
	require.Len(t, client.requests, 2)
	require.True(t, requestHasTool(client.requests[0], "bash"))
	require.True(t, requestHasTool(client.requests[0], "read_file"))
	require.True(t, requestHasTool(client.requests[0], "tool_search"))
	require.False(t, requestHasTool(client.requests[0], "exit_plan_mode"))
	require.False(t, requestHasTool(client.requests[0], "write_file"))
	require.False(t, requestHasTool(client.requests[0], "edit_file"))
	require.Len(t, result.ToolCalls, 3)
	require.False(t, result.ToolCalls[0].IsError)
	require.Contains(t, result.ToolCalls[0].Output, `"stdout": "ok"`)
	require.True(t, result.ToolCalls[1].IsError)
	require.Contains(t, result.ToolCalls[1].Output, "permission denied")
	require.True(t, result.ToolCalls[2].IsError)
	require.Contains(t, result.ToolCalls[2].Output, "plan mode blocked tool write_file")
	require.NoFileExists(t, filepath.Join(workspace, "blocked-by-bash.txt"))
	require.NoFileExists(t, filepath.Join(workspace, "blocked.txt"))
}

func TestRunnerToolSelectionFiltersDefinitionsAndExecution(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "allowed.txt"), []byte("ok\n"), 0o644))
	client := &scriptedClient{
		responses: []anthropic.AssistantMessage{
			{
				Blocks: []anthropic.ContentBlock{
					{
						Type:  "tool_use",
						ID:    "tool-1",
						Name:  "bash",
						Input: []byte(`{"command":"touch blocked.txt"}`),
					},
					{
						Type:  "tool_use",
						ID:    "tool-2",
						Name:  "Read",
						Input: []byte(`{"file_path":"allowed.txt"}`),
					},
				},
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
			Model:        "mock",
			MaxTokens:    128,
			MaxTurns:     2,
			ToolNames:    []string{"Read"},
			ToolNamesSet: true,
		},
		Client:    client,
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
	}.Run(context.Background(), nil, "read only")
	require.NoError(t, err)
	require.Len(t, client.requests, 2)
	require.True(t, requestHasTool(client.requests[0], "read_file"))
	require.False(t, requestHasTool(client.requests[0], "bash"))
	require.False(t, requestHasTool(client.requests[0], "write_file"))
	require.Len(t, result.ToolCalls, 2)
	require.True(t, result.ToolCalls[0].IsError)
	require.Contains(t, result.ToolCalls[0].Output, "not available because it was not included by --tools")
	require.False(t, result.ToolCalls[1].IsError)
	require.Contains(t, result.ToolCalls[1].Output, "ok")
	require.NoFileExists(t, filepath.Join(workspace, "blocked.txt"))
}

func TestRunnerLoadsDeferredToolDefinitionsAfterSearch(t *testing.T) {
	workspace := t.TempDir()
	client := &scriptedClient{
		responses: []anthropic.AssistantMessage{
			{
				Blocks: []anthropic.ContentBlock{{
					Type:  "tool_use",
					ID:    "tool-search-1",
					Name:  "tool_search",
					Input: []byte(`{"query":"select:WebFetch"}`),
				}},
			},
			{Blocks: []anthropic.ContentBlock{{Type: "text", Text: "done"}}},
		},
	}

	result, err := Runner{
		Config: config.Config{Model: "mock", MaxTokens: 128, MaxTurns: 2},
		Client: client,
		Tools:  tools.NewRegistry(workspace),
	}.Run(context.Background(), nil, "fetch a page")

	require.NoError(t, err)
	require.Len(t, result.ToolCalls, 1)
	require.False(t, result.ToolCalls[0].IsError)
	require.Len(t, client.requests, 2)
	require.True(t, requestHasTool(client.requests[0], "tool_search"))
	require.True(t, requestHasTool(client.requests[0], "read_file"))
	require.False(t, requestHasTool(client.requests[0], "web_fetch"))
	require.False(t, requestHasTool(client.requests[0], "brief"))
	require.True(t, requestHasTool(client.requests[1], "web_fetch"))
	require.False(t, requestHasTool(client.requests[1], "brief"))
}

func TestRunnerRestoresDeferredToolsFromSessionMessages(t *testing.T) {
	client := &scriptedClient{responses: []anthropic.AssistantMessage{{
		Blocks: []anthropic.ContentBlock{{Type: "text", Text: "done"}},
	}}}
	previous := []anthropic.Message{
		{
			Role: "assistant",
			Content: []anthropic.ContentBlock{{
				Type: "tool_use",
				ID:   "tool-search-previous",
				Name: "ToolSearch",
			}},
		},
		anthropic.ToolResultMessage("tool-search-previous", `{"match_names":["web_fetch"]}`, false),
	}

	_, err := Runner{
		Config: config.Config{Model: "mock", MaxTokens: 128, MaxTurns: 1},
		Client: client,
		Tools:  tools.NewRegistry(t.TempDir()),
	}.Run(context.Background(), previous, "fetch another page")

	require.NoError(t, err)
	require.Len(t, client.requests, 1)
	require.True(t, requestHasTool(client.requests[0], "web_fetch"))
	require.False(t, requestHasTool(client.requests[0], "brief"))
}

func TestRunnerExplicitToolSelectionCanEnableProductOutputTool(t *testing.T) {
	client := &scriptedClient{responses: []anthropic.AssistantMessage{{
		Blocks: []anthropic.ContentBlock{{Type: "text", Text: "done"}},
	}}}

	_, err := Runner{
		Config: config.Config{
			Model:        "mock",
			MaxTokens:    128,
			MaxTurns:     1,
			ToolNames:    []string{"Brief"},
			ToolNamesSet: true,
		},
		Client: client,
		Tools:  tools.NewRegistry(t.TempDir()),
	}.Run(context.Background(), nil, "brief mode")

	require.NoError(t, err)
	require.Len(t, client.requests, 1)
	require.Len(t, client.requests[0].Tools, 1)
	require.True(t, requestHasTool(client.requests[0], "brief"))
}

func TestRunnerAppliesPreToolUseHookDecisionAndUpdatedInput(t *testing.T) {
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
					Input: []byte(`{"path":"original.txt","content":"no"}`),
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
				PreToolUseCommands: []config.HookCommand{{
					Matcher: "write_file",
					Command: `printf '%s' '{"systemMessage":"updated","hookSpecificOutput":{"permissionDecision":"allow","permissionDecisionReason":"hook ok","updatedInput":{"path":"hooked.txt","content":"ok"}}}'`,
				}},
			},
		},
		Client:    client,
		Tools:     tools.NewRegistry(workspace),
		Prompter:  &tools.Prompter{Mode: tools.PermissionReadOnly, Workspace: workspace},
		Workspace: workspace,
	}.Run(context.Background(), nil, "write")
	require.NoError(t, err)
	require.Len(t, result.ToolCalls, 1)
	require.False(t, result.ToolCalls[0].IsError)
	require.JSONEq(t, `{"path":"hooked.txt","content":"ok"}`, result.ToolCalls[0].Input)
	require.Contains(t, result.ToolCalls[0].Output, "Hook feedback:\nupdated")
	require.FileExists(t, filepath.Join(workspace, "hooked.txt"))
	require.NoFileExists(t, filepath.Join(workspace, "original.txt"))
	data, err := os.ReadFile(filepath.Join(workspace, "hooked.txt"))
	require.NoError(t, err)
	require.Equal(t, "ok", string(data))
}

func TestRunnerMergesPostToolUseHookFeedback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hello\n"), 0o644))
	client := &scriptedClient{
		responses: []anthropic.AssistantMessage{
			{
				Blocks: []anthropic.ContentBlock{{
					Type:  "tool_use",
					ID:    "tool-1",
					Name:  "read_file",
					Input: []byte(`{"path":"README.md"}`),
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
				PostToolUseCommands: []config.HookCommand{{
					Matcher: "read_file",
					Command: `printf '%s' '{"systemMessage":"post note","hookSpecificOutput":{"additionalContext":"ctx"}}'`,
				}},
			},
		},
		Client:    client,
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
	}.Run(context.Background(), nil, "read")
	require.NoError(t, err)
	require.Len(t, result.ToolCalls, 1)
	require.False(t, result.ToolCalls[0].IsError)
	require.Contains(t, result.ToolCalls[0].Output, "hello")
	require.Contains(t, result.ToolCalls[0].Output, "Hook feedback:\npost note\nctx")
	require.Len(t, client.requests, 2)
	require.Len(t, client.requests[1].Messages, 3)
	require.Contains(t, client.requests[1].Messages[2].Content[0].Content, "Hook feedback:\npost note\nctx")
}

func TestRunnerReturnsPreToolUseMalformedJSONDiagnostic(t *testing.T) {
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
					Name:  "bash",
					Input: []byte(`{"command":"printf blocked","timeout":1000}`),
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
				PreToolUseCommands: []config.HookCommand{{
					Matcher: "bash",
					Command: `printf '{not-json'; printf 'stderr warning' >&2; exit 1`,
				}},
			},
		},
		Client:    client,
		Tools:     tools.NewRegistry(workspace),
		Prompter:  &tools.Prompter{Mode: tools.PermissionAllow, Workspace: workspace},
		Workspace: workspace,
	}.Run(context.Background(), nil, "run")
	require.NoError(t, err)
	require.Len(t, result.ToolCalls, 1)
	require.True(t, result.ToolCalls[0].IsError)
	require.Contains(t, result.ToolCalls[0].Output, "hook_invalid_json:")
	require.Contains(t, result.ToolCalls[0].Output, "phase=PreToolUse")
	require.Contains(t, result.ToolCalls[0].Output, "tool=bash")
	require.Contains(t, result.ToolCalls[0].Output, "stderr_preview=stderr warning")
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
				UserPromptSubmitCommands: []config.HookCommand{{
					Command: `cat > prompt.json; printf '%s' '{"systemMessage":"prompt note","hookSpecificOutput":{"additionalContext":"prompt context"}}'`,
				}},
				StopCommands: []config.HookCommand{{
					Command: `cat > stop.json; printf '%s' '{"systemMessage":"stop note","hookSpecificOutput":{"additionalContext":"stop context"}}'`,
				}},
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
	require.Len(t, client.requests, 1)
	require.Len(t, client.requests[0].Messages, 2)
	require.Equal(t, "hello", client.requests[0].Messages[0].Content[0].Text)
	promptFeedback := client.requests[0].Messages[1].Content[0].Text
	require.Contains(t, promptFeedback, "UserPromptSubmit hook feedback:")
	require.Contains(t, promptFeedback, "prompt note")
	require.Contains(t, promptFeedback, "prompt context")

	stopPayload, err := os.ReadFile(workspace + "/stop.json")
	require.NoError(t, err)
	require.Contains(t, string(stopPayload), `"event":"stop"`)
	require.Contains(t, string(stopPayload), `"output":"done"`)
	require.Equal(t, []string{"stop note", "stop context"}, result.StopHookFeedback)
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
				PreCompactCommands: []config.HookCommand{{
					Command: `cat > compact.json; printf '%s' '{"systemMessage":"pre compact note","hookSpecificOutput":{"additionalContext":"pre compact context"}}'`,
				}},
				PostCompactCommands: []config.HookCommand{{
					Command: `cat > post-compact.json; printf '%s' '{"systemMessage":"post compact note","hookSpecificOutput":{"additionalContext":"post compact context"}}'`,
				}},
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
	require.Len(t, client.requests, 1)
	summary := client.requests[0].Messages[0].Content[0].Text
	require.Contains(t, summary, "auto-compacted")
	require.Contains(t, summary, "Compaction hook feedback:")
	require.Contains(t, summary, "pre compact note")
	require.Contains(t, summary, "pre compact context")
	require.Contains(t, summary, "post compact note")
	require.Contains(t, summary, "post compact context")
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
				FileChangedCommands: []config.HookCommand{{Matcher: "write_file", Command: "cat > file-changed.json; printf '%s' '{\"systemMessage\":\"file changed note\"}'"}},
			},
		},
		Client:    client,
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
	}.Run(context.Background(), nil, "write notes")
	require.NoError(t, err)
	require.Len(t, result.ToolCalls, 1)
	require.False(t, result.ToolCalls[0].IsError)
	require.Contains(t, result.ToolCalls[0].Output, "Hook feedback:\nfile changed note")
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

func TestRunnerAppliesHookEnvironmentToLaterBashTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	client := &scriptedClient{
		responses: []anthropic.AssistantMessage{
			{
				Blocks: []anthropic.ContentBlock{
					{
						Type:  "tool_use",
						ID:    "tool-1",
						Name:  "write_file",
						Input: []byte(`{"path":"notes.txt","content":"hello"}`),
					},
					{
						Type:  "tool_use",
						ID:    "tool-2",
						Name:  "bash",
						Input: []byte(`{"command":"printf %s \"$CODOG_TEST_HOOK_ENV\""}`),
					},
				},
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
			ConfigHome: configHome,
			Model:      "mock",
			MaxTokens:  128,
			MaxTurns:   2,
			Hooks: config.HookConfig{
				FileChangedCommands: []config.HookCommand{{Matcher: "write_file", Command: "printf 'export CODOG_TEST_HOOK_ENV=from-hook\\n' > \"$CLAUDE_ENV_FILE\""}},
			},
		},
		Client:    client,
		Tools:     tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome}),
		Workspace: workspace,
		SessionID: "session-1",
	}.Run(context.Background(), nil, "write then inspect env")
	require.NoError(t, err)
	require.Len(t, result.ToolCalls, 2)
	require.False(t, result.ToolCalls[1].IsError)
	require.Contains(t, result.ToolCalls[1].Output, `"stdout": "from-hook"`)
}

func TestRunnerExecutesCwdChangedHookAfterBashCWDChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	subdir := workspace + "/sub"
	physicalWorkspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	physicalSubdir := filepath.Join(physicalWorkspace, "sub")
	client := &scriptedClient{
		responses: []anthropic.AssistantMessage{
			{
				Blocks: []anthropic.ContentBlock{
					{
						Type:  "tool_use",
						ID:    "tool-1",
						Name:  "bash",
						Input: []byte(`{"command":"mkdir sub && cd sub"}`),
					},
					{
						Type:  "tool_use",
						ID:    "tool-2",
						Name:  "bash",
						Input: []byte(`{"command":"printf %s \"$PWD\""}`),
					},
				},
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
			ConfigHome: configHome,
			Model:      "mock",
			MaxTokens:  128,
			MaxTurns:   2,
			Hooks: config.HookConfig{
				CwdChangedCommands: []config.HookCommand{{Matcher: physicalSubdir, Command: "cat > cwd-changed.json; printf '%s' '{\"systemMessage\":\"cwd changed note\"}'"}},
			},
		},
		Client:    client,
		Tools:     tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome}),
		Workspace: workspace,
		SessionID: "session-1",
	}.Run(context.Background(), nil, "change cwd")
	require.NoError(t, err)
	require.Len(t, result.ToolCalls, 2)
	require.False(t, result.ToolCalls[0].IsError)
	require.False(t, result.ToolCalls[1].IsError)
	require.Contains(t, result.ToolCalls[0].Output, "Hook feedback:\ncwd changed note")
	require.Contains(t, result.ToolCalls[1].Output, subdir)

	payload, err := os.ReadFile(workspace + "/cwd-changed.json")
	require.NoError(t, err)
	var hookPayload struct {
		Event  string `json:"event"`
		Tool   string `json:"tool"`
		OldCWD string `json:"old_cwd"`
		NewCWD string `json:"new_cwd"`
	}
	require.NoError(t, json.Unmarshal(payload, &hookPayload))
	require.Equal(t, "cwd_changed", hookPayload.Event)
	require.Equal(t, physicalSubdir, hookPayload.Tool)
	require.Equal(t, physicalWorkspace, hookPayload.OldCWD)
	require.Equal(t, physicalSubdir, hookPayload.NewCWD)
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
				PostToolUseCommands:        []config.HookCommand{{Command: "cat > post.json"}},
				PostToolUseFailureCommands: []config.HookCommand{{Command: "cat > failure.json && printf '%s' '{\"systemMessage\":\"failure note\"}'"}},
			},
		},
		Client:    client,
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
	}.Run(context.Background(), nil, "run missing tool")
	require.NoError(t, err)
	require.Len(t, result.ToolCalls, 1)
	require.True(t, result.ToolCalls[0].IsError)
	require.Contains(t, result.ToolCalls[0].Output, "Hook feedback (error):\nfailure note")
	require.NoFileExists(t, filepath.Join(workspace, "post.json"))

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
		anthropic.TextMessage("user", "inspect flaky tests"),
		{
			Role: "assistant",
			Content: []anthropic.ContentBlock{{
				Type: "tool_use",
				ID:   "tool-1",
				Name: "bash",
			}},
		},
		anthropic.ToolResultMessage("tool-1", "failed", true),
		anthropic.TextMessage("assistant", "runloop test failed"),
		anthropic.TextMessage("user", "keep this"),
	}
	compacted := CompactMessages(messages, 1)
	require.Len(t, compacted, 2)
	require.Equal(t, "keep this", compacted[1].Content[0].Text)
	summary := compacted[0].Content[0].Text
	require.Contains(t, summary, "auto-compacted")
	require.Contains(t, summary, "inspect flaky tests")
	require.Contains(t, summary, "Tools mentioned: bash")
	require.Contains(t, summary, "Tool results: 1 result message(s), 1 error result(s).")
}

func TestValidateTranscriptAcceptsToolUseResultPairs(t *testing.T) {
	messages := []anthropic.Message{
		anthropic.TextMessage("user", "list files"),
		{
			Role: "assistant",
			Content: []anthropic.ContentBlock{{
				Type:  "tool_use",
				ID:    "tool-1",
				Name:  "glob",
				Input: []byte(`{"pattern":"*.go"}`),
			}},
		},
		anthropic.ToolResultMessage("tool-1", "runloop.go", false),
		anthropic.TextMessage("assistant", "done"),
	}

	report := ValidateTranscript(messages)

	require.True(t, report.Valid)
	require.Equal(t, 1, report.ToolUseCount)
	require.Equal(t, 1, report.ToolResultCount)
	require.Empty(t, report.Issues)
}

func TestValidateTranscriptAllowsOnlyRichSupplementalToolContent(t *testing.T) {
	toolUse := anthropic.Message{Role: "assistant", Content: []anthropic.ContentBlock{{Type: "tool_use", ID: "read", Name: "read_file"}}}
	richResult := anthropic.ToolResultMessageWithSupplemental("read", "image metadata", false, []anthropic.ContentBlock{{
		Type: "image", Source: &anthropic.ContentSource{Type: "base64", MediaType: "image/png", Data: "aW1n"},
	}})
	require.True(t, ValidateTranscript([]anthropic.Message{toolUse, richResult}).Valid)

	invalidResult := richResult
	invalidResult.Content = append(invalidResult.Content, anthropic.ContentBlock{Type: "text", Text: "interleaved"})
	report := ValidateTranscript([]anthropic.Message{toolUse, invalidResult})
	require.False(t, report.Valid)
	requireIssueCodes(t, report, "interleaved_user_content")
}

func TestValidateTranscriptRejectsBrokenToolPairing(t *testing.T) {
	messages := []anthropic.Message{
		{
			Role: "assistant",
			Content: []anthropic.ContentBlock{{
				Type: "tool_use",
				ID:   "dup",
				Name: "glob",
			}},
		},
		anthropic.TextMessage("assistant", "continued too early"),
		{
			Role: "assistant",
			Content: []anthropic.ContentBlock{{
				Type: "tool_use",
				ID:   "dup",
				Name: "grep",
			}},
		},
		anthropic.ToolResultMessage("missing", "orphan", true),
	}

	report := ValidateTranscript(messages)

	require.False(t, report.Valid)
	requireIssueCodes(t, report,
		"pending_tool_results",
		"duplicate_tool_use_id",
		"orphan_tool_result",
		"unexpected_tool_result",
		"missing_tool_result",
	)
	err := ValidateTurnResult(TurnResult{Messages: messages})
	require.Error(t, err)
	var contractErr TranscriptContractError
	require.ErrorAs(t, err, &contractErr)
}

func requireIssueCodes(t *testing.T, report TranscriptReport, codes ...string) {
	t.Helper()
	seen := make(map[string]bool, len(report.Issues))
	for _, issue := range report.Issues {
		seen[issue.Code] = true
	}
	for _, code := range codes {
		require.Truef(t, seen[code], "missing issue code %s in %#v", code, report.Issues)
	}
}

func requestHasTool(req anthropic.Request, name string) bool {
	for _, tool := range req.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
