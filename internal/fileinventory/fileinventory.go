package fileinventory

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Rememorio/codog/internal/gitignore"
)

const (
	maxScopeRiskSinks    = 20
	largeFileThreshold   = 1 << 20
	hugeFileThreshold    = 5 << 20
	scopeRiskStatusClean = "clean"
	scopeRiskStatusWarn  = "warn"
)

type Options struct {
	Path             string
	Glob             string
	Limit            int
	IncludeHidden    bool
	RespectGitignore bool
}

type Entry struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Ext   string `json:"ext,omitempty"`
	Depth int    `json:"depth"`
}

// ScopeRiskSink describes one path that may bloat context or token usage.
type ScopeRiskSink struct {
	Path           string `json:"path"`
	Kind           string `json:"kind"`
	Reason         string `json:"reason"`
	Size           int64  `json:"size,omitempty"`
	Recommendation string `json:"recommendation"`
}

// ScopeRisk summarizes token-risk preflight findings for a workspace scan.
type ScopeRisk struct {
	Status          string          `json:"status"`
	Level           string          `json:"level"`
	Summary         string          `json:"summary"`
	SinkCount       int             `json:"sink_count"`
	Sinks           []ScopeRiskSink `json:"sinks,omitempty"`
	Recommendations []string        `json:"recommendations,omitempty"`
}

type Report struct {
	Kind             string    `json:"kind"`
	Action           string    `json:"action"`
	Root             string    `json:"root"`
	Path             string    `json:"path,omitempty"`
	Glob             string    `json:"glob,omitempty"`
	Total            int       `json:"total"`
	Limit            int       `json:"limit"`
	Truncated        bool      `json:"truncated"`
	Bytes            int64     `json:"bytes"`
	RespectGitignore bool      `json:"respect_gitignore"`
	Files            []Entry   `json:"files"`
	ScopeRisk        ScopeRisk `json:"scope_risk"`
}

func Build(workspace string, opts Options) (Report, error) {
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
	start := root
	if strings.TrimSpace(opts.Path) != "" {
		start, err = scopedPath(root, opts.Path)
		if err != nil {
			return Report{}, err
		}
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}
	report := Report{
		Kind:             "files",
		Action:           "list",
		Root:             root,
		Path:             displayPath(root, start),
		Glob:             opts.Glob,
		Limit:            limit,
		RespectGitignore: opts.RespectGitignore,
		Files:            []Entry{},
		ScopeRisk:        ScopeRisk{Status: scopeRiskStatusClean, Level: "low"},
	}
	var ignoreMatcher *gitignore.Matcher
	if opts.RespectGitignore {
		ignoreMatcher, err = gitignore.New(root)
		if err != nil {
			return Report{}, err
		}
	}
	collector := inventoryCollector{root: root, start: start, opts: opts, limit: limit, report: &report, ignore: ignoreMatcher}
	err = filepath.WalkDir(start, collector.visit)
	if err != nil {
		return Report{}, err
	}
	sort.Slice(report.Files, func(i, j int) bool { return report.Files[i].Path < report.Files[j].Path })
	if report.Total > len(report.Files) {
		report.Truncated = true
	}
	finalizeScopeRisk(&report.ScopeRisk)
	return report, nil
}

type inventoryCollector struct {
	root   string
	start  string
	opts   Options
	limit  int
	report *Report
	ignore *gitignore.Matcher
}

func (c *inventoryCollector) visit(path string, entry os.DirEntry, walkErr error) error {
	if walkErr != nil || path == c.start {
		return walkErr
	}
	rel := displayPath(c.root, path)
	if entry.IsDir() {
		return c.visitDir(path, rel, entry)
	}
	if !c.opts.IncludeHidden && strings.HasPrefix(entry.Name(), ".") || c.ignore != nil && c.ignore.Ignored(path, false) {
		return nil
	}
	return c.visitFile(path, rel, entry)
}

func (c *inventoryCollector) visitDir(path string, rel string, entry os.DirEntry) error {
	if sink, ok := scopeRiskSinkForDir(rel, entry.Name()); ok {
		addScopeRiskSink(&c.report.ScopeRisk, sink)
	}
	if skipDir(entry.Name(), c.opts.IncludeHidden) || c.ignore != nil && c.ignore.Ignored(path, true) {
		return filepath.SkipDir
	}
	return nil
}

