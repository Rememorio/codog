package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/anttrace"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/contextview"
	"github.com/Rememorio/codog/internal/focus"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/mocklimits"
	"github.com/Rememorio/codog/internal/onboarding"
	"github.com/Rememorio/codog/internal/outputstyle"
	"github.com/Rememorio/codog/internal/pathscope"
	"github.com/Rememorio/codog/internal/perfissue"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/todos"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/tui"
	"github.com/Rememorio/codog/internal/undo"
	"github.com/stretchr/testify/require"
)

func TestMemoryCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Memory first line\nsecret body"), 0o644))
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Memory(nil))
	require.Contains(t, out.String(), "Memory")
	require.Contains(t, out.String(), "Instruction files 1")
	require.Contains(t, out.String(), "preview=Memory first line")
	require.Contains(t, out.String(), "words=5")
	require.Contains(t, out.String(), "bytes=29")
	require.NotContains(t, out.String(), "secret body")
	out.Reset()

	require.NoError(t, app.Memory([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "memory"`)
	require.Contains(t, out.String(), `"instruction_files": 1`)
	require.Contains(t, out.String(), `"preview": "Memory first line"`)
	require.Contains(t, out.String(), `"words": 5`)
	require.Contains(t, out.String(), `"size_bytes": 29`)
	require.Contains(t, out.String(), `"modified_at":`)
	require.NotContains(t, out.String(), "secret body")
	out.Reset()

	require.NoError(t, app.Memory([]string{"select", "--json"}))
	require.Contains(t, out.String(), `"action": "select"`)
	require.Contains(t, out.String(), `"selected":`)
	require.Contains(t, out.String(), `"option_count": 1`)
	require.Contains(t, out.String(), `"exists": true`)
	out.Reset()

	require.NoError(t, app.Memory([]string{"select", "NEW.md", "--json"}))
	require.Contains(t, out.String(), `"target": "NEW.md"`)
	require.Contains(t, out.String(), `"option_count": 2`)
	require.Contains(t, out.String(), `"exists": false`)
	require.NoFileExists(t, filepath.Join(workspace, "NEW.md"))
	out.Reset()

	require.NoError(t, app.Memory([]string{"show", "AGENTS.md"}))
	require.Contains(t, out.String(), "Memory File")
	require.Contains(t, out.String(), "secret body")
	out.Reset()

	err := app.Memory([]string{"add", "--json"})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var addError actionErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &addError))
	require.Equal(t, "memory", addError.Kind)
	require.Equal(t, "add", addError.Action)
	require.Equal(t, "missing_argument", addError.ErrorKind)
	require.Equal(t, "text", addError.Argument)
	require.Equal(t, "usage", addError.Error.Kind)
	require.Equal(t, "parse_args", addError.Error.Operation)
	require.Equal(t, "text", addError.Error.Target)
	out.Reset()

	require.NoError(t, app.Memory([]string{"add", "Use", "focused", "tests."}))
	require.Contains(t, out.String(), "Memory Updated")
	data, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), "Use focused tests.")
	out.Reset()

	err = app.Memory([]string{"search", "--json"})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var searchError actionErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &searchError))
	require.Equal(t, "memory", searchError.Kind)
	require.Equal(t, "search", searchError.Action)
	require.Equal(t, "missing_argument", searchError.ErrorKind)
	require.Equal(t, "query", searchError.Argument)
	require.Equal(t, "usage", searchError.Error.Kind)
	require.Equal(t, "parse_args", searchError.Error.Operation)
	require.Equal(t, "query", searchError.Error.Target)
	out.Reset()

	require.NoError(t, app.Memory([]string{"search", "focused", "--limit", "1", "--json"}))
	require.Contains(t, out.String(), `"action": "search"`)
	require.Contains(t, out.String(), `"match_count": 1`)
	require.Contains(t, out.String(), `"line": "Use focused tests."`)
	out.Reset()

	require.NoError(t, app.Memory([]string{"relevant", "focused", "--limit=1"}))
	require.Contains(t, out.String(), "Memory Search")
	require.Contains(t, out.String(), "Use focused tests.")
	out.Reset()

	require.NoError(t, app.Memory([]string{"path", "AGENTS.md", "--json"}))
	require.Contains(t, out.String(), `"action": "path"`)
	require.Contains(t, out.String(), "AGENTS.md")
	out.Reset()

	require.NoError(t, app.Memory([]string{"ensure", ".codog/instructions.md"}))
	require.Contains(t, out.String(), "Memory File")
	_, err = os.Stat(filepath.Join(workspace, ".codog", "instructions.md"))
	require.NoError(t, err)
	out.Reset()

	require.NoError(t, app.Memory([]string{"edit", ".codog/instructions.md", "--no-open", "--json"}))
	require.Contains(t, out.String(), `"action": "edit"`)
	require.Contains(t, out.String(), `"Editor launch skipped."`)
	out.Reset()

	require.NoError(t, app.Memory([]string{"reset", ".codog/instructions.md", "--confirm", "--json"}))
	require.Contains(t, out.String(), `"action": "reset"`)
	require.Contains(t, out.String(), `"reset_count": 1`)
	data, err = os.ReadFile(filepath.Join(workspace, ".codog", "instructions.md"))
	require.NoError(t, err)
	require.Empty(t, data)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/memory show AGENTS.md", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Memory")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/memory select AGENTS.md", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Memory Selection")
	require.Contains(t, out.String(), "Selected")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/memory search focused", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Memory Search")
	require.Contains(t, out.String(), "Use focused tests.")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/memory edit --no-open", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Memory File")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/memory reset --all --confirm", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Memory Reset")
	data, err = os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	require.NoError(t, err)
	require.Empty(t, data)
}

func TestMemoryCommandActionAliases(t *testing.T) {
	for _, tc := range []struct {
		alias  string
		action string
		rest   []string
	}{
		{alias: "ls", action: "list"},
		{alias: "choose", action: "select", rest: []string{"AGENTS.md"}},
		{alias: "use", action: "select", rest: []string{"AGENTS.md"}},
		{alias: "view", action: "show", rest: []string{"AGENTS.md"}},
		{alias: "cat", action: "show", rest: []string{"AGENTS.md"}},
		{alias: "read", action: "show", rest: []string{"AGENTS.md"}},
		{alias: "append", action: "add", rest: []string{"remember"}},
		{alias: "find", action: "search", rest: []string{"remember"}},
		{alias: "file", action: "path", rest: []string{"AGENTS.md"}},
		{alias: "init", action: "ensure", rest: []string{".codog/instructions.md"}},
		{alias: "create", action: "ensure", rest: []string{".codog/instructions.md"}},
		{alias: "touch", action: "ensure", rest: []string{".codog/instructions.md"}},
		{alias: "open", action: "edit", rest: []string{".codog/instructions.md"}},
		{alias: "clear", action: "reset", rest: []string{"AGENTS.md"}},
	} {
		args := append([]string{tc.alias}, tc.rest...)
		req, err := parseMemoryArgs(args)
		require.NoError(t, err, tc.alias)
		require.Equal(t, tc.action, req.Action, tc.alias)
		require.Equal(t, tc.rest, req.Rest, tc.alias)
	}
}

func TestMemoryCommandAliasExecution(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Alias first line\n"), 0o644))
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Memory([]string{"view", "AGENTS.md", "--json"}))
	require.Contains(t, out.String(), `"action": "show"`)
	require.Contains(t, out.String(), "Alias first line")
	out.Reset()

	require.NoError(t, app.Memory([]string{"append", "Alias", "appended."}))
	data, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), "Alias appended.")
	out.Reset()

	require.NoError(t, app.Memory([]string{"find", "appended", "--json"}))
	require.Contains(t, out.String(), `"action": "search"`)
	require.Contains(t, out.String(), `"match_count": 1`)
	out.Reset()

	require.NoError(t, app.Memory([]string{"file", "AGENTS.md", "--json"}))
	require.Contains(t, out.String(), `"action": "path"`)
	out.Reset()

	require.NoError(t, app.Memory([]string{"init", ".codog/instructions.md", "--json"}))
	require.Contains(t, out.String(), `"action": "ensure"`)
	require.FileExists(t, filepath.Join(workspace, ".codog", "instructions.md"))
	out.Reset()

	require.NoError(t, app.Memory([]string{"clear", ".codog/instructions.md", "--confirm", "--json"}))
	require.Contains(t, out.String(), `"action": "reset"`)
	data, err = os.ReadFile(filepath.Join(workspace, ".codog", "instructions.md"))
	require.NoError(t, err)
	require.Empty(t, data)
}

func TestMemoryCommandJSONErrors(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	err := app.Memory([]string{"show", "--json"})
	requireStructuredMemoryError(t, err, out.Bytes(), "show", "no_memory_files", "", "")
	out.Reset()

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Memory first line\n"), 0o644))
	err = app.Memory([]string{"show", "missing.md", "--json"})
	requireStructuredMemoryError(t, err, out.Bytes(), "show", "memory_file_not_found", "path", "")
	out.Reset()

	err = app.Memory([]string{"path", "../outside.md", "--json"})
	requireStructuredMemoryError(t, err, out.Bytes(), "path", "invalid_memory_path", "path", "")
	out.Reset()

	err = app.Memory([]string{"serch", "Memory", "--json"})
	requireStructuredMemoryError(t, err, out.Bytes(), "serch", "unsupported_memory_action", "", "Did you mean `codog memory search`?")
	out.Reset()

	err = app.Memory([]string{"--json", "reset"})
	requireStructuredMemoryError(t, err, out.Bytes(), "reset", "confirmation_required", "", "")
	out.Reset()

	err = app.Memory([]string{"reset", "--output-format", "json"})
	requireStructuredMemoryError(t, err, out.Bytes(), "reset", "confirmation_required", "", "")
}

func requireStructuredMemoryError(t *testing.T, err error, data []byte, action string, errorKind string, argument string, hintPart string) {
	t.Helper()
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var report actionErrorReport
	require.NoError(t, json.Unmarshal(data, &report))
	require.Equal(t, "memory", report.Kind)
	require.Equal(t, action, report.Action)
	require.Equal(t, "error", report.Status)
	require.Equal(t, errorKind, report.ErrorKind)
	if argument != "" {
		require.Equal(t, argument, report.Argument)
	}
	require.NotEmpty(t, report.Message)
	require.NotEmpty(t, report.Hint)
	if hintPart != "" {
		require.Contains(t, report.Hint, hintPart)
	}
}

func TestFocusCommandAndSlashInjectsSystemPrompt(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("focus body\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "a.go"), []byte("package pkg\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Focus([]string{"notes.md"}))
	require.Contains(t, out.String(), "Focus")
	require.Contains(t, out.String(), "notes.md")
	require.FileExists(t, focus.Path(workspace))
	require.Contains(t, app.systemPrompt(), "<focused_context>")
	require.Contains(t, app.systemPrompt(), "focus body")
	out.Reset()

	require.NoError(t, app.Focus([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "focus"`)
	require.Contains(t, out.String(), `"path": "notes.md"`)
	out.Reset()

	require.NoError(t, app.Focus([]string{"pkg"}))
	require.Contains(t, app.systemPrompt(), "- pkg/a.go")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/unfocus notes.md", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Focus")
	require.NotContains(t, app.systemPrompt(), "focus body")
	require.Contains(t, app.systemPrompt(), "- pkg/a.go")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/unfocus --all", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Entries          0")
	require.NotContains(t, app.systemPrompt(), "<focused_context>")
	require.Empty(t, errOut.String())
}

