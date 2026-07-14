package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/acpserver"
	"github.com/Rememorio/codog/internal/agentruns"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/bridge"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/workspaceops"
)

func (a *App) serveACP(ctx context.Context) error {
	in := a.In
	if in == nil {
		in = os.Stdin
	}
	out := a.Out
	if out == nil {
		out = os.Stdout
	}
	return acpserver.Serve(ctx, in, out, a.acpHandlers(), acpserver.Options{Version: version, Workspace: a.Workspace})
}

func (a *App) acpHandlers() acpserver.Handlers {
	handlers := acpserver.Handlers{}
	a.addACPSessionHandlers(&handlers)
	a.addACPWorkspaceHandlers(&handlers)
	a.addACPEditorHandlers(&handlers)
	a.addACPCodeIntelCoreHandlers(&handlers)
	a.addACPCodeIntelNavigationHandlers(&handlers)
	a.addACPNotebookReadHandler(&handlers)
	a.addACPNotebookEditHandler(&handlers)
	a.addACPLSPMetadataHandlers(&handlers)
	a.addACPLSPLifecycleHandlers(&handlers)
	a.addACPLSPQueryHandler(&handlers)
	a.addACPBackgroundLookupHandlers(&handlers)
	a.addACPBackgroundStatusHandlers(&handlers)
	a.addACPBackgroundLifecycleHandlers(&handlers)
	a.addACPBackgroundMaintenanceHandlers(&handlers)
	a.addACPAgentRunsLookupHandlers(&handlers)
	a.addACPAgentRunsStatusHandlers(&handlers)
	a.addACPAgentRunsHeartbeatHandler(&handlers)
	a.addACPAgentRunsMaintenanceHandlers(&handlers)
	a.addACPMCPServerHandlers(&handlers)
	a.addACPMCPToolHandlers(&handlers)
	a.addACPMCPResourceHandlers(&handlers)
	a.addACPMCPPromptHandlers(&handlers)
	a.addACPSessionLookupHandlers(&handlers)
	a.addACPSessionHistoryHandlers(&handlers)
	a.addACPSessionAppendHandlers(&handlers)
	a.addACPSessionBranchHandlers(&handlers)
	a.addACPSessionRenameHandler(&handlers)
	a.addACPSessionCleanupHandlers(&handlers)
	a.addACPPromptHandlers(&handlers)
	return handlers
}

func (a *App) acpBackgroundStore() (background.Store, error) {
	if strings.TrimSpace(a.Config.ConfigHome) == "" {
		return background.Store{}, errors.New("config home is required")
	}
	return background.NewStore(a.Config.ConfigHome), nil
}

func (a *App) acpAgentRunStores() (agentruns.Store, background.Store, error) {
	if strings.TrimSpace(a.Config.ConfigHome) == "" {
		return agentruns.Store{}, background.Store{}, errors.New("config home is required")
	}
	return agentruns.NewStore(a.Config.ConfigHome), background.NewStore(a.Config.ConfigHome), nil
}

func (a *App) acpBridgeServer() bridge.Server {
	return bridge.Server{
		Sessions:   a.Sessions,
		Version:    version,
		Workspace:  a.Workspace,
		ConfigHome: a.Config.ConfigHome,
		TrustToken: a.Config.Future.EditorBridgeToken,
		MCPServers: a.Config.MCPServers,
	}
}

func (a *App) acpMCPServer(name string) (config.MCPServerConfig, error) {
	name = strings.TrimSpace(name)
	server, ok := a.Config.MCPServers[name]
	if !ok {
		return config.MCPServerConfig{}, fmt.Errorf("mcp server %q is not configured", name)
	}
	return server, nil
}

func (a *App) addACPSessionHandlers(h *acpserver.Handlers) {
	h.NewSession = func(context.Context) (acpserver.SessionInfo, error) {
		if a.Sessions == nil {
			return acpserver.SessionInfo{}, errors.New("session store is unavailable")
		}
		sess, err := a.Sessions.Open("")
		if err != nil {
			return acpserver.SessionInfo{}, err
		}
		return acpserver.SessionInfo{SessionID: sess.ID, Workspace: a.Workspace}, nil
	}
}

