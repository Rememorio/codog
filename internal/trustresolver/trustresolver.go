package trustresolver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	PolicyAutoTrust       = "auto_trust"
	PolicyRequireApproval = "require_approval"
	PolicyDeny            = "deny"

	ResolutionAutoAllowlisted = "auto_allowlisted"
	ResolutionManualApproval  = "manual_approval"

	StatusNotRequired      = "not_required"
	StatusAutoTrusted      = "auto_trusted"
	StatusRequiresApproval = "requires_approval"
	StatusDenied           = "denied"
)

var trustPromptCues = []string{
	"do you trust the files in this folder",
	"trust the files in this folder",
	"trust this folder",
	"allow and continue",
	"yes, proceed",
}

var manualApprovalCues = []string{
	"yes, i trust",
	"i trust this",
	"trusted manually",
	"approval granted",
}

type AllowlistEntry struct {
	Pattern         string `json:"pattern"`
	WorktreePattern string `json:"worktree_pattern,omitempty"`
	Description     string `json:"description,omitempty"`
}

type Config struct {
	Allowlisted []AllowlistEntry `json:"allowlisted,omitempty"`
	Denied      []string         `json:"denied,omitempty"`
	EmitEvents  bool             `json:"emit_events"`
}

type Event struct {
	Type       string `json:"type"`
	CWD        string `json:"cwd"`
	Repo       string `json:"repo,omitempty"`
	Worktree   string `json:"worktree,omitempty"`
	Policy     string `json:"policy,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type Decision struct {
	Status         string  `json:"status"`
	PromptDetected bool    `json:"prompt_detected"`
	Trusted        bool    `json:"trusted"`
	Policy         string  `json:"policy,omitempty"`
	Resolution     string  `json:"resolution,omitempty"`
	MatchedPattern string  `json:"matched_pattern,omitempty"`
	Events         []Event `json:"events,omitempty"`
}

type Resolver struct {
	config Config
}

func New(config Config) Resolver {
	if !config.EmitEvents {
		// Zero-value configs should emit events. Callers that need no events can
		// use NewWithoutEvents to make the choice explicit.
		config.EmitEvents = true
	}
	return Resolver{config: config}
}

func NewWithoutEvents(config Config) Resolver {
	config.EmitEvents = false
	return Resolver{config: config}
}

func (r Resolver) Resolve(cwd string, worktree string, screenText string) Decision {
	cwd = cleanPath(cwd)
	worktree = strings.TrimSpace(worktree)
	promptDetected := DetectTrustPrompt(screenText)
	if !promptDetected {
		return Decision{Status: StatusNotRequired, PromptDetected: false, Trusted: r.Trusts(cwd, worktree)}
	}

	events := r.events(Event{Type: "trust_required", CWD: cwd, Repo: repoName(cwd), Worktree: worktree})
	if denied, matchedRoot := r.denied(cwd); denied {
		reason := "cwd matches denied trust root: " + matchedRoot
		events = append(events, r.events(Event{Type: "trust_denied", CWD: cwd, Reason: reason})...)
		return Decision{
			Status:         StatusDenied,
			PromptDetected: true,
			Trusted:        false,
			Policy:         PolicyDeny,
			MatchedPattern: matchedRoot,
			Events:         events,
		}
	}

	if entry, ok := r.allowlisted(cwd, worktree); ok {
		events = append(events, r.events(Event{Type: "trust_resolved", CWD: cwd, Policy: PolicyAutoTrust, Resolution: ResolutionAutoAllowlisted})...)
		return Decision{
			Status:         StatusAutoTrusted,
			PromptDetected: true,
			Trusted:        true,
			Policy:         PolicyAutoTrust,
			Resolution:     ResolutionAutoAllowlisted,
			MatchedPattern: entry.Pattern,
			Events:         events,
		}
	}

	if DetectManualApproval(screenText) {
		events = append(events, r.events(Event{Type: "trust_resolved", CWD: cwd, Policy: PolicyRequireApproval, Resolution: ResolutionManualApproval})...)
		return Decision{
			Status:         StatusRequiresApproval,
			PromptDetected: true,
			Trusted:        true,
			Policy:         PolicyRequireApproval,
			Resolution:     ResolutionManualApproval,
			Events:         events,
		}
	}

	return Decision{
		Status:         StatusRequiresApproval,
		PromptDetected: true,
		Trusted:        false,
		Policy:         PolicyRequireApproval,
		Events:         events,
	}
}

func (r Resolver) Trusts(cwd string, worktree string) bool {
	cwd = cleanPath(cwd)
	if denied, _ := r.denied(cwd); denied {
		return false
	}
	_, ok := r.allowlisted(cwd, worktree)
	return ok
}

func (r Resolver) denied(cwd string) (bool, string) {
	for _, root := range r.config.Denied {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if PathMatchesTrustedRoot(cwd, root) {
			return true, cleanPath(root)
		}
	}
	return false, ""
}

func (r Resolver) allowlisted(cwd string, worktree string) (AllowlistEntry, bool) {
	for _, entry := range r.config.Allowlisted {
		if !PatternMatches(entry.Pattern, cwd) {
			continue
		}
		if strings.TrimSpace(entry.WorktreePattern) != "" && !PatternMatches(entry.WorktreePattern, worktree) {
			continue
		}
		return entry, true
	}
	return AllowlistEntry{}, false
}

func (r Resolver) events(event Event) []Event {
	if !r.config.EmitEvents {
		return nil
	}
	return []Event{event}
}

func DetectTrustPrompt(screenText string) bool {
	lowered := strings.ToLower(screenText)
	for _, cue := range trustPromptCues {
		if strings.Contains(lowered, cue) {
			return true
		}
	}
	return false
}

func DetectManualApproval(screenText string) bool {
	lowered := strings.ToLower(screenText)
	for _, cue := range manualApprovalCues {
		if strings.Contains(lowered, cue) {
			return true
		}
	}
	return false
}

func PathMatchesTrustedRoot(cwd string, trustedRoot string) bool {
	candidate := cleanPath(cwd)
	root := cleanPath(trustedRoot)
	if candidate == "" || root == "" {
		return false
	}
	if candidate == root {
		return true
	}
	rest := strings.TrimPrefix(candidate, root)
	return rest != candidate && (strings.HasPrefix(rest, string(filepath.Separator)) || strings.HasPrefix(rest, "/"))
}

func PatternMatches(pattern string, value string) bool {
	pattern = cleanPattern(pattern)
	value = cleanPattern(value)
	if pattern == "" || value == "" {
		return false
	}
	if pattern == value || PathMatchesTrustedRoot(value, pattern) {
		return true
	}
	if !strings.ContainsAny(pattern, "*?") {
		for _, component := range strings.Split(value, "/") {
			if component == pattern || strings.Contains(component, pattern) {
				return true
			}
		}
		return false
	}
	if strings.HasSuffix(pattern, "/*") {
		prefix := strings.TrimSuffix(pattern, "/*")
		if value == prefix || strings.HasPrefix(value, prefix+"/") {
			return true
		}
	}
	return globMatch(pattern, value)
}

func globMatch(pattern string, value string) bool {
	patternRunes := []rune(pattern)
	valueRunes := []rune(value)
	var match func(int, int) bool
	match = func(pi int, vi int) bool {
		for pi < len(patternRunes) {
			switch patternRunes[pi] {
			case '*':
				pi++
				if pi == len(patternRunes) {
					return true
				}
				for skip := vi; skip <= len(valueRunes); skip++ {
					if match(pi, skip) {
						return true
					}
				}
				return false
			case '?':
				if vi >= len(valueRunes) {
					return false
				}
				pi++
				vi++
			default:
				if vi >= len(valueRunes) || patternRunes[pi] != valueRunes[vi] {
					return false
				}
				pi++
				vi++
			}
		}
		return vi == len(valueRunes)
	}
	return match(0, 0)
}

func cleanPath(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	if path == "" {
		return ""
	}
	return strings.TrimRight(filepath.Clean(path), "/")
}

func cleanPattern(pattern string) string {
	pattern = strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/"))
	if pattern == "" {
		return ""
	}
	return strings.TrimRight(pattern, "/")
}

func repoName(cwd string) string {
	cwd = cleanPath(cwd)
	if cwd == "" {
		return ""
	}
	if info, err := os.Stat(filepath.FromSlash(cwd)); err == nil && info.IsDir() {
		cmd := exec.Command("git", "rev-parse", "--show-toplevel")
		cmd.Dir = filepath.FromSlash(cwd)
		if output, err := cmd.Output(); err == nil {
			root := strings.TrimSpace(string(output))
			if root != "" {
				if base := filepath.Base(root); base != "." && base != string(filepath.Separator) {
					return base
				}
			}
		}
	}
	return filepath.Base(cwd)
}
