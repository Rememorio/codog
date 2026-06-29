package bridge

import (
	"bytes"
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