func (a *App) addACPWorkspaceHandlers(h *acpserver.Handlers) {
	h.WorkspaceInfo = func(context.Context) (workspaceops.InfoResult, error) {
		return (workspaceops.Service{Workspace: a.Workspace}).Info()
	}
	h.WorkspaceFiles = func(_ context.Context, options workspaceops.FilesOptions) (workspaceops.FilesResult, error) {
		return (workspaceops.Service{Workspace: a.Workspace}).Files(options)
	}
	h.WorkspaceSearch = func(_ context.Context, options workspaceops.SearchOptions) (workspaceops.SearchResult, error) {
		return (workspaceops.Service{Workspace: a.Workspace}).Search(options)
	}
	h.FileRead = func(_ context.Context, options workspaceops.ReadOptions) (workspaceops.ReadResult, error) {
		return (workspaceops.Service{Workspace: a.Workspace}).Read(options)
	}
	h.FileWrite = func(_ context.Context, options workspaceops.WriteOptions) (workspaceops.WriteResult, error) {
		return (workspaceops.Service{Workspace: a.Workspace}).Write(options)
	}
	h.FileEdit = func(_ context.Context, options workspaceops.EditOptions) (workspaceops.EditResult, error) {
		return (workspaceops.Service{Workspace: a.Workspace}).Edit(options)
	}
	h.FileDiff = func(_ context.Context, options workspaceops.DiffOptions) (workspaceops.DiffResult, error) {
		return (workspaceops.Service{Workspace: a.Workspace}).Diff(options)
	}
}

func (a *App) addACPEditorHandlers(h *acpserver.Handlers) {
	h.EditorIdentify = func(_ context.Context, req acpserver.EditorIdentifyRequest) (any, error) {
		params, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		return a.acpBridgeServer().IdentifyEditor(params)
	}
	h.EditorState = func(context.Context) (any, error) {
		return a.acpBridgeServer().EditorState()
	}
	h.EditorOpen = func(_ context.Context, req acpserver.EditorOpenRequest) (any, error) {
		params, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		return a.acpBridgeServer().OpenEditorFile(params)
	}
	h.EditorSelection = func(_ context.Context, req acpserver.EditorSelectionRequest) (any, error) {
		params, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		return a.acpBridgeServer().SetEditorSelection(params)
	}
	h.BridgeFaultsList = func(context.Context) (any, error) {
		events, err := a.acpBridgeServer().BridgeFaults()
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "bridge_faults", "total": len(events), "events": events}, nil
	}
	h.BridgeFaultsRecord = func(_ context.Context, req acpserver.BridgeFaultRecordRequest) (any, error) {
		server := a.acpBridgeServer()
		event, err := server.RecordBridgeFault(req.Action, req.Args)
		if err != nil {
			return nil, err
		}
		events, err := server.BridgeFaults()
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "bridge_faults", "total": len(events), "recorded": event, "events": events}, nil
	}
	h.BridgeFaultsClear = func(context.Context) (any, error) {
		if err := a.acpBridgeServer().ClearBridgeFaults(); err != nil {
			return nil, err
		}
		return map[string]any{"kind": "bridge_faults", "cleared": true, "total": 0, "events": []bridge.FaultEvent{}}, nil
	}
}