func (c *inventoryCollector) visitFile(path string, rel string, entry os.DirEntry) error {
	info, err := entry.Info()
	if err != nil {
		return err
	}
	if sink, ok := scopeRiskSinkForFile(rel, entry.Name(), info.Size()); ok {
		addScopeRiskSink(&c.report.ScopeRisk, sink)
	}
	if c.opts.Glob != "" && !matchesGlob(c.opts.Glob, rel, filepath.Base(path)) {
		return nil
	}
	c.report.Total++
	c.report.Bytes += info.Size()
	if len(c.report.Files) >= c.limit {
		c.report.Truncated = true
		return nil
	}
	c.report.Files = append(c.report.Files, Entry{
		Path: rel, Size: info.Size(), Ext: strings.TrimPrefix(filepath.Ext(path), "."), Depth: depth(rel),
	})
	return nil
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Files")
	fmt.Fprintf(w, "  Root             %s\n", report.Root)
	if report.Path != "" && report.Path != "." {
		fmt.Fprintf(w, "  Path             %s\n", report.Path)
	}
	if report.Glob != "" {
		fmt.Fprintf(w, "  Glob             %s\n", report.Glob)
	}
	fmt.Fprintf(w, "  Total            %d\n", report.Total)
	fmt.Fprintf(w, "  Listed           %d\n", len(report.Files))
	fmt.Fprintf(w, "  Bytes            %d\n", report.Bytes)
	fmt.Fprintf(w, "  Respect gitignore %t\n", report.RespectGitignore)
	fmt.Fprintf(w, "  Truncated        %t\n", report.Truncated)
	fmt.Fprintf(w, "  Scope risk       %s", report.ScopeRisk.Status)
	if report.ScopeRisk.Level != "" {
		fmt.Fprintf(w, " (%s)", report.ScopeRisk.Level)
	}
	fmt.Fprintln(w)
	if report.ScopeRisk.Summary != "" {
		fmt.Fprintf(w, "  Scope summary    %s\n", report.ScopeRisk.Summary)
	}
	if len(report.ScopeRisk.Sinks) > 0 {
		fmt.Fprintln(w, "  Token sinks")
		for _, sink := range report.ScopeRisk.Sinks {
			fmt.Fprintf(w, "    %-18s %-18s %s\n", sink.Kind, sink.Path, sink.Reason)
		}
	}
	for _, recommendation := range report.ScopeRisk.Recommendations {
		fmt.Fprintf(w, "  Scope recommendation %s\n", recommendation)
	}
	if len(report.Files) == 0 {
		return
	}
	fmt.Fprintln(w)
	for _, file := range report.Files {
		fmt.Fprintf(w, "  %s\t%d", file.Path, file.Size)
		if file.Ext != "" {
			fmt.Fprintf(w, "\t%s", file.Ext)
		}
		fmt.Fprintln(w)
	}
}

