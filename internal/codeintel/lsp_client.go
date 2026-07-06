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
	Query           string `json:"query,omitempty"`
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

type lspRange struct {
	Start LSPPosition `json:"start"`
	End   LSPPosition `json:"end"`
}

type lspColorInformation struct {
	Range lspRange `json:"range"`
	Color struct {
		Red   float64 `json:"red"`
		Green float64 `json:"green"`
		Blue  float64 `json:"blue"`
		Alpha float64 `json:"alpha"`
	} `json:"color"`
}

type lspCodeLens struct {
	Range   lspRange `json:"range"`
	Command any      `json:"command,omitempty"`
	Data    any      `json:"data,omitempty"`
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
		Name:             "document-diagnostic",
		Method:           "textDocument/diagnostic",
		Aliases:          []string{"document_diagnostic", "documentDiagnostic", "pull_diagnostic", "pull-diagnostic"},
		RequiresDocument: true,
		Description:      "Pull diagnostics for one document using the language-server diagnostic request.",
	},
	{
		Name:        "workspace-diagnostic",
		Method:      "workspace/diagnostic",
		Aliases:     []string{"workspace_diagnostic", "workspaceDiagnostic", "workspace_diagnostics", "pull_workspace_diagnostic"},
		Description: "Pull diagnostics for the workspace using the language-server diagnostic request.",
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
		Name:             "code-action-resolve",
		Method:           "codeAction/resolve",
		Aliases:          []string{"code_action_resolve", "codeActionResolve", "resolve_code_action", "resolveCodeAction"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Resolve a code action selected from code actions at a document position.",
	},
	{
		Name:             "code-lens",
		Method:           "textDocument/codeLens",
		Aliases:          []string{"code_lens", "codeLens", "lenses"},
		RequiresDocument: true,
		Description:      "Return code lenses for a document.",
	},
	{
		Name:             "code-lens-resolve",
		Method:           "codeLens/resolve",
		Aliases:          []string{"code_lens_resolve", "codeLensResolve", "resolve_code_lens", "resolveCodeLens"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Resolve the code lens at a document position.",
	},
	{
		Name:             "prepare-call-hierarchy",
		Method:           "textDocument/prepareCallHierarchy",
		Aliases:          []string{"prepare_call_hierarchy", "prepareCallHierarchy", "call_hierarchy", "callHierarchy"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return call hierarchy items for a symbol at a document position.",
	},
	{
		Name:             "call-hierarchy-incoming",
		Method:           "callHierarchy/incomingCalls",
		Aliases:          []string{"incoming_calls", "incomingCalls", "call_hierarchy_incoming", "incoming_call_hierarchy"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return callers for the symbol at a document position.",
	},
	{
		Name:             "call-hierarchy-outgoing",
		Method:           "callHierarchy/outgoingCalls",
		Aliases:          []string{"outgoing_calls", "outgoingCalls", "call_hierarchy_outgoing", "outgoing_call_hierarchy"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return callees for the symbol at a document position.",
	},
	{
		Name:             "prepare-type-hierarchy",
		Method:           "textDocument/prepareTypeHierarchy",
		Aliases:          []string{"prepare_type_hierarchy", "prepareTypeHierarchy", "type_hierarchy", "typeHierarchy"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return type hierarchy items for a symbol at a document position.",
	},
	{
		Name:             "type-hierarchy-supertypes",
		Method:           "typeHierarchy/supertypes",
		Aliases:          []string{"supertypes", "super_types", "type_supertypes", "type_hierarchy_supertypes"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return supertypes for the type at a document position.",
	},
	{
		Name:             "type-hierarchy-subtypes",
		Method:           "typeHierarchy/subtypes",
		Aliases:          []string{"subtypes", "sub_types", "type_subtypes", "type_hierarchy_subtypes"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return subtypes for the type at a document position.",
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
		Name:             "completion-item-resolve",
		Method:           "completionItem/resolve",
		Aliases:          []string{"completion_resolve", "completionItemResolve", "resolve_completion", "resolveCompletion"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Resolve a completion item selected from completion candidates at a document position.",
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
		Name:             "document-link-resolve",
		Method:           "documentLink/resolve",
		Aliases:          []string{"document_link_resolve", "documentLinkResolve", "resolve_document_link", "resolveDocumentLink"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Resolve a document link selected from links discovered in a document.",
	},
	{
		Name:             "document-color",
		Method:           "textDocument/documentColor",
		Aliases:          []string{"document_color", "documentColor", "document-colors", "document_colors", "colors"},
		RequiresDocument: true,
		Description:      "Return color literals and their ranges discovered in a document.",
	},
	{
		Name:             "color-presentation",
		Method:           "textDocument/colorPresentation",
		Aliases:          []string{"color_presentation", "colorPresentation", "color-presentations", "color_presentations"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return presentation labels and edits for a color literal at a document position.",
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
		Name:             "inlay-hint-resolve",
		Method:           "inlayHint/resolve",
		Aliases:          []string{"inlay_hint_resolve", "inlayHintResolve", "resolve_inlay_hint", "resolveInlayHint"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Resolve an inlay hint selected from hints around a document position.",
	},
	{
		Name:             "inline-value",
		Method:           "textDocument/inlineValue",
		Aliases:          []string{"inline_value", "inlineValue", "inline-values", "inline_values"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return debugger inline values around a stopped document position.",
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
		Name:             "moniker",
		Method:           "textDocument/moniker",
		Aliases:          []string{"monikers", "symbol_moniker", "symbol-moniker"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return symbol monikers that identify a symbol across tools and indexes.",
	},
	{
		Name:             "semantic-tokens",
		Method:           "textDocument/semanticTokens/full",
		Aliases:          []string{"semantic_tokens", "semanticTokens", "semantic_tokens_full", "semantic-tokens-full", "semanticTokensFull"},
		RequiresDocument: true,
		Description:      "Return full-document semantic tokens for syntax-aware highlighting and code understanding.",
	},
	{
		Name:             "semantic-tokens-range",
		Method:           "textDocument/semanticTokens/range",
		Aliases:          []string{"semantic_tokens_range", "semanticTokensRange", "semantic-range", "semantic_range"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return semantic tokens for a document range ending at the requested position.",
	},
	{
		Name:        "workspace-symbol",
		Method:      "workspace/symbol",
		Aliases:     []string{"workspace_symbol", "workspace_symbols", "workspaceSymbol", "workspaceSymbols", "symbol_search", "symbol-search"},
		Description: "Return symbols matching a workspace-wide language-server query.",
	},
	{
		Name:        "workspace-symbol-resolve",
		Method:      "workspaceSymbol/resolve",
		Aliases:     []string{"workspace_symbol_resolve", "workspaceSymbolResolve", "resolve_workspace_symbol", "resolveWorkspaceSymbol"},
		Description: "Resolve a workspace symbol selected from a workspace-wide language-server query.",
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
	{
		Name:             "range-format",
		Method:           "textDocument/rangeFormatting",
		Aliases:          []string{"range_format", "rangeFormatting", "format_range", "range-formatting"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return formatted text edits for a document range ending at the requested position.",
	},
	{
		Name:             "on-type-format",
		Method:           "textDocument/onTypeFormatting",
		Aliases:          []string{"on_type_format", "onTypeFormatting", "format_on_type", "on-type-formatting"},
		RequiresDocument: true,
		RequiresPosition: true,
		Description:      "Return formatting edits triggered by typing a character at a document position.",
	},
	{
		Name:             "will-save",
		Method:           "textDocument/willSaveWaitUntil",
		Aliases:          []string{"will_save", "willSave", "will_save_wait_until", "willSaveWaitUntil", "save_edits"},
		RequiresDocument: true,
		Description:      "Return text edits a language server wants to apply before saving a document.",
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

// LSPActionRequiresDocument reports whether an action must open a document.
func LSPActionRequiresDocument(action string) (bool, error) {
	info, err := lookupLSPActionInfo(action)
	if err != nil {
		return false, err
	}
	return info.RequiresDocument, nil
}

func lookupLSPActionInfo(action string) (LSPActionInfo, error) {
	canonical, err := NormalizeLSPAction(action)
	if err != nil {
		return LSPActionInfo{}, err
	}
	for _, info := range lspActionInfos {
		if info.Name == canonical {
			return info, nil
		}
	}
	return LSPActionInfo{}, fmt.Errorf("unknown lsp action %q", action)
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
	actionInfo, err := lookupLSPActionInfo(action)
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
	path := ""
	rel := ""
	data := []byte(nil)
	if actionInfo.RequiresDocument {
		path, rel, err = resolveWorkspaceFile(workspace, request.Path)
		if err != nil {
			return LSPQueryResult{}, err
		}
		data, err = os.ReadFile(path)
		if err != nil {
			return LSPQueryResult{}, err
		}
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
	uri := ""
	if actionInfo.RequiresDocument {
		uri = fileURI(path)
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
	if action == "code-lens-resolve" {
		decoded, err := runLSPCodeLensResolveQuery(client, uri, request.Line, request.Character)
		if err != nil {
			return LSPQueryResult{}, err
		}
		return LSPQueryResult{
			Kind:     "lsp_query",
			Language: language,
			Action:   action,
			Method:   "codeLens/resolve",
			Path:     rel,
			Result:   decoded,
		}, nil
	}
	if action == "code-action-resolve" {
		title := request.CodeActionTitle
		if strings.TrimSpace(title) == "" {
			title = request.Query
		}
		decoded, err := runLSPCodeActionResolveQuery(client, uri, request.Line, request.Character, title)
		if err != nil {
			return LSPQueryResult{}, err
		}
		return LSPQueryResult{
			Kind:     "lsp_query",
			Language: language,
			Action:   action,
			Method:   "codeAction/resolve",
			Path:     rel,
			Result:   decoded,
		}, nil
	}
	if action == "document-link-resolve" {
		decoded, err := runLSPDocumentLinkResolveQuery(client, uri, request.Line, request.Character)
		if err != nil {
			return LSPQueryResult{}, err
		}
		return LSPQueryResult{
			Kind:     "lsp_query",
			Language: language,
			Action:   action,
			Method:   "documentLink/resolve",
			Path:     rel,
			Result:   decoded,
		}, nil
	}
	if action == "inlay-hint-resolve" {
		decoded, err := runLSPInlayHintResolveQuery(client, uri, request.Line, request.Character)
		if err != nil {
			return LSPQueryResult{}, err
		}
		return LSPQueryResult{
			Kind:     "lsp_query",
			Language: language,
			Action:   action,
			Method:   "inlayHint/resolve",
			Path:     rel,
			Result:   decoded,
		}, nil
	}
	if action == "completion-item-resolve" {
		decoded, err := runLSPCompletionItemResolveQuery(client, uri, request.Line, request.Character, request.Query)
		if err != nil {
			return LSPQueryResult{}, err
		}
		return LSPQueryResult{
			Kind:     "lsp_query",
			Language: language,
			Action:   action,
			Method:   "completionItem/resolve",
			Path:     rel,
			Result:   decoded,
		}, nil
	}
	if action == "workspace-symbol-resolve" {
		decoded, err := runLSPWorkspaceSymbolResolveQuery(client, request.Query)
		if err != nil {
			return LSPQueryResult{}, err
		}
		return LSPQueryResult{
			Kind:     "lsp_query",
			Language: language,
			Action:   action,
			Method:   "workspaceSymbol/resolve",
			Path:     rel,
			Result:   decoded,
		}, nil
	}
	if strings.HasPrefix(action, "call-hierarchy") || action == "prepare-call-hierarchy" {
		method, decoded, err := runLSPHierarchyQuery(client, hierarchyQuerySpec{
			Action:        action,
			URI:           uri,
			Line:          request.Line,
			Character:     request.Character,
			PrepareAction: "prepare-call-hierarchy",
			PrepareMethod: "textDocument/prepareCallHierarchy",
			IncomingName:  "call-hierarchy-incoming",
			Incoming:      "callHierarchy/incomingCalls",
			Outgoing:      "callHierarchy/outgoingCalls",
			CallKey:       "calls",
		})
		if err != nil {
			return LSPQueryResult{}, err
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
	if strings.HasPrefix(action, "type-hierarchy") || action == "prepare-type-hierarchy" {
		method, decoded, err := runLSPHierarchyQuery(client, hierarchyQuerySpec{
			Action:        action,
			URI:           uri,
			Line:          request.Line,
			Character:     request.Character,
			PrepareAction: "prepare-type-hierarchy",
			PrepareMethod: "textDocument/prepareTypeHierarchy",
			IncomingName:  "type-hierarchy-supertypes",
			Incoming:      "typeHierarchy/supertypes",
			Outgoing:      "typeHierarchy/subtypes",
			CallKey:       "types",
		})
		if err != nil {
			return LSPQueryResult{}, err
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
	if action == "color-presentation" {
		decoded, err := runLSPColorPresentationQuery(client, uri, request.Line, request.Character)
		if err != nil {
			return LSPQueryResult{}, err
		}
		return LSPQueryResult{
			Kind:     "lsp_query",
			Language: language,
			Action:   action,
			Method:   "textDocument/colorPresentation",
			Path:     rel,
			Result:   decoded,
		}, nil
	}
	method, params, err := lspMethodParams(action, uri, request.Line, request.Character, request.NewName, request.Query)
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
	if isLSPFormattingAction(action) && len(raw) > 0 && string(raw) != "null" {
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

func isLSPFormattingAction(action string) bool {
	switch action {
	case "format", "range-format", "on-type-format", "will-save":
		return true
	default:
		return false
	}
}

type hierarchyQuerySpec struct {
	Action        string
	URI           string
	Line          int
	Character     int
	PrepareAction string
	PrepareMethod string
	IncomingName  string
	Incoming      string
	Outgoing      string
	CallKey       string
}

func runLSPHierarchyQuery(client *lspClient, spec hierarchyQuerySpec) (string, any, error) {
	if spec.CallKey == "" {
		spec.CallKey = "items"
	}
	position := map[string]any{"line": max(0, spec.Line), "character": max(0, spec.Character)}
	textDocument := map[string]any{"uri": spec.URI}
	raw, err := client.request(spec.PrepareMethod, map[string]any{"textDocument": textDocument, "position": position})
	if err != nil {
		return "", nil, err
	}
	var items []any
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &items); err != nil {
			return "", nil, err
		}
	}
	if spec.Action == spec.PrepareAction {
		return spec.PrepareMethod, items, nil
	}
	method := spec.Incoming
	if spec.Action != spec.IncomingName {
		method = spec.Outgoing
	}
	if len(items) == 0 {
		return method, map[string]any{"items": items, spec.CallKey: []any{}}, nil
	}
	raw, err = client.request(method, map[string]any{"item": items[0]})
	if err != nil {
		return "", nil, err
	}
	var values any
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &values); err != nil {
			return "", nil, err
		}
	}
	if values == nil {
		values = []any{}
	}
	return method, map[string]any{"items": items, spec.CallKey: values}, nil
}

func runLSPColorPresentationQuery(client *lspClient, uri string, line int, character int) (any, error) {
	textDocument := map[string]any{"uri": uri}
	raw, err := client.request("textDocument/documentColor", map[string]any{"textDocument": textDocument})
	if err != nil {
		return nil, err
	}
	var colors []lspColorInformation
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &colors); err != nil {
			return nil, err
		}
	}
	if len(colors) == 0 {
		return map[string]any{"colors": colors, "presentations": []any{}}, nil
	}
	selected := colors[0]
	position := LSPPosition{Line: max(0, line), Character: max(0, character)}
	for _, candidate := range colors {
		if lspRangeContains(candidate.Range, position) {
			selected = candidate
			break
		}
	}
	raw, err = client.request("textDocument/colorPresentation", map[string]any{
		"textDocument": textDocument,
		"color":        selected.Color,
		"range":        selected.Range,
	})
	if err != nil {
		return nil, err
	}
	var presentations any
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &presentations); err != nil {
			return nil, err
		}
	}
	if presentations == nil {
		presentations = []any{}
	}
	return map[string]any{"colors": colors, "selected": selected, "presentations": presentations}, nil
}

func lspRangeContains(r lspRange, position LSPPosition) bool {
	if position.Line < r.Start.Line || position.Line > r.End.Line {
		return false
	}
	if position.Line == r.Start.Line && position.Character < r.Start.Character {
		return false
	}
	if position.Line == r.End.Line && position.Character > r.End.Character {
		return false
	}
	return true
}

func runLSPCodeLensResolveQuery(client *lspClient, uri string, line int, character int) (any, error) {
	textDocument := map[string]any{"uri": uri}
	raw, err := client.request("textDocument/codeLens", map[string]any{"textDocument": textDocument})
	if err != nil {
		return nil, err
	}
	var lenses []lspCodeLens
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &lenses); err != nil {
			return nil, err
		}
	}
	if len(lenses) == 0 {
		return map[string]any{"lenses": lenses, "resolved": nil}, nil
	}
	selected := lenses[0]
	position := LSPPosition{Line: max(0, line), Character: max(0, character)}
	for _, candidate := range lenses {
		if lspRangeContains(candidate.Range, position) {
			selected = candidate
			break
		}
	}
	raw, err = client.request("codeLens/resolve", selected)
	if err != nil {
		return nil, err
	}
	var resolved any
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &resolved); err != nil {
			return nil, err
		}
	}
	if resolved == nil {
		resolved = selected
	}
	return map[string]any{"lenses": lenses, "selected": selected, "resolved": resolved}, nil
}

func runLSPCodeActionResolveQuery(client *lspClient, uri string, line int, character int, title string) (any, error) {
	position := map[string]any{"line": max(0, line), "character": max(0, character)}
	textDocument := map[string]any{"uri": uri}
	raw, err := client.request("textDocument/codeAction", map[string]any{
		"textDocument": textDocument,
		"range":        map[string]any{"start": position, "end": position},
		"context":      map[string]any{"diagnostics": []any{}},
	})
	if err != nil {
		return nil, err
	}
	var actions []map[string]any
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &actions); err != nil {
			return nil, err
		}
	}
	if len(actions) == 0 {
		return map[string]any{"actions": actions, "resolved": nil}, nil
	}
	selected := selectLSPActionByTitle(actions, title)
	raw, err = client.request("codeAction/resolve", selected)
	if err != nil {
		return nil, err
	}
	var resolved any
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &resolved); err != nil {
			return nil, err
		}
	}
	if resolved == nil {
		resolved = selected
	}
	return map[string]any{"actions": actions, "selected": selected, "resolved": resolved}, nil
}

func selectLSPActionByTitle(actions []map[string]any, title string) map[string]any {
	title = strings.TrimSpace(title)
	if title == "" {
		return actions[0]
	}
	for _, action := range actions {
		value, _ := action["title"].(string)
		if strings.EqualFold(value, title) {
			return action
		}
	}
	lowerTitle := strings.ToLower(title)
	for _, action := range actions {
		value, _ := action["title"].(string)
		if strings.Contains(strings.ToLower(value), lowerTitle) {
			return action
		}
	}
	return actions[0]
}

func runLSPDocumentLinkResolveQuery(client *lspClient, uri string, line int, character int) (any, error) {
	textDocument := map[string]any{"uri": uri}
	raw, err := client.request("textDocument/documentLink", map[string]any{"textDocument": textDocument})
	if err != nil {
		return nil, err
	}
	items, err := decodeLSPMapItems(raw)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return map[string]any{"items": items, "resolved": nil}, nil
	}
	selected := selectLSPRangeItem(items, LSPPosition{Line: max(0, line), Character: max(0, character)})
	raw, err = client.request("documentLink/resolve", selected)
	if err != nil {
		return nil, err
	}
	resolved, err := decodeLSPAny(raw, selected)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "selected": selected, "resolved": resolved}, nil
}

func runLSPInlayHintResolveQuery(client *lspClient, uri string, line int, character int) (any, error) {
	line = max(0, line)
	character = max(0, character)
	textDocument := map[string]any{"uri": uri}
	start := map[string]any{"line": line, "character": 0}
	end := map[string]any{"line": line, "character": character}
	raw, err := client.request("textDocument/inlayHint", map[string]any{"textDocument": textDocument, "range": map[string]any{"start": start, "end": end}})
	if err != nil {
		return nil, err
	}
	items, err := decodeLSPMapItems(raw)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return map[string]any{"items": items, "resolved": nil}, nil
	}
	selected := selectLSPPositionItem(items, LSPPosition{Line: line, Character: character})
	raw, err = client.request("inlayHint/resolve", selected)
	if err != nil {
		return nil, err
	}
	resolved, err := decodeLSPAny(raw, selected)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "selected": selected, "resolved": resolved}, nil
}

func runLSPWorkspaceSymbolResolveQuery(client *lspClient, query string) (any, error) {
	query = strings.TrimSpace(query)
	raw, err := client.request("workspace/symbol", map[string]any{"query": query})
	if err != nil {
		return nil, err
	}
	items, err := decodeLSPMapItems(raw)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return map[string]any{"items": items, "resolved": nil}, nil
	}
	selected := selectLSPWorkspaceSymbol(items, query)
	raw, err = client.request("workspaceSymbol/resolve", selected)
	if err != nil {
		return nil, err
	}
	resolved, err := decodeLSPAny(raw, selected)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "selected": selected, "resolved": resolved}, nil
}

func runLSPCompletionItemResolveQuery(client *lspClient, uri string, line int, character int, query string) (any, error) {
	position := map[string]any{"line": max(0, line), "character": max(0, character)}
	textDocument := map[string]any{"uri": uri}
	raw, err := client.request("textDocument/completion", map[string]any{
		"textDocument": textDocument,
		"position":     position,
		"context":      map[string]any{"triggerKind": 1},
	})
	if err != nil {
		return nil, err
	}
	items, err := decodeLSPCompletionItems(raw)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return map[string]any{"items": items, "resolved": nil}, nil
	}
	selected := selectLSPCompletionItem(items, query)
	raw, err = client.request("completionItem/resolve", selected)
	if err != nil {
		return nil, err
	}
	var resolved any
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &resolved); err != nil {
			return nil, err
		}
	}
	if resolved == nil {
		resolved = selected
	}
	return map[string]any{"items": items, "selected": selected, "resolved": resolved}, nil
}

func decodeLSPMapItems(raw json.RawMessage) ([]map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return []map[string]any{}, nil
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func decodeLSPAny(raw json.RawMessage, fallback any) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return fallback, nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	if decoded == nil {
		return fallback, nil
	}
	return decoded, nil
}

func decodeLSPCompletionItems(raw json.RawMessage) ([]map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return []map[string]any{}, nil
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err == nil {
		return items, nil
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	if list.Items == nil {
		return []map[string]any{}, nil
	}
	return list.Items, nil
}

func selectLSPRangeItem(items []map[string]any, position LSPPosition) map[string]any {
	for _, item := range items {
		if value, ok := item["range"]; ok {
			var r lspRange
			if decodeLSPParams(value, &r) == nil && lspRangeContains(r, position) {
				return item
			}
		}
	}
	return items[0]
}

func selectLSPPositionItem(items []map[string]any, position LSPPosition) map[string]any {
	for _, item := range items {
		if value, ok := item["position"]; ok {
			var candidate LSPPosition
			if decodeLSPParams(value, &candidate) == nil && candidate.Line == position.Line && candidate.Character == position.Character {
				return item
			}
		}
	}
	for _, item := range items {
		if value, ok := item["position"]; ok {
			var candidate LSPPosition
			if decodeLSPParams(value, &candidate) == nil && candidate.Line == position.Line {
				return item
			}
		}
	}
	return items[0]
}

func selectLSPWorkspaceSymbol(items []map[string]any, query string) map[string]any {
	query = strings.TrimSpace(query)
	if query == "" {
		return items[0]
	}
	for _, item := range items {
		if name, ok := item["name"].(string); ok && strings.EqualFold(name, query) {
			return item
		}
	}
	lowerQuery := strings.ToLower(query)
	for _, item := range items {
		if name, ok := item["name"].(string); ok && strings.Contains(strings.ToLower(name), lowerQuery) {
			return item
		}
	}
	return items[0]
}

func selectLSPCompletionItem(items []map[string]any, query string) map[string]any {
	query = strings.TrimSpace(query)
	if query == "" {
		return items[0]
	}
	for _, item := range items {
		label, _ := item["label"].(string)
		if strings.EqualFold(label, query) {
			return item
		}
	}
	lowerQuery := strings.ToLower(query)
	for _, item := range items {
		label, _ := item["label"].(string)
		if strings.Contains(strings.ToLower(label), lowerQuery) {
			return item
		}
	}
	return items[0]
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

func lspMethodParams(action string, uri string, line int, character int, newName string, query string) (string, any, error) {
	position := map[string]any{"line": max(0, line), "character": max(0, character)}
	textDocument := map[string]any{"uri": uri}
	switch action {
	case "document-diagnostic":
		return "textDocument/diagnostic", map[string]any{"textDocument": textDocument}, nil
	case "workspace-diagnostic":
		return "workspace/diagnostic", map[string]any{"previousResultIds": []any{}}, nil
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
	case "code-lens":
		return "textDocument/codeLens", map[string]any{"textDocument": textDocument}, nil
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
	case "document-color":
		return "textDocument/documentColor", map[string]any{"textDocument": textDocument}, nil
	case "inlay-hint":
		line = max(0, line)
		start := map[string]any{"line": line, "character": 0}
		end := map[string]any{"line": line, "character": max(0, character)}
		return "textDocument/inlayHint", map[string]any{"textDocument": textDocument, "range": map[string]any{"start": start, "end": end}}, nil
	case "inline-value":
		line = max(0, line)
		character = max(0, character)
		start := map[string]any{"line": line, "character": 0}
		position := map[string]any{"line": line, "character": character}
		valueRange := map[string]any{"start": start, "end": position}
		stoppedLocation := map[string]any{"start": position, "end": position}
		frameID := strings.TrimSpace(query)
		if frameID == "" {
			frameID = "codog"
		}
		return "textDocument/inlineValue", map[string]any{
			"textDocument": textDocument,
			"range":        valueRange,
			"context":      map[string]any{"frameId": frameID, "stoppedLocation": stoppedLocation},
		}, nil
	case "linked-editing-range":
		return "textDocument/linkedEditingRange", map[string]any{"textDocument": textDocument, "position": position}, nil
	case "moniker":
		return "textDocument/moniker", map[string]any{"textDocument": textDocument, "position": position}, nil
	case "semantic-tokens":
		return "textDocument/semanticTokens/full", map[string]any{"textDocument": textDocument}, nil
	case "semantic-tokens-range":
		line = max(0, line)
		start := map[string]any{"line": line, "character": 0}
		end := map[string]any{"line": line, "character": max(0, character)}
		return "textDocument/semanticTokens/range", map[string]any{"textDocument": textDocument, "range": map[string]any{"start": start, "end": end}}, nil
	case "workspace-symbol":
		return "workspace/symbol", map[string]any{"query": strings.TrimSpace(query)}, nil
	case "signature-help":
		return "textDocument/signatureHelp", map[string]any{"textDocument": textDocument, "position": position, "context": map[string]any{"triggerKind": 1}}, nil
	case "symbols":
		return "textDocument/documentSymbol", map[string]any{"textDocument": textDocument}, nil
	case "format":
		return "textDocument/formatting", map[string]any{"textDocument": textDocument, "options": map[string]any{"tabSize": 4, "insertSpaces": false}}, nil
	case "range-format":
		line = max(0, line)
		start := map[string]any{"line": line, "character": 0}
		end := map[string]any{"line": line, "character": max(0, character)}
		return "textDocument/rangeFormatting", map[string]any{"textDocument": textDocument, "range": map[string]any{"start": start, "end": end}, "options": map[string]any{"tabSize": 4, "insertSpaces": false}}, nil
	case "on-type-format":
		ch := firstLSPTriggerCharacter(query)
		return "textDocument/onTypeFormatting", map[string]any{"textDocument": textDocument, "position": position, "ch": ch, "options": map[string]any{"tabSize": 4, "insertSpaces": false}}, nil
	case "will-save":
		return "textDocument/willSaveWaitUntil", map[string]any{"textDocument": textDocument, "reason": 1}, nil
	default:
		return "", nil, fmt.Errorf("unsupported lsp action %q", action)
	}
}

func firstLSPTriggerCharacter(query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return "}"
	}
	r, _ := utf8.DecodeRuneInString(query)
	if r == utf8.RuneError {
		return "}"
	}
	return string(r)
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