func TestAddDirCommandAndSlashUpdatesToolScope(t *testing.T) {
	workspace := t.TempDir()
	extra := filepath.Join(t.TempDir(), "extra")
	require.NoError(t, os.MkdirAll(extra, 0o755))
	extraFile := filepath.Join(extra, "notes.txt")
	require.NoError(t, os.WriteFile(extraFile, []byte("extra body\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.AddDir([]string{extra, "--json"}))
	require.Contains(t, out.String(), `"kind": "additional_dirs"`)
	require.Contains(t, out.String(), extra)
	require.FileExists(t, pathscope.Path(workspace))
	require.Contains(t, app.systemPrompt(), "<additional_directories>")
	out.Reset()

	require.NoError(t, app.Validation([]string{"add-dir", extra, filepath.Join(workspace, "missing"), "--json"}))
	var validationReport pathscope.ValidationReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &validationReport))
	require.Equal(t, "validation", validationReport.Kind)
	require.Equal(t, "error", validationReport.Status)
	require.Equal(t, 2, validationReport.Total)
	require.Equal(t, 1, validationReport.ValidCount)
	require.Equal(t, 1, validationReport.InvalidCount)
	require.True(t, validationReport.Entries[0].AlreadyAllowed)
	out.Reset()

	input, _ := json.Marshal(map[string]string{"path": extraFile})
	toolOut, err := app.Tools.Execute(context.Background(), "read_file", input, nil)
	require.NoError(t, err)
	require.Contains(t, toolOut, "extra body")

	require.True(t, app.handleSlash(context.Background(), "/add-dir remove "+extra, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Additional Directories")
	require.NotContains(t, app.systemPrompt(), "<additional_directories>")
	_, err = app.Tools.Execute(context.Background(), "read_file", input, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workspace")
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/validation add-dir "+extra, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Add-dir Validation")
	require.Contains(t, out.String(), "Valid            1")
	require.Empty(t, errOut.String())

	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": app.Config.ConfigHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "validation", "add-dir", extra}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(cliOut), &validationReport))
	require.Equal(t, "validation", validationReport.Kind)
	require.Equal(t, "ok", validationReport.Status)
}

func TestWorkspaceCommandAndSlashSwitchesRuntimeWorkspace(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	next := t.TempDir()
	var err error
	workspace, err = filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	next, err = filepath.EvalSymlinks(next)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(next, "next.txt"), []byte("next body\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.WorkspaceCommand([]string{"--json"}))
	var report workspaceReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, workspace, report.Workspace)
	require.False(t, report.Changed)
	require.Equal(t, session.NewWorkspaceStore(configHome, workspace).Dir, report.SessionDir)
	out.Reset()

	require.NoError(t, app.WorkspaceCommand([]string{next, "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, next, app.Workspace)
	require.Equal(t, next, report.Workspace)
	require.True(t, report.Changed)
	require.Equal(t, workspace, report.PreviousWorkspace)
	require.Equal(t, session.NewWorkspaceStore(configHome, next).Dir, app.Sessions.Dir)

	input, _ := json.Marshal(map[string]string{"path": "next.txt"})
	toolOut, err := app.Tools.Execute(context.Background(), "read_file", input, nil)
	require.NoError(t, err)
	require.Contains(t, toolOut, "next body")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/cwd "+workspace, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Workspace")
	require.Equal(t, workspace, app.Workspace)
	require.Empty(t, errOut.String())
}

func TestContextCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Use focused tests.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("context body\n"), 0o644))
	_, err := focus.Add(workspace, []string{"notes.md"})
	require.NoError(t, err)
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("context-session", anthropic.TextMessage("user", "hello")))
	require.NoError(t, store.Append("context-session", anthropic.TextMessage("assistant", "done")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			PermissionMode: "workspace-write",
			MaxTokens:      4096,
			MaxTurns:       8,
			APIKey:         "test-key",
		},
		Sessions:  store,
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Context([]string{"--json"}, config.FlagOverrides{SessionID: "context-session"}))
	require.Contains(t, out.String(), `"kind": "context"`)
	require.Contains(t, out.String(), `"focused_paths": 1`)
	require.Contains(t, out.String(), `"message_count": 2`)
	require.Contains(t, out.String(), `"total_tokens":`)
	out.Reset()

	require.NoError(t, app.Context([]string{"--json"}, config.FlagOverrides{SessionID: "context-session"}))
	var contextReport contextview.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &contextReport))
	require.Equal(t, "context", contextReport.Kind)
	require.Equal(t, "context-session", contextReport.Session.ID)
	require.Equal(t, 2, contextReport.Session.MessageCount)
	out.Reset()

	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome, "model": "claude-test"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--session", "context-session", "--output-format", "json", "context-noninteractive"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(cliOut), &contextReport))
	require.Equal(t, "context", contextReport.Kind)
	require.Equal(t, "context-session", contextReport.Session.ID)

	sess, err := store.Open("context-session")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/context", sess))
	require.Contains(t, out.String(), "Context")
	require.Contains(t, out.String(), "Session          context-session (2 messages)")
	require.Contains(t, out.String(), "Focused paths    1")
	require.Empty(t, errOut.String())
	out.Reset()

	vizPath := filepath.Join(workspace, "context.html")
	require.NoError(t, app.ContextViz([]string{"--output", vizPath, "--json"}, config.FlagOverrides{SessionID: "context-session"}))
	require.Contains(t, out.String(), `"kind": "ctx_viz"`)
	require.Contains(t, out.String(), `"bytes":`)
	data, err := os.ReadFile(vizPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "<!doctype html>")
	require.Contains(t, string(data), "Codog Context")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/ctx_viz --output "+filepath.Join(workspace, "slash-context.html"), sess))
	require.Contains(t, out.String(), "Context Viz")
	require.FileExists(t, filepath.Join(workspace, "slash-context.html"))
	require.Empty(t, errOut.String())
}

func TestUsageCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("usage-session", anthropic.TextMessage("user", "hello usage")))
	providerUsage := anthropic.Usage{InputTokens: 50, OutputTokens: 11, CacheCreationInputTokens: 6, CacheReadInputTokens: 4}
	require.NoError(t, store.AppendWithUsage("usage-session", anthropic.Message{
		Role: "assistant",
		Content: []anthropic.ContentBlock{{
			Type:  "tool_use",
			Name:  "read_file",
			Input: json.RawMessage(`{"path":"README.md"}`),
		}},
	}, &providerUsage))
	require.NoError(t, store.Append("usage-session", anthropic.ToolResultMessage("tool-1", "ok", false)))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome, Model: "claude-haiku"},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Usage([]string{"--json"}, config.FlagOverrides{SessionID: "usage-session"}))
	require.Contains(t, out.String(), `"kind": "usage"`)
	require.Contains(t, out.String(), `"session_id": "usage-session"`)
	require.Contains(t, out.String(), `"tool_uses": 1`)
	require.Contains(t, out.String(), `"tool_results": 1`)
	require.Contains(t, out.String(), `"source": "actual"`)
	require.Contains(t, out.String(), `"input_tokens": 50`)
	require.Contains(t, out.String(), `"cache_creation_input_tokens": 6`)
	require.Contains(t, out.String(), `"cache_read_input_tokens": 4`)
	out.Reset()

	require.NoError(t, app.Cache([]string{"--session", "usage-session", "--json"}, config.FlagOverrides{}))
	var cache cacheReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &cache))
	require.Equal(t, "cache", cache.Kind)
	require.Equal(t, "usage-session", cache.SessionID)
	require.Equal(t, 1, cache.UsageRecords)
	require.Equal(t, 6, cache.CacheCreationInputTokens)
	require.Equal(t, 4, cache.CacheReadInputTokens)
	require.Equal(t, 10, cache.CacheTotalInputTokens)
	require.Equal(t, 0.0667, cache.CacheHitRatio)
	require.Equal(t, "actual", cache.Source)
	out.Reset()

	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome, "model": "claude-haiku"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--session", "usage-session", "--output-format", "json", "caches"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(cliOut), &cache))
	require.Equal(t, "cache", cache.Kind)
	require.Equal(t, "usage-session", cache.SessionID)
	require.Equal(t, 10, cache.CacheTotalInputTokens)

	require.NoError(t, app.BreakCache([]string{"--session", "usage-session", "--message", "force a new provider prompt prefix", "--json"}, config.FlagOverrides{}))
	var breakReport breakCacheReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &breakReport))
	require.Equal(t, "break_cache", breakReport.Kind)
	require.Equal(t, "append_marker", breakReport.Action)
	require.Equal(t, "ok", breakReport.Status)
	require.Equal(t, "usage-session", breakReport.SessionID)
	require.False(t, breakReport.CreatedSession)
	require.NotEmpty(t, breakReport.Nonce)
	require.Contains(t, breakReport.Marker, "force a new provider prompt prefix")
	require.Contains(t, breakReport.Marker, breakReport.Nonce)
	breakSession, err := store.Open("usage-session")
	require.NoError(t, err)
	require.Len(t, breakSession.Messages, 4)
	require.Equal(t, "user", breakSession.Messages[3].Role)
	require.Contains(t, breakSession.Messages[3].Content[0].Text, breakReport.Nonce)
	out.Reset()

	require.NoError(t, app.Metrics([]string{"--session", "usage-session", "--json"}, config.FlagOverrides{}))
	var metrics metricsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &metrics))
	require.Equal(t, "metrics", metrics.Kind)
	require.Equal(t, "show", metrics.Action)
	require.NotNil(t, metrics.Session)
	require.Equal(t, "usage-session", metrics.Session.ID)
	require.Equal(t, 1, metrics.Session.UsageRecords)
	require.Equal(t, 71, metrics.Session.TotalTokens)
	require.Equal(t, "actual", metrics.Session.TokenSource)
	require.Equal(t, 1, metrics.Session.ToolUses)
	require.Equal(t, 1, metrics.Session.ToolResults)
	require.Equal(t, 0.0667, metrics.Session.CacheHitRatio)
	require.Equal(t, 1, metrics.WorkspaceMetrics.SessionCount)
	require.Equal(t, 71, metrics.WorkspaceMetrics.TotalTokens)
	require.Equal(t, 1, metrics.WorkspaceMetrics.UsageRecords)
	require.NotEmpty(t, metrics.TopTools)
	require.Equal(t, "read_file", metrics.TopTools[0].Name)
	require.True(t, commandAcceptsGlobalOutputFormat("metrics"))
	out.Reset()

	require.NoError(t, app.PerfIssue([]string{"--token-threshold", "40", "--tool-threshold", "1", "--json"}))
	var perf perfissue.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &perf))
	require.Equal(t, "perf_issue", perf.Kind)
	require.Equal(t, "warn", perf.Status)
	require.Equal(t, 71, perf.TotalTokens)
	require.Contains(t, perfSignalKinds(perf.Signals), "high_token_usage")
	require.Contains(t, perfSignalKinds(perf.Signals), "high_tool_use")
	require.True(t, commandAcceptsGlobalOutputFormat("perf-issue"))
	out.Reset()

	require.NoError(t, app.PerfIssue([]string{"--write", "--token-threshold=40", "--tool-threshold=1"}))
	require.Contains(t, out.String(), "Performance Issue")
	require.Contains(t, out.String(), "File")
	perfFiles, err := filepath.Glob(filepath.Join(workspace, ".codog", "perf", "*.md"))
	require.NoError(t, err)
	require.Len(t, perfFiles, 1)
	perfData, err := os.ReadFile(perfFiles[0])
	require.NoError(t, err)
	require.Contains(t, string(perfData), "# Codog Performance Issue")
	require.Contains(t, string(perfData), "high_token_usage")
	out.Reset()

	require.NoError(t, app.Summary([]string{"--session", "usage-session", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "summary"`)
	require.Contains(t, out.String(), `"session_id": "usage-session"`)
	require.Contains(t, out.String(), `"tool_uses": 1`)
	require.Contains(t, out.String(), `"first_user":`)
	require.Contains(t, out.String(), `"hello usage"`)
	out.Reset()

	sess, err := store.Open("usage-session")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/usage", sess))
	require.Contains(t, out.String(), "Usage")
	require.Contains(t, out.String(), "Session          usage-session")
	require.Contains(t, out.String(), "Tool use         calls=1 results=1 errors=0")
	require.Contains(t, out.String(), "Token source     actual")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/stats", sess))
	require.Contains(t, out.String(), "Stats")
	require.Contains(t, out.String(), "Session          usage-session")
	require.Contains(t, out.String(), "Tool use         calls=1 results=1 errors=0")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/tokens", sess))
	require.Contains(t, out.String(), "Tokens")
	require.Contains(t, out.String(), "Total tokens")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/cache", sess))
	require.Contains(t, out.String(), "Prompt Cache")
	require.Contains(t, out.String(), "Cache created    6")
	require.Contains(t, out.String(), "Cache read       4")
	require.Contains(t, out.String(), "Hit ratio        6.67%")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/caches", sess))
	require.Contains(t, out.String(), "Prompt Cache")
	require.Contains(t, out.String(), "Cache created    6")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/break-cache slash marker", sess))
	require.Contains(t, out.String(), "Break Cache")
	require.Len(t, sess.Messages, 5)
	require.Contains(t, sess.Messages[4].Content[0].Text, "slash marker")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/metrics --limit 1", sess))
	require.Contains(t, out.String(), "Metrics")
	require.Contains(t, out.String(), "Current session")
	require.Contains(t, out.String(), "ID               usage-session")
	require.Contains(t, out.String(), "Tool use         calls=1 results=1 errors=0")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/perf-issue --token-threshold 40 --tool-threshold 1", sess))
	require.Contains(t, out.String(), "Performance Issue")
	require.Contains(t, out.String(), "high_token_usage")
	out.Reset()

	require.NoError(t, app.Insights([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "insights"`)
	require.Contains(t, out.String(), `"sessions": 1`)
	require.Contains(t, out.String(), `"tool_uses": 1`)
	require.Contains(t, out.String(), `"name": "read_file"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/insights --limit 1", sess))
	require.Contains(t, out.String(), "Insights")
	require.Contains(t, out.String(), "Recent prompts")
	out.Reset()

	thinkBackPath := filepath.Join(workspace, "think-back.html")
	require.NoError(t, app.ThinkBack([]string{"--year", "2026", "--output", thinkBackPath, "--json"}))
	require.Contains(t, out.String(), `"kind": "think_back"`)
	require.Contains(t, out.String(), `"year": 2026`)
	_, err = os.Stat(thinkBackPath)
	require.NoError(t, err)
	out.Reset()

	slashThinkBackPath := filepath.Join(workspace, "slash-think-back.html")
	require.True(t, app.handleSlash(context.Background(), "/think-back --year 2026 --output "+slashThinkBackPath, sess))
	require.Contains(t, out.String(), "Think Back")
	_, err = os.Stat(slashThinkBackPath)
	require.NoError(t, err)
	out.Reset()

	playPath := filepath.Join(workspace, "thinkback-play.html")
	require.True(t, app.handleSlash(context.Background(), "/thinkback-play --year 2026 --output "+playPath, sess))
	require.Contains(t, out.String(), "Think Back")
	_, err = os.Stat(playPath)
	require.NoError(t, err)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/summary", sess))
	require.Contains(t, out.String(), "Summary")
	require.Contains(t, out.String(), "Session          usage-session")
	require.Contains(t, out.String(), "Tool use         calls=1 results=1 errors=0")
	require.Empty(t, errOut.String())
}

func TestCompactCommandPersistsCompactedSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	compactPath := filepath.Join(workspace, "compact-hook.json")
	postCompactPath := filepath.Join(workspace, "post-compact-hook.json")
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("compact-session", anthropic.TextMessage("user", "one")))
	require.NoError(t, store.Append("compact-session", anthropic.TextMessage("assistant", "two")))
	require.NoError(t, store.Append("compact-session", anthropic.TextMessage("user", "three")))
	require.NoError(t, store.Append("compact-session", anthropic.TextMessage("assistant", "four")))
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			AutoCompactMessages: 2,
			Hooks: config.HookConfig{
				PreCompactCommands:  []config.HookCommand{{Command: "cat > " + shellQuote(compactPath)}},
				PostCompactCommands: []config.HookCommand{{Command: "cat > " + shellQuote(postCompactPath)}},
			},
		},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Compact([]string{"--session", "compact-session", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"original_messages": 4`)
	require.Contains(t, out.String(), `"remaining_messages": 3`)
	opened, err := store.Open("compact-session")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 3)
	require.Contains(t, opened.Messages[0].Content[0].Text, "auto-compacted")
	require.Equal(t, "three", opened.Messages[1].Content[0].Text)
	require.Equal(t, "four", opened.Messages[2].Content[0].Text)
	hookPayload, err := os.ReadFile(compactPath)
	require.NoError(t, err)
	var compactHook struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(hookPayload, &compactHook))
	require.Equal(t, "pre_compact", compactHook.Event)
	require.Contains(t, compactHook.Input, `"session_id":"compact-session"`)
	postHookPayload, err := os.ReadFile(postCompactPath)
	require.NoError(t, err)
	var postCompactHook struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(postHookPayload, &postCompactHook))
	require.Equal(t, "post_compact", postCompactHook.Event)
	require.Contains(t, postCompactHook.Input, `"session_id":"compact-session"`)
}

func TestCompactErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "missing output format",
			args:      []string{"compact", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "compact"`, `"option": "--output-format"`},
		},
		{
			name:      "invalid output format",
			args:      []string{"compact", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`},
		},
		{
			name:      "missing keep",
			args:      []string{"compact", "--keep"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "compact"`, `"option": "--keep"`},
		},
		{
			name:      "invalid keep",
			args:      []string{"compact", "--keep", "many"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--keep"`, `"value": "many"`},
		},
		{
			name:      "negative keep",
			args:      []string{"compact", "--keep", "-1"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--keep"`, `"value": "-1"`},
		},
		{
			name:      "missing session",
			args:      []string{"compact", "--session"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "compact"`, `"option": "--session"`},
		},
		{
			name:      "missing resume",
			args:      []string{"compact", "--resume"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "compact"`, `"option": "--resume"`},
		},
		{
			name:      "unknown option",
			args:      []string{"compact", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "compact"`, `"option": "--bogus"`},
		},
		{
			name:      "unexpected argument",
			args:      []string{"compact", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "compact"`, `"bogus"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
}

func TestUndoCommandAndSlashRestoreFile(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "notes.txt")
	require.NoError(t, os.WriteFile(path, []byte("old\n"), 0o644))

	_, err := undo.Push(workspace, "edit_file", path, true, []byte("old\n"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("new\n"), 0o644))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	require.NoError(t, app.Undo([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "undo"`)
	require.Contains(t, out.String(), `"restored": true`)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "old\n", string(data))

	out.Reset()
	_, err = undo.Push(workspace, "edit_file", path, true, []byte("old\n"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("newer\n"), 0o644))
	require.True(t, app.handleSlash(context.Background(), "/undo", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Undo")
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "old\n", string(data))
	require.Empty(t, errOut.String())
}

func TestUndoTUIChangeRestoresLastFileChange(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "notes.txt")
	require.NoError(t, os.WriteFile(path, []byte("old\n"), 0o644))
	_, err := undo.Push(workspace, "edit_file", path, true, []byte("old\n"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("new\n"), 0o644))

	app := &App{Workspace: workspace}
	result, err := app.undoTUIChange(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Undo", result.Title)
	require.Equal(t, "restored", result.Status)
	require.Contains(t, result.Lines, "Tool: edit_file")
	require.Contains(t, result.Lines, "Path: notes.txt")
	require.Contains(t, result.Lines, "Restored: true")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "old\n", string(data))
}

func TestUndoErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	workspace := t.TempDir()

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "missing output format",
			args:      []string{"undo", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "undo"`, `"option": "--output-format"`},
		},
		{
			name:      "invalid output format",
			args:      []string{"undo", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`},
		},
		{
			name:      "unknown option",
			args:      []string{"undo", "bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "undo"`, `"option": "bogus"`},
		},
		{
			name:      "no records",
			args:      []string{"undo"},
			kind:      "no_undo_records",
			errorKind: "no_undo_records",
			contains:  []string{`"kind": "no_undo_records"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--cwd", workspace, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
}

func TestRateLimitOptionsCommandAndSlash(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: t.TempDir(),
			RateLimit: config.RateLimitConfig{
				MaxRetries:       4,
				InitialBackoffMS: 250,
				MaxBackoffMS:     2000,
			},
		},
		Out: &out,
		Err: &errOut,
	}

	require.NoError(t, app.RateLimitOptions([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "rate_limit_options"`)
	require.Contains(t, out.String(), `"max_retries": 4`)
	require.Contains(t, out.String(), `"initial_backoff_ms": 250`)
	out.Reset()

	require.NoError(t, app.RateLimit([]string{"status", "--json"}))
	require.Contains(t, out.String(), `"kind": "rate_limit"`)
	require.Contains(t, out.String(), `"max_retries": 4`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/rate-limit status", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Rate Limit")
	require.Contains(t, out.String(), "Max retries      4")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/rate-limit-options", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Rate Limit Options")
	require.Contains(t, out.String(), "Max retries      4")
	require.Contains(t, out.String(), "429,500,502,503,504")
	out.Reset()

	require.NoError(t, app.MockLimits([]string{"--json", "--addr", ":9099", "--failures", "3", "--retry-after-ms", "1500"}))
	var mockReport mocklimits.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &mockReport))
	require.Equal(t, "mock_limits", mockReport.Kind)
	require.Equal(t, "ready", mockReport.Status)
	require.Equal(t, "http://127.0.0.1:9099", mockReport.BaseURL)
	require.Equal(t, 3, mockReport.Failures)
	require.Equal(t, 1500, mockReport.RetryAfterMS)
	require.True(t, commandAcceptsGlobalOutputFormat("mock-limits"))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/mock-limits --addr :9098 --failures 2", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Mock Limits")
	require.Contains(t, out.String(), "127.0.0.1:9098")
	require.Contains(t, out.String(), "Failures         2")
	out.Reset()

	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "mock-limits", "--addr", ":9097", "--failures", "1"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "mock_limits"`)
	require.Contains(t, cliOut, `"failures": 1`)
	require.Empty(t, errOut.String())
}

func TestRateLimitErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "missing output format",
			args:      []string{"rate-limit", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "rate-limit"`, `"option": "--output-format"`},
		},
		{
			name:      "invalid output format",
			args:      []string{"rate-limit", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`},
		},
		{
			name:      "missing target",
			args:      []string{"rate-limit", "--target"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "rate-limit"`, `"option": "--target"`},
		},
		{
			name:      "missing path",
			args:      []string{"rate-limit", "--path"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "rate-limit"`, `"option": "--path"`},
		},
		{
			name:      "missing max retries",
			args:      []string{"rate-limit", "--max-retries"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "rate-limit"`, `"option": "--max-retries"`},
		},
		{
			name:      "invalid max retries",
			args:      []string{"rate-limit", "--max-retries", "many"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--max-retries"`, `"value": "many"`},
		},
		{
			name:      "negative max retries",
			args:      []string{"rate-limit", "--max-retries", "-1"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--max-retries"`, `"value": "-1"`},
		},
		{
			name:      "unknown option",
			args:      []string{"rate-limit", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "rate-limit"`, `"option": "--bogus"`},
		},
		{
			name:      "unknown action",
			args:      []string{"rate-limit", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "rate-limit"`, `"bogus"`},
		},
		{
			name:      "set missing value",
			args:      []string{"rate-limit", "set"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "rate-limit set"`, `"argument": "VALUE"`},
		},
		{
			name:      "set missing field value",
			args:      []string{"rate-limit", "set", "max-retries"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "rate-limit set"`, `"option": "max-retries"`},
		},
		{
			name:      "set invalid field value",
			args:      []string{"rate-limit", "set", "max-retries", "many"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "max-retries"`, `"value": "many"`},
		},
		{
			name:      "set unknown field",
			args:      []string{"rate-limit", "set", "bogus", "1"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "rate-limit set"`, `"bogus"`},
		},
		{
			name:      "status with set flag",
			args:      []string{"rate-limit", "--max-retries", "2", "status"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "rate-limit status"`, `"--max-retries"`},
		},
		{
			name:      "options unknown option",
			args:      []string{"rate-limit-options", "bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "rate-limit-options"`, `"option": "bogus"`},
		},
		{
			name:      "options invalid format",
			args:      []string{"rate-limit-options", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
}

func TestMockLimitsErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "unknown argument",
			args:      []string{"mock-limits", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "mock-limits"`, `"bogus"`},
		},
		{
			name:      "missing output format",
			args:      []string{"mock-limits", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "mock-limits"`, `"option": "--output-format"`},
		},
		{
			name:      "invalid output format",
			args:      []string{"mock-limits", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`},
		},
		{
			name:      "missing failures",
			args:      []string{"mock-limits", "--failures"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "mock-limits"`, `"option": "--failures"`},
		},
		{
			name:      "invalid failures",
			args:      []string{"mock-limits", "--failures", "many"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--failures"`, `"value": "many"`},
		},
		{
			name:      "negative failures",
			args:      []string{"mock-limits", "--failures", "-1"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--failures"`, `"value": "-1"`},
		},
		{
			name:      "missing retry after",
			args:      []string{"mock-limits", "--retry-after-ms"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "mock-limits"`, `"option": "--retry-after-ms"`},
		},
		{
			name:      "invalid retry after",
			args:      []string{"mock-limits", "--retry-after-ms", "slow"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--retry-after-ms"`, `"value": "slow"`},
		},
		{
			name:      "negative retry after",
			args:      []string{"mock-limits", "--retry-after-ms", "-1"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--retry-after-ms"`, `"value": "-1"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
}

func TestAnthropicClientOptionsUseAPITimeoutConfig(t *testing.T) {
	options := anthropicClientOptionsFromConfig(config.Config{
		RateLimit: config.RateLimitConfig{
			MaxRetries:       3,
			InitialBackoffMS: 100,
			MaxBackoffMS:     200,
		},
		APITimeout: config.APITimeoutConfig{
			ConnectTimeoutSeconds: 11,
			RequestTimeoutSeconds: 222,
			MaxRetries:            7,
		},
		ProviderFallbacks: config.ProviderFallbackConfig{
			Primary:   "claude-primary",
			Fallbacks: []string{"claude-backup"},
		},
	})

	require.Equal(t, 7, options.RateLimit.MaxRetries)
	require.Equal(t, 100*time.Millisecond, options.RateLimit.InitialBackoff)
	require.Equal(t, 200*time.Millisecond, options.RateLimit.MaxBackoff)
	require.Equal(t, 11*time.Second, options.ConnectTimeout)
	require.Equal(t, 222*time.Second, options.RequestTimeout)
	require.Equal(t, anthropic.ProviderFallbackOptions{Primary: "claude-primary", Models: []string{"claude-backup"}}, options.Fallbacks)
}

func TestAntTraceCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	var providerRequest json.RawMessage
	server := httptest.NewServer(mockanthropic.Server{
		Text: "trace ok",
		OnRequest: func(raw json.RawMessage) {
			providerRequest = raw
		},
	}.Handler())
	defer server.Close()

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: t.TempDir(),
			Model:      "claude-test",
			BaseURL:    server.URL,
			APIKey:     "test-key",
			RateLimit: config.RateLimitConfig{
				MaxRetries:       1,
				InitialBackoffMS: 1,
				MaxBackoffMS:     2,
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.AntTrace(context.Background(), []string{"--message", "trace me", "--json"}))
	var report anttrace.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "ant_trace", report.Kind)
	require.Equal(t, "ok", report.Status)
	require.True(t, report.AuthConfigured)
	require.True(t, report.RequestSent)
	require.Equal(t, server.URL, report.BaseURL)
	require.Equal(t, "trace ok", report.TextPreview)
	require.Equal(t, 2, report.StreamEvents)
	require.Equal(t, 10, report.Usage.InputTokens)
	require.Equal(t, 5, report.Usage.OutputTokens)
	require.Equal(t, 1, report.RateLimit.MaxRetries)
	require.NotEmpty(t, providerRequest)
	var request map[string]any
	require.NoError(t, json.Unmarshal(providerRequest, &request))
	require.Equal(t, "claude-test", request["model"])
	require.Equal(t, float64(64), request["max_tokens"])
	out.Reset()

	require.NoError(t, app.AntTrace(context.Background(), []string{"--no-request", "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "skipped", report.Status)
	require.False(t, report.RequestSent)
	out.Reset()

	require.NoError(t, app.AntTrace(context.Background(), []string{"--write", "--message", "trace me"}))
	require.Contains(t, out.String(), "Anthropic Trace")
	require.Contains(t, out.String(), "File")
	traceFiles, err := filepath.Glob(filepath.Join(workspace, ".codog", "traces", "*.md"))
	require.NoError(t, err)
	require.Len(t, traceFiles, 1)
	traceData, err := os.ReadFile(traceFiles[0])
	require.NoError(t, err)
	require.Contains(t, string(traceData), "# Codog Anthropic Trace")
	require.Contains(t, string(traceData), "trace ok")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/ant-trace --no-request --json", &session.Session{ID: "session"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "skipped", report.Status)
	require.Empty(t, errOut.String())
	require.True(t, commandAcceptsGlobalOutputFormat("ant-trace"))
}

func TestAntTraceErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "unknown option",
			args:      []string{"ant-trace", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "ant-trace"`, `"option": "--bogus"`},
		},
		{
			name:      "missing output format",
			args:      []string{"ant-trace", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "ant-trace"`, `"option": "--output-format"`},
		},
		{
			name:      "invalid output format",
			args:      []string{"ant-trace", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`},
		},
		{
			name:      "missing message",
			args:      []string{"ant-trace", "--message"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "ant-trace"`, `"option": "--message"`},
		},
		{
			name:      "missing timeout",
			args:      []string{"ant-trace", "--timeout-ms"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "ant-trace"`, `"option": "--timeout-ms"`},
		},
		{
			name:      "invalid timeout",
			args:      []string{"ant-trace", "--timeout-ms", "soon"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--timeout-ms"`, `"value": "soon"`},
		},
		{
			name:      "negative timeout",
			args:      []string{"ant-trace", "--timeout-ms", "-1"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--timeout-ms"`, `"value": "-1"`},
		},
		{
			name:      "missing model",
			args:      []string{"ant-trace", "--model"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "ant-trace"`, `"option": "--model"`},
		},
		{
			name:      "missing base url",
			args:      []string{"ant-trace", "--base-url"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "ant-trace"`, `"option": "--base-url"`},
		},
		{
			name:      "missing provider",
			args:      []string{"ant-trace", "--provider"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "ant-trace"`, `"option": "--provider"`},
		},
		{
			name:      "missing output",
			args:      []string{"ant-trace", "--output"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "ant-trace"`, `"option": "--output"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
}

func TestResetLimitsCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"model":"test","rate_limit":{"max_retries":4,"initial_backoff_ms":250}}`), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			RateLimit:  config.RateLimitConfig{MaxRetries: 4, InitialBackoffMS: 250},
		},
		Out: &out,
		Err: &errOut,
	}

	require.NoError(t, app.ResetLimits([]string{"--path", configPath, "--json"}))
	require.Contains(t, out.String(), `"kind": "reset_limits"`)
	require.Contains(t, out.String(), `"max_retries": 4`)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), "rate_limit")
	out.Reset()

	require.NoError(t, os.WriteFile(configPath, []byte(`{"rate_limit":{"max_retries":3}}`), 0o644))
	app.Config.RateLimit = config.RateLimitConfig{MaxRetries: 3}
	require.True(t, app.handleSlash(context.Background(), "/reset-limits --path "+configPath, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Reset Limits")
	require.Contains(t, out.String(), "Previous retries 3")
	require.Empty(t, errOut.String())
}

func TestResetLimitsErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "missing output format",
			args:      []string{"reset-limits", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "reset-limits"`, `"option": "--output-format"`},
		},
		{
			name:      "invalid output format",
			args:      []string{"reset-limits", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`},
		},
		{
			name:      "missing target",
			args:      []string{"reset-limits", "--target"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "reset-limits"`, `"option": "--target"`},
		},
		{
			name:      "missing path",
			args:      []string{"reset-limits", "--path"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "reset-limits"`, `"option": "--path"`},
		},
		{
			name:      "unknown option",
			args:      []string{"reset-limits", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "reset-limits"`, `"option": "--bogus"`},
		},
		{
			name:      "unexpected argument",
			args:      []string{"reset-limits", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "reset-limits"`, `"bogus"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
}

func TestOutputStyleCommandAndSlashInjectsSystemPrompt(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "output-styles"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "output-styles", "brief.md"), []byte("Answer in one compact paragraph.\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.OutputStyle(nil))
	require.Contains(t, out.String(), "Output Style")
	require.Contains(t, out.String(), "brief")
	require.Contains(t, out.String(), "concise")
	out.Reset()

	require.NoError(t, app.OutputStyle([]string{"status", "--json"}))
	var listReport outputstyle.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &listReport))
	require.Equal(t, "output_style", listReport.Kind)
	require.Equal(t, "list", listReport.Action)
	require.NotEmpty(t, listReport.Styles)
	require.NotNil(t, listReport.Summary)
	out.Reset()

	require.NoError(t, app.OutputStyle([]string{"find", "compact", "--json"}))
	var searchReport outputstyle.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &searchReport))
	require.Equal(t, "output_style", searchReport.Kind)
	require.Equal(t, "search", searchReport.Action)
	require.Equal(t, "compact", searchReport.Query)
	require.Len(t, searchReport.Styles, 1)
	require.Equal(t, "brief", searchReport.Styles[0].Name)
	out.Reset()

	require.NoError(t, app.OutputStyle([]string{"sources", "--json"}))
	var sourcesReport outputstyle.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &sourcesReport))
	require.Equal(t, "output_style", sourcesReport.Kind)
	require.Equal(t, "sources", sourcesReport.Action)
	require.Equal(t, len(sourcesReport.Sources), sourcesReport.SourceCount)
	requireOutputStyleSourceRoot(t, sourcesReport.Sources, "user", filepath.Join(configHome, "output-styles"), true)
	requireOutputStyleSourceRoot(t, sourcesReport.Sources, "workspace", filepath.Join(workspace, ".codog", "output-styles"), false)
	out.Reset()

	require.NoError(t, app.OutputStyle([]string{"audit", "--json"}))
	var auditReport outputstyle.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &auditReport))
	require.Equal(t, "output_style", auditReport.Kind)
	require.Equal(t, "audit", auditReport.Action)
	require.Equal(t, "ok", auditReport.Status)
	require.NotNil(t, auditReport.Summary)
	require.Contains(t, auditReport.Message, "passed")
	out.Reset()

	require.NoError(t, app.OutputStyle([]string{"use", "brief", "--json"}))
	require.Contains(t, out.String(), `"active": "brief"`)
	require.FileExists(t, outputstyle.StatePath(workspace))
	require.Contains(t, app.systemPrompt(), `<output_style name="brief" source="user">`)
	require.Contains(t, app.systemPrompt(), "Answer in one compact paragraph.")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/output-style view brief", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Body")
	require.Contains(t, out.String(), "Answer in one compact paragraph.")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/output-style doctor --json", &session.Session{ID: "session"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &auditReport))
	require.Equal(t, "audit", auditReport.Action)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/output-style off", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Output Style")
	require.NotContains(t, app.systemPrompt(), "<output_style")
	require.Empty(t, errOut.String())
}

func TestOutputStyleErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "missing output format",
			args:      []string{"output-style", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "output-style"`, `"option": "--output-format"`},
		},
		{
			name:      "invalid output format",
			args:      []string{"output-style", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`},
		},
		{
			name:      "show missing name",
			args:      []string{"output-style", "show"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "output-style show"`, `"argument": "NAME"`},
		},
		{
			name:      "search missing query",
			args:      []string{"output-style", "search"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "output-style search"`, `"argument": "QUERY"`},
		},
		{
			name:      "set missing name",
			args:      []string{"output-style", "set"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "output-style set"`, `"argument": "NAME"`},
		},
		{
			name:      "unknown option",
			args:      []string{"output-style", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "output-style"`, `"option": "--bogus"`},
		},
		{
			name:      "extra argument",
			args:      []string{"output-style", "show", "concise", "extra"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "output-style show"`, `"extra"`},
		},
		{
			name:      "not found",
			args:      []string{"output-style", "missing-style"},
			kind:      "output_style_not_found",
			errorKind: "output_style_not_found",
			contains:  []string{`"value": "missing-style"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
}

func requireOutputStyleSourceRoot(t *testing.T, roots []outputstyle.DiscoveryRoot, source string, path string, exists bool) {
	t.Helper()
	for _, root := range roots {
		if root.Source == source && root.Path == path {
			require.Equal(t, exists, root.Exists)
			require.NotEmpty(t, root.Label)
			return
		}
	}
	require.Failf(t, "output style source root not found", "source=%s path=%s roots=%v", source, path, roots)
}

func TestThemeVimAndPrivacyCommandsPersistPreferences(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("session", anthropic.TextMessage("assistant", "assistant answer")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Sessions:  store,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Theme([]string{"list", "--json"}))
	require.Contains(t, out.String(), `"theme": "default"`)
	out.Reset()

	require.NoError(t, app.Theme([]string{"dark", "--json"}))
	require.Contains(t, out.String(), `"kind": "theme"`)
	require.Contains(t, out.String(), `"theme": "dark"`)
	require.Equal(t, "dark", app.Config.Theme)
	configPath := filepath.Join(configHome, "config.json")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"theme": "dark"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/color light", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Theme")
	require.Equal(t, "light", app.Config.Theme)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"theme": "light"`)
	out.Reset()

	require.NoError(t, app.Theme([]string{"view", "--json"}))
	require.Contains(t, out.String(), `"action": "status"`)
	require.Contains(t, out.String(), `"theme": "light"`)
	out.Reset()

	require.NoError(t, app.Theme([]string{"off", "--json"}))
	require.Contains(t, out.String(), `"action": "clear"`)
	require.Equal(t, "", app.Config.Theme)
	out.Reset()

	require.NoError(t, app.Theme([]string{"use", "dark", "--json"}))
	require.Contains(t, out.String(), `"action": "set"`)
	require.Contains(t, out.String(), `"theme": "dark"`)
	require.Equal(t, "dark", app.Config.Theme)
	out.Reset()

	require.NoError(t, app.Theme([]string{"use", "ansi", "--json"}))
	require.Contains(t, out.String(), `"theme": "dark-ansi"`)
	require.Equal(t, "dark-ansi", app.Config.Theme)
	out.Reset()

	err = app.Theme([]string{"use", "unknown-theme", "--json"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "theme must be one of")
	require.Equal(t, "dark-ansi", app.Config.Theme)
	out.Reset()

	require.NoError(t, app.Language([]string{"use", "Japanese", "--json"}))
	require.Contains(t, out.String(), `"kind": "language"`)
	require.Contains(t, out.String(), `"language": "Japanese"`)
	require.Equal(t, "Japanese", app.Config.Language)
	require.Contains(t, app.systemPrompt(), "<codog_interface_language>Japanese</codog_interface_language>")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"language": "Japanese"`)
	out.Reset()

	require.NoError(t, app.Language([]string{"view", "--json"}))
	require.Contains(t, out.String(), `"action": "status"`)
	require.Contains(t, out.String(), `"language": "Japanese"`)
	out.Reset()

	require.NoError(t, app.Language([]string{"off", "--json"}))
	require.Contains(t, out.String(), `"action": "clear"`)
	require.Equal(t, "", app.Config.Language)
	out.Reset()

	require.NoError(t, app.Language([]string{"select", "Japanese", "--json"}))
	require.Contains(t, out.String(), `"action": "set"`)
	require.Contains(t, out.String(), `"language": "Japanese"`)
	require.Equal(t, "Japanese", app.Config.Language)
	out.Reset()

	require.NoError(t, app.Vim([]string{"on", "--json"}))
	require.Contains(t, out.String(), `"kind": "vim"`)
	require.Contains(t, out.String(), `"enabled": true`)
	require.Equal(t, "vim", app.Config.EditorMode)
	require.True(t, app.readlineVimMode())
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"editorMode": "vim"`)
	out.Reset()

	require.NoError(t, app.Effort([]string{"high", "--json"}))
	require.Contains(t, out.String(), `"kind": "effort"`)
	require.Contains(t, out.String(), `"effort": "high"`)
	require.Equal(t, "high", app.Config.ReasoningEffort)
	require.Contains(t, app.systemPrompt(), "<codog_reasoning_effort>high</codog_reasoning_effort>")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"reasoning_effort": "high"`)
	out.Reset()

	require.NoError(t, app.Reasoning([]string{"medium", "--json"}))
	require.Contains(t, out.String(), `"kind": "reasoning"`)
	require.Contains(t, out.String(), `"effort": "medium"`)
	require.Equal(t, "medium", app.Config.ReasoningEffort)
	require.Contains(t, app.systemPrompt(), "<codog_reasoning_effort>medium</codog_reasoning_effort>")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"reasoning_effort": "medium"`)
	out.Reset()

	require.NoError(t, app.Effort([]string{"disabled", "--json"}))
	require.Contains(t, out.String(), `"kind": "effort"`)
	require.Contains(t, out.String(), `"effort": "disabled"`)
	require.Equal(t, "disabled", app.Config.ReasoningEffort)
	require.NotContains(t, app.systemPrompt(), "<codog_reasoning_effort>")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"reasoning_effort": "disabled"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/effort low", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Effort")
	require.Equal(t, "low", app.Config.ReasoningEffort)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/fast on", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Fast Mode")
	require.NotNil(t, app.Config.FastMode)
	require.True(t, *app.Config.FastMode)
	require.Contains(t, app.systemPrompt(), "<codog_fast_mode>enabled</codog_fast_mode>")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"fast_mode": true`)
	out.Reset()

	voiceCommand := os.Args[0] + " -test.run=TestVoiceCommandHelperProcess"
	require.NoError(t, app.Voice([]string{"set-command", voiceCommand, "--json"}))
	require.Contains(t, out.String(), `"kind": "voice"`)
	require.Contains(t, out.String(), `"command_configured": true`)
	require.Contains(t, out.String(), `"command_available": true`)
	require.Equal(t, voiceCommand, app.Config.VoiceCommand)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"voice_command"`)
	out.Reset()

	require.NoError(t, app.Voice([]string{"on", "--json"}))
	require.Contains(t, out.String(), `"enabled": true`)
	require.NotNil(t, app.Config.VoiceEnabled)
	require.True(t, *app.Config.VoiceEnabled)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"voice_enabled": true`)
	out.Reset()

	t.Setenv("CODOG_TEST_VOICE_HELPER", "1")
	require.NoError(t, app.Voice([]string{"test", "--input", "mic-check", "--json"}))
	require.Contains(t, out.String(), `"action": "test"`)
	require.Contains(t, out.String(), `"transcript": "voice:mic-check"`)
	require.Contains(t, out.String(), `"exit_code": 0`)
	out.Reset()

	require.NoError(t, app.Voice([]string{"listen", "--input", "listen-check"}))
	require.Contains(t, out.String(), "Transcript       voice:listen-check")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/listen --input slash-check", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Transcript       voice:slash-check")
	out.Reset()

	speakCommand := os.Args[0] + " -test.run=TestVoiceCommandHelperProcess"
	require.NoError(t, app.Speak(context.Background(), []string{"set-command", speakCommand, "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "speak"`)
	require.Contains(t, out.String(), `"command_configured": true`)
	require.Contains(t, out.String(), `"command_available": true`)
	require.Equal(t, speakCommand, app.Config.SpeechCommand)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"speech_command"`)
	out.Reset()

	t.Setenv("CODOG_TEST_SPEAK_HELPER", "1")
	require.NoError(t, app.Speak(context.Background(), []string{"--input", "say this", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"action": "speak"`)
	require.Contains(t, out.String(), `"text_preview": "say this"`)
	require.Contains(t, out.String(), `"stdout": "speak:say this"`)
	require.Contains(t, out.String(), `"exit_code": 0`)
	out.Reset()

	require.NoError(t, app.Speak(context.Background(), []string{"--json"}, config.FlagOverrides{SessionID: "session"}))
	require.Contains(t, out.String(), `"session_id": "session"`)
	require.Contains(t, out.String(), `"text_preview": "assistant answer"`)
	require.Contains(t, out.String(), `"stdout": "speak:assistant answer"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/speak", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Text             assistant answer")
	require.Contains(t, out.String(), "Stdout           speak:assistant answer")
	out.Reset()

	require.NoError(t, app.Speak(context.Background(), []string{"clear-command", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "speak"`)
	require.Equal(t, "", app.Config.SpeechCommand)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"speech_command"`)
	out.Reset()

	require.NoError(t, app.Chrome([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "chrome"`)
	require.Contains(t, out.String(), `"enabled": false`)
	require.Contains(t, out.String(), `"install_url": "https://claude.ai/chrome"`)
	out.Reset()

	require.NoError(t, app.Chrome([]string{"on", "--json"}))
	require.Contains(t, out.String(), `"action": "set"`)
	require.Contains(t, out.String(), `"enabled": true`)
	require.NotNil(t, app.Config.Future.ChromeDefaultEnabled)
	require.True(t, *app.Config.Future.ChromeDefaultEnabled)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"preferences"`)
	require.Contains(t, string(data), `"chrome_default_enabled": true`)
	require.NotContains(t, string(data), `"future"`)
	out.Reset()

	require.NoError(t, app.Chrome([]string{"permissions"}))
	require.Contains(t, out.String(), "Permissions URL")
	require.Contains(t, out.String(), "https://clau.de/chrome/permissions")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/chrome off", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Chrome")
	require.NotNil(t, app.Config.Future.ChromeDefaultEnabled)
	require.False(t, *app.Config.Future.ChromeDefaultEnabled)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"preferences"`)
	require.Contains(t, string(data), `"chrome_default_enabled": false`)
	require.NotContains(t, string(data), `"future"`)
	out.Reset()

	require.NoError(t, app.PrivacySettings([]string{"set", "prompt-history", "off", "--json"}))
	require.Contains(t, out.String(), `"kind": "privacy_settings"`)
	require.Contains(t, out.String(), `"prompt_history_enabled": false`)
	require.False(t, app.promptHistoryEnabled())
	require.Empty(t, app.replHistoryFile())
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"prompt_history_enabled": false`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/theme clear", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Theme")
	require.Equal(t, "", app.Config.Theme)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/language clear", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Language")
	require.Equal(t, "", app.Config.Language)
	require.NotContains(t, app.systemPrompt(), "<codog_interface_language>")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"language"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/reasoning clear", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Reasoning")
	require.Equal(t, "", app.Config.ReasoningEffort)
	require.NotContains(t, app.systemPrompt(), "<codog_reasoning_effort>")
	out.Reset()

	require.NoError(t, app.Fast([]string{"clear", "--json"}))
	require.Contains(t, out.String(), `"kind": "fast"`)
	require.Nil(t, app.Config.FastMode)
	require.NotContains(t, app.systemPrompt(), "<codog_fast_mode>")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"fast_mode"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/voice clear", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Voice")
	require.Nil(t, app.Config.VoiceEnabled)
	require.Equal(t, "", app.Config.VoiceCommand)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"voice_enabled"`)
	require.NotContains(t, string(data), `"voice_command"`)
	out.Reset()

	require.NoError(t, app.Chrome([]string{"clear", "--json"}))
	require.Contains(t, out.String(), `"kind": "chrome"`)
	require.Nil(t, app.Config.Future.ChromeDefaultEnabled)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"chrome_default_enabled"`)
	require.NotContains(t, string(data), `"preferences"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/privacy-settings enable prompt-history", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Privacy Settings")
	require.True(t, app.promptHistoryEnabled())
	require.Empty(t, errOut.String())
}

