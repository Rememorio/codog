package skills

import (
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Rememorio/codog/internal/frontmatter"
)

type Skill struct {
	Name                   string   `json:"name"`
	DisplayName            string   `json:"display_name,omitempty"`
	Path                   string   `json:"path"`
	Description            string   `json:"description,omitempty"`
	WhenToUse              string   `json:"when_to_use,omitempty"`
	Version                string   `json:"version,omitempty"`
	AllowedTools           []string `json:"allowed_tools,omitempty"`
	ArgumentHint           string   `json:"argument_hint,omitempty"`
	Arguments              []string `json:"arguments,omitempty"`
	Paths                  []string `json:"paths,omitempty"`
	Model                  string   `json:"model,omitempty"`
	ExecutionContext       string   `json:"execution_context,omitempty"`
	Agent                  string   `json:"agent,omitempty"`
	Effort                 string   `json:"effort,omitempty"`
	UserInvocable          bool     `json:"user_invocable"`
	DisableModelInvocation bool     `json:"disable_model_invocation,omitempty"`
	FrontmatterError       string   `json:"frontmatter_error,omitempty"`
	Body                   string   `json:"body,omitempty"`
	Source                 string   `json:"source"`
}

type InstallReport struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Status string `json:"status"`
	Name   string `json:"name"`
	Source string `json:"source"`
	Path   string `json:"path"`
	Target string `json:"target"`
}

type UninstallReport struct {
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Removed bool   `json:"removed"`
}

var ErrNotFound = errors.New("skill not found")