func scopedPath(root, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", errors.New("path is required")
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if !within(root, resolved) {
		return "", fmt.Errorf("path escapes workspace: %s", requested)
	}
	return resolved, nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func displayPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func matchesGlob(pattern, rel, base string) bool {
	if ok, _ := filepath.Match(pattern, rel); ok {
		return true
	}
	ok, _ := filepath.Match(pattern, base)
	return ok
}

func skipDir(name string, includeHidden bool) bool {
	switch name {
	case ".git", "node_modules", "vendor", "target", "dist", "build", "coverage", ".next", ".cache", "logs", "dumps", "generated", "reports":
		return true
	}
	return !includeHidden && strings.HasPrefix(name, ".")
}

func scopeRiskSinkForDir(rel string, name string) (ScopeRiskSink, bool) {
	kind := ""
	reason := ""
	recommendation := ""
	switch name {
	case "node_modules", "vendor":
		kind = "vendored_dependency"
		reason = "dependency directory commonly contains many files"
		recommendation = "Run Codog from a narrower source directory or keep dependency directories ignored."
	case "dist", "build", "target", ".next", "generated", "reports":
		kind = "generated_artifact"
		reason = "generated output can dominate file discovery and prompt context"
		recommendation = "Ignore generated output or start from the source package instead."
	case "coverage":
		kind = "coverage_artifact"
		reason = "coverage output is usually generated and high-volume"
		recommendation = "Ignore coverage output unless the current task needs it."
	case "logs", "dumps", ".cache":
		kind = "runtime_artifact"
		reason = "runtime artifacts are noisy and rarely useful as coding context"
		recommendation = "Ignore or clean runtime artifacts before starting a broad session."
	default:
		return ScopeRiskSink{}, false
	}
	return ScopeRiskSink{Path: rel, Kind: kind, Reason: reason, Recommendation: recommendation}, true
}

func scopeRiskSinkForFile(rel string, name string, size int64) (ScopeRiskSink, bool) {
	lower := strings.ToLower(name)
	ext := strings.ToLower(filepath.Ext(lower))
	if size >= hugeFileThreshold {
		return ScopeRiskSink{
			Path:           rel,
			Kind:           "huge_file",
			Reason:         "large file is likely to burn context quickly",
			Size:           size,
			Recommendation: "Narrow the workspace or ignore large generated/binary files.",
		}, true
	}
	if size >= largeFileThreshold {
		return ScopeRiskSink{
			Path:           rel,
			Kind:           "large_file",
			Reason:         "large file may be expensive when included in context",
			Size:           size,
			Recommendation: "Keep large files out of broad prompts unless they are required.",
		}, true
	}
	switch ext {
	case ".log", ".dump", ".dmp", ".trace", ".har":
		return ScopeRiskSink{
			Path:           rel,
			Kind:           "log_or_dump",
			Reason:         "logs and dumps are noisy token sinks",
			Size:           size,
			Recommendation: "Ignore logs and dumps or attach only the relevant excerpt.",
		}, true
	case ".db", ".sqlite", ".sqlite3", ".sql":
		return ScopeRiskSink{
			Path:           rel,
			Kind:           "database_or_dump",
			Reason:         "database and dump files are poor broad-context inputs",
			Size:           size,
			Recommendation: "Keep database and dump files ignored; summarize excerpts explicitly when needed.",
		}, true
	case ".zip", ".tar", ".gz", ".tgz", ".rar", ".7z":
		return ScopeRiskSink{
			Path:           rel,
			Kind:           "archive",
			Reason:         "archives are opaque and can hide large generated content",
			Size:           size,
			Recommendation: "Exclude archives from workspace context unless unpacking is the task.",
		}, true
	}
	if strings.HasSuffix(lower, ".min.js") || strings.HasSuffix(lower, ".map") {
		return ScopeRiskSink{
			Path:           rel,
			Kind:           "generated_artifact",
			Reason:         "minified or map output is generated and token-heavy",
			Size:           size,
			Recommendation: "Prefer source files and ignore generated frontend artifacts.",
		}, true
	}
	return ScopeRiskSink{}, false
}

func addScopeRiskSink(risk *ScopeRisk, sink ScopeRiskSink) {
	risk.SinkCount++
	if len(risk.Sinks) < maxScopeRiskSinks {
		risk.Sinks = append(risk.Sinks, sink)
	}
}

func finalizeScopeRisk(risk *ScopeRisk) {
	if risk.SinkCount == 0 {
		risk.Status = scopeRiskStatusClean
		risk.Level = "low"
		risk.Summary = "Workspace looks clean for a first pass."
		risk.Recommendations = []string{"Keep starting from the smallest useful directory for large monorepos."}
		return
	}
	risk.Status = scopeRiskStatusWarn
	risk.Level = "medium"
	for _, sink := range risk.Sinks {
		if sink.Kind == "vendored_dependency" || sink.Kind == "huge_file" || sink.Kind == "generated_artifact" {
			risk.Level = "high"
			break
		}
	}
	if risk.SinkCount >= 5 {
		risk.Level = "high"
	}
	risk.Summary = fmt.Sprintf("Workspace is likely to burn tokens fast: %d high-risk path(s) found.", risk.SinkCount)
	risk.Recommendations = dedupeStrings([]string{
		"Start from a narrower package or service directory when possible.",
		"Add ignore patterns for generated output, dependency caches, logs, dumps, and archives.",
		"Clean or move large runtime artifacts before broad prompt flows.",
	})
}

func dedupeStrings(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func depth(rel string) int {
	rel = strings.Trim(rel, "/")
	if rel == "" || rel == "." {
		return 0
	}
	return strings.Count(rel, "/") + 1
}
