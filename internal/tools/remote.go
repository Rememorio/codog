package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	// Register image decoders for read_image and notebook image outputs.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/cron"
	"github.com/Rememorio/codog/internal/planmode"
	"github.com/Rememorio/codog/internal/recovery"
	"github.com/Rememorio/codog/internal/team"
	"github.com/Rememorio/codog/internal/toolnames"
	"github.com/Rememorio/codog/internal/worktree"
)

func (PermissionCheckTool) Permission() Permission { return PermissionReadOnly }

func (PermissionCheckTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", errors.New("permission_check must be executed through the tool registry")
}

// TestingPermissionTool is a compatibility alias for older transcripts and
// archived Claude-style tool names.
type TestingPermissionTool = PermissionCheckTool

type testingPermissionInput struct {
	TargetTool         string          `json:"target_tool"`
	Tool               string          `json:"tool"`
	RequiredPermission Permission      `json:"required_permission"`
	Input              json.RawMessage `json:"input"`
	Action             string          `json:"action"`
}

func (r *Registry) executePermissionCheck(input json.RawMessage, prompter *Prompter) (string, error) {
	var payload testingPermissionInput
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	target := strings.TrimSpace(payload.TargetTool)
	if target == "" {
		target = strings.TrimSpace(payload.Tool)
	}
	if target == "" {
		target = strings.TrimSpace(payload.Action)
	}
	if target == "" {
		return "", errors.New("target_tool is required")
	}
	targetTool, canonical, found := r.toolByName(target)
	required := payload.RequiredPermission
	permissionSource := "request_override"
	if required != "" {
		if !validPermission(required) {
			return "", suggestedValueError("unsupported required_permission", string(required), permissionValueNames)
		}
	} else if found {
		required = targetTool.Permission()
		permissionSource = "tool_definition"
	} else {
		required = PermissionDanger
		permissionSource = "unknown_tool_default"
	}
	suggestions := []string(nil)
	if !found {
		suggestions = toolnames.Suggestions(target, r.toolNameSuggestionCandidates(), 4)
	}
	targetInput := payload.Input
	if len(targetInput) == 0 || string(targetInput) == "null" {
		targetInput = json.RawMessage(`{}`)
	}
	if prompter == nil {
		prompter = &Prompter{Mode: PermissionWorkspace}
	}
	decision := prompter.Decide(canonicalOrTarget(canonical, target), required, targetInput)
	report := map[string]any{
		"kind":                "permission_check",
		"source":              "registry_permission_policy",
		"requested_tool":      target,
		"target_tool":         canonicalOrTarget(canonical, target),
		"canonical_tool":      canonical,
		"known_tool":          found,
		"required_permission": string(required),
		"permission_source":   permissionSource,
		"mode":                string(decision.Mode),
		"input":               string(targetInput),
		"input_json":          targetInput,
		"allowed":             decision.Allowed,
		"would_prompt":        decision.WouldPrompt,
		"reason":              decision.Reason,
		"message":             decision.Message,
		"decision": map[string]any{
			"tool_name":           decision.ToolName,
			"required_permission": string(decision.Required),
			"mode":                string(decision.Mode),
			"allowed":             decision.Allowed,
			"would_prompt":        decision.WouldPrompt,
			"reason":              decision.Reason,
			"message":             decision.Message,
		},
	}
	if len(suggestions) > 0 {
		report["suggestions"] = suggestions
	}
	return pretty(report), nil
}

func (r *Registry) toolByName(name string) (Tool, string, bool) {
	canonical, tool, ok := r.resolve(name)
	if ok {
		return tool, canonical, true
	}
	return nil, "", false
}

func canonicalOrTarget(canonical string, target string) string {
	if canonical != "" {
		return canonical
	}
	return target
}

func (r *Registry) toolNameSuggestionCandidates() []string {
	candidates := map[string]struct{}{}
	for name := range r.tools {
		candidates[name] = struct{}{}
	}
	for _, canonical := range claudeToolAliases {
		if _, ok := r.tools[canonical]; ok {
			candidates[canonical] = struct{}{}
		}
	}
	out := make([]string, 0, len(candidates))
	for candidate := range candidates {
		out = append(out, candidate)
	}
	return out
}

func validPermission(permission Permission) bool {
	switch permission {
	case PermissionReadOnly, PermissionWorkspace, PermissionDanger, PermissionPrompt, PermissionAllow:
		return true
	default:
		return false
	}
}

type NotebookReadTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (NotebookReadTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "notebook_read",
		Description: "Read cell sources and optional outputs from a Jupyter .ipynb notebook inside the workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"notebook_path":   map[string]any{"type": "string"},
				"cell_index":      map[string]any{"type": "integer", "minimum": 0},
				"limit":           map[string]any{"type": "integer", "minimum": 1},
				"include_outputs": map[string]any{"type": "boolean"},
			},
			"required":             []string{"notebook_path"},
			"additionalProperties": false,
		},
	}
}

func (NotebookReadTool) Permission() Permission { return PermissionReadOnly }

func (t NotebookReadTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		NotebookPath   string `json:"notebook_path"`
		CellIndex      *int   `json:"cell_index"`
		Limit          int    `json:"limit"`
		IncludeOutputs bool   `json:"include_outputs"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, payload.NotebookPath, false)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(strings.ToLower(path), ".ipynb") {
		return "", errors.New("notebook_path must point to a .ipynb file")
	}
	result, err := codeintel.ReadNotebook(path, codeintel.NotebookReadOptions{
		CellIndex:      payload.CellIndex,
		Limit:          payload.Limit,
		IncludeOutputs: payload.IncludeOutputs,
	})
	if err != nil {
		return "", err
	}
	result.Path = displayPath(t.Workspace, path)
	return pretty(result), nil
}

type NotebookEditTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (NotebookEditTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "notebook_edit",
		Description: "Replace, insert, or delete a cell in a Jupyter .ipynb notebook inside the workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"notebook_path": map[string]any{"type": "string"},
				"cell_index":    map[string]any{"type": "integer", "minimum": 0},
				"cell_id":       map[string]any{"type": "string"},
				"cell_type":     map[string]any{"type": "string", "enum": []string{"code", "markdown", "raw"}},
				"new_source":    map[string]any{"type": "string"},
				"edit_mode":     map[string]any{"type": "string", "enum": []string{"replace", "insert", "delete"}},
			},
			"required":             []string{"notebook_path"},
			"additionalProperties": false,
		},
	}
}

func (NotebookEditTool) Permission() Permission { return PermissionWorkspace }

func (t NotebookEditTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		NotebookPath string  `json:"notebook_path"`
		CellIndex    *int    `json:"cell_index"`
		CellID       string  `json:"cell_id"`
		CellType     string  `json:"cell_type"`
		NewSource    *string `json:"new_source"`
		EditMode     string  `json:"edit_mode"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, payload.NotebookPath, false)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(strings.ToLower(path), ".ipynb") {
		return "", errors.New("notebook_path must point to a .ipynb file")
	}
	mode := strings.ToLower(strings.TrimSpace(payload.EditMode))
	if mode == "" {
		mode = "replace"
	}
	source := ""
	if payload.NewSource != nil {
		source = *payload.NewSource
	} else if mode == "insert" || mode == "replace" {
		return "", errors.New("new_source is required for insert and replace edits")
	}
	index, err := codeintel.ResolveNotebookEditIndex(path, payload.CellIndex, payload.CellID, payload.EditMode)
	if err != nil {
		return "", err
	}
	result, err := codeintel.EditNotebook(path, codeintel.NotebookEditOptions{
		Index:    index,
		CellType: payload.CellType,
		Source:   source,
		Mode:     mode,
	})
	if err != nil {
		return "", err
	}
	return pretty(result), nil
}

