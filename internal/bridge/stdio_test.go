package bridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/agentruns"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
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
	require.Contains(t, out.String(), `"sessions/history"`)
	require.Contains(t, out.String(), `"sessions/fork"`)
	require.Contains(t, out.String(), `"sessions/rename"`)
	require.Contains(t, out.String(), `"sessions/delete"`)
	require.Contains(t, out.String(), `"sessions/prune"`)
	require.Contains(t, out.String(), `"sessions/prompt"`)
	require.Contains(t, out.String(), `"workspace/files"`)
	require.Contains(t, out.String(), `"workspace/search"`)
	require.Contains(t, out.String(), `"file/read"`)
	require.Contains(t, out.String(), `"file/diff"`)
	require.Contains(t, out.String(), `"editor/identify"`)
	require.Contains(t, out.String(), `"editor/selection"`)
	require.Contains(t, out.String(), `"bridge/faults/list"`)
	require.Contains(t, out.String(), `"bridge/faults/record"`)
	require.Contains(t, out.String(), `"bridge/faults/clear"`)
	require.Contains(t, out.String(), `"diagnostics/go"`)
	require.Contains(t, out.String(), `"code/symbols"`)
	require.Contains(t, out.String(), `"code/references"`)
	require.Contains(t, out.String(), `"code/definition"`)
	require.Contains(t, out.String(), `"code/hover"`)
	require.Contains(t, out.String(), `"code/completion"`)
	require.Contains(t, out.String(), `"code/format"`)
	require.Contains(t, out.String(), `"notebook/read"`)
	require.Contains(t, out.String(), `"notebook/edit"`)
	require.Contains(t, out.String(), `"lsp/actions"`)
	require.Contains(t, out.String(), `"lsp/discover"`)
	require.Contains(t, out.String(), `"lsp/list"`)
	require.Contains(t, out.String(), `"lsp/start"`)
	require.Contains(t, out.String(), `"lsp/status"`)
	require.Contains(t, out.String(), `"lsp/stop"`)
	require.Contains(t, out.String(), `"lsp/query"`)
	require.Contains(t, out.String(), `"mcp/list"`)
	require.Contains(t, out.String(), `"mcp/show"`)
	require.Contains(t, out.String(), `"mcp/auth"`)
	require.Contains(t, out.String(), `"mcp/tools"`)
	require.Contains(t, out.String(), `"mcp/call"`)
	require.Contains(t, out.String(), `"mcp/resources"`)
	require.Contains(t, out.String(), `"mcp/resource-templates"`)
	require.Contains(t, out.String(), `"mcp/read"`)
	require.Contains(t, out.String(), `"mcp/prompts"`)
	require.Contains(t, out.String(), `"mcp/prompt"`)
	require.Contains(t, out.String(), `"background/list"`)
	require.Contains(t, out.String(), `"background/run"`)
	require.Contains(t, out.String(), `"background/logs"`)
	require.Contains(t, out.String(), `"background/board"`)
	require.Contains(t, out.String(), `"background/heartbeat"`)
	require.Contains(t, out.String(), `"background/watch"`)
	require.Contains(t, out.String(), `"background/prune"`)
	require.Contains(t, out.String(), `"background/supervise"`)
	require.Contains(t, out.String(), `"agent-runs/list"`)
	require.Contains(t, out.String(), `"agent-runs/get"`)
	require.Contains(t, out.String(), `"agent-runs/logs"`)
	require.Contains(t, out.String(), `"agent-runs/board"`)
	require.Contains(t, out.String(), `"agent-runs/heartbeat"`)
	require.Contains(t, out.String(), `"agent-runs/stop"`)
	require.Contains(t, out.String(), `"agent-runs/prune"`)
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