func TestKeybindingsCommandAndSlash(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	configHome := t.TempDir()
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			EditorMode: "vim",
		},
		Out: &out,
		Err: &errOut,
	}

	require.NoError(t, app.Keybindings([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "keybindings"`)
	require.Contains(t, out.String(), `"editor_mode": "vim"`)
	require.Contains(t, out.String(), `"vim_mode": true`)
	require.Contains(t, out.String(), `"keybindings_exists": false`)
	out.Reset()

	keybindingsPath := filepath.Join(configHome, "keybindings.json")
	require.NoError(t, app.Keybindings([]string{"path"}))
	require.Equal(t, keybindingsPath+"\n", out.String())
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "repl", "Control-R", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+r"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"source": "default"`)
	require.Contains(t, out.String(), `"binding_action": "reverse search prompt history"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Shift-Enter", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "shift+enter"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "insert newline"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-S", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+s"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "stash or restore composer"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-G", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+g"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "edit composer in $EDITOR"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-X", "Ctrl-E", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+x ctrl+e"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "edit composer in $EDITOR"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-_", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+_"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "undo composer edit"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-Shift--", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+shift+-"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "undo composer edit"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-V", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+v"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "paste clipboard text or image"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-Shift-P", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+shift+p"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "quick open files"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-P", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+p"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "quick open fallback"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-Shift-F", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+shift+f"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "search workspace"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-F", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+f"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "search workspace fallback"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Alt-M", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "alt+m"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "cycle permission mode fallback"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Meta-M", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "meta+m"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "cycle permission mode fallback"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Alt-P", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "alt+p"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "open model picker"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Alt-O", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "alt+o"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "toggle fast mode"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Alt-T", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "alt+t"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "cycle thinking effort"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Shift-Up", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "shift+up"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "open message actions"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui-modal", "j", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"context": "tui-modal"`)
	require.Contains(t, out.String(), `"normalized_key": "j"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "move modal selection down"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui-modal", "Shift-Down", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"context": "tui-modal"`)
	require.Contains(t, out.String(), `"normalized_key": "shift+down"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "move to next user message"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui-attachments", "Backspace", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"context": "tui-attachments"`)
	require.Contains(t, out.String(), `"normalized_key": "backspace"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "remove selected attachment"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui-diff", "Enter", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"context": "tui-diff"`)
	require.Contains(t, out.String(), `"normalized_key": "enter"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "view selected file diff"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-X Ctrl-K", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+x ctrl+k"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "stop running background tasks and agents"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-X Ctrl-C", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+x ctrl+c"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "compact current session"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-X Ctrl-U", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+x ctrl+u"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "undo last file change"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-X Ctrl-S", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+x ctrl+s"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "export current conversation"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-X Ctrl-Y", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+x ctrl+y"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "copy current conversation"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-X Backspace", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+x backspace"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "remove last attachment"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-O", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+o"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "toggle expanded transcript"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-L", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+l"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "clear screen"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-B", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+b"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "run composer prompt in background"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-T", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+t"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "toggle tasks"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-Shift-T", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+shift+t"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"binding_action": "show background task board"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"init", "--json"}))
	require.Contains(t, out.String(), `"status": "created"`)
	require.Contains(t, out.String(), `"created": true`)
	data, err := os.ReadFile(keybindingsPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"context": "repl"`)
	require.Contains(t, string(data), `"shift+enter": "insert newline"`)
	require.Contains(t, string(data), `"ctrl+s": "stash or restore composer"`)
	require.Contains(t, string(data), `"ctrl+g": "edit composer in $EDITOR"`)
	require.Contains(t, string(data), `"ctrl+x ctrl+e": "edit composer in $EDITOR"`)
	require.Contains(t, string(data), `"ctrl+x ctrl+k": "stop running background tasks and agents"`)
	require.Contains(t, string(data), `"ctrl+x ctrl+c": "compact current session"`)
	require.Contains(t, string(data), `"ctrl+x ctrl+u": "undo last file change"`)
	require.Contains(t, string(data), `"ctrl+x ctrl+s": "export current conversation"`)
	require.Contains(t, string(data), `"ctrl+x ctrl+y": "copy current conversation"`)
	require.Contains(t, string(data), `"ctrl+x backspace": "remove last attachment"`)
	require.Contains(t, string(data), `"ctrl+_": "undo composer edit"`)
	require.Contains(t, string(data), `"ctrl+shift+-": "undo composer edit"`)
	require.Contains(t, string(data), `"ctrl+v": "paste clipboard text or image"`)
	require.Contains(t, string(data), `"ctrl+shift+p": "quick open files"`)
	require.Contains(t, string(data), `"ctrl+p": "quick open fallback"`)
	require.Contains(t, string(data), `"ctrl+shift+f": "search workspace"`)
	require.Contains(t, string(data), `"ctrl+f": "search workspace fallback"`)
	require.Contains(t, string(data), `"alt+m": "cycle permission mode fallback"`)
	require.Contains(t, string(data), `"meta+m": "cycle permission mode fallback"`)
	require.Contains(t, string(data), `"alt+p": "open model picker"`)
	require.Contains(t, string(data), `"alt+o": "toggle fast mode"`)
	require.Contains(t, string(data), `"alt+t": "cycle thinking effort"`)
	require.Contains(t, string(data), `"shift+up": "open message actions"`)
	require.Contains(t, string(data), `"ctrl+o": "toggle expanded transcript"`)
	require.Contains(t, string(data), `"ctrl+l": "clear screen"`)
	require.Contains(t, string(data), `"ctrl+d": "exit when composer is empty"`)
	require.Contains(t, string(data), `"ctrl+b": "run composer prompt in background"`)
	require.Contains(t, string(data), `"ctrl+t": "toggle tasks"`)
	require.Contains(t, string(data), `"ctrl+shift+t": "show background task board"`)
	require.Contains(t, string(data), `"up": "edit queued prompts, choose completion, or recall history"`)
	require.Contains(t, string(data), `"context": "tui-modal"`)
	require.Contains(t, string(data), `"j": "move modal selection down"`)
	require.Contains(t, string(data), `"k": "move modal selection up"`)
	require.Contains(t, string(data), `"shift+down": "move to next user message"`)
	require.Contains(t, string(data), `"context": "tui-attachments"`)
	require.Contains(t, string(data), `"right": "select next attachment"`)
	require.Contains(t, string(data), `"backspace": "remove selected attachment"`)
	require.Contains(t, string(data), `"context": "tui-diff"`)
	require.Contains(t, string(data), `"enter": "view selected file diff"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"validate", "--json"}))
	require.Contains(t, out.String(), `"action": "validate"`)
	require.Contains(t, out.String(), `"valid": true`)
	require.Contains(t, out.String(), `"context_count": 7`)
	require.Contains(t, out.String(), `"binding_count": 86`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+r"`)
	require.Contains(t, out.String(), `"normalized_key": "shift+enter"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+s"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+g"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+x ctrl+e"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+x ctrl+u"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+x ctrl+s"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+x ctrl+y"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+x backspace"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+_"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+shift+-"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+v"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+shift+p"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+p"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+shift+f"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+f"`)
	require.Contains(t, out.String(), `"normalized_key": "alt+m"`)
	require.Contains(t, out.String(), `"normalized_key": "meta+m"`)
	require.Contains(t, out.String(), `"normalized_key": "alt+p"`)
	require.Contains(t, out.String(), `"normalized_key": "alt+o"`)
	require.Contains(t, out.String(), `"normalized_key": "alt+t"`)
	require.Contains(t, out.String(), `"normalized_key": "shift+up"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+x ctrl+k"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+x ctrl+c"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+o"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+l"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+b"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+t"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+shift+t"`)
	require.Contains(t, out.String(), `"normalized_key": "up"`)
	require.Contains(t, out.String(), `"normalized_key": "j"`)
	require.Contains(t, out.String(), `"normalized_key": "k"`)
	require.Contains(t, out.String(), `"normalized_key": "shift+down"`)
	require.Contains(t, out.String(), `"normalized_key": "backspace"`)
	out.Reset()

	require.NoError(t, os.WriteFile(keybindingsPath, []byte(`{
  "bindings": [
    {
      "context": "tui",
      "bindings": {
        "ctrl+e": "edit composer in $EDITOR",
        "alt+q": "quick open files"
      }
    },
    {
      "context": "tui-modal",
      "bindings": {
        "alt+j": "move modal selection down"
      }
    },
    {
      "context": "tui-attachments",
      "bindings": {
        "alt+x": "remove selected attachment"
      }
    },
    {
      "context": "tui-diff",
      "bindings": {
        "alt+o": "view selected file diff"
      }
    }
  ]
}
`), 0o644))
	require.NoError(t, app.Keybindings([]string{"resolve", "tui", "Ctrl-E", "--json"}))
	require.Contains(t, out.String(), `"source": "user"`)
	require.Contains(t, out.String(), `"binding_action": "edit composer in $EDITOR"`)
	out.Reset()
	require.Equal(t, []string{"alt+q"}, app.tuiKeybindings()["quick open files"])
	require.Equal(t, []string{"ctrl+e"}, app.tuiKeybindings()["edit composer in $EDITOR"])
	require.Equal(t, []string{"alt+j"}, app.tuiContextKeybindings()["tui-modal"]["move modal selection down"])
	require.Equal(t, []string{"alt+x"}, app.tuiContextKeybindings()["tui-attachments"]["remove selected attachment"])
	require.Equal(t, []string{"alt+o"}, app.tuiContextKeybindings()["tui-diff"]["view selected file diff"])

	require.NoError(t, os.WriteFile(keybindingsPath, []byte("custom\n"), 0o644))
	require.Error(t, app.Keybindings([]string{"validate", "--json"}))
	require.Contains(t, out.String(), `"status": "invalid"`)
	require.Contains(t, out.String(), `"valid": false`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"init"}))
	require.Contains(t, out.String(), "already exists")
	data, err = os.ReadFile(keybindingsPath)
	require.NoError(t, err)
	require.Equal(t, "custom\n", string(data))
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"init", "--force"}))
	require.Contains(t, out.String(), "Wrote keybindings template:")
	data, err = os.ReadFile(keybindingsPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"bindings"`)
	out.Reset()

	editorLog := filepath.Join(configHome, "editor.log")
	editorScript := filepath.Join(configHome, "editor.sh")
	require.NoError(t, os.WriteFile(editorScript, []byte("#!/bin/sh\nprintf '%s\\n' \"$2\" > \"$1\"\n"), 0o755))
	t.Setenv("VISUAL", editorScript+" "+editorLog)
	require.NoError(t, os.Remove(keybindingsPath))
	require.NoError(t, app.Keybindings([]string{"open", "--json"}))
	require.Contains(t, out.String(), `"action": "open"`)
	require.Contains(t, out.String(), `"status": "created_opened"`)
	require.Contains(t, out.String(), `"created": true`)
	require.Contains(t, out.String(), `"opened": true`)
	openedPath, err := os.ReadFile(editorLog)
	require.NoError(t, err)
	require.Equal(t, keybindingsPath+"\n", string(openedPath))
	data, err = os.ReadFile(keybindingsPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"bindings"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"edit"}))
	require.Contains(t, out.String(), "Opened keybindings in editor:")
	openedPath, err = os.ReadFile(editorLog)
	require.NoError(t, err)
	require.Equal(t, keybindingsPath+"\n", string(openedPath))
	out.Reset()

	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	require.NoError(t, os.Remove(keybindingsPath))
	require.NoError(t, app.Keybindings([]string{"open", "--json"}))
	require.Contains(t, out.String(), `"status": "open_failed"`)
	require.Contains(t, out.String(), `"created": true`)
	require.Contains(t, out.String(), `"editor_error": "no editor configured; set VISUAL or EDITOR"`)
	require.FileExists(t, keybindingsPath)
	out.Reset()

	require.NoError(t, os.WriteFile(keybindingsPath, []byte(`{"bindings":[{"context":"repl","bindings":{"Ctrl-R":"custom history search"}}]}`), 0o644))
	require.NoError(t, app.Keybindings([]string{"resolve", "repl", "ctrl+r"}))
	require.Contains(t, out.String(), "Keybinding Resolve")
	require.Contains(t, out.String(), "Source           user")
	require.Contains(t, out.String(), "Action           custom history search")
	out.Reset()

	require.NoError(t, os.WriteFile(keybindingsPath, []byte(`{"bindings":[{"context":"repl","bindings":{"Ctrl-R":"custom","ctrl+r":"duplicate"}}]}`), 0o644))
	require.Error(t, app.Keybindings([]string{"validate", "--json"}))
	require.Contains(t, out.String(), `"status": "invalid"`)
	require.Contains(t, out.String(), "duplicate binding")
	out.Reset()

	require.NoError(t, os.WriteFile(keybindingsPath, []byte(`{"bindings":[{"context":"repl","bindings":{"enter":"submit"}},{"context":"repl","bindings":{"enter":"duplicate"}},{"context":"slash","bindings":{"/empty":""}}]}`), 0o644))
	require.Error(t, app.Keybindings([]string{"validate"}))
	require.Contains(t, out.String(), "duplicate binding")
	require.Contains(t, out.String(), "action is required")
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"init", "--force"}))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/keybindings", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Keybindings")
	require.Contains(t, out.String(), "Editor mode      vim")
	require.Contains(t, out.String(), "REPL vim")
	require.Contains(t, out.String(), "Config exists    true")
	require.Contains(t, out.String(), "User valid       true")
	require.Contains(t, out.String(), "User bindings    86")
	require.Contains(t, out.String(), "Home / Ctrl-A")
	require.Contains(t, out.String(), "End")
	require.Contains(t, out.String(), "Shift-Enter")
	require.Contains(t, out.String(), "Ctrl-S")
	require.Contains(t, out.String(), "Ctrl-G")
	require.Contains(t, out.String(), "Ctrl-X Ctrl-E")
	require.Contains(t, out.String(), "Ctrl-X Ctrl-K")
	require.Contains(t, out.String(), "Ctrl-X Ctrl-C")
	require.Contains(t, out.String(), "Ctrl-X Ctrl-U")
	require.Contains(t, out.String(), "Ctrl-X Ctrl-S")
	require.Contains(t, out.String(), "Ctrl-X Ctrl-Y")
	require.Contains(t, out.String(), "Ctrl-X Backspace")
	require.Contains(t, out.String(), "Ctrl-_")
	require.Contains(t, out.String(), "Ctrl-Shift--")
	require.Contains(t, out.String(), "Ctrl-V")
	require.Contains(t, out.String(), "Ctrl-Shift-P")
	require.Contains(t, out.String(), "Ctrl-P")
	require.Contains(t, out.String(), "Ctrl-Shift-F")
	require.Contains(t, out.String(), "Ctrl-F")
	require.Contains(t, out.String(), "Alt-M")
	require.Contains(t, out.String(), "Meta-M")
	require.Contains(t, out.String(), "Alt-P")
	require.Contains(t, out.String(), "Alt-O")
	require.Contains(t, out.String(), "Alt-T")
	require.Contains(t, out.String(), "Shift-Up")
	require.Contains(t, out.String(), "Ctrl-O")
	require.Contains(t, out.String(), "Ctrl-L")
	require.Contains(t, out.String(), "Ctrl-D")
	require.Contains(t, out.String(), "Ctrl-B")
	require.Contains(t, out.String(), "Ctrl-T")
	require.Contains(t, out.String(), "Ctrl-Shift-T")
	require.Contains(t, out.String(), "Up")
	require.Contains(t, out.String(), "TUI modal")
	require.Contains(t, out.String(), "J / Down / Ctrl-N")
	require.Contains(t, out.String(), "Shift-Up / Shift-Down")
	require.Contains(t, out.String(), "TUI attachments")
	require.Contains(t, out.String(), "Backspace / Delete")
	require.Contains(t, out.String(), "TUI diff")
	require.Contains(t, out.String(), "view selected file diff")
	require.Empty(t, errOut.String())
}

func TestRenderTUITaskBoard(t *testing.T) {
	app := &App{Config: config.Config{ConfigHome: t.TempDir()}}
	taskStore := background.NewStore(app.Config.ConfigHome)
	task, err := taskStore.RunWithOptions("printf board", t.TempDir(), background.RunOptions{
		Kind:      "prompt",
		SessionID: "session-tui",
		Prompt:    "review background board",
	})
	require.NoError(t, err)

	out, err := app.renderTUITaskBoard(context.Background())
	require.NoError(t, err)
	require.Contains(t, out, "Background tasks")
	require.Contains(t, out, "Active")
	require.Contains(t, out, task.ID)
	require.Contains(t, out, "review background board")
	require.Contains(t, out, "session=session-tui")
}

func TestCompactTUISessionRefreshesCurrentSession(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("tui-compact", anthropic.TextMessage("user", "one")))
	require.NoError(t, store.Append("tui-compact", anthropic.TextMessage("assistant", "two")))
	require.NoError(t, store.Append("tui-compact", anthropic.TextMessage("user", "three")))
	sess, err := store.Open("tui-compact")
	require.NoError(t, err)
	require.Len(t, sess.Messages, 3)

	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			AutoCompactMessages: 1,
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}

	result, err := app.compactTUISession(context.Background(), sess)
	require.NoError(t, err)

	require.Equal(t, "Session Compacted", result.Title)
	require.Equal(t, "compacted 1", result.Status)
	require.Contains(t, result.Lines, "Session: tui-compact")
	require.Contains(t, result.Lines, "Removed: 1")
	require.Len(t, sess.Messages, 2)
}

func TestRestoreTUIConversationReplacesSessionMessages(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("tui-restore", anthropic.TextMessage("user", "one")))
	require.NoError(t, store.Append("tui-restore", anthropic.TextMessage("assistant", "two")))
	require.NoError(t, store.Append("tui-restore", anthropic.TextMessage("user", "three")))
	require.NoError(t, store.Append("tui-restore", anthropic.TextMessage("assistant", "four")))
	sess, err := store.Open("tui-restore")
	require.NoError(t, err)
	require.Len(t, sess.Messages, 4)

	app := &App{
		Config:    config.Config{ConfigHome: configHome, MCPServers: map[string]config.MCPServerConfig{}},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}

	result, err := app.restoreTUIConversation(context.Background(), sess, 2)
	require.NoError(t, err)

	require.Equal(t, "Conversation Restored", result.Title)
	require.Equal(t, "restored 2", result.Status)
	require.Contains(t, result.Lines, "Session: tui-restore")
	require.Contains(t, result.Lines, "Remaining: 2")
	require.Contains(t, result.Lines, "Removed: 2")
	require.Len(t, sess.Messages, 2)
	require.Equal(t, "one", sess.Messages[0].Content[0].Text)
	require.Equal(t, "two", sess.Messages[1].Content[0].Text)
}

func TestForkTUIConversationCreatesTruncatedChildSession(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("tui-fork", anthropic.TextMessage("user", "one")))
	require.NoError(t, store.Append("tui-fork", anthropic.TextMessage("assistant", "two")))
	require.NoError(t, store.Append("tui-fork", anthropic.TextMessage("user", "three")))
	require.NoError(t, store.Append("tui-fork", anthropic.TextMessage("assistant", "four")))
	sess, err := store.Open("tui-fork")
	require.NoError(t, err)
	require.Len(t, sess.Messages, 4)

	app := &App{
		Config:    config.Config{ConfigHome: configHome, MCPServers: map[string]config.MCPServerConfig{}},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}

	result, err := app.forkTUIConversation(context.Background(), sess, 2)
	require.NoError(t, err)

	require.Equal(t, "Conversation Forked", result.Title)
	require.Equal(t, "forked 2", result.Status)
	require.NotEqual(t, "tui-fork", sess.ID)
	require.Contains(t, result.Lines, "Session: "+sess.ID)
	require.Contains(t, result.Lines, "Parent: tui-fork")
	require.Contains(t, result.Lines, "Remaining: 2")
	require.Contains(t, result.Lines, "Removed: 2")
	require.Len(t, sess.Messages, 2)
	require.Equal(t, "one", sess.Messages[0].Content[0].Text)
	require.Equal(t, "two", sess.Messages[1].Content[0].Text)
	require.Equal(t, "tui-fork", sess.Metadata.ParentSessionID)
	require.Equal(t, "rewind", sess.Metadata.BranchName)

	original, err := store.Open("tui-fork")
	require.NoError(t, err)
	require.Len(t, original.Messages, 4)
	forked, err := store.OpenExisting(sess.ID)
	require.NoError(t, err)
	require.Len(t, forked.Messages, 2)
}

func TestSummarizeTUIConversationCompactsFromTurn(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("tui-summarize", anthropic.TextMessage("user", "one")))
	require.NoError(t, store.Append("tui-summarize", anthropic.TextMessage("assistant", "two")))
	require.NoError(t, store.Append("tui-summarize", anthropic.TextMessage("user", "three")))
	require.NoError(t, store.Append("tui-summarize", anthropic.TextMessage("assistant", "four")))
	sess, err := store.Open("tui-summarize")
	require.NoError(t, err)
	require.Len(t, sess.Messages, 4)

	app := &App{
		Config:    config.Config{ConfigHome: configHome, MCPServers: map[string]config.MCPServerConfig{}},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}

	result, err := app.summarizeTUIConversation(context.Background(), sess, 2)
	require.NoError(t, err)

	require.Equal(t, "Conversation Summarized", result.Title)
	require.Equal(t, "summarized 2", result.Status)
	require.Contains(t, result.Lines, "Session: tui-summarize")
	require.Contains(t, result.Lines, "Before: 2")
	require.Contains(t, result.Lines, "Summarized: 2")
	require.Contains(t, result.Lines, "Remaining: 3")
	require.Contains(t, result.Lines, "Removed: 1")
	require.Len(t, sess.Messages, 3)
	require.Equal(t, "one", sess.Messages[0].Content[0].Text)
	require.Equal(t, "two", sess.Messages[1].Content[0].Text)
	summary := sess.Messages[2].Content[0].Text
	require.Contains(t, summary, "Conversation summary:")
	require.Contains(t, summary, "three")
	require.Contains(t, summary, "four")

	reopened, err := store.Open("tui-summarize")
	require.NoError(t, err)
	require.Len(t, reopened.Messages, 3)
	require.Equal(t, summary, reopened.Messages[2].Content[0].Text)
}

func TestSummarizeUpToTUIConversationCompactsEarlierTurns(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("tui-summarize-up-to", anthropic.TextMessage("user", "one")))
	require.NoError(t, store.Append("tui-summarize-up-to", anthropic.TextMessage("assistant", "two")))
	require.NoError(t, store.Append("tui-summarize-up-to", anthropic.TextMessage("user", "three")))
	require.NoError(t, store.Append("tui-summarize-up-to", anthropic.TextMessage("assistant", "four")))
	sess, err := store.Open("tui-summarize-up-to")
	require.NoError(t, err)
	require.Len(t, sess.Messages, 4)

	app := &App{
		Config:    config.Config{ConfigHome: configHome, MCPServers: map[string]config.MCPServerConfig{}},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}

	result, err := app.summarizeUpToTUIConversation(context.Background(), sess, 2)
	require.NoError(t, err)

	require.Equal(t, "Earlier Conversation Summarized", result.Title)
	require.Equal(t, "summarized earlier 2", result.Status)
	require.Contains(t, result.Lines, "Session: tui-summarize-up-to")
	require.Contains(t, result.Lines, "Summarized: 2")
	require.Contains(t, result.Lines, "After: 2")
	require.Contains(t, result.Lines, "Remaining: 3")
	require.Contains(t, result.Lines, "Removed: 1")
	require.Equal(t, "tui-summarize-up-to", sess.ID)
	require.Len(t, sess.Messages, 3)
	summary := sess.Messages[0].Content[0].Text
	require.Contains(t, summary, "Conversation summary:")
	require.Contains(t, summary, "one")
	require.Contains(t, summary, "two")
	require.Equal(t, "three", sess.Messages[1].Content[0].Text)
	require.Equal(t, "four", sess.Messages[2].Content[0].Text)

	reopened, err := store.Open("tui-summarize-up-to")
	require.NoError(t, err)
	require.Len(t, reopened.Messages, 3)
	require.Equal(t, summary, reopened.Messages[0].Content[0].Text)
}

func TestReadTUITodosUsesWorkspaceState(t *testing.T) {
	workspace := t.TempDir()
	app := &App{Workspace: workspace}
	_, err := todos.Replace(workspace, []todos.Item{
		{ID: "todo-1", Content: "write tests", ActiveForm: "writing tests", Status: "in_progress", Priority: "high"},
		{ID: "todo-2", Content: "run smoke", Status: "pending", Priority: "medium"},
	})
	require.NoError(t, err)

	items, err := app.readTUITodos(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, tui.TodoItem{ID: "todo-1", Content: "write tests", ActiveForm: "writing tests", Status: "in_progress", Priority: "high"}, items[0])
	require.Equal(t, tui.TodoItem{ID: "todo-2", Content: "run smoke", ActiveForm: "run smoke", Status: "pending", Priority: "medium"}, items[1])
}

func TestDetachedPromptCommandCarriesConfigHome(t *testing.T) {
	command := buildDetachedPromptCommand("/tmp/codog home", "/bin/codog", "review Bob's diff")

	require.Contains(t, command, "CODOG_CONFIG_HOME='/tmp/codog home'")
	require.Contains(t, command, "'/bin/codog' prompt")
	require.Contains(t, command, "'review Bob'\"'\"'s diff'")
}

func TestTUIBackgroundPromptCreatesTask(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	app := &App{
		Config:     config.Config{ConfigHome: configHome},
		Workspace:  workspace,
		Executable: "/bin/echo",
		Out:        io.Discard,
		Err:        io.Discard,
	}

	message, err := app.startTUIBackgroundPrompt(context.Background(), "session-tui", "review this diff")
	require.NoError(t, err)
	require.Contains(t, message, "Background task")

	tasks, err := background.NewStore(configHome).List()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "prompt", tasks[0].Kind)
	require.Equal(t, "session-tui", tasks[0].SessionID)
	require.Equal(t, "review this diff", tasks[0].Prompt)
	require.Equal(t, "TUI background prompt", tasks[0].Description)
	require.Contains(t, tasks[0].Command, "CODOG_CONFIG_HOME=")
	require.Contains(t, tasks[0].Command, "'/bin/echo' prompt 'review this diff'")
}

func TestTUIComposerExternalEditorUsesEditorEnv(t *testing.T) {
	dir := t.TempDir()
	editor := filepath.Join(dir, "editor.sh")
	require.NoError(t, os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'edited composer\\nsecond line\\n' > \"$1\"\n"), 0o755))
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", editor)

	app := &App{}
	edited, err := app.editTUIComposer(context.Background(), "draft composer")
	require.NoError(t, err)
	require.Equal(t, "edited composer\nsecond line\n", edited)
}

func TestTUIComposerExternalEditorRequiresEditor(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")

	app := &App{}
	_, err := app.editTUIComposer(context.Background(), "draft")
	require.ErrorContains(t, err, "set VISUAL or EDITOR")
}

func TestPreferenceCompatErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	workspace := t.TempDir()

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "fast missing output format",
			args:      []string{"fast", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "fast"`, `"option": "--output-format"`},
		},
		{
			name:      "fast missing target",
			args:      []string{"fast", "--target", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "fast"`, `"option": "--target"`},
		},
		{
			name:      "fast unknown command",
			args:      []string{"fast", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "fast"`, `"bogus"`},
		},
		{
			name:      "voice missing path",
			args:      []string{"voice", "--path", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "voice"`, `"option": "--path"`},
		},
		{
			name:      "voice set command missing command",
			args:      []string{"voice", "set-command"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "voice set-command"`, `"argument": "COMMAND"`},
		},
		{
			name:      "voice invalid timeout",
			args:      []string{"voice", "--timeout-ms", "-1"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--timeout-ms"`, `"value": "-1"`},
		},
		{
			name:      "chrome missing path",
			args:      []string{"chrome", "--path", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "chrome"`, `"option": "--path"`},
		},
		{
			name:      "chrome unknown command",
			args:      []string{"chrome", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "chrome"`, `"bogus"`},
		},
		{
			name:      "privacy missing target",
			args:      []string{"privacy-settings", "--target", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "privacy-settings"`, `"option": "--target"`},
		},
		{
			name:      "privacy set missing key",
			args:      []string{"privacy-settings", "set"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "privacy-settings set"`, `"argument": "KEY"`},
		},
		{
			name:      "privacy invalid key",
			args:      []string{"privacy-settings", "set", "nope", "on"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "key"`, `"value": "nope"`},
		},
		{
			name:      "privacy invalid value",
			args:      []string{"privacy-settings", "telemetry", "maybe"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "value"`, `"value": "maybe"`},
		},
		{
			name:      "profile missing output format",
			args:      []string{"profile", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "profile"`, `"option": "--output-format"`},
		},
		{
			name:      "profile set missing name",
			args:      []string{"profile", "set"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "profile set"`, `"argument": "NAME"`},
		},
		{
			name:      "telemetry missing target",
			args:      []string{"telemetry", "--target", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "telemetry"`, `"option": "--target"`},
		},
		{
			name:      "telemetry unknown command",
			args:      []string{"telemetry", "maybe"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "telemetry"`, `"maybe"`},
		},
		{
			name:      "keybindings missing output format",
			args:      []string{"keybindings", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "keybindings"`, `"option": "--output-format"`},
		},
		{
			name:      "keybindings resolve missing key",
			args:      []string{"keybindings", "resolve", "repl"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "keybindings resolve"`, `"argument": "KEY"`},
		},
		{
			name:      "keybindings unknown command",
			args:      []string{"keybindings", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "keybindings"`, `"bogus"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--cwd", workspace, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
	require.NoFileExists(t, filepath.Join(workspace, "--output-format"))
}

func TestThemeErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	workspace := t.TempDir()

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "missing output format",
			args:      []string{"theme", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "theme"`, `"option": "--output-format"`},
		},
		{
			name:      "invalid output format",
			args:      []string{"theme", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`},
		},
		{
			name:      "missing target",
			args:      []string{"theme", "--target"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "theme"`, `"option": "--target"`},
		},
		{
			name:      "missing path",
			args:      []string{"theme", "--path"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "theme"`, `"option": "--path"`},
		},
		{
			name:      "unknown option",
			args:      []string{"theme", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "theme"`, `"option": "--bogus"`},
		},
		{
			name:      "set missing name",
			args:      []string{"theme", "set"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "theme set"`, `"argument": "NAME"`},
		},
		{
			name:      "show extra",
			args:      []string{"theme", "show", "extra"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "theme show"`, `"extra"`},
		},
		{
			name:      "clear extra",
			args:      []string{"theme", "clear", "extra"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "theme clear"`, `"extra"`},
		},
		{
			name:      "invalid name",
			args:      []string{"theme", "bad/name"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "theme"`, `"value": "bad/name"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--cwd", workspace, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
	require.NoFileExists(t, filepath.Join(workspace, "--output-format"))
}

func TestLanguageErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	workspace := t.TempDir()

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "missing output format",
			args:      []string{"language", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "language"`, `"option": "--output-format"`},
		},
		{
			name:      "invalid output format",
			args:      []string{"language", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`},
		},
		{
			name:      "missing target",
			args:      []string{"language", "--target", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "language"`, `"option": "--target"`},
		},
		{
			name:      "missing path",
			args:      []string{"language", "--path", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "language"`, `"option": "--path"`},
		},
		{
			name:      "unknown option",
			args:      []string{"language", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "language"`, `"option": "--bogus"`},
		},
		{
			name:      "set missing name",
			args:      []string{"language", "set"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "language set"`, `"argument": "LANGUAGE"`},
		},
		{
			name:      "clear extra",
			args:      []string{"language", "clear", "extra"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "language clear"`, `"extra"`},
		},
		{
			name:      "invalid target",
			args:      []string{"language", "--target", "bogus", "Japanese"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--target"`, `"value": "bogus"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--cwd", workspace, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
	require.NoFileExists(t, filepath.Join(workspace, "--output-format"))
}

func TestVimErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	workspace := t.TempDir()

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "missing output format",
			args:      []string{"vim", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "vim"`, `"option": "--output-format"`},
		},
		{
			name:      "invalid output format",
			args:      []string{"vim", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`},
		},
		{
			name:      "missing target",
			args:      []string{"vim", "--target", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "vim"`, `"option": "--target"`},
		},
		{
			name:      "missing path",
			args:      []string{"vim", "--path", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "vim"`, `"option": "--path"`},
		},
		{
			name:      "unknown option",
			args:      []string{"vim", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "vim"`, `"option": "--bogus"`},
		},
		{
			name:      "set missing mode",
			args:      []string{"vim", "set"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "vim set"`, `"argument": "MODE"`},
		},
		{
			name:      "invalid mode",
			args:      []string{"vim", "maybe"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "mode"`, `"value": "maybe"`},
		},
		{
			name:      "extra argument",
			args:      []string{"vim", "on", "extra"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "vim on"`, `"extra"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--cwd", workspace, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
	require.NoFileExists(t, filepath.Join(workspace, "--output-format"))
}

func TestEffortReasoningErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	workspace := t.TempDir()

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "effort missing output format",
			args:      []string{"effort", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "effort"`, `"option": "--output-format"`},
		},
		{
			name:      "reasoning invalid output format",
			args:      []string{"reasoning", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`},
		},
		{
			name:      "effort missing target",
			args:      []string{"effort", "--target", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "effort"`, `"option": "--target"`},
		},
		{
			name:      "reasoning missing path",
			args:      []string{"reasoning", "--path", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "reasoning"`, `"option": "--path"`},
		},
		{
			name:      "effort unknown option",
			args:      []string{"effort", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "effort"`, `"option": "--bogus"`},
		},
		{
			name:      "reasoning set missing level",
			args:      []string{"reasoning", "set"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "reasoning set"`, `"argument": "LEVEL"`},
		},
		{
			name:      "effort invalid level",
			args:      []string{"effort", "bogus"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "effort"`, `"value": "bogus"`},
		},
		{
			name:      "reasoning invalid level",
			args:      []string{"reasoning", "bogus"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "reasoning"`, `"value": "bogus"`},
		},
		{
			name:      "effort extra argument",
			args:      []string{"effort", "high", "extra"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "effort"`, `"extra"`},
		},
		{
			name:      "reasoning clear extra",
			args:      []string{"reasoning", "clear", "extra"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "reasoning clear"`, `"extra"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--cwd", workspace, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
	require.NoFileExists(t, filepath.Join(workspace, "--output-format"))
}

func TestNotificationsCommandAndHookGate(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	notificationPath := filepath.Join(workspace, "notification.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Hooks: config.HookConfig{
				Notification: []string{
					"cat > " + shellQuote(notificationPath) + `; printf '%s' '{"systemMessage":"notification note","hookSpecificOutput":{"additionalContext":"notification context"}}'`,
				},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Notifications([]string{"status", "--json"}))
	require.Contains(t, out.String(), `"kind": "notifications"`)
	require.Contains(t, out.String(), `"enabled": true`)
	require.Contains(t, out.String(), `"configured": false`)
	require.Contains(t, out.String(), `"hook_count": 1`)
	out.Reset()

	require.NoError(t, app.Notifications([]string{"off", "--json"}))
	require.Contains(t, out.String(), `"action": "set"`)
	require.Contains(t, out.String(), `"enabled": false`)
	require.NotNil(t, app.Config.Future.NotificationsEnabled)
	require.False(t, *app.Config.Future.NotificationsEnabled)
	data, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"preferences"`)
	require.Contains(t, string(data), `"notifications_enabled": false`)
	require.NotContains(t, string(data), `"future"`)
	out.Reset()

	app.runNotificationHook(context.Background(), "background_task_started", "Started", "task started")
	require.NoFileExists(t, notificationPath)

	require.True(t, app.handleSlash(context.Background(), "/notifications on", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Notifications")
	require.NotNil(t, app.Config.Future.NotificationsEnabled)
	require.True(t, *app.Config.Future.NotificationsEnabled)
	out.Reset()

	app.runNotificationHook(context.Background(), "background_task_started", "Started", "task started")
	data, err = os.ReadFile(notificationPath)
	require.NoError(t, err)
	var payload struct {
		Event            string `json:"event"`
		NotificationType string `json:"notification_type"`
		Title            string `json:"title"`
		Message          string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "notification", payload.Event)
	require.Equal(t, "background_task_started", payload.NotificationType)
	require.Equal(t, "Started", payload.Title)
	require.Equal(t, "task started", payload.Message)
	require.Contains(t, errOut.String(), "notification hook feedback:")
	require.Contains(t, errOut.String(), "notification note")
	require.Contains(t, errOut.String(), "notification context")
	out.Reset()
	errOut.Reset()

	require.NoError(t, app.Notifications([]string{"clear", "--json"}))
	require.Contains(t, out.String(), `"configured": false`)
	require.Nil(t, app.Config.Future.NotificationsEnabled)
	data, err = os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.NotContains(t, string(data), `"notifications_enabled"`)
	require.NotContains(t, string(data), `"preferences"`)
	require.Empty(t, errOut.String())
}

func TestNotificationsErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	workspace := t.TempDir()

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "missing output format",
			args:      []string{"notifications", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "notifications"`, `"option": "--output-format"`},
		},
		{
			name:      "invalid output format",
			args:      []string{"notifications", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`},
		},
		{
			name:      "missing target",
			args:      []string{"notifications", "--target"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "notifications"`, `"option": "--target"`},
		},
		{
			name:      "missing path",
			args:      []string{"notifications", "--path"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "notifications"`, `"option": "--path"`},
		},
		{
			name:      "unknown option",
			args:      []string{"notifications", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "notifications"`, `"option": "--bogus"`},
		},
		{
			name:      "unknown command",
			args:      []string{"notifications", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "notifications"`, `"bogus"`},
		},
		{
			name:      "extra argument",
			args:      []string{"notifications", "on", "extra"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "notifications on"`, `"extra"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--cwd", workspace, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
	require.NoFileExists(t, filepath.Join(workspace, "--output-format"))
}

func TestTerminalSetupCommandAndSlash(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	path := filepath.Join(t.TempDir(), ".zshrc")
	app := &App{Out: &out, Err: &errOut}

	require.NoError(t, app.TerminalSetup([]string{"install", "--shell", "zsh", "--path", path, "--json"}))
	require.Contains(t, out.String(), `"kind": "terminal_setup"`)
	require.Contains(t, out.String(), `"installed": true`)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "codog_statusline")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/terminal-setup status --shell zsh --path "+path, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Terminal Setup")
	require.Contains(t, out.String(), "Installed        true")
	require.Empty(t, errOut.String())
}

func TestSetupCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/setup\n"), 0o644))
	terminalPath := filepath.Join(t.TempDir(), ".zshrc")
	var out bytes.Buffer
	var errOut bytes.Buffer
	var setupPayloads []struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	setupServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
			Input string `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		setupPayloads = append(setupPayloads, payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer setupServer.Close()
	app := &App{
		Config: config.Config{
			APIKey:     "test-key",
			ConfigHome: t.TempDir(),
			Model:      "claude-test",
			Hooks: config.HookConfig{
				SetupCommands: []config.HookCommand{{Type: "http", URL: setupServer.URL}},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Setup(context.Background(), []string{"status", "--shell", "zsh", "--path", terminalPath, "--json"}))
	var report setupReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "setup", report.Kind)
	require.Equal(t, "status", report.Action)
	require.Equal(t, "warn", report.Status)
	require.NotNil(t, report.Terminal)
	require.False(t, report.Terminal.Installed)
	requireSetupCheck(t, report.Checks, "Provider credentials", "ok")
	requireSetupCheck(t, report.Checks, "Project memory", "warn")
	requireSetupCheck(t, report.Checks, "Terminal integration", "warn")
	out.Reset()

	require.NoError(t, app.Setup(context.Background(), []string{"init", "--json"}))
	report = setupReport{}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "init", report.Action)
	require.Equal(t, "ok", report.Status)
	require.NotNil(t, report.Project)
	require.FileExists(t, filepath.Join(workspace, ".codog", "instructions.md"))
	require.FileExists(t, filepath.Join(workspace, ".codog.json"))
	require.FileExists(t, filepath.Join(workspace, "AGENTS.md"))
	require.FileExists(t, filepath.Join(workspace, "CLAUDE.md"))
	require.Len(t, setupPayloads, 1)
	require.Equal(t, "setup", setupPayloads[0].Event)
	require.Contains(t, setupPayloads[0].Input, `"source":"setup"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/setup status --shell zsh --path "+terminalPath, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Setup")
	require.Contains(t, out.String(), "Terminal integration")
	require.Empty(t, errOut.String())
}

func TestOnboardingCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Demo\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/onboarding\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package onboarding\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main_test.go"), []byte("package onboarding\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Run tests.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog.json"), []byte(`{"permission_mode":"workspace-write"}`), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(workspace, ".git"), 0o755))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Onboarding([]string{"--json"}))
	var report onboarding.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "onboarding", report.Kind)
	require.Equal(t, "inspect", report.Action)
	require.Equal(t, "ready", report.Status)
	require.True(t, report.HasReadme)
	require.True(t, report.HasTests)
	require.Equal(t, "Go", report.PrimaryLanguage)
	require.True(t, report.GitRepository)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/onboarding --json", &session.Session{ID: "session"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "ready", report.Status)
	require.Empty(t, errOut.String())
	out.Reset()

	other := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(other, "app.py"), []byte("print('hi')\n"), 0o644))
	require.NoError(t, app.Onboarding([]string{"--path", other, "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "needs_setup", report.Status)
	require.Equal(t, "Python", report.PrimaryLanguage)
	require.True(t, report.PythonFirst)
}

func requireSetupCheck(t *testing.T, checks []setupCheck, name string, status string) {
	t.Helper()
	for _, check := range checks {
		if check.Name == name {
			require.Equal(t, status, check.Status)
			return
		}
	}
	require.Failf(t, "missing setup check", "check %q not found in %#v", name, checks)
}

func TestRemoteEnvCommandPersistsSettings(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Out: &out, Err: &errOut}

	require.NoError(t, app.RemoteEnv([]string{"set", "--enabled", "on", "--auth-token", "secret-token", "--lease-seconds", "60", "--json"}))
	require.Contains(t, out.String(), `"kind": "remote_env"`)
	require.Contains(t, out.String(), `"enabled": true`)
	require.Contains(t, out.String(), `"auth_token_configured": true`)
	require.NotContains(t, out.String(), "secret-token")
	require.True(t, app.Config.Future.RemoteEnabled)
	require.Equal(t, "secret-token", app.Config.Future.RemoteAuthToken)
	require.Equal(t, 60, app.Config.Future.RemoteLeaseSeconds)
	configPath := filepath.Join(configHome, "config.json")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"remote"`)
	require.Contains(t, string(data), `"enabled": true`)
	require.Contains(t, string(data), `"auth_token": "secret-token"`)
	require.Contains(t, string(data), `"lease_seconds": 60`)
	require.NotContains(t, string(data), "remote_enabled")
	require.NotContains(t, string(data), "remote_auth_token")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/remote-env clear", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Remote Environment")
	require.False(t, app.Config.Future.RemoteEnabled)
	require.Equal(t, "", app.Config.Future.RemoteAuthToken)
	require.Equal(t, 0, app.Config.Future.RemoteLeaseSeconds)
	require.Empty(t, errOut.String())
}

func TestRemoteSetupCommandPersistsAndReports(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	tokenPath := filepath.Join(configHome, "remote-token")
	caPath := filepath.Join(configHome, "ca-bundle.crt")
	require.NoError(t, os.WriteFile(tokenPath, []byte("upstream-secret\n"), 0o600))
	t.Setenv("CLAUDE_CODE_REMOTE", "1")
	t.Setenv("CCR_UPSTREAM_PROXY_ENABLED", "true")
	t.Setenv("CLAUDE_CODE_REMOTE_SESSION_ID", "setup-session")
	t.Setenv("ANTHROPIC_BASE_URL", "https://remote.test")
	t.Setenv("CCR_SESSION_TOKEN_PATH", tokenPath)
	t.Setenv("CCR_CA_BUNDLE_PATH", caPath)
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.RemoteSetup([]string{"enable", "--addr", ":8799", "--auth-token", "secret-token", "--lease-seconds", "120", "--json"}, config.FlagOverrides{SessionID: "setup-session"}))
	require.Contains(t, out.String(), `"kind": "remote_setup"`)
	require.Contains(t, out.String(), `"enabled": true`)
	require.Contains(t, out.String(), `"ready": true`)
	require.Contains(t, out.String(), `"auth_token_configured": true`)
	require.Contains(t, out.String(), `"remote_url": "http://127.0.0.1:8799"`)
	require.Contains(t, out.String(), `"session_id": "setup-session"`)
	require.Contains(t, out.String(), `"upstream_proxy"`)
	require.Contains(t, out.String(), `"ready": true`)
	require.Contains(t, out.String(), `"proxy_url": "http://127.0.0.1:8799"`)
	require.NotContains(t, out.String(), "secret-token")
	require.NotContains(t, out.String(), "upstream-secret")
	var setupReport remoteSetupReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &setupReport))
	require.True(t, setupReport.Runtime.UpstreamProxy.Ready)
	require.Equal(t, "wss://remote.test/v1/code/upstreamproxy/ws", setupReport.Runtime.UpstreamProxy.WebSocketURL)
	require.True(t, app.Config.Future.RemoteEnabled)
	require.Equal(t, "secret-token", app.Config.Future.RemoteAuthToken)
	require.Equal(t, 120, app.Config.Future.RemoteLeaseSeconds)
	data, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"remote"`)
	require.Contains(t, string(data), `"enabled": true`)
	require.Contains(t, string(data), `"auth_token": "secret-token"`)
	require.Contains(t, string(data), `"lease_seconds": 120`)
	require.NotContains(t, string(data), "remote_enabled")
	require.NotContains(t, string(data), "remote_auth_token")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/remote-setup disable --addr 127.0.0.1:9999", &session.Session{ID: "active-session"}))
	require.Contains(t, out.String(), "Remote Setup")
	require.Contains(t, out.String(), "Enabled          false")
	require.Contains(t, out.String(), "Upstream proxy   true")
	require.Contains(t, out.String(), "127.0.0.1:9999")
	require.Contains(t, out.String(), "active-session")
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/web-setup status --addr 127.0.0.1:8888", &session.Session{ID: "web-session"}))
	require.Contains(t, out.String(), "Remote Setup")
	require.Contains(t, out.String(), "127.0.0.1:8888")
	require.Contains(t, out.String(), "web-session")
	require.Empty(t, errOut.String())
}

