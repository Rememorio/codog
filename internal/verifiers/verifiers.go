package verifiers

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	StatusCreated = "created"
	StatusUpdated = "updated"
	StatusSkipped = "skipped"
)

type Options struct {
	Workspace string
	Target    string
	Force     bool
	DryRun    bool
}

type Area struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Kind        string   `json:"kind"`
	Stack       []string `json:"stack"`
	SkillName   string   `json:"skill_name"`
	Description string   `json:"description"`
}

type Artifact struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Status string `json:"status"`
}

type Report struct {
	Kind       string     `json:"kind"`
	Action     string     `json:"action"`
	Status     string     `json:"status"`
	Workspace  string     `json:"workspace"`
	Target     string     `json:"target"`
	TargetRoot string     `json:"target_root"`
	DryRun     bool       `json:"dry_run"`
	Force      bool       `json:"force"`
	Areas      []Area     `json:"areas"`
	Artifacts  []Artifact `json:"artifacts"`
	Created    []string   `json:"created"`
	Updated    []string   `json:"updated"`
	Skipped    []string   `json:"skipped"`
	Warnings   []string   `json:"warnings,omitempty"`
	NextStep   string     `json:"next_step"`
}

type packageJSON struct {
	Name            string            `json:"name"`
	Bin             any               `json:"bin"`
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

type markerSet struct {
	goMod        bool
	cargo        bool
	packageJSON  packageJSON
	packageFound bool
	pyproject    string
	requirements string
}

func Initialize(options Options) (Report, error) {
	workspace := strings.TrimSpace(options.Workspace)
	if workspace == "" {
		workspace = "."
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return Report{}, err
	}
	target := strings.ToLower(strings.TrimSpace(options.Target))
	if target == "" {
		target = "claude"
	}
	targetRoot, err := targetRoot(abs, target)
	if err != nil {
		return Report{}, err
	}

	areas, warnings, err := Detect(abs)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		Kind:       "init_verifiers",
		Action:     "init",
		Status:     "ok",
		Workspace:  abs,
		Target:     target,
		TargetRoot: targetRoot,
		DryRun:     options.DryRun,
		Force:      options.Force,
		Areas:      areas,
		Warnings:   warnings,
		NextStep:   "Review generated verifier skills and tailor commands, URLs, and authentication details.",
	}
	if len(areas) == 0 {
		report.Status = "skipped"
		report.NextStep = "No verifier skill was generated because no project markers were detected."
		return report, nil
	}
	for _, area := range areas {
		artifact, err := writeSkill(targetRoot, area, options)
		if err != nil {
			return Report{}, err
		}
		report.Artifacts = append(report.Artifacts, artifact)
		switch artifact.Status {
		case StatusCreated:
			report.Created = append(report.Created, artifact.Name)
		case StatusUpdated:
			report.Updated = append(report.Updated, artifact.Name)
		case StatusSkipped:
			report.Skipped = append(report.Skipped, artifact.Name)
		}
	}
	return report, nil
}

func Detect(workspace string) ([]Area, []string, error) {
	candidates, err := candidateDirs(workspace)
	if err != nil {
		return nil, nil, err
	}
	var areas []Area
	var warnings []string
	seen := map[string]int{}
	for _, dir := range candidates {
		rel, err := filepath.Rel(workspace, dir)
		if err != nil {
			return nil, nil, err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			rel = "."
		}
		markers := readMarkers(dir)
		if !markers.present() {
			continue
		}
		area := areaFor(rel, markers)
		if area.Kind == "" {
			continue
		}
		if count := seen[area.SkillName]; count > 0 {
			seen[area.SkillName] = count + 1
			area.SkillName = fmt.Sprintf("%s-%d", area.SkillName, count+1)
		} else {
			seen[area.SkillName] = 1
		}
		areas = append(areas, area)
	}
	if len(areas) == 0 {
		warnings = append(warnings, "No go.mod, package.json, Cargo.toml, pyproject.toml, or requirements.txt marker was detected.")
	}
	sort.Slice(areas, func(i, j int) bool {
		if areas[i].Path == areas[j].Path {
			return areas[i].SkillName < areas[j].SkillName
		}
		if areas[i].Path == "." {
			return true
		}
		if areas[j].Path == "." {
			return false
		}
		return areas[i].Path < areas[j].Path
	})
	return areas, warnings, nil
}

