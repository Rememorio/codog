package skills

import (
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Rememorio/codog/internal/argsub"
	"github.com/Rememorio/codog/internal/frontmatter"
	"github.com/Rememorio/codog/internal/plugins"
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
	SkillDir               string   `json:"skill_dir,omitempty"`
	PluginRoot             string   `json:"plugin_root,omitempty"`
	PluginData             string   `json:"plugin_data,omitempty"`
	Model                  string   `json:"model,omitempty"`
	ExecutionContext       string   `json:"execution_context,omitempty"`
	Agent                  string   `json:"agent,omitempty"`
	Effort                 string   `json:"effort,omitempty"`
	UserInvocable          bool     `json:"user_invocable"`
	DisableModelInvocation bool     `json:"disable_model_invocation,omitempty"`
	FrontmatterError       string   `json:"frontmatter_error,omitempty"`
	Body                   string   `json:"body,omitempty"`
	Source                 string   `json:"source"`
	Origin                 *Origin  `json:"origin,omitempty"`
	Active                 bool     `json:"active"`
	ShadowedBy             string   `json:"shadowed_by,omitempty"`
	ShadowedByPath         string   `json:"shadowed_by_path,omitempty"`
	NameDrift              bool     `json:"metadata_drift,omitempty"`
}

type Origin struct {
	ID          string `json:"id"`
	DetailLabel string `json:"detail_label,omitempty"`
}

type DiscoveryRoot struct {
	Source     string  `json:"source"`
	Label      string  `json:"label"`
	Path       string  `json:"path"`
	Exists     bool    `json:"exists"`
	Origin     *Origin `json:"origin,omitempty"`
	PluginID   string  `json:"plugin_id,omitempty"`
	PluginRoot string  `json:"plugin_root,omitempty"`
}

