package taskpacket

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type Scope string

const (
	ScopeWorkspace  Scope = "workspace"
	ScopeModule     Scope = "module"
	ScopeSingleFile Scope = "single_file"
	ScopeCustom     Scope = "custom"
)

type Resource struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type Packet struct {
	Objective          string     `json:"objective"`
	Scope              Scope      `json:"scope"`
	ScopePath          string     `json:"scope_path,omitempty"`
	Repo               string     `json:"repo"`
	Worktree           string     `json:"worktree,omitempty"`
	BranchPolicy       string     `json:"branch_policy"`
	AcceptanceTests    []string   `json:"acceptance_tests,omitempty"`
	AcceptanceCriteria []string   `json:"acceptance_criteria,omitempty"`
	Resources          []Resource `json:"resources,omitempty"`
	Model              string     `json:"model,omitempty"`
	Provider           string     `json:"provider,omitempty"`
	PermissionProfile  string     `json:"permission_profile,omitempty"`
	CommitPolicy       string     `json:"commit_policy"`
	ReportingContract  string     `json:"reporting_contract,omitempty"`
	ReportingTargets   []string   `json:"reporting_targets,omitempty"`
	EscalationPolicy   string     `json:"escalation_policy,omitempty"`
	RecoveryPolicy     string     `json:"recovery_policy,omitempty"`
	VerificationPlan   []string   `json:"verification_plan,omitempty"`
}

type ResolvedScope struct {
	Scope        Scope  `json:"scope"`
	Path         string `json:"path,omitempty"`
	AbsolutePath string `json:"absolute_path,omitempty"`
}

type ValidationError struct {
	Errors []string `json:"errors"`
}

func (e ValidationError) Error() string {
	return strings.Join(e.Errors, "; ")
}

func Parse(data []byte) (Packet, error) {
	return parseRaw(data)
}

func (p *Packet) UnmarshalJSON(data []byte) error {
	packet, err := parseRaw(data)
	if err != nil {
		return err
	}
	*p = packet
	return nil
}

