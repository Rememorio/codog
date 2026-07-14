package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	// Register image decoders for read_image and notebook image outputs.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/approval"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/hookenv"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/policyengine"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/shellstate"
	"github.com/Rememorio/codog/internal/toolnames"
	"github.com/Rememorio/codog/internal/undo"
)

// GetMCPPromptTool renders one prompt exposed by a configured MCP server.
type GetMCPPromptTool struct {
	Servers map[string]config.MCPServerConfig
}

func (t GetMCPPromptTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "get_mcp_prompt",
		Description: "Read a prompt exposed by a configured MCP server.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"server":    map[string]any{"type": "string"},
				"prompt":    map[string]any{"type": "string"},
				"arguments": map[string]any{"type": "object", "additionalProperties": true},
			},
			"required": []string{"server", "prompt"},
		},
	}
}

func (GetMCPPromptTool) Permission() Permission { return PermissionReadOnly }

func (t GetMCPPromptTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Server    string          `json:"server"`
		Prompt    string          `json:"prompt"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Server) == "" {
		return "", errors.New("server is required")
	}
	if strings.TrimSpace(payload.Prompt) == "" {
		return "", errors.New("prompt is required")
	}
	server, ok := t.Servers[payload.Server]
	if !ok {
		return "", unknownMCPServerError(payload.Server, t.Servers)
	}
	result := mcp.GetPrompt(ctx, payload.Server, server, payload.Prompt, payload.Arguments)
	if result.Error != "" {
		return "", errors.New(result.Error)
	}
	return pretty(result), nil
}

type PolicyEvaluateTool struct{}

func (PolicyEvaluateTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "policy_evaluate",
		Description: "Evaluate Codog automation policy for a lane context and return structured next actions.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"lane_id":                  map[string]any{"type": "string"},
				"green_level":              map[string]any{"type": "integer", "minimum": 0},
				"green_contract_satisfied": map[string]any{"type": "boolean"},
				"branch_status":            map[string]any{"type": "string"},
				"branch_behind":            map[string]any{"type": "integer", "minimum": 0},
				"verification_blocked":     map[string]any{"type": "boolean"},
				"blocker":                  map[string]any{"type": "string"},
				"review_status":            map[string]any{"type": "string"},
				"diff_scope":               map[string]any{"type": "string"},
				"requested_action":         map[string]any{"type": "string"},
				"repository":               map[string]any{"type": "string"},
				"branch":                   map[string]any{"type": "string"},
				"actor":                    map[string]any{"type": "string"},
				"actor_scope":              map[string]any{"type": "string"},
				"policy_source":            map[string]any{"type": "string"},
				"policy_block_reason":      map[string]any{"type": "string"},
				"completed":                map[string]any{"type": "boolean"},
				"retry_count":              map[string]any{"type": "integer", "minimum": 0},
				"retry_limit":              map[string]any{"type": "integer", "minimum": 0},
			},
		},
	}
}

func (PolicyEvaluateTool) Permission() Permission { return PermissionReadOnly }

func (PolicyEvaluateTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var ctx policyengine.LaneContext
	if len(input) != 0 {
		if err := json.Unmarshal(input, &ctx); err != nil {
			return "", err
		}
	}
	evaluation := policyengine.DefaultEngine().Evaluate(ctx)
	return pretty(evaluation), nil
}

type ApprovalTokenTool struct {
	ConfigHome string
}

var approvalTokenActionNames = []string{"grant", "pending", "approve", "verify", "consume", "revoke", "list"}

func (ApprovalTokenTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "approval_token",
		Description: "Create, verify, consume, revoke, or list auditable local policy-exception approval tokens.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": append([]string(nil), approvalTokenActionNames...)},
				"token":  map[string]any{"type": "string"},
				"scope": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"policy":     map[string]any{"type": "string"},
						"action":     map[string]any{"type": "string"},
						"repository": map[string]any{"type": "string"},
						"branch":     map[string]any{"type": "string"},
						"commit":     map[string]any{"type": "string"},
					},
				},
				"approving_actor":   map[string]any{"type": "string"},
				"requesting_actor":  map[string]any{"type": "string"},
				"approved_executor": map[string]any{"type": "string"},
				"executing_actor":   map[string]any{"type": "string"},
				"expires_at":        map[string]any{"type": "string", "description": "RFC3339 timestamp."},
				"ttl_seconds":       map[string]any{"type": "integer", "minimum": 1},
				"max_uses":          map[string]any{"type": "integer", "minimum": 1},
				"delegation_chain": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"actor":      map[string]any{"type": "string"},
							"session_id": map[string]any{"type": "string"},
							"reason":     map[string]any{"type": "string"},
						},
					},
				},
			},
			"required": []string{"action"},
		},
	}
}

func (ApprovalTokenTool) Permission() Permission { return PermissionReadOnly }

func (t ApprovalTokenTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Action           string                   `json:"action"`
		Token            string                   `json:"token,omitempty"`
		Scope            approval.Scope           `json:"scope,omitempty"`
		ApprovingActor   string                   `json:"approving_actor,omitempty"`
		RequestingActor  string                   `json:"requesting_actor,omitempty"`
		ApprovedExecutor string                   `json:"approved_executor,omitempty"`
		ExecutingActor   string                   `json:"executing_actor,omitempty"`
		ExpiresAt        string                   `json:"expires_at,omitempty"`
		TTLSeconds       int                      `json:"ttl_seconds,omitempty"`
		MaxUses          int                      `json:"max_uses,omitempty"`
		DelegationChain  []approval.DelegationHop `json:"delegation_chain,omitempty"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	action := strings.TrimSpace(strings.ToLower(payload.Action))
	store := approval.NewStore(t.ConfigHome)
	now := time.Now().UTC()
	switch action {
	case "grant", "pending":
		expiresAt, err := approvalExpiry(payload.ExpiresAt, payload.TTLSeconds, now)
		if err != nil {
			return "", err
		}
		status := approval.StatusGranted
		if action == "pending" {
			status = approval.StatusPending
		}
		grant, err := store.Grant(approval.GrantOptions{
			Token:            payload.Token,
			Scope:            payload.Scope,
			ApprovingActor:   payload.ApprovingActor,
			RequestingActor:  payload.RequestingActor,
			ApprovedExecutor: payload.ApprovedExecutor,
			Status:           status,
			ExpiresAt:        expiresAt,
			MaxUses:          payload.MaxUses,
			DelegationChain:  payload.DelegationChain,
			Now:              now,
		})
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"kind": "approval_token", "action": action, "status": "ok", "grant": grant}), nil
	case "approve":
		expiresAt, err := approvalExpiry(payload.ExpiresAt, payload.TTLSeconds, now)
		if err != nil {
			return "", err
		}
		grant, err := store.Approve(payload.Token, approval.GrantOptions{
			Scope:            payload.Scope,
			ApprovingActor:   payload.ApprovingActor,
			RequestingActor:  payload.RequestingActor,
			ApprovedExecutor: payload.ApprovedExecutor,
			ExpiresAt:        expiresAt,
			MaxUses:          payload.MaxUses,
			DelegationChain:  payload.DelegationChain,
			Now:              now,
		})
		if err != nil {
			return approvalTokenDeniedReport(action, err), nil
		}
		return pretty(map[string]any{"kind": "approval_token", "action": action, "status": "ok", "grant": grant}), nil
	case "verify":
		audit, err := store.Verify(payload.Token, payload.Scope, payload.ExecutingActor, now)
		if err != nil {
			return approvalTokenDeniedReport(action, err), nil
		}
		return pretty(map[string]any{"kind": "approval_token", "action": action, "status": "ok", "audit": audit}), nil
	case "consume":
		audit, err := store.Consume(payload.Token, payload.Scope, payload.ExecutingActor, now)
		if err != nil {
			return approvalTokenDeniedReport(action, err), nil
		}
		return pretty(map[string]any{"kind": "approval_token", "action": action, "status": "ok", "audit": audit}), nil
	case "revoke":
		audit, err := store.Revoke(payload.Token, now)
		if err != nil {
			return approvalTokenDeniedReport(action, err), nil
		}
		return pretty(map[string]any{"kind": "approval_token", "action": action, "status": "ok", "audit": audit}), nil
	case "list":
		ledger, err := store.List()
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"kind": "approval_token", "action": action, "status": "ok", "ledger": ledger}), nil
	default:
		return "", unknownApprovalTokenActionError(payload.Action)
	}
}

