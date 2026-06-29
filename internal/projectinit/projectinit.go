package projectinit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	StatusCreated = "created"
	StatusUpdated = "updated"
	StatusSkipped = "skipped"
	StatusPartial = "partial"
	NextStep      = "Review and tailor the generated project guidance"
)

const starterConfig = `{
  "permission_mode": "workspace-write",
  "auto_compact_messages": 40
}
`

const gitignoreComment = "# Codog local artifacts"

var gitignoreEntries = []string{".codog.local.json", ".codog/worker-state.json", ".codog/focus.json"}

type Artifact struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type Report struct {
	Kind               string     `json:"kind"`
	Action             string     `json:"action"`
	Status             string     `json:"status"`
	AlreadyInitialized bool       `json:"already_initialized"`
	ProjectPath        string     `json:"project_path"`
	Created            []string   `json:"created"`
	Updated            []string   `json:"updated"`
	Skipped            []string   `json:"skipped"`
	Partial            []string   `json:"partial"`
	Artifacts          []Artifact `json:"artifacts"`
	Hint               string     `json:"hint"`
	NextStep           string     `json:"next_step"`
}

func Initialize(workspace string) (Report, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return Report{}, err
	}

	var artifacts []Artifact
	codogDir := filepath.Join(abs, ".codog")
	dirStatus, err := ensureDir(codogDir)
	if err != nil {
		return Report{}, err
	}
	instructionsStatus, err := writeFileIfMissing(filepath.Join(codogDir, "instructions.md"), RenderInstructions(abs))
	if err != nil {
		return Report{}, err
	}
	if dirStatus == StatusSkipped && instructionsStatus == StatusCreated {
		dirStatus = StatusPartial
	}
	artifacts = append(artifacts,
		Artifact{Name: ".codog/", Status: dirStatus},
		Artifact{Name: ".codog/instructions.md", Status: instructionsStatus},
	)

	configStatus, err := writeFileIfMissing(filepath.Join(abs, ".codog.json"), starterConfig)
	if err != nil {
		return Report{}, err
	}
	artifacts = append(artifacts, Artifact{Name: ".codog.json", Status: configStatus})

	gitignoreStatus, err := ensureGitignoreEntries(filepath.Join(abs, ".gitignore"))
	if err != nil {
		return Report{}, err
	}
	artifacts = append(artifacts, Artifact{Name: ".gitignore", Status: gitignoreStatus})

	return newReport(abs, artifacts), nil
}

func RenderText(report Report) string {
	var builder strings.Builder
	builder.WriteString("Init\n")
	builder.WriteString("  Project          ")
	builder.WriteString(report.ProjectPath)
	builder.WriteString("\n")
	for _, artifact := range report.Artifacts {
		builder.WriteString("  ")
		builder.WriteString(padRight(artifact.Name, 24))
		builder.WriteString(artifact.Status)
		builder.WriteString("\n")
	}
	builder.WriteString("  Next step        ")
	builder.WriteString(report.NextStep)
	return builder.String()
}

func RenderInstructions(workspace string) string {
	detection := detect(workspace)
	lines := []string{
		"# Codog Project Instructions",
		"",
		"This file gives Codog repository-specific guidance. Keep it accurate as build, test, and review workflows change.",
		"",
		"## Detected Stack",
	}
	languages := detection.languages()
	if len(languages) == 0 {
		lines = append(lines, "- No specific language markers were detected yet.")
	} else {
		lines = append(lines, "- Languages: "+strings.Join(languages, ", ")+".")
	}
	frameworks := detection.frameworks()
	if len(frameworks) != 0 {
		lines = append(lines, "- Frameworks/tooling markers: "+strings.Join(frameworks, ", ")+".")
	}
	verification := detection.verification()
	if len(verification) != 0 {
		lines = append(lines, "", "## Verification")
		lines = append(lines, verification...)
	}
	lines = append(lines,
		"",
		"## Working Agreement",
		"- Prefer small, reviewable changes with focused tests for changed behavior.",
		"- Keep `.codog.json` for shared project defaults and `.codog.local.json` for machine-local overrides.",
		"- Update this file intentionally when repository workflows change.",
		"",
	)
	return strings.Join(lines, "\n")
}

func newReport(projectPath string, artifacts []Artifact) Report {
	report := Report{
		Kind:        "init",
		Action:      "init",
		Status:      "ok",
		ProjectPath: projectPath,
		Created:     []string{},
		Updated:     []string{},
		Skipped:     []string{},
		Partial:     []string{},
		Artifacts:   append([]Artifact(nil), artifacts...),
		NextStep:    NextStep,
	}
	for _, artifact := range artifacts {
		switch artifact.Status {
		case StatusCreated:
			report.Created = append(report.Created, artifact.Name)
		case StatusUpdated:
			report.Updated = append(report.Updated, artifact.Name)
		case StatusSkipped:
			report.Skipped = append(report.Skipped, artifact.Name)
		case StatusPartial:
			report.Partial = append(report.Partial, artifact.Name)
		}
	}
	report.AlreadyInitialized = len(report.Created) == 0 && len(report.Updated) == 0 && len(report.Partial) == 0
	if report.AlreadyInitialized {
		report.Hint = "Workspace already initialized. Run `codog doctor` to verify health, or edit .codog/instructions.md to customize guidance."
	} else {
		report.Hint = "Review and tailor .codog/instructions.md, then run `codog doctor` to verify the workspace."
	}
	return report
}

