package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/planmode"
	"github.com/stretchr/testify/require"
)

func TestReadFileRejectsWorkspaceEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"path": outside})
	_, err := ReadFileTool{Workspace: workspace}.Execute(context.Background(), input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workspace")
}

func TestFileToolsEnforceSizeLimits(t *testing.T) {
	workspace := t.TempDir()
	largeContent := strings.Repeat("a", int(maxFileToolBytes)+1)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "large.txt"), []byte(largeContent), 0o644))

	out, err := ReadFileTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"large.txt"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"truncated": true`)

	_, err = WriteFileTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"too-large.txt","content":"`+largeContent+`"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum file tool size")

	_, err = EditFileTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"large.txt","old_string":"a","new_string":"b"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum editable size")
}

func TestPowerShellToolExecutesForegroundAndBackground(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	script := filepath.Join(t.TempDir(), "pwsh-shim")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf 'ps:%s\\n' \"$*\"\n"), 0o755))
	tool := PowerShellTool{Workspace: workspace, ConfigHome: configHome, Executable: script}

	out, err := tool.Execute(context.Background(), []byte(`{"command":"Write-Output ok","timeout":5}`))
	require.NoError(t, err)
	require.Contains(t, out, `ps:-NoProfile -Command Write-Output ok`)
	require.Contains(t, out, `"exit_code": 0`)
	require.Contains(t, out, `"duration_ms":`)

	out, err = tool.Execute(context.Background(), []byte(`{"command":"Write-Output bg","run_in_background":true}`))
	require.NoError(t, err)
	require.Contains(t, out, `"background": true`)
	var payload struct {
		Task background.Task `json:"task"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.NotEmpty(t, payload.Task.ID)
	require.Eventually(t, func() bool {
		logs, err := background.NewStore(configHome).Logs(payload.Task.ID, 4096)
		return err == nil && strings.Contains(logs, `ps:-NoProfile -Command Write-Output bg`)
	}, 5*time.Second, 50*time.Millisecond)
}

func TestBashToolReportsExitCodeAndDuration(t *testing.T) {
	out, err := BashTool{Workspace: t.TempDir()}.Execute(context.Background(), []byte(`{"command":"printf ok; exit 7"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"stdout": "ok"`)
	require.Contains(t, out, `"exit_code": 7`)
	require.Contains(t, out, `"duration_ms":`)
	require.Contains(t, out, `"error": "exit status 7"`)
}

func TestFileToolsAllowAdditionalDirs(t *testing.T) {
	workspace := t.TempDir()
	extra := filepath.Join(t.TempDir(), "extra")
	require.NoError(t, os.MkdirAll(extra, 0o755))
	extraFile := filepath.Join(extra, "notes.txt")
	require.NoError(t, os.WriteFile(extraFile, []byte("alpha\nbeta\n"), 0o644))

	input, _ := json.Marshal(map[string]string{"path": extraFile})
	out, err := ReadFileTool{Workspace: workspace, AdditionalDirs: []string{extra}}.Execute(context.Background(), input)
	require.NoError(t, err)
	require.Contains(t, out, "alpha")

	writeInput, _ := json.Marshal(map[string]string{"path": filepath.Join(extra, "new", "created.txt"), "content": "created"})
	out, err = WriteFileTool{Workspace: workspace, AdditionalDirs: []string{extra}}.Execute(context.Background(), writeInput)
	require.NoError(t, err)
	require.Contains(t, out, "create")
	require.FileExists(t, filepath.Join(extra, "new", "created.txt"))

	grepInput, _ := json.Marshal(map[string]any{"pattern": "beta", "path": extra, "limit": 5})
	out, err = GrepTool{Workspace: workspace, AdditionalDirs: []string{extra}}.Execute(context.Background(), grepInput)
	require.NoError(t, err)
	require.Contains(t, out, extraFile)
}

func TestEditFileRequiresUniqueMatch(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "a.txt")
	if err := os.WriteFile(path, []byte("one\none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{
		"path":       "a.txt",
		"old_string": "one",
		"new_string": "two",
	})
	_, err := EditFileTool{Workspace: workspace}.Execute(context.Background(), input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "appears 2 times")
}

func TestMultiEditAppliesAtomically(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "a.txt")
	require.NoError(t, os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644))

	out, err := MultiEditTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"a.txt","edits":[{"old_string":"one","new_string":"1"},{"old_string":"two","new_string":"2"}]}`))
	require.NoError(t, err)
	require.Contains(t, out, `"replacements": 2`)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "1\n2\nthree\n", string(data))

	_, err = MultiEditTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"a.txt","edits":[{"old_string":"1","new_string":"one"},{"old_string":"missing","new_string":"x"}]}`))
	require.Error(t, err)
	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, "1\n2\nthree\n", string(data))
}

func TestPrompterRules(t *testing.T) {
	p := &Prompter{
		Mode:      PermissionAllow,
		DenyRules: []string{"bash:rm -rf"},
	}
	require.Error(t, p.Authorize("bash", PermissionDanger, []byte(`{"command":"rm -rf tmp"}`)))

	p = &Prompter{
		Mode:       PermissionReadOnly,
		AllowRules: []string{"bash:go test"},
	}
	require.NoError(t, p.Authorize("bash", PermissionDanger, []byte(`{"command":"go test ./..."}`)))

	p = &Prompter{
		Mode:        PermissionAllow,
		DeniedTools: []string{"bash"},
	}
	require.Error(t, p.Authorize("bash", PermissionDanger, []byte(`{"command":"pwd"}`)))
}