type MetadataDrift struct {
	InvocationName  string `json:"invocation_name"`
	FrontmatterName string `json:"frontmatter_name"`
	Path            string `json:"path"`
	Source          string `json:"source"`
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

const (
	originSkillsDir          = "skills_dir"
	originLegacyCommandsDir  = "legacy_commands_dir"
	legacyCommandsDetailText = "legacy /commands"
)

type SourceNotFoundError struct {
	Source string
	Err    error
}

func (e SourceNotFoundError) Error() string {
	return fmt.Sprintf("skill source %q not found: %v", e.Source, e.Err)
}

func (e SourceNotFoundError) Unwrap() error {
	return e.Err
}

var bundledSkillDocuments = map[string]string{
	"batch": `---
description: Break a large request into a clear sequence of smaller coding tasks.
argument-hint: GOAL
---
# Batch

Use this skill when the user asks for broad implementation work that should be split into ordered, verifiable steps.

Create a short task queue, group related edits together, call out validation for each group, and keep the current turn focused on one coherent batch of work.
`,
	"claudeApi": `---
description: Work with Anthropic Claude-compatible API requests and responses.
argument-hint: API_TASK
allowed-tools:
  - Read
  - Grep
  - Bash(go test:*)
---
# Claude API

Use this skill for code paths that build, send, parse, retry, or test Anthropic Messages API compatible requests.

Check request shape, streaming event handling, usage accounting, retry behavior, and error rendering before changing provider code.
`,
	"claudeApiContent": `---
description: Inspect or transform Claude-compatible message content blocks.
argument-hint: CONTENT_TASK
allowed-tools:
  - Read
  - Grep
---
# Claude API Content

Use this skill when working with text, tool_use, tool_result, image, or structured content blocks.

Preserve block order, IDs, tool result pairing, and JSON compatibility. Prefer typed structures over string concatenation.
`,
	"claudeInChrome": `---
description: Reason about browser-assisted Claude workflows and web handoff surfaces.
argument-hint: BROWSER_TASK
---
# Claude In Chrome

Use this skill for browser handoff, Chrome integration, or web-based assistant workflows.

Keep local state explicit, avoid assuming a logged-in browser session, and make no-op or unavailable states visible to the user.
`,
	"debug": `---
description: Debug failing Codog behavior with a narrow reproduce-inspect-fix loop.
argument-hint: FAILURE
allowed-tools:
  - Read
  - Grep
  - Bash(go test:*)
---
# Debug

Use this skill when a command, tool, session, provider call, or integration fails.

Start from the smallest failing reproduction, inspect the code path that owns it, add a regression test when the failure is real, and validate the changed package before broader tests.
`,
	"keybindings": `---
description: Work on shortcut parsing, validation, and keybinding resolution.
argument-hint: SHORTCUT_TASK
allowed-tools:
  - Read
  - Grep
  - Bash(go test ./internal/agent:*)
---
# Keybindings

Use this skill when changing shortcut config, key normalization, vim mode shortcuts, or command completion bindings.

Normalize equivalent keys before comparing them, preserve reserved terminal behavior, and make resolution results inspectable.
`,
	"loop": `---
description: Build a tight implementation loop with repeated validation.
argument-hint: TASK
---
# Loop

Use this skill for work that needs repeated inspect, edit, run, and refine cycles.

Keep each iteration small, record what failed, update the implementation based on evidence, and stop only after the relevant validation passes or a real blocker is identified.
`,
	"loremIpsum": `---
description: Generate neutral placeholder copy for local demos and tests.
argument-hint: COPY_NEED
---
# Lorem Ipsum

Use this skill when placeholder prose is needed for fixtures, examples, or UI smoke tests.

Prefer short neutral text that is clearly sample content and avoid realistic secrets, credentials, personal data, or operational claims.
`,
	"remember": `---
description: Capture durable project guidance in memory files.
argument-hint: GUIDANCE
allowed-tools:
  - Read
  - Write
  - Edit
---
# Remember

Use this skill when the user gives a durable preference, workflow rule, or repository instruction that should affect future sessions.

Store concise guidance in the appropriate project memory file, avoid transient task notes, and keep sensitive or machine-specific details out of committed files.
`,
	"scheduleRemoteAgents": `---
description: Plan remote or background agent work with clear handoff boundaries.
argument-hint: REMOTE_TASK
---
# Schedule Remote Agents

Use this skill for remote sessions, background tasks, team workers, or delayed automation.

Define the objective, workspace, trust boundary, timeout, expected artifacts, and how the result should be observed or stopped.
`,
	"simplify": `---
description: Reduce unnecessary complexity while preserving behavior.
argument-hint: TARGET
---
# Simplify

Use this skill when code, docs, or command output has become too complex.

Remove redundant branches, collapse repeated wording, preserve compatibility, and verify the behavior that matters before and after the simplification.
`,
	"skillify": `---
description: Turn repeatable workflow knowledge into a reusable Codog skill.
argument-hint: WORKFLOW
---
# Skillify

Use this skill when a workflow should become a reusable Markdown skill.

Capture when to use it, required tools, inputs, steps, and validation. Keep the body actionable and avoid embedding one-off task details.
`,
	"stuck": `---
description: Recover when implementation progress stalls.
argument-hint: BLOCKER
---
# Stuck

Use this skill when the current approach is not producing progress.

Restate the failure, list evidence already gathered, reduce the reproduction, inspect the owning boundary, and choose the next smallest reversible step.
`,
	"updateConfig": `---
description: Update Codog configuration without losing existing user or project settings.
argument-hint: CONFIG_CHANGE
allowed-tools:
  - Read
  - Write
  - Edit
---
# Update Config

Use this skill when editing user, project, or local Codog config.

Preserve unrelated keys, respect config precedence, avoid writing secrets into project files, and prefer command helpers when they already express the change.
`,
	"verify": `---
description: Choose and run validation that proves a change works.
argument-hint: CHANGE
allowed-tools:
  - Read
  - Grep
  - Bash(go test:*)
  - Bash(go build:*)
---
# Verify

Use this skill after implementation or when assessing readiness.

Identify the smallest tests that cover the changed behavior, add focused regression tests when needed, then run broader validation proportional to the risk.
`,
	"verifyContent": `---
description: Validate generated or transformed content for accuracy and portability.
argument-hint: CONTENT
---
# Verify Content

Use this skill for docs, prompts, reports, exported sessions, generated Markdown, or user-facing text.

Check that claims match current behavior, examples are portable, links or paths are appropriate, and no local-only or sensitive data leaked into the artifact.
`,
}

type root struct {
	path         string
	source       string
	prefix       string
	originID     string
	originDetail string
	pluginRoot   string
	pluginData   string
}

func Load(configHome, workspace string) ([]Skill, error) {
	out := Bundled()
	for _, root := range roots(configHome, workspace) {
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
			name, err := skillName(root, path)
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
			out = append(out, parseDocumentFromRoot(name, path, root, string(data)))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if strings.EqualFold(out[i].Name, out[j].Name) {
			leftSource := sourceRank(out[i].Source)
			rightSource := sourceRank(out[j].Source)
			if leftSource == rightSource {
				leftOrigin := originRank(out[i].Origin)
				rightOrigin := originRank(out[j].Origin)
				if leftOrigin == rightOrigin {
					return out[i].Path < out[j].Path
				}
				return leftOrigin < rightOrigin
			}
			return leftSource < rightSource
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	annotateActiveSkills(out)
	return out, nil
}

func Bundled() []Skill {
	out := make([]Skill, 0, len(bundledSkillDocuments))
	names := make([]string, 0, len(bundledSkillDocuments))
	for name := range bundledSkillDocuments {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, ParseDocument(name, "builtin://skills/"+name+".md", "bundled", bundledSkillDocuments[name]))
	}
	return out
}

func Sources(configHome, workspace string) []DiscoveryRoot {
	out := []DiscoveryRoot{{
		Source: "bundled",
		Label:  "Bundled skills",
		Path:   "builtin://skills",
		Exists: true,
		Origin: newOrigin(originSkillsDir, ""),
	}}
	for _, root := range roots(configHome, workspace) {
		out = append(out, discoveryRoot(root))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if sourceRank(out[i].Source) == sourceRank(out[j].Source) {
			leftOrigin := originRank(out[i].Origin)
			rightOrigin := originRank(out[j].Origin)
			if leftOrigin == rightOrigin {
				return out[i].Path < out[j].Path
			}
			return leftOrigin < rightOrigin
		}
		return sourceRank(out[i].Source) < sourceRank(out[j].Source)
	})
	return out
}

func annotateActiveSkills(all []Skill) {
	winners := map[string]int{}
	for index := range all {
		key := strings.ToLower(strings.TrimSpace(all[index].Name))
		if key == "" {
			all[index].Active = false
			continue
		}
		winnerIndex, ok := winners[key]
		if !ok {
			winners[key] = index
			all[index].Active = true
			continue
		}
		winner := all[winnerIndex]
		all[index].Active = false
		all[index].ShadowedBy = winner.Source
		all[index].ShadowedByPath = winner.Path
	}
}

func MetadataDrifts(all []Skill) []MetadataDrift {
	out := []MetadataDrift{}
	for _, skill := range all {
		if !skill.NameDrift {
			continue
		}
		out = append(out, MetadataDrift{
			InvocationName:  skill.Name,
			FrontmatterName: skill.DisplayName,
			Path:            skill.Path,
			Source:          skill.Source,
		})
	}
	return out
}

func discoveryRoot(root root) DiscoveryRoot {
	exists := false
	if root.path != "" {
		if _, err := os.Stat(root.path); err == nil {
			exists = true
		}
	}
	return DiscoveryRoot{
		Source:     root.source,
		Label:      sourceLabel(root.source),
		Path:       root.path,
		Exists:     exists,
		Origin:     rootOrigin(root),
		PluginID:   pluginIDFromSource(root.source),
		PluginRoot: root.pluginRoot,
	}
}

func sourceLabel(source string) string {
	switch {
	case source == "bundled":
		return "Bundled skills"
	case source == "user":
		return "User skills"
	case source == "workspace":
		return "Workspace skills"
	case source == "claude":
		return "Claude-compatible workspace skills"
	case source == "codex":
		return "Codex-compatible project skills"
	case source == "claw":
		return "Claw-compatible project skills"
	case source == "agents":
		return "Agents-compatible project skills"
	case source == "omc":
		return "OMC-compatible project skills"
	case source == "opencode":
		return "OpenCode-compatible user skills"
	case strings.HasPrefix(source, "plugin:"):
		return "Plugin skills"
	default:
		return source
	}
}

func pluginIDFromSource(source string) string {
	id, ok := strings.CutPrefix(source, "plugin:")
	if !ok {
		return ""
	}
	return id
}

func roots(configHome, workspace string) []root {
	out := []root{
		skillRoot(filepath.Join(configHome, "skills"), "user"),
		skillRoot(filepath.Join(workspace, ".codog", "skills"), "workspace"),
		skillRoot(filepath.Join(workspace, ".claude", "skills"), "claude"),
		legacyCommandsRoot(filepath.Join(configHome, "commands"), "user"),
		legacyCommandsRoot(filepath.Join(workspace, ".codog", "commands"), "workspace"),
		legacyCommandsRoot(filepath.Join(workspace, ".claude", "commands"), "claude"),
	}
	out = append(out, compatibilityProjectRoots(workspace)...)
	out = append(out, compatibilityConfigRoots(out)...)
	manifests, err := plugins.Load(workspace)
	if err != nil {
		return out
	}
	for _, manifest := range manifests {
		out = append(out, skillRootsForPlugin(manifest)...)
	}
	return out
}

func compatibilityConfigRoots(existing []root) []root {
	out := []root{}
	seen := rootPathSet(existing)
	addSkillRoot := func(path string, source string) {
		path = strings.TrimSpace(path)
		if path == "" || !existingDir(path) {
			return
		}
		key := filepath.Clean(path)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, skillRoot(path, source))
	}
	addCommandRoot := func(path string, source string) {
		path = strings.TrimSpace(path)
		if path == "" || !existingDir(path) {
			return
		}
		key := filepath.Clean(path)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, legacyCommandsRoot(path, source))
	}
	addPrefixedRoots := func(prefix string, source string) {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			return
		}
		addSkillRoot(filepath.Join(prefix, "skills"), source)
		addCommandRoot(filepath.Join(prefix, "commands"), source)
	}

	addPrefixedRoots(os.Getenv("CLAW_CONFIG_HOME"), "claw")
	addPrefixedRoots(os.Getenv("CODEX_HOME"), "codex")
	if claudeConfigDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); claudeConfigDir != "" {
		addSkillRoot(filepath.Join(claudeConfigDir, "skills"), "claude")
		addSkillRoot(filepath.Join(claudeConfigDir, "skills", "omc-learned"), "claude")
		addCommandRoot(filepath.Join(claudeConfigDir, "commands"), "claude")
	}
	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		home = strings.TrimSpace(os.Getenv("USERPROFILE"))
	}
	if home != "" {
		addPrefixedRoots(filepath.Join(home, ".omc"), "omc")
		addPrefixedRoots(filepath.Join(home, ".claw"), "claw")
		addPrefixedRoots(filepath.Join(home, ".codex"), "codex")
		addPrefixedRoots(filepath.Join(home, ".claude"), "claude")
		addSkillRoot(filepath.Join(home, ".agents", "skills"), "agents")
		addSkillRoot(filepath.Join(home, ".config", "opencode", "skills"), "opencode")
		addSkillRoot(filepath.Join(home, ".claude", "skills", "omc-learned"), "claude")
	}
	return out
}