func TestBridgeSessionHistoryForkRenameAndDelete(t *testing.T) {
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	require.NoError(t, store.AppendInput("ide-session", "first prompt"))
	require.NoError(t, store.AppendInput("ide-session", "second prompt"))
	require.NoError(t, store.Append("ide-session", anthropic.TextMessage("user", "hello")))
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"sessions/history","params":{"session_id":"ide-session","limit":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"sessions/fork","params":{"session_id":"ide-session","branch_name":"investigation"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"sessions/rename","params":{"session_id":"ide-session","new_session_id":"renamed-ide-session"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"sessions/delete","params":{"id":"renamed-ide-session"}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test"}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"kind":"session_history"`)
	require.Contains(t, out.String(), `"count":1`)
	require.Contains(t, out.String(), `"text":"second prompt"`)
	require.NotContains(t, out.String(), `"text":"first prompt"`)
	require.Contains(t, out.String(), `"action":"fork"`)
	require.Contains(t, out.String(), `"parent_id":"ide-session"`)
	require.Contains(t, out.String(), `"branch_name":"investigation"`)
	require.Contains(t, out.String(), `"action":"rename"`)
	require.Contains(t, out.String(), `"old_id":"ide-session"`)
	require.Contains(t, out.String(), `"new_id":"renamed-ide-session"`)
	require.Contains(t, out.String(), `"action":"delete"`)
	require.Contains(t, out.String(), `"id":"renamed-ide-session"`)
	require.NoFileExists(t, filepath.Join(store.Dir, "ide-session.jsonl"))
	require.NoFileExists(t, filepath.Join(store.Dir, "renamed-ide-session.jsonl"))
	sessions, err := store.List()
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, "hello", sessions[0].Messages[0].Content[0].Text)
	require.Equal(t, "fork:investigation", sessions[0].Identity.Purpose)
}

func TestBridgeSessionPruneDryRunAndConfirm(t *testing.T) {
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	_, err := store.Create("empty-ide-session")
	require.NoError(t, err)
	require.NoError(t, store.Append("kept-ide-session", anthropic.TextMessage("user", "keep me")))
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"sessions/prune"}`,
		`{"jsonrpc":"2.0","id":2,"method":"sessions/prune","params":{"confirm":true}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	err = Server{Sessions: store, Version: "test"}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"kind":"session_prune"`)
	require.Contains(t, out.String(), `"status":"dry_run"`)
	require.Contains(t, out.String(), `"dry_run":true`)
	require.Contains(t, out.String(), `"candidate_count":1`)
	require.Contains(t, out.String(), `"status":"ok"`)
	require.Contains(t, out.String(), `"deleted_count":1`)
	require.Contains(t, out.String(), `"id":"empty-ide-session"`)
	require.NoFileExists(t, filepath.Join(store.Dir, "empty-ide-session.jsonl"))
	require.FileExists(t, filepath.Join(store.Dir, "kept-ide-session.jsonl"))
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
	}, 20*time.Second, 50*time.Millisecond)
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
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "messy.go"), []byte("package demo\n\nfunc messy(){return}\n"), 0o644))
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"code/symbols","params":{"path":"demo.go"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"code/references","params":{"symbol":"Widget","limit":5}}`,
		`{"jsonrpc":"2.0","id":3,"method":"code/definition","params":{"symbol":"BuildWidget"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"code/hover","params":{"symbol":"Widget","context_lines":1}}`,
		`{"jsonrpc":"2.0","id":5,"method":"code/completion","params":{"query":"Build","limit":5}}`,
		`{"jsonrpc":"2.0","id":6,"method":"code/format","params":{"path":"messy.go"}}`,
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
	require.Contains(t, out.String(), `"kind":"completion"`)
	require.Contains(t, out.String(), `"label":"BuildWidget"`)
	require.Contains(t, out.String(), `"kind":"format"`)
	require.Contains(t, out.String(), `"changed":true`)
	require.Contains(t, out.String(), `func messy()`)
}

func TestBridgeNotebookReadEdit(t *testing.T) {
	workspace := t.TempDir()
	notebook := `{
  "cells": [
    {"cell_type": "markdown", "id": "intro", "metadata": {}, "source": ["# Title\n"]},
    {"cell_type": "code", "id": "calc", "metadata": {}, "source": ["print(1)\n"], "outputs": [{"output_type": "stream", "name": "stdout", "text": ["1\n"]}], "execution_count": 1}
  ],
  "metadata": {"kernelspec": {"language": "python"}},
  "nbformat": 4,
  "nbformat_minor": 5
}`
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "analysis.ipynb"), []byte(notebook), 0o644))
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"notebook/read","params":{"notebook_path":"analysis.ipynb","limit":1,"include_outputs":true}}`,
		`{"jsonrpc":"2.0","id":2,"method":"notebook/edit","params":{"notebook_path":"analysis.ipynb","cell_id":"intro","new_source":"# Renamed\n"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"notebook/edit","params":{"path":"analysis.ipynb","edit_mode":"insert","cell_id":"intro","cell_type":"markdown","source":"inserted note"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"notebook/edit","params":{"path":"analysis.ipynb","mode":"delete","cell_id":"calc"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"notebook/read","params":{"path":"analysis.ipynb","cell_index":0,"outputs":false}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test", Workspace: workspace}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"kind":"notebook_read"`)
	require.Contains(t, out.String(), `"path":"analysis.ipynb"`)
	require.Contains(t, out.String(), `"cell_count":2`)
	require.Contains(t, out.String(), `"truncated":true`)
	require.Contains(t, out.String(), `"mode":"replace"`)
	require.Contains(t, out.String(), `"mode":"insert"`)
	require.Contains(t, out.String(), `"mode":"delete"`)
	require.Contains(t, out.String(), `"cell_count":2`)
	require.Contains(t, out.String(), `# Renamed`)
	require.NotContains(t, out.String(), `"cell_id":"calc","cell_type":"code","source":"print(1)`)

	data, err := os.ReadFile(filepath.Join(workspace, "analysis.ipynb"))
	require.NoError(t, err)
	require.Contains(t, string(data), "# Renamed")
	require.Contains(t, string(data), "inserted note")
	require.NotContains(t, string(data), "print(1)")
}

func TestBridgeNotebookRejectsInvalidInputs(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("not a notebook"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "outside.ipynb"), []byte(`{"cells":[]}`), 0o644))
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"notebook/read","params":{"path":"notes.txt"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"notebook/read","params":{"path":"../outside.ipynb"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"notebook/edit","params":{"path":"notes.txt","new_source":"x"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"notebook/edit","params":{"path":"notes.txt","mode":"replace"}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test", Workspace: workspace}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), "notebook path must point to a .ipynb file")
	require.Contains(t, out.String(), "escapes workspace")
	require.Contains(t, out.String(), "new_source is required for insert and replace edits")
}

func TestBridgeLSPLifecycleAndQuery(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte(strings.Join([]string{
		"package main",
		"",
		"func main() {",
		"\tprintln(\"hi\")",
		"}",
		"",
	}, "\n")), 0o644))
	fakeCommand := "CODOG_BRIDGE_FAKE_LSP=1 " + bridgeShellQuote(os.Args[0]) + " -test.run '^TestBridgeFakeLSPServer$'"
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"lsp/actions"}`,
		`{"jsonrpc":"2.0","id":2,"method":"lsp/discover"}`,
		`{"jsonrpc":"2.0","id":3,"method":"lsp/start","params":{"language":"go","command":"` + jsonEscape(fakeCommand) + `"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"lsp/query","params":{"language":"go","action":"hover","path":"main.go","line":2,"character":5}}`,
		`{"jsonrpc":"2.0","id":5,"method":"lsp/query","params":{"language":"go","action":"diagnostics","file_path":"main.go","timeout_ms":1000}}`,
		`{"jsonrpc":"2.0","id":6,"method":"lsp/query","params":{"language":"go","action":"rename","path":"main.go","line":2,"character":5,"new_name":"Start","write":true}}`,
		`{"jsonrpc":"2.0","id":7,"method":"lsp/query","params":{"language":"go","action":"code_action","path":"main.go","line":2,"character":5,"code_action_title":"Bridge fake fix","apply":true}}`,
		`{"jsonrpc":"2.0","id":8,"method":"lsp/query","params":{"language":"go","action":"prepare_call_hierarchy","path":"main.go","line":2,"character":5}}`,
		`{"jsonrpc":"2.0","id":9,"method":"lsp/query","params":{"language":"go","action":"incoming_calls","path":"main.go","line":2,"character":5}}`,
		`{"jsonrpc":"2.0","id":10,"method":"lsp/query","params":{"language":"go","action":"outgoing_calls","path":"main.go","line":2,"character":5}}`,
		`{"jsonrpc":"2.0","id":11,"method":"lsp/query","params":{"language":"go","action":"prepare_type_hierarchy","path":"main.go","line":2,"character":5}}`,
		`{"jsonrpc":"2.0","id":12,"method":"lsp/query","params":{"language":"go","action":"supertypes","path":"main.go","line":2,"character":5}}`,
		`{"jsonrpc":"2.0","id":13,"method":"lsp/query","params":{"language":"go","action":"subtypes","path":"main.go","line":2,"character":5}}`,
		`{"jsonrpc":"2.0","id":14,"method":"lsp/query","params":{"language":"go","action":"prepare_rename","path":"main.go","line":2,"character":5}}`,
		`{"jsonrpc":"2.0","id":15,"method":"lsp/query","params":{"language":"go","action":"selection_range","path":"main.go","line":2,"character":5}}`,
		`{"jsonrpc":"2.0","id":16,"method":"lsp/query","params":{"language":"go","action":"folding_range","path":"main.go"}}`,
		`{"jsonrpc":"2.0","id":17,"method":"lsp/query","params":{"language":"go","action":"document_link","path":"main.go"}}`,
		`{"jsonrpc":"2.0","id":18,"method":"lsp/query","params":{"language":"go","action":"document_color","path":"main.go"}}`,
		`{"jsonrpc":"2.0","id":19,"method":"lsp/query","params":{"language":"go","action":"color_presentation","path":"main.go","line":2,"character":3}}`,
		`{"jsonrpc":"2.0","id":20,"method":"lsp/query","params":{"language":"go","action":"code_lens","path":"main.go"}}`,
		`{"jsonrpc":"2.0","id":21,"method":"lsp/query","params":{"language":"go","action":"code_lens_resolve","path":"main.go","line":2,"character":3}}`,
		`{"jsonrpc":"2.0","id":22,"method":"lsp/query","params":{"language":"go","action":"completion_resolve","path":"main.go","line":2,"character":5,"query":"BridgeWidget"}}`,
		`{"jsonrpc":"2.0","id":23,"method":"lsp/query","params":{"language":"go","action":"range_format","path":"main.go","line":2,"character":14}}`,
		`{"jsonrpc":"2.0","id":24,"method":"lsp/query","params":{"language":"go","action":"on_type_format","path":"main.go","line":2,"character":14,"query":"}"}}`,
		`{"jsonrpc":"2.0","id":25,"method":"lsp/query","params":{"language":"go","action":"will_save","path":"main.go"}}`,
		`{"jsonrpc":"2.0","id":26,"method":"lsp/query","params":{"language":"go","action":"inlay_hint","path":"main.go","line":2,"character":12}}`,
		`{"jsonrpc":"2.0","id":27,"method":"lsp/query","params":{"language":"go","action":"linked_editing_range","path":"main.go","line":2,"character":5}}`,
		`{"jsonrpc":"2.0","id":28,"method":"lsp/query","params":{"language":"go","action":"moniker","path":"main.go","line":2,"character":5}}`,
		`{"jsonrpc":"2.0","id":29,"method":"lsp/query","params":{"language":"go","action":"semantic_tokens","path":"main.go"}}`,
		`{"jsonrpc":"2.0","id":30,"method":"lsp/query","params":{"language":"go","action":"semantic_tokens_range","path":"main.go","line":2,"character":12}}`,
		`{"jsonrpc":"2.0","id":31,"method":"lsp/query","params":{"language":"go","action":"semantic_tokens_delta","path":"main.go","query":"bridge-full-1"}}`,
		`{"jsonrpc":"2.0","id":32,"method":"lsp/query","params":{"language":"go","action":"workspace_symbol","query":"Build"}}`,
		`{"jsonrpc":"2.0","id":33,"method":"lsp/query","params":{"language":"go","action":"document_diagnostic","path":"main.go"}}`,
		`{"jsonrpc":"2.0","id":34,"method":"lsp/query","params":{"language":"go","action":"workspace_diagnostic"}}`,
		`{"jsonrpc":"2.0","id":35,"method":"lsp/query","params":{"language":"go","action":"code_action_resolve","path":"main.go","line":2,"character":5,"query":"Bridge lazy fix"}}`,
		`{"jsonrpc":"2.0","id":36,"method":"lsp/query","params":{"language":"go","action":"document_link_resolve","path":"main.go","line":2,"character":3}}`,
		`{"jsonrpc":"2.0","id":37,"method":"lsp/query","params":{"language":"go","action":"inlay_hint_resolve","path":"main.go","line":2,"character":10}}`,
		`{"jsonrpc":"2.0","id":38,"method":"lsp/query","params":{"language":"go","action":"inline_value","path":"main.go","line":2,"character":10,"query":"frame-1"}}`,
		`{"jsonrpc":"2.0","id":39,"method":"lsp/status","params":{"language":"go"}}`,
		`{"jsonrpc":"2.0","id":40,"method":"lsp/list"}`,
		`{"jsonrpc":"2.0","id":41,"method":"lsp/query","params":{"language":"go","action":"workspace_symbol_resolve","query":"Build"}}`,
		`{"jsonrpc":"2.0","id":42,"method":"lsp/query","params":{"language":"go","action":"execute_command","query":"bridge.run","arguments":["main.go",{"line":2}]}}`,
		`{"jsonrpc":"2.0","id":43,"method":"lsp/stop","params":{"language":"go"}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test", Workspace: workspace, ConfigHome: configHome}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"kind":"lsp_actions"`)
	require.Contains(t, out.String(), `"method":"textDocument/definition"`)
	require.Contains(t, out.String(), `"kind":"lsp_discover"`)
	require.Contains(t, out.String(), `"command":"gopls"`)
	require.Contains(t, out.String(), `"kind":"lsp_start"`)
	require.Contains(t, out.String(), `"language":"go"`)
	require.Contains(t, out.String(), `"kind":"lsp_query"`)
	require.Contains(t, out.String(), `"action":"hover"`)
	require.Contains(t, out.String(), `"value":"bridge fake hover"`)
	require.Contains(t, out.String(), `"action":"diagnostics"`)
	require.Contains(t, out.String(), `"message":"bridge fake diagnostic"`)
	require.Contains(t, out.String(), `"href":"https://example.test/bridge-diagnostic"`)
	require.Contains(t, out.String(), `"tags":[1]`)
	require.Contains(t, out.String(), `"message":"bridge related diagnostic"`)
	require.Contains(t, out.String(), `"rule":"bridge-rule"`)
	require.Contains(t, out.String(), `"action":"rename"`)
	require.Contains(t, out.String(), `"file_edits":1`)
	require.Contains(t, out.String(), `"applied":true`)
	require.Contains(t, out.String(), `func Start()`)
	require.Contains(t, out.String(), `"action":"code-action"`)
	require.Contains(t, out.String(), `"title":"Bridge fake fix"`)
	require.Contains(t, out.String(), `"file_edits":1`)
	require.Contains(t, out.String(), `"applied":true`)
	require.Contains(t, out.String(), `func Launch()`)
	require.Contains(t, out.String(), `"action":"prepare-call-hierarchy"`)
	require.Contains(t, out.String(), `"method":"textDocument/prepareCallHierarchy"`)
	require.Contains(t, out.String(), `"name":"BridgeWidget"`)
	require.Contains(t, out.String(), `"action":"call-hierarchy-incoming"`)
	require.Contains(t, out.String(), `"method":"callHierarchy/incomingCalls"`)
	require.Contains(t, out.String(), `"name":"BridgeCaller"`)
	require.Contains(t, out.String(), `"action":"call-hierarchy-outgoing"`)
	require.Contains(t, out.String(), `"method":"callHierarchy/outgoingCalls"`)
	require.Contains(t, out.String(), `"name":"BridgeCallee"`)
	require.Contains(t, out.String(), `"action":"prepare-type-hierarchy"`)
	require.Contains(t, out.String(), `"method":"textDocument/prepareTypeHierarchy"`)
	require.Contains(t, out.String(), `"name":"BridgeType"`)
	require.Contains(t, out.String(), `"action":"type-hierarchy-supertypes"`)
	require.Contains(t, out.String(), `"method":"typeHierarchy/supertypes"`)
	require.Contains(t, out.String(), `"name":"BridgeBaseType"`)
	require.Contains(t, out.String(), `"action":"type-hierarchy-subtypes"`)
	require.Contains(t, out.String(), `"method":"typeHierarchy/subtypes"`)
	require.Contains(t, out.String(), `"name":"BridgeSubType"`)
	data, err := os.ReadFile(filepath.Join(workspace, "main.go"))
	require.NoError(t, err)
	require.Contains(t, string(data), "func Launch()")
	require.Contains(t, out.String(), `"action":"prepare-rename"`)
	require.Contains(t, out.String(), `"placeholder":"Launch"`)
	require.Contains(t, out.String(), `"action":"selection-range"`)
	require.Contains(t, out.String(), `"method":"textDocument/selectionRange"`)
	require.Contains(t, out.String(), `"action":"folding-range"`)
	require.Contains(t, out.String(), `"method":"textDocument/foldingRange"`)
	require.Contains(t, out.String(), `"startLine":2`)
	require.Contains(t, out.String(), `"action":"document-link"`)
	require.Contains(t, out.String(), `"method":"textDocument/documentLink"`)
	require.Contains(t, out.String(), `"target":"https://example.test/bridge"`)
	require.Contains(t, out.String(), `"action":"document-color"`)
	require.Contains(t, out.String(), `"method":"textDocument/documentColor"`)
	require.Contains(t, out.String(), `"red":0.25`)
	require.Contains(t, out.String(), `"action":"color-presentation"`)
	require.Contains(t, out.String(), `"method":"textDocument/colorPresentation"`)
	require.Contains(t, out.String(), `"label":"rgba(64,128,255,1)"`)
	require.Contains(t, out.String(), `"action":"code-lens"`)
	require.Contains(t, out.String(), `"method":"textDocument/codeLens"`)
	require.Contains(t, out.String(), `"title":"Run bridge lens"`)
	require.Contains(t, out.String(), `"action":"code-lens-resolve"`)
	require.Contains(t, out.String(), `"method":"codeLens/resolve"`)
	require.Contains(t, out.String(), `"title":"Run bridge lens (resolved)"`)
	require.Contains(t, out.String(), `"action":"completion-item-resolve"`)
	require.Contains(t, out.String(), `"method":"completionItem/resolve"`)
	require.Contains(t, out.String(), `"detail":"func BridgeWidget() Widget"`)
	require.Contains(t, out.String(), `"action":"range-format"`)
	require.Contains(t, out.String(), `"method":"textDocument/rangeFormatting"`)
	require.Contains(t, out.String(), `func bridgeRangeFormatted()`)
	require.Contains(t, out.String(), `"action":"on-type-format"`)
	require.Contains(t, out.String(), `"method":"textDocument/onTypeFormatting"`)
	require.Contains(t, out.String(), `func bridgeOnTypeFormatted()`)
	require.Contains(t, out.String(), `"action":"will-save"`)
	require.Contains(t, out.String(), `"method":"textDocument/willSaveWaitUntil"`)
	require.Contains(t, out.String(), `func bridgeSaved()`)
	require.Contains(t, out.String(), `"action":"inlay-hint"`)
	require.Contains(t, out.String(), `"method":"textDocument/inlayHint"`)
	require.Contains(t, out.String(), `"label":": string"`)
	require.Contains(t, out.String(), `"action":"linked-editing-range"`)
	require.Contains(t, out.String(), `"method":"textDocument/linkedEditingRange"`)
	require.Contains(t, out.String(), `"wordPattern":"[A-Za-z_]+"`)
	require.Contains(t, out.String(), `"action":"moniker"`)
	require.Contains(t, out.String(), `"method":"textDocument/moniker"`)
	require.Contains(t, out.String(), `"identifier":"bridge.test/demo.BridgeWidget"`)
	require.Contains(t, out.String(), `"action":"semantic-tokens"`)
	require.Contains(t, out.String(), `"method":"textDocument/semanticTokens/full"`)
	require.Contains(t, out.String(), `"resultId":"bridge-full-1"`)
	require.Contains(t, out.String(), `"action":"semantic-tokens-range"`)
	require.Contains(t, out.String(), `"method":"textDocument/semanticTokens/range"`)
	require.Contains(t, out.String(), `"data":[0,5,6,12,0]`)
	require.Contains(t, out.String(), `"action":"semantic-tokens-delta"`)
	require.Contains(t, out.String(), `"method":"textDocument/semanticTokens/full/delta"`)
	require.Contains(t, out.String(), `"resultId":"bridge-full-2"`)
	require.Contains(t, out.String(), `"action":"workspace-symbol"`)
	require.Contains(t, out.String(), `"method":"workspace/symbol"`)
	require.Contains(t, out.String(), `"name":"BridgeWidget"`)
	require.Contains(t, out.String(), `"action":"workspace-symbol-resolve"`)
	require.Contains(t, out.String(), `"method":"workspaceSymbol/resolve"`)
	require.Contains(t, out.String(), `"containerName":"bridge"`)
	require.Contains(t, out.String(), `"action":"execute-command"`)
	require.Contains(t, out.String(), `"method":"workspace/executeCommand"`)
	require.Contains(t, out.String(), `"command":"bridge.run"`)
	require.Contains(t, out.String(), `"arguments":["main.go",{"line":2}]`)
	require.Contains(t, out.String(), `"configurationHandled":true`)
	require.Contains(t, out.String(), `"foldersHandled":true`)
	require.Contains(t, out.String(), `"registerHandled":true`)
	require.Contains(t, out.String(), `"unregisterHandled":true`)
	require.Contains(t, out.String(), `"progressHandled":true`)
	require.Contains(t, out.String(), `"showDocumentHandled":true`)
	require.Contains(t, out.String(), `"refreshHandled":true`)
	require.Contains(t, out.String(), `"method":"window/showMessage"`)
	require.Contains(t, out.String(), `"method":"window/logMessage"`)
	require.Contains(t, out.String(), `"method":"telemetry/event"`)
	require.Contains(t, out.String(), `"method":"$/progress"`)
	require.Contains(t, out.String(), `func bridgeExecutePreview()`)
	require.Contains(t, out.String(), `"action":"document-diagnostic"`)
	require.Contains(t, out.String(), `"method":"textDocument/diagnostic"`)
	require.Contains(t, out.String(), `"message":"bridge pulled document diagnostic"`)
	require.Contains(t, out.String(), `"action":"workspace-diagnostic"`)
	require.Contains(t, out.String(), `"method":"workspace/diagnostic"`)
	require.Contains(t, out.String(), `"message":"bridge pulled workspace diagnostic"`)
	require.Contains(t, out.String(), `"action":"code-action-resolve"`)
	require.Contains(t, out.String(), `"method":"codeAction/resolve"`)
	require.Contains(t, out.String(), `"title":"Bridge lazy fix"`)
	require.Contains(t, out.String(), `"action":"document-link-resolve"`)
	require.Contains(t, out.String(), `"method":"documentLink/resolve"`)
	require.Contains(t, out.String(), `"target":"https://example.test/bridge-resolved"`)
	require.Contains(t, out.String(), `"action":"inlay-hint-resolve"`)
	require.Contains(t, out.String(), `"method":"inlayHint/resolve"`)
	require.Contains(t, out.String(), `"tooltip":"bridge resolved inlay hint"`)
	require.Contains(t, out.String(), `"action":"inline-value"`)
	require.Contains(t, out.String(), `"method":"textDocument/inlineValue"`)
	require.Contains(t, out.String(), `"text":"bridgeCount = 1"`)
	require.Contains(t, out.String(), `"kind":"lsp_status"`)
	require.Contains(t, out.String(), `"kind":"lsp_list"`)
	require.Contains(t, out.String(), `"kind":"lsp_stop"`)
}

func TestBridgeLSPRejectsInvalidInputs(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"lsp/start","params":{"language":"bad/language"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"lsp/query","params":{"language":"go","action":"hover","path":"main.go","line":-1}}`,
		`{"jsonrpc":"2.0","id":3,"method":"lsp/start","params":{"language":"go","command":123}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test", Workspace: workspace, ConfigHome: configHome}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), "language must be a single safe name")
	require.Contains(t, out.String(), "line must be non-negative")
	require.Contains(t, out.String(), "command must be a string or string array")
}

func TestBridgeFakeLSPServer(t *testing.T) {
	if os.Getenv("CODOG_BRIDGE_FAKE_LSP") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	currentURI := ""
	rootURI := ""
	for {
		raw, err := readBridgeTestLSPMessage(reader)
		if err != nil {
			return
		}
		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      any             `json:"id,omitempty"`
			Method  string          `json:"method,omitempty"`
			Params  json.RawMessage `json:"params,omitempty"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			return
		}
		switch msg.Method {
		case "initialize":
			var params struct {
				RootURI string `json:"rootUri"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			rootURI = strings.TrimRight(params.RootURI, "/")
			if currentURI == "" && rootURI != "" {
				currentURI = rootURI + "/main.go"
			}
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"capabilities": map[string]any{}}})
		case "textDocument/diagnostic":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"kind": "full",
				"items": []map[string]any{{
					"range": map[string]any{
						"start": map[string]any{"line": 2, "character": 0},
						"end":   map[string]any{"line": 2, "character": 4},
					},
					"severity": 2,
					"source":   "bridge-fake-lsp",
					"message":  "bridge pulled document diagnostic",
				}},
			}})
		case "workspace/diagnostic":
			if currentURI == "" {
				currentURI = "file:///workspace/main.go"
			}
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"items": []map[string]any{{
					"uri":     currentURI,
					"version": 1,
					"kind":    "full",
					"items": []map[string]any{{
						"range": map[string]any{
							"start": map[string]any{"line": 4, "character": 0},
							"end":   map[string]any{"line": 4, "character": 4},
						},
						"severity": 2,
						"source":   "bridge-fake-lsp",
						"message":  "bridge pulled workspace diagnostic",
					}},
				}},
			}})
		case "textDocument/didOpen":
			uri := bridgeTestLSPDocumentURI(msg.Params)
			currentURI = uri
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{
				"jsonrpc": "2.0",
				"method":  "textDocument/publishDiagnostics",
				"params": map[string]any{
					"uri": uri,
					"diagnostics": []map[string]any{{
						"range": map[string]any{
							"start": map[string]any{"line": 2, "character": 0},
							"end":   map[string]any{"line": 2, "character": 4},
						},
						"severity": 2,
						"code":     "bridge-code",
						"codeDescription": map[string]any{
							"href": "https://example.test/bridge-diagnostic",
						},
						"source":  "bridge-fake-lsp",
						"message": "bridge fake diagnostic",
						"tags":    []int{1},
						"relatedInformation": []map[string]any{{
							"location": map[string]any{
								"uri": uri,
								"range": map[string]any{
									"start": map[string]any{"line": 1, "character": 0},
									"end":   map[string]any{"line": 1, "character": 4},
								},
							},
							"message": "bridge related diagnostic",
						}},
						"data": map[string]any{"rule": "bridge-rule"},
					}},
				},
			})
		case "textDocument/hover":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"contents": map[string]any{"kind": "markdown", "value": "bridge fake hover"}}})
		case "textDocument/completion":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"isIncomplete": false,
				"items": []map[string]any{
					{"label": "BridgeWidget", "kind": 3, "data": map[string]any{"id": "bridge-completion-1"}},
					{"label": "BridgeOther", "kind": 3, "data": map[string]any{"id": "bridge-completion-2"}},
				},
			}})
		case "completionItem/resolve":
			var item map[string]any
			_ = json.Unmarshal(msg.Params, &item)
			item["detail"] = "func BridgeWidget() Widget"
			item["documentation"] = map[string]any{"kind": "markdown", "value": "Constructs a bridge widget."}
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": item})
		case "textDocument/rename":
			var params struct {
				NewName string `json:"newName"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			if currentURI == "" {
				currentURI = "file:///workspace/main.go"
			}
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"changes": map[string]any{
					currentURI: []map[string]any{{
						"range": map[string]any{
							"start": map[string]any{"line": 2, "character": 5},
							"end":   map[string]any{"line": 2, "character": 9},
						},
						"newText": params.NewName,
					}},
				},
			}})
		case "textDocument/codeAction":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{
				{
					"title": "Bridge fake fix",
					"kind":  "quickfix",
					"edit": map[string]any{
						"changes": map[string]any{
							currentURI: []map[string]any{{
								"range": map[string]any{
									"start": map[string]any{"line": 2, "character": 5},
									"end":   map[string]any{"line": 2, "character": 10},
								},
								"newText": "Launch",
							}},
						},
					},
				},
				{"title": "Bridge lazy fix", "kind": "quickfix", "data": map[string]any{"id": "bridge-lazy-fix"}},
			}})
		case "codeAction/resolve":
			var action map[string]any
			_ = json.Unmarshal(msg.Params, &action)
			action["edit"] = map[string]any{
				"changes": map[string]any{
					currentURI: []map[string]any{{
						"range": map[string]any{
							"start": map[string]any{"line": 2, "character": 5},
							"end":   map[string]any{"line": 2, "character": 10},
						},
						"newText": "BridgeLazy",
					}},
				},
			}
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": action})
		case "textDocument/prepareCallHierarchy":
			if currentURI == "" {
				currentURI = "file:///workspace/main.go"
			}
			item := map[string]any{
				"name": "BridgeWidget",
				"kind": 12,
				"uri":  currentURI,
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 20},
				},
				"selectionRange": map[string]any{
					"start": map[string]any{"line": 2, "character": 5},
					"end":   map[string]any{"line": 2, "character": 17},
				},
			}
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{item}})
		case "callHierarchy/incomingCalls":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"from": map[string]any{
					"name": "BridgeCaller",
					"kind": 12,
					"uri":  currentURI,
					"range": map[string]any{
						"start": map[string]any{"line": 8, "character": 0},
						"end":   map[string]any{"line": 10, "character": 1},
					},
					"selectionRange": map[string]any{
						"start": map[string]any{"line": 8, "character": 5},
						"end":   map[string]any{"line": 8, "character": 17},
					},
				},
				"fromRanges": []map[string]any{{
					"start": map[string]any{"line": 9, "character": 1},
					"end":   map[string]any{"line": 9, "character": 13},
				}},
			}}})
		case "callHierarchy/outgoingCalls":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"to": map[string]any{
					"name": "BridgeCallee",
					"kind": 12,
					"uri":  currentURI,
					"range": map[string]any{
						"start": map[string]any{"line": 4, "character": 0},
						"end":   map[string]any{"line": 4, "character": 20},
					},
					"selectionRange": map[string]any{
						"start": map[string]any{"line": 4, "character": 5},
						"end":   map[string]any{"line": 4, "character": 17},
					},
				},
				"fromRanges": []map[string]any{{
					"start": map[string]any{"line": 3, "character": 1},
					"end":   map[string]any{"line": 3, "character": 13},
				}},
			}}})
		case "textDocument/prepareTypeHierarchy":
			if currentURI == "" {
				currentURI = "file:///workspace/main.go"
			}
			item := map[string]any{
				"name": "BridgeType",
				"kind": 23,
				"uri":  currentURI,
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 20},
				},
				"selectionRange": map[string]any{
					"start": map[string]any{"line": 2, "character": 5},
					"end":   map[string]any{"line": 2, "character": 15},
				},
			}
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{item}})
		case "typeHierarchy/supertypes":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"name": "BridgeBaseType",
				"kind": 23,
				"uri":  currentURI,
				"range": map[string]any{
					"start": map[string]any{"line": 1, "character": 0},
					"end":   map[string]any{"line": 1, "character": 20},
				},
				"selectionRange": map[string]any{
					"start": map[string]any{"line": 1, "character": 5},
					"end":   map[string]any{"line": 1, "character": 19},
				},
			}}})
		case "typeHierarchy/subtypes":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"name": "BridgeSubType",
				"kind": 23,
				"uri":  currentURI,
				"range": map[string]any{
					"start": map[string]any{"line": 5, "character": 0},
					"end":   map[string]any{"line": 5, "character": 20},
				},
				"selectionRange": map[string]any{
					"start": map[string]any{"line": 5, "character": 5},
					"end":   map[string]any{"line": 5, "character": 18},
				},
			}}})
		case "textDocument/prepareRename":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 5},
					"end":   map[string]any{"line": 2, "character": 11},
				},
				"placeholder": "Launch",
			}})
		case "textDocument/selectionRange":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 5},
					"end":   map[string]any{"line": 2, "character": 11},
				},
			}}})
		case "textDocument/foldingRange":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"startLine": 2,
				"endLine":   4,
				"kind":      "region",
			}}})
		case "textDocument/documentLink":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 10},
				},
				"target": "https://example.test/bridge",
			}}})
		case "documentLink/resolve":
			var link map[string]any
			_ = json.Unmarshal(msg.Params, &link)
			link["target"] = "https://example.test/bridge-resolved"
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": link})
		case "textDocument/documentColor":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 1},
					"end":   map[string]any{"line": 2, "character": 8},
				},
				"color": map[string]any{"red": 0.25, "green": 0.5, "blue": 1.0, "alpha": 1.0},
			}}})
		case "textDocument/colorPresentation":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"label": "rgba(64,128,255,1)",
				"textEdit": map[string]any{
					"range": map[string]any{
						"start": map[string]any{"line": 2, "character": 1},
						"end":   map[string]any{"line": 2, "character": 8},
					},
					"newText": "rgba(64,128,255,1)",
				},
			}}})
		case "textDocument/codeLens":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 20},
				},
				"command": map[string]any{"title": "Run bridge lens", "command": "bridge.run"},
				"data":    map[string]any{"id": "bridge-lens-1"},
			}}})
		case "codeLens/resolve":
			var lens map[string]any
			_ = json.Unmarshal(msg.Params, &lens)
			lens["command"] = map[string]any{"title": "Run bridge lens (resolved)", "command": "bridge.run", "arguments": []string{"main.go"}}
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": lens})
		case "textDocument/inlayHint":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"position": map[string]any{"line": 2, "character": 10},
				"label":    ": string",
				"kind":     1,
			}}})
		case "inlayHint/resolve":
			var hint map[string]any
			_ = json.Unmarshal(msg.Params, &hint)
			hint["tooltip"] = "bridge resolved inlay hint"
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": hint})
		case "textDocument/inlineValue":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 5},
					"end":   map[string]any{"line": 2, "character": 10},
				},
				"text": "bridgeCount = 1",
			}}})
		case "textDocument/linkedEditingRange":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"ranges": []map[string]any{
					{"start": map[string]any{"line": 2, "character": 5}, "end": map[string]any{"line": 2, "character": 11}},
					{"start": map[string]any{"line": 4, "character": 5}, "end": map[string]any{"line": 4, "character": 11}},
				},
				"wordPattern": "[A-Za-z_]+",
			}})
		case "textDocument/moniker":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"scheme":     "gomod",
				"identifier": "bridge.test/demo.BridgeWidget",
				"kind":       "export",
			}}})
		case "textDocument/semanticTokens/full":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"resultId": "bridge-full-1",
				"data":     []int{2, 5, 4, 12, 0},
			}})
		case "textDocument/semanticTokens/range":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"data": []int{0, 5, 6, 12, 0},
			}})
		case "textDocument/semanticTokens/full/delta":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"resultId": "bridge-full-2",
				"edits": []map[string]any{{
					"start":       0,
					"deleteCount": 5,
					"data":        []int{0, 2, 4, 8, 16},
				}},
			}})
		case "workspace/symbol":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"name": "BridgeWidget",
				"kind": 12,
				"location": map[string]any{
					"uri": currentURI,
					"range": map[string]any{
						"start": map[string]any{"line": 2, "character": 5},
						"end":   map[string]any{"line": 2, "character": 17},
					},
				},
			}}})
		case "workspaceSymbol/resolve":
			var symbol map[string]any
			_ = json.Unmarshal(msg.Params, &symbol)
			symbol["containerName"] = "bridge"
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": symbol})
		case "workspace/executeCommand":
			var params struct {
				Command   string `json:"command"`
				Arguments []any  `json:"arguments"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			configurationHandled := bridgeFakeLSPServerRequestHandled(reader, "bridge-configuration-check", "workspace/configuration", map[string]any{
				"items": []map[string]any{{"section": "gopls"}},
			})
			foldersHandled := bridgeFakeLSPServerRequestHandled(reader, "bridge-folders-check", "workspace/workspaceFolders", nil)
			registerHandled := bridgeFakeLSPServerRequestHandled(reader, "bridge-register-check", "client/registerCapability", map[string]any{
				"registrations": []map[string]any{{"id": "watch", "method": "workspace/didChangeWatchedFiles"}},
			})
			unregisterHandled := bridgeFakeLSPServerRequestHandled(reader, "bridge-unregister-check", "client/unregisterCapability", map[string]any{
				"unregisterations": []map[string]any{{"id": "watch", "method": "workspace/didChangeWatchedFiles"}},
			})
			progressHandled := bridgeFakeLSPServerRequestHandled(reader, "bridge-progress-check", "window/workDoneProgress/create", map[string]any{
				"token": "bridge-progress",
			})
			showDocumentHandled := bridgeFakeLSPServerRequestHandled(reader, "bridge-show-document-check", "window/showDocument", map[string]any{
				"uri":       currentURI,
				"takeFocus": false,
			})
			refreshHandled := bridgeFakeLSPServerRequestHandled(reader, "bridge-semantic-refresh-check", "workspace/semanticTokens/refresh", nil) &&
				bridgeFakeLSPServerRequestHandled(reader, "bridge-inlay-refresh-check", "workspace/inlayHint/refresh", nil) &&
				bridgeFakeLSPServerRequestHandled(reader, "bridge-code-lens-refresh-check", "workspace/codeLens/refresh", nil) &&
				bridgeFakeLSPServerRequestHandled(reader, "bridge-diagnostic-refresh-check", "workspace/diagnostic/refresh", nil)
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "method": "window/showMessage", "params": map[string]any{
				"type":    3,
				"message": "bridge execute notice",
			}})
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "method": "window/logMessage", "params": map[string]any{
				"type":    3,
				"message": "bridge execute log",
			}})
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "method": "telemetry/event", "params": map[string]any{
				"name": "bridge-execute",
			}})
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "method": "$/progress", "params": map[string]any{
				"token": "bridge-progress",
				"value": map[string]any{"kind": "report", "message": "running"},
			}})
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": "bridge-apply-execute", "method": "workspace/applyEdit", "params": map[string]any{
				"label": "bridge execute preview",
				"edit": map[string]any{
					"changes": map[string]any{
						currentURI: []map[string]any{{
							"range": map[string]any{
								"start": map[string]any{"line": 2, "character": 0},
								"end":   map[string]any{"line": 2, "character": 14},
							},
							"newText": "func bridgeExecutePreview() {}",
						}},
					},
				},
			}})
			_, _ = readBridgeTestLSPMessage(reader)
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"status":               "ok",
				"command":              params.Command,
				"arguments":            params.Arguments,
				"configurationHandled": configurationHandled,
				"foldersHandled":       foldersHandled,
				"registerHandled":      registerHandled,
				"unregisterHandled":    unregisterHandled,
				"progressHandled":      progressHandled,
				"showDocumentHandled":  showDocumentHandled,
				"refreshHandled":       refreshHandled,
			}})
		case "textDocument/rangeFormatting":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 14},
				},
				"newText": "func bridgeRangeFormatted() {}\n",
			}}})
		case "textDocument/onTypeFormatting":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 14},
				},
				"newText": "func bridgeOnTypeFormatted() {}\n",
			}}})
		case "textDocument/willSaveWaitUntil":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": []map[string]any{{
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 14},
				},
				"newText": "func bridgeSaved() {}\n",
			}}})
		case "shutdown":
			_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": nil})
			return
		default:
			if msg.ID != nil {
				_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": nil})
			}
		}
	}
}

