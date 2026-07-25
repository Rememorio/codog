package visualization

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	directivePrefix = "::codog-inline-vis{"
	maxSourceBytes  = 2 << 20
)

var (
	// ErrInvalidFile reports a visualization name outside the managed source directory.
	ErrInvalidFile = errors.New("invalid visualization file")
	// ErrUnsafeSource reports a visualization source that resolves through a symbolic link.
	ErrUnsafeSource = errors.New("unsafe visualization source")
	// ErrSourceTooLarge reports a visualization source larger than the supported limit.
	ErrSourceTooLarge = errors.New("visualization source is too large")
)

// Manager owns visualization sources for one workspace and sandboxed viewers
// under one Codog config home.
type Manager struct {
	Workspace  string
	ConfigHome string
}

// Item describes a source visualization and its generated sandbox viewer.
type Item struct {
	File       string `json:"file"`
	Title      string `json:"title"`
	SourcePath string `json:"source_path"`
	ViewerPath string `json:"viewer_path"`
	URL        string `json:"url"`
}

type directive struct {
	File  string `json:"file"`
	Title string `json:"title,omitempty"`
}

// SourceDir returns the workspace directory in which assistant-authored
// visualization files are allowed.
func (m Manager) SourceDir() string {
	return filepath.Join(m.Workspace, ".codog", "visualizations")
}

// RewriteMarkdown replaces visualization directives outside fenced code blocks
// with links to generated sandbox viewers.
func (m Manager) RewriteMarkdown(markdown string) (string, bool) {
	if !strings.Contains(markdown, directivePrefix) {
		return markdown, false
	}
	scanner := bufio.NewScanner(strings.NewReader(markdown))
	scanner.Buffer(make([]byte, 4096), maxSourceBytes+4096)
	lines := make([]string, 0, strings.Count(markdown, "\n")+1)
	inFence := false
	changed := false
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if isFenceLine(trimmed) {
			inFence = !inFence
			lines = append(lines, line)
			continue
		}
		if !inFence && strings.HasPrefix(trimmed, directivePrefix) {
			lines = append(lines, m.rewriteDirective(trimmed))
			changed = true
			continue
		}
		lines = append(lines, line)
	}
	if scanner.Err() != nil {
		return markdown, false
	}
	rewritten := strings.Join(lines, "\n")
	if strings.HasSuffix(markdown, "\n") {
		rewritten += "\n"
	}
	return rewritten, changed
}

func isFenceLine(line string) bool {
	return strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")
}

func (m Manager) rewriteDirective(line string) string {
	var request directive
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(line, "::codog-inline-vis")))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || hasTrailingJSON(decoder) {
		return "_Visualization unavailable: invalid directive._"
	}
	item, err := m.Materialize(request.File, request.Title)
	if err != nil {
		return "_Visualization unavailable: " + publicError(err) + "._"
	}
	return fmt.Sprintf("[Open visualization: %s](%s)", escapeMarkdownLabel(item.Title), item.URL)
}

func hasTrailingJSON(decoder *json.Decoder) bool {
	var extra any
	return decoder.Decode(&extra) != io.EOF
}

func publicError(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "source file not found"
	case errors.Is(err, ErrInvalidFile):
		return "invalid file name"
	case errors.Is(err, ErrUnsafeSource):
		return "unsafe source file"
	case errors.Is(err, ErrSourceTooLarge):
		return "source file is too large"
	default:
		return "viewer could not be created"
	}
}

func escapeMarkdownLabel(value string) string {
	value = strings.TrimSpace(value)
	value = strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]").Replace(value)
	if value == "" {
		return "Visualization"
	}
	return value
}