func TestPrompterEmitsDecision(t *testing.T) {
	var decision PermissionDecision
	p := &Prompter{
		Mode:       PermissionAllow,
		DenyRules:  []string{"bash:rm -rf"},
		OnDecision: func(next PermissionDecision) { decision = next },
	}
	require.Error(t, p.Authorize("bash", PermissionDanger, []byte(`{"command":"rm -rf tmp"}`)))
	require.Equal(t, "bash", decision.ToolName)
	require.False(t, decision.Allowed)
	require.Equal(t, "deny_rule", decision.Reason)
}

func TestPrompterBashValidation(t *testing.T) {
	var decision PermissionDecision
	p := &Prompter{
		Mode:       PermissionReadOnly,
		OnDecision: func(next PermissionDecision) { decision = next },
	}
	require.NoError(t, p.Authorize("bash", PermissionDanger, []byte(`{"command":"pwd"}`)))
	require.True(t, decision.Allowed)
	require.Equal(t, "bash_validation_read_only", decision.Reason)

	p = &Prompter{Mode: PermissionReadOnly}
	err := p.Authorize("bash", PermissionDanger, []byte(`{"command":"touch file.txt"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "bash validation")

	var prompt strings.Builder
	p = &Prompter{
		Mode: PermissionDanger,
		In:   strings.NewReader("n\n"),
		Err:  &prompt,
	}
	err = p.Authorize("bash", PermissionDanger, []byte(`{"command":"rm -rf tmp"}`))
	require.Error(t, err)
	require.Contains(t, prompt.String(), "Bash validation warning")

	p = &Prompter{Mode: PermissionAllow}
	require.NoError(t, p.Authorize("bash", PermissionDanger, []byte(`{"command":"rm -rf tmp"}`)))
}

func TestRegistryInfoReportsToolPermissionAndSchema(t *testing.T) {
	registry := NewRegistry(t.TempDir())

	info, ok := registry.Info("BASH")
	require.True(t, ok)
	require.Equal(t, "bash", info.Name)
	require.Equal(t, PermissionDanger, info.Permission)
	required, ok := info.InputSchema["required"].([]string)
	require.True(t, ok)
	require.Contains(t, required, "command")

	infos := registry.Infos()
	require.Len(t, infos, 68)
	info, ok = registry.Info("bash")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("powershell")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("Read")
	require.True(t, ok)
	require.Equal(t, "read_file", info.Name)
	info, ok = registry.Info("TodoWrite")
	require.True(t, ok)
	require.Equal(t, "todo_write", info.Name)
	require.True(t, registry.Has("MultiEdit"))
	_, ok = registry.Info("ask_user_question")
	require.True(t, ok)
	_, ok = registry.Info("notebook_edit")
	require.True(t, ok)
	info, ok = registry.Info("lsp")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("enter_worktree")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("exit_worktree")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("agent")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("cron_create")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("cron_delete")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("cron_list")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("team_create")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	for _, name := range []string{"team_list", "team_get"} {
		info, ok = registry.Info(name)
		require.True(t, ok)
		require.Equal(t, PermissionReadOnly, info.Permission)
	}
	info, ok = registry.Info("team_delete")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	for _, name := range []string{"worker_create", "worker_resolve_trust", "worker_send_prompt", "worker_restart", "worker_terminate"} {
		info, ok = registry.Info(name)
		require.True(t, ok)
		require.Equal(t, PermissionDanger, info.Permission)
	}
	for _, name := range []string{"worker_list", "worker_get", "worker_observe", "worker_await_ready", "worker_observe_completion"} {
		info, ok = registry.Info(name)
		require.True(t, ok)
		require.Equal(t, PermissionReadOnly, info.Permission)
	}
	_, ok = registry.Info("multi_edit")
	require.True(t, ok)
	_, ok = registry.Info("task_create")
	require.True(t, ok)
	info, ok = registry.Info("run_task_packet")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("task_get")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	_, ok = registry.Info("task_output")
	require.True(t, ok)
	info, ok = registry.Info("task_supervise")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("task_update")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	_, ok = registry.Info("web_fetch")
	require.True(t, ok)
	_, ok = registry.Info("web_search")
	require.True(t, ok)
	_, ok = registry.Info("tool_search")
	require.True(t, ok)
	info, ok = registry.Info("brief")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("send_user_message")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("structured_output")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("sleep")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("repl")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("remote_trigger")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("testing_permission")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("skill")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("config")
	require.True(t, ok)
	require.Equal(t, PermissionWorkspace, info.Permission)
	info, ok = registry.Info("mcp")
	require.True(t, ok)
	require.Equal(t, PermissionWorkspace, info.Permission)
	info, ok = registry.Info("mcp_auth")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	for _, name := range []string{"git_status", "git_diff", "git_log", "git_show", "git_blame"} {
		info, ok = registry.Info(name)
		require.True(t, ok)
		require.Equal(t, PermissionReadOnly, info.Permission)
	}
	info, ok = registry.Info("enter_plan_mode")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("exit_plan_mode")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("list_mcp_resources")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("read_mcp_resource")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("list_mcp_resource_templates")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("list_mcp_prompts")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("get_mcp_prompt")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
}

func TestRegistryExecutesClaudeToolAliases(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("alpha\n"), 0o644))
	registry := NewRegistry(workspace)

	out, err := registry.Execute(context.Background(), "Read", []byte(`{"path":"notes.txt"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, "alpha")

	out, err = registry.Execute(context.Background(), "Bash", []byte(`{"command":"printf alias-ok"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"stdout": "alias-ok"`)
}

func TestTodoToolsReadAndWriteWorkspaceTodos(t *testing.T) {
	workspace := t.TempDir()
	writeOut, err := TodoWriteTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"todos":[{"content":"write tests","status":"pending","priority":"high"}]}`))
	require.NoError(t, err)
	require.Contains(t, writeOut, `"kind": "todos"`)
	require.Contains(t, writeOut, `"total": 1`)

	readOut, err := TodoReadTool{Workspace: workspace}.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	require.Contains(t, readOut, "write tests")

	registry := NewRegistry(workspace)
	info, ok := registry.Info("todo_write")
	require.True(t, ok)
	require.Equal(t, PermissionWorkspace, info.Permission)
	info, ok = registry.Info("todo_read")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
}

func TestWebToolsFetchAndSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><head><title>Local</title></head><body><p>Hello web tool.</p></body></html>`)
		case "/search":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<a class="result__a" href="https://example.com/result">Example Result</a><div class="result__snippet">A local search summary.</div>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("CODOG_WEB_SEARCH_BASE_URL", server.URL+"/search")

	fetchOut, err := WebFetchTool{}.Execute(context.Background(), []byte(`{"url":"`+server.URL+`/page","prompt":"title"}`))
	require.NoError(t, err)
	require.Contains(t, fetchOut, `"title": "Local"`)
	require.Contains(t, fetchOut, `"summary": "Title: Local"`)

	searchOut, err := WebSearchTool{}.Execute(context.Background(), []byte(`{"query":"local result"}`))
	require.NoError(t, err)
	require.Contains(t, searchOut, `"title": "Example Result"`)
	require.Contains(t, searchOut, `"url": "https://example.com/result"`)
	require.Contains(t, searchOut, `"snippet": "A local search summary."`)

	registry := NewRegistry(t.TempDir())
	info, ok := registry.Info("web_fetch")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("web_search")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
}

func TestRemoteTriggerToolCallsWebhook(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/large" {
			fmt.Fprint(w, "abcdef")
			return
		}
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "token", r.Header.Get("x-test"))
		data, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, "payload", string(data))
		w.Header().Set("x-result", "ok")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer server.Close()

	out, err := RemoteTriggerTool{}.Execute(context.Background(), []byte(`{"url":"`+server.URL+`","method":"POST","headers":{"x-test":"token"},"body":"payload"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"status_code": 200`)
	require.Contains(t, out, `"body": "{\"ok\":true}"`)
	require.Contains(t, out, `"X-Result": [`)
	require.Contains(t, out, `"truncated": false`)

	out, err = RemoteTriggerTool{}.Execute(context.Background(), []byte(`{"url":"`+server.URL+`/large","max_bytes":3}`))
	require.NoError(t, err)
	require.Contains(t, out, `"body": "abc"`)
	require.Contains(t, out, `"bytes": 3`)
	require.Contains(t, out, `"truncated": true`)

	_, err = RemoteTriggerTool{}.Execute(context.Background(), []byte(`{"url":"file:///etc/passwd"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "http or https")
}

func TestTestingPermissionToolReturnsReceipt(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	prompter := &Prompter{Mode: PermissionReadOnly}

	out, err := registry.Execute(context.Background(), "testing_permission", []byte(`{"target_tool":"bash","input":{"command":"pwd"}}`), prompter)
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "permission_check"`)
	require.Contains(t, out, `"target_tool": "bash"`)
	require.Contains(t, out, `"allowed": true`)
	require.Contains(t, out, `"reason": "bash_validation_read_only"`)

	out, err = registry.Execute(context.Background(), "testing_permission", []byte(`{"target_tool":"bash","input":{"command":"pwd && touch created.txt"}}`), prompter)
	require.NoError(t, err)
	require.Contains(t, out, `"allowed": false`)
	require.Contains(t, out, `"reason": "bash_validation"`)
	require.Contains(t, out, `"message": "bash command is not read-only"`)

	prompter = &Prompter{Mode: PermissionAllow, DeniedTools: []string{"write_file"}}
	out, err = registry.Execute(context.Background(), "testing_permission", []byte(`{"target_tool":"write_file","input":{"path":"a.txt","content":"x"}}`), prompter)
	require.NoError(t, err)
	require.Contains(t, out, `"known_tool": true`)
	require.Contains(t, out, `"required_permission": "workspace-write"`)
	require.Contains(t, out, `"allowed": false`)
	require.Contains(t, out, `"reason": "denied_tools"`)

	_, err = TestingPermissionTool{}.Execute(context.Background(), []byte(`{"target_tool":"bash"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "through the tool registry")
}

func TestNotebookEditToolUpdatesNotebook(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "analysis.ipynb")
	require.NoError(t, os.WriteFile(path, []byte(`{"metadata":{"name":"kept"},"cells":[]}`), 0o644))

	out, err := NotebookEditTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"notebook_path":"analysis.ipynb","cell_index":0,"cell_type":"markdown","new_source":"# Title"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"cell_type": "markdown"`)
	require.Contains(t, out, `"cell_count": 1`)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"name": "kept"`)
	require.Contains(t, string(data), "# Title")

	registry := NewRegistry(workspace)
	info, ok := registry.Info("notebook_edit")
	require.True(t, ok)
	require.Equal(t, PermissionWorkspace, info.Permission)
}

func TestLSPToolQueriesCodeIntel(t *testing.T) {
	workspace := t.TempDir()
	source := strings.Join([]string{
		"package demo",
		"",
		"type Widget struct{}",
		"",
		"func BuildWidget() Widget {",
		"	return Widget{}",
		"}",
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "demo.go"), []byte(source), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "messy.go"), []byte("package demo\n\nfunc messy(){return}\n"), 0o644))
	tool := LSPTool{Workspace: workspace}

	symbolsOut, err := tool.Execute(context.Background(), []byte(`{"action":"symbols","path":"demo.go"}`))
	require.NoError(t, err)
	require.Contains(t, symbolsOut, `"action": "symbols"`)
	require.Contains(t, symbolsOut, "BuildWidget")

	documentSymbolsOut, err := tool.Execute(context.Background(), []byte(`{"action":"document_symbols","path":"demo.go"}`))
	require.NoError(t, err)
	require.Contains(t, documentSymbolsOut, `"action": "symbols"`)
	require.Contains(t, documentSymbolsOut, "BuildWidget")

	definitionOut, err := tool.Execute(context.Background(), []byte(`{"action":"definition","query":"Widget"}`))
	require.NoError(t, err)
	require.Contains(t, definitionOut, `"found": true`)
	require.Contains(t, definitionOut, `"name": "Widget"`)

	gotoDefinitionOut, err := tool.Execute(context.Background(), []byte(`{"action":"goto_definition","query":"Widget"}`))
	require.NoError(t, err)
	require.Contains(t, gotoDefinitionOut, `"action": "definition"`)
	require.Contains(t, gotoDefinitionOut, `"found": true`)

	languageFallbackOut, err := tool.Execute(context.Background(), []byte(`{"action":"definition","query":"Widget","language":"go"}`))
	require.NoError(t, err)
	require.Contains(t, languageFallbackOut, `"action": "definition"`)
	require.Contains(t, languageFallbackOut, `"found": true`)

	_, err = tool.Execute(context.Background(), []byte(`{"action":"hover","path":"demo.go","line":4,"character":6,"use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	hoverOut, err := tool.Execute(context.Background(), []byte(`{"action":"hover","path":"demo.go","line":4,"character":6}`))
	require.NoError(t, err)
	require.Contains(t, hoverOut, `"query": "BuildWidget"`)
	require.Contains(t, hoverOut, `"found": true`)

	completionOut, err := tool.Execute(context.Background(), []byte(`{"action":"completion","query":"Build","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, completionOut, `"action": "completion"`)
	require.Contains(t, completionOut, `"label": "BuildWidget"`)

	formatOut, err := tool.Execute(context.Background(), []byte(`{"action":"format","path":"messy.go"}`))
	require.NoError(t, err)
	require.Contains(t, formatOut, `"action": "format"`)
	require.Contains(t, formatOut, `"changed": true`)
	require.Contains(t, formatOut, `func messy()`)
}

func TestWorktreeToolsAllocateAndRemove(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	workspace := t.TempDir()
	runToolTestGit(t, workspace, "init", "-q")
	runToolTestGit(t, workspace, "config", "user.email", "codog@example.test")
	runToolTestGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hello\n"), 0o644))
	runToolTestGit(t, workspace, "add", "README.md")
	runToolTestGit(t, workspace, "commit", "-q", "-m", "init")

	enterOut, err := EnterWorktreeTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"name":"reviewer"}`))
	require.NoError(t, err)
	require.Contains(t, enterOut, `"operation": "enter"`)
	var payload struct {
		Allocation struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"allocation"`
	}
	require.NoError(t, json.Unmarshal([]byte(enterOut), &payload))
	require.NotEmpty(t, payload.Allocation.ID)
	require.FileExists(t, filepath.Join(payload.Allocation.Path, "README.md"))

	exitOut, err := ExitWorktreeTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"id":"`+payload.Allocation.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, exitOut, `"removed": true`)
	require.NoDirExists(t, payload.Allocation.Path)
}

func TestGitToolsReadRepositoryState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	workspace := t.TempDir()
	runToolTestGit(t, workspace, "init", "-q")
	runToolTestGit(t, workspace, "config", "user.email", "codog@example.test")
	runToolTestGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("alpha\n"), 0o644))
	runToolTestGit(t, workspace, "add", "notes.txt")
	runToolTestGit(t, workspace, "commit", "-q", "-m", "initial notes")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("alpha\nbeta\n"), 0o644))

	statusOut, err := GitStatusTool{Workspace: workspace}.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	require.Contains(t, statusOut, `"output"`)
	require.Contains(t, statusOut, "notes.txt")

	diffOut, err := GitDiffTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"notes.txt"}`))
	require.NoError(t, err)
	require.Contains(t, diffOut, "+beta")

	logOut, err := GitLogTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"count":1,"oneline":true}`))
	require.NoError(t, err)
	require.Contains(t, logOut, "initial notes")

	showOut, err := GitShowTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"commit":"HEAD","format":"metadata"}`))
	require.NoError(t, err)
	require.Contains(t, showOut, "initial notes")

	blameOut, err := GitBlameTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"notes.txt","start_line":1,"end_line":1}`))
	require.NoError(t, err)
	require.Contains(t, blameOut, "alpha")

	_, err = GitDiffTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"../outside.txt"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workspace")

	_, err = GitShowTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"commit":"--help"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "safe git ref")
}

