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
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// LSPQueryRequest describes one language-server query against a document.
type LSPQueryRequest struct {
	Action          string `json:"action"`
	Path            string `json:"path"`
	Line            int    `json:"line,omitempty"`
	Character       int    `json:"character,omitempty"`
	NewName         string `json:"new_name,omitempty"`
	CodeActionTitle string `json:"code_action_title,omitempty"`
	Apply           bool   `json:"apply,omitempty"`
}

// LSPQueryResult is the normalized result of an LSP JSON-RPC request.
type LSPQueryResult struct {
	Kind        string          `json:"kind"`
	Language    string          `json:"language"`
	Action      string          `json:"action"`
	Method      string          `json:"method"`
	Path        string          `json:"path"`
	Result      any             `json:"result,omitempty"`
	Diagnostics []LSPDiagnostic `json:"diagnostics,omitempty"`
	TextEdits   int             `json:"text_edits,omitempty"`
	FileEdits   int             `json:"file_edits,omitempty"`
	Edits       []LSPFileEdit   `json:"edits,omitempty"`
	Changed     bool            `json:"changed,omitempty"`
	Applied     bool            `json:"applied,omitempty"`
	Content     string          `json:"content,omitempty"`
}

// LSPFileEdit previews the text edits a language server returned for one file.
type LSPFileEdit struct {
	Path        string `json:"path"`
	AbsPath     string `json:"-"`
	ActionTitle string `json:"action_title,omitempty"`
	ActionKind  string `json:"action_kind,omitempty"`
	TextEdits   int    `json:"text_edits"`
	Changed     bool   `json:"changed"`
	Content     string `json:"content,omitempty"`
}

