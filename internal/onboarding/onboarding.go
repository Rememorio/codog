// Package onboarding inspects a workspace and reports setup gaps for first-run
// guidance.
package onboarding

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Options configures workspace onboarding analysis.
type Options struct {
	Workspace string
}

// Language reports how many files were detected for one language family.
type Language struct {
	Name  string `json:"name"`
	Files int    `json:"files"`
}

// Check describes one onboarding prerequisite or recommendation signal.
type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

// IgnoreBehavior documents how one ignore-file family affects Codog surfaces.
type IgnoreBehavior struct {
	File      string   `json:"file"`
	HonoredBy []string `json:"honored_by"`
	Notes     string   `json:"notes"`
}

// ScopeGuidance explains how to keep repository context small before a first
// provider-backed run.
type ScopeGuidance struct {
	Recommendation         string           `json:"recommendation"`
	RiskStatus             string           `json:"risk_status"`
	RiskLevel              string           `json:"risk_level"`
	RiskSummary            string           `json:"risk_summary"`
	HeavyDirectoryPatterns []string         `json:"heavy_directory_patterns"`
	IgnoreBehaviors        []IgnoreBehavior `json:"ignore_behaviors"`
	IgnoreFiles            []string         `json:"ignore_files,omitempty"`
	HeavyPaths             []string         `json:"heavy_paths,omitempty"`
}

// Report is the stable JSON payload returned by workspace onboarding analysis.
type Report struct {
	Kind             string        `json:"kind"`
	Action           string        `json:"action"`
	Status           string        `json:"status"`
	Workspace        string        `json:"workspace"`
	HasReadme        bool          `json:"has_readme"`
	HasTests         bool          `json:"has_tests"`
	PythonFirst      bool          `json:"python_first"`
	PrimaryLanguage  string        `json:"primary_language,omitempty"`
	Languages        []Language    `json:"languages"`
	ReadmeFiles      []string      `json:"readme_files,omitempty"`
	TestFiles        []string      `json:"test_files,omitempty"`
	InstructionFiles []string      `json:"instruction_files,omitempty"`
	ConfigFiles      []string      `json:"config_files,omitempty"`
	GitRepository    bool          `json:"git_repository"`
	Checks           []Check       `json:"checks"`
	Recommendations  []string      `json:"recommendations,omitempty"`
	ScopeGuidance    ScopeGuidance `json:"scope_guidance"`
}

