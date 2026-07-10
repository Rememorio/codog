package bridgeparity

import (
	"sort"
	"strings"

	"github.com/Rememorio/codog/internal/bridge"
	"github.com/Rememorio/codog/internal/control"
	"github.com/Rememorio/codog/internal/remote"
)

var requiredBridgeMethods = []string{
	"sessions/list",
	"sessions/open",
	"sessions/get",
	"sessions/append_message",
	"sessions/prompt",
	"workspace/info",
	"workspace/files",
	"workspace/search",
	"file/read",
	"file/write",
	"file/edit",
	"file/diff",
	"editor/identify",
	"editor/state",
	"editor/open",
	"editor/selection",
	"background/list",
	"background/run",
	"background/get",
	"background/logs",
	"background/watch",
	"agent-runs/list",
	"mcp/list",
	"mcp/tools",
	"mcp/call",
	"mcp/resources",
	"code/symbols",
	"lsp/actions",
	"notebook/read",
	"notebook/edit",
}

var requiredControlRoutes = []string{
	"/health",
	"/capabilities",
	"/routes",
	"/sessions",
	"/sessions/{id}/prompt",
	"/workspace/info",
	"/workspace/files",
	"/workspace/search",
	"/file/read",
	"/file/write",
	"/file/edit",
	"/file/diff",
	"/terminal",
	"/background",
	"/agents/runs",
	"/editor/identify",
	"/editor/state",
	"/editor/open",
	"/editor/selection",
	"/bridge/capabilities",
	"/mcp/list",
	"/mcp/call",
	"/lsp/actions",
}

// Options supplies runtime inputs that cannot be inferred from static bridge
// registries.
type Options struct {
	RemoteAuthToken   string
	EditorBridgeToken string
	RemoteEnabled     bool
	RemoteEnv         map[string]string
	RemoteProxyPort   uint16
}

// Report is the JSON-safe bridge and remote-session readiness summary.
type Report struct {
	Status                    string   `json:"status"`
	BridgeMethodCount         int      `json:"bridge_method_count"`
	RequiredBridgeMethodCount int      `json:"required_bridge_method_count"`
	MissingBridgeMethods      []string `json:"missing_bridge_methods,omitempty"`
	ControlRouteCount         int      `json:"control_route_count"`
	RequiredControlRouteCount int      `json:"required_control_route_count"`
	MissingControlRoutes      []string `json:"missing_control_routes,omitempty"`
	RemoteEnabled             bool     `json:"remote_enabled"`
	RemoteAuthConfigured      bool     `json:"remote_auth_configured"`
	EditorAuthConfigured      bool     `json:"editor_auth_configured"`
	RemoteSessionConfigured   bool     `json:"remote_session_configured"`
	RemoteProxyReady          bool     `json:"remote_proxy_ready"`
	RemoteProxyMissing        []string `json:"remote_proxy_missing,omitempty"`
	ExcludedExternalSurfaces  []string `json:"excluded_external_surfaces,omitempty"`
}

// Build returns a bridge readiness report without starting any listeners or
// reading local editor state.
func Build(options Options) Report {
	remoteEnv := options.RemoteEnv
	if remoteEnv == nil {
		remoteEnv = remote.Env()
	}
	runtimeReport := remote.InspectEnv(remoteEnv, options.RemoteProxyPort)
	missingBridge := missing(requiredBridgeMethods, bridge.Capabilities())
	missingRoutes := missing(requiredControlRoutes, controlRoutePaths())
	report := Report{
		Status:                    "ready",
		BridgeMethodCount:         len(bridge.Capabilities()),
		RequiredBridgeMethodCount: len(requiredBridgeMethods),
		MissingBridgeMethods:      missingBridge,
		ControlRouteCount:         len(control.RouteSpecs()),
		RequiredControlRouteCount: len(requiredControlRoutes),
		MissingControlRoutes:      missingRoutes,
		RemoteEnabled:             options.RemoteEnabled || runtimeReport.Remote.Enabled,
		RemoteAuthConfigured:      strings.TrimSpace(options.RemoteAuthToken) != "",
		EditorAuthConfigured:      strings.TrimSpace(options.EditorBridgeToken) != "",
		RemoteSessionConfigured:   strings.TrimSpace(runtimeReport.Remote.SessionID) != "",
		RemoteProxyReady:          runtimeReport.UpstreamProxy.Ready,
		RemoteProxyMissing:        remoteProxyMissing(runtimeReport),
		ExcludedExternalSurfaces:  excludedExternalSurfaces(options, runtimeReport),
	}
	if len(report.MissingBridgeMethods) > 0 ||
		len(report.MissingControlRoutes) > 0 {
		report.Status = "degraded"
	}
	if report.RemoteEnabled && !report.RemoteSessionConfigured {
		report.Status = "degraded"
	}
	if report.RemoteEnabled && !report.RemoteProxyReady && runtimeReport.UpstreamProxy.Enabled {
		report.Status = "degraded"
	}
	return report
}

// RequiredBridgeMethods returns the stable bridge-method checklist used by
// Build.
func RequiredBridgeMethods() []string {
	return append([]string(nil), requiredBridgeMethods...)
}

// RequiredControlRoutes returns the stable control-route checklist used by
// Build.
func RequiredControlRoutes() []string {
	return append([]string(nil), requiredControlRoutes...)
}

func controlRoutePaths() []string {
	specs := control.RouteSpecs()
	paths := make([]string, 0, len(specs))
	for _, spec := range specs {
		paths = append(paths, spec.Path)
	}
	return paths
}

func missing(required []string, available []string) []string {
	seen := map[string]bool{}
	for _, value := range available {
		seen[strings.ToLower(strings.TrimSpace(value))] = true
	}
	var out []string
	for _, value := range required {
		if !seen[strings.ToLower(strings.TrimSpace(value))] {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func remoteProxyMissing(runtimeReport remote.RuntimeReport) []string {
	if runtimeReport.Remote.Enabled || runtimeReport.UpstreamProxy.Enabled {
		return runtimeReport.UpstreamProxy.Missing
	}
	return nil
}

func excludedExternalSurfaces(options Options, runtimeReport remote.RuntimeReport) []string {
	var excluded []string
	if strings.TrimSpace(options.RemoteAuthToken) == "" {
		excluded = append(excluded, "official_remote_auth")
	}
	if strings.TrimSpace(options.EditorBridgeToken) == "" {
		excluded = append(excluded, "official_ide_bridge_auth")
	}
	if !runtimeReport.Remote.Enabled && !runtimeReport.UpstreamProxy.Enabled {
		excluded = append(excluded, "ccr_hosted_remote_proxy")
	}
	sort.Strings(excluded)
	return excluded
}
