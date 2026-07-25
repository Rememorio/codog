package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Rememorio/codog/internal/tui"
	"github.com/Rememorio/codog/internal/visualization"
)

const visualizeUsage = "codog visualize [list|path|show|open] [FILE] [--output-format text|json]"

type visualizeRequest struct {
	Action string
	File   string
	Format string
}

type visualizeReport struct {
	Kind      string               `json:"kind"`
	Action    string               `json:"action"`
	Status    string               `json:"status"`
	SourceDir string               `json:"source_dir"`
	Opener    string               `json:"opener,omitempty"`
	Item      *visualization.Item  `json:"item,omitempty"`
	Items     []visualization.Item `json:"items,omitempty"`
}

// Visualize lists, materializes, or opens local sandboxed visualizations.
func (a *App) Visualize(args []string) error {
	request, err := parseVisualizeArgs(args)
	if err != nil {
		return err
	}
	manager := a.visualizationManager()
	report := visualizeReport{
		Kind:      "visualization",
		Action:    request.Action,
		Status:    "ok",
		SourceDir: manager.SourceDir(),
	}
	switch request.Action {
	case "path":
	case "list":
		report.Items, err = manager.List()
	case "show", "open":
		var item visualization.Item
		item, err = manager.Materialize(request.File, "")
		if err == nil {
			report.Item = &item
		}
		if err == nil && request.Action == "open" {
			report.Opener, err = openSystemURL(item.URL)
		}
	default:
		return fmt.Errorf("unknown visualize action %q", request.Action)
	}
	if err != nil {
		return err
	}
	return renderVisualizeReport(a.Out, request.Format, report)
}

func parseVisualizeArgs(args []string) (visualizeRequest, error) {
	request := visualizeRequest{Action: "list", Format: "text"}
	positionals := make([]string, 0, 2)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			request.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return request, missingFlagValueError{Command: "visualize", Flag: arg, Usage: visualizeUsage}
			}
			request.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			request.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return request, fmt.Errorf("unknown visualize flag %q\nusage: %s", arg, visualizeUsage)
		default:
			positionals = append(positionals, arg)
		}
	}
	request.Format = strings.ToLower(strings.TrimSpace(request.Format))
	if request.Format != "text" && request.Format != "json" {
		return request, fmt.Errorf("unsupported output format %q", request.Format)
	}
	if len(positionals) > 0 {
		request.Action = strings.ToLower(positionals[0])
	}
	if len(positionals) > 1 {
		request.File = positionals[1]
	}
	if len(positionals) > 2 {
		return request, fmt.Errorf("too many visualize arguments\nusage: %s", visualizeUsage)
	}
	if request.Action == "show" || request.Action == "open" {
		if strings.TrimSpace(request.File) == "" {
			return request, fmt.Errorf("visualize %s requires FILE\nusage: %s", request.Action, visualizeUsage)
		}
	} else if request.File != "" {
		return request, fmt.Errorf("visualize %s does not accept FILE\nusage: %s", request.Action, visualizeUsage)
	}
	return request, nil
}

func renderVisualizeReport(out io.Writer, format string, report visualizeReport) error {
	if format == "json" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Codog Visualizations")
	fmt.Fprintf(out, "  Source directory  %s\n", report.SourceDir)
	if report.Item != nil {
		fmt.Fprintf(out, "  File              %s\n", report.Item.File)
		fmt.Fprintf(out, "  Viewer            %s\n", report.Item.ViewerPath)
		fmt.Fprintf(out, "  URL               %s\n", report.Item.URL)
	}
	if report.Opener != "" {
		fmt.Fprintf(out, "  Opened with       %s\n", report.Opener)
	}
	for _, item := range report.Items {
		fmt.Fprintf(out, "  %-17s %s\n", item.File, item.URL)
	}
	if report.Action == "list" && len(report.Items) == 0 {
		fmt.Fprintln(out, "  No visualizations.")
	}
	return nil
}

func (a *App) visualizationManager() visualization.Manager {
	return visualization.Manager{Workspace: a.Workspace, ConfigHome: a.Config.ConfigHome}
}

func (a *App) rewriteVisualizationMarkdown(value string) (string, bool) {
	if a == nil {
		return value, false
	}
	return a.visualizationManager().RewriteMarkdown(value)
}

func (a *App) rewriteVisualizationEntries(entries []tui.Entry) []tui.Entry {
	if a == nil {
		return append([]tui.Entry(nil), entries...)
	}
	out := append([]tui.Entry(nil), entries...)
	for index := range out {
		if !strings.EqualFold(out[index].Role, "assistant") {
			continue
		}
		if rewritten, changed := a.rewriteVisualizationMarkdown(out[index].Text); changed {
			out[index].Text = rewritten
		}
	}
	return out
}
