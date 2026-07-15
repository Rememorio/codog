package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type concurrencySafeTool interface {
	ConcurrencySafe(json.RawMessage) bool
}

// AuthorizedExecution is a tool invocation whose permission decision has
// already been resolved. Its fields are private so only Registry can create a
// runnable value.
type AuthorizedExecution struct {
	tool     Tool
	feedback string
}

// AuthorizeExecution resolves a tool and performs its permission decision
// without starting the tool. This lets callers authorize in deterministic
// order before executing independent tools concurrently.
func (r *Registry) AuthorizeExecution(name string, input json.RawMessage, prompter *Prompter) (AuthorizedExecution, error) {
	_, tool, ok := r.resolve(name)
	if !ok {
		return AuthorizedExecution{}, r.unknownToolError(name)
	}
	decision := PermissionDecision{}
	if prompter != nil {
		var err error
		decision, err = prompter.AuthorizeDecision(name, tool.Permission(), input)
		if err != nil {
			return AuthorizedExecution{}, err
		}
	}
	return AuthorizedExecution{tool: tool, feedback: strings.TrimSpace(decision.Feedback)}, nil
}

// Execute runs an authorized tool invocation.
func (e AuthorizedExecution) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	if e.tool == nil {
		return "", fmt.Errorf("tool execution was not authorized")
	}
	output, err := e.tool.Execute(ctx, input)
	if err != nil {
		if e.feedback != "" {
			return output, fmt.Errorf("%w; permission feedback: %s", err, e.feedback)
		}
		return output, err
	}
	if e.feedback != "" {
		return appendPermissionFeedback(output, e.feedback), nil
	}
	return output, nil
}

// ConcurrencySafe reports whether a tool invocation explicitly guarantees it
// can run alongside other safe invocations without changing shared state.
func (r *Registry) ConcurrencySafe(name string, input json.RawMessage) bool {
	_, tool, ok := r.resolve(name)
	if !ok || tool.Permission() != PermissionReadOnly {
		return false
	}
	safe, ok := tool.(concurrencySafeTool)
	return ok && safe.ConcurrencySafe(input)
}

func (ReadFileTool) ConcurrencySafe(json.RawMessage) bool     { return true }
func (GrepTool) ConcurrencySafe(json.RawMessage) bool         { return true }
func (GlobTool) ConcurrencySafe(json.RawMessage) bool         { return true }
func (LSTool) ConcurrencySafe(json.RawMessage) bool           { return true }
func (NotebookReadTool) ConcurrencySafe(json.RawMessage) bool { return true }
func (TodoReadTool) ConcurrencySafe(json.RawMessage) bool     { return true }
func (GitStatusTool) ConcurrencySafe(json.RawMessage) bool    { return true }
func (GitDiffTool) ConcurrencySafe(json.RawMessage) bool      { return true }
func (GitLogTool) ConcurrencySafe(json.RawMessage) bool       { return true }
func (GitShowTool) ConcurrencySafe(json.RawMessage) bool      { return true }
func (GitBlameTool) ConcurrencySafe(json.RawMessage) bool     { return true }
func (WebFetchTool) ConcurrencySafe(json.RawMessage) bool     { return true }
func (WebSearchTool) ConcurrencySafe(json.RawMessage) bool    { return true }
