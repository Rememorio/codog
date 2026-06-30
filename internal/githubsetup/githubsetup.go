package githubsetup

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	DefaultSecretName = "ANTHROPIC_API_KEY"
	DocsURL           = "https://github.com/anthropics/claude-code-action/blob/main/docs/setup.md"
)

var secretNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Options struct {
	Workspace  string
	SecretName string
	Workflows  []string
	Force      bool
	DryRun     bool
}

type WorkflowFile struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Exists      bool   `json:"exists"`
	Created     bool   `json:"created"`
	Overwritten bool   `json:"overwritten"`
}

type Report struct {
	Kind         string         `json:"kind"`
	Action       string         `json:"action"`
	Status       string         `json:"status"`
	Workspace    string         `json:"workspace"`
	Repo         string         `json:"repo,omitempty"`
	SecretName   string         `json:"secret_name"`
	DryRun       bool           `json:"dry_run"`
	DocsURL      string         `json:"docs_url"`
	Workflows    []WorkflowFile `json:"workflows"`
	Instructions []string       `json:"instructions"`
	Warnings     []string       `json:"warnings,omitempty"`
}

func Setup(opts Options) (Report, error) {
	workspace, err := normalizeWorkspace(opts.Workspace)
	if err != nil {
		return Report{}, err
	}
	secretName := strings.TrimSpace(opts.SecretName)
	if secretName == "" {
		secretName = DefaultSecretName
	}
	if !secretNamePattern.MatchString(secretName) {
		return Report{}, fmt.Errorf("invalid GitHub secret name %q", secretName)
	}
	workflows, err := normalizeWorkflows(opts.Workflows)
	if err != nil {
		return Report{}, err
	}
	repo := detectGitHubRepo(workspace)
	report := Report{
		Kind:       "install_github_app",
		Action:     "setup",
		Status:     "ok",
		Workspace:  workspace,
		Repo:       repo,
		SecretName: secretName,
		DryRun:     opts.DryRun,
		DocsURL:    DocsURL,
	}
	if repo == "" {
		report.Warnings = append(report.Warnings, "Could not detect a GitHub origin remote for this workspace.")
	}
	for _, workflow := range workflows {
		spec := workflowSpec(workflow, secretName)
		file := WorkflowFile{Name: workflow, Path: filepath.Join(workspace, spec.path)}
		_, statErr := os.Stat(file.Path)
		file.Exists = statErr == nil
		if statErr != nil && !os.IsNotExist(statErr) {
			return Report{}, statErr
		}
		switch {
		case opts.DryRun:
		case file.Exists && !opts.Force:
			report.Warnings = append(report.Warnings, fmt.Sprintf("Workflow already exists: %s; pass --force to overwrite.", spec.path))
		default:
			if err := os.MkdirAll(filepath.Dir(file.Path), 0o755); err != nil {
				return Report{}, err
			}
			if err := os.WriteFile(file.Path, []byte(spec.content), 0o644); err != nil {
				return Report{}, err
			}
			if file.Exists {
				file.Overwritten = true
			} else {
				file.Created = true
			}
			file.Exists = true
		}
		report.Workflows = append(report.Workflows, file)
	}
	report.Instructions = githubInstructions(repo, secretName)
	return report, nil
}