func TestRemoteCommandReportsAndPersistsSetup(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Remote([]string{"status", "--addr", ":8798", "--json"}))
	var status remoteSetupReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &status))
	require.Equal(t, "remote_setup", status.Kind)
	require.Equal(t, "status", status.Action)
	require.Equal(t, "disabled", status.Status)
	require.Equal(t, "http://127.0.0.1:8798", status.RemoteURL)
	out.Reset()

	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"--cwd", workspace,
			"--output-format", "json",
			"remote", "status", "--addr", ":8797",
		}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var cliStatus remoteSetupReport
	require.NoError(t, json.Unmarshal([]byte(cliOut), &cliStatus))
	require.Equal(t, "remote_setup", cliStatus.Kind)
	require.Equal(t, "status", cliStatus.Action)
	require.Equal(t, "disabled", cliStatus.Status)
	require.Equal(t, "http://127.0.0.1:8797", cliStatus.RemoteURL)

	require.NoError(t, app.Remote([]string{"enable", "--auth-token", "secret-token", "--lease-seconds", "33", "--json"}))
	var enabled remoteSetupReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &enabled))
	require.Equal(t, "enable", enabled.Action)
	require.Equal(t, "ready", enabled.Status)
	require.True(t, enabled.Enabled)
	require.True(t, enabled.AuthTokenConfigured)
	require.Equal(t, 33, enabled.LeaseSeconds)
	require.NotContains(t, out.String(), "secret-token")
	data, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"remote"`)
	require.Contains(t, string(data), `"enabled": true`)
	require.Contains(t, string(data), `"auth_token": "secret-token"`)
	require.Contains(t, string(data), `"lease_seconds": 33`)
	require.NotContains(t, string(data), "remote_enabled")
	require.NotContains(t, string(data), "remote_auth_token")
	out.Reset()

	require.ErrorContains(t, app.Remote([]string{"serve", "127.0.0.1:8798", "extra", "--json"}), "unexpected argument")
	var serveError cliErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &serveError))
	require.Equal(t, "unexpected_extra_args", serveError.ErrorKind)
	require.Equal(t, "remote serve", serveError.Command)
	require.Equal(t, []string{"extra"}, serveError.Args)
	require.Contains(t, serveError.Hint, "codog remote serve [ADDR]")
	out.Reset()

	require.ErrorContains(t, app.Remote([]string{"--json", "serve", "--addr", "127.0.0.1:8798", "extra"}), "unexpected argument")
	require.NoError(t, json.Unmarshal(out.Bytes(), &serveError))
	require.Equal(t, "unexpected_extra_args", serveError.ErrorKind)
	require.Equal(t, "remote serve", serveError.Command)
	require.Equal(t, []string{"extra"}, serveError.Args)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/remote status --addr 127.0.0.1:8801 --json", &session.Session{ID: "active-session"}))
	var slashStatus remoteSetupReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &slashStatus))
	require.Equal(t, "status", slashStatus.Action)
	require.Equal(t, "active-session", slashStatus.SessionID)
	require.Equal(t, "http://127.0.0.1:8801", slashStatus.RemoteURL)
	out.Reset()

	require.NoError(t, app.runResumedRemoteSlash([]string{"status", "--addr", "127.0.0.1:8802", "--json"}, config.FlagOverrides{Resume: "resumed-session"}, "json"))
	var resumedStatus remoteSetupReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &resumedStatus))
	require.Equal(t, "status", resumedStatus.Action)
	require.Equal(t, "resumed-session", resumedStatus.SessionID)
	require.Equal(t, "http://127.0.0.1:8802", resumedStatus.RemoteURL)
	out.Reset()

	executableShim := filepath.Join(t.TempDir(), "codog-shim")
	require.NoError(t, os.WriteFile(executableShim, []byte("#!/bin/sh\necho remote-shim \"$@\"\n"), 0o755))
	app.Executable = executableShim
	require.NoError(t, app.runResumedRemoteSlash([]string{"serve", "--addr", "127.0.0.1:0", "--json"}, config.FlagOverrides{Resume: "resumed-session"}, "json"))
	var remoteTask backgroundCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &remoteTask))
	require.Equal(t, "background", remoteTask.Kind)
	require.Equal(t, "run", remoteTask.Action)
	require.Equal(t, "ok", remoteTask.Status)
	require.Equal(t, "resumed-session", remoteTask.SessionID)
	require.NotEmpty(t, remoteTask.TaskID)
	require.NotNil(t, remoteTask.Task)
	require.Equal(t, "remote", remoteTask.Task.Kind)
	require.Equal(t, "resumed-session", remoteTask.Task.SessionID)
	require.Contains(t, remoteTask.Task.Command, "remote serve 127.0.0.1:0")
	require.NotContains(t, remoteTask.Task.Command, "--json")
	require.NotContains(t, remoteTask.Task.Command, "--output-format")
}
