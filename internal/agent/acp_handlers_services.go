package agent

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"github.com/Rememorio/codog/internal/acpserver"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/session"
)

func (a *App) addACPMCPServerHandlers(h *acpserver.Handlers) {
	h.MCPList = func(ctx context.Context, req acpserver.MCPListRequest) (any, error) {
		names := sortedMCPServerNames(a.Config.MCPServers)
		descriptors := make([]mcp.ServerDescriptor, 0, len(names))
		for _, name := range names {
			descriptors = append(descriptors, mcp.DescribeServer(name, a.Config.MCPServers[name]))
		}
		inspect := true
		if req.Inspect != nil {
			inspect = *req.Inspect
		}
		result := map[string]any{
			"kind":        "mcp_list",
			"count":       len(names),
			"servers":     names,
			"descriptors": descriptors,
		}
		if inspect {
			statuses := mcp.InspectAll(ctx, a.Config.MCPServers)
			result["statuses"] = statuses
			result["startup"] = mcp.BuildStartupReport(statuses)
		}
		return result, nil
	}
	h.MCPShow = func(ctx context.Context, req acpserver.MCPServerRequest) (any, error) {
		name := firstNonEmpty(req.Server, req.Name)
		server, err := a.acpMCPServer(name)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"kind":       "mcp_show",
			"server":     name,
			"descriptor": mcp.DescribeServer(name, server),
			"status":     mcp.Inspect(ctx, name, server),
		}, nil
	}
}

func (a *App) addACPMCPToolHandlers(h *acpserver.Handlers) {
	h.MCPAuth = func(ctx context.Context, req acpserver.MCPServerRequest) (any, error) {
		name := firstNonEmpty(req.Server, req.Name)
		if strings.TrimSpace(name) != "" {
			server, err := a.acpMCPServer(name)
			if err != nil {
				return nil, err
			}
			return map[string]any{"kind": "mcp_auth", "server": name, "result": mcp.InspectAuth(ctx, name, server)}, nil
		}
		names := sortedMCPServerNames(a.Config.MCPServers)
		results := make([]mcp.AuthStatusResult, 0, len(names))
		for _, name := range names {
			results = append(results, mcp.InspectAuth(ctx, name, a.Config.MCPServers[name]))
		}
		return map[string]any{"kind": "mcp_auth", "count": len(results), "servers": results}, nil
	}
	h.MCPTools = func(ctx context.Context, req acpserver.MCPServerRequest) (any, error) {
		name := firstNonEmpty(req.Server, req.Name)
		if strings.TrimSpace(name) != "" {
			server, err := a.acpMCPServer(name)
			if err != nil {
				return nil, err
			}
			return map[string]any{"kind": "mcp_tools", "server": name, "result": mcp.ListTools(ctx, name, server)}, nil
		}
		names := sortedMCPServerNames(a.Config.MCPServers)
		results := make([]mcp.ToolListResult, 0, len(names))
		for _, name := range names {
			results = append(results, mcp.ListTools(ctx, name, a.Config.MCPServers[name]))
		}
		return map[string]any{"kind": "mcp_tools", "count": len(results), "servers": results}, nil
	}
	h.MCPCall = func(ctx context.Context, req acpserver.MCPCallRequest) (any, error) {
		server, err := a.acpMCPServer(req.Server)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "mcp_call", "server": req.Server, "tool": req.Tool, "result": mcp.CallTool(ctx, req.Server, server, req.Tool, req.Arguments)}, nil
	}
}