type LSPTool struct {
	Workspace      string
	AdditionalDirs []string
	ConfigHome     string
}

func (LSPTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "lsp",
		Description: "Query code intelligence for Go symbols, references, diagnostics, definitions, hover context, completions, and formatting.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": lspToolActionEnum(),
				},
				"path":      map[string]any{"type": "string"},
				"line":      map[string]any{"type": "integer", "minimum": 0},
				"character": map[string]any{"type": "integer", "minimum": 0},
				"new_name":  map[string]any{"type": "string"},
				"query":     map[string]any{"type": "string"},
				"arguments": map[string]any{"type": "array", "items": map[string]any{}},
				"limit":     map[string]any{"type": "integer", "minimum": 1},
				"language":  map[string]any{"type": "string"},
				"use_server": map[string]any{
					"type":        "boolean",
					"description": "Use a configured stdio LSP server from codog code-intel lsp start/query metadata instead of the static fallback.",
				},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	}
}

func lspToolActionEnum() []string {
	seen := map[string]bool{}
	actions := []string{}
	for _, action := range codeintel.SupportedLSPActions() {
		for _, candidate := range append([]string{action.Name}, action.Aliases...) {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" || seen[candidate] {
				continue
			}
			seen[candidate] = true
			actions = append(actions, candidate)
		}
	}
	return actions
}

func (LSPTool) Permission() Permission {
	return PermissionReadOnly
}

