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
			Description: "Stdio JSON-RPC bridge exposes trusted editor identity, open-file and selection state, sessions, workspace info, file listing/search/diff/read/write/edit RPCs, Go diagnostics, and background watch notifications.",
			DependsOn:   []string{"remote", "enterprise"},
			NextSteps: []string{
				"Package VS Code and JetBrains extensions.",
				"Add IDE-side action routing for diagnostics and diffs.",
				"Add editor-initiated prompt context packets.",
			},
		},
		{
			Name:        "Remote sessions",
			Command:     "remote",
			Status:      StatusExperimental,
			Horizon:     "6-12 months",
			Description: "Local HTTP control server exposes sessions, background tasks, logs/watch streams, Go diagnostics, bearer-token auth, heartbeat leases, and structured failure state; durable remote transport remains planned.",
			DependsOn:   []string{"background", "enterprise"},
			NextSteps: []string{
				"Add authenticated terminal/websocket transport.",
				"Add durable reconnect transport.",
				"Add remote connection backoff policy.",
			},
		},
		{
			Name:        "Multi-agent orchestration",
			Command:     "agents",
			Status:      StatusAvailable,
			Horizon:     "6-12 months",
			Description: "Local agent specs under .codog/agents can launch named session-attached background prompt workers, optionally isolated in git worktrees.",
			DependsOn:   []string{"background", "sandbox"},
			NextSteps: []string{
				"Add parent-child session aggregation.",
				"Add task packet routing between parent and worker agents.",
				"Add per-agent resource and policy limits.",
			},
		},
		{
			Name:        "Background tasks",
			Command:     "background",
			Status:      StatusAvailable,
			Horizon:     "6-12 months",
			Description: "Local background command registry supports run/list/status/stop/restart/logs/watch/prune/supervise with persisted metadata, session IDs, restart policies, log files, HTTP streams, bridge notifications, retention cleanup, and NDJSON status/log events.",
			DependsOn:   []string{"sandbox"},
			NextSteps: []string{
				"Add session task summaries and parent-child aggregation.",
				"Add health probes and backoff policies.",
				"Add resource-aware restart limits.",
			},
		},
		{
			Name:        "Notebook and LSP tools",
			Command:     "code-intel",
			Status:      StatusExperimental,
			Horizon:     "6-12 months",
			Description: "Go symbol scanning, Go test diagnostics, notebook cell editing, and local LSP discovery/start/list/status/stop lifecycle management are available.",
			DependsOn:   []string{"sandbox"},
			NextSteps: []string{
				"Proxy LSP JSON-RPC requests and responses.",
				"Normalize LSP diagnostics into tool results.",
				"Stream diagnostics through IDE bridges.",
			},
		},
		{
			Name:        "OAuth",
			Command:     "oauth",
			Status:      StatusExperimental,
			Horizon:     "6-12 months",
			Description: "PKCE helper, provider metadata discovery, device authorization, and keychain-backed token storage with local file fallback are available; browser authorization remains planned.",
			NextSteps: []string{
				"Add localhost browser authorization callback flow.",
				"Refresh tokens before model requests when provider metadata is configured.",
				"Persist provider metadata profiles.",
			},
		},
		{
			Name:        "Enterprise policy",
			Command:     "enterprise",
			Status:      StatusExperimental,
			Horizon:     "6-12 months",
			Description: "Managed policy files can cap permission mode, inject permission rules, require Ed25519 signatures, and expose local permission/tool-use audit.",
			DependsOn:   []string{"oauth"},
			NextSteps: []string{
				"Add policy key rotation and organization trust pinning.",
				"Forward audit events to managed collectors.",
				"Add organization-scoped policy precedence.",
			},
		},
		{
			Name:        "Plugin marketplace",
			Command:     "marketplace",
			Status:      StatusExperimental,
			Horizon:     "6-12 months",
			Description: "Local plugin inventory supports install, update, enable, disable, remove, remote index fetching, signed index verification, and SHA-256 verified remote zip installs.",
			DependsOn:   []string{"enterprise", "sandbox"},
			NextSteps: []string{
				"Add organization-scoped marketplace allowlists.",
				"Add plugin compatibility and version constraints.",
				"Add plugin release channels and rollback pruning.",
			},
		},
		{
			Name:        "Cross-platform sandbox",
			Command:     "sandbox",
			Status:      StatusExperimental,
			Horizon:     "6-12 months",
			Description: "OS strategy detection is available; bash execution can opt into detected macOS/Linux wrappers.",
			NextSteps: []string{
				"Harden macOS sandbox-exec profiles.",
				"Harden Linux namespace/bubblewrap mounts.",
				"Add Windows restricted process strategy.",
			},
		},
		{
			Name:        "Updater",
			Command:     "updater",
			Status:      StatusExperimental,
			Horizon:     "6-12 months",
			Description: "Manifest check, Ed25519 signed manifest verification, SHA-256 verified artifact download, and backup-based binary install/rollback are available.",
			NextSteps: []string{
				"Add platform-specific package manager handoff.",
				"Keep package-manager installs out of self-update.",
				"Add automatic update policy scheduling.",
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
