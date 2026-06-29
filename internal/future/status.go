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
	NextSteps   []string `json:"next_steps"`
}

func Surfaces() []Surface {
	return []Surface{
		{
			Name:        "IDE bridge",
			Command:     "bridge",
			Status:      StatusPlanned,
			Horizon:     "6-12 months",
			Description: "Editor protocol bridge for VS Code, JetBrains, and other clients.",
			NextSteps: []string{
				"Define a local JSON-RPC control plane.",
				"Add editor identity and workspace trust checks.",
				"Map file open, selection, diagnostics, and diff actions.",
			},
		},
		{
			Name:        "Remote sessions",
			Command:     "remote",
			Status:      StatusPlanned,
			Horizon:     "6-12 months",
			Description: "Durable remote agent sessions with resumable transport and explicit lifecycle state.",
			NextSteps: []string{
				"Add session control API above terminal transport.",
				"Persist heartbeat and last-error state.",
				"Support reconnect and structured failure reasons.",
			},
		},
		{
			Name:        "Multi-agent orchestration",
			Command:     "agents",
			Status:      StatusExperimental,
			Horizon:     "6-12 months",
			Description: "Coordinator-facing agent inventory and future worker orchestration surface.",
			NextSteps: []string{
				"Define agent spec files under .codog/agents.",
				"Add local worker lifecycle registry.",
				"Route task packets to isolated worktrees.",
			},
		},
		{
			Name:        "Background tasks",
			Command:     "background",
			Status:      StatusPlanned,
			Horizon:     "6-12 months",
			Description: "Long-running task registry with logs, cancellation, and completion events.",
			NextSteps: []string{
				"Add process supervision.",
				"Persist task output and exit status.",
				"Expose watch and stop commands.",
			},
		},
		{
			Name:        "Notebook and LSP tools",
			Command:     "code-intel",
			Status:      StatusPlanned,
			Horizon:     "6-12 months",
			Description: "Notebook editing and language-server-backed diagnostics, symbols, hover, and references.",
			NextSteps: []string{
				"Add ipynb cell edit operations.",
				"Define LSP server discovery and lifecycle.",
				"Normalize diagnostics into tool results.",
			},
		},
		{
			Name:        "OAuth",
			Command:     "oauth",
			Status:      StatusPlanned,
			Horizon:     "6-12 months",
			Description: "Browser/device authorization flow for providers that support OAuth tokens.",
			NextSteps: []string{
				"Add PKCE helper.",
				"Store refresh tokens in OS keychain when available.",
				"Refresh tokens before model requests.",
			},
		},
		{
			Name:        "Enterprise policy",
			Command:     "enterprise",
			Status:      StatusPlanned,
			Horizon:     "6-12 months",
			Description: "Managed settings, policy checks, audit records, and organization guardrails.",
			NextSteps: []string{
				"Define policy schema.",
				"Add signed managed-settings file support.",
				"Emit audit events for tool and permission decisions.",
			},
		},
		{
			Name:        "Plugin marketplace",
			Command:     "marketplace",
			Status:      StatusPlanned,
			Horizon:     "6-12 months",
			Description: "Installable plugin index for tools, commands, hooks, and skills.",
			NextSteps: []string{
				"Define plugin manifest.",
				"Add local install/enable/disable flows.",
				"Add trust and signature policy before remote installs.",
			},
		},
		{
			Name:        "Cross-platform sandbox",
			Command:     "sandbox",
			Status:      StatusPlanned,
			Horizon:     "6-12 months",
			Description: "OS-specific filesystem and network isolation for tool execution.",
			NextSteps: []string{
				"Add macOS sandbox-exec profile generation.",
				"Add Linux namespace/bubblewrap strategy.",
				"Add Windows restricted process strategy.",
			},
		},
		{
			Name:        "Updater",
			Command:     "updater",
			Status:      StatusPlanned,
			Horizon:     "6-12 months",
			Description: "Release check, binary provenance, and safe self-update flow.",
			NextSteps: []string{
				"Add version manifest format.",
				"Verify checksums before replacement.",
				"Keep package-manager installs out of self-update.",
			},
		},
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
