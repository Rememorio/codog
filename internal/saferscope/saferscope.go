// Package saferscope turns workspace token-risk findings into reversible scope
// changes.
package saferscope

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/fileinventory"
)

const (
	StateFileName = "safer-scope.json"
	IgnoreMarker  = "# Codog safer-scope exclusions"
)

// Options controls safer-scope planning and application.
type Options struct {
	Choice           string
	Target           string
	RespectGitignore bool
	Now              time.Time
}

// Choice describes one actionable safer-scope option.
type Choice struct {
	ID              string   `json:"id"`
	Action          string   `json:"action"`
	Status          string   `json:"status"`
	Target          string   `json:"target,omitempty"`
	PreviewIncludes []string `json:"preview_includes,omitempty"`
	PreviewExcludes []string `json:"preview_excludes,omitempty"`
	IgnoreFile      string   `json:"ignore_file,omitempty"`
	IgnoreEntries   []string `json:"ignore_entries,omitempty"`
	Description     string   `json:"description"`
}

// State records the broader scope so an applied safer scope can be restored.
type State struct {
	Kind              string    `json:"kind"`
	OriginalWorkspace string    `json:"original_workspace"`
	ActiveWorkspace   string    `json:"active_workspace,omitempty"`
	AppliedChoice     string    `json:"applied_choice"`
	IgnoreFile        string    `json:"ignore_file,omitempty"`
	IgnoreEntries     []string  `json:"ignore_entries,omitempty"`
	AppliedAt         time.Time `json:"applied_at"`
}

// Report is the stable payload returned by safer-scope preview/apply/restore.
type Report struct {
	Kind              string                  `json:"kind"`
	Action            string                  `json:"action"`
	Status            string                  `json:"status"`
	Advisory          bool                    `json:"advisory"`
	Confirmed         bool                    `json:"confirmed"`
	Workspace         string                  `json:"workspace"`
	OriginalWorkspace string                  `json:"original_workspace,omitempty"`
	ActiveWorkspace   string                  `json:"active_workspace,omitempty"`
	AppliedChoice     string                  `json:"applied_choice,omitempty"`
	Restored          bool                    `json:"restored,omitempty"`
	Message           string                  `json:"message,omitempty"`
	Risk              fileinventory.ScopeRisk `json:"scope_risk"`
	Choices           []Choice                `json:"choices,omitempty"`
	Applied           []Choice                `json:"applied,omitempty"`
	RestoreCommand    string                  `json:"restore_command,omitempty"`
}

// Preview builds actionable safer-scope choices without changing workspace
// state.
func Preview(workspace string, opts Options) (Report, error) {
	workspace, err := cleanWorkspace(workspace)
	if err != nil {
		return Report{}, err
	}
	plan, err := buildPlan(workspace, opts)
	if err != nil {
		return Report{}, err
	}
	return Report{
		Kind:           "safer_scope",
		Action:         "preview",
		Status:         planStatus(plan),
		Advisory:       true,
		Confirmed:      false,
		Workspace:      workspace,
		Risk:           plan.Inventory.ScopeRisk,
		Choices:        plan.Choices,
		RestoreCommand: "codog scope restore",
	}, nil
}

