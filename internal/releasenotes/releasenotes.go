package releasenotes

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

const defaultLimit = 50

type Options struct {
	From  string
	To    string
	Limit int
}

type Commit struct {
	Hash    string   `json:"hash"`
	Short   string   `json:"short"`
	Date    string   `json:"date"`
	Author  string   `json:"author"`
	Subject string   `json:"subject"`
	Files   []string `json:"files,omitempty"`
}

type Section struct {
	Name    string   `json:"name"`
	Commits []Commit `json:"commits"`
}

type Report struct {
	Kind     string    `json:"kind"`
	Action   string    `json:"action"`
	Status   string    `json:"status"`
	From     string    `json:"from,omitempty"`
	To       string    `json:"to"`
	Total    int       `json:"total"`
	Sections []Section `json:"sections"`
	Commits  []Commit  `json:"commits"`
}

func Generate(workspace string, options Options) (Report, error) {
	if workspace == "" {
		workspace = "."
	}
	to := strings.TrimSpace(options.To)
	if to == "" {
		to = "HEAD"
	}
	resolvedTo, err := git(workspace, "rev-parse", "--short", to)
	if err != nil {
		return Report{}, err
	}
	from := strings.TrimSpace(options.From)
	if from == "" {
		from, _ = latestTag(workspace)
	}
	limit := options.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	commits, err := logCommits(workspace, from, to, limit)
	if err != nil {
		return Report{}, err
	}
	status := "ok"
	if len(commits) == 0 {
		status = "empty"
	}
	return Report{
		Kind:     "release_notes",
		Action:   "generate",
		Status:   status,
		From:     from,
		To:       resolvedTo,
		Total:    len(commits),
		Sections: sections(commits),
		Commits:  commits,
	}, nil
}

func RenderMarkdown(w io.Writer, report Report) {
	fmt.Fprintln(w, "# Release Notes")
	fmt.Fprintln(w)
	if report.From != "" {
		fmt.Fprintf(w, "Range: `%s..%s`\n\n", report.From, report.To)
	} else {
		fmt.Fprintf(w, "Range: latest %d commits through `%s`\n\n", report.Total, report.To)
	}
	if report.Total == 0 {
		fmt.Fprintln(w, "No changes found.")
		return
	}
	for _, section := range report.Sections {
		if len(section.Commits) == 0 {
			continue
		}
		fmt.Fprintf(w, "## %s\n\n", section.Name)
		for _, commit := range section.Commits {
			fmt.Fprintf(w, "- `%s` %s\n", commit.Short, commit.Subject)
			if len(commit.Files) != 0 {
				fmt.Fprintf(w, "  - Files: %s\n", strings.Join(commit.Files, ", "))
			}
		}
		fmt.Fprintln(w)
	}
}

func logCommits(workspace, from, to string, limit int) ([]Commit, error) {
	args := []string{"log", "--date=short", "--format=%H%x1f%h%x1f%ad%x1f%an%x1f%s", "--name-only"}
	if from != "" {
		args = append(args, from+".."+to)
	} else {
		args = append(args, "--max-count="+strconv.Itoa(limit), to)
	}
	raw, err := git(workspace, args...)
	if err != nil {
		if strings.Contains(err.Error(), "does not have any commits yet") {
			return nil, nil
		}
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var commits []Commit
	var current *Commit
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, "\x1f") {
			parts := strings.SplitN(line, "\x1f", 5)
			if len(parts) != 5 {
				continue
			}
			commits = append(commits, Commit{
				Hash:    parts[0],
				Short:   parts[1],
				Date:    parts[2],
				Author:  parts[3],
				Subject: parts[4],
			})
			current = &commits[len(commits)-1]
			continue
		}
		if current != nil {
			current.Files = append(current.Files, line)
		}
	}
	return commits, nil
}

func sections(commits []Commit) []Section {
	order := []string{"Features", "Fixes", "Security", "Performance", "Documentation", "Tests", "Refactors", "Build and CI", "Chores", "Other"}
	grouped := map[string][]Commit{}
	for _, commit := range commits {
		group := sectionName(commit.Subject)
		grouped[group] = append(grouped[group], commit)
	}
	out := make([]Section, 0, len(order))
	for _, name := range order {
		if len(grouped[name]) != 0 {
			out = append(out, Section{Name: name, Commits: grouped[name]})
		}
	}
	return out
}

func sectionName(subject string) string {
	prefix := strings.ToLower(strings.TrimSpace(subject))
	if before, _, ok := strings.Cut(prefix, ":"); ok {
		if beforeScope, _, ok := strings.Cut(before, "("); ok {
			before = beforeScope
		}
		switch before {
		case "feat":
			return "Features"
		case "fix":
			return "Fixes"
		case "security", "sec":
			return "Security"
		case "perf":
			return "Performance"
		case "docs", "doc":
			return "Documentation"
		case "test", "tests":
			return "Tests"
		case "refactor":
			return "Refactors"
		case "build", "ci":
			return "Build and CI"
		case "chore":
			return "Chores"
		}
	}
	return "Other"
}

func latestTag(workspace string) (string, error) {
	tag, err := git(workspace, "describe", "--tags", "--abbrev=0")
	if err != nil {
		return "", err
	}
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "", errors.New("no tags")
	}
	return tag, nil
}

func git(workspace string, args ...string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", err
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(data))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return "", fmt.Errorf("%s", text)
	}
	return text, nil
}