func normalizeWorkspace(workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func normalizeWorkflows(values []string) ([]string, error) {
	if len(values) == 0 {
		return []string{"claude", "review"}, nil
	}
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			value := strings.ToLower(strings.TrimSpace(part))
			if value == "" {
				continue
			}
			if value == "all" {
				for _, item := range []string{"claude", "review"} {
					if !seen[item] {
						out = append(out, item)
						seen[item] = true
					}
				}
				continue
			}
			if value == "claude-review" {
				value = "review"
			}
			if value != "claude" && value != "review" {
				return nil, fmt.Errorf("unknown GitHub workflow %q", part)
			}
			if !seen[value] {
				out = append(out, value)
				seen[value] = true
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one workflow is required")
	}
	return out, nil
}

type workflowTemplate struct {
	path    string
	content string
}

func workflowSpec(name string, secretName string) workflowTemplate {
	switch name {
	case "review":
		return workflowTemplate{
			path:    filepath.Join(".github", "workflows", "claude-code-review.yml"),
			content: reviewWorkflow(secretName),
		}
	default:
		return workflowTemplate{
			path:    filepath.Join(".github", "workflows", "claude.yml"),
			content: claudeWorkflow(secretName),
		}
	}
}

func claudeWorkflow(secretName string) string {
	return fmt.Sprintf(`name: Claude Code

on:
  issue_comment:
    types: [created]
  pull_request_review_comment:
    types: [created]
  issues:
    types: [opened, assigned]
  pull_request_review:
    types: [submitted]

jobs:
  claude:
    if: |
      (github.event_name == 'issue_comment' && contains(github.event.comment.body, '@claude')) ||
      (github.event_name == 'pull_request_review_comment' && contains(github.event.comment.body, '@claude')) ||
      (github.event_name == 'pull_request_review' && contains(github.event.review.body, '@claude')) ||
      (github.event_name == 'issues' && (contains(github.event.issue.body, '@claude') || contains(github.event.issue.title, '@claude')))
    runs-on: ubuntu-latest
    permissions:
      contents: read
      pull-requests: read
      issues: read
      id-token: write
      actions: read
    steps:
      - name: Checkout repository
        uses: actions/checkout@v4
        with:
          fetch-depth: 1

      - name: Run Claude Code
        uses: anthropics/claude-code-action@v1
        with:
          anthropic_api_key: ${{ secrets.%s }}
          additional_permissions: |
            actions: read
`, secretName)
}

func reviewWorkflow(secretName string) string {
	return fmt.Sprintf(`name: Claude Code Review

on:
  pull_request:
    types: [opened, synchronize, ready_for_review, reopened]

jobs:
  claude-review:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      pull-requests: read
      issues: read
      id-token: write
    steps:
      - name: Checkout repository
        uses: actions/checkout@v4
        with:
          fetch-depth: 1

      - name: Run Claude Code Review
        uses: anthropics/claude-code-action@v1
        with:
          anthropic_api_key: ${{ secrets.%s }}
          plugin_marketplaces: 'https://github.com/anthropics/claude-code.git'
          plugins: 'code-review@claude-code-plugins'
          prompt: '/code-review:code-review ${{ github.repository }}/pull/${{ github.event.pull_request.number }}'
`, secretName)
}

func githubInstructions(repo string, secretName string) []string {
	instructions := []string{
		fmt.Sprintf("Set the repository secret: gh secret set %s --body \"$ANTHROPIC_API_KEY\"", secretName),
		"Commit the generated workflow file(s) and open a pull request.",
		"After merging, mention @claude in an issue or pull request comment to trigger the assistant workflow.",
	}
	if repo != "" {
		instructions[0] = fmt.Sprintf("Set the repository secret: gh secret set %s --repo %s --body \"$ANTHROPIC_API_KEY\"", secretName, repo)
	}
	return instructions
}

func detectGitHubRepo(workspace string) string {
	cmd := exec.Command("git", "-C", workspace, "config", "--get", "remote.origin.url")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return ""
	}
	return parseGitHubRepo(strings.TrimSpace(stdout.String()))
}

func parseGitHubRepo(remote string) string {
	remote = strings.TrimSpace(remote)
	remote = strings.TrimSuffix(remote, ".git")
	switch {
	case strings.HasPrefix(remote, "git@github.com:"):
		return strings.TrimPrefix(remote, "git@github.com:")
	case strings.HasPrefix(remote, "https://github.com/"):
		return strings.TrimPrefix(remote, "https://github.com/")
	case strings.HasPrefix(remote, "ssh://git@github.com/"):
		return strings.TrimPrefix(remote, "ssh://git@github.com/")
	default:
		return ""
	}
}