func (a *App) addACPCodeIntelCoreHandlers(h *acpserver.Handlers) {
	h.DiagnosticsGo = func(ctx context.Context, req acpserver.DiagnosticsRequest) (any, error) {
		diagnostics, err := codeintel.GoDiagnostics(ctx, a.Workspace, req.Patterns)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "diagnostics", "diagnostics": diagnostics, "total": len(diagnostics)}, nil
	}
	h.CodeSymbols = func(_ context.Context, req acpserver.CodeSymbolsRequest) (any, error) {
		symbols, err := codeintel.GoSymbols(a.Workspace)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(req.Path) != "" {
			_, rel, err := (workspaceops.Service{Workspace: a.Workspace}).Resolve(req.Path, false)
			if err != nil {
				return nil, err
			}
			filtered := symbols[:0]
			for _, symbol := range symbols {
				if filepath.ToSlash(symbol.Path) == filepath.ToSlash(rel) {
					filtered = append(filtered, symbol)
				}
			}
			symbols = filtered
		}
		return map[string]any{"kind": "symbols", "total": len(symbols), "symbols": symbols}, nil
	}
	h.CodeReferences = func(_ context.Context, req acpserver.CodeReferencesRequest) (any, error) {
		refs, err := codeintel.References(a.Workspace, req.Symbol, req.Limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "references", "symbol": strings.TrimSpace(req.Symbol), "total": len(refs), "references": refs}, nil
	}
}

func (a *App) addACPCodeIntelNavigationHandlers(h *acpserver.Handlers) {
	h.CodeDefinition = func(_ context.Context, req acpserver.CodeDefinitionRequest) (any, error) {
		definition, found, err := codeintel.Definition(a.Workspace, req.Symbol)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "definition", "symbol": strings.TrimSpace(req.Symbol), "found": found, "definition": definition}, nil
	}
	h.CodeHover = func(_ context.Context, req acpserver.CodeHoverRequest) (any, error) {
		hover, err := codeintel.HoverInfo(a.Workspace, req.Symbol, req.ContextLines)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "hover", "symbol": strings.TrimSpace(req.Symbol), "hover": hover}, nil
	}
	h.CodeCompletion = func(_ context.Context, req acpserver.CodeCompletionRequest) (any, error) {
		completions, err := codeintel.Completions(a.Workspace, req.Query, req.Limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "completion", "query": strings.TrimSpace(req.Query), "total": len(completions), "completions": completions}, nil
	}
	h.CodeFormat = func(_ context.Context, req acpserver.CodeFormatRequest) (any, error) {
		result, err := codeintel.FormatGoFile(a.Workspace, req.Path, req.Write)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "format", "write": req.Write, "result": result}, nil
	}
}

func (a *App) addACPNotebookReadHandler(h *acpserver.Handlers) {
	h.NotebookRead = func(_ context.Context, req acpserver.NotebookReadRequest) (any, error) {
		cellIndex := req.CellIndex
		if cellIndex == nil {
			cellIndex = req.Index
		}
		if cellIndex != nil && *cellIndex < 0 {
			return nil, errors.New("cell_index must be non-negative")
		}
		includeOutputs := req.IncludeOutputs
		if req.Outputs != nil {
			includeOutputs = *req.Outputs
		}
		path, err := a.resolveCodeIntelNotebookPath(firstNonEmpty(req.NotebookPath, req.Path))
		if err != nil {
			return nil, err
		}
		result, err := codeintel.ReadNotebook(path, codeintel.NotebookReadOptions{
			CellIndex:      cellIndex,
			Limit:          req.Limit,
			IncludeOutputs: includeOutputs,
		})
		if err != nil {
			return nil, err
		}
		result.Path = displayCodeIntelNotebookPath(a.Workspace, path)
		return result, nil
	}
}