type lspToolInput struct {
	Action    string `json:"action"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
	Query     string `json:"query"`
	Arguments []any  `json:"arguments"`
	NewName   string `json:"new_name"`
	Limit     int    `json:"limit"`
	Language  string `json:"language"`
	UseServer bool   `json:"use_server"`
}

var errStaticLSPActionNotHandled = errors.New("static LSP action not handled by group")

func (t LSPTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload lspToolInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	action, err := codeintel.NormalizeLSPAction(payload.Action)
	if err != nil {
		return "", unknownToolActionError("lsp", payload.Action, lspToolActionEnum())
	}
	var fallback any
	if payload.UseServer || strings.TrimSpace(payload.Language) != "" {
		result, err := t.executeServerLSP(ctx, action, payload.Language, payload.Path, payload.Query, payload.Arguments, payload.Line, payload.Character, payload.NewName)
		if err == nil {
			return pretty(map[string]any{"action": action, "source": "lsp", "lsp": result}), nil
		}
		if payload.UseServer {
			return "", err
		}
		fallback = map[string]any{
			"from":   "lsp",
			"to":     "static",
			"reason": "lsp_server_unavailable",
			"error":  err.Error(),
		}
	}
	return t.executeStaticLSP(ctx, action, payload, fallback)
}

func (t LSPTool) executeStaticLSP(ctx context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	handlers := []func(context.Context, string, lspToolInput, any) (string, error){
		t.executeStaticSymbolAction,
		t.executeStaticReferenceAction,
		t.executeStaticRangeAction,
		t.executeStaticLinkingAction,
		t.executeStaticDocumentAction,
		t.executeStaticColorAction,
		t.executeStaticInlayAction,
		t.executeStaticLensAction,
		t.executeStaticHierarchyAction,
		t.executeStaticRenameAction,
		t.executeStaticCallHierarchyAction,
		t.executeStaticTypeHierarchyAction,
		t.executeStaticCodeAction,
		t.executeStaticCodeActionResolve,
		t.executeStaticEditorAction,
		t.executeStaticCompletionAction,
		t.executeStaticFormattingAction,
		t.executeStaticDiagnosticAction,
		t.executeStaticCommandAction,
	}
	for _, handle := range handlers {
		result, err := handle(ctx, action, payload, fallback)
		if !errors.Is(err, errStaticLSPActionNotHandled) {
			return result, err
		}
	}
	return "", unknownToolActionError("lsp", payload.Action, lspToolActionEnum())
}

func (t LSPTool) executeStaticSymbolAction(_ context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "symbols":
		symbols, err := codeintel.GoSymbols(t.Workspace)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(payload.Path) != "" {
			rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
			if err != nil {
				return "", err
			}
			filtered := symbols[:0]
			for _, symbol := range symbols {
				if filepath.ToSlash(symbol.Path) == rel {
					filtered = append(filtered, symbol)
				}
			}
			symbols = filtered
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"symbols": symbols, "total": len(symbols)})), nil
	case "definition", "declaration", "type-definition":
		query, err := t.lspQuery(payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		definition, found, err := codeintel.Definition(t.Workspace, query)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": query, "found": found, "definition": definition})), nil
	case "workspace-symbol":
		symbols, err := codeintel.WorkspaceSymbols(t.Workspace, payload.Query, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": strings.TrimSpace(payload.Query), "symbols": symbols, "total": len(symbols)})), nil
	case "workspace-symbol-resolve":
		query, err := t.lspQuery(payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		resolved, found, err := codeintel.ResolveWorkspaceSymbol(t.Workspace, query)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": query, "found": found, "resolved": resolved})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) executeStaticReferenceAction(_ context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "references":
		query, err := t.lspQuery(payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		refs, err := codeintel.References(t.Workspace, query, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": query, "references": refs, "total": len(refs)})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) executeStaticRangeAction(_ context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "document-highlight":
		query, err := t.lspQuery(payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		rel := ""
		if strings.TrimSpace(payload.Path) != "" {
			rel, err = scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
			if err != nil {
				return "", err
			}
		}
		highlights, err := codeintel.DocumentHighlights(t.Workspace, query, rel, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": query, "highlights": highlights, "total": len(highlights)})), nil
	case "folding-range":
		rel := ""
		if strings.TrimSpace(payload.Path) != "" {
			var err error
			rel, err = scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
			if err != nil {
				return "", err
			}
		}
		ranges, err := codeintel.FoldingRanges(t.Workspace, rel, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "ranges": ranges, "total": len(ranges)})), nil
	case "selection-range":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp selection_range")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		ranges, err := codeintel.SelectionRanges(t.Workspace, rel, payload.Line, payload.Character, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "ranges": ranges, "total": len(ranges)})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) executeStaticLinkingAction(_ context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "moniker":
		query, err := t.lspQuery(payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		monikers, err := codeintel.Monikers(t.Workspace, query)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": query, "monikers": monikers, "total": len(monikers)})), nil
	case "linked-editing-range":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp linked_editing_range")
		}
		query, err := t.lspQuery(payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		ranges, err := codeintel.LinkedEditingRanges(t.Workspace, query, rel, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": query, "linked_editing": ranges, "total": len(ranges.Ranges)})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) executeStaticDocumentAction(_ context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "document-link":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp document_link")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		links, err := codeintel.DocumentLinks(t.Workspace, rel, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "links": links, "total": len(links)})), nil
	case "document-link-resolve":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp document_link_resolve")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		link, found, err := codeintel.DocumentLinkAtPosition(t.Workspace, rel, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "found": found, "link": link})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) executeStaticColorAction(_ context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "document-color":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp document_color")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		colors, err := codeintel.DocumentColors(t.Workspace, rel, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "colors": colors, "total": len(colors)})), nil
	case "color-presentation":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp color_presentation")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		presentations, found, err := codeintel.ColorPresentations(t.Workspace, rel, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "found": found, "presentations": presentations, "total": len(presentations)})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) executeStaticInlayAction(_ context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "inlay-hint":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp inlay_hint")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		hints, err := codeintel.InlayHints(t.Workspace, rel, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "hints": hints, "total": len(hints)})), nil
	case "inlay-hint-resolve":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp inlay_hint_resolve")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		hint, found, err := codeintel.InlayHintAtPosition(t.Workspace, rel, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "found": found, "hint": hint})), nil
	case "signature-help":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp signature_help")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		help, err := codeintel.SignatureHelpAtPosition(t.Workspace, rel, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "signature_help": help, "found": help.Found})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) executeStaticLensAction(_ context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "code-lens":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp code_lens")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		lenses, err := codeintel.CodeLenses(t.Workspace, rel, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "lenses": lenses, "total": len(lenses)})), nil
	case "code-lens-resolve":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp code_lens_resolve")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		lens, found, err := codeintel.CodeLensAtPosition(t.Workspace, rel, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "found": found, "lens": lens})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) executeStaticHierarchyAction(_ context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "semantic-tokens":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp semantic_tokens")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		tokens, err := codeintel.SemanticTokensForDocument(t.Workspace, rel, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "semantic_tokens": tokens, "total": len(tokens.Tokens)})), nil
	case "semantic-tokens-range":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp semantic_tokens_range")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		tokens, err := codeintel.SemanticTokensForLine(t.Workspace, rel, payload.Line, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "semantic_tokens": tokens, "total": len(tokens.Tokens)})), nil
	case "semantic-tokens-delta":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp semantic_tokens_delta")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		delta, err := codeintel.SemanticTokensDeltaForDocument(t.Workspace, rel, payload.Query, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "semantic_tokens_delta": delta, "total": len(delta.Tokens.Tokens)})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) executeStaticRenameAction(_ context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "prepare-rename":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp prepare_rename")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		prepared, err := codeintel.PrepareRenameAtPosition(t.Workspace, rel, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "prepared": prepared, "found": prepared.Found})), nil
	case "rename":
		if strings.TrimSpace(payload.NewName) == "" {
			return "", errors.New("new_name is required for lsp rename")
		}
		query, err := t.lspQuery(payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		renamed, err := codeintel.RenameSymbol(t.Workspace, query, payload.NewName, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": query, "rename": renamed, "text_edits": renamed.TextEdits, "file_edits": renamed.FileEdits})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) executeStaticCallHierarchyAction(_ context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "prepare-call-hierarchy":
		items, err := codeintel.PrepareCallHierarchy(t.Workspace, payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": strings.TrimSpace(payload.Query), "items": items, "total": len(items)})), nil
	case "call-hierarchy-incoming":
		query, err := t.lspQuery(payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		calls, err := codeintel.IncomingCalls(t.Workspace, query, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": query, "calls": calls, "total": len(calls)})), nil
	case "call-hierarchy-outgoing":
		query, err := t.lspQuery(payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		calls, err := codeintel.OutgoingCalls(t.Workspace, query, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": query, "calls": calls, "total": len(calls)})), nil
	case "prepare-type-hierarchy":
		items, err := codeintel.PrepareTypeHierarchy(t.Workspace, payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": strings.TrimSpace(payload.Query), "items": items, "total": len(items)})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) executeStaticTypeHierarchyAction(_ context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "type-hierarchy-supertypes":
		query, err := t.lspQuery(payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		items, err := codeintel.TypeHierarchySupertypes(t.Workspace, query, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": query, "items": items, "total": len(items)})), nil
	case "type-hierarchy-subtypes":
		query, err := t.lspQuery(payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		items, err := codeintel.TypeHierarchySubtypes(t.Workspace, query, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": query, "items": items, "total": len(items)})), nil
	case "implementation":
		query, err := t.lspQuery(payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		items, err := codeintel.TypeHierarchySubtypes(t.Workspace, query, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": query, "implementations": items, "total": len(items)})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) executeStaticCodeAction(ctx context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "code-action":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp code_action")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		actions, err := t.staticCodeActions(ctx, rel)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "actions": actions, "total": len(actions)})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) staticCodeActions(ctx context.Context, path string) ([]map[string]any, error) {
	diagnostics, err := codeintel.GoDiagnostics(ctx, t.Workspace, []string{path})
	if err != nil {
		return nil, err
	}
	format, err := codeintel.FormatGoFile(t.Workspace, path, false)
	if err != nil {
		return nil, err
	}
	organized, err := codeintel.OrganizeGoImports(t.Workspace, path, false)
	if err != nil {
		return nil, err
	}
	fixAll, err := codeintel.FixAllGoFile(t.Workspace, path, false)
	if err != nil {
		return nil, err
	}
	actions := []map[string]any{}
	actions = appendStaticCodeAction(actions, format.Changed, "Format Go file", "source.format", path, "edit", format)
	actions = appendStaticCodeAction(actions, organized.ImportCount > 0 && organized.Changed, "Organize Go imports", "source.organizeImports", path, "edit", organized)
	actions = appendStaticCodeAction(actions, fixAll.Changed, "Fix all Go source", "source.fixAll", path, "edit", fixAll)
	actions = appendStaticCodeAction(actions, len(diagnostics) > 0, "Review Go diagnostics", "quickfix", path, "diagnostics", diagnostics)
	return actions, nil
}

func appendStaticCodeAction(actions []map[string]any, include bool, title string, kind string, path string, valueKey string, value any) []map[string]any {
	if !include {
		return actions
	}
	return append(actions, map[string]any{
		"title":  title,
		"kind":   kind,
		"path":   path,
		valueKey: value,
	})
}

func (t LSPTool) executeStaticCodeActionResolve(ctx context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "code-action-resolve":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp code_action_resolve")
		}
		title := staticCodeActionTitle(payload)
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		switch normalizeStaticCodeActionTitle(title) {
		case "format":
			format, err := codeintel.FormatGoFile(t.Workspace, rel, false)
			if err != nil {
				return "", err
			}
			resolved := map[string]any{"title": "Format Go file", "kind": "source.format", "path": rel, "edit": format}
			return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "selected": title, "resolved": resolved})), nil
		case "organize-imports":
			organized, err := codeintel.OrganizeGoImports(t.Workspace, rel, false)
			if err != nil {
				return "", err
			}
			resolved := map[string]any{"title": "Organize Go imports", "kind": "source.organizeImports", "path": rel, "edit": organized}
			return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "selected": title, "resolved": resolved})), nil
		case "fix-all":
			fixAll, err := codeintel.FixAllGoFile(t.Workspace, rel, false)
			if err != nil {
				return "", err
			}
			resolved := map[string]any{"title": "Fix all Go source", "kind": "source.fixAll", "path": rel, "edit": fixAll}
			return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "selected": title, "resolved": resolved})), nil
		case "diagnostics":
			diagnostics, err := codeintel.GoDiagnostics(ctx, t.Workspace, []string{rel})
			if err != nil {
				return "", err
			}
			resolved := map[string]any{"title": "Review Go diagnostics", "kind": "quickfix", "path": rel, "diagnostics": diagnostics}
			return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "selected": title, "resolved": resolved})), nil
		default:
			return "", unknownToolActionError("static code", title, staticCodeActionNames)
		}
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func staticCodeActionTitle(payload lspToolInput) string {
	if title := strings.TrimSpace(payload.Query); title != "" {
		return title
	}
	if len(payload.Arguments) > 0 {
		if title, ok := payload.Arguments[0].(string); ok && strings.TrimSpace(title) != "" {
			return strings.TrimSpace(title)
		}
	}
	return "Format Go file"
}

func (t LSPTool) executeStaticEditorAction(_ context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "inline-value":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp inline_value")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		values, err := codeintel.InlineValues(t.Workspace, rel, payload.Line, payload.Character, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "values": values, "total": len(values)})), nil
	case "hover":
		query, err := t.lspQuery(payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		hover, err := codeintel.HoverInfo(t.Workspace, query, 2)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": query, "hover": hover})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) executeStaticCompletionAction(_ context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "completion":
		query := strings.TrimSpace(payload.Query)
		if query == "" && strings.TrimSpace(payload.Path) != "" {
			var err error
			query, err = symbolAtPosition(t.Workspace, t.AdditionalDirs, payload.Path, payload.Line, payload.Character)
			if err != nil {
				return "", err
			}
		}
		completions, err := codeintel.Completions(t.Workspace, query, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport("completion", fallback, map[string]any{"query": query, "completions": completions, "total": len(completions)})), nil
	case "completion-item-resolve":
		query := strings.TrimSpace(payload.Query)
		if query == "" {
			return "", errors.New("query is required for lsp completion_resolve")
		}
		completions, err := codeintel.Completions(t.Workspace, query, 10)
		if err != nil {
			return "", err
		}
		var resolved codeintel.Completion
		found := false
		for _, completion := range completions {
			if completion.Label == query {
				resolved = completion
				found = true
				break
			}
			if !found && strings.HasPrefix(strings.ToLower(completion.Label), strings.ToLower(query)) {
				resolved = completion
				found = true
			}
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"query": query, "found": found, "item": resolved})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) executeStaticFormattingAction(ctx context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "format":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp format")
		}
		result, err := codeintel.FormatGoFile(t.Workspace, payload.Path, false)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport("format", fallback, map[string]any{"format": result})), nil
	case "range-format":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp range_format")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		result, err := codeintel.FormatGoFile(t.Workspace, rel, false)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "line": payload.Line, "character": payload.Character, "format": result})), nil
	case "on-type-format":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp on_type_format")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		result, err := codeintel.FormatGoFile(t.Workspace, rel, false)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "line": payload.Line, "character": payload.Character, "format": result})), nil
	case "will-save":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp will_save")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		result, err := codeintel.FormatGoFile(t.Workspace, rel, false)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "format": result, "edits": result.Changed})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) executeStaticDiagnosticAction(ctx context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "document-diagnostic":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp document_diagnostic")
		}
		rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
		if err != nil {
			return "", err
		}
		diagnostics, err := codeintel.GoDiagnostics(ctx, t.Workspace, []string{rel})
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"path": rel, "diagnostics": diagnostics, "total": len(diagnostics)})), nil
	case "workspace-diagnostic":
		patterns := []string{}
		if strings.TrimSpace(payload.Path) != "" {
			patterns = append(patterns, payload.Path)
		} else if strings.TrimSpace(payload.Query) != "" {
			patterns = append(patterns, payload.Query)
		}
		diagnostics, err := codeintel.GoDiagnostics(ctx, t.Workspace, patterns)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"diagnostics": diagnostics, "total": len(diagnostics)})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func (t LSPTool) executeStaticCommand(ctx context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	command := strings.ToLower(strings.TrimSpace(payload.Query))
	if command == "" {
		return "", errors.New("query is required for lsp execute_command")
	}
	commandPath := lspExecuteCommandPath(payload)
	switch command {
	case "format", "gofmt", "source.format":
		return t.executeStaticFormatCommand(action, command, commandPath, fallback)
	case "organize_imports", "organize-imports", "organize imports", "source.organizeimports", "gopls.organize_imports", "gopls.organizeimports":
		return t.executeStaticOrganizeImportsCommand(action, command, commandPath, fallback)
	case "fix_all", "fix-all", "fix all", "source.fixall", "gopls.fixall":
		return t.executeStaticFixAllCommand(action, command, commandPath, fallback)
	case "diagnostics", "go.diagnostics":
		return t.executeStaticDiagnosticsCommand(ctx, action, command, commandPath, fallback)
	case "symbols", "workspace.symbols":
		return t.executeStaticSymbolsCommand(action, command, payload.Limit, fallback)
	default:
		return "", suggestedValueError("unsupported static execute command", payload.Query, staticExecuteCommandNames)
	}
}

func (t LSPTool) executeStaticFormatCommand(action string, command string, path string, fallback any) (string, error) {
	rel, err := t.staticCommandPath(path, "format")
	if err != nil {
		return "", err
	}
	result, err := codeintel.FormatGoFile(t.Workspace, rel, false)
	if err != nil {
		return "", err
	}
	return pretty(staticLSPToolReport(action, fallback, map[string]any{"command": command, "path": rel, "format": result})), nil
}

func (t LSPTool) executeStaticOrganizeImportsCommand(action string, command string, path string, fallback any) (string, error) {
	rel, err := t.staticCommandPath(path, "organize_imports")
	if err != nil {
		return "", err
	}
	result, err := codeintel.OrganizeGoImports(t.Workspace, rel, false)
	if err != nil {
		return "", err
	}
	return pretty(staticLSPToolReport(action, fallback, map[string]any{"command": command, "path": rel, "organize_imports": result})), nil
}

func (t LSPTool) executeStaticFixAllCommand(action string, command string, path string, fallback any) (string, error) {
	rel, err := t.staticCommandPath(path, "fix_all")
	if err != nil {
		return "", err
	}
	result, err := codeintel.FixAllGoFile(t.Workspace, rel, false)
	if err != nil {
		return "", err
	}
	return pretty(staticLSPToolReport(action, fallback, map[string]any{"command": command, "path": rel, "fix_all": result})), nil
}

func (t LSPTool) executeStaticDiagnosticsCommand(ctx context.Context, action string, command string, path string, fallback any) (string, error) {
	patterns := []string{}
	if strings.TrimSpace(path) != "" {
		patterns = append(patterns, path)
	}
	diagnostics, err := codeintel.GoDiagnostics(ctx, t.Workspace, patterns)
	if err != nil {
		return "", err
	}
	return pretty(staticLSPToolReport(action, fallback, map[string]any{"command": command, "diagnostics": diagnostics, "total": len(diagnostics)})), nil
}

func (t LSPTool) executeStaticSymbolsCommand(action string, command string, limit int, fallback any) (string, error) {
	symbols, err := codeintel.WorkspaceSymbols(t.Workspace, "", limit)
	if err != nil {
		return "", err
	}
	return pretty(staticLSPToolReport(action, fallback, map[string]any{"command": command, "symbols": symbols, "total": len(symbols)})), nil
}

func (t LSPTool) staticCommandPath(path string, command string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path or first string argument is required for lsp execute_command %s", command)
	}
	return scopedRelativePath(t.Workspace, t.AdditionalDirs, path)
}

func lspExecuteCommandPath(payload lspToolInput) string {
	if path := strings.TrimSpace(payload.Path); path != "" {
		return path
	}
	if len(payload.Arguments) == 0 {
		return ""
	}
	path, _ := payload.Arguments[0].(string)
	return strings.TrimSpace(path)
}

func (t LSPTool) executeStaticCommandAction(ctx context.Context, action string, payload lspToolInput, fallback any) (string, error) {
	switch action {
	case "execute-command":
		return t.executeStaticCommand(ctx, action, payload, fallback)
	case "diagnostics":
		patterns := []string{}
		if strings.TrimSpace(payload.Path) != "" {
			patterns = append(patterns, payload.Path)
		} else if strings.TrimSpace(payload.Query) != "" {
			patterns = append(patterns, payload.Query)
		}
		diagnostics, err := codeintel.GoDiagnostics(ctx, t.Workspace, patterns)
		if err != nil {
			return "", err
		}
		return pretty(staticLSPToolReport(action, fallback, map[string]any{"diagnostics": diagnostics, "total": len(diagnostics)})), nil
	default:
		return "", errStaticLSPActionNotHandled
	}
}

func staticLSPToolReport(action string, fallback any, values map[string]any) map[string]any {
	report := map[string]any{"action": action, "source": "static"}
	for key, value := range values {
		report[key] = value
	}
	if fallback != nil {
		report["fallback"] = fallback
	}
	return report
}

var staticCodeActionNames = []string{
	"Format Go file",
	"Organize Go imports",
	"Fix all Go source",
	"Review Go diagnostics",
	"format",
	"organize-imports",
	"fix-all",
	"diagnostics",
}

var staticExecuteCommandNames = []string{
	"format",
	"gofmt",
	"source.format",
	"organize_imports",
	"organize-imports",
	"source.organizeimports",
	"fix_all",
	"fix-all",
	"source.fixall",
	"diagnostics",
	"go.diagnostics",
	"symbols",
	"workspace.symbols",
}

func normalizeStaticCodeActionTitle(title string) string {
	normalized := strings.ToLower(strings.TrimSpace(title))
	normalized = strings.TrimPrefix(normalized, "source action:")
	normalized = strings.TrimPrefix(normalized, "source action")
	normalized = strings.TrimSpace(normalized)
	normalized = strings.ReplaceAll(normalized, "_", " ")
	normalized = strings.ReplaceAll(normalized, "-", " ")
	normalized = strings.Join(strings.Fields(normalized), " ")
	dotted := strings.ReplaceAll(normalized, " ", "")
	switch normalized {
	case "format go file", "format", "format document", "format file", "gofmt", "gopls gofmt":
		return "format"
	case "organize go imports", "organize imports", "add missing imports", "remove unused imports", "sort imports", "go organize imports", "gopls organize imports", "gopls organizeimports":
		return "organize-imports"
	case "fix all go source", "fix all", "fix all go", "source fix all", "gopls fixall":
		return "fix-all"
	case "review go diagnostics", "diagnostics", "diagnostic", "quickfix", "quick fix", "fix":
		return "diagnostics"
	}
	switch dotted {
	case "source.format", "source.format.go", "source.format.gofmt", "gopls.gofmt":
		return "format"
	case "source.organizeimports", "source.organizeimports.go", "source.addmissingimports", "source.addmissingimports.go", "source.removeunusedimports", "source.removeunusedimports.go", "gopls.organizeimports", "gopls.organize_imports":
		return "organize-imports"
	case "source.fixall", "source.fixall.go", "source.fixall.gopls", "gopls.fixall":
		return "fix-all"
	}
	return normalized
}

func (t LSPTool) lspQuery(query string, path string, line int, character int) (string, error) {
	query = strings.TrimSpace(query)
	if query != "" {
		return query, nil
	}
	if strings.TrimSpace(path) == "" {
		return "", errors.New("query or path position is required")
	}
	return symbolAtPosition(t.Workspace, t.AdditionalDirs, path, line, character)
}

func (t LSPTool) executeServerLSP(ctx context.Context, action string, language string, path string, query string, arguments []any, line int, character int, newName string) (codeintel.LSPQueryResult, error) {
	if strings.TrimSpace(t.ConfigHome) == "" {
		return codeintel.LSPQueryResult{}, errors.New("config home is required for lsp server queries")
	}
	requiresDocument, err := codeintel.LSPActionRequiresDocument(action)
	if err != nil {
		return codeintel.LSPQueryResult{}, err
	}
	if requiresDocument && strings.TrimSpace(path) == "" {
		return codeintel.LSPQueryResult{}, errors.New("path is required for lsp server queries")
	}
	language = strings.TrimSpace(language)
	if language == "" {
		if strings.TrimSpace(path) == "" {
			return codeintel.LSPQueryResult{}, errors.New("language is required for pathless lsp server queries")
		}
		language = codeintel.InferLanguageID(path)
	}
	return codeintel.NewLSPStore(t.ConfigHome, t.Workspace).Query(ctx, language, codeintel.LSPQueryRequest{
		Action:    action,
		Path:      path,
		Query:     query,
		Arguments: arguments,
		Line:      line,
		Character: character,
		NewName:   newName,
	})
}

func scopedRelativePath(workspace string, additionalDirs []string, requested string) (string, error) {
	path, err := safePathInScope(workspace, additionalDirs, requested, false)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(workspace, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(filepath.Clean(requested)), nil
	}
	return filepath.ToSlash(rel), nil
}

func symbolAtPosition(workspace string, additionalDirs []string, requested string, line int, character int) (string, error) {
	path, err := safePathInScope(workspace, additionalDirs, requested, false)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	if line < 0 || line >= len(lines) {
		return "", fmt.Errorf("line %d is out of range", line)
	}
	text := lines[line]
	if character < 0 {
		character = 0
	}
	if character > len(text) {
		character = len(text)
	}
	start := character
	for start > 0 && isIdentifierByte(text[start-1]) {
		start--
	}
	end := character
	for end < len(text) && isIdentifierByte(text[end]) {
		end++
	}
	if start == end {
		return "", errors.New("no symbol found at position")
	}
	return text[start:end], nil
}

func isIdentifierByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

type EnterWorktreeTool struct {
	Workspace string
}

func (EnterWorktreeTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "enter_worktree",
		Description: "Allocate a detached git worktree for isolated agent or verification work.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
			"required":             []string{"name"},
			"additionalProperties": false,
		},
	}
}

func (EnterWorktreeTool) Permission() Permission { return PermissionDanger }

func (t EnterWorktreeTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	allocation, err := worktree.Allocate(t.Workspace, payload.Name)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"kind":       "worktree",
		"operation":  "enter",
		"allocation": allocation,
	}), nil
}

type ExitWorktreeTool struct {
	Workspace string
}

func (ExitWorktreeTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "exit_worktree",
		Description: "Remove a Codog-managed git worktree allocation by id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":      map[string]any{"type": "string"},
				"task_id": map[string]any{"type": "string"},
				"taskId":  map[string]any{"type": "string"},
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
	}
}

func (ExitWorktreeTool) Permission() Permission { return PermissionDanger }

func (t ExitWorktreeTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if err := worktree.Remove(t.Workspace, payload.ID); err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"kind":      "worktree",
		"operation": "exit",
		"id":        payload.ID,
		"removed":   true,
	}), nil
}

type EnterPlanModeTool struct {
	Workspace string
}

type planModeInput struct {
	Plan string `json:"plan,omitempty"`
}

func (EnterPlanModeTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "enter_plan_mode",
		Description: "Enter plan mode and optionally persist the current implementation plan. While plan mode is active, future tool permission checks are read-only until exit_plan_mode is called or the user exits plan mode.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"plan": map[string]any{
					"type":        "string",
					"description": "Optional plan text to store with the active plan-mode state.",
				},
			},
		},
	}
}

func (EnterPlanModeTool) Permission() Permission {
	return PermissionReadOnly
}

func (t EnterPlanModeTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload planModeInput
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	report, err := planmode.Enter(t.Workspace, payload.Plan)
	if err != nil {
		return "", err
	}
	return pretty(report), nil
}

type ExitPlanModeTool struct {
	Workspace string
}

func (ExitPlanModeTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "exit_plan_mode",
		Description: "Exit plan mode. Include the final implementation plan to persist it before returning to normal tool permissions on the next user turn.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"plan": map[string]any{
					"type":        "string",
					"description": "Optional final plan text to store before leaving plan mode.",
				},
			},
		},
	}
}

func (ExitPlanModeTool) Permission() Permission {
	return PermissionReadOnly
}

func (t ExitPlanModeTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload planModeInput
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(payload.Plan) != "" {
		if _, err := planmode.Set(t.Workspace, payload.Plan); err != nil {
			return "", err
		}
	}
	report, err := planmode.Exit(t.Workspace)
	if err != nil {
		return "", err
	}
	return pretty(report), nil
}

type AgentTool struct {
	Workspace   string
	ConfigHome  string
	ConfigEnv   map[string]string
	Executable  string
	Definitions []agentdefs.Definition
	PluginDirs  []string
}

func (AgentTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "agent",
		Description: "Launch a specialized Codog agent task in the background and return its task metadata.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description":   map[string]any{"type": "string"},
				"prompt":        map[string]any{"type": "string"},
				"subagent_type": map[string]any{"type": "string"},
				"name":          map[string]any{"type": "string"},
				"model":         map[string]any{"type": "string"},
				"session_id":    map[string]any{"type": "string"},
			},
			"required":             []string{"description", "prompt"},
			"additionalProperties": false,
		},
	}
}

func (AgentTool) Permission() Permission {
	return PermissionDanger
}

func (t AgentTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Description  string `json:"description"`
		Prompt       string `json:"prompt"`
		SubagentType string `json:"subagent_type"`
		Name         string `json:"name"`
		Model        string `json:"model"`
		SessionID    string `json:"session_id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	payload.Description = strings.TrimSpace(payload.Description)
	payload.Prompt = strings.TrimSpace(payload.Prompt)
	if payload.Description == "" {
		return "", errors.New("description is required")
	}
	if payload.Prompt == "" {
		return "", errors.New("prompt is required")
	}
	def, found, err := findAgentDefinition(t.Workspace, t.Definitions, payload.Name, payload.SubagentType)
	if err != nil {
		return "", err
	}
	agentName := strings.TrimSpace(payload.Name)
	if agentName == "" {
		agentName = strings.TrimSpace(payload.SubagentType)
	}
	if found {
		agentName = def.Name
		if strings.TrimSpace(payload.Model) == "" {
			payload.Model = def.Model
		}
	}
	if agentName == "" {
		agentName = payload.Description
	}
	executable := strings.TrimSpace(t.Executable)
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return "", err
		}
	}
	command := buildAgentToolCommandWithPluginDirs(executable, def, payload.Description, payload.Prompt, payload.Model, t.PluginDirs)
	env, err := toolEnvironment(ctx, t.ConfigHome, t.ConfigEnv)
	if err != nil {
		return "", err
	}
	cwd, err := toolCWD(ctx, t.ConfigHome, t.Workspace)
	if err != nil {
		return "", err
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).RunWithOptions(command, cwd, background.RunOptions{
		Kind:      "agent",
		AgentType: agentName,
		SessionID: payload.SessionID,
		Env:       env,
	})
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"kind":          "agent",
		"agent":         agentName,
		"description":   payload.Description,
		"subagent_type": strings.TrimSpace(payload.SubagentType),
		"definition":    found,
		"task":          task,
	}), nil
}

