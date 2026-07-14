package outputstyle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StateFileName is the workspace-local file that stores the selected output style.
const StateFileName = "output-style.json"

// Style describes one available output style loaded from built-in, user, or
// workspace catalogs.
type Style struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Path    string `json:"path,omitempty"`
	Preview string `json:"preview"`
	Body    string `json:"body,omitempty"`
}

// StyleSummary is the list-facing view of a style, including precedence
// diagnostics.
type StyleSummary struct {
	Name           string `json:"name"`
	Source         string `json:"source"`
	Path           string `json:"path,omitempty"`
	Preview        string `json:"preview"`
	Active         bool   `json:"active,omitempty"`
	Effective      bool   `json:"effective,omitempty"`
	ShadowedBy     string `json:"shadowed_by,omitempty"`
	ShadowedByPath string `json:"shadowed_by_path,omitempty"`
}

// DiscoveryRoot describes a catalog location that can provide output styles.
type DiscoveryRoot struct {
	Source string `json:"source"`
	Label  string `json:"label"`
	Path   string `json:"path,omitempty"`
	Exists bool   `json:"exists"`
}

// HealthSummary summarizes effective and shadowed output style definitions.
type HealthSummary struct {
	Total     int `json:"total"`
	Effective int `json:"effective"`
	Shadowed  int `json:"shadowed"`
}