func rootPathSet(roots []root) map[string]bool {
	seen := map[string]bool{}
	for _, root := range roots {
		if strings.TrimSpace(root.path) != "" {
			seen[filepath.Clean(root.path)] = true
		}
	}
	return seen
}

func compatibilityProjectRoots(workspace string) []root {
	out := []root{}
	seen := map[string]bool{}
	for _, ancestor := range workspaceAncestors(workspace) {
		for _, prefix := range []struct {
			dir    string
			source string
		}{
			{dir: ".codex", source: "codex"},
			{dir: ".claw", source: "claw"},
			{dir: ".agents", source: "agents"},
			{dir: ".omc", source: "omc"},
		} {
			skillsPath := filepath.Join(ancestor, prefix.dir, "skills")
			if existingDir(skillsPath) && !seen[filepath.Clean(skillsPath)] {
				out = append(out, skillRoot(skillsPath, prefix.source))
				seen[filepath.Clean(skillsPath)] = true
			}
			commandsPath := filepath.Join(ancestor, prefix.dir, "commands")
			if existingDir(commandsPath) && !seen[filepath.Clean(commandsPath)] {
				out = append(out, legacyCommandsRoot(commandsPath, prefix.source))
				seen[filepath.Clean(commandsPath)] = true
			}
		}
	}
	return out
}