func (a *App) addACPMCPResourceHandlers(h *acpserver.Handlers) {
	h.MCPResources = func(ctx context.Context, req acpserver.MCPServerRequest) (any, error) {
		name := firstNonEmpty(req.Server, req.Name)
		if strings.TrimSpace(name) != "" {
			server, err := a.acpMCPServer(name)
			if err != nil {
				return nil, err
			}
			return map[string]any{"kind": "mcp_resources", "server": name, "result": mcp.ListResources(ctx, name, server)}, nil
		}
		names := sortedMCPServerNames(a.Config.MCPServers)
		results := make([]mcp.ResourceListResult, 0, len(names))
		for _, name := range names {
			results = append(results, mcp.ListResources(ctx, name, a.Config.MCPServers[name]))
		}
		return map[string]any{"kind": "mcp_resources", "count": len(results), "servers": results}, nil
	}
	h.MCPResourceTemplates = func(ctx context.Context, req acpserver.MCPServerRequest) (any, error) {
		name := firstNonEmpty(req.Server, req.Name)
		if strings.TrimSpace(name) != "" {
			server, err := a.acpMCPServer(name)
			if err != nil {
				return nil, err
			}
			return map[string]any{"kind": "mcp_resource_templates", "server": name, "result": mcp.ListResourceTemplates(ctx, name, server)}, nil
		}
		names := sortedMCPServerNames(a.Config.MCPServers)
		results := make([]mcp.ResourceTemplateListResult, 0, len(names))
		for _, name := range names {
			results = append(results, mcp.ListResourceTemplates(ctx, name, a.Config.MCPServers[name]))
		}
		return map[string]any{"kind": "mcp_resource_templates", "count": len(results), "servers": results}, nil
	}
	h.MCPRead = func(ctx context.Context, req acpserver.MCPReadRequest) (any, error) {
		server, err := a.acpMCPServer(req.Server)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "mcp_read", "server": req.Server, "uri": req.URI, "result": mcp.ReadResource(ctx, req.Server, server, req.URI)}, nil
	}
}

func (a *App) addACPMCPPromptHandlers(h *acpserver.Handlers) {
	h.MCPPrompts = func(ctx context.Context, req acpserver.MCPServerRequest) (any, error) {
		name := firstNonEmpty(req.Server, req.Name)
		if strings.TrimSpace(name) != "" {
			server, err := a.acpMCPServer(name)
			if err != nil {
				return nil, err
			}
			return map[string]any{"kind": "mcp_prompts", "server": name, "result": mcp.ListPrompts(ctx, name, server)}, nil
		}
		names := sortedMCPServerNames(a.Config.MCPServers)
		results := make([]mcp.PromptListResult, 0, len(names))
		for _, name := range names {
			results = append(results, mcp.ListPrompts(ctx, name, a.Config.MCPServers[name]))
		}
		return map[string]any{"kind": "mcp_prompts", "count": len(results), "servers": results}, nil
	}
	h.MCPPrompt = func(ctx context.Context, req acpserver.MCPPromptRequest) (any, error) {
		name := firstNonEmpty(req.Prompt, req.Name)
		server, err := a.acpMCPServer(req.Server)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "mcp_prompt", "server": req.Server, "prompt": name, "result": mcp.GetPrompt(ctx, req.Server, server, name, req.Arguments)}, nil
	}
}

func (a *App) addACPSessionLookupHandlers(h *acpserver.Handlers) {
	h.OpenSession = func(_ context.Context, req acpserver.SessionOpenRequest) (acpserver.SessionDetail, error) {
		if a.Sessions == nil {
			return acpserver.SessionDetail{}, errors.New("session store is unavailable")
		}
		sess, err := a.Sessions.Open(req.SessionID)
		if err != nil {
			return acpserver.SessionDetail{}, err
		}
		return acpSessionDetail(a.Workspace, sess), nil
	}
	h.ListSessions = func(context.Context) (acpserver.SessionList, error) {
		if a.Sessions == nil {
			return acpserver.SessionList{}, errors.New("session store is unavailable")
		}
		sessions, err := a.Sessions.List()
		if err != nil {
			return acpserver.SessionList{}, err
		}
		summaries := make([]acpserver.SessionSummary, 0, len(sessions))
		for _, sess := range sessions {
			summaries = append(summaries, acpSessionSummary(a.Workspace, &sess))
		}
		return acpserver.SessionList{
			Kind:      "session_list",
			Count:     len(summaries),
			Sessions:  summaries,
			Workspace: a.Workspace,
		}, nil
	}
}