func unknownApprovalTokenActionError(action string) error {
	suggestions := toolnames.Suggestions(action, approvalTokenActionNames, 4)
	switch len(suggestions) {
	case 0:
		return fmt.Errorf("unknown approval_token action %q", action)
	case 1:
		return fmt.Errorf("unknown approval_token action %q; did you mean %q?", action, suggestions[0])
	default:
		return fmt.Errorf("unknown approval_token action %q; suggestions: %s", action, strings.Join(suggestions, ", "))
	}
}

func approvalExpiry(expiresAt string, ttlSeconds int, now time.Time) (*time.Time, error) {
	expiresAt = strings.TrimSpace(expiresAt)
	if expiresAt != "" && ttlSeconds > 0 {
		return nil, errors.New("approval_token cannot set both expires_at and ttl_seconds")
	}
	if expiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			return nil, err
		}
		parsed = parsed.UTC()
		return &parsed, nil
	}
	if ttlSeconds > 0 {
		value := now.Add(time.Duration(ttlSeconds) * time.Second).UTC()
		return &value, nil
	}
	return nil, nil
}

func approvalTokenDeniedReport(action string, err error) string {
	var approvalErr approval.Error
	if errors.As(err, &approvalErr) {
		return pretty(map[string]any{
			"kind":       "approval_token",
			"action":     action,
			"status":     "denied",
			"error_kind": approvalErr.Kind,
			"error":      approvalErr,
		})
	}
	return pretty(map[string]any{
		"kind":       "approval_token",
		"action":     action,
		"status":     "error",
		"error_kind": "approval_error",
		"error":      err.Error(),
	})
}

func sortedMCPServerNames(servers map[string]config.MCPServerConfig) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func unknownMCPServerError(name string, servers map[string]config.MCPServerConfig) error {
	suggestions := toolnames.Suggestions(name, sortedMCPServerNames(servers), 4)
	switch len(suggestions) {
	case 0:
		return fmt.Errorf("unknown MCP server %q", name)
	case 1:
		return fmt.Errorf("unknown MCP server %q; did you mean %q?", name, suggestions[0])
	default:
		return fmt.Errorf("unknown MCP server %q; suggestions: %s", name, strings.Join(suggestions, ", "))
	}
}

func unknownToolActionError(tool string, action string, candidates []string) error {
	suggestions := toolnames.Suggestions(action, candidates, 4)
	switch len(suggestions) {
	case 0:
		return fmt.Errorf("unknown %s action %q", tool, action)
	case 1:
		return fmt.Errorf("unknown %s action %q; did you mean %q?", tool, action, suggestions[0])
	default:
		return fmt.Errorf("unknown %s action %q; suggestions: %s", tool, action, strings.Join(suggestions, ", "))
	}
}

func suggestedValueError(prefix string, value string, candidates []string) error {
	suggestions := toolnames.Suggestions(value, candidates, 4)
	switch len(suggestions) {
	case 0:
		return fmt.Errorf("%s %q", prefix, value)
	case 1:
		return fmt.Errorf("%s %q; did you mean %q?", prefix, value, suggestions[0])
	default:
		return fmt.Errorf("%s %q; suggestions: %s", prefix, value, strings.Join(suggestions, ", "))
	}
}

type GitStatusTool struct {
	Workspace string
}

func (GitStatusTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "git_status",
		Description: "Show working tree status with structured JSON output.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"short": map[string]any{"type": "boolean", "description": "Use --short --branch output. Defaults to true."},
			},
		},
	}
}

func (GitStatusTool) Permission() Permission { return PermissionReadOnly }

func (t GitStatusTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Short *bool `json:"short,omitempty"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	args := []string{"status"}
	if payload.Short == nil || *payload.Short {
		args = append(args, "--short", "--branch")
	}
	output, err := gitops.Run(t.Workspace, args...)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{"output": output}), nil
}

type BranchFreshnessTool struct {
	Workspace string
}

func (BranchFreshnessTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "branch_freshness",
		Description: "Compare a branch against a base branch and emit a stale-branch guard event when broad verification should wait.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"branch": map[string]any{"type": "string"},
				"base":   map[string]any{"type": "string"},
			},
		},
	}
}

func (BranchFreshnessTool) Permission() Permission { return PermissionReadOnly }

func (t BranchFreshnessTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Branch string `json:"branch"`
		Base   string `json:"base"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	freshness, err := gitops.CheckBranchFreshness(t.Workspace, payload.Branch, payload.Base)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{"kind": "branch_freshness", "freshness": freshness}), nil
}

type GitDiffTool struct {
	Workspace string
}

func (GitDiffTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "git_diff",
		Description: "Show git diff output for the working tree, index, commits, and optional path filters.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"staged":  map[string]any{"type": "boolean"},
				"commit":  map[string]any{"type": "string"},
				"commit2": map[string]any{"type": "string"},
			},
		},
	}
}

func (GitDiffTool) Permission() Permission { return PermissionReadOnly }