func findAgentDefinition(workspace string, definitions []agentdefs.Definition, name string, subagentType string) (agentdefs.Definition, bool, error) {
	defs := definitions
	if defs == nil {
		var err error
		defs, err = agentdefs.Load(workspace)
		if err != nil {
			return agentdefs.Definition{}, false, err
		}
	}
	candidates := []string{strings.TrimSpace(name), strings.TrimSpace(subagentType)}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		for _, def := range defs {
			if strings.EqualFold(def.Name, candidate) {
				return def, true, nil
			}
		}
	}
	return agentdefs.Definition{}, false, nil
}

func buildAgentToolCommandWithPluginDirs(executable string, def agentdefs.Definition, description string, prompt string, model string, pluginDirs []string) string {
	parts := []string{}
	if strings.TrimSpace(description) != "" {
		parts = append(parts, "Task: "+strings.TrimSpace(description))
	}
	if strings.TrimSpace(def.Prompt) != "" {
		parts = append(parts, strings.TrimSpace(def.Prompt))
	}
	parts = append(parts, strings.TrimSpace(prompt))
	args := []string{shellQuoteToolArg(executable)}
	if strings.TrimSpace(model) != "" {
		args = append(args, "--model", shellQuoteToolArg(strings.TrimSpace(model)))
	}
	if len(def.Tools) > 0 {
		args = append(args, "--tools", shellQuoteToolArg(strings.Join(def.Tools, ",")))
	}
	for _, dir := range pluginDirs {
		if strings.TrimSpace(dir) != "" {
			args = append(args, "--plugin-dir", shellQuoteToolArg(strings.TrimSpace(dir)))
		}
	}
	args = append(args, "prompt", shellQuoteToolArg(strings.Join(parts, "\n\n")))
	return strings.Join(args, " ")
}

func shellQuoteToolArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

type CronCreateTool struct {
	ConfigHome string
}

func (CronCreateTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "cron_create",
		Description: "Create a scheduled recurring Codog task registry entry.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"schedule":    map[string]any{"type": "string"},
				"prompt":      map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
			},
			"required":             []string{"schedule", "prompt"},
			"additionalProperties": false,
		},
	}
}

func (CronCreateTool) Permission() Permission { return PermissionDanger }

func (t CronCreateTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Schedule    string `json:"schedule"`
		Prompt      string `json:"prompt"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	entry, err := cron.NewStore(t.ConfigHome).Create(payload.Schedule, payload.Prompt, payload.Description)
	if err != nil {
		return "", err
	}
	return pretty(entry), nil
}

type CronDeleteTool struct {
	ConfigHome string
}

func (CronDeleteTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "cron_delete",
		Description: "Delete a scheduled recurring Codog task by cron id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cron_id": map[string]any{"type": "string"},
			},
			"required":             []string{"cron_id"},
			"additionalProperties": false,
		},
	}
}

func (CronDeleteTool) Permission() Permission { return PermissionDanger }

func (t CronDeleteTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		CronID string `json:"cron_id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	entry, err := cron.NewStore(t.ConfigHome).Delete(payload.CronID)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"cron_id":  entry.ID,
		"schedule": entry.Schedule,
		"status":   "deleted",
		"message":  "Cron entry removed",
	}), nil
}