// Apply executes selected safer-scope choices and writes restore metadata.
func Apply(workspace string, opts Options) (Report, error) {
	workspace, err := cleanWorkspace(workspace)
	if err != nil {
		return Report{}, err
	}
	plan, err := buildPlan(workspace, opts)
	if err != nil {
		return Report{}, err
	}
	selected := selectChoices(plan, opts.Choice)
	if len(selected) == 0 {
		return Report{}, errors.New("no safer-scope choice is available to apply")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	activeWorkspace := workspace
	appliedChoiceIDs := make([]string, 0, len(selected))
	var ignoreFile string
	var ignoreEntries []string
	for _, choice := range selected {
		appliedChoiceIDs = append(appliedChoiceIDs, choice.ID)
		switch choice.Action {
		case "switch_workspace":
			activeWorkspace = choice.Target
		case "write_ignore_stub":
			if err := appendIgnoreBlock(workspace, choice.IgnoreFile, choice.IgnoreEntries); err != nil {
				return Report{}, err
			}
			ignoreFile = choice.IgnoreFile
			ignoreEntries = append([]string(nil), choice.IgnoreEntries...)
		}
	}
	state := State{
		Kind:              "safer_scope",
		OriginalWorkspace: workspace,
		ActiveWorkspace:   activeWorkspace,
		AppliedChoice:     strings.Join(appliedChoiceIDs, ","),
		IgnoreFile:        ignoreFile,
		IgnoreEntries:     ignoreEntries,
		AppliedAt:         now.UTC(),
	}
	if err := SaveState(workspace, state); err != nil {
		return Report{}, err
	}
	if activeWorkspace != workspace {
		_ = SaveState(activeWorkspace, state)
	}
	return Report{
		Kind:              "safer_scope",
		Action:            "apply",
		Status:            "applied",
		Advisory:          false,
		Confirmed:         true,
		Workspace:         workspace,
		OriginalWorkspace: workspace,
		ActiveWorkspace:   activeWorkspace,
		AppliedChoice:     state.AppliedChoice,
		Message:           "safer scope applied",
		Risk:              plan.Inventory.ScopeRisk,
		Choices:           plan.Choices,
		Applied:           selected,
		RestoreCommand:    "codog scope restore",
	}, nil
}

// Restore loads safer-scope state and returns the original workspace.
func Restore(workspace string) (Report, error) {
	workspace, err := cleanWorkspace(workspace)
	if err != nil {
		return Report{}, err
	}
	state, err := LoadState(workspace)
	if err != nil {
		return Report{}, err
	}
	original := strings.TrimSpace(state.OriginalWorkspace)
	if original == "" {
		return Report{}, errors.New("safer-scope state is missing original workspace")
	}
	if state.IgnoreFile != "" {
		_ = removeIgnoreBlock(state.OriginalWorkspace, state.IgnoreFile)
	}
	return Report{
		Kind:              "safer_scope",
		Action:            "restore",
		Status:            "restored",
		Advisory:          false,
		Confirmed:         true,
		Workspace:         workspace,
		OriginalWorkspace: original,
		ActiveWorkspace:   original,
		AppliedChoice:     state.AppliedChoice,
		Restored:          true,
		Message:           "broader workspace restored",
	}, nil
}

// StatePath returns the workspace-local safer-scope state path.
func StatePath(workspace string) string {
	return filepath.Join(workspace, ".codog", StateFileName)
}

// LoadState reads safer-scope state from a workspace.
func LoadState(workspace string) (State, error) {
	workspace, err := cleanWorkspace(workspace)
	if err != nil {
		return State{}, err
	}
	data, err := os.ReadFile(StatePath(workspace))
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	if state.Kind != "safer_scope" {
		return State{}, errors.New("safer-scope state kind is invalid")
	}
	return state, nil
}

// SaveState writes safer-scope state below the given workspace.
func SaveState(workspace string, state State) error {
	workspace, err := cleanWorkspace(workspace)
	if err != nil {
		return err
	}
	state.Kind = "safer_scope"
	path := StatePath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".safer-scope-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// RenderText writes a human-readable safer-scope report.
func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Safer Scope")
	fmt.Fprintf(w, "  Status           %s\n", report.Status)
	fmt.Fprintf(w, "  Workspace        %s\n", report.Workspace)
	if report.ActiveWorkspace != "" {
		fmt.Fprintf(w, "  Active workspace %s\n", report.ActiveWorkspace)
	}
	fmt.Fprintf(w, "  Advisory         %t\n", report.Advisory)
	fmt.Fprintf(w, "  Confirmed        %t\n", report.Confirmed)
	if report.Risk.Status != "" {
		fmt.Fprintf(w, "  Scope risk       %s", report.Risk.Status)
		if report.Risk.Level != "" {
			fmt.Fprintf(w, " (%s)", report.Risk.Level)
		}
		fmt.Fprintln(w)
	}
	for _, choice := range report.Choices {
		fmt.Fprintf(w, "  Choice           %s %s %s\n", choice.ID, choice.Status, choice.Description)
		if choice.Target != "" {
			fmt.Fprintf(w, "    Target         %s\n", choice.Target)
		}
		if len(choice.PreviewIncludes) > 0 {
			fmt.Fprintf(w, "    Includes       %s\n", strings.Join(choice.PreviewIncludes, ", "))
		}
		if len(choice.PreviewExcludes) > 0 {
			fmt.Fprintf(w, "    Excludes       %s\n", strings.Join(choice.PreviewExcludes, ", "))
		}
		if len(choice.IgnoreEntries) > 0 {
			fmt.Fprintf(w, "    Ignore entries %s\n", strings.Join(choice.IgnoreEntries, ", "))
		}
	}
	if report.RestoreCommand != "" {
		fmt.Fprintf(w, "  Restore          %s\n", report.RestoreCommand)
	}
}

type plan struct {
	Inventory fileinventory.Report
	Choices   []Choice
}

func buildPlan(workspace string, opts Options) (plan, error) {
	inventory, err := fileinventory.Build(workspace, fileinventory.Options{
		Limit:            200,
		RespectGitignore: opts.RespectGitignore,
	})
	if err != nil {
		return plan{}, err
	}
	excludes := excludePaths(inventory.ScopeRisk.Sinks)
	choices := []Choice{}
	if target := strings.TrimSpace(opts.Target); target != "" {
		resolved, err := scopedDir(workspace, target)
		if err != nil {
			return plan{}, err
		}
		choices = append(choices, workspaceChoice(workspace, resolved, inventory, excludes))
	} else if target := recommendedWorkspace(workspace, inventory); target != "" && target != workspace {
		choices = append(choices, workspaceChoice(workspace, target, inventory, excludes))
	}
	if len(excludes) > 0 {
		choices = append(choices, Choice{
			ID:              "ignore",
			Action:          "write_ignore_stub",
			Status:          "available",
			PreviewExcludes: append([]string(nil), excludes...),
			IgnoreFile:      ".codogignore",
			IgnoreEntries:   ignoreEntries(excludes),
			Description:     "append a reversible .codogignore block for detected heavy paths",
		})
	}
	return plan{Inventory: inventory, Choices: choices}, nil
}

func planStatus(plan plan) string {
	if plan.Inventory.ScopeRisk.Status == "clean" {
		return "clean"
	}
	if len(plan.Choices) == 0 {
		return "no_action"
	}
	return "actionable"
}

func selectChoices(plan plan, choice string) []Choice {
	choice = strings.ToLower(strings.TrimSpace(choice))
	if choice == "" || choice == "auto" {
		for _, candidate := range plan.Choices {
			if candidate.ID == "workspace" {
				return []Choice{candidate}
			}
		}
		for _, candidate := range plan.Choices {
			if candidate.ID == "ignore" {
				return []Choice{candidate}
			}
		}
		return nil
	}
	if choice == "both" || choice == "all" {
		return append([]Choice(nil), plan.Choices...)
	}
	for _, candidate := range plan.Choices {
		if candidate.ID == choice || candidate.Action == choice {
			return []Choice{candidate}
		}
	}
	return nil
}

func workspaceChoice(workspace string, target string, inventory fileinventory.Report, excludes []string) Choice {
	return Choice{
		ID:              "workspace",
		Action:          "switch_workspace",
		Status:          "available",
		Target:          target,
		PreviewIncludes: previewIncludes(workspace, target, inventory),
		PreviewExcludes: append([]string(nil), excludes...),
		Description:     "switch the current runtime workspace to the safer source subdirectory",
	}
}

func previewIncludes(workspace string, target string, inventory fileinventory.Report) []string {
	prefix, err := filepath.Rel(workspace, target)
	if err != nil {
		return nil
	}
	prefix = filepath.ToSlash(prefix)
	if prefix == "." || prefix == "" {
		return nil
	}
	out := []string{}
	for _, entry := range inventory.Files {
		if entry.Path == prefix || strings.HasPrefix(entry.Path, prefix+"/") {
			out = append(out, entry.Path)
		}
		if len(out) >= 8 {
			break
		}
	}
	if len(out) == 0 {
		out = append(out, prefix)
	}
	return out
}

func recommendedWorkspace(workspace string, inventory fileinventory.Report) string {
	counts := map[string]int{}
	for _, entry := range inventory.Files {
		if entry.Depth < 2 {
			continue
		}
		if !sourceLike(entry.Path) {
			continue
		}
		top := strings.Split(entry.Path, "/")[0]
		if riskTop(top, inventory.ScopeRisk.Sinks) {
			continue
		}
		counts[top]++
	}
	if len(counts) == 0 {
		return ""
	}
	type candidate struct {
		name  string
		count int
	}
	var ranked []candidate
	for name, count := range counts {
		ranked = append(ranked, candidate{name: name, count: count})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].count != ranked[j].count {
			return ranked[i].count > ranked[j].count
		}
		return ranked[i].name < ranked[j].name
	})
	target := filepath.Join(workspace, ranked[0].name)
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return target
	}
	return ""
}