func runToolTestGit(t *testing.T, workspace string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestPlanModeToolsEnterAndExit(t *testing.T) {
	workspace := t.TempDir()
	enterTool := EnterPlanModeTool{Workspace: workspace}
	exitTool := ExitPlanModeTool{Workspace: workspace}

	require.Equal(t, PermissionReadOnly, enterTool.Permission())
	require.Equal(t, PermissionReadOnly, exitTool.Permission())

	enterOut, err := enterTool.Execute(context.Background(), []byte(`{"plan":"inspect first"}`))
	require.NoError(t, err)
	require.Contains(t, enterOut, `"action": "enter"`)
	require.Contains(t, enterOut, `"status": "active"`)
	require.Contains(t, enterOut, "inspect first")

	state, err := planmode.Load(workspace)
	require.NoError(t, err)
	require.True(t, state.Active)
	require.Equal(t, "inspect first", state.Plan)

	exitOut, err := exitTool.Execute(context.Background(), []byte(`{"plan":"ship final plan"}`))
	require.NoError(t, err)
	require.Contains(t, exitOut, `"action": "exit"`)
	require.Contains(t, exitOut, `"status": "inactive"`)
	require.Contains(t, exitOut, "ship final plan")

	state, err = planmode.Load(workspace)
	require.NoError(t, err)
	require.False(t, state.Active)
	require.Equal(t, "ship final plan", state.Plan)
}

func TestAgentToolLaunchesBackgroundAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "agents", "reviewer.json"), []byte(`{"name":"reviewer","model":"agent-model","prompt":"Base review instructions"}`), 0o644))
	script := filepath.Join(t.TempDir(), "agent-shim")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755))

	out, err := AgentTool{Workspace: workspace, ConfigHome: configHome, Executable: script}.Execute(context.Background(), []byte(`{"description":"review code","prompt":"check auth flow","subagent_type":"reviewer","session_id":"session-1"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "agent"`)
	require.Contains(t, out, `"agent": "reviewer"`)
	var payload struct {
		Task background.Task `json:"task"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.NotEmpty(t, payload.Task.ID)
	require.Equal(t, "agent", payload.Task.Kind)
	require.Equal(t, "session-1", payload.Task.SessionID)

	store := background.NewStore(configHome)
	require.Eventually(t, func() bool {
		logs, err := store.Logs(payload.Task.ID, 4096)
		return err == nil && strings.Contains(logs, "agent-model") && strings.Contains(logs, "Base review instructions") && strings.Contains(logs, "check auth flow")
	}, 5*time.Second, 50*time.Millisecond)
}

func TestCronToolsCreateListAndDeleteEntries(t *testing.T) {
	configHome := t.TempDir()

	createOut, err := CronCreateTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"schedule":"0 9 * * 1","prompt":"review weekly status","description":"weekly review"}`))
	require.NoError(t, err)
	require.Contains(t, createOut, `"schedule": "0 9 * * 1"`)
	require.Contains(t, createOut, `"prompt": "review weekly status"`)
	var entry struct {
		ID string `json:"cron_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(createOut), &entry))
	require.NotEmpty(t, entry.ID)

	listOut, err := CronListTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	require.Contains(t, listOut, `"count": 1`)
	require.Contains(t, listOut, entry.ID)

	deleteOut, err := CronDeleteTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"cron_id":"`+entry.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, deleteOut, `"status": "deleted"`)
	listOut, err = CronListTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	require.Contains(t, listOut, `"count": 0`)
}