func workspaceAncestors(workspace string) []string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		abs = filepath.Clean(workspace)
	}
	out := []string{}
	for current := abs; ; current = filepath.Dir(current) {
		out = append(out, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return out
}

func existingDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func skillRoot(path string, source string) root {
	return root{path: path, source: source, originID: originSkillsDir}
}

func legacyCommandsRoot(path string, source string) root {
	return root{path: path, source: source, originID: originLegacyCommandsDir, originDetail: legacyCommandsDetailText}
}

func skillRootsForPlugin(manifest plugins.Manifest) []root {
	if !manifest.Enabled {
		return nil
	}
	out := []root{{
		path:       filepath.Join(manifest.Root, "skills"),
		source:     "plugin:" + manifest.ID,
		prefix:     manifest.ID,
		originID:   originSkillsDir,
		pluginRoot: manifest.Root,
		pluginData: plugins.DataDirForManifest(manifest),
	}}
	seen := map[string]bool{filepath.Clean(out[0].path): true}
	for _, spec := range manifest.Skills {
		path, err := plugins.ResolveContentPath(manifest.Root, spec)
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		rootPath := path
		if info.IsDir() {
			if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err == nil {
				rootPath = filepath.Dir(path)
			}
		} else {
			if !strings.EqualFold(filepath.Ext(path), ".md") {
				continue
			}
			rootPath = filepath.Dir(path)
		}
		key := filepath.Clean(rootPath)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, root{path: rootPath, source: "plugin:" + manifest.ID, prefix: manifest.ID, originID: originSkillsDir, pluginRoot: manifest.Root, pluginData: plugins.DataDirForManifest(manifest)})
	}
	return out
}