// Analyze scans a workspace and reports repository, language, README, test, and
// instruction-file readiness.
func Analyze(options Options) (Report, error) {
	workspace := strings.TrimSpace(options.Workspace)
	if workspace == "" {
		workspace = "."
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return Report{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Report{}, err
	}
	if !info.IsDir() {
		return Report{}, fmt.Errorf("workspace is not a directory: %s", abs)
	}

	state := scanState{
		workspace:      abs,
		languageCounts: map[string]int{},
	}
	if err := filepath.WalkDir(abs, state.visit); err != nil {
		return Report{}, err
	}
	report := state.report()
	return report, nil
}

// RenderText writes a human-readable onboarding report.
func RenderText(out io.Writer, report Report) {
	fmt.Fprintln(out, "Onboarding")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Workspace        %s\n", report.Workspace)
	fmt.Fprintf(out, "  README           %t\n", report.HasReadme)
	fmt.Fprintf(out, "  Tests            %t\n", report.HasTests)
	fmt.Fprintf(out, "  Git repository   %t\n", report.GitRepository)
	if report.PrimaryLanguage != "" {
		fmt.Fprintf(out, "  Primary language %s\n", report.PrimaryLanguage)
	}
	if len(report.Languages) > 0 {
		fmt.Fprintln(out, "  Languages")
		for _, lang := range report.Languages {
			fmt.Fprintf(out, "    %-16s %d files\n", lang.Name, lang.Files)
		}
	}
	if len(report.Checks) > 0 {
		fmt.Fprintln(out, "  Checks")
		for _, check := range report.Checks {
			if check.Path != "" {
				fmt.Fprintf(out, "    %-20s %-5s %s (%s)\n", check.Name, check.Status, check.Message, check.Path)
			} else {
				fmt.Fprintf(out, "    %-20s %-5s %s\n", check.Name, check.Status, check.Message)
			}
		}
	}
	for _, rec := range report.Recommendations {
		fmt.Fprintf(out, "  Recommendation   %s\n", rec)
	}
	if report.ScopeGuidance.Recommendation != "" {
		fmt.Fprintln(out, "  Scope guidance")
		fmt.Fprintf(out, "    Recommendation %s\n", report.ScopeGuidance.Recommendation)
		if report.ScopeGuidance.RiskStatus != "" {
			fmt.Fprintf(out, "    Scope risk     %s", report.ScopeGuidance.RiskStatus)
			if report.ScopeGuidance.RiskLevel != "" {
				fmt.Fprintf(out, " (%s)", report.ScopeGuidance.RiskLevel)
			}
			fmt.Fprintln(out)
		}
		if report.ScopeGuidance.RiskSummary != "" {
			fmt.Fprintf(out, "    Risk summary   %s\n", report.ScopeGuidance.RiskSummary)
		}
		if len(report.ScopeGuidance.IgnoreFiles) > 0 {
			fmt.Fprintf(out, "    Ignore files   %s\n", strings.Join(report.ScopeGuidance.IgnoreFiles, ", "))
		}
		if len(report.ScopeGuidance.HeavyPaths) > 0 {
			fmt.Fprintf(out, "    Heavy paths    %s\n", strings.Join(report.ScopeGuidance.HeavyPaths, ", "))
		}
		for _, behavior := range report.ScopeGuidance.IgnoreBehaviors {
			fmt.Fprintf(out, "    %-14s %s\n", behavior.File, behavior.Notes)
		}
	}
}

// RenderJSON writes the structured onboarding report as indented JSON.
func RenderJSON(out io.Writer, report Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(data))
	return err
}

type scanState struct {
	workspace        string
	readmes          []string
	tests            []string
	instructions     []string
	configs          []string
	ignoreFiles      []string
	heavyPaths       []string
	gitRepository    bool
	languageCounts   map[string]int
	packageTestFound bool
}

func (s *scanState) visit(path string, entry os.DirEntry, err error) error {
	if err != nil {
		return err
	}
	if path == s.workspace {
		return nil
	}
	rel, err := filepath.Rel(s.workspace, path)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	name := entry.Name()
	if entry.IsDir() {
		if name == ".git" {
			s.gitRepository = true
			return filepath.SkipDir
		}
		if heavyDir(name) {
			s.heavyPaths = appendUnique(s.heavyPaths, rel)
		}
		if skipDir(name) {
			return filepath.SkipDir
		}
		if name == "tests" || name == "test" || strings.HasSuffix(rel, "/tests") {
			s.tests = appendUnique(s.tests, rel)
		}
		return nil
	}

	lower := strings.ToLower(name)
	if isReadme(lower) {
		s.readmes = appendUnique(s.readmes, rel)
	}
	if isInstruction(rel, lower) {
		s.instructions = appendUnique(s.instructions, rel)
	}
	if isConfig(rel, lower) {
		s.configs = appendUnique(s.configs, rel)
	}
	if isIgnoreFile(lower) {
		s.ignoreFiles = appendUnique(s.ignoreFiles, rel)
	}
	if isTestFile(rel, lower) {
		s.tests = appendUnique(s.tests, rel)
	}
	if lower == "package.json" && packageHasTestScript(path) {
		s.packageTestFound = true
	}
	if lang := languageForFile(lower); lang != "" {
		s.languageCounts[lang]++
	}
	return nil
}