type CronListTool struct {
	ConfigHome string
}

func (CronListTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "cron_list",
		Description: "List scheduled recurring Codog tasks.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
	}
}

func (CronListTool) Permission() Permission { return PermissionReadOnly }

func (t CronListTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	if len(input) != 0 {
		var payload map[string]any
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	entries, err := cron.NewStore(t.ConfigHome).List()
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{"crons": entries, "count": len(entries)}), nil
}

type TeamCreateTool struct {
	Workspace  string
	ConfigHome string
	ConfigEnv  map[string]string
	Executable string
}

func (TeamCreateTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "team_create",
		Description: "Create a team of background Codog sub-agent tasks for parallel execution.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
				"tasks": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"prompt":      map[string]any{"type": "string"},
							"description": map[string]any{"type": "string"},
						},
						"required":             []string{"prompt"},
						"additionalProperties": false,
					},
				},
				"session_id": map[string]any{"type": "string"},
			},
			"required":             []string{"name", "tasks"},
			"additionalProperties": false,
		},
	}
}

func (TeamCreateTool) Permission() Permission { return PermissionDanger }

func (t TeamCreateTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Name      string          `json:"name"`
		Tasks     []team.TaskSpec `json:"tasks"`
		SessionID string          `json:"session_id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Name) == "" {
		return "", errors.New("name is required")
	}
	if len(payload.Tasks) == 0 {
		return "", errors.New("tasks are required")
	}
	executable := strings.TrimSpace(t.Executable)
	var err error
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return "", err
		}
	}
	store := taskStore(t.ConfigHome, t.Workspace)
	env, err := toolEnvironment(ctx, t.ConfigHome, t.ConfigEnv)
	if err != nil {
		return "", err
	}
	cwd, err := toolCWD(ctx, t.ConfigHome, t.Workspace)
	if err != nil {
		return "", err
	}
	taskIDs := make([]string, 0, len(payload.Tasks))
	for _, task := range payload.Tasks {
		prompt := strings.TrimSpace(task.Prompt)
		if prompt == "" {
			return "", errors.New("task prompt is required")
		}
		description := strings.TrimSpace(task.Description)
		if description != "" {
			prompt = "Task: " + description + "\n\n" + prompt
		}
		started, err := store.RunWithOptions(buildTeamTaskCommand(executable, prompt), cwd, background.RunOptions{
			Kind:      "team",
			SessionID: payload.SessionID,
			Env:       env,
		})
		if err != nil {
			return "", err
		}
		taskIDs = append(taskIDs, started.ID)
	}
	created, err := team.NewStore(t.ConfigHome).Create(payload.Name, payload.Tasks, taskIDs)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"team_id":    created.ID,
		"name":       created.Name,
		"task_count": len(created.TaskIDs),
		"task_ids":   created.TaskIDs,
		"status":     created.Status,
		"created_at": created.CreatedAt,
	}), nil
}

type TeamListTool struct {
	Workspace  string
	ConfigHome string
}

func (TeamListTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "team_list",
		Description: "List team task groups and summarize their background task states.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"status": map[string]any{"type": "string"},
			},
		},
	}
}

func (TeamListTool) Permission() Permission { return PermissionReadOnly }

func (t TeamListTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Status string `json:"status"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	status := strings.TrimSpace(payload.Status)
	teams, err := team.NewStore(t.ConfigHome).List()
	if err != nil {
		return "", err
	}
	out := make([]map[string]any, 0, len(teams))
	for _, item := range teams {
		if status != "" && !strings.EqualFold(item.Status, status) {
			continue
		}
		out = append(out, teamSummary(t.ConfigHome, item))
	}
	return pretty(map[string]any{
		"kind":   "team_list",
		"total":  len(out),
		"status": status,
		"teams":  out,
	}), nil
}

