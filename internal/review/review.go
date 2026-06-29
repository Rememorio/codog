package review

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Rememorio/codog/internal/securityreview"
)

type Options struct {
	Base   string
	Staged bool
	Limit  int
}

type FileChange struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

type Summary struct {
	Files     int `json:"files"`
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
}

type Report struct {
	Kind             string                   `json:"kind"`
	Action           string                   `json:"action"`
	Status           string                   `json:"status"`
	Base             string                   `json:"base,omitempty"`
	Staged           bool                     `json:"staged,omitempty"`
	Summary          Summary                  `json:"summary"`
	Files            []FileChange             `json:"files"`
	SecurityFindings []securityreview.Finding `json:"security_findings,omitempty"`
	Signals          []string                 `json:"signals,omitempty"`
}

func Run(workspace string, options Options) (Report, error) {
	if workspace == "" {
		workspace = "."
	}
	if options.Limit <= 0 {
		options.Limit = 200
	}
	files, err := changedFiles(workspace, options)
	if err != nil {
		return Report{}, err
	}
	findings, err := changedSecurityFindings(workspace, files, options.Limit)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		Kind:             "review",
		Action:           "scan",
		Status:           "ok",
		Base:             options.Base,
		Staged:           options.Staged,
		Files:            files,
		SecurityFindings: findings,
	}
	for _, file := range files {
		report.Summary.Files++
		report.Summary.Additions += file.Additions
		report.Summary.Deletions += file.Deletions
	}
	if len(files) == 0 {
		report.Status = "clean"
		report.Signals = append(report.Signals, "no changed files")
	}
	if len(findings) != 0 {
		report.Status = "findings"
		report.Signals = append(report.Signals, "security findings in changed files")
	}
	return report, nil
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Review")
	fmt.Fprintf(w, "  Status           %s\n", report.Status)
	if report.Base != "" {
		fmt.Fprintf(w, "  Base             %s\n", report.Base)
	}
	if report.Staged {
		fmt.Fprintln(w, "  Scope            staged")
	}
	fmt.Fprintf(w, "  Files            %d\n", report.Summary.Files)
	fmt.Fprintf(w, "  Lines            +%d -%d\n", report.Summary.Additions, report.Summary.Deletions)
	if len(report.Files) != 0 {
		fmt.Fprintln(w, "Changed files")
		for _, file := range report.Files {
			fmt.Fprintf(w, "  %s\t+%d -%d\t%s\n", file.Status, file.Additions, file.Deletions, file.Path)
		}
	}
	if len(report.SecurityFindings) != 0 {
		fmt.Fprintln(w, "Security findings")
		for _, finding := range report.SecurityFindings {
			fmt.Fprintf(w, "  %s:%d\t%s\t%s\t%s\n", finding.Path, finding.Line, finding.Severity, finding.Rule, finding.Message)
		}
	}
	if len(report.Signals) != 0 {
		fmt.Fprintln(w, "Signals")
		for _, signal := range report.Signals {
			fmt.Fprintf(w, "  - %s\n", signal)
		}
	}
}

func changedFiles(workspace string, options Options) ([]FileChange, error) {
	nameStatus, err := git(workspace, append(diffBaseArgs(options), "--name-status")...)
	if err != nil {
		return nil, err
	}
	numstat, err := git(workspace, append(diffBaseArgs(options), "--numstat")...)
	if err != nil {
		return nil, err
	}
	stats := parseNumstat(numstat)
	files := parseNameStatus(nameStatus)
	for index := range files {
		if stat, ok := stats[files[index].Path]; ok {
			files[index].Additions = stat.Additions
			files[index].Deletions = stat.Deletions
		}
	}
	if !options.Staged && strings.TrimSpace(options.Base) == "" {
		untracked, err := untrackedFiles(workspace)
		if err != nil {
			return nil, err
		}
		files = append(files, untracked...)
	}
	return files, nil
}

func changedSecurityFindings(workspace string, files []FileChange, limit int) ([]securityreview.Finding, error) {
	if len(files) == 0 {
		return nil, nil
	}
	allowed := map[string]struct{}{}
	for _, file := range files {
		allowed[file.Path] = struct{}{}
	}
	report, err := securityreview.Review(workspace, limit)
	if err != nil {
		return nil, err
	}
	var findings []securityreview.Finding
	for _, finding := range report.Findings {
		if _, ok := allowed[finding.Path]; ok {
			findings = append(findings, finding)
		}
	}
	return findings, nil
}

func diffBaseArgs(options Options) []string {
	args := []string{"diff"}
	if options.Staged {
		args = append(args, "--cached")
	}
	if strings.TrimSpace(options.Base) != "" {
		args = append(args, strings.TrimSpace(options.Base))
	} else if !options.Staged {
		args = append(args, "HEAD")
	}
	return args
}

func parseNameStatus(raw string) []FileChange {
	var files []FileChange
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		path := parts[len(parts)-1]
		files = append(files, FileChange{Status: statusName(parts[0]), Path: filepath.ToSlash(path)})
	}
	return files
}

func parseNumstat(raw string) map[string]FileChange {
	stats := map[string]FileChange{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		additions, _ := strconv.Atoi(parts[0])
		deletions, _ := strconv.Atoi(parts[1])
		path := filepath.ToSlash(parts[len(parts)-1])
		stats[path] = FileChange{Path: path, Additions: additions, Deletions: deletions}
	}
	return stats
}

func untrackedFiles(workspace string) ([]FileChange, error) {
	raw, err := git(workspace, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	var files []FileChange
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		path := strings.TrimSpace(line)
		if path == "" {
			continue
		}
		files = append(files, FileChange{
			Path:      filepath.ToSlash(path),
			Status:    "added",
			Additions: countFileLines(filepath.Join(workspace, filepath.FromSlash(path))),
		})
	}
	return files, nil
}

func countFileLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return 0
	}
	body := strings.TrimRight(string(data), "\n")
	if body == "" {
		return 1
	}
	return strings.Count(body, "\n") + 1
}

func statusName(code string) string {
	if code == "" {
		return "modified"
	}
	switch code[0] {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	default:
		return "modified"
	}
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
		return "", errors.New(text)
	}
	return text, nil
}
