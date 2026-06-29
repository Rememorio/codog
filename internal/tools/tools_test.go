package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/config"
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
	require.Len(t, infos, 8)
	require.Equal(t, "bash", infos[0].Name)
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
	out, err := MCPTool{
		Name:       NewMCPToolName("test server", "echo"),
		ServerName: "test server",
		Server: config.MCPServerConfig{
			Command: os.Args[0],
			Args:    []string{"-test.run=TestMCPToolHelperProcess"},
			Env:     []string{"CODOG_MCP_TOOL_HELPER=1"},
		},
		RemoteName: "echo",
	}.Execute(context.Background(), []byte(`{"text":"hi"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"text":"echo"`)
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
			writeMCPResponse(id, map[string]any{"protocolVersion": "2024-11-05"})
		case "tools/call":
			params, _ := req["params"].(map[string]any)
			name, _ := params["name"].(string)
			writeMCPResponse(id, map[string]any{"content": []map[string]any{{"type": "text", "text": name}}})
		}
	}
	os.Exit(0)
}

func writeMCPResponse(id any, result map[string]any) {
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	data, _ := json.Marshal(payload)
	fmt.Println(strings.TrimSpace(string(data)))
}