type TeamGetTool struct {
	Workspace  string
	ConfigHome string
}

func (TeamGetTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "team_get",
		Description: "Fetch a team task group with task prompts and current background task states.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"team_id": map[string]any{"type": "string"},
			},
			"required": []string{"team_id"},
		},
	}
}

func (TeamGetTool) Permission() Permission { return PermissionReadOnly }

func (t TeamGetTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		TeamID string `json:"team_id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	item, err := team.NewStore(t.ConfigHome).Get(payload.TeamID)
	if err != nil {
		return "", err
	}
	summary := teamSummary(t.ConfigHome, item)
	summary["kind"] = "team"
	summary["tasks"] = item.Tasks
	return pretty(summary), nil
}

type TeamDeleteTool struct {
	ConfigHome string
}

func (TeamDeleteTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "team_delete",
		Description: "Delete a team and stop all background tasks associated with it.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"team_id": map[string]any{"type": "string"},
			},
			"required":             []string{"team_id"},
			"additionalProperties": false,
		},
	}
}

func (TeamDeleteTool) Permission() Permission { return PermissionDanger }

func (t TeamDeleteTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		TeamID string `json:"team_id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	teamStore := team.NewStore(t.ConfigHome)
	existing, err := teamStore.Get(payload.TeamID)
	if err != nil {
		return "", err
	}
	stopped := []string{}
	taskStore := background.NewStore(t.ConfigHome)
	for _, id := range existing.TaskIDs {
		if task, err := taskStore.Stop(id); err == nil {
			stopped = append(stopped, task.ID)
		}
	}
	deleted, err := teamStore.MarkDeleted(payload.TeamID)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"team_id":       deleted.ID,
		"name":          deleted.Name,
		"status":        deleted.Status,
		"stopped_tasks": stopped,
		"message":       "Team deleted",
	}), nil
}