func TestTeamToolsCreateAndDeleteBackgroundTasks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	script := filepath.Join(t.TempDir(), "team-shim")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755))

	createOut, err := TeamCreateTool{Workspace: workspace, ConfigHome: configHome, Executable: script}.Execute(context.Background(), []byte(`{"name":"review","session_id":"session-1","tasks":[{"description":"auth","prompt":"check auth"},{"prompt":"check tests"}]}`))
	require.NoError(t, err)
	require.Contains(t, createOut, `"name": "review"`)
	require.Contains(t, createOut, `"task_count": 2`)
	var created struct {
		ID      string   `json:"team_id"`
		TaskIDs []string `json:"task_ids"`
	}
	require.NoError(t, json.Unmarshal([]byte(createOut), &created))
	require.NotEmpty(t, created.ID)
	require.Len(t, created.TaskIDs, 2)

	store := background.NewStore(configHome)
	require.Eventually(t, func() bool {
		logs, err := store.Logs(created.TaskIDs[0], 4096)
		return err == nil && strings.Contains(logs, "Task: auth") && strings.Contains(logs, "check auth")
	}, 5*time.Second, 50*time.Millisecond)

	listOut, err := TeamListTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"status":"running"}`))
	require.NoError(t, err)
	require.Contains(t, listOut, `"kind": "team_list"`)
	require.Contains(t, listOut, `"total": 1`)
	require.Contains(t, listOut, created.ID)
	require.Contains(t, listOut, `"task_statuses": [`)

	getOut, err := TeamGetTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"team_id":"`+created.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, getOut, `"kind": "team"`)
	require.Contains(t, getOut, `"tasks": [`)
	require.Contains(t, getOut, created.TaskIDs[0])

	deleteOut, err := TeamDeleteTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"team_id":"`+created.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, deleteOut, `"status": "deleted"`)
	require.Contains(t, deleteOut, `"message": "Team deleted"`)
}