func RenderText(report Report) string {
	var builder strings.Builder
	builder.WriteString("Verifier Init\n")
	builder.WriteString("  Target           ")
	builder.WriteString(report.TargetRoot)
	builder.WriteString("\n")
	builder.WriteString("  Dry run          ")
	builder.WriteString(fmt.Sprintf("%t", report.DryRun))
	builder.WriteString("\n")
	if len(report.Areas) == 0 {
		builder.WriteString("  Areas            0\n")
	}
	for _, area := range report.Areas {
		builder.WriteString("  Area             ")
		builder.WriteString(area.Path)
		builder.WriteString(" -> ")
		builder.WriteString(area.SkillName)
		builder.WriteString(" (")
		builder.WriteString(area.Kind)
		builder.WriteString(")\n")
	}
	for _, artifact := range report.Artifacts {
		builder.WriteString("  ")
		builder.WriteString(padRight(artifact.Name, 32))
		builder.WriteString(artifact.Status)
		builder.WriteString("\n")
	}
	for _, warning := range report.Warnings {
		builder.WriteString("  Warning          ")
		builder.WriteString(warning)
		builder.WriteString("\n")
	}
	builder.WriteString("  Next step        ")
	builder.WriteString(report.NextStep)
	return builder.String()
}

func targetRoot(workspace, target string) (string, error) {
	switch target {
	case "claude":
		return filepath.Join(workspace, ".claude", "skills"), nil
	case "codog", "workspace":
		return filepath.Join(workspace, ".codog", "skills"), nil
	default:
		return "", fmt.Errorf("unknown verifier target %q", target)
	}
}

func candidateDirs(workspace string) ([]string, error) {
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return nil, err
	}
	out := []string{workspace}
	for _, entry := range entries {
		if !entry.IsDir() || skipDir(entry.Name()) {
			continue
		}
		out = append(out, filepath.Join(workspace, entry.Name()))
	}
	sort.Strings(out[1:])
	return out, nil
}

func skipDir(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "dist", "build", "target", "coverage", "__pycache__":
		return true
	default:
		return false
	}
}

func readMarkers(dir string) markerSet {
	var markers markerSet
	markers.goMod = fileExists(filepath.Join(dir, "go.mod"))
	markers.cargo = fileExists(filepath.Join(dir, "Cargo.toml"))
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		markers.packageFound = true
		_ = json.Unmarshal(data, &markers.packageJSON)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "pyproject.toml")); err == nil {
		markers.pyproject = strings.ToLower(string(data))
	}
	if data, err := os.ReadFile(filepath.Join(dir, "requirements.txt")); err == nil {
		markers.requirements = strings.ToLower(string(data))
	}
	return markers
}

func (m markerSet) present() bool {
	return m.goMod || m.cargo || m.packageFound || m.pyproject != "" || m.requirements != ""
}

func areaFor(rel string, markers markerSet) Area {
	stack := markers.stack()
	kind := "cli"
	switch {
	case markers.isWeb():
		kind = "web"
	case markers.isAPI():
		kind = "api"
	case markers.goMod && markers.packageFound:
		kind = "fullstack"
	}
	base := "root"
	if rel != "." {
		base = filepath.Base(filepath.FromSlash(rel))
	}
	skillName := verifierName(base, kind, rel == ".")
	return Area{
		Name:        base,
		Path:        rel,
		Kind:        kind,
		Stack:       stack,
		SkillName:   skillName,
		Description: descriptionFor(kind, stack, rel),
	}
}