func (a *App) addACPNotebookEditHandler(h *acpserver.Handlers) {
	h.NotebookEdit = func(_ context.Context, req acpserver.NotebookEditRequest) (any, error) {
		cellIndex := req.CellIndex
		if cellIndex == nil {
			cellIndex = req.Index
		}
		if cellIndex != nil && *cellIndex < 0 {
			return nil, errors.New("cell_index must be non-negative")
		}
		if cellIndex != nil && strings.TrimSpace(req.CellID) != "" {
			return nil, errors.New("notebook/edit accepts either cell_index or cell_id, not both")
		}
		mode, err := codeintel.NormalizeNotebookEditMode(firstNonEmpty(req.Mode, req.EditMode))
		if err != nil {
			return nil, err
		}
		source, sourceSet := "", false
		if req.Source != nil {
			source = *req.Source
			sourceSet = true
		}
		if req.NewSource != nil {
			source = *req.NewSource
			sourceSet = true
		}
		if (mode == "replace" || mode == "insert") && !sourceSet {
			return nil, errors.New("new_source is required for insert and replace edits")
		}
		path, err := a.resolveCodeIntelNotebookPath(firstNonEmpty(req.NotebookPath, req.Path))
		if err != nil {
			return nil, err
		}
		index, err := codeintel.ResolveNotebookEditIndex(path, cellIndex, req.CellID, mode)
		if err != nil {
			return nil, err
		}
		result, err := codeintel.EditNotebook(path, codeintel.NotebookEditOptions{
			Index:    index,
			CellType: firstNonEmpty(req.CellType, req.Type),
			Source:   source,
			Mode:     mode,
		})
		if err != nil {
			return nil, err
		}
		result.Path = displayCodeIntelNotebookPath(a.Workspace, path)
		return map[string]any{"kind": "notebook_edit", "result": result}, nil
	}
}

func (a *App) addACPLSPMetadataHandlers(h *acpserver.Handlers) {
	h.LSPActions = func(context.Context) (any, error) {
		actions := codeintel.SupportedLSPActions()
		return map[string]any{
			"kind":    "lsp_actions",
			"action":  "actions",
			"status":  "ok",
			"count":   len(actions),
			"actions": actions,
		}, nil
	}
	h.LSPDiscover = func(context.Context) (any, error) {
		candidates := codeintel.DefaultLSPCandidates()
		return map[string]any{
			"kind":       "lsp_discover",
			"candidates": candidates,
			"count":      len(candidates),
		}, nil
	}
	h.LSPList = func(context.Context) (any, error) {
		store := codeintel.NewLSPStore(a.Config.ConfigHome, a.Workspace)
		statuses, err := store.List()
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"kind":    "lsp_list",
			"count":   len(statuses),
			"servers": statuses,
		}, nil
	}
}

func (a *App) addACPLSPLifecycleHandlers(h *acpserver.Handlers) {
	h.LSPStart = func(_ context.Context, req acpserver.LSPStartRequest) (any, error) {
		store := codeintel.NewLSPStore(a.Config.ConfigHome, a.Workspace)
		status, err := store.Start(req.Language, req.CommandArgs)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "lsp_start", "status": "ok", "server": status}, nil
	}
	h.LSPStatus = func(_ context.Context, req acpserver.LSPStatusRequest) (any, error) {
		store := codeintel.NewLSPStore(a.Config.ConfigHome, a.Workspace)
		status, err := store.Status(req.Language)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "lsp_status", "server": status}, nil
	}
	h.LSPStop = func(_ context.Context, req acpserver.LSPStopRequest) (any, error) {
		store := codeintel.NewLSPStore(a.Config.ConfigHome, a.Workspace)
		status, err := store.Stop(req.Language)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "lsp_stop", "status": "ok", "server": status}, nil
	}
}

func (a *App) addACPLSPQueryHandler(h *acpserver.Handlers) {
	h.LSPQuery = func(ctx context.Context, req acpserver.LSPQueryRequest) (any, error) {
		if req.TimeoutMS > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutMS)*time.Millisecond)
			defer cancel()
		}
		store := codeintel.NewLSPStore(a.Config.ConfigHome, a.Workspace)
		return store.Query(ctx, req.Language, codeintel.LSPQueryRequest{
			Action:          req.Action,
			Path:            firstNonEmpty(req.Path, req.FilePath),
			Query:           req.Query,
			Line:            req.Line,
			Character:       req.Character,
			NewName:         req.NewName,
			CodeActionTitle: req.CodeActionTitle,
			Apply:           req.Apply || req.Write,
		})
	}
}
