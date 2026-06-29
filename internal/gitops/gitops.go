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
