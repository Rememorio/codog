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

type BranchInfo struct {
	Name     string `json:"name"`
	Current  bool   `json:"current"`
	Upstream string `json:"upstream,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Subject  string `json:"subject,omitempty"`
}

type BranchList struct {
	Current  string       `json:"current"`
	Branches []BranchInfo `json:"branches"`
}

type BranchFreshness struct {
	Branch       string   `json:"branch"`
	Base         string   `json:"base"`
	Status       string   `json:"status"`
	Fresh        bool     `json:"fresh"`
	Ahead        int      `json:"ahead"`
	Behind       int      `json:"behind"`
	MissingFixes []string `json:"missing_fixes,omitempty"`
}

type TagInfo struct {
	Name    string `json:"name"`
	Commit  string `json:"commit,omitempty"`
	Subject string `json:"subject,omitempty"`
}

type StashPushOptions struct {
	Message          string
	IncludeUntracked bool
}

func Status(workspace string) (string, error) {
	return git(workspace, "status", "--short", "--branch")
}

func Run(workspace string, args ...string) (string, error) {
	return git(workspace, args...)
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

func ListBranches(workspace string) (BranchList, error) {
	current, err := Branch(workspace)
	if err != nil {
		return BranchList{}, err
	}
	raw, err := git(workspace, "branch", "--format=%(HEAD)%00%(refname:short)%00%(upstream:short)%00%(objectname:short)%00%(subject)")
	if err != nil {
		return BranchList{}, err
	}
	var branches []BranchInfo
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		for len(parts) < 5 {
			parts = append(parts, "")
		}
		name := strings.TrimSpace(parts[1])
		if name == "" {
			continue
		}
		branches = append(branches, BranchInfo{
			Name:     name,
			Current:  strings.TrimSpace(parts[0]) == "*",
			Upstream: strings.TrimSpace(parts[2]),
			Commit:   strings.TrimSpace(parts[3]),
			Subject:  strings.TrimSpace(parts[4]),
		})
	}
	return BranchList{Current: strings.TrimSpace(current), Branches: branches}, nil
}

func CheckBranchFreshness(workspace, branch, base string) (BranchFreshness, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		current, err := Branch(workspace)
		if err != nil {
			return BranchFreshness{}, err
		}
		branch = current
	}
	base = strings.TrimSpace(base)
	if base == "" {
		base = "main"
	}
	if err := validateSafeRef(branch, "branch"); err != nil {
		return BranchFreshness{}, err
	}
	if err := validateSafeRef(base, "base"); err != nil {
		return BranchFreshness{}, err
	}
	if _, err := git(workspace, "rev-parse", "--verify", branch+"^{commit}"); err != nil {
		return BranchFreshness{}, fmt.Errorf("branch %q does not resolve to a commit: %w", branch, err)
	}
	if _, err := git(workspace, "rev-parse", "--verify", base+"^{commit}"); err != nil {
		return BranchFreshness{}, fmt.Errorf("base %q does not resolve to a commit: %w", base, err)
	}
	behind, err := revListCount(workspace, branch+".."+base)
	if err != nil {
		return BranchFreshness{}, err
	}
	ahead, err := revListCount(workspace, base+".."+branch)
	if err != nil {
		return BranchFreshness{}, err
	}
	missing, err := logSubjects(workspace, branch+".."+base)
	if err != nil {
		return BranchFreshness{}, err
	}
	status := "fresh"
	if behind > 0 && ahead > 0 {
		status = "diverged"
	} else if behind > 0 {
		status = "stale"
	}
	return BranchFreshness{
		Branch:       branch,
		Base:         base,
		Status:       status,
		Fresh:        behind == 0,
		Ahead:        ahead,
		Behind:       behind,
		MissingFixes: missing,
	}, nil
}

func CreateBranch(workspace, name, startPoint string, checkout bool) (string, error) {
	name = strings.TrimSpace(name)
	if err := validateBranchName(workspace, name); err != nil {
		return "", err
	}
	startPoint = strings.TrimSpace(startPoint)
	args := []string{"branch", name}
	if checkout {
		args = []string{"switch", "-c", name}
	}
	if startPoint != "" {
		args = append(args, startPoint)
	}
	return git(workspace, args...)
}

func SwitchBranch(workspace, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("branch name is required")
	}
	return git(workspace, "switch", name)
}

func DeleteBranch(workspace, name string, force bool) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("branch name is required")
	}
	flag := "-d"
	if force {
		flag = "-D"
	}
	return git(workspace, "branch", flag, name)
}

func RenameBranch(workspace, oldName, newName string) (string, error) {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if err := validateBranchName(workspace, newName); err != nil {
		return "", err
	}
	args := []string{"branch", "-m"}
	if oldName != "" {
		args = append(args, oldName)
	}
	args = append(args, newName)
	return git(workspace, args...)
}

func ListTags(workspace, pattern string, limit int) ([]TagInfo, error) {
	args := []string{"tag", "--list"}
	if strings.TrimSpace(pattern) != "" {
		args = append(args, strings.TrimSpace(pattern))
	}
	args = append(args, "--sort=-creatordate", "--format=%(refname:short)%00%(objectname:short)%00%(subject)")
	raw, err := git(workspace, args...)
	if err != nil {
		return nil, err
	}
	var tags []TagInfo
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		for len(parts) < 3 {
			parts = append(parts, "")
		}
		tags = append(tags, TagInfo{
			Name:    strings.TrimSpace(parts[0]),
			Commit:  strings.TrimSpace(parts[1]),
			Subject: strings.TrimSpace(parts[2]),
		})
		if limit > 0 && len(tags) >= limit {
			break
		}
	}
	return tags, nil
}

func CreateTag(workspace, name, ref, message string) (string, error) {
	name = strings.TrimSpace(name)
	if err := validateTagName(workspace, name); err != nil {
		return "", err
	}
	args := []string{"tag"}
	if strings.TrimSpace(message) != "" {
		args = append(args, "-a", name, "-m", strings.TrimSpace(message))
	} else {
		args = append(args, name)
	}
	if strings.TrimSpace(ref) != "" {
		args = append(args, strings.TrimSpace(ref))
	}
	return git(workspace, args...)
}

func DeleteTag(workspace, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("tag name is required")
	}
	return git(workspace, "tag", "-d", name)
}

func ShowTag(workspace, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("tag name is required")
	}
	return git(workspace, "show", "--stat", "--oneline", "--decorate", "--no-renames", name)
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

func validateBranchName(workspace, name string) error {
	if name == "" {
		return errors.New("branch name is required")
	}
	if err := validateSafeRef(name, "branch name"); err != nil {
		return err
	}
	if _, err := git(workspace, "check-ref-format", "--branch", name); err != nil {
		return fmt.Errorf("invalid branch name %q: %w", name, err)
	}
	return nil
}

func validateTagName(workspace, name string) error {
	if name == "" {
		return errors.New("tag name is required")
	}
	if err := validateSafeRef(name, "tag name"); err != nil {
		return err
	}
	if _, err := git(workspace, "check-ref-format", "refs/tags/"+name); err != nil {
		return fmt.Errorf("invalid tag name %q: %w", name, err)
	}
	return nil
}

func validateSafeRef(ref, field string) error {
	if strings.TrimSpace(ref) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.HasPrefix(ref, "-") || strings.ContainsRune(ref, '\x00') {
		return fmt.Errorf("%s is not a safe git ref", field)
	}
	return nil
}

func revListCount(workspace, revRange string) (int, error) {
	out, err := git(workspace, "rev-list", "--count", revRange)
	if err != nil {
		return 0, err
	}
	var count int
	if _, err := fmt.Sscanf(strings.TrimSpace(out), "%d", &count); err != nil {
		return 0, fmt.Errorf("parse git rev-list count: %w", err)
	}
	return count, nil
}

func logSubjects(workspace, revRange string) ([]string, error) {
	out, err := git(workspace, "log", "--format=%s", revRange)
	if err != nil {
		return nil, err
	}
	var subjects []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			subjects = append(subjects, line)
		}
	}
	return subjects, nil
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