func parseRaw(data []byte) (Packet, error) {
	var raw struct {
		Objective          string     `json:"objective"`
		Scope              string     `json:"scope"`
		ScopePath          string     `json:"scope_path"`
		Repo               string     `json:"repo"`
		Worktree           string     `json:"worktree"`
		BranchPolicy       string     `json:"branch_policy"`
		AcceptanceTests    []string   `json:"acceptance_tests"`
		AcceptanceCriteria []string   `json:"acceptance_criteria"`
		Resources          []Resource `json:"resources"`
		Model              string     `json:"model"`
		Provider           string     `json:"provider"`
		PermissionProfile  string     `json:"permission_profile"`
		CommitPolicy       string     `json:"commit_policy"`
		ReportingContract  string     `json:"reporting_contract"`
		ReportingTargets   []string   `json:"reporting_targets"`
		EscalationPolicy   string     `json:"escalation_policy"`
		RecoveryPolicy     string     `json:"recovery_policy"`
		VerificationPlan   []string   `json:"verification_plan"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Packet{}, err
	}
	scope, scopePath := normalizeScope(raw.Scope, raw.ScopePath)
	return Packet{
		Objective:          strings.TrimSpace(raw.Objective),
		Scope:              scope,
		ScopePath:          scopePath,
		Repo:               strings.TrimSpace(raw.Repo),
		Worktree:           strings.TrimSpace(raw.Worktree),
		BranchPolicy:       strings.TrimSpace(raw.BranchPolicy),
		AcceptanceTests:    normalizeStrings(raw.AcceptanceTests),
		AcceptanceCriteria: normalizeStrings(raw.AcceptanceCriteria),
		Resources:          normalizeResources(raw.Resources),
		Model:              strings.TrimSpace(raw.Model),
		Provider:           strings.TrimSpace(raw.Provider),
		PermissionProfile:  strings.TrimSpace(raw.PermissionProfile),
		CommitPolicy:       strings.TrimSpace(raw.CommitPolicy),
		ReportingContract:  strings.TrimSpace(raw.ReportingContract),
		ReportingTargets:   normalizeStrings(raw.ReportingTargets),
		EscalationPolicy:   strings.TrimSpace(raw.EscalationPolicy),
		RecoveryPolicy:     strings.TrimSpace(raw.RecoveryPolicy),
		VerificationPlan:   normalizeStrings(raw.VerificationPlan),
	}, nil
}

func Validate(packet Packet) error {
	var errs []string
	required := map[string]string{
		"objective":     packet.Objective,
		"repo":          packet.Repo,
		"branch_policy": packet.BranchPolicy,
		"commit_policy": packet.CommitPolicy,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			errs = append(errs, field+" must not be empty")
		}
	}
	if packet.Scope == "" {
		errs = append(errs, "scope must not be empty")
	}
	if scopeNeedsPath(packet.Scope) && strings.TrimSpace(packet.ScopePath) == "" {
		errs = append(errs, fmt.Sprintf("scope_path is required for scope %q", packet.Scope))
	}
	if len(packet.AcceptanceTests) == 0 && len(packet.AcceptanceCriteria) == 0 {
		errs = append(errs, "acceptance_tests or acceptance_criteria must not be empty")
	}
	if strings.TrimSpace(packet.ReportingContract) == "" && len(packet.ReportingTargets) == 0 {
		errs = append(errs, "reporting_contract or reporting_targets must not be empty")
	}
	if strings.TrimSpace(packet.EscalationPolicy) == "" && strings.TrimSpace(packet.RecoveryPolicy) == "" {
		errs = append(errs, "escalation_policy or recovery_policy must not be empty")
	}
	validateNonEmptyList("acceptance_tests", packet.AcceptanceTests, &errs)
	validateNonEmptyList("acceptance_criteria", packet.AcceptanceCriteria, &errs)
	validateNonEmptyList("reporting_targets", packet.ReportingTargets, &errs)
	validateNonEmptyList("verification_plan", packet.VerificationPlan, &errs)
	for index, resource := range packet.Resources {
		if strings.TrimSpace(resource.Kind) == "" || strings.TrimSpace(resource.Value) == "" {
			errs = append(errs, fmt.Sprintf("resources contains an incomplete entry at index %d", index))
		}
	}
	if len(errs) > 0 {
		return ValidationError{Errors: errs}
	}
	return nil
}

func ResolveScope(workspace string, packet Packet) (ResolvedScope, error) {
	resolved := ResolvedScope{Scope: packet.Scope, Path: strings.TrimSpace(packet.ScopePath)}
	if packet.Scope == ScopeWorkspace {
		abs, err := filepath.Abs(workspace)
		if err != nil {
			return ResolvedScope{}, err
		}
		resolved.AbsolutePath = abs
		return resolved, nil
	}
	if resolved.Path == "" {
		return ResolvedScope{}, errors.New("scope_path is required")
	}
	candidate := resolved.Path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workspace, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return ResolvedScope{}, err
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return ResolvedScope{}, err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return ResolvedScope{}, err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ResolvedScope{}, fmt.Errorf("scope_path %q escapes workspace", resolved.Path)
	}
	resolved.AbsolutePath = abs
	return resolved, nil
}

func normalizeScope(scopeValue string, scopePath string) (Scope, string) {
	scopeValue = strings.TrimSpace(scopeValue)
	scopePath = strings.TrimSpace(scopePath)
	normalized := strings.ToLower(scopeValue)
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.ReplaceAll(normalized, " ", "_")
	switch Scope(normalized) {
	case ScopeWorkspace:
		return ScopeWorkspace, scopePath
	case ScopeModule:
		return ScopeModule, scopePath
	case ScopeSingleFile:
		return ScopeSingleFile, scopePath
	case ScopeCustom:
		return ScopeCustom, scopePath
	default:
		if scopePath == "" {
			scopePath = scopeValue
		}
		return ScopeCustom, scopePath
	}
}

func scopeNeedsPath(scope Scope) bool {
	switch scope {
	case ScopeModule, ScopeSingleFile, ScopeCustom:
		return true
	default:
		return false
	}
}

func normalizeStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.TrimSpace(value))
	}
	return out
}

func normalizeResources(values []Resource) []Resource {
	out := make([]Resource, 0, len(values))
	for _, value := range values {
		out = append(out, Resource{Kind: strings.TrimSpace(value.Kind), Value: strings.TrimSpace(value.Value)})
	}
	return out
}

func validateNonEmptyList(field string, values []string, errs *[]string) {
	for index, value := range values {
		if strings.TrimSpace(value) == "" {
			*errs = append(*errs, fmt.Sprintf("%s contains an empty value at index %d", field, index))
		}
	}
}