// State stores the workspace's active output style selection.
type State struct {
	Kind      string    `json:"kind"`
	Active    string    `json:"active,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Report is the JSON and text-rendering payload for output style commands.
type Report struct {
	Kind        string          `json:"kind"`
	Action      string          `json:"action"`
	Status      string          `json:"status"`
	Active      string          `json:"active,omitempty"`
	Query       string          `json:"query,omitempty"`
	Styles      []StyleSummary  `json:"styles,omitempty"`
	Style       *Style          `json:"style,omitempty"`
	Sources     []DiscoveryRoot `json:"sources,omitempty"`
	SourceCount int             `json:"source_count,omitempty"`
	Summary     *HealthSummary  `json:"summary,omitempty"`
	Message     string          `json:"message,omitempty"`
}

// NotFoundError reports that a named output style could not be found.
type NotFoundError struct {
	Name string
}

func (e NotFoundError) Error() string {
	return fmt.Sprintf("output style %q not found", e.Name)
}

type root struct {
	path   string
	source string
}

var builtinStyles = []Style{
	{
		Name:    "concise",
		Source:  "builtin",
		Preview: "Prefer short, direct answers with only necessary detail.",
		Body:    "Prefer short, direct answers. Keep summaries tight, avoid repetition, and include detail only when it changes the decision or next step.",
	},
	{
		Name:    "explanatory",
		Source:  "builtin",
		Preview: "Explain reasoning and tradeoffs before final recommendations.",
		Body:    "Explain reasoning and tradeoffs clearly. When there are alternatives, compare them briefly and make the final recommendation explicit.",
	},
	{
		Name:    "reviewer",
		Source:  "builtin",
		Preview: "Lead with risks, defects, regressions, and missing tests.",
		Body:    "Use a code-review stance. Lead with bugs, regressions, risks, and missing tests, ordered by severity, then give a concise summary.",
	},
}

// StatePath returns the workspace-local path used to persist output style state.
func StatePath(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	return filepath.Join(workspace, ".codog", StateFileName)
}

// List reports every discovered output style with precedence diagnostics.
func List(configHome, workspace string) (Report, error) {
	styles, err := Load(configHome, workspace)
	if err != nil {
		return Report{}, err
	}
	state, err := LoadState(workspace)
	if err != nil {
		return Report{}, err
	}
	return Report{
		Kind:    "output_style",
		Action:  "list",
		Status:  "ok",
		Active:  state.Active,
		Styles:  summarize(styles, state.Active),
		Summary: summarizeStyleHealth(styles),
	}, nil
}

// Search reports output styles whose name, source, preview, or body matches query.
func Search(configHome, workspace, query string) (Report, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Report{}, errors.New("output style search query is required")
	}
	styles, err := Load(configHome, workspace)
	if err != nil {
		return Report{}, err
	}
	filtered := filterStyles(styles, query)
	state, err := LoadState(workspace)
	if err != nil {
		return Report{}, err
	}
	return Report{
		Kind:    "output_style",
		Action:  "search",
		Status:  "ok",
		Active:  state.Active,
		Query:   query,
		Styles:  summarize(filtered, state.Active),
		Summary: summarizeStyleHealth(filtered),
	}, nil
}

// Sources reports the output style catalog roots in effective precedence order.
func Sources(configHome, workspace string) []DiscoveryRoot {
	out := []DiscoveryRoot{{Source: "builtin", Label: "Built-in output styles", Exists: true}}
	for _, root := range roots(configHome, workspace) {
		_, err := os.Stat(root.path)
		out = append(out, DiscoveryRoot{
			Source: root.source,
			Label:  sourceLabel(root.source),
			Path:   root.path,
			Exists: err == nil,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if sourceRank(out[i].Source) == sourceRank(out[j].Source) {
			return out[i].Path < out[j].Path
		}
		return sourceRank(out[i].Source) < sourceRank(out[j].Source)
	})
	return out
}

// Audit reports output style catalog health and shadowing diagnostics.
func Audit(configHome, workspace string) (Report, error) {
	styles, err := Load(configHome, workspace)
	if err != nil {
		return Report{}, err
	}
	state, err := LoadState(workspace)
	if err != nil {
		return Report{}, err
	}
	sources := Sources(configHome, workspace)
	return Report{
		Kind:        "output_style",
		Action:      "audit",
		Status:      "ok",
		Active:      state.Active,
		Styles:      summarize(styles, state.Active),
		Sources:     sources,
		SourceCount: len(sources),
		Summary:     summarizeStyleHealth(styles),
		Message:     "Output style audit passed.",
	}, nil
}

// Show reports the effective output style with the requested name.
func Show(configHome, workspace, name string) (Report, error) {
	style, err := Find(configHome, workspace, name)
	if err != nil {
		return Report{}, err
	}
	state, err := LoadState(workspace)
	if err != nil {
		return Report{}, err
	}
	return Report{
		Kind:   "output_style",
		Action: "show",
		Status: "ok",
		Active: state.Active,
		Style:  &style,
	}, nil
}

// Set selects the effective output style with the requested name for workspace.
func Set(configHome, workspace, name string) (Report, error) {
	style, err := Find(configHome, workspace, name)
	if err != nil {
		return Report{}, err
	}
	if err := SaveState(workspace, State{Active: style.Name}); err != nil {
		return Report{}, err
	}
	return Report{
		Kind:   "output_style",
		Action: "set",
		Status: "ok",
		Active: style.Name,
		Style:  &style,
	}, nil
}

// Clear removes the workspace's selected output style.
func Clear(workspace string) (Report, error) {
	path := StatePath(workspace)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return Report{}, err
	}
	return Report{Kind: "output_style", Action: "clear", Status: "ok"}, nil
}

// Load discovers built-in, user, and workspace output styles.
func Load(configHome, workspace string) ([]Style, error) {
	var styles []Style
	styles = append(styles, builtinStyles...)
	for _, root := range roots(configHome, workspace) {
		entries, err := os.ReadDir(root.path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			path := filepath.Join(root.path, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			body := string(data)
			styles = append(styles, Style{
				Name:    strings.TrimSuffix(entry.Name(), ".md"),
				Source:  root.source,
				Path:    path,
				Preview: preview(body),
				Body:    body,
			})
		}
	}
	sort.Slice(styles, func(i, j int) bool {
		if styles[i].Name == styles[j].Name {
			return sourceRank(styles[i].Source) < sourceRank(styles[j].Source)
		}
		return styles[i].Name < styles[j].Name
	})
	return styles, nil
}

func filterStyles(styles []Style, query string) []Style {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return styles
	}
	out := make([]Style, 0, len(styles))
	for _, style := range styles {
		if strings.Contains(strings.ToLower(style.Name), query) ||
			strings.Contains(strings.ToLower(style.Source), query) ||
			strings.Contains(strings.ToLower(style.Preview), query) ||
			strings.Contains(strings.ToLower(style.Body), query) {
			out = append(out, style)
		}
	}
	return out
}

// Find returns the effective output style with the requested name.
func Find(configHome, workspace, name string) (Style, error) {
	name, err := cleanName(name)
	if err != nil {
		return Style{}, err
	}
	for _, root := range rootsByPrecedence(configHome, workspace) {
		path := filepath.Join(root.path, name+".md")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return Style{}, err
		}
		body := string(data)
		return Style{Name: name, Source: root.source, Path: path, Preview: preview(body), Body: body}, nil
	}
	for _, style := range builtinStyles {
		if style.Name == name {
			return style, nil
		}
	}
	return Style{}, NotFoundError{Name: name}
}

// LoadState reads the workspace's persisted output style selection.
func LoadState(workspace string) (State, error) {
	data, err := os.ReadFile(StatePath(workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return State{Kind: "output_style"}, nil
		}
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	if state.Kind != "output_style" {
		return State{}, errors.New("output style state kind is invalid")
	}
	state.Active = strings.TrimSpace(state.Active)
	return state, nil
}

// SaveState persists the workspace's output style selection atomically.
func SaveState(workspace string, state State) error {
	state.Kind = "output_style"
	state.Active = strings.TrimSpace(state.Active)
	state.UpdatedAt = time.Now().UTC()
	path := StatePath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".output-style-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// RenderPrompt renders the selected output style as system-prompt context.
func RenderPrompt(configHome, workspace string) string {
	state, err := LoadState(workspace)
	if err != nil || state.Active == "" {
		return ""
	}
	style, err := Find(configHome, workspace, state.Active)
	if err != nil {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("<output_style name=\"")
	builder.WriteString(escapeAttr(style.Name))
	builder.WriteString("\" source=\"")
	builder.WriteString(escapeAttr(style.Source))
	builder.WriteString("\">\n")
	builder.WriteString(strings.TrimSpace(style.Body))
	builder.WriteString("\n</output_style>")
	return builder.String()
}

// RenderText writes a human-readable output style report.
func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Output Style")
	fmt.Fprintf(w, "  Active           %s\n", valueOrNone(report.Active))
	if report.Query != "" {
		fmt.Fprintf(w, "  Query            %s\n", report.Query)
	}
	if report.Summary != nil {
		fmt.Fprintf(w, "  Styles           %d\n", report.Summary.Total)
		fmt.Fprintf(w, "  Effective        %d\n", report.Summary.Effective)
		fmt.Fprintf(w, "  Shadowed         %d\n", report.Summary.Shadowed)
	}
	if len(report.Sources) != 0 {
		fmt.Fprintf(w, "  Sources          %d\n", report.SourceCount)
	}
	if report.Message != "" {
		fmt.Fprintf(w, "  Message          %s\n", report.Message)
	}
	if report.Style != nil {
		fmt.Fprintf(w, "  Selected         %s (%s)\n", report.Style.Name, report.Style.Source)
		if report.Style.Path != "" {
			fmt.Fprintf(w, "  Path             %s\n", report.Style.Path)
		}
		fmt.Fprintln(w, "Body")
		fmt.Fprintln(w, strings.TrimSpace(report.Style.Body))
		return
	}
	if len(report.Styles) == 0 {
		fmt.Fprintln(w, "  Styles           none")
		return
	}
	fmt.Fprintln(w, "Styles")
	for _, style := range report.Styles {
		marker := " "
		if style.Active {
			marker = "*"
		}
		status := "effective"
		if style.ShadowedBy != "" {
			status = "shadowed by " + style.ShadowedBy
		}
		fmt.Fprintf(w, "  %s %s\t%s\t%s\t%s\n", marker, style.Name, style.Source, status, style.Preview)
	}
	if len(report.Sources) != 0 {
		fmt.Fprintln(w, "Sources")
		for _, root := range report.Sources {
			state := "missing"
			if root.Exists {
				state = "present"
			}
			path := root.Path
			if path == "" {
				path = "builtin://output-styles"
			}
			fmt.Fprintf(w, "  %-11s %-8s %s\n", root.Source, state, path)
		}
	}
}

func roots(configHome, workspace string) []root {
	return []root{
		{filepath.Join(configHome, "output-styles"), "user"},
		{filepath.Join(workspace, ".codog", "output-styles"), "workspace"},
	}
}

func rootsByPrecedence(configHome, workspace string) []root {
	return []root{
		{filepath.Join(workspace, ".codog", "output-styles"), "workspace"},
		{filepath.Join(configHome, "output-styles"), "user"},
	}
}

func summarize(styles []Style, active string) []StyleSummary {
	out := make([]StyleSummary, 0, len(styles))
	winners := map[string]Style{}
	for _, style := range styles {
		key := strings.ToLower(strings.TrimSpace(style.Name))
		summary := StyleSummary{
			Name:    style.Name,
			Source:  style.Source,
			Path:    style.Path,
			Preview: style.Preview,
			Active:  style.Name == active,
		}
		if winner, ok := winners[key]; ok {
			summary.ShadowedBy = winner.Source
			summary.ShadowedByPath = winner.Path
		} else {
			winners[key] = style
			summary.Effective = true
		}
		out = append(out, summary)
	}
	return out
}

func summarizeStyleHealth(styles []Style) *HealthSummary {
	summaries := summarize(styles, "")
	report := &HealthSummary{Total: len(styles)}
	for _, style := range summaries {
		if style.Effective {
			report.Effective++
		}
		if style.ShadowedBy != "" {
			report.Shadowed++
		}
	}
	return report
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("output style name is required")
	}
	if strings.Contains(name, "/") || strings.Contains(name, string(filepath.Separator)) || name == "." || name == ".." || strings.Contains(name, "..") {
		return "", fmt.Errorf("invalid output style name %q", name)
	}
	return name, nil
}

func preview(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return "<empty>"
}

func sourceRank(source string) int {
	switch source {
	case "workspace":
		return 0
	case "user":
		return 1
	case "builtin":
		return 2
	default:
		return 3
	}
}

func sourceLabel(source string) string {
	switch source {
	case "user":
		return "User output styles"
	case "workspace":
		return "Workspace output styles"
	case "builtin":
		return "Built-in output styles"
	default:
		return source
	}
}

func valueOrNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return value
}

func escapeAttr(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "\"", "&quot;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}
