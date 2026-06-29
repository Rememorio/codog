package bughunt

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
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Severity   string `json:"severity"`
	Rule       string `json:"rule"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
	Text       string `json:"text,omitempty"`
}

type Report struct {
	Kind     string    `json:"kind"`
	Scope    string    `json:"scope"`
	Status   string    `json:"status"`
	Total    int       `json:"total"`
	Findings []Finding `json:"findings"`
}

type Options struct {
	Scope string
	Limit int
}

type rule struct {
	id         string
	severity   string
	message    string
	suggestion string
	pattern    *regexp.Regexp
	goOnly     bool
}

var rules = []rule{
	{
		id:         "ignored-return-value",
		severity:   "medium",
		message:    "A returned value is discarded with `_`; this often hides errors or important state.",
		suggestion: "Handle the returned error/value explicitly, or add a short comment explaining why it is intentionally ignored.",
		pattern:    regexp.MustCompile(`,\s*_\s*(:=|=)`),
		goOnly:     true,
	},
	{
		id:         "panic-in-runtime-path",
		severity:   "medium",
		message:    "panic is used outside tests; this can crash an interactive agent session.",
		suggestion: "Return an error or convert the panic into a controlled diagnostic unless this is process startup code.",
		pattern:    regexp.MustCompile(`\bpanic\s*\(`),
		goOnly:     true,
	},
	{
		id:         "process-exit",
		severity:   "medium",
		message:    "os.Exit terminates the process immediately and skips defers.",
		suggestion: "Return an error to the caller and let the CLI boundary decide the exit code.",
		pattern:    regexp.MustCompile(`\bos\.Exit\s*\(`),
		goOnly:     true,
	},
	{
		id:         "unchecked-type-assertion",
		severity:   "medium",
		message:    "Single-result type assertion can panic when the dynamic type differs.",
		suggestion: "Use the comma-ok form and handle the false case.",
		pattern:    regexp.MustCompile(`\.\([^)]+\)`),
		goOnly:     true,
	},
	{
		id:         "empty-recover",
		severity:   "high",
		message:    "recover result appears to be ignored, which can hide panics.",
		suggestion: "Log or return the recovered panic value so failure is observable.",
		pattern:    regexp.MustCompile(`\brecover\s*\(\s*\)`),
		goOnly:     true,
	},
	{
		id:         "curl-pipe-shell",
		severity:   "high",
		message:    "Remote content is piped directly into a shell.",
		suggestion: "Download, verify, and inspect the script before execution.",
		pattern:    regexp.MustCompile(`(?i)\b(curl|wget)\b.*\|\s*(sh|bash)\b`),
	},
}

func Scan(workspace string, options Options) (Report, error) {
	if workspace == "" {
		workspace = "."
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return Report{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Report{}, err
	}
	scopeRoot, scopeLabel, err := resolveScope(root, options.Scope)
	if err != nil {
		return Report{}, err
	}
	limit := options.Limit
	if limit <= 0 {
		limit = 200
	}
	var findings []Finding
	err = filepath.WalkDir(scopeRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(findings) >= limit {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if path != scopeRoot && ignoredDir(entry.Name()) {
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
		if findings[i].Severity == findings[j].Severity {
			if findings[i].Path == findings[j].Path {
				return findings[i].Line < findings[j].Line
			}
			return findings[i].Path < findings[j].Path
		}
		return severityRank(findings[i].Severity) < severityRank(findings[j].Severity)
	})
	status := "ok"
	if len(findings) != 0 {
		status = "findings"
	}
	return Report{Kind: "bughunter", Scope: scopeLabel, Status: status, Total: len(findings), Findings: findings}, nil
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Bughunter")
	fmt.Fprintf(w, "  Scope            %s\n", report.Scope)
	fmt.Fprintf(w, "  Status           %s\n", report.Status)
	fmt.Fprintf(w, "  Findings         %d\n", report.Total)
	for _, finding := range report.Findings {
		fmt.Fprintf(w, "  %s:%d\t%s\t%s\t%s\n", finding.Path, finding.Line, finding.Severity, finding.Rule, finding.Message)
		if finding.Text != "" {
			fmt.Fprintf(w, "    %s\n", finding.Text)
		}
		if finding.Suggestion != "" {
			fmt.Fprintf(w, "    fix: %s\n", finding.Suggestion)
		}
	}
}

func resolveScope(root string, scope string) (string, string, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(scope) == "" {
		return root, ".", nil
	}
	candidate := scope
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("scope escapes workspace: %s", scope)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", "", err
	}
	rel, err = filepath.Rel(root, resolved)
	if err != nil {
		return "", "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("scope escapes workspace: %s", scope)
	}
	return resolved, filepath.ToSlash(rel), nil
}

func scanFile(root string, path string, limit int) ([]Finding, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > maxFileBytes {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(path))
	if !supportedExt(ext) {
		return nil, nil
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
	rel = filepath.ToSlash(rel)
	goFile := ext == ".go" && !strings.HasSuffix(path, "_test.go")
	var findings []Finding
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	inLoop := false
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "for ") || trimmed == "for {" || strings.HasPrefix(trimmed, "for{") {
			inLoop = true
		}
		for _, next := range scanLine(rel, lineNo, line, goFile, inLoop) {
			findings = append(findings, next)
			if len(findings) >= limit {
				return findings, nil
			}
		}
		if trimmed == "}" {
			inLoop = false
		}
	}
	return findings, scanner.Err()
}

func scanLine(path string, lineNo int, line string, goFile bool, inLoop bool) []Finding {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "//") {
		return nil
	}
	var findings []Finding
	for _, rule := range rules {
		if rule.goOnly && !goFile {
			continue
		}
		if rule.id == "unchecked-type-assertion" && strings.Contains(line, ", ok") {
			continue
		}
		if rule.id == "empty-recover" && (strings.Contains(line, ":=") || strings.Contains(line, "=")) {
			continue
		}
		if rule.pattern.MatchString(line) {
			findings = append(findings, Finding{
				Path:       path,
				Line:       lineNo,
				Severity:   rule.severity,
				Rule:       rule.id,
				Message:    rule.message,
				Suggestion: rule.suggestion,
				Text:       trimmed,
			})
		}
	}
	if goFile && inLoop && strings.HasPrefix(trimmed, "defer ") {
		findings = append(findings, Finding{
			Path:       path,
			Line:       lineNo,
			Severity:   "medium",
			Rule:       "defer-in-loop",
			Message:    "defer inside a loop delays cleanup until the outer function returns.",
			Suggestion: "Move loop body into a helper function or close resources explicitly each iteration.",
			Text:       trimmed,
		})
	}
	if goFile && inLoop && strings.Contains(trimmed, "go func()") {
		findings = append(findings, Finding{
			Path:       path,
			Line:       lineNo,
			Severity:   "medium",
			Rule:       "goroutine-loop-capture",
			Message:    "goroutine closure inside a loop may capture changing loop variables.",
			Suggestion: "Pass loop variables as parameters to the closure.",
			Text:       trimmed,
		})
	}
	return findings
}

func supportedExt(ext string) bool {
	switch ext {
	case ".go", ".sh", ".bash", ".zsh", ".js", ".ts", ".tsx", ".jsx", ".py", ".rb":
		return true
	default:
		return false
	}
}

func ignoredDir(name string) bool {
	switch name {
	case ".git", ".codog", "node_modules", "vendor", "dist", "build", "target", "coverage":
		return true
	default:
		return false
	}
}

func severityRank(severity string) int {
	switch severity {
	case "high":
		return 0
	case "medium":
		return 1
	default:
		return 2
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