func (t GitDiffTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path    string `json:"path,omitempty"`
		Staged  bool   `json:"staged,omitempty"`
		Commit  string `json:"commit,omitempty"`
		Commit2 string `json:"commit2,omitempty"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	args := []string{"diff"}
	if payload.Staged {
		args = append(args, "--cached")
	}
	if strings.TrimSpace(payload.Commit) != "" {
		commit, err := safeGitRef(payload.Commit, "commit")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(payload.Commit2) != "" {
			commit2, err := safeGitRef(payload.Commit2, "commit2")
			if err != nil {
				return "", err
			}
			args = append(args, commit+"..."+commit2)
		} else {
			args = append(args, commit)
		}
	}
	if strings.TrimSpace(payload.Path) != "" {
		path, err := gitPathArg(t.Workspace, payload.Path, true)
		if err != nil {
			return "", err
		}
		args = append(args, "--", path)
	}
	output, err := gitops.Run(t.Workspace, args...)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{"output": output}), nil
}

type GitLogTool struct {
	Workspace string
}

func (GitLogTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "git_log",
		Description: "Show commit history with optional count, author, date, and path filters.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"count":   map[string]any{"type": "integer", "minimum": 1},
				"oneline": map[string]any{"type": "boolean"},
				"author":  map[string]any{"type": "string"},
				"since":   map[string]any{"type": "string"},
				"until":   map[string]any{"type": "string"},
			},
		},
	}
}

func (GitLogTool) Permission() Permission { return PermissionReadOnly }

func (t GitLogTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path    string `json:"path,omitempty"`
		Count   int    `json:"count,omitempty"`
		Oneline bool   `json:"oneline,omitempty"`
		Author  string `json:"author,omitempty"`
		Since   string `json:"since,omitempty"`
		Until   string `json:"until,omitempty"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	count := payload.Count
	if count <= 0 {
		count = 20
	}
	args := []string{"log", fmt.Sprintf("-n%d", count)}
	if payload.Oneline {
		args = append(args, "--oneline")
	}
	if strings.TrimSpace(payload.Author) != "" {
		args = append(args, "--author="+payload.Author)
	}
	if strings.TrimSpace(payload.Since) != "" {
		args = append(args, "--since="+payload.Since)
	}
	if strings.TrimSpace(payload.Until) != "" {
		args = append(args, "--until="+payload.Until)
	}
	if strings.TrimSpace(payload.Path) != "" {
		path, err := gitPathArg(t.Workspace, payload.Path, true)
		if err != nil {
			return "", err
		}
		args = append(args, "--", path)
	}
	output, err := gitops.Run(t.Workspace, args...)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{"output": output}), nil
}

type GitShowTool struct {
	Workspace string
}

var gitShowFormatNames = []string{"patch", "stat", "metadata"}

func (GitShowTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "git_show",
		Description: "Show a commit, tag, or tree object in patch, stat, or metadata format.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"commit": map[string]any{"type": "string"},
				"path":   map[string]any{"type": "string"},
				"stat":   map[string]any{"type": "boolean"},
				"format": map[string]any{"type": "string", "enum": append([]string(nil), gitShowFormatNames...)},
			},
			"required": []string{"commit"},
		},
	}
}

func (GitShowTool) Permission() Permission { return PermissionReadOnly }

