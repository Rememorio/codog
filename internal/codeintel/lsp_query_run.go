package codeintel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

type lspQueryRun struct {
	ctx       context.Context
	cancel    context.CancelFunc
	workspace string
	command   string
	language  string
	request   LSPQueryRequest
	action    string
	info      LSPActionInfo
	path      string
	rel       string
	data      []byte
	uri       string
	client    *lspClient
	stdin     io.WriteCloser
	wait      func() error
}

func newLSPQueryRun(ctx context.Context, workspace string, command string, language string, request LSPQueryRequest) (*lspQueryRun, error) {
	action, err := NormalizeLSPAction(request.Action)
	if err != nil {
		return nil, err
	}
	info, err := lookupLSPActionInfo(action)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(command) == "" {
		return nil, errors.New("lsp command is required")
	}
	ctx, cancel := lspQueryContext(ctx, action)
	run := &lspQueryRun{
		ctx: ctx, cancel: cancel, workspace: workspace, command: command,
		language: language, request: request, action: action, info: info,
	}
	if err := run.loadDocument(); err != nil {
		run.close()
		return nil, err
	}
	return run, nil
}

func lspQueryContext(ctx context.Context, action string) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	timeout := 10 * time.Second
	if action == "diagnostics" {
		timeout = 3 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

func (r *lspQueryRun) loadDocument() error {
	if !r.info.RequiresDocument {
		return nil
	}
	path, rel, err := resolveWorkspaceFile(r.workspace, r.request.Path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	r.path, r.rel, r.data, r.uri = path, rel, data, fileURI(path)
	return nil
}

func (r *lspQueryRun) start() error {
	cmd := lspShellCommand(r.ctx, r.command)
	cmd.Dir = r.workspace
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	r.stdin = stdin
	r.wait = func() error {
		err := cmd.Wait()
		if err != nil && strings.TrimSpace(stderr.String()) != "" {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return err
	}
	r.client = &lspClient{
		stdin: stdin, stdout: bufio.NewReader(stdout), workspace: r.workspace,
		applyWorkspaceEdits: r.request.Apply,
	}
	if err := r.initialize(); err != nil {
		return err
	}
	return r.openDocument()
}

func (r *lspQueryRun) initialize() error {
	_, err := r.client.request("initialize", map[string]any{
		"processId": nil,
		"rootUri":   fileURI(r.workspace),
		"capabilities": map[string]any{
			"textDocument": map[string]any{},
			"workspace":    map[string]any{},
		},
		"clientInfo": map[string]any{"name": "codog"},
	})
	if err != nil {
		return err
	}
	return r.client.notify("initialized", map[string]any{})
}

func (r *lspQueryRun) openDocument() error {
	if !r.info.RequiresDocument {
		return nil
	}
	return r.client.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": r.uri, "languageId": languageID(r.language, r.path),
			"version": 1, "text": string(r.data),
		},
	})
}

func (r *lspQueryRun) close() {
	if r.client != nil {
		_, _ = r.client.request("shutdown", nil)
		_ = r.client.notify("exit", nil)
	}
	if r.stdin != nil {
		_ = r.stdin.Close()
	}
	if r.wait != nil {
		_ = r.wait()
	}
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *lspQueryRun) execute() (LSPQueryResult, error) {
	if result, handled, err := r.executeDiagnostic(); handled {
		return result, err
	}
	if result, handled, err := r.executeResolveAction(); handled {
		return result, err
	}
	if result, handled, err := r.executeHierarchyAction(); handled {
		return result, err
	}
	if r.action == "color-presentation" {
		decoded, err := runLSPColorPresentationQuery(r.client, r.uri, r.request.Line, r.request.Character)
		return r.actionResult("textDocument/colorPresentation", decoded), err
	}
	return r.executeGeneric()
}

func (r *lspQueryRun) executeDiagnostic() (LSPQueryResult, bool, error) {
	if r.action != "diagnostics" {
		return LSPQueryResult{}, false, nil
	}
	diagnostics, err := r.client.waitForDiagnostics(r.uri)
	if err != nil && r.ctx.Err() == nil {
		return LSPQueryResult{}, true, err
	}
	if err != nil {
		diagnostics = []LSPDiagnostic{}
	}
	result := r.actionResult("textDocument/publishDiagnostics", diagnostics)
	result.Diagnostics = diagnostics
	return result, true, nil
}

func (r *lspQueryRun) executeResolveAction() (LSPQueryResult, bool, error) {
	var method string
	var decoded any
	var err error
	switch r.action {
	case "code-lens-resolve":
		method = "codeLens/resolve"
		decoded, err = runLSPCodeLensResolveQuery(r.client, r.uri, r.request.Line, r.request.Character)
	case "code-action-resolve":
		method = "codeAction/resolve"
		title := r.request.CodeActionTitle
		if strings.TrimSpace(title) == "" {
			title = r.request.Query
		}
		decoded, err = runLSPCodeActionResolveQuery(r.client, r.uri, r.request.Line, r.request.Character, title)
	case "document-link-resolve":
		method = "documentLink/resolve"
		decoded, err = runLSPDocumentLinkResolveQuery(r.client, r.uri, r.request.Line, r.request.Character)
	case "inlay-hint-resolve":
		method = "inlayHint/resolve"
		decoded, err = runLSPInlayHintResolveQuery(r.client, r.uri, r.request.Line, r.request.Character)
	case "completion-item-resolve":
		method = "completionItem/resolve"
		decoded, err = runLSPCompletionItemResolveQuery(r.client, r.uri, r.request.Line, r.request.Character, r.request.Query)
	case "workspace-symbol-resolve":
		method = "workspaceSymbol/resolve"
		decoded, err = runLSPWorkspaceSymbolResolveQuery(r.client, r.request.Query)
	default:
		return LSPQueryResult{}, false, nil
	}
	return r.actionResult(method, decoded), true, err
}