func teamSummary(configHome string, item team.Team) map[string]any {
	return map[string]any{
		"team_id":       item.ID,
		"name":          item.Name,
		"status":        item.Status,
		"task_count":    len(item.Tasks),
		"task_ids":      item.TaskIDs,
		"task_statuses": teamTaskStatuses(configHome, item.TaskIDs),
		"created_at":    item.CreatedAt,
		"updated_at":    item.UpdatedAt,
	}
}

func teamTaskStatuses(configHome string, ids []string) []map[string]any {
	out := make([]map[string]any, 0, len(ids))
	store := background.NewStore(configHome)
	for _, id := range ids {
		status := map[string]any{"id": id, "status": "unknown"}
		task, err := store.Status(id)
		if err != nil {
			status["error"] = err.Error()
		} else {
			status["status"] = task.Status
			status["kind"] = task.Kind
			status["exit_code"] = task.ExitCode
			status["started_at"] = task.StartedAt
			status["completed_at"] = task.CompletedAt
		}
		out = append(out, status)
	}
	return out
}

func buildTeamTaskCommand(executable string, prompt string) string {
	return strings.Join([]string{shellQuoteToolArg(executable), "prompt", shellQuoteToolArg(prompt)}, " ")
}

type RecoveryRecipeTool struct {
	ConfigHome string
}

func (RecoveryRecipeTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "recovery_recipe",
		Description: "Return known automatic recovery recipes for common coding-agent failure scenarios.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"scenario": map[string]any{"type": "string"},
			},
		},
	}
}

func (RecoveryRecipeTool) Permission() Permission { return PermissionReadOnly }

func (RecoveryRecipeTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	scenario, hasScenario, err := parseOptionalRecoveryScenario(input)
	if err != nil {
		return "", err
	}
	if hasScenario {
		recipe, err := recovery.RecipeFor(scenario)
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"kind": "recovery_recipe", "recipe": recipe}), nil
	}
	recipes := []recovery.Recipe{}
	for _, scenario := range recovery.AllScenarios() {
		recipe, err := recovery.RecipeFor(scenario)
		if err != nil {
			return "", err
		}
		recipes = append(recipes, recipe)
	}
	return pretty(map[string]any{"kind": "recovery_recipes", "recipes": recipes}), nil
}

type RecoveryAttemptTool struct {
	ConfigHome string
}

func (RecoveryAttemptTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "recovery_attempt",
		Description: "Record one automatic recovery attempt for a failure scenario and update the recovery ledger.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"scenario":          map[string]any{"type": "string"},
				"failure_summary":   map[string]any{"type": "string"},
				"failed_step_index": map[string]any{"type": "integer", "minimum": 0},
			},
			"required": []string{"scenario"},
		},
	}
}