func (s scanState) report() Report {
	languages := languagesFromCounts(s.languageCounts)
	primary := ""
	if len(languages) > 0 {
		primary = languages[0].Name
	}
	hasTests := len(s.tests) > 0 || s.packageTestFound
	report := Report{
		Kind:             "onboarding",
		Action:           "inspect",
		Status:           "ready",
		Workspace:        s.workspace,
		HasReadme:        len(s.readmes) > 0,
		HasTests:         hasTests,
		PythonFirst:      primary == "Python",
		PrimaryLanguage:  primary,
		Languages:        languages,
		ReadmeFiles:      sortedCopy(s.readmes),
		TestFiles:        sortedCopy(s.tests),
		InstructionFiles: sortedCopy(s.instructions),
		ConfigFiles:      sortedCopy(s.configs),
		GitRepository:    s.gitRepository,
		ScopeGuidance:    buildScopeGuidance(sortedCopy(s.ignoreFiles), sortedCopy(s.heavyPaths)),
	}
	report.Checks = []Check{
		check("README", report.HasReadme, "README file found", "add a README that explains setup and verification", first(report.ReadmeFiles)),
		check("Tests", report.HasTests, "test entry point found", "add or document a repeatable test command", first(report.TestFiles)),
		check("Project guidance", len(report.InstructionFiles) > 0, "project instruction file found", "run `codog init` or add AGENTS.md, CLAUDE.md, .claude/CLAUDE.md, or .codog/instructions.md", first(report.InstructionFiles)),
		check("Codog config", len(report.ConfigFiles) > 0, "Codog project config found", "run `codog init` to create shared defaults", first(report.ConfigFiles)),
		check("Git", report.GitRepository, "git repository detected", "initialize git before using branch and PR workflows", ""),
	}
	for _, c := range report.Checks {
		if c.Status != "ok" {
			report.Status = "needs_setup"
			report.Recommendations = append(report.Recommendations, c.Message)
		}
	}
	if len(report.Languages) == 0 {
		report.Status = "needs_setup"
		report.Recommendations = append(report.Recommendations, "add source files or project manifests so Codog can infer the stack")
	}
	report.Recommendations = dedupe(report.Recommendations)
	return report
}

func buildScopeGuidance(ignoreFiles []string, heavyPaths []string) ScopeGuidance {
	riskStatus := "clean"
	riskLevel := "low"
	riskSummary := "Workspace looks clean for a first pass."
	if len(heavyPaths) > 0 {
		riskStatus = "warn"
		riskLevel = "medium"
		riskSummary = fmt.Sprintf("Workspace contains %d heavy/generated path(s) that can burn tokens quickly.", len(heavyPaths))
		for _, path := range heavyPaths {
			if path == "node_modules" || path == "vendor" || path == "dist" || path == "build" || path == ".next" || path == "generated" || path == "reports" {
				riskLevel = "high"
				break
			}
		}
	}
	return ScopeGuidance{
		Recommendation:         "Start Codog from the smallest useful package or service directory; add only needed sibling paths with `codog add-dir`.",
		RiskStatus:             riskStatus,
		RiskLevel:              riskLevel,
		RiskSummary:            riskSummary,
		HeavyDirectoryPatterns: heavyDirectoryPatterns(),
		IgnoreBehaviors: []IgnoreBehavior{
			{
				File:      ".gitignore",
				HonoredBy: []string{"grep", "glob", "ls"},
				Notes:     "honored by grep and glob when respectGitignore is enabled, and by ls directory listings",
			},
			{
				File:      ".codogignore",
				HonoredBy: []string{"ls"},
				Notes:     "honored by ls directory listings for Codog-specific local pruning",
			},
			{
				File:      ".claudeignore",
				HonoredBy: []string{"ls"},
				Notes:     "honored by ls directory listings for Claude-compatible local pruning",
			},
			{
				File:      ".clawignore",
				HonoredBy: []string{"ls"},
				Notes:     "honored by ls directory listings for claw-compatible local pruning",
			},
		},
		IgnoreFiles: sortedCopy(ignoreFiles),
		HeavyPaths:  sortedCopy(heavyPaths),
	}
}

