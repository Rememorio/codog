package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/stretchr/testify/require"
)

type concurrencyFixtureTool struct {
	name       string
	permission Permission
	output     string
	err        error
}

func (t concurrencyFixtureTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{Name: t.name, InputSchema: map[string]any{"type": "object"}}
}

func (t concurrencyFixtureTool) Permission() Permission { return t.permission }

func (concurrencyFixtureTool) ConcurrencySafe(json.RawMessage) bool { return true }

func (t concurrencyFixtureTool) Execute(context.Context, json.RawMessage) (string, error) {
	return t.output, t.err
}

func TestRegistryConcurrencySafetyRequiresExplicitReadOnlyContract(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	for _, name := range []string{
		"read_file", "grep", "glob", "ls", "notebook_read", "todo_read",
		"git_status", "git_diff", "git_log", "git_show", "git_blame", "web_fetch", "web_search",
	} {
		require.True(t, registry.ConcurrencySafe(name, json.RawMessage(`{}`)), name)
	}
	require.True(t, registry.ConcurrencySafe("Read", json.RawMessage(`{"path":"README.md"}`)))
	require.False(t, registry.ConcurrencySafe("write_file", json.RawMessage(`{}`)))
	require.False(t, registry.ConcurrencySafe("ask_user_question", json.RawMessage(`{}`)))
	require.False(t, registry.ConcurrencySafe("missing", json.RawMessage(`{}`)))

	registry.Register(concurrencyFixtureTool{name: "unsafe_write", permission: PermissionWorkspace})
	require.False(t, registry.ConcurrencySafe("unsafe_write", json.RawMessage(`{}`)))
}

func TestAuthorizedExecutionPreservesFeedbackAndErrors(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	registry.Register(concurrencyFixtureTool{name: "fixture", permission: PermissionPrompt, output: "ok"})
	prompter := &Prompter{
		Mode: PermissionPrompt,
		In:   strings.NewReader(`{"decision":"allow_once","feedback":"use cached data"}` + "\n"),
		Err:  io.Discard,
	}
	execution, err := registry.AuthorizeExecution("fixture", json.RawMessage(`{}`), prompter)
	require.NoError(t, err)
	output, err := execution.Execute(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Contains(t, output, "ok")
	require.Contains(t, output, "use cached data")

	_, err = registry.AuthorizeExecution("missing", nil, nil)
	require.ErrorContains(t, err, "unknown tool")
	_, err = (AuthorizedExecution{}).Execute(context.Background(), nil)
	require.ErrorContains(t, err, "not authorized")

	registry.Register(concurrencyFixtureTool{name: "failure", permission: PermissionReadOnly, err: errors.New("failed")})
	execution, err = registry.AuthorizeExecution("failure", nil, nil)
	require.NoError(t, err)
	_, err = execution.Execute(context.Background(), nil)
	require.ErrorContains(t, err, "failed")
}