// Materialize validates one source file and writes a sandboxed viewer outside
// the workspace so assistant tools cannot replace the security wrapper.
func (m Manager) Materialize(file string, title string) (Item, error) {
	file, err := validFileName(file)
	if err != nil {
		return Item{}, err
	}
	sourcePath := filepath.Join(m.SourceDir(), file)
	source, err := readSafeSource(m.Workspace, m.SourceDir(), sourcePath)
	if err != nil {
		return Item{}, err
	}
	viewerPath := filepath.Join(m.viewerDir(), strings.TrimSuffix(file, filepath.Ext(file))+".viewer.html")
	if err := os.MkdirAll(filepath.Dir(viewerPath), 0o700); err != nil {
		return Item{}, fmt.Errorf("create visualization viewer directory: %w", err)
	}
	if err := os.WriteFile(viewerPath, []byte(renderViewer(source, title)), 0o600); err != nil {
		return Item{}, fmt.Errorf("write visualization viewer: %w", err)
	}
	if strings.TrimSpace(title) == "" {
		title = strings.TrimSuffix(file, filepath.Ext(file))
	}
	absoluteViewer, err := filepath.Abs(viewerPath)
	if err != nil {
		return Item{}, fmt.Errorf("resolve visualization viewer: %w", err)
	}
	return Item{
		File:       file,
		Title:      strings.TrimSpace(title),
		SourcePath: sourcePath,
		ViewerPath: absoluteViewer,
		URL:        (&url.URL{Scheme: "file", Path: filepath.ToSlash(absoluteViewer)}).String(),
	}, nil
}

func validFileName(file string) (string, error) {
	file = strings.TrimSpace(file)
	if file == "" || filepath.Base(file) != file || strings.ContainsAny(file, `/\`) ||
		!strings.EqualFold(filepath.Ext(file), ".html") {
		return "", ErrInvalidFile
	}
	return file, nil
}

func readSafeSource(workspace string, root string, path string) ([]byte, error) {
	workspaceRoot, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, err
	}
	sourceRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	relativeRoot, err := filepath.Rel(workspaceRoot, sourceRoot)
	if err != nil || relativeRoot == ".." || strings.HasPrefix(relativeRoot, ".."+string(filepath.Separator)) {
		return nil, ErrUnsafeSource
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnsafeSource
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnsafeSource
	}
	if info.Size() > maxSourceBytes {
		return nil, ErrSourceTooLarge
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(source) > maxSourceBytes {
		return nil, ErrSourceTooLarge
	}
	return source, nil
}

func (m Manager) viewerDir() string {
	absoluteWorkspace, _ := filepath.Abs(m.Workspace)
	digest := sha256.Sum256([]byte(absoluteWorkspace))
	return filepath.Join(m.ConfigHome, "visualizations", hex.EncodeToString(digest[:8]))
}

func renderViewer(source []byte, title string) string {
	if strings.TrimSpace(title) == "" {
		title = "Codog visualization"
	}
	const embeddedCSP = "default-src 'none'; img-src data: blob:; media-src data: blob:; font-src data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'none'; form-action 'none'; navigate-to 'none'; base-uri 'none'"
	srcdoc := `<meta http-equiv="Content-Security-Policy" content="` + embeddedCSP + `">` + string(source)
	return "<!doctype html>\n" +
		`<html><head><meta charset="utf-8"><meta name="referrer" content="no-referrer">` +
		`<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; frame-src 'self'">` +
		"<title>" + html.EscapeString(title) + "</title>" +
		`<style>html,body,iframe{width:100%;height:100%;margin:0;border:0;background:#101418}iframe{display:block}</style>` +
		`</head><body><iframe title="` + html.EscapeString(title) + `" sandbox="allow-scripts" referrerpolicy="no-referrer" srcdoc="` +
		html.EscapeString(srcdoc) + `"></iframe></body></html>` + "\n"
}

// List materializes every valid HTML source in deterministic name order.
func (m Manager) List() ([]Item, error) {
	entries, err := os.ReadDir(m.SourceDir())
	if errors.Is(err, os.ErrNotExist) {
		return []Item{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list visualization sources: %w", err)
	}
	items := make([]Item, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".html") {
			continue
		}
		item, materializeErr := m.Materialize(entry.Name(), "")
		if materializeErr == nil {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].File < items[j].File })
	return items, nil
}

func viewerContainsSandbox(data []byte) bool {
	return bytes.Contains(data, []byte(`sandbox="allow-scripts"`)) &&
		bytes.Contains(data, []byte(`connect-src`)) &&
		bytes.Contains(data, []byte(`form-action`))
}
