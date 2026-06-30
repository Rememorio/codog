package acpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type Options struct {
	Version   string
	Workspace string
}

type Handlers struct {
	NewSession   func(context.Context) (SessionInfo, error)
	ListSessions func(context.Context) (SessionList, error)
	GetSession   func(context.Context, SessionLookupRequest) (SessionDetail, error)
	History      func(context.Context, SessionHistoryRequest) (SessionHistory, error)
	Prompt       func(context.Context, PromptRequest) (PromptResult, error)
	Status       func(context.Context) (any, error)
}

type SessionInfo struct {
	SessionID string `json:"session_id"`
	Workspace string `json:"workspace,omitempty"`
}

type SessionSummary struct {
	SessionID    string `json:"session_id"`
	Workspace    string `json:"workspace,omitempty"`
	Path         string `json:"path,omitempty"`
	MessageCount int    `json:"message_count"`
}

type SessionList struct {
	Kind      string           `json:"kind"`
	Count     int              `json:"count"`
	Sessions  []SessionSummary `json:"sessions"`
	Workspace string           `json:"workspace,omitempty"`
}

type SessionLookupRequest struct {
	SessionID string `json:"session_id,omitempty"`
}

type SessionDetail struct {
	SessionID    string `json:"session_id"`
	Workspace    string `json:"workspace,omitempty"`
	Path         string `json:"path,omitempty"`
	MessageCount int    `json:"message_count"`
	Messages     any    `json:"messages,omitempty"`
}

type SessionHistoryRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type SessionHistory struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
	Count     int    `json:"count"`
	Entries   any    `json:"entries"`
}

type PromptRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Prompt    string `json:"prompt"`
}

type PromptResult struct {
	SessionID string `json:"session_id"`
	Output    string `json:"output"`
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *responseError  `json:"error,omitempty"`
}

type responseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func Serve(ctx context.Context, in io.Reader, out io.Writer, handlers Handlers, opts Options) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			if err := writeError(out, nil, -32700, err.Error()); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(req.Method, "notifications/") || len(req.ID) == 0 {
			continue
		}
		stop, err := handle(ctx, out, handlers, opts, req)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return scanner.Err()
}

func handle(ctx context.Context, out io.Writer, handlers Handlers, opts Options, req request) (bool, error) {
	switch req.Method {
	case "initialize":
		return false, writeResult(out, req.ID, initializeResult(opts))
	case "shutdown":
		return true, writeResult(out, req.ID, map[string]any{})
	case "status":
		return false, handleStatus(ctx, out, handlers, opts, req)
	case "session/new", "session/create", "sessions/new":
		return false, handleNewSession(ctx, out, handlers, opts, req)
	case "session/list", "sessions/list":
		return false, handleListSessions(ctx, out, handlers, opts, req)
	case "session/get", "sessions/get", "session/read":
		return false, handleGetSession(ctx, out, handlers, opts, req)
	case "session/history", "sessions/history", "history":
		return false, handleHistory(ctx, out, handlers, req)
	case "prompt", "session/prompt":
		return false, handlePrompt(ctx, out, handlers, req)
	default:
		return false, writeError(out, req.ID, -32601, "method not found: "+req.Method)
	}
}

func initializeResult(opts Options) map[string]any {
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = "0.1.0"
	}
	return map[string]any{
		"protocolVersion": "codog-acp-0.1",
		"serverInfo":      map[string]any{"name": "codog", "version": version},
		"capabilities": map[string]any{
			"sessions": map[string]any{
				"new":     true,
				"list":    true,
				"get":     true,
				"history": true,
			},
			"prompt": true,
			"status": true,
		},
	}
}

func handleStatus(ctx context.Context, out io.Writer, handlers Handlers, opts Options, req request) error {
	if handlers.Status != nil {
		status, err := handlers.Status(ctx)
		if err != nil {
			return writeError(out, req.ID, -32603, err.Error())
		}
		return writeResult(out, req.ID, status)
	}
	return writeResult(out, req.ID, map[string]any{
		"kind":      "acp",
		"status":    "ok",
		"workspace": opts.Workspace,
	})
}

func handleNewSession(ctx context.Context, out io.Writer, handlers Handlers, opts Options, req request) error {
	if handlers.NewSession == nil {
		return writeError(out, req.ID, -32603, "session handler is not configured")
	}
	info, err := handlers.NewSession(ctx)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	if strings.TrimSpace(info.Workspace) == "" {
		info.Workspace = opts.Workspace
	}
	return writeResult(out, req.ID, info)
}