func sourceLike(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go", ".py", ".rs", ".js", ".jsx", ".ts", ".tsx", ".java", ".rb", ".php", ".c", ".h", ".cc", ".cpp", ".hpp", ".cs":
		return true
	}
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "go.mod", "package.json", "cargo.toml", "pyproject.toml":
		return true
	default:
		return false
	}
}

func riskTop(name string, sinks []fileinventory.ScopeRiskSink) bool {
	for _, sink := range sinks {
		top := strings.Split(strings.Trim(sink.Path, "/"), "/")[0]
		if top == name {
			return true
		}
	}
	return false
}

func excludePaths(sinks []fileinventory.ScopeRiskSink) []string {
	seen := map[string]bool{}
	var out []string
	for _, sink := range sinks {
		path := strings.Trim(strings.TrimSpace(sink.Path), "/")
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func ignoreEntries(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.Contains(filepath.Base(path), ".") {
			out = append(out, path)
		} else {
			out = append(out, strings.TrimSuffix(path, "/")+"/")
		}
	}
	return out
}

func appendIgnoreBlock(workspace string, ignoreFile string, entries []string) error {
	if ignoreFile == "" || len(entries) == 0 {
		return nil
	}
	path := filepath.Join(workspace, ignoreFile)
	existing, _ := os.ReadFile(path)
	content := string(existing)
	if strings.Contains(content, IgnoreMarker) {
		return nil
	}
	var builder strings.Builder
	if strings.TrimSpace(content) != "" {
		builder.WriteString(strings.TrimRight(content, "\n"))
		builder.WriteString("\n\n")
	}
	builder.WriteString(IgnoreMarker)
	builder.WriteByte('\n')
	for _, entry := range entries {
		builder.WriteString(entry)
		builder.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(builder.String()), 0o644)
}

func removeIgnoreBlock(workspace string, ignoreFile string) error {
	path := filepath.Join(workspace, ignoreFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	var kept []string
	dropping := false
	for _, line := range lines {
		if strings.TrimSpace(line) == IgnoreMarker {
			dropping = true
			continue
		}
		if dropping {
			if strings.TrimSpace(line) == "" {
				dropping = false
			}
			continue
		}
		kept = append(kept, line)
	}
	return os.WriteFile(path, []byte(strings.TrimRight(strings.Join(kept, "\n"), "\n")+"\n"), 0o644)
}

func scopedDir(workspace string, requested string) (string, error) {
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workspace, candidate)
	}
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(workspace, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("scope target escapes workspace: %s", requested)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("scope target is not a directory: %s", requested)
	}
	return resolved, nil
}

func cleanWorkspace(workspace string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		workspace = "."
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return abs, nil
}