func TestToolSearchToolFindsRegisteredTools(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	out, err := ToolSearchTool{Registry: registry}.Execute(context.Background(), []byte(`{"query":"web fetch","max_results":3}`))
	require.NoError(t, err)
	require.Contains(t, out, `"query": "web fetch"`)
	require.Contains(t, out, `"name": "web_fetch"`)
	require.NotContains(t, out, `"name": "write_file"`)

	info, ok := registry.Info("tool_search")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
}

func TestAskUserQuestionToolReadsChoiceAndDefault(t *testing.T) {
	var out strings.Builder
	tool := AskUserQuestionTool{
		In:  strings.NewReader("2\n"),
		Out: &out,
	}
	result, err := tool.Execute(context.Background(), []byte(`{"question":"Pick one","choices":["alpha","beta"],"default":"alpha"}`))
	require.NoError(t, err)
	require.Contains(t, out.String(), "Pick one")
	require.Contains(t, out.String(), "2. beta")
	require.Contains(t, result, `"answer": "beta"`)

	out.Reset()
	tool.In = strings.NewReader("\n")
	result, err = tool.Execute(context.Background(), []byte(`{"question":"Continue?","default":"yes"}`))
	require.NoError(t, err)
	require.Contains(t, result, `"answer": "yes"`)
}

func TestBriefToolReturnsAttachmentMetadata(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "image.png"), []byte("png"), 0o644))

	out, err := BriefTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"message":"Review ready","status":"normal","attachments":["image.png"]}`))
	require.NoError(t, err)
	require.Contains(t, out, `"message": "Review ready"`)
	require.Contains(t, out, `"status": "normal"`)
	require.Contains(t, out, `"is_image": true`)
	require.Contains(t, out, `"size": 3`)

	out, err = SendUserMessageTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"message":"Heads up","status":"proactive","attachments":["image.png"]}`))
	require.NoError(t, err)
	require.Contains(t, out, `"message": "Heads up"`)
	require.Contains(t, out, `"status": "proactive"`)
	require.Contains(t, out, `"is_image": true`)
}