func (r *lspQueryRun) executeHierarchyAction() (LSPQueryResult, bool, error) {
	spec, ok := r.hierarchySpec()
	if !ok {
		return LSPQueryResult{}, false, nil
	}
	method, decoded, err := runLSPHierarchyQuery(r.client, spec)
	return r.actionResult(method, decoded), true, err
}

func (r *lspQueryRun) hierarchySpec() (hierarchyQuerySpec, bool) {
	base := hierarchyQuerySpec{
		Action: r.action, URI: r.uri, Line: r.request.Line, Character: r.request.Character,
	}
	if strings.HasPrefix(r.action, "call-hierarchy") || r.action == "prepare-call-hierarchy" {
		base.PrepareAction = "prepare-call-hierarchy"
		base.PrepareMethod = "textDocument/prepareCallHierarchy"
		base.IncomingName, base.Incoming = "call-hierarchy-incoming", "callHierarchy/incomingCalls"
		base.Outgoing, base.CallKey = "callHierarchy/outgoingCalls", "calls"
		return base, true
	}
	if strings.HasPrefix(r.action, "type-hierarchy") || r.action == "prepare-type-hierarchy" {
		base.PrepareAction = "prepare-type-hierarchy"
		base.PrepareMethod = "textDocument/prepareTypeHierarchy"
		base.IncomingName, base.Incoming = "type-hierarchy-supertypes", "typeHierarchy/supertypes"
		base.Outgoing, base.CallKey = "typeHierarchy/subtypes", "types"
		return base, true
	}
	return hierarchyQuerySpec{}, false
}

func (r *lspQueryRun) executeGeneric() (LSPQueryResult, error) {
	method, params, err := lspMethodParams(
		r.action, r.uri, r.request.Line, r.request.Character,
		r.request.NewName, r.request.Query, r.request.Arguments,
	)
	if err != nil {
		return LSPQueryResult{}, err
	}
	raw, err := r.client.request(method, params)
	if err != nil {
		return LSPQueryResult{}, err
	}
	decoded, err := decodeLSPResult(raw)
	if err != nil {
		return LSPQueryResult{}, err
	}
	result := r.actionResult(method, decoded)
	mergeLSPClientWorkspaceEdits(r.client, &result)
	mergeLSPClientNotifications(r.client, &result)
	if err := r.applyFormattingResult(&result, raw); err != nil {
		return LSPQueryResult{}, err
	}
	if err := r.applyRenameResult(&result, raw); err != nil {
		return LSPQueryResult{}, err
	}
	if err := r.applyCodeActionResult(&result, raw); err != nil {
		return LSPQueryResult{}, err
	}
	return result, nil
}

func decodeLSPResult(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (r *lspQueryRun) actionResult(method string, decoded any) LSPQueryResult {
	return LSPQueryResult{
		Kind: "lsp_query", Language: r.language, Action: r.action,
		Method: method, Path: r.rel, Result: decoded,
	}
}

func (r *lspQueryRun) applyFormattingResult(result *LSPQueryResult, raw json.RawMessage) error {
	if !isLSPFormattingAction(r.action) || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var edits []lspTextEdit
	if err := json.Unmarshal(raw, &edits); err != nil {
		return err
	}
	formatted, err := applyLSPTextEdits(string(r.data), edits)
	if err != nil {
		return err
	}
	result.TextEdits, result.Content = len(edits), formatted
	result.Changed = formatted != string(r.data)
	return nil
}

func (r *lspQueryRun) applyRenameResult(result *LSPQueryResult, raw json.RawMessage) error {
	if r.action != "rename" || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var edit lspWorkspaceEdit
	if err := json.Unmarshal(raw, &edit); err != nil {
		return err
	}
	fileEdits, textEdits, err := summarizeLSPWorkspaceEdit(r.workspace, edit)
	if err != nil {
		return err
	}
	result.FileEdits, result.TextEdits = len(fileEdits), textEdits
	result.Edits, result.Changed = fileEdits, textEdits > 0
	if r.request.Apply && result.Changed {
		if err := applyLSPFileEdits(fileEdits); err != nil {
			return err
		}
		result.Applied = true
	}
	return nil
}

func (r *lspQueryRun) applyCodeActionResult(result *LSPQueryResult, raw json.RawMessage) error {
	if r.action != "code-action" || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	fileEdits, textEdits, actionEdits, err := summarizeLSPCodeActionEdits(r.workspace, raw, r.request.CodeActionTitle)
	if err != nil {
		return err
	}
	result.FileEdits, result.TextEdits = len(fileEdits), textEdits
	result.Edits, result.Changed = fileEdits, textEdits > 0
	if !r.request.Apply {
		return nil
	}
	if actionEdits != 1 {
		return fmt.Errorf("lsp code-action apply requires exactly one matching edit-bearing action, got %d", actionEdits)
	}
	if err := applyLSPFileEdits(fileEdits); err != nil {
		return err
	}
	result.Applied = true
	return nil
}
