package codeintel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type LSPQueryRequest struct {
	Action    string `json:"action"`
	Path      string `json:"path"`
	Line      int    `json:"line,omitempty"`
	Character int    `json:"character,omitempty"`
}

type LSPQueryResult struct {
	Kind     string `json:"kind"`
	Language string `json:"language"`
	Action   string `json:"action"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Result   any    `json:"result,omitempty"`
}

type lspClient struct {
	stdin  io.Writer
	stdout *bufio.Reader
	nextID int
}

type lspRPCMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *lspRPCError    `json:"error,omitempty"`
}

type lspRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func NormalizeLSPAction(action string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "diagnostics":
		return "diagnostics", nil
	case "hover":
		return "hover", nil
	case "definition", "goto_definition":
		return "definition", nil
	case "references", "find_references":
		return "references", nil
	case "completion", "completions":
		return "completion", nil
	case "symbols", "document_symbols":
		return "symbols", nil
	case "format", "formatting":
		return "format", nil
	default:
		return "", fmt.Errorf("unknown lsp action %q", action)
	}
}

func (s LSPStore) Query(ctx context.Context, language string, request LSPQueryRequest) (LSPQueryResult, error) {
	language, err := normalizeLanguage(language)
	if err != nil {
		return LSPQueryResult{}, err
	}
	server, err := s.load(language)
	if err != nil {
		return LSPQueryResult{}, err
	}
	workspace := strings.TrimSpace(server.Workspace)
	if workspace == "" {
		workspace = s.Workspace
	}
	if workspace == "" {
		workspace, err = os.Getwd()
		if err != nil {
			return LSPQueryResult{}, err
		}
	}
	return runLSPQuery(ctx, workspace, server.Command, language, request)
}

func runLSPQuery(ctx context.Context, workspace string, command string, language string, request LSPQueryRequest) (LSPQueryResult, error) {
	action, err := NormalizeLSPAction(request.Action)
	if err != nil {
		return LSPQueryResult{}, err
	}
	if action == "diagnostics" {
		return LSPQueryResult{}, errors.New("lsp diagnostics are delivered as notifications; use codog diagnostics for now")
	}
	if strings.TrimSpace(command) == "" {
		return LSPQueryResult{}, errors.New("lsp command is required")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	path, rel, err := resolveWorkspaceFile(workspace, request.Path)
	if err != nil {
		return LSPQueryResult{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return LSPQueryResult{}, err
	}
	cmd := lspShellCommand(ctx, command)
	cmd.Dir = workspace
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return LSPQueryResult{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return LSPQueryResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return LSPQueryResult{}, err
	}
	client := &lspClient{stdin: stdin, stdout: bufio.NewReader(stdout)}
	wait := func() error {
		err := cmd.Wait()
		if err != nil && strings.TrimSpace(stderr.String()) != "" {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return err
	}
	defer func() {
		_, _ = client.request("shutdown", nil)
		_ = client.notify("exit", nil)
		_ = stdin.Close()
		_ = wait()
	}()
	rootURI := fileURI(workspace)
	if _, err := client.request("initialize", map[string]any{
		"processId": nil,
		"rootUri":   rootURI,
		"capabilities": map[string]any{
			"textDocument": map[string]any{},
			"workspace":    map[string]any{},
		},
		"clientInfo": map[string]any{"name": "codog"},
	}); err != nil {
		return LSPQueryResult{}, err
	}
	if err := client.notify("initialized", map[string]any{}); err != nil {
		return LSPQueryResult{}, err
	}
	uri := fileURI(path)
	if err := client.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": languageID(language, path),
			"version":    1,
			"text":       string(data),
		},
	}); err != nil {
		return LSPQueryResult{}, err
	}
	method, params, err := lspMethodParams(action, uri, request.Line, request.Character)
	if err != nil {
		return LSPQueryResult{}, err
	}
	raw, err := client.request(method, params)
	if err != nil {
		return LSPQueryResult{}, err
	}
	var decoded any
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return LSPQueryResult{}, err
		}
	}
	return LSPQueryResult{
		Kind:     "lsp_query",
		Language: language,
		Action:   action,
		Method:   method,
		Path:     rel,
		Result:   decoded,
	}, nil
}

func (c *lspClient) request(method string, params any) (json.RawMessage, error) {
	c.nextID++
	id := c.nextID
	if err := writeLSPMessage(c.stdin, lspRPCMessage{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	for {
		raw, err := readLSPMessage(c.stdout)
		if err != nil {
			return nil, err
		}
		var msg lspRPCMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			return nil, err
		}
		if !sameLSPID(msg.ID, id) {
			continue
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("lsp %s failed: %s", method, msg.Error.Message)
		}
		return msg.Result, nil
	}
}

func (c *lspClient) notify(method string, params any) error {
	return writeLSPMessage(c.stdin, lspRPCMessage{JSONRPC: "2.0", Method: method, Params: params})
}

func lspMethodParams(action string, uri string, line int, character int) (string, any, error) {
	position := map[string]any{"line": max(0, line), "character": max(0, character)}
	textDocument := map[string]any{"uri": uri}
	switch action {
	case "hover":
		return "textDocument/hover", map[string]any{"textDocument": textDocument, "position": position}, nil
	case "definition":
		return "textDocument/definition", map[string]any{"textDocument": textDocument, "position": position}, nil
	case "references":
		return "textDocument/references", map[string]any{"textDocument": textDocument, "position": position, "context": map[string]any{"includeDeclaration": true}}, nil
	case "completion":
		return "textDocument/completion", map[string]any{"textDocument": textDocument, "position": position, "context": map[string]any{"triggerKind": 1}}, nil
	case "symbols":
		return "textDocument/documentSymbol", map[string]any{"textDocument": textDocument}, nil
	case "format":
		return "textDocument/formatting", map[string]any{"textDocument": textDocument, "options": map[string]any{"tabSize": 4, "insertSpaces": false}}, nil
	default:
		return "", nil, fmt.Errorf("unsupported lsp action %q", action)
	}
}

func readLSPMessage(reader *bufio.Reader) (json.RawMessage, error) {
	length := -1
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
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, err
			}
			length = parsed
		}
	}
	if length < 0 {
		return nil, errors.New("missing LSP Content-Length header")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, err
	}
	return body, nil
}

func writeLSPMessage(writer io.Writer, value any) error {
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

func sameLSPID(value any, id int) bool {
	switch typed := value.(type) {
	case float64:
		return int(typed) == id
	case int:
		return typed == id
	case string:
		return typed == strconv.Itoa(id)
	default:
		return false
	}
}

func lspShellCommand(ctx context.Context, command string) *exec.Cmd {
	if isWindows() {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}

func isWindows() bool {
	return os.PathSeparator == '\\'
}

func fileURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
}

func languageID(language string, path string) string {
	if strings.TrimSpace(language) != "" {
		return language
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	default:
		return "plaintext"
	}
}