func ensureDir(path string) (string, error) {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return StatusSkipped, nil
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", err
	}
	return StatusCreated, nil
}

func writeFileIfMissing(path string, content string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		return StatusSkipped, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return StatusCreated, os.WriteFile(path, []byte(content), 0o644)
}

func ensureGitignoreEntries(path string) (string, error) {
	existingData, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if os.IsNotExist(err) {
		lines := append([]string{gitignoreComment}, gitignoreEntries...)
		return StatusCreated, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	}
	lines := strings.Split(strings.TrimRight(string(existingData), "\n"), "\n")
	changed := false
	if !containsLine(lines, gitignoreComment) {
		lines = append(lines, gitignoreComment)
		changed = true
	}
	for _, entry := range gitignoreEntries {
		if !containsLine(lines, entry) {
			lines = append(lines, entry)
			changed = true
		}
	}
	if !changed {
		return StatusSkipped, nil
	}
	return StatusUpdated, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func containsLine(lines []string, target string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) == target {
			return true
		}
	}
	return false
}

func padRight(value string, width int) string {
	if len(value) >= width {
		return value + " "
	}
	return value + strings.Repeat(" ", width-len(value))
}

type detection struct {
	goMod        bool
	cargo        bool
	pyproject    bool
	requirements bool
	packageJSON  packageJSON
	testsDir     bool
	srcDir       bool
}

type packageJSON struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Scripts         map[string]string `json:"scripts"`
}

func detect(workspace string) detection {
	var pkg packageJSON
	if data, err := os.ReadFile(filepath.Join(workspace, "package.json")); err == nil {
		_ = json.Unmarshal(data, &pkg)
	}
	return detection{
		goMod:        fileExists(filepath.Join(workspace, "go.mod")),
		cargo:        fileExists(filepath.Join(workspace, "Cargo.toml")),
		pyproject:    fileExists(filepath.Join(workspace, "pyproject.toml")),
		requirements: fileExists(filepath.Join(workspace, "requirements.txt")),
		packageJSON:  pkg,
		testsDir:     dirExists(filepath.Join(workspace, "tests")),
		srcDir:       dirExists(filepath.Join(workspace, "src")),
	}
}

func (d detection) languages() []string {
	var languages []string
	if d.goMod {
		languages = append(languages, "Go")
	}
	if d.cargo {
		languages = append(languages, "Rust")
	}
	if d.pyproject || d.requirements {
		languages = append(languages, "Python")
	}
	if len(d.packageJSON.Scripts) != 0 || len(d.packageJSON.Dependencies) != 0 || len(d.packageJSON.DevDependencies) != 0 {
		languages = append(languages, "JavaScript/TypeScript")
	}
	return languages
}

func (d detection) frameworks() []string {
	deps := map[string]string{}
	for name, version := range d.packageJSON.Dependencies {
		deps[strings.ToLower(name)] = version
	}
	for name, version := range d.packageJSON.DevDependencies {
		deps[strings.ToLower(name)] = version
	}
	var frameworks []string
	for _, candidate := range []struct {
		dependency string
		name       string
	}{
		{"next", "Next.js"},
		{"react", "React"},
		{"vite", "Vite"},
		{"@nestjs/core", "NestJS"},
	} {
		if _, ok := deps[candidate.dependency]; ok {
			frameworks = append(frameworks, candidate.name)
		}
	}
	return frameworks
}

func (d detection) verification() []string {
	var lines []string
	if d.goMod {
		lines = append(lines, "- Run Go checks from the repo root: `go test ./...`.")
	}
	if d.cargo {
		lines = append(lines, "- Run Rust checks from the repo root: `cargo test`.")
	}
	if d.pyproject || d.requirements {
		lines = append(lines, "- Run the repo's Python test and lint commands before shipping changes.")
	}
	scripts := make([]string, 0, len(d.packageJSON.Scripts))
	for name := range d.packageJSON.Scripts {
		scripts = append(scripts, name)
	}
	sort.Strings(scripts)
	for _, name := range scripts {
		if name == "test" || name == "lint" || name == "build" {
			lines = append(lines, "- Run `npm run "+name+"` when JavaScript/TypeScript changes affect that surface.")
		}
	}
	if d.srcDir && d.testsDir {
		lines = append(lines, "- `src/` and `tests/` are both present; update tests with behavior changes.")
	}
	return lines
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
