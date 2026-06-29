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

const StateFileName = "output-style.json"

type Style struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Path    string `json:"path,omitempty"`
	Preview string `json:"preview"`
	Body    string `json:"body,omitempty"`
}

type StyleSummary struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Path    string `json:"path,omitempty"`
	Preview string `json:"preview"`
	Active  bool   `json:"active,omitempty"`
}

type State struct {
	Kind      string    `json:"kind"`
	Active    string    `json:"active,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Report struct {
	Kind   string         `json:"kind"`
	Action string         `json:"action"`
	Status string         `json:"status"`
	Active string         `json:"active,omitempty"`
	Styles []StyleSummary `json:"styles,omitempty"`
	Style  *Style         `json:"style,omitempty"`
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

func StatePath(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	return filepath.Join(workspace, ".codog", StateFileName)
}

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
		Kind:   "output_style",
		Action: "list",
		Status: "ok",
		Active: state.Active,
		Styles: summarize(styles, state.Active),
	}, nil
}

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

func Clear(workspace string) (Report, error) {
	path := StatePath(workspace)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return Report{}, err
	}
	return Report{Kind: "output_style", Action: "clear", Status: "ok"}, nil
}

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
	return Style{}, fmt.Errorf("output style %q not found", name)
}

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
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

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

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Output Style")
	fmt.Fprintf(w, "  Active           %s\n", valueOrNone(report.Active))
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
		fmt.Fprintf(w, "  %s %s\t%s\t%s\n", marker, style.Name, style.Source, style.Preview)
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
	for _, style := range styles {
		out = append(out, StyleSummary{
			Name:    style.Name,
			Source:  style.Source,
			Path:    style.Path,
			Preview: style.Preview,
			Active:  style.Name == active,
		})
	}
	return out
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
	default:
		return 2
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
