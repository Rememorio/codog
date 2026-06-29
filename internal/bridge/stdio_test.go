package bridge

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/session"
	"github.com/stretchr/testify/require"
)

func TestBridgeInitialize(t *testing.T) {
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test"}.Serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n"), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"name":"codog"`)
	require.Contains(t, out.String(), `"sessions/list"`)
	require.Contains(t, out.String(), `"sessions/open"`)
	require.Contains(t, out.String(), `"sessions/append_message"`)
	require.Contains(t, out.String(), `"sessions/append_input"`)
	require.Contains(t, out.String(), `"sessions/rewind"`)
	require.Contains(t, out.String(), `"sessions/prompt"`)
	require.Contains(t, out.String(), `"workspace/files"`)
	require.Contains(t, out.String(), `"workspace/search"`)
	require.Contains(t, out.String(), `"file/read"`)
	require.Contains(t, out.String(), `"file/diff"`)
	require.Contains(t, out.String(), `"editor/identify"`)
	require.Contains(t, out.String(), `"editor/selection"`)
	require.Contains(t, out.String(), `"diagnostics/go"`)
	require.Contains(t, out.String(), `"code/symbols"`)
	require.Contains(t, out.String(), `"code/references"`)
	require.Contains(t, out.String(), `"code/definition"`)
	require.Contains(t, out.String(), `"code/hover"`)
	require.Contains(t, out.String(), `"background/list"`)
	require.Contains(t, out.String(), `"background/run"`)
	require.Contains(t, out.String(), `"background/logs"`)
	require.Contains(t, out.String(), `"background/watch"`)
}

func TestBridgeSessionMutations(t *testing.T) {
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"sessions/open","params":{"id":"ide-session"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"sessions/append_input","params":{"id":"ide-session","input":"bridge prompt"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"sessions/append_message","params":{"id":"ide-session","role":"user","text":"hello from bridge"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"sessions/append_message","params":{"id":"ide-session","message":{"role":"assistant","content":[{"type":"text","text":"bridge answer"}]}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"sessions/get","params":{"id":"ide-session"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"sessions/rewind","params":{"id":"ide-session","remove_messages":1}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test"}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"id":"ide-session"`)
	require.Contains(t, out.String(), `"input":"bridge prompt"`)
	require.Contains(t, out.String(), "hello from bridge")
	require.Contains(t, out.String(), "bridge answer")
	require.Contains(t, out.String(), `"removed_messages":1`)

	entries, err := store.PromptHistory("ide-session")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "bridge prompt", entries[0].Text)
}

func TestBridgeSessionPromptStartsBackgroundTask(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	script := filepath.Join(t.TempDir(), "codog-shim")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf 'bridge-prompt:%s\\n' \"$*\"\n"), 0o755))
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	input := `{"jsonrpc":"2.0","id":1,"method":"sessions/prompt","params":{"id":"ide-session","prompt":"summarize selection"}}` + "\n"

	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test", Workspace: workspace, ConfigHome: configHome, Executable: script}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"kind":"prompt"`)
	require.Contains(t, out.String(), `"session_id":"ide-session"`)

	tasks, err := background.NewStore(configHome).List()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Eventually(t, func() bool {
		logs, err := background.NewStore(configHome).Logs(tasks[0].ID, 4096)
		return err == nil && strings.Contains(logs, "bridge-prompt:--resume ide-session prompt summarize selection")
	}, 5*time.Second, 50*time.Millisecond)
}