func Load(configHome, workspace string) ([]Skill, error) {
	roots := []struct {
		path   string
		source string
	}{
		{filepath.Join(configHome, "skills"), "user"},
		{filepath.Join(workspace, ".codog", "skills"), "workspace"},
		{filepath.Join(workspace, ".claude", "skills"), "claude"},
	}
	var out []Skill
	for _, root := range roots {
		if _, err := os.Stat(root.path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		err := filepath.WalkDir(root.path, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if !isSkillFile(path, entry.Name()) {
				return nil
			}
			name, err := skillName(root.path, path)
			if err != nil {
				return err
			}
			if name == "" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out = append(out, ParseDocument(name, path, root.source, string(data)))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func Find(configHome, workspace, name string) (Skill, error) {
	all, err := Load(configHome, workspace)
	if err != nil {
		return Skill{}, err
	}
	for _, skill := range all {
		if strings.EqualFold(skill.Name, name) {
			return skill, nil
		}
	}
	return Skill{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}

func MatchesAnyPath(skill Skill, paths []string) bool {
	if len(skill.Paths) == 0 || len(paths) == 0 {
		return false
	}
	for _, candidate := range paths {
		for _, pattern := range skill.Paths {
			if matchPathPattern(pattern, candidate) {
				return true
			}
		}
	}
	return false
}

func matchPathPattern(pattern string, candidate string) bool {
	pattern = cleanMatchPath(pattern)
	candidate = cleanMatchPath(candidate)
	if pattern == "" || candidate == "" {
		return false
	}
	if pattern == candidate || strings.HasPrefix(candidate, pattern+"/") {
		return true
	}
	if ok, _ := pathpkg.Match(pattern, candidate); ok {
		return true
	}
	if !strings.Contains(pattern, "/") {
		if ok, _ := pathpkg.Match(pattern, pathpkg.Base(candidate)); ok {
			return true
		}
	}
	if rest, ok := strings.CutPrefix(pattern, "**/"); ok {
		return matchPathPattern(rest, candidate) || matchPathPattern(rest, pathpkg.Base(candidate))
	}
	if prefix, rest, ok := strings.Cut(pattern, "/**/"); ok {
		if candidate == prefix {
			return true
		}
		if strings.HasPrefix(candidate, prefix+"/") {
			return matchPathPattern(rest, strings.TrimPrefix(candidate, prefix+"/"))
		}
	}
	return false
}

func cleanMatchPath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = strings.TrimPrefix(value, "./")
	value = pathpkg.Clean(value)
	if value == "." {
		return ""
	}
	return strings.TrimPrefix(value, "/")
}

func Install(source string, targetRoot string, explicitName string, targetLabel string) (InstallReport, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return InstallReport{}, errors.New("skill install source is required")
	}
	targetRoot = strings.TrimSpace(targetRoot)
	if targetRoot == "" {
		return InstallReport{}, errors.New("skill install target is required")
	}
	absSource, err := filepath.Abs(source)
	if err != nil {
		return InstallReport{}, err
	}
	resolvedSource, err := filepath.EvalSymlinks(absSource)
	if err != nil {
		return InstallReport{}, fmt.Errorf("skill source %q not found: %w", source, err)
	}
	info, err := os.Stat(resolvedSource)
	if err != nil {
		return InstallReport{}, err
	}
	name := strings.TrimSpace(explicitName)
	if name == "" {
		name = defaultInstallName(resolvedSource, info)
	}
	if err := validateSkillName(name); err != nil {
		return InstallReport{}, err
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return InstallReport{}, err
	}
	targetName := skillNamePath(name)
	var dest string
	if info.IsDir() {
		if _, err := os.Stat(filepath.Join(resolvedSource, "SKILL.md")); err != nil {
			return InstallReport{}, fmt.Errorf("skill directory %q must contain SKILL.md", source)
		}
		dest = filepath.Join(targetRoot, targetName)
		if err := os.RemoveAll(dest); err != nil {
			return InstallReport{}, err
		}
		if err := copyDir(resolvedSource, dest); err != nil {
			return InstallReport{}, err
		}
	} else {
		if !strings.EqualFold(filepath.Ext(resolvedSource), ".md") {
			return InstallReport{}, fmt.Errorf("skill source %q must be a markdown file or directory with SKILL.md", source)
		}
		dest = filepath.Join(targetRoot, targetName+".md")
		if err := copyFile(resolvedSource, dest, 0o644); err != nil {
			return InstallReport{}, err
		}
	}
	return InstallReport{
		Kind:   "skills",
		Action: "install",
		Status: "ok",
		Name:   name,
		Source: resolvedSource,
		Path:   dest,
		Target: targetLabel,
	}, nil
}

func Uninstall(name string, roots []string) (UninstallReport, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return UninstallReport{}, errors.New("skill name is required")
	}
	if err := validateSkillName(name); err != nil {
		return UninstallReport{}, err
	}
	pathName := skillNamePath(name)
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		candidates := []string{
			filepath.Join(root, pathName+".md"),
			filepath.Join(root, pathName),
		}
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return UninstallReport{}, err
			}
			if err := os.RemoveAll(candidate); err != nil {
				return UninstallReport{}, err
			}
			return UninstallReport{
				Kind:    "skills",
				Action:  "uninstall",
				Status:  "ok",
				Name:    name,
				Path:    candidate,
				Removed: true,
			}, nil
		}
	}
	return UninstallReport{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}

func RenderInvocation(skill Skill, args string) string {
	args = strings.TrimSpace(args)
	var builder strings.Builder
	builder.WriteString("Use the following Codog skill for this request.\n\n")
	builder.WriteString(RenderPromptBlock(skill))
	builder.WriteString("\n\n")
	if args == "" {
		builder.WriteString("User request: apply this skill.")
	} else {
		builder.WriteString("User request: ")
		builder.WriteString(args)
	}
	return builder.String()
}

func RenderPromptBlock(skill Skill) string {
	var builder strings.Builder
	builder.WriteString("<skill name=\"")
	builder.WriteString(escapeAttr(skill.Name))
	builder.WriteString("\" source=\"")
	builder.WriteString(escapeAttr(skill.Source))
	builder.WriteString("\" path=\"")
	builder.WriteString(escapeAttr(skill.Path))
	if skill.DisplayName != "" {
		builder.WriteString("\" display_name=\"")
		builder.WriteString(escapeAttr(skill.DisplayName))
	}
	builder.WriteString("\">\n")
	if metadata := renderMetadata(skill); metadata != "" {
		builder.WriteString("<metadata>\n")
		builder.WriteString(metadata)
		builder.WriteString("</metadata>\n\n")
	}
	builder.WriteString(strings.TrimSpace(skill.Body))
	builder.WriteString("\n</skill>")
	return builder.String()
}