func (a *App) addACPSessionHistoryHandlers(h *acpserver.Handlers) {
	h.GetSession = func(_ context.Context, req acpserver.SessionLookupRequest) (acpserver.SessionDetail, error) {
		if a.Sessions == nil {
			return acpserver.SessionDetail{}, errors.New("session store is unavailable")
		}
		sess, err := a.Sessions.OpenExisting(req.SessionID)
		if err != nil {
			return acpserver.SessionDetail{}, err
		}
		return acpSessionDetail(a.Workspace, sess), nil
	}
	h.History = func(_ context.Context, req acpserver.SessionHistoryRequest) (acpserver.SessionHistory, error) {
		if a.Sessions == nil {
			return acpserver.SessionHistory{}, errors.New("session store is unavailable")
		}
		sessionID := strings.TrimSpace(req.SessionID)
		if sessionID == "" || session.IsSessionReferenceAlias(sessionID) {
			latest, err := a.Sessions.LatestID()
			if err != nil {
				return acpserver.SessionHistory{}, err
			}
			sessionID = latest
		}
		entries, err := a.Sessions.PromptHistory(sessionID)
		if err != nil {
			return acpserver.SessionHistory{}, err
		}
		entries = limitACPHistory(entries, req.Limit)
		return acpserver.SessionHistory{
			Kind:      "session_history",
			SessionID: sessionID,
			Count:     len(entries),
			Entries:   entries,
		}, nil
	}
}

func (a *App) addACPSessionAppendHandlers(h *acpserver.Handlers) {
	h.AppendMessage = func(_ context.Context, req acpserver.SessionAppendMessageRequest) (acpserver.SessionMutationResult, error) {
		if a.Sessions == nil {
			return acpserver.SessionMutationResult{}, errors.New("session store is unavailable")
		}
		var msg anthropic.Message
		if req.Message != nil {
			msg = *req.Message
		} else {
			msg = anthropic.TextMessage(req.Role, req.Text)
		}
		if err := a.Sessions.Append(req.SessionID, msg); err != nil {
			return acpserver.SessionMutationResult{}, err
		}
		sess, err := a.Sessions.Open(req.SessionID)
		if err != nil {
			return acpserver.SessionMutationResult{}, err
		}
		return acpserver.SessionMutationResult{
			Kind:         "session_mutation",
			Action:       "append_message",
			Status:       "ok",
			SessionID:    sess.ID,
			Path:         sess.Path,
			MessageCount: len(sess.Messages),
		}, nil
	}
	h.AppendInput = func(_ context.Context, req acpserver.SessionAppendInputRequest) (acpserver.SessionMutationResult, error) {
		if a.Sessions == nil {
			return acpserver.SessionMutationResult{}, errors.New("session store is unavailable")
		}
		if err := a.Sessions.AppendInput(req.SessionID, req.Input); err != nil {
			return acpserver.SessionMutationResult{}, err
		}
		sess, err := a.Sessions.Open(req.SessionID)
		if err != nil {
			return acpserver.SessionMutationResult{}, err
		}
		return acpserver.SessionMutationResult{
			Kind:         "session_mutation",
			Action:       "append_input",
			Status:       "ok",
			SessionID:    sess.ID,
			Path:         sess.Path,
			MessageCount: len(sess.Messages),
		}, nil
	}
}

func (a *App) addACPSessionBranchHandlers(h *acpserver.Handlers) {
	h.RewindSession = func(_ context.Context, req acpserver.SessionRewindRequest) (acpserver.SessionRewindResult, error) {
		if a.Sessions == nil {
			return acpserver.SessionRewindResult{}, errors.New("session store is unavailable")
		}
		result, err := a.Sessions.Rewind(req.SessionID, req.RemoveMessages)
		if err != nil {
			return acpserver.SessionRewindResult{}, err
		}
		return acpserver.SessionRewindResult{
			Kind:              "session_mutation",
			Action:            "rewind",
			Status:            "ok",
			SessionID:         result.SessionID,
			Path:              result.Path,
			OriginalMessages:  result.OriginalMessages,
			RemainingMessages: result.RemainingMessages,
			RemovedMessages:   result.RemovedMessages,
		}, nil
	}
	h.ForkSession = func(_ context.Context, req acpserver.SessionForkRequest) (acpserver.SessionMutationResult, error) {
		if a.Sessions == nil {
			return acpserver.SessionMutationResult{}, errors.New("session store is unavailable")
		}
		forked, err := a.Sessions.Fork(req.SessionID, req.BranchName)
		if err != nil {
			return acpserver.SessionMutationResult{}, err
		}
		return acpserver.SessionMutationResult{
			Kind:         "session_mutation",
			Action:       "fork",
			Status:       "ok",
			SessionID:    forked.ID,
			Path:         forked.Path,
			MessageCount: len(forked.Messages),
		}, nil
	}
}

