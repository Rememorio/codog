package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Rememorio/codog/internal/companion"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/tui"
)

const petsUsage = "codog pets [list|status|use ID|off|example] [--output PATH] [--target user|project|local] [--output-format text|json]"

type petsRequest struct {
	Action string
	ID     string
	Format string
	Output string
	Target string
	Path   string
}

type petsReport struct {
	Kind         string                   `json:"kind"`
	Action       string                   `json:"action"`
	Status       string                   `json:"status"`
	Selected     string                   `json:"selected"`
	Enabled      bool                     `json:"enabled"`
	ConfigPath   string                   `json:"config_path,omitempty"`
	ManifestPath string                   `json:"manifest_path,omitempty"`
	Companions   []companion.CatalogEntry `json:"companions,omitempty"`
}

// Pets lists and configures opt-in local terminal companions.
func (a *App) Pets(args []string) error {
	request, err := parsePetsArgs(args)
	if err != nil {
		return err
	}
	report, _, err := a.applyPets(request)
	if err != nil {
		return err
	}
	return renderPetsReport(a.Out, request.Format, report)
}

func parsePetsArgs(args []string) (petsRequest, error) {
	request := petsRequest{Action: "list", Format: "text", Target: "user"}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			request.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return request, missingFlagValueError{Command: "pets", Flag: arg, Usage: petsUsage}
			}
			request.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			request.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--output":
			index++
			if index >= len(args) {
				return request, missingFlagValueError{Command: "pets", Flag: arg, Usage: petsUsage}
			}
			request.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			request.Output = strings.TrimPrefix(arg, "--output=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return request, missingFlagValueError{Command: "pets", Flag: arg, Usage: petsUsage}
			}
			request.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			request.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return request, missingFlagValueError{Command: "pets", Flag: arg, Usage: petsUsage}
			}
			request.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			request.Path = strings.TrimPrefix(arg, "--path=")
		case strings.HasPrefix(arg, "-"):
			return request, fmt.Errorf("unknown pets flag %q\nusage: %s", arg, petsUsage)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 0 {
		request.Action = strings.ToLower(strings.TrimSpace(positionals[0]))
	}
	if len(positionals) > 1 {
		request.ID = strings.ToLower(strings.TrimSpace(positionals[1]))
	}
	if len(positionals) > 2 {
		return request, fmt.Errorf("too many pets arguments\nusage: %s", petsUsage)
	}
	if request.Action == "use" && request.ID == "" {
		return request, fmt.Errorf("pets use requires ID\nusage: %s", petsUsage)
	}
	if request.Action != "use" && request.ID != "" {
		return request, fmt.Errorf("pets %s does not accept ID\nusage: %s", request.Action, petsUsage)
	}
	switch request.Action {
	case "list", "status", "use", "off", "example":
	default:
		return request, fmt.Errorf("unknown pets action %q\nusage: %s", request.Action, petsUsage)
	}
	request.Format = strings.ToLower(strings.TrimSpace(request.Format))
	if request.Format != "text" && request.Format != "json" {
		return request, fmt.Errorf("unsupported output format %q", request.Format)
	}
	return request, nil
}

func (a *App) applyPets(request petsRequest) (petsReport, *companion.Manifest, error) {
	selected := strings.ToLower(strings.TrimSpace(a.Config.TUIPet))
	report := petsReport{
		Kind:       "pets",
		Action:     request.Action,
		Status:     "ok",
		Selected:   selected,
		Enabled:    selected != "" && selected != companion.DisabledID,
		Companions: companion.List(a.Config.ConfigHome),
	}
	switch request.Action {
	case "list":
		return report, nil, nil
	case "status":
		loaded, err := companion.Load(a.Config.ConfigHome, selected)
		return report, loaded, err
	case "example":
		path, err := a.writeCompanionExample(request.Output)
		report.ManifestPath = path
		return report, nil, err
	case "use":
		loaded, err := companion.Load(a.Config.ConfigHome, request.ID)
		if err != nil {
			return report, nil, err
		}
		path, err := a.persistPetPreference(request, request.ID)
		if err != nil {
			return report, nil, err
		}
		a.Config.TUIPet = request.ID
		report.Selected, report.Enabled, report.ConfigPath = request.ID, true, path
		return report, loaded, nil
	case "off":
		path, err := a.persistPetPreference(request, companion.DisabledID)
		if err != nil {
			return report, nil, err
		}
		a.Config.TUIPet = companion.DisabledID
		report.Selected, report.Enabled, report.ConfigPath = companion.DisabledID, false, path
		return report, nil, nil
	default:
		return report, nil, fmt.Errorf("unknown pets action %q", request.Action)
	}
}

func (a *App) persistPetPreference(request petsRequest, value string) (string, error) {
	path, err := a.preferenceConfigPath(request.Target, request.Path)
	if err != nil {
		return "", err
	}
	if _, err := config.SetFileValue(path, "tui_pet", value); err != nil {
		return "", err
	}
	return path, nil
}

func (a *App) writeCompanionExample(output string) (string, error) {
	path := strings.TrimSpace(output)
	if path == "" {
		path = filepath.Join(a.Config.ConfigHome, "pets", "helper", "pet.json")
	} else {
		path = a.resolveOutputPath(path)
	}
	if _, err := os.Stat(path); err == nil {
		return path, fmt.Errorf("companion manifest already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return path, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return path, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return path, err
	}
	writeErr := companion.WriteExample(file)
	closeErr := file.Close()
	return path, errors.Join(writeErr, closeErr)
}

func renderPetsReport(out io.Writer, format string, report petsReport) error {
	if format == "json" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Codog Terminal Companions")
	fmt.Fprintf(out, "  Selected     %s\n", firstNonEmpty(report.Selected, "off"))
	fmt.Fprintf(out, "  Enabled      %t\n", report.Enabled)
	for _, item := range report.Companions {
		source := "local"
		if item.Builtin {
			source = "built-in"
		}
		fmt.Fprintf(out, "  %-12s %-20s %s\n", item.ID, item.Name, source)
	}
	if report.ManifestPath != "" {
		fmt.Fprintf(out, "  Manifest     %s\n", report.ManifestPath)
	}
	if report.ConfigPath != "" {
		fmt.Fprintf(out, "  Config       %s\n", report.ConfigPath)
	}
	return nil
}

func (a *App) tuiPetPickerView() tui.CommandView {
	selected := strings.ToLower(strings.TrimSpace(a.Config.TUIPet))
	items := []tui.CommandViewItem{{
		Label:       "Off",
		Value:       selectedMarker(selected == "" || selected == companion.DisabledID),
		Description: "Hide the terminal companion.",
		Command:     "/pets off",
	}}
	for _, item := range companion.List(a.Config.ConfigHome) {
		items = append(items, tui.CommandViewItem{
			Label:       item.Name,
			Value:       selectedMarker(selected == item.ID),
			Description: companionSource(item),
			Command:     "/pets use " + item.ID,
		})
	}
	return tui.CommandView{
		Title: "Terminal Companion",
		Tabs: []tui.CommandViewTab{{
			Title: "Pets",
			Lines: []string{
				"Optional, local, and hidden in narrow terminals.",
				"Custom manifests live under the Codog config home.",
			},
			Items: items,
		}},
	}
}

func selectedMarker(selected bool) string {
	if selected {
		return "selected"
	}
	return ""
}

func companionSource(item companion.CatalogEntry) string {
	if item.Builtin {
		return "Built-in ASCII companion; no download required."
	}
	return "Local custom manifest."
}