func TestStructuredOutputToolReturnsPayload(t *testing.T) {
	out, err := StructuredOutputTool{}.Execute(context.Background(), []byte(`{"ok":true,"items":[1,2,3]}`))
	require.NoError(t, err)
	require.Contains(t, out, `"data": "Structured output provided successfully"`)
	require.Contains(t, out, `"ok": true`)

	_, err = StructuredOutputTool{}.Execute(context.Background(), []byte(`{}`))
	require.Error(t, err)
}

func TestSleepToolWaitsAndReportsDuration(t *testing.T) {
	out, err := SleepTool{}.Execute(context.Background(), []byte(`{"duration_ms":1}`))
	require.NoError(t, err)
	require.Contains(t, out, `"duration_ms": 1`)
	require.Contains(t, out, "Slept for 1ms")
}

func TestREPLToolExecutesShellCode(t *testing.T) {
	out, err := REPLTool{Workspace: t.TempDir()}.Execute(context.Background(), []byte(`{"language":"sh","code":"printf repl-ok","timeout_ms":1000}`))
	require.NoError(t, err)
	require.Contains(t, out, `"stdout": "repl-ok"`)
	require.Contains(t, out, `"exit_code": 0`)

	_, err = REPLTool{Workspace: t.TempDir()}.Execute(context.Background(), []byte(`{"language":"unknown","code":"x"}`))
	require.Error(t, err)
}

func TestSkillToolLoadsAndRendersSkill(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "skills", "review.md"), []byte("Review skill body"), 0o644))

	out, err := SkillTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"skill":"review","args":"check auth"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "skill"`)
	require.Contains(t, out, `"skill": "review"`)
	require.Contains(t, out, "Review skill body")
	require.Contains(t, out, "User request: check auth")
}

func TestConfigToolGetsAndSetsUserConfig(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(configHome, 0o755))
	configPath := filepath.Join(configHome, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"model":"old-model","api_key":"secret","future":{"sandbox_strategy":"detect"}}`), 0o644))
	tool := ConfigTool{Workspace: workspace, ConfigHome: configHome}

	getOut, err := tool.Execute(context.Background(), []byte(`{"setting":"model"}`))
	require.NoError(t, err)
	require.Contains(t, getOut, `"operation": "get"`)
	require.Contains(t, getOut, `"value": "old-model"`)

	secretOut, err := tool.Execute(context.Background(), []byte(`{"setting":"api_key"}`))
	require.NoError(t, err)
	require.Contains(t, secretOut, `[redacted]`)
	require.NotContains(t, secretOut, `secret`)

	setOut, err := tool.Execute(context.Background(), []byte(`{"setting":"future.sandbox_strategy","value":"sandbox-exec"}`))
	require.NoError(t, err)
	require.Contains(t, setOut, `"operation": "set"`)
	require.Contains(t, setOut, `"previous_value": "detect"`)
	require.Contains(t, setOut, `"new_value": "sandbox-exec"`)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"sandbox_strategy": "sandbox-exec"`)
}

func TestTaskToolsManageBackgroundTasks(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	createOut, err := TaskCreateTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"command":"printf task-output","kind":"test","session_id":"session-1"}`))
	require.NoError(t, err)
	var task background.Task
	require.NoError(t, json.Unmarshal([]byte(createOut), &task))
	require.NotEmpty(t, task.ID)

	var completed background.Task
	require.Eventually(t, func() bool {
		statusOut, err := TaskStatusTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"id":"`+task.ID+`"}`))
		if err != nil {
			return false
		}
		if err := json.Unmarshal([]byte(statusOut), &completed); err != nil {
			return false
		}
		return completed.Status != "running" && completed.ExitCode != nil
	}, 2*time.Second, 20*time.Millisecond)
	require.NotNil(t, completed.ExitCode)
	require.Equal(t, 0, *completed.ExitCode)

	outputOut, err := TaskOutputTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"id":"`+task.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, outputOut, "task-output")
	require.Contains(t, outputOut, `"status": "completed"`)
	require.Contains(t, outputOut, `"exit_code": 0`)

	updateOut, err := TaskUpdateTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"task_id":"`+task.ID+`","message":"review logs"}`))
	require.NoError(t, err)
	require.Contains(t, updateOut, `"message_count": 1`)
	require.Contains(t, updateOut, `"last_message": "review logs"`)

	getOut, err := TaskGetTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"task_id":"`+task.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, getOut, `"messages": [`)
	require.Contains(t, getOut, "review logs")

	listOut, err := TaskListTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"session_id":"session-1","kind":"test"}`))
	require.NoError(t, err)
	require.Contains(t, listOut, task.ID)
	require.Contains(t, listOut, `"total": 1`)

	stopOut, err := TaskStopTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"id":"`+task.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, stopOut, task.ID)

	registry := NewRegistryWithOptions(workspace, RegistryOptions{ConfigHome: configHome})
	info, ok := registry.Info("task_create")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
}

