package future

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type Status string

const (
	StatusAvailable    Status = "available"
	StatusExperimental Status = "experimental"
	StatusPlanned      Status = "planned"
)

type Surface struct {
	Name        string   `json:"name"`
	Command     string   `json:"command"`
	Status      Status   `json:"status"`
	Horizon     string   `json:"horizon"`
	Description string   `json:"description"`
	DependsOn   []string `json:"depends_on,omitempty"`
	NextSteps   []string `json:"next_steps"`
}

type Report struct {
	Project  string    `json:"project"`
	Version  string    `json:"version"`
	Surfaces []Surface `json:"surfaces"`
}

func Surfaces() []Surface {
	return []Surface{
		{
			Name:        "IDE bridge",
			Command:     "bridge",
			Status:      StatusExperimental,
			Horizon:     "6-12 months",
			Description: "Stdio JSON-RPC bridge is available; editor-specific integrations remain planned.",
			DependsOn:   []string{"remote", "enterprise"},
			NextSteps: []string{
				"Define a local JSON-RPC control plane.",
				"Add editor identity and workspace trust checks.",
				"Map file open, selection, diagnostics, and diff actions.",
			},
		},
		{
			Name:        "Remote sessions",
			Command:     "remote",
			Status:      StatusExperimental,
			Horizon:     "6-12 months",
			Description: "Local HTTP session control server is available; durable remote transport remains planned.",
			DependsOn:   []string{"background", "enterprise"},
			NextSteps: []string{
				"Add session control API above terminal transport.",
				"Persist heartbeat and last-error state.",
				"Support reconnect and structured failure reasons.",
			},
		},
		{
			Name:        "Multi-agent orchestration",
			Command:     "agents",
			Status:      StatusAvailable,
			Horizon:     "6-12 months",
			Description: "Local agent spec inventory under .codog/agents, with future worker orchestration planned.",
			DependsOn:   []string{"background", "sandbox"},
			NextSteps: []string{
				"Define agent spec files under .codog/agents.",
				"Add local worker lifecycle registry.",
				"Route task packets to isolated worktrees.",
			},
		},
		{
			Name:        "Background tasks",
			Command:     "background",
			Status:      StatusAvailable,
			Horizon:     "6-12 months",
			Description: "Local background command registry with persisted metadata and log files.",
			DependsOn:   []string{"sandbox"},
			NextSteps: []string{
				"Add process supervision.",
				"Persist task output and exit status.",
				"Expose watch and stop commands.",
			},
		},
		{
			Name:        "Notebook and LSP tools",
			Command:     "code-intel",
			Status:      StatusExperimental,
			Horizon:     "6-12 months",
			Description: "Go symbol scanning and notebook cell editing; full LSP lifecycle remains planned.",
			DependsOn:   []string{"sandbox"},
			NextSteps: []string{
				"Add ipynb cell edit operations.",
				"Define LSP server discovery and lifecycle.",
				"Normalize diagnostics into tool results.",
			},
		},
		{
			Name:        "OAuth",
			Command:     "oauth",
			Status:      StatusExperimental,
			Horizon:     "6-12 months",
			Description: "PKCE helper is available; browser/device authorization and token storage remain planned.",
			NextSteps: []string{
				"Add PKCE helper.",
				"Store refresh tokens in OS keychain when available.",
				"Refresh tokens before model requests.",
			},
		},
		{
			Name:        "Enterprise policy",
			Command:     "enterprise",
			Status:      StatusExperimental,
			Horizon:     "6-12 months",
			Description: "Managed policy file can cap permission mode and inject permission rules; local permission and tool-use audit is available.",
			DependsOn:   []string{"oauth"},
			NextSteps: []string{
				"Add signed managed-settings file support.",
				"Forward audit events to managed collectors.",
				"Add organization-scoped policy precedence.",
			},
		},
		{
			Name:        "Plugin marketplace",
			Command:     "marketplace",
			Status:      StatusExperimental,
			Horizon:     "6-12 months",
			Description: "Local plugin manifest inventory under .codog/plugins; remote marketplace remains planned.",
			DependsOn:   []string{"enterprise", "sandbox"},
			NextSteps: []string{
				"Define plugin manifest.",
				"Add local install/enable/disable flows.",
				"Add trust and signature policy before remote installs.",
			},
		},
		{
			Name:        "Cross-platform sandbox",
			Command:     "sandbox",
			Status:      StatusExperimental,
			Horizon:     "6-12 months",
			Description: "OS strategy detection is available; enforced isolation remains planned.",
			NextSteps: []string{
				"Add macOS sandbox-exec profile generation.",
				"Add Linux namespace/bubblewrap strategy.",
				"Add Windows restricted process strategy.",
			},
		},
		{
			Name:        "Updater",
			Command:     "updater",
			Status:      StatusExperimental,
			Horizon:     "6-12 months",
			Description: "Manifest check is available; verified binary replacement remains planned.",
			NextSteps: []string{
				"Add version manifest format.",
				"Verify checksums before replacement.",
				"Keep package-manager installs out of self-update.",
			},
		},
	}
}

func NewReport(version string) Report {
	return Report{
		Project:  "codog",
		Version:  version,
		Surfaces: Surfaces(),
	}
}

func Find(command string) (Surface, bool) {
	for _, surface := range Surfaces() {
		if strings.EqualFold(surface.Command, command) {
			return surface, true
		}
	}
	return Surface{}, false
}

func RenderText(w io.Writer, surfaces []Surface) {
	for _, surface := range surfaces {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", surface.Command, surface.Status, surface.Horizon, surface.Description)
		for _, step := range surface.NextSteps {
			fmt.Fprintf(w, "  - %s\n", step)
		}
	}
}

func RenderJSON(w io.Writer, surfaces []Surface) error {
	data, err := json.MarshalIndent(surfaces, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

func RenderReportJSON(w io.Writer, report Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}