func (t GitShowTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Commit string `json:"commit"`
		Path   string `json:"path,omitempty"`
		Stat   bool   `json:"stat,omitempty"`
		Format string `json:"format,omitempty"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	commit, err := safeGitRef(payload.Commit, "commit")
	if err != nil {
		return "", err
	}
	args := []string{"show"}
	switch strings.ToLower(strings.TrimSpace(payload.Format)) {
	case "metadata":
		if strings.TrimSpace(payload.Path) != "" {
			return "", errors.New(`git_show format "metadata" cannot be combined with path`)
		}
		args = append(args, "--format=medium", "--no-patch")
	case "stat":
		args = append(args, "--stat")
	case "", "patch":
		if payload.Format == "" && payload.Stat {
			args = append(args, "--stat")
		}
	default:
		return "", suggestedValueError("unknown git_show format", payload.Format, gitShowFormatNames)
	}
	if strings.TrimSpace(payload.Path) != "" {
		path, err := gitPathArg(t.Workspace, payload.Path, true)
		if err != nil {
			return "", err
		}
		args = append(args, commit+":"+path)
	} else {
		args = append(args, commit)
	}
	output, err := gitops.Run(t.Workspace, args...)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{"output": output}), nil
}

type GitBlameTool struct {
	Workspace string
}

func (GitBlameTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "git_blame",
		Description: "Show revision and author information for each line of a file.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"path":       map[string]any{"type": "string"},
				"start_line": map[string]any{"type": "integer", "minimum": 1},
				"end_line":   map[string]any{"type": "integer", "minimum": 1},
			},
			"required": []string{"path"},
		},
	}
}

func (GitBlameTool) Permission() Permission { return PermissionReadOnly }

func (t GitBlameTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line,omitempty"`
		EndLine   int    `json:"end_line,omitempty"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	path, err := gitPathArg(t.Workspace, payload.Path, false)
	if err != nil {
		return "", err
	}
	args := []string{"blame"}
	if payload.StartLine > 0 && payload.EndLine > 0 {
		if payload.EndLine < payload.StartLine {
			return "", errors.New("end_line must be greater than or equal to start_line")
		}
		args = append(args, fmt.Sprintf("-L%d,%d", payload.StartLine, payload.EndLine))
	}
	args = append(args, "--", path)
	output, err := gitops.Run(t.Workspace, args...)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{"output": output}), nil
}

func safeGitRef(value, field string) (string, error) {
	ref := strings.TrimSpace(value)
	if ref == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if strings.HasPrefix(ref, "-") || strings.ContainsRune(ref, '\x00') {
		return "", fmt.Errorf("%s is not a safe git ref", field)
	}
	return ref, nil
}

func gitPathArg(workspace, requested string, allowMissing bool) (string, error) {
	path, err := safePath(workspace, requested, allowMissing)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes workspace scope: %s", requested)
	}
	return filepath.ToSlash(rel), nil
}

type BashTool struct {
	Workspace       string
	ConfigHome      string
	ConfigEnv       map[string]string
	DefaultShell    string
	PowerShell      string
	SandboxStrategy string
	Sandbox         config.SandboxConfig
}

func (BashTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "bash",
		Description: "Execute a shell command in the current workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":           map[string]any{"type": "string"},
				"timeout":           map[string]any{"type": "integer", "minimum": 1},
				"timeout_ms":        map[string]any{"type": "integer", "minimum": 1},
				"description":       map[string]any{"type": "string"},
				"run_in_background": map[string]any{"type": "boolean"},
				"namespaceRestrictions": map[string]any{
					"type":        "boolean",
					"description": "Request namespace restrictions for this bash invocation.",
				},
				"namespace_restrictions": map[string]any{
					"type":        "boolean",
					"description": "Snake-case alias for namespaceRestrictions.",
				},
				"isolateNetwork": map[string]any{
					"type":        "boolean",
					"description": "Request network isolation for this bash invocation.",
				},
				"isolate_network": map[string]any{
					"type":        "boolean",
					"description": "Snake-case alias for isolateNetwork.",
				},
				"filesystemMode": map[string]any{
					"type":        "string",
					"enum":        []string{"off", "workspace-only", "allow-list"},
					"description": "Filesystem isolation mode for this bash invocation.",
				},
				"filesystem_mode": map[string]any{
					"type":        "string",
					"enum":        []string{"off", "workspace-only", "allow-list"},
					"description": "Snake-case alias for filesystemMode.",
				},
				"allowedMounts": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Extra paths to mount when filesystemMode is allow-list.",
				},
				"allowed_mounts": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Snake-case alias for allowedMounts.",
				},
				"dangerouslyDisableSandbox": map[string]any{
					"type":        "boolean",
					"description": "Claude-compatible per-call sandbox bypass.",
				},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	}
}

func (BashTool) Permission() Permission { return PermissionDanger }

func toolEnvironment(ctx context.Context, configHome string, configEnv map[string]string) ([]string, error) {
	env := toolEnvironmentFromConfig(configEnv, nil)
	hookEnv, err := hookenv.Load(configHome, SessionIDFromContext(ctx))
	if err != nil {
		return nil, err
	}
	return hookenv.Merge(env, hookEnv), nil
}

func toolEnvironmentFromConfig(configEnv map[string]string, base []string) []string {
	if base == nil {
		base = os.Environ()
	}
	if len(configEnv) == 0 {
		return append([]string(nil), base...)
	}
	keys := make([]string, 0, len(configEnv))
	for rawKey := range configEnv {
		key := strings.TrimSpace(rawKey)
		if key == "" || strings.Contains(key, "=") {
			continue
		}
		keys = append(keys, rawKey)
	}
	sort.Strings(keys)
	overlay := make([]string, 0, len(keys))
	for _, rawKey := range keys {
		key := strings.TrimSpace(rawKey)
		overlay = append(overlay, key+"="+configEnv[rawKey])
	}
	return hookenv.Merge(base, overlay)
}

func toolCWD(ctx context.Context, configHome string, workspace string) (string, error) {
	return shellstate.CurrentCWD(configHome, SessionIDFromContext(ctx), workspace)
}

func wrapCommandWithCWDProbe(command string, cwdFile string) string {
	cwdFile = strings.TrimSpace(cwdFile)
	if cwdFile == "" {
		return command
	}
	return command + "\n__codog_status=$?\npwd -P > " + shellQuoteToolArg(cwdFile) + "\nexit $__codog_status"
}

func createCWDProbe(configHome string, sessionID string) (string, string, error) {
	stateDir := shellstate.Dir(configHome, sessionID)
	if stateDir == "" {
		return "", "", nil
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", "", err
	}
	probeDir, err := os.MkdirTemp(stateDir, ".bash-cwd-")
	if err != nil {
		return "", "", err
	}
	probePath := filepath.Join(probeDir, "cwd")
	if err := os.WriteFile(probePath, nil, 0o600); err != nil {
		_ = os.RemoveAll(probeDir)
		return "", "", err
	}
	return probePath, probeDir, nil
}

const maxBashOutputBytes = 16 * 1024

type persistedBashOutput struct {
	Kind            string   `json:"kind"`
	Command         string   `json:"command"`
	CWD             string   `json:"cwd"`
	Stdout          string   `json:"stdout"`
	Stderr          string   `json:"stderr"`
	ExitCode        int      `json:"exit_code"`
	DurationMS      int64    `json:"duration_ms"`
	TruncatedFields []string `json:"truncated_fields"`
	CreatedAt       string   `json:"created_at"`
}

func truncateBashOutput(value string) (string, bool) {
	if len(value) <= maxBashOutputBytes {
		return value, false
	}
	end := maxBashOutputBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	if end == 0 {
		return "[output truncated - exceeded 16384 bytes]", true
	}
	return value[:end] + "\n\n[output truncated - exceeded 16384 bytes]", true
}

func bashNoOutputExpected(stdout, stderr string) bool {
	return strings.TrimSpace(stdout) == "" && strings.TrimSpace(stderr) == ""
}

func bashReturnCodeInterpretation(exitCode int, interrupted bool, command string) string {
	if interrupted {
		if isBashTestCommand(command) {
			return "test.hung"
		}
		return "timeout"
	}
	if exitCode == 0 {
		return ""
	}
	return fmt.Sprintf("exit_code:%d", exitCode)
}

func isBashTestCommand(command string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(command), " "))
	for _, needle := range []string{"cargo test", "cargo nextest", "npm test", "npm run test", "pnpm test", "yarn test", "bun test", "deno test", "vitest", "pytest", "go test"} {
		if strings.Contains(normalized, needle) {
			return true
		}
	}
	return false
}

func bashTimeoutStructuredContent(command string, timeoutMS int, interpretation string) []map[string]any {
	failureClass := "timeout"
	event := "command.timeout"
	if interpretation == "test.hung" {
		failureClass = "test_hang"
		event = "test.hung"
	}
	return []map[string]any{{
		"event":        event,
		"failureClass": failureClass,
		"data": map[string]any{
			"command":        command,
			"timeoutMs":      timeoutMS,
			"provenance":     "bash.timeout",
			"classification": interpretation,
		},
	}}
}

func bashOutputContractFields(dangerouslyDisable bool) map[string]any {
	return map[string]any{
		"rawOutputPath":             nil,
		"interrupted":               false,
		"isImage":                   nil,
		"backgroundTaskId":          nil,
		"backgroundedByUser":        nil,
		"assistantAutoBackgrounded": nil,
		"dangerouslyDisableSandbox": dangerouslyDisable,
		"returnCodeInterpretation":  nil,
		"noOutputExpected":          nil,
		"structuredContent":         nil,
		"persistedOutputPath":       nil,
		"persistedOutputSize":       nil,
	}
}

func persistBashOutput(configHome, command, cwd, stdout, stderr string, exitCode int, durationMS int64, truncatedFields []string) (string, int64, error) {
	configHome = strings.TrimSpace(configHome)
	if configHome == "" || len(truncatedFields) == 0 {
		return "", 0, nil
	}
	dir := filepath.Join(configHome, "bash-output")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	file, err := os.CreateTemp(dir, "bash-*.json")
	if err != nil {
		return "", 0, err
	}
	path := file.Name()
	payload := persistedBashOutput{
		Kind:            "bash_output",
		Command:         command,
		CWD:             cwd,
		Stdout:          stdout,
		Stderr:          stderr,
		ExitCode:        exitCode,
		DurationMS:      durationMS,
		TruncatedFields: append([]string(nil), truncatedFields...),
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(payload); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", 0, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	return path, info.Size(), nil
}

func bashSandboxStrategy(strategy string, cfg config.SandboxConfig, dangerouslyDisable bool) string {
	if dangerouslyDisable {
		return "off"
	}
	strategy = strings.TrimSpace(strategy)
	if strategy == "" && cfg.Enabled != nil && *cfg.Enabled {
		return "detect"
	}
	return strategy
}

func bashSandboxRequestOptions(cfg config.SandboxConfig, strategy string, dangerouslyDisable bool, namespaceRestrictions, namespaceRestrictionsAlt, isolateNetwork, isolateNetworkAlt *bool, filesystemMode, filesystemModeAlt string, allowedMounts, allowedMountsAlt []string) (sandbox.SandboxRequestOptions, error) {
	opts, err := sandboxRequestOptionsFromConfig(cfg)
	if err != nil {
		return opts, err
	}
	if dangerouslyDisable {
		disabled := false
		opts.Enabled = &disabled
	}
	if value := firstBoolPointer(namespaceRestrictions, namespaceRestrictionsAlt); value != nil {
		opts.NamespaceRestrictions = value
	}
	if value := firstBoolPointer(isolateNetwork, isolateNetworkAlt); value != nil {
		opts.NetworkIsolation = value
	}
	mode, err := sandbox.ParseFilesystemIsolationMode(firstNonEmpty(filesystemMode, filesystemModeAlt))
	if err != nil {
		return opts, err
	}
	if mode != "" {
		opts.FilesystemMode = mode
	}
	if allowedMounts != nil {
		opts.AllowedMounts = append([]string(nil), allowedMounts...)
	} else if allowedMountsAlt != nil {
		opts.AllowedMounts = append([]string(nil), allowedMountsAlt...)
	}
	if !sandboxStrategyRequestsStatus(strategy) && !dangerouslyDisable {
		disabled := false
		opts.Enabled = &disabled
	}
	return opts, nil
}

func sandboxRequestOptionsFromConfig(cfg config.SandboxConfig) (sandbox.SandboxRequestOptions, error) {
	opts := sandbox.SandboxRequestOptions{
		Enabled:               cloneBoolPointer(cfg.Enabled),
		NamespaceRestrictions: cloneBoolPointer(cfg.NamespaceRestrictions),
		NetworkIsolation:      cloneBoolPointer(cfg.NetworkIsolation),
		AllowedMounts:         append([]string(nil), cfg.AllowedMounts...),
	}
	mode, err := sandbox.ParseFilesystemIsolationMode(cfg.FilesystemMode)
	if err != nil {
		return opts, err
	}
	opts.FilesystemMode = mode
	return opts, nil
}

func sandboxStrategyRequestsStatus(strategy string) bool {
	switch strings.TrimSpace(strategy) {
	case "", "off", "none":
		return false
	default:
		return true
	}
}

func firstBoolPointer(values ...*bool) *bool {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (t BashTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Command                   string   `json:"command"`
		Timeout                   int      `json:"timeout"`
		TimeoutMS                 int      `json:"timeout_ms"`
		RunInBackground           bool     `json:"run_in_background"`
		DangerouslyDisableSandbox bool     `json:"dangerouslyDisableSandbox"`
		NamespaceRestrictions     *bool    `json:"namespaceRestrictions"`
		NamespaceRestrictionsAlt  *bool    `json:"namespace_restrictions"`
		IsolateNetwork            *bool    `json:"isolateNetwork"`
		IsolateNetworkAlt         *bool    `json:"isolate_network"`
		FilesystemMode            string   `json:"filesystemMode"`
		FilesystemModeAlt         string   `json:"filesystem_mode"`
		AllowedMounts             []string `json:"allowedMounts"`
		AllowedMountsAlt          []string `json:"allowed_mounts"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Command) == "" {
		return "", errors.New("command is required")
	}
	if effectiveDefaultShell(t.DefaultShell) == "powershell" {
		return (PowerShellTool{
			Workspace:  t.Workspace,
			ConfigHome: t.ConfigHome,
			ConfigEnv:  t.ConfigEnv,
			Executable: t.PowerShell,
		}).Execute(ctx, input)
	}
	cwd, err := toolCWD(ctx, t.ConfigHome, t.Workspace)
	if err != nil {
		return "", err
	}
	commandText := payload.Command
	cwdProbePath := ""
	cwdProbeDir := ""
	if SessionIDFromContext(ctx) != "" && strings.TrimSpace(t.ConfigHome) != "" && !payload.RunInBackground {
		cwdProbePath, cwdProbeDir, err = createCWDProbe(t.ConfigHome, SessionIDFromContext(ctx))
		if err != nil {
			return "", err
		}
		defer func() { _ = os.RemoveAll(cwdProbeDir) }()
		commandText = wrapCommandWithCWDProbe(commandText, cwdProbePath)
	}
	strategy := bashSandboxStrategy(t.SandboxStrategy, t.Sandbox, payload.DangerouslyDisableSandbox)
	requestOptions, err := bashSandboxRequestOptions(t.Sandbox, strategy, payload.DangerouslyDisableSandbox, payload.NamespaceRestrictions, payload.NamespaceRestrictionsAlt, payload.IsolateNetwork, payload.IsolateNetworkAlt, payload.FilesystemMode, payload.FilesystemModeAlt, payload.AllowedMounts, payload.AllowedMountsAlt)
	if err != nil {
		return "", err
	}
	if cwdProbeDir != "" {
		requestOptions.InternalWritablePaths = append(requestOptions.InternalWritablePaths, cwdProbeDir)
	}
	command, args, effectiveSandbox, sandboxStatus, err := sandbox.ShellCommandWithSandboxStatus(strategy, t.Workspace, commandText, requestOptions)
	if err != nil {
		return "", err
	}
	if payload.RunInBackground {
		env, err := toolEnvironment(ctx, t.ConfigHome, t.ConfigEnv)
		if err != nil {
			return "", err
		}
		task, err := taskStore(t.ConfigHome, t.Workspace).RunWithOptions(shellCommandLine(command, args), cwd, background.RunOptions{Kind: "bash", Env: env})
		if err != nil {
			return "", err
		}
		result := bashOutputContractFields(payload.DangerouslyDisableSandbox)
		result["background"] = true
		result["task"] = task
		result["backgroundTaskId"] = task.ID
		result["backgroundedByUser"] = false
		result["assistantAutoBackgrounded"] = false
		result["noOutputExpected"] = true
		result["sandboxStatus"] = sandboxStatus
		if effectiveSandbox != "" {
			result["sandbox"] = effectiveSandbox
		}
		return pretty(result), nil
	}
	timeoutMS := payload.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = payload.Timeout
	}
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 2 * time.Minute
		timeoutMS = int(timeout / time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = cwd
	env, err := toolEnvironment(ctx, t.ConfigHome, t.ConfigEnv)
	if err != nil {
		return "", err
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	stdoutRaw := stdout.String()
	stderrRaw := stderr.String()
	stdoutText, stdoutTruncated := truncateBashOutput(stdoutRaw)
	stderrText, stderrTruncated := truncateBashOutput(stderrRaw)
	finalCWD := cwd
	cwdChanged := false
	if cwdProbePath != "" {
		if data, readErr := os.ReadFile(cwdProbePath); readErr == nil {
			if saved, saveErr := shellstate.SaveCWD(t.ConfigHome, SessionIDFromContext(ctx), strings.TrimSpace(string(data))); saveErr == nil && saved != "" {
				finalCWD = saved
				cwdChanged = finalCWD != cwd
			}
		}
	}
	exit := exitCode(err)
	durationMS := time.Since(started).Milliseconds()
	truncatedFields := []string{}
	if stdoutTruncated {
		truncatedFields = append(truncatedFields, "stdout")
	}
	if stderrTruncated {
		truncatedFields = append(truncatedFields, "stderr")
	}
	persistedOutputPath, persistedOutputSize, persistErr := persistBashOutput(t.ConfigHome, payload.Command, finalCWD, stdoutRaw, stderrRaw, exit, durationMS, truncatedFields)
	if persistErr != nil {
		return "", persistErr
	}
	result := bashOutputContractFields(payload.DangerouslyDisableSandbox)
	result["stdout"] = stdoutText
	result["stderr"] = stderrText
	result["exit_code"] = exit
	result["duration_ms"] = durationMS
	result["cwd"] = finalCWD
	result["noOutputExpected"] = bashNoOutputExpected(stdoutText, stderrText)
	if persistedOutputPath != "" {
		result["persistedOutputPath"] = persistedOutputPath
		result["persistedOutputSize"] = persistedOutputSize
	}
	if interpretation := bashReturnCodeInterpretation(exit, false, payload.Command); interpretation != "" {
		result["returnCodeInterpretation"] = interpretation
	}
	if cwdChanged {
		result["old_cwd"] = cwd
		result["cwd_changed"] = true
	}
	result["sandboxStatus"] = sandboxStatus
	if effectiveSandbox != "" {
		result["sandbox"] = effectiveSandbox
	}
	if ctx.Err() == context.DeadlineExceeded {
		interpretation := bashReturnCodeInterpretation(-1, true, payload.Command)
		result["stdout"] = ""
		result["stderr"] = fmt.Sprintf("Command exceeded timeout of %d ms", timeoutMS)
		result["interrupted"] = true
		result["error"] = "timeout"
		result["exit_code"] = -1
		result["returnCodeInterpretation"] = interpretation
		result["noOutputExpected"] = true
		result["structuredContent"] = bashTimeoutStructuredContent(payload.Command, timeoutMS, interpretation)
		return pretty(result), nil
	}
	if err != nil {
		result["error"] = err.Error()
	}
	return pretty(result), nil
}

type BashOutputTool struct {
	Workspace  string
	ConfigHome string
}

func (BashOutputTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "bash_output",
		Description: "Read recent output from a background bash task started by the bash tool.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"bash_id":     map[string]any{"type": "string"},
				"task_id":     map[string]any{"type": "string"},
				"id":          map[string]any{"type": "string"},
				"limit_bytes": map[string]any{"type": "integer", "minimum": 1},
				"limit":       map[string]any{"type": "integer", "minimum": 1},
				"offset":      map[string]any{"type": "integer", "minimum": 0},
				"block":       map[string]any{"type": "boolean"},
				"timeout":     map[string]any{"type": "integer", "minimum": 0},
				"timeout_ms":  map[string]any{"type": "integer", "minimum": 0},
			},
			"additionalProperties": false,
		},
	}
}

