package memory

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func BuildReport(workspace string) (Report, error) {
	absWorkspace := strings.TrimSpace(workspace)
	if absWorkspace == "" {
		absWorkspace = "."
	}
	absWorkspace, err := filepath.Abs(absWorkspace)
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

func escapeAttr(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "\"", "&quot;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	return value
}
