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
	"unicode"
)

const MaxFileBytes = 64 * 1024

var CandidateNames = []string{
	"AGENTS.md",
	"CLAUDE.md",
	filepath.Join(".claude", "CLAUDE.md"),
	"CLAW.md",
	filepath.Join(".codog", "instructions.md"),
}

type File struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	Chars     int    `json:"chars"`
	Truncated bool   `json:"truncated,omitempty"`
	Body      string `json:"-"`
}

type Summary struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	Lines     int    `json:"lines"`
	Chars     int    `json:"chars"`
	Preview   string `json:"preview"`
	Truncated bool   `json:"truncated,omitempty"`
}

type Report struct {
	Kind             string    `json:"kind"`
	Action           string    `json:"action"`
	Status           string    `json:"status"`
	WorkingDirectory string    `json:"working_directory"`
	InstructionFiles int       `json:"instruction_files"`
	Files            []Summary `json:"files"`
}

type SearchMatch struct {
	Path         string   `json:"path"`
	Name         string   `json:"name"`
	Scope        string   `json:"scope"`
	LineNumber   int      `json:"line_number"`
	Line         string   `json:"line"`
	Score        int      `json:"score"`
	MatchedTerms []string `json:"matched_terms,omitempty"`
}

type SearchReport struct {
	Kind             string        `json:"kind"`
	Action           string        `json:"action"`
	Status           string        `json:"status"`
	WorkingDirectory string        `json:"working_directory"`
	Query            string        `json:"query"`
	MatchCount       int           `json:"match_count"`
	Matches          []SearchMatch `json:"matches"`
}

type ShowReport struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Status string `json:"status"`
	File   File   `json:"file"`
	Body   string `json:"body,omitempty"`
}

type AppendReport struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Status string `json:"status"`
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
}

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

func Path(workspace string, target string) (FileReport, error) {
	path, err := ResolvePath(workspace, target)
	if err != nil {
		return FileReport{}, err
	}
	return FileReport{Kind: "memory", Action: "path", Status: "ok", Path: path}, nil
}

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
	out := make([]SearchMatch, 0, minInt(limit, len(matches)))
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

func RenderAppendReport(w io.Writer, report AppendReport) {
	fmt.Fprintln(w, "Memory Updated")
	fmt.Fprintf(w, "  Path             %s\n", report.Path)
	fmt.Fprintf(w, "  Bytes appended   %d\n", report.Bytes)
}

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

func Summaries(files []File) []Summary {
	summaries := make([]Summary, 0, len(files))
	for _, file := range files {
		summaries = append(summaries, Summary{
			Path:      file.Path,
			Name:      file.Name,
			Scope:     file.Scope,
			Lines:     countLines(file.Body),
			Chars:     file.Chars,
			Preview:   preview(file.Body),
			Truncated: file.Truncated,
		})
	}
	return summaries
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

func RenderReport(w io.Writer, report Report) {
	fmt.Fprintln(w, "Memory")
	fmt.Fprintf(w, "  Working directory %s\n", report.WorkingDirectory)
	fmt.Fprintf(w, "  Instruction files %d\n", report.InstructionFiles)
	fmt.Fprintln(w, "Discovered files")
	if report.InstructionFiles == 0 {
		fmt.Fprintln(w, "  No AGENTS.md, CLAUDE.md, .claude/CLAUDE.md, CLAW.md, or .codog/instructions.md files discovered in the current workspace ancestry.")
		return
	}
	for i, file := range report.Files {
		fmt.Fprintf(w, "  %d. %s\n", i+1, file.Path)
		truncated := ""
		if file.Truncated {
			truncated = " truncated=true"
		}
		fmt.Fprintf(w, "     source=%s lines=%d chars=%d preview=%s%s\n", file.Name, file.Lines, file.Chars, file.Preview, truncated)
	}
}

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
			path := filepath.Join(dir, name)
			if _, ok := seen[path]; ok {
				continue
			}
			file, ok, err := readCandidate(path, dir, name)
			if err != nil {
				return nil, err
			}
			if ok {
				files = append(files, file)
				seen[path] = struct{}{}
			}
		}
	}
	return files, nil
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
		Path:      path,
		Name:      filepath.ToSlash(name),
		Scope:     scope,
		Chars:     len([]rune(body)),
		Truncated: truncated,
		Body:      body,
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

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