func (BashOutputTool) Permission() Permission { return PermissionReadOnly }

func (t BashOutputTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		BashID     string `json:"bash_id"`
		TaskID     string `json:"task_id"`
		ID         string `json:"id"`
		LimitBytes int64  `json:"limit_bytes"`
		Limit      int64  `json:"limit"`
		Offset     *int64 `json:"offset"`
		Block      bool   `json:"block"`
		Timeout    int    `json:"timeout"`
		TimeoutMS  int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	id, err := bashTaskID(payload.BashID, payload.TaskID, payload.ID)
	if err != nil {
		return "", err
	}
	limitBytes := payload.LimitBytes
	if limitBytes <= 0 {
		limitBytes = payload.Limit
	}
	if limitBytes <= 0 {
		limitBytes = 64 * 1024
	}
	store := taskStore(t.ConfigHome, t.Workspace)
	task, err := store.Status(id)
	if err != nil {
		return "", err
	}
	if err := requireBashTask(task); err != nil {
		return "", err
	}
	logRead, task, err := readBackgroundLog(store, id, task, backgroundLogReadOptions{
		LimitBytes: limitBytes,
		Offset:     payload.Offset,
		Block:      payload.Block,
		TimeoutMS:  firstPositiveInt(payload.TimeoutMS, payload.Timeout),
	})
	if err != nil {
		return "", err
	}
	output := logRead.Output
	outputText, outputTruncated := truncateBashOutput(output)
	result := bashOutputContractFields(false)
	result["bash_id"] = id
	result["id"] = id
	result["backgroundTaskId"] = id
	result["status"] = task.Status
	result["output"] = outputText
	result["stdout"] = outputText
	result["stderr"] = ""
	result["task"] = task
	result["rawOutputPath"] = task.LogPath
	result["interrupted"] = task.Status == "stopped"
	result["noOutputExpected"] = bashNoOutputExpected(outputText, "")
	result["offset"] = logRead.Offset
	result["nextOffset"] = logRead.NextOffset
	result["bytesRead"] = logRead.BytesRead
	result["timedOut"] = logRead.TimedOut
	result["timeoutMs"] = logRead.TimeoutMS
	if task.ExitCode != nil {
		result["exit_code"] = *task.ExitCode
		if interpretation := bashReturnCodeInterpretation(*task.ExitCode, false, task.Command); interpretation != "" {
			result["returnCodeInterpretation"] = interpretation
		}
	}
	if info, statErr := os.Stat(task.LogPath); statErr == nil {
		if outputTruncated || int64(len([]byte(output))) < info.Size() {
			result["persistedOutputPath"] = task.LogPath
			result["persistedOutputSize"] = info.Size()
		}
	}
	return pretty(result), nil
}