func handleListSessions(ctx context.Context, out io.Writer, handlers Handlers, opts Options, req request) error {
	if handlers.ListSessions == nil {
		return writeError(out, req.ID, -32603, "session list handler is not configured")
	}
	list, err := handlers.ListSessions(ctx)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	if strings.TrimSpace(list.Kind) == "" {
		list.Kind = "session_list"
	}
	if strings.TrimSpace(list.Workspace) == "" {
		list.Workspace = opts.Workspace
	}
	list.Count = len(list.Sessions)
	return writeResult(out, req.ID, list)
}

func handleGetSession(ctx context.Context, out io.Writer, handlers Handlers, opts Options, req request) error {
	if handlers.GetSession == nil {
		return writeError(out, req.ID, -32603, "session get handler is not configured")
	}
	lookup, err := parseSessionLookupRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	detail, err := handlers.GetSession(ctx, lookup)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	if strings.TrimSpace(detail.Workspace) == "" {
		detail.Workspace = opts.Workspace
	}
	return writeResult(out, req.ID, detail)
}

func handleHistory(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.History == nil {
		return writeError(out, req.ID, -32603, "session history handler is not configured")
	}
	historyReq, err := parseSessionHistoryRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	history, err := handlers.History(ctx, historyReq)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	if strings.TrimSpace(history.Kind) == "" {
		history.Kind = "session_history"
	}
	return writeResult(out, req.ID, history)
}

func handlePrompt(ctx context.Context, out io.Writer, handlers Handlers, req request) error {
	if handlers.Prompt == nil {
		return writeError(out, req.ID, -32603, "prompt handler is not configured")
	}
	promptReq, err := parsePromptRequest(req.Params)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	result, err := handlers.Prompt(ctx, promptReq)
	if err != nil {
		return writeError(out, req.ID, -32603, err.Error())
	}
	return writeResult(out, req.ID, map[string]any{
		"session_id": result.SessionID,
		"text":       result.Output,
		"content":    []map[string]string{{"type": "text", "text": result.Output}},
	})
}

func parseSessionLookupRequest(params json.RawMessage) (SessionLookupRequest, error) {
	var raw struct {
		SessionID      string `json:"session_id"`
		SessionIDCamel string `json:"sessionId"`
		ID             string `json:"id"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &raw); err != nil {
			return SessionLookupRequest{}, err
		}
	}
	sessionID := firstNonEmpty(raw.SessionID, raw.SessionIDCamel, raw.ID)
	if strings.TrimSpace(sessionID) == "" {
		sessionID = "latest"
	}
	return SessionLookupRequest{SessionID: sessionID}, nil
}

func parseSessionHistoryRequest(params json.RawMessage) (SessionHistoryRequest, error) {
	var raw struct {
		SessionID      string `json:"session_id"`
		SessionIDCamel string `json:"sessionId"`
		ID             string `json:"id"`
		Limit          int    `json:"limit"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &raw); err != nil {
			return SessionHistoryRequest{}, err
		}
	}
	sessionID := firstNonEmpty(raw.SessionID, raw.SessionIDCamel, raw.ID)
	if strings.TrimSpace(sessionID) == "" {
		sessionID = "latest"
	}
	return SessionHistoryRequest{SessionID: sessionID, Limit: raw.Limit}, nil
}

func parsePromptRequest(params json.RawMessage) (PromptRequest, error) {
	var raw struct {
		SessionID      string `json:"session_id"`
		SessionIDCamel string `json:"sessionId"`
		Prompt         string `json:"prompt"`
		Input          string `json:"input"`
		Text           string `json:"text"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &raw); err != nil {
			return PromptRequest{}, err
		}
	}
	prompt := firstNonEmpty(raw.Prompt, raw.Input, raw.Text)
	if strings.TrimSpace(prompt) == "" {
		return PromptRequest{}, fmt.Errorf("prompt is required")
	}
	return PromptRequest{
		SessionID: firstNonEmpty(raw.SessionID, raw.SessionIDCamel),
		Prompt:    prompt,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func writeResult(out io.Writer, id json.RawMessage, result any) error {
	data, err := json.Marshal(response{JSONRPC: "2.0", ID: id, Result: result})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", data)
	return err
}

func writeError(out io.Writer, id json.RawMessage, code int, message string) error {
	data, err := json.Marshal(response{JSONRPC: "2.0", ID: id, Error: &responseError{Code: code, Message: message}})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", data)
	return err
}
