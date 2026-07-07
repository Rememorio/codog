package prworkflow

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/laneevents"
	"github.com/Rememorio/codog/internal/ship"
)

type Options struct {
	Workspace string
	Message   string
	Title     string
	Body      string
	Branch    string
	Base      string
	Remote    string
	All       bool
	Draft     bool
	NoPR      bool
	DryRun    bool
}

type Step struct {
	Name    string   `json:"name"`
	Command []string `json:"command,omitempty"`
	Status  string   `json:"status"`
	Output  string   `json:"output,omitempty"`
}

type Report struct {
	Kind       string             `json:"kind"`
	Status     string             `json:"status"`
	DryRun     bool               `json:"dry_run"`
	Branch     string             `json:"branch"`
	Base       string             `json:"base,omitempty"`
	Remote     string             `json:"remote"`
	Title      string             `json:"title"`
	Commit     string             `json:"commit,omitempty"`
	PRURL      string             `json:"pr_url,omitempty"`
	Message    string             `json:"message,omitempty"`
	Steps      []Step             `json:"steps"`
	Ship       *ship.Report       `json:"ship,omitempty"`
	ShipEvents []laneevents.Event `json:"ship_events,omitempty"`
}

func Run(ctx context.Context, opts Options) (Report, error) {
	opts = normalizeOptions(opts)
	if strings.TrimSpace(opts.Message) == "" {
		return Report{}, errors.New("commit-push-pr requires a commit message")
	}
	if strings.TrimSpace(opts.Workspace) == "" {
		return Report{}, errors.New("workspace is required")
	}
	if _, err := gitops.Root(opts.Workspace); err != nil {
		return Report{}, err
	}
	base := opts.Base
	if base == "" {
		base = defaultBranch(opts.Workspace, opts.Remote)
	}
	current, err := gitops.Branch(opts.Workspace)
	if err != nil {
		return Report{}, err
	}
	baseCommit := gitTrim(opts.Workspace, "rev-parse", "HEAD")
	actor := gitActor(opts.Workspace)
	branch := strings.TrimSpace(opts.Branch)
	if branch == "" {
		branch = current
	}
	if isProtectedBranch(branch, base) {
		branch = uniqueBranchName(opts.Message)
	}
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = firstLine(opts.Message)
	}
	body := strings.TrimSpace(opts.Body)
	if body == "" {
		body = defaultBody(opts.Workspace, title)
	}
	status := "ok"
	if opts.DryRun {
		status = "planned"
	}
	report := Report{
		Kind:    "commit_push_pr",
		Status:  status,
		DryRun:  opts.DryRun,
		Branch:  branch,
		Base:    base,
		Remote:  opts.Remote,
		Title:   title,
		Message: opts.Message,
	}
	report.Ship = plannedShipReport(ship.Provenance{
		SourceBranch: branch,
		BaseBranch:   base,
		BaseCommit:   baseCommit,
		MergeMethod:  mergeMethod(branch, base, opts.NoPR),
		Actor:        actor,
		Remote:       opts.Remote,
		TargetBranch: targetBranch(branch, base, opts.NoPR),
	})
	report.ShipEvents = ship.LaneEvents(*report.Ship)
	if branch != current {
		report.Steps = append(report.Steps, Step{Name: "branch", Command: []string{"git", "switch", "-c", branch}, Status: statusForDryRun(opts.DryRun)})
	}
	if opts.All {
		report.Steps = append(report.Steps, Step{Name: "stage", Command: []string{"git", "add", "-A"}, Status: statusForDryRun(opts.DryRun)})
	}
	report.Steps = append(report.Steps,
		Step{Name: "commit", Command: []string{"git", "commit", "-m", opts.Message}, Status: statusForDryRun(opts.DryRun)},
		Step{Name: "push", Command: []string{"git", "push", "-u", opts.Remote, branch}, Status: statusForDryRun(opts.DryRun)},
	)
	if !opts.NoPR {
		report.Steps = append(report.Steps, Step{Name: "pull_request", Command: prCommand(opts, title, base), Status: statusForDryRun(opts.DryRun)})
	}
	if opts.DryRun {
		return report, nil
	}
	if branch != current {
		output, err := switchOrCreateBranch(opts.Workspace, branch)
		if err != nil {
			return report, err
		}
		report.Steps = markStep(report.Steps, "branch", "ok", output)
	}
	commit, err := gitops.Commit(opts.Workspace, gitops.CommitOptions{All: opts.All, Message: opts.Message})
	if err != nil {
		return report, err
	}
	report.Commit = commit.Commit
	report.Ship = completedShipReport(*report.Ship, commit.Commit)
	report.ShipEvents = ship.LaneEvents(*report.Ship)
	report.Steps = markStep(report.Steps, "stage", "ok", "")
	report.Steps = markStep(report.Steps, "commit", "ok", commit.Summary)
	pushOut, err := gitops.Run(opts.Workspace, "push", "-u", opts.Remote, branch)
	if err != nil {
		return report, err
	}
	report.Steps = markStep(report.Steps, "push", "ok", pushOut)
	if !opts.NoPR {
		prURL, prOut, err := createOrUpdatePR(ctx, opts.Workspace, title, body, base, opts.Draft)
		if err != nil {
			return report, err
		}
		report.PRURL = prURL
		report.Ship.Provenance.PRURL = prURL
		report.Ship.Provenance.PRNumber = parsePRNumber(prURL)
		report.Ship = completedShipReport(*report.Ship, report.Commit)
		report.ShipEvents = ship.LaneEvents(*report.Ship)
		report.Steps = markStep(report.Steps, "pull_request", "ok", prOut)
	}
	return report, nil
}