func Find(configHome, workspace, name string) (Skill, error) {
	all, err := Load(configHome, workspace)
	if err != nil {
		return Skill{}, err
	}
	var found *Skill
	for _, skill := range all {
		if strings.EqualFold(skill.Name, name) {
			candidate := skill
			if found == nil || skillPriority(candidate) < skillPriority(*found) {
				found = &candidate
			}
		}
	}
	if found != nil {
		return *found, nil
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
		return InstallReport{}, SourceNotFoundError{Source: source, Err: err}
	}
	info, err := os.Stat(resolvedSource)
	if err != nil {
		if os.IsNotExist(err) {
			return InstallReport{}, SourceNotFoundError{Source: source, Err: err}
		}
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
	return RenderInvocationWithSession(skill, args, "")
}

func RenderInvocationWithSession(skill Skill, args string, sessionID string) string {
	args = strings.TrimSpace(args)
	renderedSkill := skill
	renderedSkill.Body = argsub.Substitute(skill.Body, args, false, skill.Arguments)
	variables := skillVariablesWithSession(skill, sessionID)
	renderedSkill.Body = argsub.SubstituteVariables(renderedSkill.Body, variables)
	var builder strings.Builder
	builder.WriteString("Use the following Codog skill for this request.\n\n")
	builder.WriteString(renderPromptBlockWithVariables(renderedSkill, variables))
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
	return renderPromptBlockWithVariables(skill, skillVariables(skill))
}

func renderPromptBlockWithVariables(skill Skill, variables map[string]string) string {
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
	builder.WriteString(strings.TrimSpace(argsub.SubstituteVariables(skill.Body, variables)))
	builder.WriteString("\n</skill>")
	return builder.String()
}

func ParseDocument(name string, path string, source string, text string) Skill {
	return parseDocumentWithContext(name, path, source, "", "", "", newOrigin(originSkillsDir, ""), text)
}

func parseDocumentFromRoot(name string, path string, root root, text string) Skill {
	return parseDocumentWithContext(name, path, root.source, skillDir(path), root.pluginRoot, root.pluginData, rootOrigin(root), text)
}

func parseDocumentWithContext(name string, path string, source string, skillRoot string, pluginRoot string, pluginData string, origin *Origin, text string) Skill {
	body, values, parseErr := frontmatter.Parse(text)
	skill := Skill{
		Name:          name,
		Path:          path,
		Body:          body,
		Source:        source,
		Origin:        cloneOrigin(origin),
		Active:        true,
		SkillDir:      normalizedPathVariable(skillRoot),
		PluginRoot:    normalizedPathVariable(pluginRoot),
		PluginData:    normalizedPathVariable(pluginData),
		UserInvocable: true,
	}
	if parseErr != nil {
		skill.FrontmatterError = parseErr.Error()
	}
	applyFrontmatter(&skill, values)
	skill.NameDrift = hasNameDrift(skill.Name, skill.DisplayName)
	skill.AllowedTools = argsub.SubstituteVariablesInList(skill.AllowedTools, skillVariables(skill))
	if skill.Description == "" {
		skill.Description = frontmatter.DescriptionFromMarkdown(skill.Body)
	}
	return skill
}

func hasNameDrift(invocationName string, frontmatterName string) bool {
	invocationName = normalizeSkillIdentity(invocationName)
	frontmatterName = normalizeSkillIdentity(frontmatterName)
	return invocationName != "" && frontmatterName != "" && invocationName != frontmatterName
}

func normalizeSkillIdentity(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.ReplaceAll(value, "/", ":")
	value = strings.Join(strings.Fields(value), "-")
	return value
}

func skillVariables(skill Skill) map[string]string {
	return skillVariablesWithSession(skill, "")
}

func skillVariablesWithSession(skill Skill, sessionID string) map[string]string {
	variables := map[string]string{}
	if skill.SkillDir != "" {
		variables["CLAUDE_SKILL_DIR"] = skill.SkillDir
	}
	if skill.PluginRoot != "" {
		variables["CLAUDE_PLUGIN_ROOT"] = skill.PluginRoot
	}
	if skill.PluginData != "" {
		variables["CLAUDE_PLUGIN_DATA"] = skill.PluginData
	}
	if strings.TrimSpace(sessionID) != "" {
		variables["CLAUDE_SESSION_ID"] = strings.TrimSpace(sessionID)
	}
	return variables
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

func skillName(root root, path string) (string, error) {
	rel, err := filepath.Rel(root.path, path)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	var name string
	if pathpkg.Base(rel) == "SKILL.md" {
		dir := strings.TrimSuffix(pathpkg.Dir(rel), ".")
		name = strings.ReplaceAll(dir, "/", ":")
	} else {
		rel = strings.TrimSuffix(rel, ".md")
		name = strings.ReplaceAll(rel, "/", ":")
	}
	return namespacePluginName(root.prefix, name), nil
}

func skillDir(path string) string {
	if strings.HasPrefix(path, "builtin://") {
		return ""
	}
	return filepath.Dir(path)
}

func namespacePluginName(prefix string, name string) string {
	prefix = strings.TrimSpace(prefix)
	name = strings.TrimSpace(name)
	if prefix == "" || name == "" || strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)+":") {
		return name
	}
	return prefix + ":" + name
}

