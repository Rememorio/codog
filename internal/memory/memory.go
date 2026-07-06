package memory

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// MaxFileBytes limits how much of each instruction file is loaded into memory.
const MaxFileBytes = 64 * 1024

// CandidateNames lists workspace-relative instruction file names Codog treats
// as project memory.
var CandidateNames = []string{
	"AGENTS.md",
	"AGENTS.local.md",
	"CLAUDE.md",
	"CLAUDE.local.md",
	filepath.Join(".claude", "CLAUDE.md"),
	"CLAW.md",
	"CLAW.local.md",
	filepath.Join(".claw", "CLAUDE.md"),
	filepath.Join(".claw", "instructions.md"),
	filepath.Join(".codog", "instructions.md"),
}

// File is a loaded project-memory instruction file.
type File struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	Scope      string    `json:"scope"`
	Chars      int       `json:"chars"`
	SizeBytes  int64     `json:"size_bytes"`
	ModifiedAt time.Time `json:"modified_at"`
	Truncated  bool      `json:"truncated,omitempty"`
	Body       string    `json:"-"`
}

// Metadata is a JSON-safe description of one loaded project-memory file.
type Metadata struct {
	Path           string `json:"path"`
	Name           string `json:"name"`
	Source         string `json:"source"`
	Origin         string `json:"origin"`
	Scope          string `json:"scope"`
	ScopePath      string `json:"scope_path"`
	OutsideProject bool   `json:"outside_project"`
	Chars          int    `json:"chars"`
	Lines          int    `json:"lines"`
	Words          int    `json:"words"`
	SizeBytes      int64  `json:"size_bytes"`
	ModifiedAt     string `json:"modified_at,omitempty"`
	AgeSeconds     int64  `json:"age_seconds,omitempty"`
	Empty          bool   `json:"empty"`
	Contributes    bool   `json:"contributes"`
	Truncated      bool   `json:"truncated,omitempty"`
}