type KillBashTool struct {
	Workspace  string
	ConfigHome string
}

func (KillBashTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "kill_bash",
		Description: "Stop a running background bash task by id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"bash_id": map[string]any{"type": "string"},
				"task_id": map[string]any{"type": "string"},
				"id":      map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}
}

func (KillBashTool) Permission() Permission { return PermissionWorkspace }

func (t KillBashTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		BashID string `json:"bash_id"`
		TaskID string `json:"task_id"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	id, err := bashTaskID(payload.BashID, payload.TaskID, payload.ID)
	if err != nil {
		return "", err
	}
	store := taskStore(t.ConfigHome, t.Workspace)
	task, err := store.Status(id)
	if err != nil {
		return "", err
	}
	if err := requireBashTask(task); err != nil {
		return "", err
	}
	task, err = store.Stop(id)
	if err != nil {
		return "", err
	}
	result := bashOutputContractFields(false)
	result["bash_id"] = id
	result["id"] = id
	result["backgroundTaskId"] = id
	result["status"] = task.Status
	result["task"] = task
	result["interrupted"] = true
	result["noOutputExpected"] = true
	return pretty(result), nil
}

func bashTaskID(values ...string) (string, error) {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value, nil
		}
	}
	return "", errors.New("bash_id is required")
}

func requireBashTask(task background.Task) error {
	if task.Kind != "bash" {
		return fmt.Errorf("task %s is not a bash task", task.ID)
	}
	return nil
}

type backgroundLogReadOptions struct {
	LimitBytes int64
	Offset     *int64
	Block      bool
	TimeoutMS  int
}

type backgroundLogReadResult struct {
	Output     string
	Offset     int64
	NextOffset int64
	BytesRead  int
	LogSize    int64
	Truncated  bool
	TimedOut   bool
	TimeoutMS  int
}

func readBackgroundLog(store background.Store, id string, task background.Task, options backgroundLogReadOptions) (backgroundLogReadResult, background.Task, error) {
	if options.LimitBytes <= 0 {
		options.LimitBytes = 64 * 1024
	}
	if options.Offset != nil && *options.Offset < 0 {
		return backgroundLogReadResult{}, task, errors.New("offset must be non-negative")
	}
	if options.Block && options.TimeoutMS <= 0 {
		options.TimeoutMS = 30_000
	}
	if options.TimeoutMS > 300_000 {
		return backgroundLogReadResult{}, task, errors.New("timeout must be 300000 ms or less")
	}

	deadline := time.Time{}
	if options.Block {
		deadline = time.Now().Add(time.Duration(options.TimeoutMS) * time.Millisecond)
	}
	for {
		result, err := readBackgroundLogOnce(store, id, task, options)
		if err != nil {
			return result, task, err
		}
		refreshed, statusErr := store.Status(id)
		if statusErr == nil {
			task = refreshed
		}
		result.TimeoutMS = options.TimeoutMS
		if !options.Block || result.Output != "" || !background.IsActiveStatus(task.Status) {
			return result, task, nil
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			result.TimedOut = true
			return result, task, nil
		}
		sleep := 50 * time.Millisecond
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				result.TimedOut = true
				return result, task, nil
			}
			if remaining < sleep {
				sleep = remaining
			}
		}
		time.Sleep(sleep)
	}
}

func readBackgroundLogOnce(store background.Store, id string, task background.Task, options backgroundLogReadOptions) (backgroundLogReadResult, error) {
	var output string
	var appliedOffset, nextOffset, logSize int64
	if info, statErr := os.Stat(task.LogPath); statErr == nil {
		logSize = info.Size()
	}
	if options.Offset != nil {
		var err error
		nextOffset, output, err = store.LogRange(id, *options.Offset, options.LimitBytes)
		if err != nil {
			return backgroundLogReadResult{}, err
		}
		appliedOffset = nextOffset - int64(len([]byte(output)))
	} else {
		var err error
		output, err = store.Logs(id, options.LimitBytes)
		if err != nil {
			return backgroundLogReadResult{}, err
		}
		nextOffset = logSize
		appliedOffset = max(nextOffset-int64(len([]byte(output))), int64(0))
	}
	return backgroundLogReadResult{
		Output:     output,
		Offset:     appliedOffset,
		NextOffset: nextOffset,
		BytesRead:  len([]byte(output)),
		LogSize:    logSize,
		Truncated:  output != "" && (appliedOffset > 0 || nextOffset < logSize),
	}, nil
}

func shellCommandLine(command string, args []string) string {
	parts := []string{shellQuote(command)}
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

type PowerShellTool struct {
	Workspace  string
	ConfigHome string
	ConfigEnv  map[string]string
	Executable string
}

func (PowerShellTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "powershell",
		Description: "Execute a PowerShell command in the current workspace, optionally as a background task.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":           map[string]any{"type": "string"},
				"timeout":           map[string]any{"type": "integer", "minimum": 1},
				"timeout_ms":        map[string]any{"type": "integer", "minimum": 1},
				"description":       map[string]any{"type": "string"},
				"run_in_background": map[string]any{"type": "boolean"},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	}
}

func (PowerShellTool) Permission() Permission { return PermissionDanger }

func (t PowerShellTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Command         string `json:"command"`
		Timeout         int    `json:"timeout"`
		TimeoutMS       int    `json:"timeout_ms"`
		RunInBackground bool   `json:"run_in_background"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Command) == "" {
		return "", errors.New("command is required")
	}
	executable, err := t.powerShellExecutable()
	if err != nil {
		return "", err
	}
	cwd, err := toolCWD(ctx, t.ConfigHome, t.Workspace)
	if err != nil {
		return "", err
	}
	if payload.RunInBackground {
		command := strings.Join([]string{shellQuoteToolArg(executable), "-NoProfile", "-NonInteractive", "-Command", shellQuoteToolArg(payload.Command)}, " ")
		env, err := toolEnvironment(ctx, t.ConfigHome, t.ConfigEnv)
		if err != nil {
			return "", err
		}
		task, err := taskStore(t.ConfigHome, t.Workspace).RunWithOptions(command, cwd, background.RunOptions{Kind: "powershell", Env: env})
		if err != nil {
			return "", err
		}
		result := bashOutputContractFields(false)
		result["background"] = true
		result["task"] = task
		result["backgroundTaskId"] = task.ID
		result["backgroundedByUser"] = true
		result["assistantAutoBackgrounded"] = false
		result["noOutputExpected"] = true
		result["interrupted"] = false
		return pretty(result), nil
	}
	timeoutMS := firstPositiveInt(payload.TimeoutMS, payload.Timeout)
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 2 * time.Minute
		timeoutMS = int(timeout / time.Millisecond)
	}
	if timeout > 30*time.Minute {
		return "", errors.New("timeout must be 1800000 ms or less")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	cmd := exec.CommandContext(ctx, executable, "-NoProfile", "-NonInteractive", "-Command", payload.Command)
	cmd.Dir = cwd
	env, err := toolEnvironment(ctx, t.ConfigHome, t.ConfigEnv)
	if err != nil {
		return "", err
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	exit := exitCode(err)
	result := bashOutputContractFields(false)
	result["stdout"] = stdout.String()
	result["stderr"] = stderr.String()
	result["exit_code"] = exit
	result["duration_ms"] = time.Since(started).Milliseconds()
	result["noOutputExpected"] = bashNoOutputExpected(stdout.String(), stderr.String())
	if ctx.Err() == context.DeadlineExceeded {
		interpretation := bashReturnCodeInterpretation(-1, true, payload.Command)
		if strings.TrimSpace(stderr.String()) == "" {
			result["stderr"] = fmt.Sprintf("Command exceeded timeout of %d ms", timeoutMS)
		} else {
			result["stderr"] = strings.TrimRight(stderr.String(), "\r\n") + "\nCommand exceeded timeout of " + strconv.Itoa(timeoutMS) + " ms"
		}
		result["interrupted"] = true
		result["error"] = "timeout"
		result["exit_code"] = -1
		result["returnCodeInterpretation"] = interpretation
		result["noOutputExpected"] = bashNoOutputExpected(fmt.Sprint(result["stdout"]), fmt.Sprint(result["stderr"]))
		result["structuredContent"] = bashTimeoutStructuredContent(payload.Command, timeoutMS, interpretation)
		return pretty(result), nil
	}
	if interpretation := bashReturnCodeInterpretation(exit, false, payload.Command); interpretation != "" {
		result["returnCodeInterpretation"] = interpretation
	}
	if err != nil {
		result["error"] = err.Error()
	}
	return pretty(result), nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func (t PowerShellTool) powerShellExecutable() (string, error) {
	if strings.TrimSpace(t.Executable) != "" {
		return t.Executable, nil
	}
	if path, err := exec.LookPath("pwsh"); err == nil {
		return path, nil
	}
	if path, err := exec.LookPath("powershell"); err == nil {
		return path, nil
	}
	return "", errors.New("PowerShell executable not found (expected `pwsh` or `powershell` in PATH)")
}

type ReadFileTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (ReadFileTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "read_file",
		Description: "Read a UTF-8 text file from the workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
				"file_path": map[string]any{
					"type":        "string",
					"description": "Claude-compatible alias for path.",
				},
				"offset": map[string]any{"type": "integer", "minimum": 0},
				"limit":  map[string]any{"type": "integer", "minimum": 1},
			},
			"anyOf":                pathOrFilePathRequirement(),
			"additionalProperties": false,
		},
	}
}