func TestTaskSuperviseToolRestartsEligibleTasks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	createOut, err := TaskCreateTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"command":"printf failed && exit 2","kind":"test","restart_policy":{"enabled":true,"mode":"on-failure","max_attempts":1}}`))
	require.NoError(t, err)
	var task background.Task
	require.NoError(t, json.Unmarshal([]byte(createOut), &task))
	require.NotNil(t, task.RestartPolicy)
	require.True(t, task.RestartPolicy.Enabled)

	store := background.NewStore(configHome)
	require.Eventually(t, func() bool {
		status, err := store.Status(task.ID)
		return err == nil && status.Status == "failed"
	}, 2*time.Second, 20*time.Millisecond)

	superviseOut, err := TaskSuperviseTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	var result background.SuperviseResult
	require.NoError(t, json.Unmarshal([]byte(superviseOut), &result))
	require.Len(t, result.Restarted, 1)
	require.Equal(t, task.ID, result.Restarted[0].RestartedFrom)
	require.Equal(t, 1, result.Restarted[0].RestartCount)
}

func TestRunTaskPacketToolCreatesPromptTask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	script := filepath.Join(t.TempDir(), "codog-shim")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf 'shim:%s\\n' \"$*\"\n"), 0o755))

	out, err := RunTaskPacketTool{Workspace: workspace, ConfigHome: configHome, Executable: script}.Execute(context.Background(), []byte(`{
		"objective":"Update docs",
		"scope":"README only",
		"repo":"codog",
		"branch_policy":"use main",
		"acceptance_tests":["go test ./..."],
		"commit_policy":"commit changes",
		"reporting_contract":"summarize result",
		"escalation_policy":"ask if blocked"
	}`))
	require.NoError(t, err)
	require.Contains(t, out, `"task_packet": {`)
	require.Contains(t, out, `"objective": "Update docs"`)
	var payload struct {
		TaskID string `json:"task_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.NotEmpty(t, payload.TaskID)
	require.Eventually(t, func() bool {
		logs, err := background.NewStore(configHome).Logs(payload.TaskID, 4096)
		return err == nil && strings.Contains(logs, "shim:prompt") && strings.Contains(logs, "Update docs")
	}, 5*time.Second, 50*time.Millisecond)
}

func TestWorkerToolsManagePromptWorker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	script := filepath.Join(t.TempDir(), "codog-shim")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf 'worker:%s\\n' \"$*\"\n"), 0o755))

	createOut, err := WorkerCreateTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"cwd":".","trusted_roots":["."],"auto_recover_prompt_misdelivery":false}`))
	require.NoError(t, err)
	var created struct {
		WorkerID string `json:"worker_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(createOut), &created))
	require.NotEmpty(t, created.WorkerID)

	listOut, err := WorkerListTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"status":"ready_for_prompt"}`))
	require.NoError(t, err)
	require.Contains(t, listOut, `"kind": "worker_list"`)
	require.Contains(t, listOut, `"total": 1`)
	require.Contains(t, listOut, created.WorkerID)

	readyOut, err := WorkerAwaitReadyTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`"}`))
	require.NoError(t, err)
	require.Contains(t, readyOut, `"ready_for_prompt": true`)

	observeOut, err := WorkerObserveTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`","screen_text":"trust this folder?"}`))
	require.NoError(t, err)
	require.Contains(t, observeOut, `"status": "trust_prompt"`)

	resolveOut, err := WorkerResolveTrustTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`"}`))
	require.NoError(t, err)
	require.Contains(t, resolveOut, `"ready_for_prompt": true`)

	sendOut, err := WorkerSendPromptTool{Workspace: workspace, ConfigHome: configHome, Executable: script}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`","prompt":"implement worker tests","task_receipt":{"repo":"codog","task_kind":"test","source_surface":"tool","objective_preview":"implement worker tests"}}`))
	require.NoError(t, err)
	require.Contains(t, sendOut, `"status": "running"`)
	var sent struct {
		TaskID string `json:"task_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(sendOut), &sent))
	require.NotEmpty(t, sent.TaskID)
	require.Eventually(t, func() bool {
		logs, err := background.NewStore(configHome).Logs(sent.TaskID, 4096)
		return err == nil && strings.Contains(logs, "worker:prompt") && strings.Contains(logs, "implement worker tests")
	}, 5*time.Second, 50*time.Millisecond)

	getOut, err := WorkerGetTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`"}`))
	require.NoError(t, err)
	require.Contains(t, getOut, sent.TaskID)
	require.Contains(t, getOut, `"task_status":`)

	runningListOut, err := WorkerListTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"status":"running"}`))
	require.NoError(t, err)
	require.Contains(t, runningListOut, sent.TaskID)
	require.Contains(t, runningListOut, `"total": 1`)

	restartOut, err := WorkerRestartTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`"}`))
	require.NoError(t, err)
	require.Contains(t, restartOut, `"status": "running"`)

	completeOut, err := WorkerObserveCompletionTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`","finish_reason":"stop","tokens_output":12}`))
	require.NoError(t, err)
	require.Contains(t, completeOut, `"status": "finished"`)

	terminateOut, err := WorkerTerminateTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`"}`))
	require.NoError(t, err)
	require.Contains(t, terminateOut, `"status": "terminated"`)
}

func TestCommandToolExecutesWithJSONStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX cat")
	}
	out, err := CommandTool{
		Name:      "echo_json",
		Command:   "cat",
		Workspace: t.TempDir(),
	}.Execute(context.Background(), []byte(`{"ok":true}`))
	require.NoError(t, err)
	require.Contains(t, out, `ok`)
}