func plannedShipReport(provenance ship.Provenance) *ship.Report {
	report := ship.NewReport("planned", provenance, ship.Classification{Intentional: max(1, provenance.CommitCount), Riders: 0}, time.Now().UTC())
	return &report
}

func completedShipReport(previous ship.Report, commit string) *ship.Report {
	provenance := previous.Provenance
	commit = strings.TrimSpace(commit)
	if commit != "" {
		provenance.FirstCommit = commit
		provenance.LastCommit = commit
		provenance.CommitCount = 1
	}
	report := ship.NewReport("confirmed", provenance, ship.Classification{Intentional: max(1, provenance.CommitCount), Riders: 0}, time.Now().UTC())
	return &report
}

func mergeMethod(branch string, base string, noPR bool) string {
	if noPR {
		if branch == base || branch == "main" || branch == "master" {
			return "direct_push"
		}
		return "branch_push"
	}
	return "pull_request"
}

func targetBranch(branch string, base string, noPR bool) string {
	if noPR {
		return branch
	}
	return base
}

func gitTrim(workspace string, args ...string) string {
	out, err := gitops.Run(workspace, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func gitActor(workspace string) string {
	name := gitTrim(workspace, "config", "user.name")
	email := gitTrim(workspace, "config", "user.email")
	switch {
	case name != "" && email != "":
		return name + " <" + email + ">"
	case name != "":
		return name
	case email != "":
		return email
	default:
		return "unknown"
	}
}

func parsePRNumber(prURL string) int {
	prURL = strings.TrimSpace(prURL)
	if prURL == "" {
		return 0
	}
	parts := strings.Split(strings.Trim(prURL, "/"), "/")
	if len(parts) == 0 {
		return 0
	}
	number, _ := strconv.Atoi(parts[len(parts)-1])
	return number
}

func normalizeOptions(opts Options) Options {
	opts.Workspace = strings.TrimSpace(opts.Workspace)
	if opts.Workspace != "" {
		opts.Workspace = filepath.Clean(opts.Workspace)
	}
	opts.Message = strings.TrimSpace(opts.Message)
	opts.Title = strings.TrimSpace(opts.Title)
	opts.Body = strings.TrimSpace(opts.Body)
	opts.Branch = strings.TrimSpace(opts.Branch)
	opts.Base = strings.TrimSpace(opts.Base)
	opts.Remote = strings.TrimSpace(opts.Remote)
	if opts.Remote == "" {
		opts.Remote = "origin"
	}
	return opts
}

func defaultBranch(workspace, remote string) string {
	out, err := gitops.Run(workspace, "symbolic-ref", "--quiet", "--short", "refs/remotes/"+remote+"/HEAD")
	if err == nil {
		out = strings.TrimSpace(out)
		prefix := remote + "/"
		if strings.HasPrefix(out, prefix) {
			return strings.TrimPrefix(out, prefix)
		}
	}
	if out, err := gitops.Run(workspace, "config", "--get", "init.defaultBranch"); err == nil && strings.TrimSpace(out) != "" {
		return strings.TrimSpace(out)
	}
	return "main"
}

func isProtectedBranch(branch, base string) bool {
	branch = strings.TrimSpace(branch)
	base = strings.TrimSpace(base)
	return branch == "" || branch == base || branch == "main" || branch == "master"
}

func uniqueBranchName(message string) string {
	slug := branchSlug(message)
	if slug == "" {
		slug = "update"
	}
	return "codog_" + slug
}

func branchSlug(message string) string {
	message = strings.ToLower(firstLine(message))
	message = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(message, "_")
	message = strings.Trim(message, "_")
	if len(message) > 48 {
		message = strings.Trim(message[:48], "_")
	}
	return message
}

func firstLine(text string) string {
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func defaultBody(workspace, title string) string {
	status, _ := gitops.Status(workspace)
	diffStat, _ := gitops.Run(workspace, "diff", "--stat")
	if strings.TrimSpace(diffStat) == "" {
		diffStat, _ = gitops.Run(workspace, "diff", "--cached", "--stat")
	}
	var builder strings.Builder
	builder.WriteString("## Summary\n")
	builder.WriteString("- " + title + "\n\n")
	builder.WriteString("## Test plan\n")
	builder.WriteString("- [ ] Run the relevant validation before merging.\n")
	if strings.TrimSpace(status) != "" {
		builder.WriteString("\n## Git status\n```text\n")
		builder.WriteString(strings.TrimSpace(status))
		builder.WriteString("\n```\n")
	}
	if strings.TrimSpace(diffStat) != "" {
		builder.WriteString("\n## Diff stat\n```text\n")
		builder.WriteString(strings.TrimSpace(diffStat))
		builder.WriteString("\n```\n")
	}
	return builder.String()
}

func statusForDryRun(dryRun bool) string {
	if dryRun {
		return "planned"
	}
	return "pending"
}

func prCommand(opts Options, title, base string) []string {
	args := []string{"gh", "pr", "create", "--title", title, "--body", "<body>"}
	if base != "" {
		args = append(args, "--base", base)
	}
	if opts.Draft {
		args = append(args, "--draft")
	}
	return args
}

func switchOrCreateBranch(workspace, branch string) (string, error) {
	if _, err := gitops.Run(workspace, "rev-parse", "--verify", "refs/heads/"+branch); err == nil {
		return gitops.SwitchBranch(workspace, branch)
	}
	return gitops.CreateBranch(workspace, branch, "", true)
}

func markStep(steps []Step, name, status, output string) []Step {
	for i := range steps {
		if steps[i].Name == name {
			steps[i].Status = status
			steps[i].Output = strings.TrimSpace(output)
		}
	}
	return steps
}

func createOrUpdatePR(ctx context.Context, workspace, title, body, base string, draft bool) (string, string, error) {
	existing, _ := gh(ctx, workspace, "pr", "view", "--json", "url", "--jq", ".url")
	existing = strings.TrimSpace(existing)
	if existing != "" {
		output, err := gh(ctx, workspace, "pr", "edit", "--title", title, "--body", body)
		if err != nil {
			return "", output, err
		}
		return existing, output, nil
	}
	args := []string{"pr", "create", "--title", title, "--body", body}
	if base != "" {
		args = append(args, "--base", base)
	}
	if draft {
		args = append(args, "--draft")
	}
	output, err := gh(ctx, workspace, args...)
	if err != nil {
		return "", output, err
	}
	return strings.TrimSpace(output), output, nil
}

func gh(ctx context.Context, workspace string, args ...string) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(data))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("%s", text)
	}
	return text, nil
}
