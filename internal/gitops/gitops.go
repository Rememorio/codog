package gitops

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type CommitOptions struct {
	All     bool
	Message string
}

type CommitResult struct {
	Commit  string `json:"commit"`
	Summary string `json:"summary"`
	Output  string `json:"output,omitempty"`
}

type StashPushOptions struct {
	Message          string
	IncludeUntracked bool
}

func Status(workspace string) (string, error) {
	return git(workspace, "status", "--short", "--branch")
}

func Diff(workspace string, staged bool) (string, error) {
	args := []string{"diff"}
	if staged {
		args = append(args, "--cached")
	}
	return git(workspace, args...)
}

func Log(workspace string, limit int) (string, error) {
	if limit <= 0 {
		limit = 20
	}
	return git(workspace, "log", "--oneline", "--decorate", fmt.Sprintf("--max-count=%d", limit))
}

func Changelog(workspace string, limit int) (string, error) {
	if limit <= 0 {
		limit = 10
	}
	return git(workspace, "log", "--stat", "--decorate", fmt.Sprintf("--max-count=%d", limit))
}

func StashList(workspace string) (string, error) {
	return git(workspace, "stash", "list")
}

func StashPush(workspace string, options StashPushOptions) (string, error) {
	args := []string{"stash", "push"}
	if options.IncludeUntracked {
		args = append(args, "--include-untracked")
	}
	if strings.TrimSpace(options.Message) != "" {
		args = append(args, "-m", strings.TrimSpace(options.Message))
	}
	return git(workspace, args...)
}

func StashApply(workspace string, ref string) (string, error) {
	args := []string{"stash", "apply"}
	if strings.TrimSpace(ref) != "" {
		args = append(args, strings.TrimSpace(ref))
	}
	return git(workspace, args...)
}

func StashPop(workspace string, ref string) (string, error) {
	args := []string{"stash", "pop"}
	if strings.TrimSpace(ref) != "" {
		args = append(args, strings.TrimSpace(ref))
	}
	return git(workspace, args...)
}

func Root(workspace string) (string, error) {
	return git(workspace, "rev-parse", "--show-toplevel")
}

func Branch(workspace string) (string, error) {
	branch, err := git(workspace, "branch", "--show-current")
	if err == nil && strings.TrimSpace(branch) != "" {
		return branch, nil
	}
	return git(workspace, "rev-parse", "--short", "HEAD")
}

func Head(workspace string) (string, error) {
	return git(workspace, "rev-parse", "--short", "HEAD")
}

func Blame(workspace string, path string, line int) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("blame file is required")
	}
	args := []string{"blame"}
	if line > 0 {
		args = append(args, "-L", fmt.Sprintf("%d,%d", line, line))
	}
	args = append(args, "--", path)
	return git(workspace, args...)
}

func Commit(workspace string, options CommitOptions) (CommitResult, error) {
	message := strings.TrimSpace(options.Message)
	if message == "" {
		return CommitResult{}, errors.New("commit message is required")
	}
	if options.All {
		if _, err := git(workspace, "add", "-A"); err != nil {
			return CommitResult{}, err
		}
	}
	staged, err := git(workspace, "diff", "--cached", "--name-only")
	if err != nil {
		return CommitResult{}, err
	}
	if strings.TrimSpace(staged) == "" {
		return CommitResult{}, errors.New("no staged changes to commit")
	}
	output, err := git(workspace, "commit", "-m", message)
	if err != nil {
		return CommitResult{}, err
	}
	commit, err := git(workspace, "rev-parse", "--short", "HEAD")
	if err != nil {
		return CommitResult{}, err
	}
	summary, err := git(workspace, "show", "--stat", "--oneline", "--no-renames", "--format=%h %s", "HEAD")
	if err != nil {
		return CommitResult{}, err
	}
	return CommitResult{Commit: commit, Summary: summary, Output: output}, nil
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