func (a *App) addACPSessionRenameHandler(h *acpserver.Handlers) {
	h.RenameSession = func(_ context.Context, req acpserver.SessionRenameRequest) (acpserver.SessionMutationResult, error) {
		if a.Sessions == nil {
			return acpserver.SessionMutationResult{}, errors.New("session store is unavailable")
		}
		result, err := a.Sessions.Rename(req.SessionID, req.NewSessionID)
		if err != nil {
			return acpserver.SessionMutationResult{}, err
		}
		return acpserver.SessionMutationResult{
			Kind:         "session_mutation",
			Action:       "rename",
			Status:       "ok",
			SessionID:    result.OldID,
			NewSessionID: result.NewID,
			Path:         result.NewPath,
			MessageCount: result.MessageCount,
		}, nil
	}
}

func (a *App) addACPSessionCleanupHandlers(h *acpserver.Handlers) {
	h.DeleteSession = func(_ context.Context, req acpserver.SessionLookupRequest) (acpserver.SessionMutationResult, error) {
		if a.Sessions == nil {
			return acpserver.SessionMutationResult{}, errors.New("session store is unavailable")
		}
		sessionID := strings.TrimSpace(req.SessionID)
		if sessionID == "" || session.IsSessionReferenceAlias(sessionID) {
			latest, err := a.Sessions.LatestID()
			if err != nil {
				return acpserver.SessionMutationResult{}, err
			}
			sessionID = latest
		}
		sess, err := a.Sessions.Open(sessionID)
		if err != nil {
			return acpserver.SessionMutationResult{}, err
		}
		if err := a.Sessions.Delete(sessionID); err != nil {
			return acpserver.SessionMutationResult{}, err
		}
		return acpserver.SessionMutationResult{
			Kind:         "session_mutation",
			Action:       "delete",
			Status:       "ok",
			SessionID:    sess.ID,
			Path:         sess.Path,
			MessageCount: len(sess.Messages),
		}, nil
	}
	h.PruneSessions = func(_ context.Context, req acpserver.SessionPruneRequest) (any, error) {
		if a.Sessions == nil {
			return nil, errors.New("session store is unavailable")
		}
		emptyOnly := false
		if req.EmptyOnly != nil {
			emptyOnly = *req.EmptyOnly
		} else if req.Keep == 0 {
			emptyOnly = true
		}
		if req.Keep == 0 && !emptyOnly {
			return nil, errors.New("empty_only=false requires keep")
		}
		return a.Sessions.Prune(session.PruneOptions{
			ExcludeID: strings.TrimSpace(req.ExcludeID),
			Keep:      req.Keep,
			EmptyOnly: emptyOnly,
			Confirm:   req.Confirm,
		})
	}
}

func (a *App) addACPPromptHandlers(h *acpserver.Handlers) {
	h.Prompt = func(ctx context.Context, req acpserver.PromptRequest) (acpserver.PromptResult, error) {
		if a.Sessions == nil {
			return acpserver.PromptResult{}, errors.New("session store is unavailable")
		}
		if a.Tools == nil {
			return acpserver.PromptResult{}, errors.New("tool registry is not initialized")
		}
		if err := a.RegisterMCPTools(ctx); err != nil {
			return acpserver.PromptResult{}, err
		}
		sess, err := a.Sessions.Open(req.SessionID)
		if err != nil {
			return acpserver.PromptResult{}, err
		}
		if err := a.runSessionStartHook(ctx, sess, "acp"); err != nil {
			return acpserver.PromptResult{}, err
		}
		var streamed bytes.Buffer
		previousOut := a.Out
		a.Out = &streamed
		defer func() {
			a.Out = previousOut
		}()
		if err := a.runSessionTurn(ctx, "acp", sess, req.Prompt, "completed"); err != nil {
			return acpserver.PromptResult{}, err
		}
		output := strings.TrimSpace(streamed.String())
		if output == "" {
			output = acpLastAssistantText(sess.Messages)
		}
		return acpserver.PromptResult{SessionID: sess.ID, Output: output}, nil
	}
	h.Status = func(context.Context) (any, error) {
		return buildACPStatusReport(), nil
	}
}