func TestBashToolRejectsUnavailableSandbox(t *testing.T) {
	_, err := BashTool{
		Workspace:       t.TempDir(),
		SandboxStrategy: "codog-missing-sandbox",
	}.Execute(context.Background(), []byte(`{"command":"pwd"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "not available")
}

func TestMCPToolCallsRemoteTool(t *testing.T) {
	server := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPToolHelperProcess"},
		Env:     []string{"CODOG_MCP_TOOL_HELPER=1"},
	}
	out, err := MCPTool{
		Name:       NewMCPToolName("test server", "echo"),
		ServerName: "test server",
		Server:     server,
		RemoteName: "echo",
	}.Execute(context.Background(), []byte(`{"text":"hi"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"text":"echo"`)

	out, err = MCPDispatchTool{Servers: map[string]config.MCPServerConfig{"test": server}}.Execute(context.Background(), []byte(`{"server":"test","tool":"echo","arguments":{"text":"hi"}}`))
	require.NoError(t, err)
	require.Contains(t, out, `"text":"echo"`)

	_, err = MCPDispatchTool{Servers: map[string]config.MCPServerConfig{"test": server}}.Execute(context.Background(), []byte(`{"server":"missing","tool":"echo"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown MCP server")

	authOut, err := MCPAuthTool{Servers: map[string]config.MCPServerConfig{"test": server}}.Execute(context.Background(), []byte(`{"server":"test"}`))
	require.NoError(t, err)
	require.Contains(t, authOut, `"status": "ok"`)
	require.Contains(t, authOut, `"tool_count": 1`)

	authOut, err = MCPAuthTool{Servers: map[string]config.MCPServerConfig{"test": server}}.Execute(context.Background(), []byte(`{"server":"missing"}`))
	require.NoError(t, err)
	require.Contains(t, authOut, `"status": "unknown"`)
}

func TestMCPResourceToolsListAndReadRemoteResources(t *testing.T) {
	servers := map[string]config.MCPServerConfig{
		"test": {
			Command: os.Args[0],
			Args:    []string{"-test.run=TestMCPToolHelperProcess"},
			Env:     []string{"CODOG_MCP_TOOL_HELPER=1"},
		},
	}

	listAllOut, err := ListMCPResourcesTool{Servers: servers}.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	require.Contains(t, listAllOut, `"kind": "mcp_resources"`)
	require.Contains(t, listAllOut, `"server": "test"`)
	require.Contains(t, listAllOut, "codog://note")

	listOut, err := ListMCPResourcesTool{Servers: servers}.Execute(context.Background(), []byte(`{"server":"test"}`))
	require.NoError(t, err)
	require.Contains(t, listOut, `"server": "test"`)
	require.Contains(t, listOut, "codog://note")

	readOut, err := ReadMCPResourceTool{Servers: servers}.Execute(context.Background(), []byte(`{"server":"test","uri":"codog://note"}`))
	require.NoError(t, err)
	require.Contains(t, readOut, `"uri": "codog://note"`)
	require.Contains(t, readOut, "note body")

	_, err = ReadMCPResourceTool{Servers: servers}.Execute(context.Background(), []byte(`{"server":"missing","uri":"codog://note"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown MCP server")
}

func TestMCPPromptAndTemplateTools(t *testing.T) {
	servers := map[string]config.MCPServerConfig{
		"test": {
			Command: os.Args[0],
			Args:    []string{"-test.run=TestMCPToolHelperProcess"},
			Env:     []string{"CODOG_MCP_TOOL_HELPER=1"},
		},
	}

	templatesOut, err := ListMCPResourceTemplatesTool{Servers: servers}.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	require.Contains(t, templatesOut, `"kind": "mcp_resource_templates"`)
	require.Contains(t, templatesOut, `"uriTemplate": "codog://{name}"`)

	promptsOut, err := ListMCPPromptsTool{Servers: servers}.Execute(context.Background(), []byte(`{"server":"test"}`))
	require.NoError(t, err)
	require.Contains(t, promptsOut, `"name": "review"`)

	promptOut, err := GetMCPPromptTool{Servers: servers}.Execute(context.Background(), []byte(`{"server":"test","prompt":"review","arguments":{"topic":"tools"}}`))
	require.NoError(t, err)
	require.Contains(t, promptOut, `"prompt": "review"`)
	require.Contains(t, promptOut, `"Review tools"`)
}

func TestMCPToolHelperProcess(t *testing.T) {
	if os.Getenv("CODOG_MCP_TOOL_HELPER") != "1" {
		return
	}
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		var req map[string]any
		if err := json.Unmarshal([]byte(reader.Text()), &req); err != nil {
			continue
		}
		method, _ := req["method"].(string)
		id := req["id"]
		switch method {
		case "initialize":
			writeMCPResponse(id, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "test", "version": "0.0.0"},
			})
		case "tools/list":
			writeMCPResponse(id, map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description": "Echo text.",
				"inputSchema": map[string]any{"type": "object"},
			}}})
		case "tools/call":
			params, _ := req["params"].(map[string]any)
			name, _ := params["name"].(string)
			writeMCPResponse(id, map[string]any{"content": []map[string]any{{"type": "text", "text": name}}})
		case "resources/list":
			writeMCPResponse(id, map[string]any{"resources": []map[string]any{{"uri": "codog://note", "name": "note", "mimeType": "text/plain"}}})
		case "resources/templates/list":
			writeMCPResponse(id, map[string]any{"resourceTemplates": []map[string]any{{
				"uriTemplate": "codog://{name}",
				"name":        "named note",
			}}})
		case "resources/read":
			params, _ := req["params"].(map[string]any)
			uri, _ := params["uri"].(string)
			writeMCPResponse(id, map[string]any{"contents": []map[string]any{{"uri": uri, "mimeType": "text/plain", "text": "note body"}}})
		case "prompts/list":
			writeMCPResponse(id, map[string]any{"prompts": []map[string]any{{
				"name":        "review",
				"description": "Review a topic.",
			}}})
		case "prompts/get":
			params, _ := req["params"].(map[string]any)
			args, _ := params["arguments"].(map[string]any)
			topic, _ := args["topic"].(string)
			writeMCPResponse(id, map[string]any{"messages": []map[string]any{{
				"role":    "user",
				"content": map[string]any{"type": "text", "text": "Review " + topic},
			}}})
		}
	}
	os.Exit(0)
}

func writeMCPResponse(id any, result map[string]any) {
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	data, _ := json.Marshal(payload)
	fmt.Println(strings.TrimSpace(string(data)))
}