func TestBridgeFileReadWriteEdit(t *testing.T) {
	workspace := t.TempDir()
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"file/write","params":{"path":"notes.txt","content":"hello world"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"file/edit","params":{"path":"notes.txt","old_string":"world","new_string":"codog"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"file/read","params":{"path":"notes.txt"}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test", Workspace: workspace}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"bytes":11`)
	require.Contains(t, out.String(), `"replacements":1`)
	require.Contains(t, out.String(), `"content":"hello codog"`)

	data, err := os.ReadFile(filepath.Join(workspace, "notes.txt"))
	require.NoError(t, err)
	require.Equal(t, "hello codog", string(data))
}

func TestBridgeWorkspaceFilesSearchAndDiff(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Codog\n\nhello bridge\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "internal"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "internal", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"workspace/files","params":{"pattern":"*.md"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"workspace/search","params":{"query":"bridge","glob":"*.md"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"file/diff","params":{"path":"README.md","old_string":"hello bridge","new_string":"hello codog"}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test", Workspace: workspace}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"path":"README.md"`)
	require.Contains(t, out.String(), `"text":"hello bridge"`)
	require.Contains(t, out.String(), `-hello bridge`)
	require.Contains(t, out.String(), `+hello codog`)
}

func TestBridgeCodeIntelligence(t *testing.T) {
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
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"code/symbols","params":{"path":"demo.go"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"code/references","params":{"symbol":"Widget","limit":5}}`,
		`{"jsonrpc":"2.0","id":3,"method":"code/definition","params":{"symbol":"BuildWidget"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"code/hover","params":{"symbol":"Widget","context_lines":1}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test", Workspace: workspace}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"kind":"symbols"`)
	require.Contains(t, out.String(), `"name":"BuildWidget"`)
	require.Contains(t, out.String(), `"kind":"references"`)
	require.Contains(t, out.String(), `"symbol":"Widget"`)
	require.Contains(t, out.String(), `"found":true`)
	require.Contains(t, out.String(), `"kind":"hover"`)
}

func TestBridgeEditorIdentifyOpenSelectionState(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"editor/identify","params":{"editor":"VS Code","version":"1.0","workspace":"` + filepath.ToSlash(workspace) + `","token":"secret"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"editor/open","params":{"path":"main.go"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"editor/selection","params":{"start_line":1,"start_column":1,"end_line":1,"end_column":8}}`,
		`{"jsonrpc":"2.0","id":4,"method":"editor/state"}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test", Workspace: workspace, ConfigHome: configHome, TrustToken: "secret"}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"editor":"VS Code"`)
	require.Contains(t, out.String(), `"trusted":true`)
	require.Contains(t, out.String(), `"open_file":{"path":"main.go"`)
	require.Contains(t, out.String(), `"selection":{"path":"main.go","start_line":1`)
	require.Contains(t, out.String(), `"text":"package"`)
	require.FileExists(t, filepath.Join(configHome, "bridge", "editor-state.json"))
}

func TestBridgeEditorTrustRejectsInvalidTokenAndWorkspace(t *testing.T) {
	workspace := t.TempDir()
	other := t.TempDir()
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}

	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test", Workspace: workspace, ConfigHome: t.TempDir(), TrustToken: "secret"}.Serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"editor/identify","params":{"editor":"Bad","token":"wrong"}}`+"\n"), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"error"`)
	require.Contains(t, out.String(), "token is invalid")

	out.Reset()
	err = Server{Sessions: store, Version: "test", Workspace: workspace, ConfigHome: t.TempDir()}.Serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"editor/identify","params":{"editor":"Bad","workspace":"`+filepath.ToSlash(other)+`"}}`+"\n"), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"error"`)
	require.Contains(t, out.String(), "workspace is not trusted")
}

func TestBridgeBackgroundWatchStreamsNotifications(t *testing.T) {
	configHome := t.TempDir()
	bg := background.NewStore(configHome)
	task, err := bg.Run("echo bridge log", t.TempDir())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		logs, err := bg.Logs(task.ID, 100)
		return err == nil && strings.Contains(logs, "bridge log")
	}, 2*time.Second, 50*time.Millisecond)
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}

	var out bytes.Buffer
	input := `{"jsonrpc":"2.0","id":1,"method":"background/watch","params":{"id":"` + task.ID + `","max_events":2}}` + "\n"
	err = Server{Sessions: store, Version: "test", ConfigHome: configHome}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 3)
	require.Contains(t, lines[0], `"method":"background/event"`)
	require.Contains(t, lines[0], `"type":"status"`)
	require.Contains(t, lines[1], `"method":"background/event"`)
	require.Contains(t, lines[1], `"type":"log"`)
	require.Contains(t, lines[1], "bridge log")
	require.Contains(t, lines[2], `"id":1`)
	require.Contains(t, lines[2], `"events":2`)
}

func TestBridgeBackgroundControl(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	var out bytes.Buffer
	runInput := `{"jsonrpc":"2.0","id":1,"method":"background/run","params":{"command":"printf bridge-control","kind":"terminal","session_id":"ide-session"}}` + "\n"
	err := Server{Sessions: store, Version: "test", Workspace: workspace, ConfigHome: configHome}.Serve(strings.NewReader(runInput), &out)
	require.NoError(t, err)
	var runResp struct {
		Result background.Task `json:"result"`
	}
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(out.Bytes()), &runResp))
	require.NotEmpty(t, runResp.Result.ID)
	require.Equal(t, "terminal", runResp.Result.Kind)
	require.Equal(t, "ide-session", runResp.Result.SessionID)
	require.Eventually(t, func() bool {
		logs, err := background.NewStore(configHome).Logs(runResp.Result.ID, 4096)
		return err == nil && strings.Contains(logs, "bridge-control")
	}, 2*time.Second, 50*time.Millisecond)

	out.Reset()
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":2,"method":"background/list","params":{"session_id":"ide-session","kind":"terminal"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"background/get","params":{"id":"` + runResp.Result.ID + `"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"background/logs","params":{"id":"` + runResp.Result.ID + `","limit":4096}}`,
		`{"jsonrpc":"2.0","id":5,"method":"background/restart","params":{"id":"` + runResp.Result.ID + `"}}`,
	}, "\n") + "\n"
	err = Server{Sessions: store, Version: "test", Workspace: workspace, ConfigHome: configHome}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"session_id":"ide-session"`)
	require.Contains(t, out.String(), `"logs":"bridge-control"`)
	require.Contains(t, out.String(), `"restarted_from":"`+runResp.Result.ID+`"`)

	tasks, err := background.NewStore(configHome).List()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(tasks), 2)
	restartedID := ""
	for _, task := range tasks {
		if task.RestartedFrom == runResp.Result.ID {
			restartedID = task.ID
			break
		}
	}
	require.NotEmpty(t, restartedID)

	out.Reset()
	err = Server{Sessions: store, Version: "test", Workspace: workspace, ConfigHome: configHome}.Serve(strings.NewReader(`{"jsonrpc":"2.0","id":6,"method":"background/stop","params":{"id":"`+restartedID+`"}}`+"\n"), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"id":"`+restartedID+`"`)
}

func TestBridgeRejectsWorkspaceEscape(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "secret.txt"), []byte("secret"), 0o644))
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test", Workspace: workspace}.Serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"file/read","params":{"path":"../secret.txt"}}`+"\n"), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"error"`)
	require.Contains(t, out.String(), "escapes workspace")
}
