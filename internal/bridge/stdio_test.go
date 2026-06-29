package bridge

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	require.Contains(t, out.String(), `"file/read"`)
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