func check(name string, ok bool, okMessage string, missingMessage string, path string) Check {
	if ok {
		return Check{Name: name, Status: "ok", Message: okMessage, Path: path}
	}
	return Check{Name: name, Status: "missing", Message: missingMessage}
}

func skipDir(name string) bool {
	switch name {
	case ".hg", ".svn", "node_modules", "vendor", "dist", "build", "target", ".venv", "venv", "__pycache__", ".next", "coverage", "logs", "dumps", "generated", "reports":
		return true
	default:
		return false
	}
}

func heavyDir(name string) bool {
	for _, pattern := range heavyDirectoryPatterns() {
		if name == pattern {
			return true
		}
	}
	return false
}

func heavyDirectoryPatterns() []string {
	return []string{"node_modules", "dist", "build", ".next", "coverage", "logs", "dumps", "generated", "reports"}
}

func isReadme(name string) bool {
	return name == "readme" ||
		strings.HasPrefix(name, "readme.") ||
		strings.HasPrefix(name, "readme-")
}

func isInstruction(rel string, name string) bool {
	switch rel {
	case "AGENTS.md", "AGENTS.local.md", "CLAUDE.md", "CLAUDE.local.md", ".claude/CLAUDE.md", "CLAW.md", "CLAW.local.md", ".claw/CLAUDE.md", ".claw/instructions.md", ".codog/instructions.md":
		return true
	default:
		return strings.HasSuffix(name, ".agents.md")
	}
}

func isConfig(rel string, name string) bool {
	switch rel {
	case ".codog.json", ".codog.local.json":
		return true
	default:
		return name == "codog.json"
	}
}

func isIgnoreFile(name string) bool {
	switch name {
	case ".gitignore", ".codogignore", ".claudeignore", ".clawignore":
		return true
	default:
		return false
	}
}

func isTestFile(rel string, name string) bool {
	if strings.HasSuffix(name, "_test.go") ||
		strings.HasSuffix(name, ".test.js") ||
		strings.HasSuffix(name, ".test.ts") ||
		strings.HasSuffix(name, ".spec.js") ||
		strings.HasSuffix(name, ".spec.ts") ||
		strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py") ||
		strings.HasSuffix(name, "_test.py") {
		return true
	}
	return strings.Contains(rel, "/tests/") || strings.Contains(rel, "/test/")
}

func packageHasTestScript(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return false
	}
	test := strings.TrimSpace(pkg.Scripts["test"])
	return test != "" && !strings.EqualFold(test, "echo \"Error: no test specified\" && exit 1")
}

func languageForFile(name string) string {
	switch filepath.Ext(name) {
	case ".go":
		return "Go"
	case ".py":
		return "Python"
	case ".rs":
		return "Rust"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "JavaScript"
	case ".ts", ".tsx":
		return "TypeScript"
	case ".java":
		return "Java"
	case ".rb":
		return "Ruby"
	case ".php":
		return "PHP"
	case ".c", ".h":
		return "C"
	case ".cc", ".cpp", ".cxx", ".hpp":
		return "C++"
	case ".cs":
		return "C#"
	default:
		switch name {
		case "go.mod":
			return "Go"
		case "pyproject.toml", "requirements.txt":
			return "Python"
		case "cargo.toml":
			return "Rust"
		case "package.json":
			return "JavaScript"
		default:
			return ""
		}
	}
}

func languagesFromCounts(counts map[string]int) []Language {
	languages := make([]Language, 0, len(counts))
	for name, count := range counts {
		languages = append(languages, Language{Name: name, Files: count})
	}
	sort.Slice(languages, func(i, j int) bool {
		if languages[i].Files == languages[j].Files {
			return languages[i].Name < languages[j].Name
		}
		return languages[i].Files > languages[j].Files
	})
	return languages
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func dedupe(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
