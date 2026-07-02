package securityreview

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxFileBytes = 1024 * 1024

type Finding struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Rule     string `json:"rule"`
	Message  string `json:"message"`
	Text     string `json:"text,omitempty"`
}

type Report struct {
	Kind     string    `json:"kind"`
	Action   string    `json:"action"`
	Status   string    `json:"status"`
	Total    int       `json:"total"`
	Findings []Finding `json:"findings"`
}

type rule struct {
	id       string
	severity string
	message  string
	pattern  *regexp.Regexp
}

var rules = []rule{
	{
		id:       "hardcoded-secret",
		severity: "high",
		message:  "Possible hardcoded credential or token.",
		pattern:  regexp.MustCompile(`(?i)(api[_-]?key|secret|token|password)\s*[:=]\s*["'][^"']{8,}["']`),
	},
	{
		id:       "pipe-to-shell",
		severity: "high",
		message:  "Remote content appears to be piped into a shell.",
		pattern:  regexp.MustCompile(`(?i)\bcurl\b.*\|\s*(sh|bash)\b`),
	},
	{
		id:       "dangerous-rm",
		severity: "high",
		message:  "Dangerous recursive delete command.",
		pattern:  regexp.MustCompile(`\brm\s+-[a-zA-Z]*r[a-zA-Z]*f[a-zA-Z]*\s+(/|\*)`),
	},
	{
		id:       "world-writable",
		severity: "medium",
		message:  "World-writable permissions are being granted.",
		pattern:  regexp.MustCompile(`\bchmod\s+777\b`),
	},
	{
		id:       "dynamic-eval",
		severity: "medium",
		message:  "Dynamic eval-like execution needs review.",
		pattern:  regexp.MustCompile(`(?i)\beval\s*\(`),
	},
}

func Review(workspace string, limit int) (Report, error) {
	if workspace == "" {
		workspace = "."
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return Report{}, err
	}
	if limit <= 0 {
		limit = 200
	}
	var findings []Finding
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(findings) >= limit {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if path != root && ignoredDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		next, err := scanFile(root, path, limit-len(findings))
		if err != nil {
			return nil
		}
		findings = append(findings, next...)
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Path == findings[j].Path {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Path < findings[j].Path
	})
	status := "ok"
	if len(findings) != 0 {
		status = "findings"
	}
	return Report{Kind: "security_review", Action: "scan", Status: status, Total: len(findings), Findings: findings}, nil
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Security Review")
	fmt.Fprintf(w, "  Status           %s\n", report.Status)
	fmt.Fprintf(w, "  Findings         %d\n", report.Total)
	for _, finding := range report.Findings {
		fmt.Fprintf(w, "  %s:%d\t%s\t%s\t%s\n", finding.Path, finding.Line, finding.Severity, finding.Rule, finding.Message)
		if finding.Text != "" {
			fmt.Fprintf(w, "    %s\n", finding.Text)
		}
	}
}

func scanFile(root, path string, limit int) ([]Finding, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > maxFileBytes {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if bytes.Contains(data[:min(len(data), 4096)], []byte{0}) {
		return nil, nil
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	var findings []Finding
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		for _, rule := range rules {
			if rule.pattern.MatchString(line) {
				findings = append(findings, Finding{
					Path:     filepath.ToSlash(rel),
					Line:     lineNo,
					Severity: rule.severity,
					Rule:     rule.id,
					Message:  rule.message,
					Text:     strings.TrimSpace(line),
				})
				if len(findings) >= limit {
					return findings, nil
				}
			}
		}
	}
	return findings, scanner.Err()
}

func ignoredDir(name string) bool {
	switch name {
	case ".git", ".codog", "node_modules", "vendor", "dist", "build", "target":
		return true
	default:
		return false
	}
}