func (m markerSet) stack() []string {
	var stack []string
	if m.goMod {
		stack = append(stack, "Go")
	}
	if m.cargo {
		stack = append(stack, "Rust")
	}
	if m.packageFound {
		stack = append(stack, "JavaScript/TypeScript")
		for _, name := range []string{"next", "react", "vite", "express", "fastify", "@nestjs/core"} {
			if m.hasPackage(name) {
				stack = append(stack, packageLabel(name))
			}
		}
	}
	if m.pyproject != "" || m.requirements != "" {
		stack = append(stack, "Python")
		for _, name := range []string{"fastapi", "flask", "django"} {
			if strings.Contains(m.pyproject, name) || strings.Contains(m.requirements, name) {
				stack = append(stack, packageLabel(name))
			}
		}
	}
	return dedupe(stack)
}

func (m markerSet) isWeb() bool {
	for _, name := range []string{"next", "react", "vite", "@angular/core", "vue", "svelte"} {
		if m.hasPackage(name) {
			return true
		}
	}
	return false
}

func (m markerSet) isAPI() bool {
	for _, name := range []string{"express", "fastify", "@nestjs/core", "koa"} {
		if m.hasPackage(name) {
			return true
		}
	}
	for _, name := range []string{"fastapi", "flask", "django"} {
		if strings.Contains(m.pyproject, name) || strings.Contains(m.requirements, name) {
			return true
		}
	}
	return false
}

func (m markerSet) hasPackage(name string) bool {
	name = strings.ToLower(name)
	for dep := range m.packageJSON.Dependencies {
		if strings.ToLower(dep) == name {
			return true
		}
	}
	for dep := range m.packageJSON.DevDependencies {
		if strings.ToLower(dep) == name {
			return true
		}
	}
	return false
}

func packageLabel(name string) string {
	switch name {
	case "next":
		return "Next.js"
	case "react":
		return "React"
	case "vite":
		return "Vite"
	case "@nestjs/core":
		return "NestJS"
	case "fastapi":
		return "FastAPI"
	default:
		return strings.TrimPrefix(name, "@")
	}
}

func verifierName(base, kind string, root bool) string {
	if root {
		return "verifier-" + kind
	}
	base = sanitizeName(base)
	if base == "" {
		base = "project"
	}
	return "verifier-" + base + "-" + kind
}

func sanitizeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func descriptionFor(kind string, stack []string, rel string) string {
	surface := "the repository"
	if rel != "." {
		surface = rel
	}
	if len(stack) == 0 {
		return "Verify " + surface + " changes"
	}
	return "Verify " + surface + " changes for " + strings.Join(stack, ", ")
}

func writeSkill(targetRoot string, area Area, options Options) (Artifact, error) {
	relPath := filepath.ToSlash(filepath.Join(".claude", "skills", area.SkillName, "SKILL.md"))
	if strings.EqualFold(options.Target, "codog") || strings.EqualFold(options.Target, "workspace") {
		relPath = filepath.ToSlash(filepath.Join(".codog", "skills", area.SkillName, "SKILL.md"))
	}
	path := filepath.Join(targetRoot, area.SkillName, "SKILL.md")
	artifact := Artifact{Name: relPath, Path: path, Status: StatusCreated}
	if _, err := os.Stat(path); err == nil {
		if !options.Force {
			artifact.Status = StatusSkipped
			return artifact, nil
		}
		artifact.Status = StatusUpdated
	} else if !errors.Is(err, os.ErrNotExist) {
		return Artifact{}, err
	}
	if options.DryRun {
		return artifact, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Artifact{}, err
	}
	content := RenderSkill(area)
	return artifact, os.WriteFile(path, []byte(content), 0o644)
}