func normalizedPathVariable(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.ToSlash(path)
}

func sourceRank(source string) int {
	switch {
	case source == "workspace":
		return 0
	case source == "user":
		return 1
	case source == "claude":
		return 2
	case source == "codex", source == "claw", source == "agents", source == "omc", source == "opencode":
		return 3
	case strings.HasPrefix(source, "plugin:"):
		return 4
	case source == "bundled":
		return 5
	default:
		return 6
	}
}

func skillPriority(skill Skill) int {
	return sourceRank(skill.Source)*10 + originRank(skill.Origin)
}

func originRank(origin *Origin) int {
	if origin == nil {
		return 0
	}
	switch strings.TrimSpace(origin.ID) {
	case "", originSkillsDir:
		return 0
	case originLegacyCommandsDir:
		return 1
	default:
		return 2
	}
}

func rootOrigin(root root) *Origin {
	return newOrigin(root.originID, root.originDetail)
}

func newOrigin(id string, detail string) *Origin {
	id = strings.TrimSpace(id)
	if id == "" {
		id = originSkillsDir
	}
	detail = strings.TrimSpace(detail)
	return &Origin{ID: id, DetailLabel: detail}
}

func cloneOrigin(origin *Origin) *Origin {
	if origin == nil {
		return newOrigin(originSkillsDir, "")
	}
	return newOrigin(origin.ID, origin.DetailLabel)
}

func escapeAttr(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}