func ParseDocument(name string, path string, source string, text string) Skill {
	body, values, parseErr := frontmatter.Parse(text)
	skill := Skill{
		Name:          name,
		Path:          path,
		Body:          body,
		Source:        source,
		UserInvocable: true,
	}
	if parseErr != nil {
		skill.FrontmatterError = parseErr.Error()
	}
	applyFrontmatter(&skill, values)
	if skill.Description == "" {
		skill.Description = frontmatter.DescriptionFromMarkdown(skill.Body)
	}
	return skill
}

func applyFrontmatter(skill *Skill, values map[string]any) {
	if len(values) == 0 {
		return
	}
	skill.DisplayName = frontmatter.String(values, "name")
	skill.Description = frontmatter.String(values, "description")
	skill.WhenToUse = frontmatter.FirstString(values, "when_to_use", "when-to-use")
	skill.Version = frontmatter.String(values, "version")
	skill.AllowedTools = frontmatter.StringList(values["allowed-tools"])
	skill.ArgumentHint = frontmatter.String(values, "argument-hint")
	skill.Arguments = frontmatter.ArgumentList(values["arguments"])
	skill.Paths = frontmatter.NormalizePaths(frontmatter.StringList(values["paths"]))
	skill.Model = frontmatter.String(values, "model")
	skill.Agent = frontmatter.String(values, "agent")
	skill.Effort = frontmatter.String(values, "effort")
	if context := frontmatter.String(values, "context"); context == "fork" {
		skill.ExecutionContext = context
	}
	if value, ok := frontmatter.Bool(values["user-invocable"]); ok {
		skill.UserInvocable = value
	}
	if value, ok := frontmatter.Bool(values["disable-model-invocation"]); ok {
		skill.DisableModelInvocation = value
	}
}

func renderMetadata(skill Skill) string {
	lines := []string{}
	appendLine := func(label string, value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			lines = append(lines, label+": "+value)
		}
	}
	appendLine("Description", skill.Description)
	appendLine("When to use", skill.WhenToUse)
	appendLine("Version", skill.Version)
	if len(skill.AllowedTools) > 0 {
		appendLine("Allowed tools", strings.Join(skill.AllowedTools, ", "))
	}
	appendLine("Argument hint", skill.ArgumentHint)
	if len(skill.Arguments) > 0 {
		appendLine("Arguments", strings.Join(skill.Arguments, ", "))
	}
	if len(skill.Paths) > 0 {
		appendLine("Paths", strings.Join(skill.Paths, ", "))
	}
	appendLine("Model", skill.Model)
	appendLine("Execution context", skill.ExecutionContext)
	appendLine("Agent", skill.Agent)
	appendLine("Effort", skill.Effort)
	if !skill.UserInvocable {
		appendLine("User invocable", "false")
	}
	if skill.DisableModelInvocation {
		appendLine("Disable model invocation", "true")
	}
	appendLine("Frontmatter error", skill.FrontmatterError)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func defaultInstallName(source string, info os.FileInfo) string {
	if info.IsDir() {
		return filepath.Base(source)
	}
	return strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
}

func validateSkillName(name string) error {
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid skill name %q", name)
	}
	for _, part := range strings.Split(name, ":") {
		if strings.TrimSpace(part) == "" || part == "." {
			return fmt.Errorf("invalid skill name %q", name)
		}
	}
	return nil
}

func skillNamePath(name string) string {
	return filepath.FromSlash(strings.ReplaceAll(name, ":", "/"))
}

func copyDir(source string, dest string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(source string, dest string, mode os.FileMode) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, data, mode)
}

func isSkillFile(path string, name string) bool {
	if name == "SKILL.md" {
		return true
	}
	return strings.HasSuffix(name, ".md") && filepath.Base(filepath.Dir(path)) != ""
}

func skillName(root string, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if pathpkg.Base(rel) == "SKILL.md" {
		dir := strings.TrimSuffix(pathpkg.Dir(rel), ".")
		return strings.ReplaceAll(dir, "/", ":"), nil
	}
	rel = strings.TrimSuffix(rel, ".md")
	return strings.ReplaceAll(rel, "/", ":"), nil
}

func escapeAttr(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}