func RenderSkill(area Area) string {
	var builder strings.Builder
	builder.WriteString("---\n")
	builder.WriteString("name: ")
	builder.WriteString(area.SkillName)
	builder.WriteString("\n")
	builder.WriteString("description: ")
	builder.WriteString(area.Description)
	builder.WriteString("\n")
	builder.WriteString("allowed-tools:\n")
	for _, tool := range allowedTools(area.Kind) {
		builder.WriteString("  - ")
		builder.WriteString(tool)
		builder.WriteString("\n")
	}
	builder.WriteString("---\n\n")
	builder.WriteString("# ")
	builder.WriteString(title(area.SkillName))
	builder.WriteString("\n\n")
	builder.WriteString("You are a verification executor. Follow the verification plan exactly and report PASS or FAIL for each step.\n\n")
	builder.WriteString("## Project Area\n\n")
	builder.WriteString("- Path: `")
	builder.WriteString(area.Path)
	builder.WriteString("`\n")
	builder.WriteString("- Kind: `")
	builder.WriteString(area.Kind)
	builder.WriteString("`\n")
	if len(area.Stack) != 0 {
		builder.WriteString("- Stack: ")
		builder.WriteString(strings.Join(area.Stack, ", "))
		builder.WriteString("\n")
	}
	builder.WriteString("\n## Suggested Checks\n\n")
	for _, check := range suggestedChecks(area) {
		builder.WriteString("- ")
		builder.WriteString(check)
		builder.WriteString("\n")
	}
	builder.WriteString("\n## Reporting\n\n")
	builder.WriteString("- Report the exact commands or browser/API checks you ran.\n")
	builder.WriteString("- Mark each planned check as PASS or FAIL.\n")
	builder.WriteString("- If the verifier instructions are stale, describe the minimal update needed instead of hiding the mismatch.\n")
	return builder.String()
}

func allowedTools(kind string) []string {
	common := []string{"Read", "Grep", "Glob"}
	switch kind {
	case "web":
		return append([]string{"Bash(npm:*)", "Bash(yarn:*)", "Bash(pnpm:*)", "Bash(bun:*)", "Bash(npx:*)", "mcp__playwright__*"}, common...)
	case "api":
		return append([]string{"Bash(curl:*)", "Bash(go:*)", "Bash(npm:*)", "Bash(python:*)", "Bash(pytest:*)"}, common...)
	case "fullstack":
		return append([]string{"Bash(go:*)", "Bash(npm:*)", "Bash(yarn:*)", "Bash(pnpm:*)", "Bash(bun:*)", "Bash(curl:*)", "mcp__playwright__*"}, common...)
	default:
		return append([]string{"Bash(go:*)", "Bash(cargo:*)", "Bash(npm:*)", "Bash(python:*)", "Bash(pytest:*)"}, common...)
	}
}

func suggestedChecks(area Area) []string {
	prefix := ""
	if area.Path != "." {
		prefix = " from `" + area.Path + "`"
	}
	switch area.Kind {
	case "web":
		return []string{
			"Start the app's dev server" + prefix + " using the package manager script documented by the project.",
			"Use browser automation to verify the changed user flow.",
			"Run the relevant build, lint, or test script for changed frontend code.",
		}
	case "api":
		return []string{
			"Start the API service" + prefix + " using the project command.",
			"Use HTTP checks against the changed endpoints.",
			"Run the relevant unit or integration tests for changed server code.",
		}
	case "fullstack":
		return []string{
			"Run backend tests for the changed Go code.",
			"Start the frontend or fullstack app" + prefix + " and verify the changed user flow.",
			"Use HTTP or browser checks for cross-boundary behavior.",
		}
	default:
		return []string{
			"Run the repository's primary test command" + prefix + ".",
			"Exercise the CLI or library behavior touched by the change.",
			"Inspect logs or command output for regressions before reporting PASS.",
		}
	}
}

func title(name string) string {
	parts := strings.Split(strings.ReplaceAll(name, "-", " "), " ")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func dedupe(values []string) []string {
	seen := map[string]bool{}
	var out []string
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

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func padRight(value string, width int) string {
	if len(value) >= width {
		return value + " "
	}
	return value + strings.Repeat(" ", width-len(value))
}