func readBridgeTestLSPMessage(reader *bufio.Reader) (json.RawMessage, error) {
	contentLength := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			continue
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return nil, err
		}
		contentLength = parsed
	}
	if contentLength <= 0 {
		return nil, io.ErrUnexpectedEOF
	}
	data := make([]byte, contentLength)
	_, err := io.ReadFull(reader, data)
	return data, err
}

func writeBridgeTestLSPMessage(writer io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n", len(data)); err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

func bridgeFakeLSPServerRequestHandled(reader *bufio.Reader, id string, method string, params any) bool {
	_ = writeBridgeTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	raw, err := readBridgeTestLSPMessage(reader)
	if err != nil {
		return false
	}
	var response struct {
		ID    any `json:"id"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return false
	}
	return response.Error == nil && fmt.Sprint(response.ID) == id
}

func bridgeTestLSPDocumentURI(params json.RawMessage) string {
	var payload struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	_ = json.Unmarshal(params, &payload)
	return payload.TextDocument.URI
}

func jsonEscape(value string) string {
	data, _ := json.Marshal(value)
	return strings.Trim(string(data), `"`)
}

func TestBridgeMCPMethods(t *testing.T) {
	workspace := t.TempDir()
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	servers := map[string]config.MCPServerConfig{
		"test": {
			Command: os.Args[0],
			Args:    []string{"-test.run", "^TestBridgeMCPHelperProcess$"},
			Env:     []string{"CODOG_BRIDGE_MCP_HELPER=1"},
		},
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"mcp/list","params":{"inspect":false}}`,
		`{"jsonrpc":"2.0","id":2,"method":"mcp/show","params":{"server":"test"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"mcp/tools","params":{"server":"test"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"mcp/call","params":{"server":"test","tool":"echo","arguments":{"text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"mcp/resources","params":{"server":"test"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"mcp/resource-templates","params":{"server":"test"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"mcp/read","params":{"server":"test","uri":"codog://note"}}`,
		`{"jsonrpc":"2.0","id":8,"method":"mcp/prompts","params":{"server":"test"}}`,
		`{"jsonrpc":"2.0","id":9,"method":"mcp/prompt","params":{"server":"test","prompt":"review","arguments":{"topic":"hooks"}}}`,
		`{"jsonrpc":"2.0","id":10,"method":"mcp/auth","params":{"server":"test"}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test", Workspace: workspace, MCPServers: servers}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"kind":"mcp_list"`)
	require.Contains(t, out.String(), `"servers":["test"]`)
	require.Contains(t, out.String(), `"kind":"mcp_show"`)
	require.Contains(t, out.String(), `"descriptor"`)
	require.Contains(t, out.String(), `"kind":"mcp_tools"`)
	require.Contains(t, out.String(), `"name":"echo"`)
	require.Contains(t, out.String(), `"description":"Echo text."`)
	require.Contains(t, out.String(), `"kind":"mcp_call"`)
	require.Contains(t, out.String(), `"text":"hi bridge"`)
	require.Contains(t, out.String(), `"kind":"mcp_resources"`)
	require.Contains(t, out.String(), `"uri":"codog://note"`)
	require.Contains(t, out.String(), `"kind":"mcp_resource_templates"`)
	require.Contains(t, out.String(), `"uriTemplate":"codog://notes/{name}"`)
	require.Contains(t, out.String(), `"kind":"mcp_read"`)
	require.Contains(t, out.String(), `"text":"note body"`)
	require.Contains(t, out.String(), `"kind":"mcp_prompts"`)
	require.Contains(t, out.String(), `"name":"review"`)
	require.Contains(t, out.String(), `"kind":"mcp_prompt"`)
	require.Contains(t, out.String(), `"text":"Review hooks"`)
	require.Contains(t, out.String(), `"kind":"mcp_auth"`)
	require.Contains(t, out.String(), `"status":"ok"`)
}

func TestBridgeMCPRejectsInvalidInputs(t *testing.T) {
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"mcp/show","params":{"server":"missing"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"mcp/call","params":{"server":"missing"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"mcp/read","params":{"server":"missing"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"mcp/prompt","params":{"server":"missing"}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test"}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), "no mcp servers configured")
	require.Contains(t, out.String(), "tool is required")
	require.Contains(t, out.String(), "uri is required")
	require.Contains(t, out.String(), "prompt is required")
}

func TestBridgeMCPHelperProcess(t *testing.T) {
	if os.Getenv("CODOG_BRIDGE_MCP_HELPER") != "1" {
		return
	}
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		line := reader.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var req map[string]any
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		method, _ := req["method"].(string)
		id := req["id"]
		switch method {
		case "initialize":
			writeBridgeMCP(id, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "bridge-test", "version": "0.0.0"},
			})
		case "tools/list":
			writeBridgeMCP(id, map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description": "Echo text.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"text": map[string]any{"type": "string"},
					},
				},
			}}})
		case "tools/call":
			writeBridgeMCP(id, map[string]any{"content": []map[string]any{{"type": "text", "text": "hi bridge"}}})
		case "resources/list":
			writeBridgeMCP(id, map[string]any{"resources": []map[string]any{{"uri": "codog://note", "name": "note"}}})
		case "resources/templates/list":
			writeBridgeMCP(id, map[string]any{"resourceTemplates": []map[string]any{{
				"uriTemplate": "codog://notes/{name}",
				"name":        "note by name",
			}}})
		case "resources/read":
			writeBridgeMCP(id, map[string]any{"contents": []map[string]any{{"uri": "codog://note", "text": "note body"}}})
		case "prompts/list":
			writeBridgeMCP(id, map[string]any{"prompts": []map[string]any{{
				"name":        "review",
				"description": "Review a topic.",
				"arguments": []map[string]any{{
					"name":     "topic",
					"required": true,
				}},
			}}})
		case "prompts/get":
			writeBridgeMCP(id, map[string]any{"messages": []map[string]any{{
				"role": "user",
				"content": map[string]any{
					"type": "text",
					"text": "Review hooks",
				},
			}}})
		}
	}
	os.Exit(0)
}

func writeBridgeMCP(id any, result map[string]any) {
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	data, _ := json.Marshal(payload)
	fmt.Println(string(data))
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

func TestBridgeFaultsRecordListAndClear(t *testing.T) {
	configHome := t.TempDir()
	server := Server{ConfigHome: configHome}

	event, err := server.RecordBridgeFault("poll", []string{"404"})
	require.NoError(t, err)
	require.NotEmpty(t, event.ID)
	require.Equal(t, "poll", event.Action)
	require.Equal(t, []string{"404"}, event.Args)
	require.Equal(t, "polling", event.Category)
	require.Equal(t, "warn", event.Severity)
	require.True(t, event.Recoverable)
	require.Contains(t, event.Message, "404")
	require.Contains(t, event.Remediation, "retry polling")
	require.NotContains(t, event.Message, "simulated")

	events, err := server.BridgeFaults()
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.FileExists(t, filepath.Join(configHome, "bridge", "faults.json"))

	require.NoError(t, server.ClearBridgeFaults())
	events, err = server.BridgeFaults()
	require.NoError(t, err)
	require.Empty(t, events)
	require.NoFileExists(t, filepath.Join(configHome, "bridge", "faults.json"))
}

func TestBridgeFaultsJSONRPC(t *testing.T) {
	configHome := t.TempDir()
	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"bridge/faults/record","params":{"action":"latency","args":["250ms"]}}`,
		`{"jsonrpc":"2.0","id":2,"method":"bridge/faults/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"bridge/faults/clear"}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	err := Server{Sessions: store, ConfigHome: configHome}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"kind":"bridge_faults"`)
	require.Contains(t, out.String(), `"action":"latency"`)
	require.Contains(t, out.String(), `"category":"latency"`)
	require.Contains(t, out.String(), `"severity":"warn"`)
	require.Contains(t, out.String(), `"recoverable":true`)
	require.Contains(t, out.String(), `"remediation"`)
	require.Contains(t, out.String(), `"250ms"`)
	require.Contains(t, out.String(), `"cleared":true`)
	require.NoFileExists(t, filepath.Join(configHome, "bridge", "faults.json"))
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

func TestBridgeBackgroundLifecycleControls(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	bgDir := filepath.Join(configHome, "background")
	require.NoError(t, os.MkdirAll(bgDir, 0o755))
	now := time.Now().UTC().Truncate(time.Second)
	activeLog := filepath.Join(bgDir, "active.log")
	oldLog := filepath.Join(bgDir, "old.log")
	failedLog := filepath.Join(bgDir, "failed.log")
	require.NoError(t, os.WriteFile(activeLog, []byte("active"), 0o644))
	require.NoError(t, os.WriteFile(oldLog, []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(failedLog, []byte("failed"), 0o644))
	oldCompleted := now.Add(-48 * time.Hour)
	tasks := []background.Task{
		{
			ID:        "active",
			Command:   "sleep 30",
			Status:    "running",
			PID:       os.Getpid(),
			StartedAt: now.Add(-time.Minute),
			LogPath:   activeLog,
		},
		{
			ID:          "old",
			Command:     "printf old",
			Status:      "completed",
			StartedAt:   oldCompleted.Add(-time.Minute),
			CompletedAt: &oldCompleted,
			LogPath:     oldLog,
		},
		{
			ID:            "failed",
			Command:       "printf bridge-supervise",
			Status:        "failed",
			Workspace:     workspace,
			StartedAt:     now.Add(-time.Minute),
			CompletedAt:   &now,
			LogPath:       failedLog,
			RestartPolicy: &background.RestartPolicy{Enabled: true, MaxAttempts: 1},
		},
	}
	for _, task := range tasks {
		data, err := json.MarshalIndent(task, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(bgDir, task.ID+".json"), append(data, '\n'), 0o644))
	}

	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	observedAt := now.Format(time.RFC3339)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"background/heartbeat","params":{"id":"active","status":"working","transport_alive":true,"observed_at":"` + observedAt + `"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"background/board","params":{"stalled_after_seconds":3600}}`,
		`{"jsonrpc":"2.0","id":3,"method":"background/supervise","params":{"now":"` + observedAt + `"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"background/prune","params":{"older_than_days":1,"keep":0}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	err := Server{Sessions: store, Version: "test", Workspace: workspace, ConfigHome: configHome}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"heartbeat":{"observed_at":"`+observedAt+`","transport_alive":true,"status":"working"}`)
	require.Contains(t, out.String(), `"active":[{"task_id":"active"`)
	require.Contains(t, out.String(), `"freshness":"healthy"`)
	require.Contains(t, out.String(), `"restarted_from":"failed"`)
	require.Contains(t, out.String(), `"removed":["old"]`)
	require.NoFileExists(t, filepath.Join(bgDir, "old.json"))
	require.NoFileExists(t, oldLog)
	bg := background.NewStore(configHome)
	require.Eventually(t, func() bool {
		tasks, err := bg.List()
		if err != nil {
			return false
		}
		for _, task := range tasks {
			if task.RestartedFrom == "failed" {
				return task.Status == "completed" || task.Status == "failed"
			}
		}
		return false
	}, 5*time.Second, 25*time.Millisecond)
}

func TestBridgeAgentRunsLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	bg := background.NewStore(configHome)
	runs := agentruns.NewStore(configHome)
	task, err := bg.RunWithOptions("printf agent-bridge", workspace, background.RunOptions{Kind: "agent", AgentType: "reviewer", SessionID: "session-bridge"})
	require.NoError(t, err)
	run, err := runs.Save(agentruns.Run{
		ID:        "run-" + task.ID,
		Agent:     "reviewer",
		Workspace: workspace,
		SessionID: "session-bridge",
		TaskID:    task.ID,
		CreatedAt: task.StartedAt,
		UpdatedAt: task.StartedAt,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		logs, err := bg.Logs(task.ID, 4096)
		return err == nil && strings.Contains(logs, "agent-bridge")
	}, 2*time.Second, 50*time.Millisecond)

	now := time.Now().UTC().Truncate(time.Second)
	orphan, err := runs.Save(agentruns.Run{
		ID:        "run-orphan",
		Agent:     "reviewer",
		SessionID: "session-bridge",
		TaskID:    "missing-task",
		CreatedAt: now.Add(-2 * time.Hour),
		UpdatedAt: now.Add(-2 * time.Hour),
	})
	require.NoError(t, err)
	longTask, err := bg.RunWithOptions("sleep 5", workspace, background.RunOptions{Kind: "agent", AgentType: "reviewer"})
	require.NoError(t, err)
	longRun, err := runs.Save(agentruns.Run{
		ID:        "run-" + longTask.ID,
		Agent:     "reviewer",
		Workspace: workspace,
		TaskID:    longTask.ID,
		CreatedAt: longTask.StartedAt,
		UpdatedAt: longTask.StartedAt,
	})
	require.NoError(t, err)

	observedAt := now.Format(time.RFC3339)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"agent-runs/list","params":{"agent":"reviewer","session_id":"session-bridge"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"agent-runs/get","params":{"id":"` + run.ID + `"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"agent-runs/logs","params":{"id":"` + run.ID + `","limit":4096}}`,
		`{"jsonrpc":"2.0","id":4,"method":"agent-runs/heartbeat","params":{"id":"` + run.ID + `","status":"working","transport_alive":true,"observed_at":"` + observedAt + `"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"agent-runs/board","params":{"stalled_after_seconds":60}}`,
		`{"jsonrpc":"2.0","id":6,"method":"agent-runs/prune","params":{"older_than_days":0,"keep":0}}`,
		`{"jsonrpc":"2.0","id":7,"method":"agent-runs/stop","params":{"id":"` + longRun.ID + `"}}`,
	}, "\n") + "\n"

	store := &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	var out bytes.Buffer
	err = Server{Sessions: store, Version: "test", Workspace: workspace, ConfigHome: configHome}.Serve(strings.NewReader(input), &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"id":"`+run.ID+`"`)
	require.Contains(t, out.String(), `"current_status":"completed"`)
	require.Contains(t, out.String(), `"freshness":"unknown"`)
	require.Contains(t, out.String(), `"health":{"state":"finished"`)
	require.Contains(t, out.String(), `"logs":"agent-bridge"`)
	require.Contains(t, out.String(), `"heartbeat":{"observed_at":"`+observedAt+`","transport_alive":true,"status":"working"}`)
	require.Contains(t, out.String(), `"active":[{"run":`)
	require.Contains(t, out.String(), `"removed":["`+orphan.ID+`"]`)
	require.Contains(t, out.String(), `"status":"stopped"`)
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
