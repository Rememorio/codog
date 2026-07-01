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

type Options struct {
	Workspace string
}

type Language struct {
	Name  string `json:"name"`
	Files int    `json:"files"`
}

type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

type Report struct {
	Kind             string     `json:"kind"`
	Action           string     `json:"action"`
	Status           string     `json:"status"`
	Workspace        string     `json:"workspace"`
	HasReadme        bool       `json:"has_readme"`
	HasTests         bool       `json:"has_tests"`
	PythonFirst      bool       `json:"python_first"`
	PrimaryLanguage  string     `json:"primary_language,omitempty"`
	Languages        []Language `json:"languages"`
	ReadmeFiles      []string   `json:"readme_files,omitempty"`
	TestFiles        []string   `json:"test_files,omitempty"`
	InstructionFiles []string   `json:"instruction_files,omitempty"`
	ConfigFiles      []string   `json:"config_files,omitempty"`
	GitRepository    bool       `json:"git_repository"`
	Checks           []Check    `json:"checks"`
	Recommendations  []string   `json:"recommendations,omitempty"`
}

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
}

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
	}
	report.Checks = []Check{
		check("README", report.HasReadme, "README file found", "add a README that explains setup and verification", first(report.ReadmeFiles)),
		check("Tests", report.HasTests, "test entry point found", "add or document a repeatable test command", first(report.TestFiles)),
		check("Project guidance", len(report.InstructionFiles) > 0, "project instruction file found", "run `codog init` or add AGENTS.md/.codog/instructions.md", first(report.InstructionFiles)),
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

func check(name string, ok bool, okMessage string, missingMessage string, path string) Check {
	if ok {
		return Check{Name: name, Status: "ok", Message: okMessage, Path: path}
	}
	return Check{Name: name, Status: "missing", Message: missingMessage}
}

func skipDir(name string) bool {
	switch name {
	case ".hg", ".svn", "node_modules", "vendor", "dist", "build", "target", ".venv", "venv", "__pycache__":
		return true
	default:
		return false
	}
}

func isReadme(name string) bool {
	return name == "readme" ||
		strings.HasPrefix(name, "readme.") ||
		strings.HasPrefix(name, "readme-")
}

func isInstruction(rel string, name string) bool {
	switch rel {
	case "AGENTS.md", "CLAUDE.md", "CLAW.md", ".codog/instructions.md":
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
