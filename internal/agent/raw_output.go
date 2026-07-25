package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Rememorio/codog/internal/config"
)

const rawOutputUsage = "codog raw [status|on|off|toggle] [--target user|project|local] [--output-format text|json]"

type rawOutputRequest struct {
	Action string
	Format string
	Target string
	Path   string
}

type rawOutputReport struct {
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Enabled    bool   `json:"enabled"`
	ConfigPath string `json:"config_path,omitempty"`
}

// RawOutput reports or changes copy-friendly TUI transcript rendering.
func (a *App) RawOutput(args []string) error {
	request, err := parseRawOutputArgs(args, "status")
	if err != nil {
		return err
	}
	report, err := a.applyRawOutput(request)
	if err != nil {
		return err
	}
	return renderRawOutputReport(a.Out, request.Format, report)
}

func parseRawOutputArgs(args []string, defaultAction string) (rawOutputRequest, error) {
	request := rawOutputRequest{Action: defaultAction, Format: "text", Target: "user"}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			request.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return request, missingFlagValueError{Command: "raw", Flag: arg, Usage: rawOutputUsage}
			}
			request.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			request.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return request, missingFlagValueError{Command: "raw", Flag: arg, Usage: rawOutputUsage}
			}
			request.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			request.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return request, missingFlagValueError{Command: "raw", Flag: arg, Usage: rawOutputUsage}
			}
			request.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			request.Path = strings.TrimPrefix(arg, "--path=")
		case strings.HasPrefix(arg, "-"):
			return request, fmt.Errorf("unknown raw flag %q\nusage: %s", arg, rawOutputUsage)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		return request, fmt.Errorf("too many raw arguments\nusage: %s", rawOutputUsage)
	}
	if len(positionals) == 1 {
		request.Action = strings.ToLower(strings.TrimSpace(positionals[0]))
	}
	switch request.Action {
	case "status", "on", "off", "toggle":
	default:
		return request, fmt.Errorf("unknown raw action %q\nusage: %s", request.Action, rawOutputUsage)
	}
	request.Format = strings.ToLower(strings.TrimSpace(request.Format))
	if request.Format != "text" && request.Format != "json" {
		return request, fmt.Errorf("unsupported output format %q", request.Format)
	}
	return request, nil
}

func (a *App) applyRawOutput(request rawOutputRequest) (rawOutputReport, error) {
	enabled := rawOutputEnabled(a.Config.TUIRawOutputMode)
	report := rawOutputReport{Kind: "raw_output", Action: request.Action, Status: "ok", Enabled: enabled}
	if request.Action == "status" {
		return report, nil
	}
	switch request.Action {
	case "on":
		enabled = true
	case "off":
		enabled = false
	case "toggle":
		enabled = !enabled
	}
	path, err := a.preferenceConfigPath(request.Target, request.Path)
	if err != nil {
		return report, err
	}
	if _, err := config.SetFileValue(path, "tui_raw_output_mode", enabled); err != nil {
		return report, err
	}
	a.Config.TUIRawOutputMode = &enabled
	report.Enabled = enabled
	report.ConfigPath = path
	return report, nil
}

func rawOutputEnabled(value *bool) bool {
	return value != nil && *value
}

func renderRawOutputReport(out io.Writer, format string, report rawOutputReport) error {
	if format == "json" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Codog Raw Output")
	fmt.Fprintf(out, "  Enabled      %t\n", report.Enabled)
	if report.ConfigPath != "" {
		fmt.Fprintf(out, "  Config       %s\n", report.ConfigPath)
	}
	return nil
}