// Summary is the JSON-safe metadata view of a memory file.
type Summary struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Scope      string `json:"scope"`
	Lines      int    `json:"lines"`
	Words      int    `json:"words"`
	Chars      int    `json:"chars"`
	SizeBytes  int64  `json:"size_bytes"`
	ModifiedAt string `json:"modified_at,omitempty"`
	AgeSeconds int64  `json:"age_seconds,omitempty"`
	Empty      bool   `json:"empty"`
	Preview    string `json:"preview"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// Report describes discovered project-memory files for a workspace.
type Report struct {
	Kind             string    `json:"kind"`
	Action           string    `json:"action"`
	Status           string    `json:"status"`
	WorkingDirectory string    `json:"working_directory"`
	InstructionFiles int       `json:"instruction_files"`
	Files            []Summary `json:"files"`
}

// SearchMatch records one relevant line found in project memory.
type SearchMatch struct {
	Path         string   `json:"path"`
	Name         string   `json:"name"`
	Scope        string   `json:"scope"`
	LineNumber   int      `json:"line_number"`
	Line         string   `json:"line"`
	Score        int      `json:"score"`
	MatchedTerms []string `json:"matched_terms,omitempty"`
}

// SearchReport describes a project-memory search result.
type SearchReport struct {
	Kind             string        `json:"kind"`
	Action           string        `json:"action"`
	Status           string        `json:"status"`
	WorkingDirectory string        `json:"working_directory"`
	Query            string        `json:"query"`
	MatchCount       int           `json:"match_count"`
	Matches          []SearchMatch `json:"matches"`
}

// ShowReport contains the selected memory file and its body.
type ShowReport struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Status string `json:"status"`
	File   File   `json:"file"`
	Body   string `json:"body,omitempty"`
}

// AppendReport describes a successful append to a memory file.
type AppendReport struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Status string `json:"status"`
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
}

// SelectionOption describes a memory file candidate for selector UIs.
type SelectionOption struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Source   string `json:"source"`
	Origin   string `json:"origin"`
	Scope    string `json:"scope"`
	Exists   bool   `json:"exists"`
	Selected bool   `json:"selected,omitempty"`
}

// SelectionReport previews the memory file that would be edited or created.
type SelectionReport struct {
	Kind             string            `json:"kind"`
	Action           string            `json:"action"`
	Status           string            `json:"status"`
	WorkingDirectory string            `json:"working_directory"`
	Target           string            `json:"target,omitempty"`
	Selected         string            `json:"selected"`
	OptionCount      int               `json:"option_count"`
	Options          []SelectionOption `json:"options"`
}

// FileReport describes a memory file path, creation, or editor operation.
type FileReport struct {
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Path    string `json:"path"`
	Created bool   `json:"created"`
	Opened  bool   `json:"opened,omitempty"`
	Editor  string `json:"editor,omitempty"`
	Message string `json:"message,omitempty"`
}

type ResetOptions struct {
	Target  string
	All     bool
	Confirm bool
}

type ResetFile struct {
	Path         string `json:"path"`
	Name         string `json:"name"`
	Scope        string `json:"scope"`
	BytesRemoved int    `json:"bytes_removed"`
}

type ResetReport struct {
	Kind             string      `json:"kind"`
	Action           string      `json:"action"`
	Status           string      `json:"status"`
	WorkingDirectory string      `json:"working_directory"`
	Target           string      `json:"target,omitempty"`
	All              bool        `json:"all,omitempty"`
	ResetCount       int         `json:"reset_count"`
	Files            []ResetFile `json:"files"`
}

// Discover loads project-memory files between the workspace and its boundary.
func Discover(workspace string) ([]File, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, nil
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	absWorkspace = canonicalPath(absWorkspace)
	boundary := absWorkspace
	if root, ok := gitRoot(absWorkspace); ok && isWithin(absWorkspace, root) {
		boundary = root
	}
	return discoverBetween(absWorkspace, boundary)
}

// Show returns a selected memory file, requiring a target when multiple files
// are available.
func Show(workspace string, target string) (ShowReport, error) {
	files, err := Discover(workspace)
	if err != nil {
		return ShowReport{}, err
	}
	if len(files) == 0 {
		return ShowReport{}, fmt.Errorf("no memory files found")
	}
	var selected *File
	target = strings.TrimSpace(target)
	if target == "" {
		if len(files) != 1 {
			return ShowReport{}, fmt.Errorf("memory file path is required when multiple files exist")
		}
		selected = &files[0]
	} else {
		for i := range files {
			if matchesTarget(files[i], target) {
				selected = &files[i]
				break
			}
		}
	}
	if selected == nil {
		return ShowReport{}, fmt.Errorf("memory file not found: %s", target)
	}
	return ShowReport{Kind: "memory", Action: "show", Status: "ok", File: *selected, Body: selected.Body}, nil
}

// Append adds text to the workspace AGENTS.md memory file.
func Append(workspace string, text string) (AppendReport, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return AppendReport{}, fmt.Errorf("memory text is required")
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return AppendReport{}, err
	}
	path := filepath.Join(canonicalPath(absWorkspace), "AGENTS.md")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return AppendReport{}, err
	}
	prefix := ""
	if len(existing) != 0 {
		if strings.HasSuffix(string(existing), "\n") {
			prefix = "\n"
		} else {
			prefix = "\n\n"
		}
	}
	payload := []byte(prefix + text + "\n")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return AppendReport{}, err
	}
	defer file.Close()
	if _, err := file.Write(payload); err != nil {
		return AppendReport{}, err
	}
	return AppendReport{Kind: "memory", Action: "add", Status: "ok", Path: path, Bytes: len(payload)}, nil
}

// Select returns memory selector candidates without creating files or opening
// an editor.
func Select(workspace string, target string) (SelectionReport, error) {
	absWorkspace, err := absWorkspacePath(workspace)
	if err != nil {
		return SelectionReport{}, err
	}
	files, err := Discover(absWorkspace)
	if err != nil {
		return SelectionReport{}, err
	}
	target = strings.TrimSpace(target)
	defaultPath, err := ResolvePath(absWorkspace, "")
	if err != nil {
		return SelectionReport{}, err
	}
	selectedPath := defaultPath
	if target != "" {
		selectedPath, err = ResolvePath(absWorkspace, target)
		if err != nil {
			return SelectionReport{}, err
		}
	} else if len(files) == 1 {
		selectedPath = files[0].Path
	}
	report := SelectionReport{
		Kind:             "memory",
		Action:           "select",
		Status:           "ok",
		WorkingDirectory: canonicalPath(absWorkspace),
		Target:           target,
		Selected:         canonicalPath(selectedPath),
		Options:          make([]SelectionOption, 0, len(files)+1),
	}
	seen := map[string]int{}
	projectRoot := absWorkspace
	if root, ok := gitRoot(absWorkspace); ok && isWithin(absWorkspace, root) {
		projectRoot = root
	}
	for _, file := range files {
		option := selectionOptionForFile(file, absWorkspace, projectRoot)
		index := len(report.Options)
		seen[canonicalPath(option.Path)] = index
		report.Options = append(report.Options, option)
	}
	selectedKey := canonicalPath(selectedPath)
	if index, ok := seen[selectedKey]; ok {
		report.Options[index].Selected = true
	} else {
		report.Options = append(report.Options, selectionOptionForPath(absWorkspace, selectedPath, target, true))
		report.Options[len(report.Options)-1].Selected = true
	}
	report.OptionCount = len(report.Options)
	return report, nil
}

// Path resolves the target memory file path without creating it.
func Path(workspace string, target string) (FileReport, error) {
	path, err := ResolvePath(workspace, target)
	if err != nil {
		return FileReport{}, err
	}
	return FileReport{Kind: "memory", Action: "path", Status: "ok", Path: path}, nil
}

// Ensure creates the target memory file if it does not already exist.
func Ensure(workspace string, target string) (FileReport, error) {
	path, err := ResolvePath(workspace, target)
	if err != nil {
		return FileReport{}, err
	}
	report := FileReport{Kind: "memory", Action: "ensure", Status: "ready", Path: path}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return report, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return report, nil
		}
		return report, err
	}
	defer file.Close()
	report.Status = "created"
	report.Created = true
	return report, nil
}

// Edit ensures the target memory file exists and optionally opens it in an
// editor.
func Edit(workspace string, target string, editor string, openEditor bool) (FileReport, error) {
	report, err := Ensure(workspace, target)
	if err != nil {
		return FileReport{}, err
	}
	report.Action = "edit"
	if !openEditor {
		report.Message = "Editor launch skipped."
		return report, nil
	}
	editor = resolveEditor(editor)
	if editor == "" {
		report.Message = "No editor configured; set VISUAL or EDITOR, or pass --editor."
		return report, nil
	}
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		report.Message = "No editor configured; set VISUAL or EDITOR, or pass --editor."
		return report, nil
	}
	cmd := exec.Command(fields[0], append(fields[1:], report.Path)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return report, err
	}
	report.Status = "opened"
	report.Opened = true
	report.Editor = editor
	report.Message = "Opened memory file in editor."
	return report, nil
}

// Reset clears one or more discovered project-memory files after confirmation.
func Reset(workspace string, opts ResetOptions) (ResetReport, error) {
	if !opts.Confirm {
		return ResetReport{}, fmt.Errorf("memory reset confirmation required")
	}
	absWorkspace, err := absWorkspacePath(workspace)
	if err != nil {
		return ResetReport{}, err
	}
	files, err := Discover(absWorkspace)
	if err != nil {
		return ResetReport{}, err
	}
	selected, err := selectResetFiles(files, opts)
	if err != nil {
		return ResetReport{}, err
	}
	report := ResetReport{
		Kind:             "memory",
		Action:           "reset",
		Status:           "ok",
		WorkingDirectory: canonicalPath(absWorkspace),
		Target:           strings.TrimSpace(opts.Target),
		All:              opts.All,
		Files:            make([]ResetFile, 0, len(selected)),
	}
	for _, file := range selected {
		info, err := os.Stat(file.Path)
		if err != nil {
			return ResetReport{}, err
		}
		if info.IsDir() {
			return ResetReport{}, fmt.Errorf("memory file not found: %s", file.Path)
		}
		if err := os.Truncate(file.Path, 0); err != nil {
			return ResetReport{}, err
		}
		report.Files = append(report.Files, ResetFile{
			Path:         file.Path,
			Name:         file.Name,
			Scope:        file.Scope,
			BytesRemoved: int(info.Size()),
		})
	}
	report.ResetCount = len(report.Files)
	return report, nil
}

func selectResetFiles(files []File, opts ResetOptions) ([]File, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("no memory files found")
	}
	target := strings.TrimSpace(opts.Target)
	if opts.All {
		return append([]File(nil), files...), nil
	}
	if target == "" {
		if len(files) != 1 {
			return nil, fmt.Errorf("memory file path is required when multiple files exist")
		}
		return []File{files[0]}, nil
	}
	for _, file := range files {
		if matchesTarget(file, target) {
			return []File{file}, nil
		}
	}
	return nil, fmt.Errorf("memory file not found: %s", target)
}

// ResolvePath converts a memory target into a workspace-contained absolute path.
func ResolvePath(workspace string, target string) (string, error) {
	absWorkspace, err := absWorkspacePath(workspace)
	if err != nil {
		return "", err
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return filepath.Join(absWorkspace, "AGENTS.md"), nil
	}
	if files, err := Discover(absWorkspace); err == nil {
		for _, file := range files {
			if matchesTarget(file, target) {
				return file.Path, nil
			}
		}
	}
	path := target
	if !filepath.IsAbs(path) {
		path = filepath.Join(absWorkspace, path)
	}
	path = filepath.Clean(path)
	if !isWithin(path, absWorkspace) {
		return "", fmt.Errorf("memory path escapes workspace: %s", target)
	}
	return path, nil
}

// BuildReport discovers and summarizes project-memory files for a workspace.
func BuildReport(workspace string) (Report, error) {
	absWorkspace, err := absWorkspacePath(workspace)
	if err != nil {
		return Report{}, err
	}
	files, err := Discover(absWorkspace)
	if err != nil {
		return Report{}, err
	}
	return Report{
		Kind:             "memory",
		Action:           "list",
		Status:           "ok",
		WorkingDirectory: canonicalPath(absWorkspace),
		InstructionFiles: len(files),
		Files:            Summaries(files),
	}, nil
}

// Search finds lines in project memory that match the query terms.
func Search(workspace string, query string, limit int) (SearchReport, error) {
	absWorkspace, err := absWorkspacePath(workspace)
	if err != nil {
		return SearchReport{}, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchReport{}, fmt.Errorf("memory search query is required")
	}
	if limit <= 0 {
		limit = 20
	}
	files, err := Discover(absWorkspace)
	if err != nil {
		return SearchReport{}, err
	}
	terms := searchTerms(query)
	type scoredMatch struct {
		match     SearchMatch
		fileIndex int
	}
	var matches []scoredMatch
	lowerQuery := strings.ToLower(query)
	for fileIndex, file := range files {
		lines := strings.Split(file.Body, "\n")
		for lineIndex, line := range lines {
			score, matchedTerms := scoreMemoryLine(line, lowerQuery, terms)
			if score == 0 {
				continue
			}
			matches = append(matches, scoredMatch{
				fileIndex: fileIndex,
				match: SearchMatch{
					Path:         file.Path,
					Name:         file.Name,
					Scope:        file.Scope,
					LineNumber:   lineIndex + 1,
					Line:         trimSearchLine(line),
					Score:        score,
					MatchedTerms: matchedTerms,
				},
			})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		left := matches[i]
		right := matches[j]
		if left.match.Score != right.match.Score {
			return left.match.Score > right.match.Score
		}
		if left.fileIndex != right.fileIndex {
			return left.fileIndex < right.fileIndex
		}
		return left.match.LineNumber < right.match.LineNumber
	})
	out := make([]SearchMatch, 0, min(limit, len(matches)))
	for index, match := range matches {
		if index >= limit {
			break
		}
		out = append(out, match.match)
	}
	return SearchReport{
		Kind:             "memory",
		Action:           "search",
		Status:           "ok",
		WorkingDirectory: canonicalPath(absWorkspace),
		Query:            query,
		MatchCount:       len(matches),
		Matches:          out,
	}, nil
}

// RenderShowReport writes a human-readable memory file report.
func RenderShowReport(w io.Writer, report ShowReport) {
	fmt.Fprintln(w, "Memory File")
	fmt.Fprintf(w, "  Path             %s\n", report.File.Path)
	fmt.Fprintf(w, "  Source           %s\n", report.File.Name)
	fmt.Fprintf(w, "  Scope            %s\n", report.File.Scope)
	if report.File.Truncated {
		fmt.Fprintln(w, "  Truncated        true")
	}
	fmt.Fprintln(w)
	fmt.Fprint(w, report.Body)
	if !strings.HasSuffix(report.Body, "\n") {
		fmt.Fprintln(w)
	}
}

// RenderAppendReport writes a human-readable memory append report.
func RenderAppendReport(w io.Writer, report AppendReport) {
	fmt.Fprintln(w, "Memory Updated")
	fmt.Fprintf(w, "  Path             %s\n", report.Path)
	fmt.Fprintf(w, "  Bytes appended   %d\n", report.Bytes)
}

// RenderSearchReport writes a human-readable memory search report.
func RenderSearchReport(w io.Writer, report SearchReport) {
	fmt.Fprintln(w, "Memory Search")
	fmt.Fprintf(w, "  Working directory %s\n", report.WorkingDirectory)
	fmt.Fprintf(w, "  Query             %s\n", report.Query)
	fmt.Fprintf(w, "  Matches           %d\n", report.MatchCount)
	if report.MatchCount == 0 {
		fmt.Fprintln(w, "  No matching memory lines found.")
		return
	}
	for i, match := range report.Matches {
		fmt.Fprintf(w, "  %d. %s:%d\n", i+1, match.Path, match.LineNumber)
		fmt.Fprintf(w, "     source=%s score=%d terms=%s\n", match.Name, match.Score, strings.Join(match.MatchedTerms, ","))
		fmt.Fprintf(w, "     %s\n", match.Line)
	}
}

// RenderSelectionReport writes a human-readable memory selector report.
func RenderSelectionReport(w io.Writer, report SelectionReport) {
	fmt.Fprintln(w, "Memory Selection")
	fmt.Fprintf(w, "  Working directory %s\n", report.WorkingDirectory)
	if report.Target != "" {
		fmt.Fprintf(w, "  Target            %s\n", report.Target)
	}
	fmt.Fprintf(w, "  Selected          %s\n", report.Selected)
	fmt.Fprintf(w, "  Options           %d\n", report.OptionCount)
	for i, option := range report.Options {
		selected := ""
		if option.Selected {
			selected = " selected=true"
		}
		fmt.Fprintf(w, "  %d. %s\n", i+1, option.Path)
		fmt.Fprintf(w, "     source=%s origin=%s exists=%t%s\n", option.Name, option.Origin, option.Exists, selected)
	}
}

// RenderFileReport writes a human-readable memory file operation report.
func RenderFileReport(w io.Writer, report FileReport) {
	fmt.Fprintln(w, "Memory File")
	fmt.Fprintf(w, "  Action           %s\n", report.Action)
	fmt.Fprintf(w, "  Status           %s\n", report.Status)
	fmt.Fprintf(w, "  Path             %s\n", report.Path)
	fmt.Fprintf(w, "  Created          %t\n", report.Created)
	if report.Editor != "" {
		fmt.Fprintf(w, "  Editor           %s\n", report.Editor)
	}
	if report.Message != "" {
		fmt.Fprintf(w, "  Message          %s\n", report.Message)
	}
}

// RenderResetReport writes a human-readable memory reset report.
func RenderResetReport(w io.Writer, report ResetReport) {
	fmt.Fprintln(w, "Memory Reset")
	fmt.Fprintf(w, "  Working directory %s\n", report.WorkingDirectory)
	fmt.Fprintf(w, "  Reset files       %d\n", report.ResetCount)
	for i, file := range report.Files {
		fmt.Fprintf(w, "  %d. %s\n", i+1, file.Path)
		fmt.Fprintf(w, "     source=%s bytes_removed=%d\n", file.Name, file.BytesRemoved)
	}
}

// MetadataFor returns JSON-safe metadata for loaded memory files.
func MetadataFor(workspace string, files []File) []Metadata {
	absWorkspace, err := absWorkspacePath(workspace)
	if err != nil {
		absWorkspace = canonicalPath(strings.TrimSpace(workspace))
	}
	projectRoot := absWorkspace
	if root, ok := gitRoot(absWorkspace); ok && isWithin(absWorkspace, root) {
		projectRoot = root
	}
	metadata := make([]Metadata, 0, len(files))
	for _, file := range files {
		path := canonicalPath(file.Path)
		scope := canonicalPath(file.Scope)
		fileForOrigin := file
		fileForOrigin.Path = path
		fileForOrigin.Scope = scope
		lines := countLines(file.Body)
		words := countWords(file.Body)
		empty := strings.TrimSpace(file.Body) == ""
		metadata = append(metadata, Metadata{
			Path:           path,
			Name:           file.Name,
			Source:         sourceForName(file.Name),
			Origin:         originForFile(fileForOrigin, absWorkspace, projectRoot),
			Scope:          scope,
			ScopePath:      scope,
			OutsideProject: !isWithin(path, projectRoot),
			Chars:          file.Chars,
			Lines:          lines,
			Words:          words,
			SizeBytes:      file.SizeBytes,
			ModifiedAt:     formatMemoryTime(file.ModifiedAt),
			AgeSeconds:     memoryAgeSeconds(file.ModifiedAt, time.Now()),
			Empty:          empty,
			Contributes:    !empty,
			Truncated:      file.Truncated,
		})
	}
	return metadata
}

// Summaries converts loaded memory files to metadata-only summaries.
func Summaries(files []File) []Summary {
	return SummariesAt(files, time.Now())
}

// SummariesAt converts loaded memory files to metadata-only summaries using a
// caller-provided clock for age calculation.
func SummariesAt(files []File, now time.Time) []Summary {
	summaries := make([]Summary, 0, len(files))
	for _, file := range files {
		body := file.Body
		summaries = append(summaries, Summary{
			Path:       file.Path,
			Name:       file.Name,
			Scope:      file.Scope,
			Lines:      countLines(body),
			Words:      countWords(body),
			Chars:      file.Chars,
			SizeBytes:  file.SizeBytes,
			ModifiedAt: formatMemoryTime(file.ModifiedAt),
			AgeSeconds: memoryAgeSeconds(file.ModifiedAt, now),
			Empty:      strings.TrimSpace(body) == "",
			Preview:    preview(body),
			Truncated:  file.Truncated,
		})
	}
	return summaries
}

func sourceForName(name string) string {
	switch filepath.ToSlash(name) {
	case "CLAUDE.md", "CLAUDE.local.md", ".claude/CLAUDE.md", ".claw/CLAUDE.md":
		return "claude_md"
	case "CLAW.md", "CLAW.local.md", ".claw/instructions.md":
		return "claw_md"
	case "AGENTS.md", "AGENTS.local.md":
		return "agents_md"
	case ".codog/instructions.md":
		return "codog_instructions"
	default:
		return "project_memory"
	}
}

func originForFile(file File, workspace string, projectRoot string) string {
	if workspace != "" && canonicalPath(file.Scope) == canonicalPath(workspace) {
		return "workspace"
	}
	if projectRoot != "" && canonicalPath(file.Scope) == canonicalPath(projectRoot) {
		return "project"
	}
	return "ancestor"
}

func selectionOptionForFile(file File, workspace string, projectRoot string) SelectionOption {
	path := canonicalPath(file.Path)
	file.Path = path
	file.Scope = canonicalPath(file.Scope)
	return SelectionOption{
		Path:   path,
		Name:   file.Name,
		Source: sourceForName(file.Name),
		Origin: originForFile(file, workspace, projectRoot),
		Scope:  file.Scope,
		Exists: true,
	}
}

func selectionOptionForPath(workspace string, path string, target string, selected bool) SelectionOption {
	path = canonicalPath(path)
	name := memoryNameForPath(workspace, path, target)
	return SelectionOption{
		Path:     path,
		Name:     name,
		Source:   sourceForName(name),
		Origin:   "workspace",
		Scope:    canonicalPath(workspace),
		Exists:   pathExists(path),
		Selected: selected,
	}
}

func memoryNameForPath(workspace string, path string, target string) string {
	target = strings.TrimSpace(target)
	if target != "" && !filepath.IsAbs(target) {
		return filepath.ToSlash(filepath.Clean(target))
	}
	if rel, err := filepath.Rel(workspace, path); err == nil && isWithin(path, workspace) {
		return filepath.ToSlash(rel)
	}
	return filepath.Base(path)
}

func pathExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func matchesTarget(file File, target string) bool {
	if target == file.Path || target == file.Name || target == filepath.Base(file.Path) {
		return true
	}
	abs, err := filepath.Abs(target)
	if err == nil && canonicalPath(abs) == canonicalPath(file.Path) {
		return true
	}
	return false
}

// RenderReport writes a human-readable memory discovery report.
func RenderReport(w io.Writer, report Report) {
	fmt.Fprintln(w, "Memory")
	fmt.Fprintf(w, "  Working directory %s\n", report.WorkingDirectory)
	fmt.Fprintf(w, "  Instruction files %d\n", report.InstructionFiles)
	fmt.Fprintln(w, "Discovered files")
	if report.InstructionFiles == 0 {
		fmt.Fprintf(w, "  No project memory files discovered in the current workspace ancestry. Checked: %s.\n", strings.Join(candidateNamesDisplay(), ", "))
		return
	}
	for i, file := range report.Files {
		fmt.Fprintf(w, "  %d. %s\n", i+1, file.Path)
		truncated := ""
		if file.Truncated {
			truncated = " truncated=true"
		}
		empty := ""
		if file.Empty {
			empty = " empty=true"
		}
		fmt.Fprintf(w, "     source=%s lines=%d words=%d chars=%d bytes=%d age_seconds=%d preview=%s%s%s\n", file.Name, file.Lines, file.Words, file.Chars, file.SizeBytes, file.AgeSeconds, file.Preview, truncated, empty)
	}
}

// Render formats loaded memory files for inclusion in the system prompt.
func Render(files []File) string {
	if len(files) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("<project_memory>\n")
	for _, file := range files {
		builder.WriteString("<file path=\"")
		builder.WriteString(escapeAttr(file.Path))
		builder.WriteString("\"")
		if file.Truncated {
			builder.WriteString(" truncated=\"true\"")
		}
		builder.WriteString(">\n")
		builder.WriteString(strings.TrimSpace(file.Body))
		builder.WriteString("\n</file>\n")
	}
	builder.WriteString("</project_memory>")
	return builder.String()
}

func discoverBetween(workspace string, boundary string) ([]File, error) {
	dirs := dirsFromBoundary(workspace, boundary)
	seen := map[string]struct{}{}
	var files []File
	for _, dir := range dirs {
		for _, name := range CandidateNames {
			path, ok, err := resolveCandidatePath(dir, name)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			key := canonicalPath(path)
			if _, ok := seen[key]; ok {
				continue
			}
			file, ok, err := readCandidate(path, dir, name)
			if err != nil {
				return nil, err
			}
			if ok {
				files = append(files, file)
				seen[key] = struct{}{}
			}
		}
	}
	return files, nil
}

func resolveCandidatePath(dir string, name string) (string, bool, error) {
	exact := filepath.Join(dir, name)
	if _, err := os.Stat(exact); err == nil {
		return exact, true, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", false, err
	}

	current := dir
	for _, part := range strings.Split(filepath.Clean(name), string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		entries, err := os.ReadDir(current)
		if err != nil {
			if os.IsNotExist(err) {
				return "", false, nil
			}
			return "", false, err
		}
		match := ""
		for _, entry := range entries {
			if strings.EqualFold(entry.Name(), part) {
				match = entry.Name()
				break
			}
		}
		if match == "" {
			return "", false, nil
		}
		current = filepath.Join(current, match)
	}
	return current, true, nil
}

func candidateNamesDisplay() []string {
	out := make([]string, 0, len(CandidateNames))
	for _, name := range CandidateNames {
		out = append(out, filepath.ToSlash(name))
	}
	return out
}

func readCandidate(path string, scope string, name string) (File, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return File{}, false, nil
		}
		return File{}, false, err
	}
	if info.IsDir() {
		return File{}, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, false, err
	}
	truncated := false
	if len(data) > MaxFileBytes {
		data = data[:MaxFileBytes]
		truncated = true
	}
	body := string(data)
	return File{
		Path:       path,
		Name:       filepath.ToSlash(name),
		Scope:      scope,
		Chars:      len([]rune(body)),
		SizeBytes:  info.Size(),
		ModifiedAt: info.ModTime().UTC(),
		Truncated:  truncated,
		Body:       body,
	}, true, nil
}

func countLines(body string) int {
	if body == "" {
		return 0
	}
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return 1
	}
	return strings.Count(body, "\n") + 1
}

func preview(body string) string {
	line := strings.TrimSpace(strings.SplitN(body, "\n", 2)[0])
	if line == "" {
		return "<empty>"
	}
	return line
}

func countWords(body string) int {
	return len(strings.FieldsFunc(body, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-'
	}))
}

func formatMemoryTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func memoryAgeSeconds(modifiedAt time.Time, now time.Time) int64 {
	if modifiedAt.IsZero() || now.IsZero() {
		return 0
	}
	age := now.Sub(modifiedAt)
	if age < 0 {
		return 0
	}
	return int64(age.Seconds())
}

func searchTerms(query string) []string {
	seen := map[string]bool{}
	var terms []string
	for _, term := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-'
	}) {
		term = strings.TrimSpace(term)
		if term == "" || seen[term] {
			continue
		}
		seen[term] = true
		terms = append(terms, term)
	}
	if len(terms) == 0 {
		terms = append(terms, strings.ToLower(strings.TrimSpace(query)))
	}
	return terms
}

func scoreMemoryLine(line string, lowerQuery string, terms []string) (int, []string) {
	lowerLine := strings.ToLower(line)
	score := 0
	seen := map[string]bool{}
	var matched []string
	if lowerQuery != "" && strings.Contains(lowerLine, lowerQuery) {
		score += 10
		seen[lowerQuery] = true
		matched = append(matched, lowerQuery)
	}
	for _, term := range terms {
		if term == "" || !strings.Contains(lowerLine, term) {
			continue
		}
		score += strings.Count(lowerLine, term)
		if !seen[term] {
			seen[term] = true
			matched = append(matched, term)
		}
	}
	return score, matched
}

func trimSearchLine(line string) string {
	line = strings.TrimSpace(line)
	const maxLineRunes = 240
	runes := []rune(line)
	if len(runes) <= maxLineRunes {
		return line
	}
	return string(runes[:maxLineRunes]) + "..."
}

func dirsFromBoundary(workspace string, boundary string) []string {
	var dirs []string
	cursor := filepath.Clean(workspace)
	boundary = filepath.Clean(boundary)
	for {
		dirs = append(dirs, cursor)
		if cursor == boundary {
			break
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			break
		}
		cursor = parent
	}
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

func gitRoot(workspace string) (string, bool) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", false
	}
	cmd := exec.Command("git", "-C", workspace, "rev-parse", "--show-toplevel")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", false
	}
	root := strings.TrimSpace(stdout.String())
	if root == "" {
		return "", false
	}
	return canonicalPath(root), true
}

func isWithin(path string, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func canonicalPath(path string) string {
	clean := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return clean
	}
	return resolved
}

func absWorkspacePath(workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	return canonicalPath(absWorkspace), nil
}

func resolveEditor(editor string) string {
	editor = strings.TrimSpace(editor)
	if editor != "" {
		return editor
	}
	if visual := strings.TrimSpace(os.Getenv("VISUAL")); visual != "" {
		return visual
	}
	return strings.TrimSpace(os.Getenv("EDITOR"))
}

func escapeAttr(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "\"", "&quot;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	return value
}
