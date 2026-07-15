package acceptance

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRealBinaryManagedLSPLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a test executable as a stdio language server")
	}
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	starts := filepath.Join(t.TempDir(), "lsp-starts.log")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
	extraEnv := []string{"CODOG_ACCEPTANCE_LSP_STARTS=" + starts}

	start := runCodogWithExtraEnv(t, bin, workspace, configHome, extraEnv, nil,
		"--output-format", "json", "code-intel", "lsp", "start", "go",
		os.Args[0], "-test.run=^TestAcceptanceFakeLSPServer$")
	require.Equal(t, 0, start.Code, start.Combined())
	require.Contains(t, start.Stdout, `"language": "go"`)
	require.Contains(t, start.Stdout, `"mode": "lazy"`)
	require.Contains(t, start.Stdout, `"status": "ready"`)

	status := runCodog(t, bin, workspace, configHome, nil,
		"--output-format", "json", "code-intel", "lsp", "status", "go")
	require.Equal(t, 0, status.Code, status.Combined())
	require.Contains(t, status.Stdout, `"status": "ready"`)

	query := runCodogWithExtraEnv(t, bin, workspace, configHome, extraEnv, nil,
		"--output-format", "json", "code-intel", "lsp", "query", "go", "hover", "main.go", "1", "0")
	require.Equal(t, 0, query.Code, query.Combined())
	require.Contains(t, query.Stdout, `"method": "textDocument/hover"`)
	require.Contains(t, query.Stdout, "acceptance hover")

	stop := runCodog(t, bin, workspace, configHome, nil,
		"--output-format", "json", "code-intel", "lsp", "stop", "go")
	require.Equal(t, 0, stop.Code, stop.Combined())
	require.Contains(t, stop.Stdout, `"status": "stopped"`)

	data, err := os.ReadFile(starts)
	require.NoError(t, err)
	require.Equal(t, "started\nstarted\n", string(data))
}

func TestAcceptanceFakeLSPServer(t *testing.T) {
	if starts := os.Getenv("CODOG_ACCEPTANCE_LSP_STARTS"); starts != "" {
		file, err := os.OpenFile(starts, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		require.NoError(t, err)
		_, err = io.WriteString(file, "started\n")
		require.NoError(t, err)
		require.NoError(t, file.Close())
	}

	reader := bufio.NewReader(os.Stdin)
	for {
		message, err := readAcceptanceLSPMessage(reader)
		if err != nil {
			return
		}
		var request struct {
			ID     any    `json:"id,omitempty"`
			Method string `json:"method,omitempty"`
		}
		if json.Unmarshal(message, &request) != nil {
			return
		}
		switch request.Method {
		case "initialize":
			writeAcceptanceLSPMessage(t, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"capabilities": map[string]any{}}})
		case "shutdown":
			writeAcceptanceLSPMessage(t, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": nil})
		case "textDocument/hover":
			writeAcceptanceLSPMessage(t, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"contents": "acceptance hover"}})
		case "exit":
			return
		default:
			if request.ID != nil {
				writeAcceptanceLSPMessage(t, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": nil})
			}
		}
	}
}

func readAcceptanceLSPMessage(reader *bufio.Reader) (json.RawMessage, error) {
	length := 0
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
		length, err = strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return nil, err
		}
	}
	if length <= 0 {
		return nil, io.ErrUnexpectedEOF
	}
	data := make([]byte, length)
	_, err := io.ReadFull(reader, data)
	return data, err
}

func writeAcceptanceLSPMessage(t *testing.T, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	_, err = io.WriteString(os.Stdout, "Content-Length: "+strconv.Itoa(len(data))+"\r\n\r\n")
	require.NoError(t, err)
	_, err = os.Stdout.Write(data)
	require.NoError(t, err)
}