func (ReadFileTool) Permission() Permission { return PermissionReadOnly }

func (t ReadFileTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, firstNonEmpty(payload.Path, payload.FilePath), false)
	if err != nil {
		return "", err
	}
	data, truncated, err := readFileLimited(path, maxFileToolBytes)
	if err != nil {
		return "", err
	}
	if mediaType, ok := imageMediaType(path, data); ok {
		if truncated {
			return "", fmt.Errorf("image exceeds maximum readable size of %d bytes", maxFileToolBytes)
		}
		return pretty(imageReadResult(path, data, mediaType)), nil
	}
	if bytes.Contains(data[:min(len(data), 8192)], []byte{0}) {
		return "", errors.New("file appears to be binary")
	}
	lines := splitReadFileLines(string(data))
	start := min(max(payload.Offset, 0), len(lines))
	end := len(lines)
	if payload.Limit > 0 {
		end = min(start+payload.Limit, len(lines))
	}
	content := strings.Join(lines[start:end], "\n")
	lineCount := end - start
	filePayload := map[string]any{
		"file_path":  path,
		"content":    content,
		"numLines":   lineCount,
		"startLine":  start + 1,
		"totalLines": len(lines),
	}
	return pretty(map[string]any{
		"type":        "text",
		"path":        path,
		"start_line":  start + 1,
		"line_count":  lineCount,
		"next_offset": end,
		"total":       len(lines),
		"total_lines": len(lines),
		"has_more":    end < len(lines),
		"bytes":       len(data),
		"truncated":   truncated,
		"content":     content,
		"file":        filePayload,
	}), nil
}

func splitReadFileLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") {
		return lines[:len(lines)-1]
	}
	return lines
}

type WriteFileTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (WriteFileTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "write_file",
		Description: "Create or overwrite a UTF-8 text file in the workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
				"file_path": map[string]any{
					"type":        "string",
					"description": "Claude-compatible alias for path.",
				},
				"content": map[string]any{"type": "string"},
			},
			"required":             []string{"content"},
			"anyOf":                pathOrFilePathRequirement(),
			"additionalProperties": false,
		},
	}
}

func (WriteFileTool) Permission() Permission { return PermissionWorkspace }

func (t WriteFileTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if int64(len(payload.Content)) > maxFileToolBytes {
		return "", fmt.Errorf("content exceeds maximum file tool size of %d bytes", maxFileToolBytes)
	}
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, firstNonEmpty(payload.Path, payload.FilePath), true)
	if err != nil {
		return "", err
	}
	existed, original, undoAvailable, err := fileUndoSnapshot(path)
	if err != nil {
		return "", err
	}
	undoID := ""
	if undoAvailable {
		record, err := undo.Push(t.Workspace, "write_file", path, existed, original)
		if err != nil {
			return "", err
		}
		undoID = record.ID
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	kind := "update"
	if !existed {
		kind = "create"
	}
	if err := os.WriteFile(path, []byte(payload.Content), 0o644); err != nil {
		return "", err
	}
	var originalFile any
	if existed && original != nil {
		originalFile = string(original)
	}
	result := map[string]any{
		"path":            path,
		"kind":            kind,
		"type":            kind,
		"filePath":        path,
		"content":         payload.Content,
		"bytes":           len(payload.Content),
		"structuredPatch": makeStructuredPatch(string(original), payload.Content),
		"originalFile":    originalFile,
		"gitDiff":         nil,
	}
	addUndoFields(result, undoAvailable, undoID)
	return pretty(result), nil
}

type EditFileTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (EditFileTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "edit_file",
		Description: "Replace text in a workspace file.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
				"file_path": map[string]any{
					"type":        "string",
					"description": "Claude-compatible alias for path.",
				},
				"old_string":  map[string]any{"type": "string"},
				"new_string":  map[string]any{"type": "string"},
				"replace_all": map[string]any{"type": "boolean"},
			},
			"required":             []string{"old_string", "new_string"},
			"anyOf":                pathOrFilePathRequirement(),
			"additionalProperties": false,
		},
	}
}

func (EditFileTool) Permission() Permission { return PermissionWorkspace }

func (t EditFileTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path       string `json:"path"`
		FilePath   string `json:"file_path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.OldString == "" {
		return "", errors.New("old_string is required")
	}
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, firstNonEmpty(payload.Path, payload.FilePath), false)
	if err != nil {
		return "", err
	}
	data, truncated, err := readFileLimited(path, maxFileToolBytes)
	if err != nil {
		return "", err
	}
	if truncated {
		return "", fmt.Errorf("file exceeds maximum editable size of %d bytes", maxFileToolBytes)
	}
	content := string(data)
	count := strings.Count(content, payload.OldString)
	if count == 0 {
		return "", errors.New("old_string not found")
	}
	if !payload.ReplaceAll && count > 1 {
		return "", fmt.Errorf("old_string appears %d times; set replace_all to true or provide more context", count)
	}
	next := strings.Replace(content, payload.OldString, payload.NewString, 1)
	if payload.ReplaceAll {
		next = strings.ReplaceAll(content, payload.OldString, payload.NewString)
	}
	record, err := undo.Push(t.Workspace, "edit_file", path, true, data)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return "", err
	}
	replaced := 1
	if payload.ReplaceAll {
		replaced = count
	}
	return pretty(map[string]any{
		"path":            path,
		"filePath":        path,
		"oldString":       payload.OldString,
		"newString":       payload.NewString,
		"originalFile":    content,
		"structuredPatch": makeStructuredPatch(content, next),
		"userModified":    false,
		"replaceAll":      payload.ReplaceAll,
		"gitDiff":         nil,
		"replacements":    replaced,
		"undo_available":  true,
		"undo_id":         record.ID,
	}), nil
}

type MultiEditTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (MultiEditTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "multi_edit",
		Description: "Apply multiple text replacements to one workspace file atomically.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
				"file_path": map[string]any{
					"type":        "string",
					"description": "Claude-compatible alias for path.",
				},
				"edits": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"old_string":  map[string]any{"type": "string"},
							"new_string":  map[string]any{"type": "string"},
							"replace_all": map[string]any{"type": "boolean"},
						},
						"required":             []string{"old_string", "new_string"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"edits"},
			"anyOf":                pathOrFilePathRequirement(),
			"additionalProperties": false,
		},
	}
}