type lspClient struct {
	stdin         io.Writer
	stdout        *bufio.Reader
	nextID        int
	notifications []lspRPCMessage
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

type lspTextEdit struct {
	Range struct {
		Start LSPPosition `json:"start"`
		End   LSPPosition `json:"end"`
	} `json:"range"`
	NewText string `json:"newText"`
}

type lspWorkspaceEdit struct {
	Changes         map[string][]lspTextEdit `json:"changes,omitempty"`
	DocumentChanges []json.RawMessage        `json:"documentChanges,omitempty"`
}

// LSPPosition is a zero-based language-server document position.
type LSPPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// LSPDiagnostic describes a language-server diagnostic entry.
type LSPDiagnostic struct {
	Range struct {
		Start LSPPosition `json:"start"`
		End   LSPPosition `json:"end"`
	} `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Code     any    `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

type lspPublishDiagnosticsParams struct {
	URI         string          `json:"uri"`
	Diagnostics []LSPDiagnostic `json:"diagnostics"`
}

// LSPActionInfo describes a supported high-level language-server action.
type LSPActionInfo struct {
	Name             string   `json:"name"`
	Method           string   `json:"method,omitempty"`
	Aliases          []string `json:"aliases,omitempty"`
	RequiresDocument bool     `json:"requires_document"`
	RequiresPosition bool     `json:"requires_position"`
	Description      string   `json:"description,omitempty"`
}

var lspActionInfos = []LSPActionInfo{
	{
		Name:             "diagnostics",
		Aliases:          []string{"diagnostic", "publish-diagnostics", "publishDiagnostics"},
		RequiresDocument: true,
		Description:      "Open a document and return diagnostics published by the language server.",
	},
	{
		Name:             "hover",
		Method:           "textDocument/hover",
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return hover information at a document position.",
	},
	{
		Name:             "definition",
		Method:           "textDocument/definition",
		Aliases:          []string{"goto_definition", "goto-definition", "go-to-definition", "gotoDefinition"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return definitions for the symbol at a document position.",
	},
	{
		Name:             "declaration",
		Method:           "textDocument/declaration",
		Aliases:          []string{"goto_declaration", "goto-declaration", "go-to-declaration", "gotoDeclaration"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return declarations for the symbol at a document position.",
	},
	{
		Name:             "implementation",
		Method:           "textDocument/implementation",
		Aliases:          []string{"goto_implementation", "goto-implementation", "go-to-implementation", "gotoImplementation"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return implementations for the symbol at a document position.",
	},
	{
		Name:             "type-definition",
		Method:           "textDocument/typeDefinition",
		Aliases:          []string{"type_definition", "typeDefinition", "goto_type_definition", "goto-type-definition", "go-to-type-definition", "gotoTypeDefinition"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return type definitions for the symbol at a document position.",
	},
	{
		Name:             "references",
		Method:           "textDocument/references",
		Aliases:          []string{"find_references", "find-references", "findReferences"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return references for the symbol at a document position.",
	},
	{
		Name:             "rename",
		Method:           "textDocument/rename",
		Aliases:          []string{"rename_symbol", "rename-symbol", "renameSymbol", "symbol-rename", "symbol_rename"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return a workspace edit preview for renaming the symbol at a document position.",
	},
	{
		Name:             "prepare-rename",
		Method:           "textDocument/prepareRename",
		Aliases:          []string{"prepare_rename", "prepareRename", "rename_prepare", "rename-prepare"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Check whether a symbol position can be renamed and return the rename range or placeholder.",
	},
	{
		Name:             "code-action",
		Method:           "textDocument/codeAction",
		Aliases:          []string{"code_action", "codeAction", "quickfix", "quick-fix", "quick_fix", "fixit", "fix-it"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return code actions and quick fixes for a document position.",
	},
	{
		Name:             "completion",
		Method:           "textDocument/completion",
		Aliases:          []string{"completions", "complete"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return completion candidates at a document position.",
	},
	{
		Name:             "document-highlight",
		Method:           "textDocument/documentHighlight",
		Aliases:          []string{"document_highlight", "documentHighlight", "highlights", "highlight"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return document highlights for the symbol at a document position.",
	},
	{
		Name:             "selection-range",
		Method:           "textDocument/selectionRange",
		Aliases:          []string{"selection_range", "selectionRange", "expand_selection", "expand-selection"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return nested selection ranges at a document position.",
	},
	{
		Name:             "folding-range",
		Method:           "textDocument/foldingRange",
		Aliases:          []string{"folding_range", "foldingRange", "folds", "folding"},
		RequiresDocument: true,
		Description:      "Return foldable line ranges for a document.",
	},
	{
		Name:             "document-link",
		Method:           "textDocument/documentLink",
		Aliases:          []string{"document_link", "documentLink", "links", "document-links", "document_links"},
		RequiresDocument: true,
		Description:      "Return links and link targets discovered in a document.",
	},
	{
		Name:             "inlay-hint",
		Method:           "textDocument/inlayHint",
		Aliases:          []string{"inlay_hint", "inlayHint", "hints", "inlay-hints", "inlay_hints"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return inlay hints around a document position.",
	},
	{
		Name:             "linked-editing-range",
		Method:           "textDocument/linkedEditingRange",
		Aliases:          []string{"linked_editing_range", "linkedEditingRange", "linked-editing", "linked_editing"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return ranges that should be edited together at a document position.",
	},
	{
		Name:             "signature-help",
		Method:           "textDocument/signatureHelp",
		Aliases:          []string{"signature_help", "signatureHelp", "signature", "signatures"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return call signature help at a document position.",
	},
	{
		Name:             "symbols",
		Method:           "textDocument/documentSymbol",
		Aliases:          []string{"document_symbols", "document-symbols", "documentSymbols"},
		RequiresDocument: true,
		Description:      "Return document symbols for a file.",
	},
	{
		Name:             "format",
		Method:           "textDocument/formatting",
		Aliases:          []string{"formatting", "format_document", "document_formatting", "document-formatting", "documentFormatting"},
		RequiresDocument: true,
		Description:      "Return formatted document text using LSP text edits.",
	},
}

// SupportedLSPActions returns supported LSP actions and aliases.
func SupportedLSPActions() []LSPActionInfo {
	out := make([]LSPActionInfo, len(lspActionInfos))
	for i, info := range lspActionInfos {
		info.Aliases = append([]string(nil), info.Aliases...)
		out[i] = info
	}
	return out
}

// NormalizeLSPAction returns the canonical name for an LSP action alias.
func NormalizeLSPAction(action string) (string, error) {
	normalized := normalizeLSPActionToken(action)
	for _, info := range lspActionInfos {
		if normalizeLSPActionToken(info.Name) == normalized {
			return info.Name, nil
		}
		for _, alias := range info.Aliases {
			if normalizeLSPActionToken(alias) == normalized {
				return info.Name, nil
			}
		}
	}
	return "", fmt.Errorf("unknown lsp action %q; supported actions: %s", action, strings.Join(supportedLSPActionNames(), ", "))
}

func supportedLSPActionNames() []string {
	names := make([]string, 0, len(lspActionInfos))
	for _, info := range lspActionInfos {
		names = append(names, info.Name)
	}
	return names
}

func normalizeLSPActionToken(action string) string {
	action = strings.TrimSpace(action)
	var b strings.Builder
	for index, r := range action {
		if r == '-' || r == '_' || r == ' ' {
			continue
		}
		if r >= 'A' && r <= 'Z' && index > 0 {
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// Query starts a configured stdio LSP command and runs one document query.
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
	if strings.TrimSpace(command) == "" {
		return LSPQueryResult{}, errors.New("lsp command is required")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		timeout := 10 * time.Second
		if action == "diagnostics" {
			timeout = 3 * time.Second
		}
		ctx, cancel = context.WithTimeout(ctx, timeout)
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
	if action == "diagnostics" {
		diagnostics, err := client.waitForDiagnostics(uri)
		if err != nil {
			if ctx.Err() != nil {
				diagnostics = []LSPDiagnostic{}
			} else {
				return LSPQueryResult{}, err
			}
		}
		return LSPQueryResult{
			Kind:        "lsp_query",
			Language:    language,
			Action:      action,
			Method:      "textDocument/publishDiagnostics",
			Path:        rel,
			Result:      diagnostics,
			Diagnostics: diagnostics,
		}, nil
	}
	method, params, err := lspMethodParams(action, uri, request.Line, request.Character, request.NewName)
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
	result := LSPQueryResult{
		Kind:     "lsp_query",
		Language: language,
		Action:   action,
		Method:   method,
		Path:     rel,
		Result:   decoded,
	}
	if action == "format" && len(raw) > 0 && string(raw) != "null" {
		var edits []lspTextEdit
		if err := json.Unmarshal(raw, &edits); err != nil {
			return LSPQueryResult{}, err
		}
		formatted, err := applyLSPTextEdits(string(data), edits)
		if err != nil {
			return LSPQueryResult{}, err
		}
		result.TextEdits = len(edits)
		result.Content = formatted
		result.Changed = formatted != string(data)
	}
	if action == "rename" && len(raw) > 0 && string(raw) != "null" {
		var edit lspWorkspaceEdit
		if err := json.Unmarshal(raw, &edit); err != nil {
			return LSPQueryResult{}, err
		}
		fileEdits, textEdits, err := summarizeLSPWorkspaceEdit(workspace, edit)
		if err != nil {
			return LSPQueryResult{}, err
		}
		result.FileEdits = len(fileEdits)
		result.TextEdits = textEdits
		result.Edits = fileEdits
		result.Changed = textEdits > 0
		if request.Apply && result.Changed {
			if err := applyLSPFileEdits(fileEdits); err != nil {
				return LSPQueryResult{}, err
			}
			result.Applied = true
		}
	}
	if action == "code-action" && len(raw) > 0 && string(raw) != "null" {
		fileEdits, textEdits, actionEdits, err := summarizeLSPCodeActionEdits(workspace, raw, request.CodeActionTitle)
		if err != nil {
			return LSPQueryResult{}, err
		}
		result.FileEdits = len(fileEdits)
		result.TextEdits = textEdits
		result.Edits = fileEdits
		result.Changed = textEdits > 0
		if request.Apply {
			if actionEdits != 1 {
				return LSPQueryResult{}, fmt.Errorf("lsp code-action apply requires exactly one matching edit-bearing action, got %d", actionEdits)
			}
			if err := applyLSPFileEdits(fileEdits); err != nil {
				return LSPQueryResult{}, err
			}
			result.Applied = true
		}
	}
	return result, nil
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
			if msg.Method != "" {
				c.notifications = append(c.notifications, msg)
			}
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

func (c *lspClient) waitForDiagnostics(uri string) ([]LSPDiagnostic, error) {
	for {
		if diagnostics, ok, err := c.popDiagnostics(uri); ok || err != nil {
			return diagnostics, err
		}
		raw, err := readLSPMessage(c.stdout)
		if err != nil {
			return nil, err
		}
		var msg lspRPCMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			return nil, err
		}
		c.notifications = append(c.notifications, msg)
	}
}

func (c *lspClient) popDiagnostics(uri string) ([]LSPDiagnostic, bool, error) {
	for i, msg := range c.notifications {
		if msg.Method != "textDocument/publishDiagnostics" {
			continue
		}
		var params lspPublishDiagnosticsParams
		if err := decodeLSPParams(msg.Params, &params); err != nil {
			return nil, true, err
		}
		if params.URI != "" && params.URI != uri {
			continue
		}
		c.notifications = append(c.notifications[:i], c.notifications[i+1:]...)
		if params.Diagnostics == nil {
			params.Diagnostics = []LSPDiagnostic{}
		}
		return params.Diagnostics, true, nil
	}
	return nil, false, nil
}

func decodeLSPParams(params any, out any) error {
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func lspMethodParams(action string, uri string, line int, character int, newName string) (string, any, error) {
	position := map[string]any{"line": max(0, line), "character": max(0, character)}
	textDocument := map[string]any{"uri": uri}
	switch action {
	case "hover":
		return "textDocument/hover", map[string]any{"textDocument": textDocument, "position": position}, nil
	case "definition":
		return "textDocument/definition", map[string]any{"textDocument": textDocument, "position": position}, nil
	case "declaration":
		return "textDocument/declaration", map[string]any{"textDocument": textDocument, "position": position}, nil
	case "implementation":
		return "textDocument/implementation", map[string]any{"textDocument": textDocument, "position": position}, nil
	case "type-definition":
		return "textDocument/typeDefinition", map[string]any{"textDocument": textDocument, "position": position}, nil
	case "references":
		return "textDocument/references", map[string]any{"textDocument": textDocument, "position": position, "context": map[string]any{"includeDeclaration": true}}, nil
	case "rename":
		newName = strings.TrimSpace(newName)
		if newName == "" {
			return "", nil, errors.New("new_name is required for lsp rename")
		}
		return "textDocument/rename", map[string]any{"textDocument": textDocument, "position": position, "newName": newName}, nil
	case "prepare-rename":
		return "textDocument/prepareRename", map[string]any{"textDocument": textDocument, "position": position}, nil
	case "code-action":
		return "textDocument/codeAction", map[string]any{
			"textDocument": textDocument,
			"range":        map[string]any{"start": position, "end": position},
			"context":      map[string]any{"diagnostics": []any{}},
		}, nil
	case "completion":
		return "textDocument/completion", map[string]any{"textDocument": textDocument, "position": position, "context": map[string]any{"triggerKind": 1}}, nil
	case "document-highlight":
		return "textDocument/documentHighlight", map[string]any{"textDocument": textDocument, "position": position}, nil
	case "selection-range":
		return "textDocument/selectionRange", map[string]any{"textDocument": textDocument, "positions": []any{position}}, nil
	case "folding-range":
		return "textDocument/foldingRange", map[string]any{"textDocument": textDocument}, nil
	case "document-link":
		return "textDocument/documentLink", map[string]any{"textDocument": textDocument}, nil
	case "inlay-hint":
		line = max(0, line)
		start := map[string]any{"line": line, "character": 0}
		end := map[string]any{"line": line, "character": max(0, character)}
		return "textDocument/inlayHint", map[string]any{"textDocument": textDocument, "range": map[string]any{"start": start, "end": end}}, nil
	case "linked-editing-range":
		return "textDocument/linkedEditingRange", map[string]any{"textDocument": textDocument, "position": position}, nil
	case "signature-help":
		return "textDocument/signatureHelp", map[string]any{"textDocument": textDocument, "position": position, "context": map[string]any{"triggerKind": 1}}, nil
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

func summarizeLSPWorkspaceEdit(workspace string, edit lspWorkspaceEdit) ([]LSPFileEdit, int, error) {
	editsByURI := collectLSPWorkspaceEdits(edit)
	uris := make([]string, 0, len(editsByURI))
	for uri := range editsByURI {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	out := make([]LSPFileEdit, 0, len(uris))
	totalTextEdits := 0
	for _, uri := range uris {
		path, err := lspFileURIPath(uri)
		if err != nil {
			return nil, 0, err
		}
		abs, rel, err := resolveWorkspaceFile(workspace, path)
		if err != nil {
			return nil, 0, err
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, 0, err
		}
		textEdits := editsByURI[uri]
		content, err := applyLSPTextEdits(string(data), textEdits)
		if err != nil {
			return nil, 0, err
		}
		totalTextEdits += len(textEdits)
		out = append(out, LSPFileEdit{
			Path:      rel,
			AbsPath:   abs,
			TextEdits: len(textEdits),
			Changed:   content != string(data),
			Content:   content,
		})
	}
	return out, totalTextEdits, nil
}

func applyLSPFileEdits(edits []LSPFileEdit) error {
	for _, edit := range edits {
		if !edit.Changed {
			continue
		}
		if strings.TrimSpace(edit.AbsPath) == "" {
			return fmt.Errorf("cannot apply lsp edit for %s without an absolute path", edit.Path)
		}
		info, statErr := os.Stat(edit.AbsPath)
		mode := os.FileMode(0o644)
		if statErr == nil {
			mode = info.Mode()
		}
		if err := os.WriteFile(edit.AbsPath, []byte(edit.Content), mode); err != nil {
			return err
		}
	}
	return nil
}

func summarizeLSPCodeActionEdits(workspace string, raw json.RawMessage, titleFilter string) ([]LSPFileEdit, int, int, error) {
	var actions []struct {
		Title string           `json:"title"`
		Kind  string           `json:"kind"`
		Edit  lspWorkspaceEdit `json:"edit"`
	}
	if err := json.Unmarshal(raw, &actions); err != nil {
		return nil, 0, 0, err
	}
	titleFilter = strings.TrimSpace(titleFilter)
	out := []LSPFileEdit{}
	totalTextEdits := 0
	actionEdits := 0
	for _, action := range actions {
		if titleFilter != "" && !strings.EqualFold(strings.TrimSpace(action.Title), titleFilter) {
			continue
		}
		fileEdits, textEdits, err := summarizeLSPWorkspaceEdit(workspace, action.Edit)
		if err != nil {
			return nil, 0, 0, err
		}
		if textEdits == 0 {
			continue
		}
		for index := range fileEdits {
			fileEdits[index].ActionTitle = action.Title
			fileEdits[index].ActionKind = action.Kind
		}
		out = append(out, fileEdits...)
		totalTextEdits += textEdits
		actionEdits++
	}
	return out, totalTextEdits, actionEdits, nil
}

func collectLSPWorkspaceEdits(edit lspWorkspaceEdit) map[string][]lspTextEdit {
	out := map[string][]lspTextEdit{}
	for uri, edits := range edit.Changes {
		if strings.TrimSpace(uri) == "" || len(edits) == 0 {
			continue
		}
		out[uri] = append(out[uri], edits...)
	}
	for _, raw := range edit.DocumentChanges {
		var textDocumentEdit struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Edits []lspTextEdit `json:"edits"`
		}
		if err := json.Unmarshal(raw, &textDocumentEdit); err != nil {
			continue
		}
		uri := strings.TrimSpace(textDocumentEdit.TextDocument.URI)
		if uri == "" || len(textDocumentEdit.Edits) == 0 {
			continue
		}
		out[uri] = append(out[uri], textDocumentEdit.Edits...)
	}
	return out
}

func lspFileURIPath(uri string) (string, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "file" {
		return "", fmt.Errorf("unsupported lsp workspace edit URI scheme %q", parsed.Scheme)
	}
	if parsed.Path == "" {
		return "", fmt.Errorf("lsp workspace edit URI has no path: %s", uri)
	}
	return filepath.FromSlash(parsed.Path), nil
}

func applyLSPTextEdits(source string, edits []lspTextEdit) (string, error) {
	type offsetEdit struct {
		start   int
		end     int
		newText string
	}
	offsets := make([]offsetEdit, 0, len(edits))
	for _, edit := range edits {
		start, err := lspOffset(source, edit.Range.Start.Line, edit.Range.Start.Character)
		if err != nil {
			return "", err
		}
		end, err := lspOffset(source, edit.Range.End.Line, edit.Range.End.Character)
		if err != nil {
			return "", err
		}
		if start > end {
			return "", errors.New("lsp text edit range is inverted")
		}
		offsets = append(offsets, offsetEdit{start: start, end: end, newText: edit.NewText})
	}
	sort.Slice(offsets, func(i, j int) bool {
		return offsets[i].start > offsets[j].start
	})
	out := source
	lastStart := len(out) + 1
	for _, edit := range offsets {
		if edit.end > lastStart {
			return "", errors.New("overlapping lsp text edits are not supported")
		}
		out = out[:edit.start] + edit.newText + out[edit.end:]
		lastStart = edit.start
	}
	return out, nil
}

func lspOffset(source string, line int, character int) (int, error) {
	if line < 0 || character < 0 {
		return 0, errors.New("lsp position cannot be negative")
	}
	offset := 0
	for currentLine := 0; currentLine < line; currentLine++ {
		next := strings.IndexByte(source[offset:], '\n')
		if next < 0 {
			return 0, fmt.Errorf("lsp line %d is out of range", line)
		}
		offset += next + 1
	}
	lineEnd := len(source)
	if next := strings.IndexByte(source[offset:], '\n'); next >= 0 {
		lineEnd = offset + next
	}
	currentCharacter := 0
	for byteOffset := offset; byteOffset < lineEnd; {
		if currentCharacter == character {
			return byteOffset, nil
		}
		_, size := utf8.DecodeRuneInString(source[byteOffset:lineEnd])
		byteOffset += size
		currentCharacter++
	}
	if currentCharacter == character {
		return lineEnd, nil
	}
	return 0, fmt.Errorf("lsp character %d is out of range", character)
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

// InferLanguageID returns an LSP language identifier for a file path.
func InferLanguageID(path string) string {
	return languageID("", path)
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
