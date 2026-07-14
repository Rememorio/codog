package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/audit"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/bridge"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/cron"
	"github.com/Rememorio/codog/internal/manifests"
	remoteruntime "github.com/Rememorio/codog/internal/remote"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/team"
	"github.com/Rememorio/codog/internal/toolnames"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/updater"
)

var remoteSetupActionCandidates = []string{"status", "show", "check", "enable", "on", "setup", "disable", "off", "clear", "reset", "unset"}

func (a *App) buildRemoteSetupReport(req remoteSetupRequest, sessionID, addr, remoteURL string) remoteSetupReport {
	enabled := a.Config.Future.RemoteEnabled
	authConfigured := strings.TrimSpace(a.Config.Future.RemoteAuthToken) != ""
	status := "disabled"
	switch {
	case enabled && authConfigured:
		status = "ready"
	case enabled:
		status = "enabled_without_auth"
	}
	report := remoteSetupReport{
		Kind:                "remote_setup",
		Action:              req.Action,
		Status:              status,
		Workspace:           a.Workspace,
		SessionID:           sessionID,
		Enabled:             enabled,
		Ready:               enabled,
		AuthTokenConfigured: authConfigured,
		LeaseSeconds:        a.Config.Future.RemoteLeaseSeconds,
		RemoteCommand:       "codog remote serve " + addr,
		RemoteAddr:          addr,
		RemoteURL:           remoteURL,
		HealthURL:           strings.TrimRight(remoteURL, "/") + "/health",
		StateURL:            strings.TrimRight(remoteURL, "/") + "/state",
		Path:                req.Path,
		Runtime:             remoteruntime.InspectEnv(remoteruntime.Env(), remoteProxyPortFromAddr(addr)),
	}
	switch {
	case !enabled:
		report.Messages = append(report.Messages, "Enable remote control with `codog remote-setup enable` before connecting a remote client.")
	case !authConfigured:
		report.Messages = append(report.Messages, "No auth token is configured; keep the listener on localhost or set `--auth-token` before exposing it.")
	default:
		report.Messages = append(report.Messages, "Start the remote command shown above, then connect a desktop, mobile, or remote client to the URL.")
	}
	if sessionID != "" {
		report.Messages = append(report.Messages, "Use the reported session id when the client should attach to the current conversation.")
	}
	return report
}

func renderRemoteEnvReport(out io.Writer, report remoteEnvReport) {
	fmt.Fprintln(out, "Remote Environment")
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	fmt.Fprintf(out, "  Auth token       %t\n", report.AuthTokenConfigured)
	fmt.Fprintf(out, "  Lease seconds    %d\n", report.LeaseSeconds)
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
}

func renderRemoteSetupReport(out io.Writer, report remoteSetupReport) {
	fmt.Fprintln(out, "Remote Setup")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	fmt.Fprintf(out, "  Ready            %t\n", report.Ready)
	fmt.Fprintf(out, "  Auth token       %t\n", report.AuthTokenConfigured)
	fmt.Fprintf(out, "  Lease seconds    %d\n", report.LeaseSeconds)
	fmt.Fprintf(out, "  Remote command   %s\n", report.RemoteCommand)
	fmt.Fprintf(out, "  Remote URL       %s\n", report.RemoteURL)
	fmt.Fprintf(out, "  Health URL       %s\n", report.HealthURL)
	fmt.Fprintf(out, "  Upstream proxy   %t\n", report.Runtime.UpstreamProxy.Ready)
	if report.Runtime.UpstreamProxy.WebSocketURL != "" {
		fmt.Fprintf(out, "  Upstream WS      %s\n", report.Runtime.UpstreamProxy.WebSocketURL)
	}
	if len(report.Runtime.UpstreamProxy.Missing) > 0 {
		fmt.Fprintf(out, "  Upstream missing %s\n", strings.Join(report.Runtime.UpstreamProxy.Missing, ", "))
	}
	if report.SessionID != "" {
		fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	for _, message := range report.Messages {
		fmt.Fprintf(out, "  Note             %s\n", message)
	}
}

func parseOnOffBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "on", "yes", "enabled", "enable":
		return true, nil
	case "0", "false", "off", "no", "disabled", "disable":
		return false, nil
	default:
		return false, fmt.Errorf("unknown boolean value %q", value)
	}
}

func (a *App) Bridge(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return a.IDE(args)
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	switch action {
	case "capabilities", "capability", "caps", "initialize", "init":
		format, err := parseSimpleOutputFormat("bridge capabilities", args[1:])
		if err != nil {
			return renderCLIError(a.Out, err, requestedOutputFormat(args))
		}
		return a.renderBridgeCapabilities(format)
	case "serve":
		cleanArgs, format, err := stripJSONOnlyOutputFormat("bridge", args)
		if err != nil {
			return renderCLIError(a.Out, err, requestedOutputFormat(args))
		}
		if len(cleanArgs) > 1 {
			return renderUnexpectedBridgeArguments(a.Out, "serve", cleanArgs[1:], format)
		}
	case "status", "show", "state":
		return a.IDE(append([]string{"status"}, args[1:]...))
	case "clear", "reset", "disconnect":
		return a.IDE(append([]string{"clear"}, args[1:]...))
	case "kick", "fault", "faults", "diagnostic", "diagnostics":
		return a.BridgeKick(bridgeFaultAliasArgs(args[1:]))
	default:
		return renderUnsupportedBridgeAction(a.Out, args[0], requestedOutputFormat(args))
	}
	executable, _ := os.Executable()
	return bridge.Server{
		Sessions:   a.Sessions,
		Version:    version,
		Workspace:  a.Workspace,
		ConfigHome: a.Config.ConfigHome,
		TrustToken: a.Config.Future.EditorBridgeToken,
		Executable: executable,
		MCPServers: a.Config.MCPServers,
	}.Serve(a.In, a.Out)
}

func bridgeFaultAliasArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	switch action {
	case "list", "show", "status":
		return append([]string{"status"}, args[1:]...)
	case "clear", "reset":
		return append([]string{"clear"}, args[1:]...)
	case "record":
		return append([]string(nil), args[1:]...)
	default:
		return append([]string(nil), args...)
	}
}

func renderUnexpectedBridgeArguments(out io.Writer, action string, extra []string, format string) error {
	action = strings.TrimSpace(action)
	if action == "" {
		action = "unknown"
	}
	if strings.TrimSpace(format) == "" {
		format = "text"
	}
	unexpected := append([]string(nil), extra...)
	return renderActionError(out, actionErrorReport{
		Kind:      "bridge",
		Action:    action,
		Status:    "error",
		ErrorKind: "unexpected_argument",
		Argument:  strings.Join(unexpected, " "),
		Message:   fmt.Sprintf("bridge %s received unexpected argument(s): %s", action, strings.Join(unexpected, " ")),
		Hint:      "Usage: codog bridge serve.",
	}, format)
}

func renderUnsupportedBridgeAction(out io.Writer, action string, format string) error {
	action = strings.TrimSpace(action)
	if action == "" {
		action = "unknown"
	}
	if strings.TrimSpace(format) == "" {
		format = "text"
	}
	return renderActionError(out, actionErrorReport{
		Kind:      "bridge",
		Action:    action,
		Status:    "error",
		ErrorKind: "unsupported_bridge_action",
		Message:   fmt.Sprintf("unsupported bridge action %q", action),
		Hint:      unknownBridgeActionHint(action),
	}, format)
}

var bridgeActionCandidates = []string{
	"serve", "capabilities", "capability", "caps", "initialize", "init",
	"status", "show", "state", "clear", "reset", "disconnect",
	"kick", "fault", "faults", "diagnostic", "diagnostics",
}

func unknownBridgeActionHint(action string) string {
	suggestions := toolnames.Suggestions(action, bridgeActionCandidates, 4)
	switch len(suggestions) {
	case 1:
		return fmt.Sprintf("Did you mean `codog bridge %s`? Use `codog bridge capabilities --json`, `codog bridge faults list --json`, or `codog bridge serve`.", suggestions[0])
	case 0:
		return "Supported bridge actions are serve, capabilities, status, clear, and faults. Use `codog bridge capabilities --json`, `codog bridge faults list --json`, or `codog bridge serve`."
	default:
		return fmt.Sprintf("Did you mean one of: %s? Use `codog bridge capabilities --json`, `codog bridge faults list --json`, or `codog bridge serve`.", strings.Join(suggestions, ", "))
	}
}

type bridgeCapabilitiesReport struct {
	Kind         string   `json:"kind"`
	Action       string   `json:"action"`
	Status       string   `json:"status"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Workspace    string   `json:"workspace,omitempty"`
	Count        int      `json:"count"`
	Capabilities []string `json:"capabilities"`
}

func (a *App) renderBridgeCapabilities(format string) error {
	capabilities := bridge.Capabilities()
	report := bridgeCapabilitiesReport{
		Kind:         "bridge_capabilities",
		Action:       "capabilities",
		Status:       "ok",
		Name:         "codog",
		Version:      version,
		Workspace:    a.Workspace,
		Count:        len(capabilities),
		Capabilities: capabilities,
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, "Bridge Capabilities")
	fmt.Fprintf(a.Out, "  Name             %s\n", report.Name)
	fmt.Fprintf(a.Out, "  Version          %s\n", report.Version)
	if report.Workspace != "" {
		fmt.Fprintf(a.Out, "  Workspace        %s\n", report.Workspace)
	}
	fmt.Fprintf(a.Out, "  Count            %d\n", report.Count)
	for _, capability := range report.Capabilities {
		fmt.Fprintf(a.Out, "  Capability       %s\n", capability)
	}
	return nil
}

func bridgeStatusArgs(args []string) []string {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return append([]string(nil), args...)
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	switch action {
	case "status", "show", "state":
		return append([]string{"status"}, args[1:]...)
	case "clear", "reset", "disconnect":
		return append([]string{"clear"}, args[1:]...)
	default:
		return append([]string(nil), args...)
	}
}

type ideRequest struct {
	Action string
	Format string
}

type ideReport struct {
	Kind      string             `json:"kind"`
	Action    string             `json:"action"`
	Workspace string             `json:"workspace"`
	Bridge    ideBridgeReport    `json:"bridge"`
	StatePath string             `json:"state_path,omitempty"`
	State     bridge.EditorState `json:"state"`
	Cleared   bool               `json:"cleared,omitempty"`
}

type ideBridgeReport struct {
	Command         string `json:"command"`
	Socket          string `json:"socket,omitempty"`
	TokenConfigured bool   `json:"token_configured"`
}

func (a *App) IDE(args []string) error {
	req, err := parseIDEArgs(args)
	if err != nil {
		return err
	}
	server := bridge.Server{
		Sessions:   a.Sessions,
		Version:    version,
		Workspace:  a.Workspace,
		ConfigHome: a.Config.ConfigHome,
		TrustToken: a.Config.Future.EditorBridgeToken,
	}
	if req.Action == "clear" {
		if err := server.ClearEditorState(); err != nil {
			return err
		}
	}
	state, err := server.EditorState()
	if err != nil {
		return err
	}
	statePath, err := server.EditorStatePath()
	if err != nil {
		return err
	}
	report := ideReport{
		Kind:      "ide",
		Action:    req.Action,
		Workspace: a.Workspace,
		Bridge: ideBridgeReport{
			Command:         "codog bridge serve",
			Socket:          a.Config.Future.EditorBridgeSocket,
			TokenConfigured: a.Config.Future.EditorBridgeToken != "",
		},
		StatePath: statePath,
		State:     state,
		Cleared:   req.Action == "clear",
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderIDEReport(a.Out, report)
	return nil
}

func (a *App) connectActiveIDE() error {
	server := bridge.Server{
		Sessions:   a.Sessions,
		Version:    version,
		Workspace:  a.Workspace,
		ConfigHome: a.Config.ConfigHome,
		TrustToken: a.Config.Future.EditorBridgeToken,
	}
	state, err := server.EditorState()
	if err != nil {
		return ideConnectionError{Kind: "ide_bridge_state_unavailable", ExpectedWorkspace: a.Workspace, Err: err}
	}
	if state.Identity == nil || !state.Identity.Trusted || strings.TrimSpace(state.Identity.Editor) == "" {
		return ideConnectionError{Kind: "ide_bridge_unavailable", ExpectedWorkspace: a.Workspace}
	}
	if !sameFilesystemPath(state.Identity.Workspace, a.Workspace) {
		return ideConnectionError{
			Kind:              "ide_workspace_mismatch",
			ExpectedWorkspace: a.Workspace,
			ActualWorkspace:   state.Identity.Workspace,
		}
	}
	a.ActiveIDE = &state
	return nil
}

func sameFilesystemPath(left string, right string) bool {
	leftAbs, leftErr := filepath.Abs(strings.TrimSpace(left))
	rightAbs, rightErr := filepath.Abs(strings.TrimSpace(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftResolved, leftResolveErr := filepath.EvalSymlinks(leftAbs)
	if leftResolveErr == nil {
		leftAbs = leftResolved
	}
	rightResolved, rightResolveErr := filepath.EvalSymlinks(rightAbs)
	if rightResolveErr == nil {
		rightAbs = rightResolved
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

type bridgeKickRequest struct {
	Action string
	Format string
	Args   []string
}

type bridgeKickReport struct {
	Kind     string              `json:"kind"`
	Action   string              `json:"action"`
	Status   string              `json:"status"`
	Message  string              `json:"message,omitempty"`
	Bridge   ideBridgeReport     `json:"bridge"`
	State    bridge.EditorState  `json:"state"`
	Faults   []bridge.FaultEvent `json:"faults,omitempty"`
	Recorded *bridge.FaultEvent  `json:"recorded,omitempty"`
	Cleared  bool                `json:"cleared,omitempty"`
}

func (a *App) BridgeKick(args []string) error {
	req, err := parseBridgeKickArgs(args)
	if err != nil {
		return err
	}
	server := bridge.Server{
		ConfigHome: a.Config.ConfigHome,
		Workspace:  a.Workspace,
		TrustToken: a.Config.Future.EditorBridgeToken,
	}
	report := bridgeKickReport{
		Kind:   "bridge_kick",
		Action: req.Action,
		Status: "ok",
		Bridge: ideBridgeReport{
			Command:         "codog bridge serve",
			Socket:          a.Config.Future.EditorBridgeSocket,
			TokenConfigured: a.Config.Future.EditorBridgeToken != "",
		},
	}
	switch req.Action {
	case "status":
		report.State, _ = server.EditorState()
		report.Faults, _ = server.BridgeFaults()
		report.Message = "Local bridge diagnostics are available."
	case "clear":
		if err := server.ClearEditorState(); err != nil {
			return err
		}
		if err := server.ClearBridgeFaults(); err != nil {
			return err
		}
		report.State, _ = server.EditorState()
		report.Cleared = true
		report.Message = "Cleared local trusted editor bridge state and bridge fault events."
	default:
		event, err := server.RecordBridgeFault(req.Action, req.Args)
		if err != nil {
			return err
		}
		report.Recorded = &event
		report.Faults, _ = server.BridgeFaults()
		report.State, _ = server.EditorState()
		report.Message = fmt.Sprintf("Recorded local bridge fault event for bridge-kick %s.", strings.Join(append([]string{req.Action}, req.Args...), " "))
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderBridgeKickReport(a.Out, report)
	return nil
}

func parseIDEArgs(args []string) (ideRequest, error) {
	const usage = "codog ide [status|clear] [--json|--output-format text|json]"
	req := ideRequest{Action: "status", Format: "text"}
	actionSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "ide", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "ide", Option: arg, Usage: usage}
		default:
			if actionSet {
				return req, unexpectedExtraArgsError{Command: "ide", Args: []string{arg}, Usage: usage}
			}
			action := strings.ToLower(arg)
			switch action {
			case "status", "state":
				req.Action = "status"
			case "clear", "reset", "disconnect":
				req.Action = "clear"
			default:
				return req, unexpectedExtraArgsError{Command: "ide", Args: []string{arg}, Usage: usage}
			}
			actionSet = true
		}
	}
	normalizedFormat, err := normalizeOutputFormat("ide", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func parseBridgeKickArgs(args []string) (bridgeKickRequest, error) {
	const usage = "codog bridge-kick [status|clear|FAULT [ARG...]] [--json|--output-format text|json]"
	req := bridgeKickRequest{Action: "status", Format: "text"}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "bridge-kick", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "bridge-kick", Option: arg, Usage: usage}
		default:
			positionals = append(positionals, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("bridge-kick", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(positionals) != 0 {
		req.Action = strings.ToLower(positionals[0])
		req.Args = positionals[1:]
	}
	return req, nil
}

func renderIDEReport(out io.Writer, report ideReport) {
	fmt.Fprintln(out, "IDE Bridge")
	fmt.Fprintf(out, "  Workspace        %s\n", emptyAsNone(report.Workspace))
	fmt.Fprintf(out, "  Bridge command   %s\n", report.Bridge.Command)
	fmt.Fprintf(out, "  Socket           %s\n", emptyAsNone(report.Bridge.Socket))
	fmt.Fprintf(out, "  Token configured %t\n", report.Bridge.TokenConfigured)
	if report.Cleared {
		fmt.Fprintln(out, "  State cleared    true")
	}
	if report.State.Identity == nil {
		fmt.Fprintln(out, "  Trusted editor   none")
	} else {
		identity := report.State.Identity.Editor
		if report.State.Identity.Version != "" {
			identity += " " + report.State.Identity.Version
		}
		fmt.Fprintf(out, "  Trusted editor   %s\n", identity)
		fmt.Fprintf(out, "  Trusted          %t\n", report.State.Identity.Trusted)
	}
	if report.State.OpenFile == nil {
		fmt.Fprintln(out, "  Open file        none")
	} else {
		fmt.Fprintf(out, "  Open file        %s\n", report.State.OpenFile.Path)
	}
	if report.State.Selection == nil {
		fmt.Fprintln(out, "  Selection        none")
	} else {
		selection := report.State.Selection
		fmt.Fprintf(out, "  Selection        %s:%d", selection.Path, selection.StartLine)
		if selection.EndLine > 0 && selection.EndLine != selection.StartLine {
			fmt.Fprintf(out, "-%d", selection.EndLine)
		}
		fmt.Fprintln(out)
	}
}

func renderBridgeKickReport(out io.Writer, report bridgeKickReport) {
	fmt.Fprintln(out, "Bridge Kick")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	fmt.Fprintf(out, "  Bridge command   %s\n", report.Bridge.Command)
	fmt.Fprintf(out, "  Socket           %s\n", emptyAsNone(report.Bridge.Socket))
	fmt.Fprintf(out, "  Token configured %t\n", report.Bridge.TokenConfigured)
	fmt.Fprintf(out, "  Fault events     %d\n", len(report.Faults))
	if report.Cleared {
		fmt.Fprintln(out, "  Cleared          true")
	}
	if report.Recorded != nil {
		fmt.Fprintf(out, "  Recorded         %s %s\n", report.Recorded.Action, strings.Join(report.Recorded.Args, " "))
		fmt.Fprintf(out, "  Fault severity   %s\n", report.Recorded.Severity)
		fmt.Fprintf(out, "  Fault category   %s\n", report.Recorded.Category)
		fmt.Fprintf(out, "  Fault message    %s\n", report.Recorded.Message)
		fmt.Fprintf(out, "  Remediation      %s\n", report.Recorded.Remediation)
	} else if len(report.Faults) > 0 {
		last := report.Faults[len(report.Faults)-1]
		fmt.Fprintf(out, "  Last fault       %s %s\n", last.Action, strings.Join(last.Args, " "))
		fmt.Fprintf(out, "  Fault severity   %s\n", last.Severity)
		fmt.Fprintf(out, "  Fault category   %s\n", last.Category)
		fmt.Fprintf(out, "  Fault message    %s\n", last.Message)
		fmt.Fprintf(out, "  Remediation      %s\n", last.Remediation)
	}
	if report.State.Identity == nil {
		fmt.Fprintln(out, "  Trusted editor   none")
	} else {
		fmt.Fprintf(out, "  Trusted editor   %s\n", report.State.Identity.Editor)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

type desktopHandoffRequest struct {
	Action    string
	Format    string
	SessionID string
}

type desktopHandoffReport struct {
	Kind         string             `json:"kind"`
	Action       string             `json:"action"`
	Surface      string             `json:"surface"`
	Workspace    string             `json:"workspace"`
	SessionID    string             `json:"session_id,omitempty"`
	Supported    bool               `json:"supported"`
	Platform     string             `json:"platform"`
	Bridge       ideBridgeReport    `json:"bridge"`
	StatePath    string             `json:"state_path,omitempty"`
	State        bridge.EditorState `json:"state"`
	HandoffID    string             `json:"handoff_id,omitempty"`
	ManifestPath string             `json:"manifest_path,omitempty"`
	CreatedAt    time.Time          `json:"created_at,omitempty"`
	DeepLink     string             `json:"deep_link,omitempty"`
	Messages     []string           `json:"messages,omitempty"`
}

type mobileHandoffRequest struct {
	Action    string
	Platform  string
	Format    string
	Addr      string
	SessionID string
}

type mobileHandoffReport struct {
	Kind                string     `json:"kind"`
	Action              string     `json:"action"`
	Surface             string     `json:"surface"`
	Workspace           string     `json:"workspace"`
	SessionID           string     `json:"session_id,omitempty"`
	Platform            string     `json:"platform"`
	RemoteCommand       string     `json:"remote_command"`
	RemoteAddr          string     `json:"remote_addr"`
	RemoteURL           string     `json:"remote_url"`
	RemoteEnabled       bool       `json:"remote_enabled"`
	AuthTokenConfigured bool       `json:"auth_token_configured"`
	LeaseSeconds        int        `json:"lease_seconds"`
	HandoffID           string     `json:"handoff_id,omitempty"`
	ManifestPath        string     `json:"manifest_path,omitempty"`
	CreatedAt           time.Time  `json:"created_at,omitempty"`
	ExpiresAt           *time.Time `json:"expires_at,omitempty"`
	DeepLink            string     `json:"deep_link,omitempty"`
	Messages            []string   `json:"messages,omitempty"`
}

type handoffManifest struct {
	Kind                string           `json:"kind"`
	Version             int              `json:"version"`
	ID                  string           `json:"id"`
	Surface             string           `json:"surface"`
	Platform            string           `json:"platform,omitempty"`
	Workspace           string           `json:"workspace,omitempty"`
	SessionID           string           `json:"session_id,omitempty"`
	Command             string           `json:"command,omitempty"`
	RemoteAddr          string           `json:"remote_addr,omitempty"`
	RemoteURL           string           `json:"remote_url,omitempty"`
	RemoteEnabled       bool             `json:"remote_enabled,omitempty"`
	AuthTokenConfigured bool             `json:"auth_token_configured,omitempty"`
	LeaseSeconds        int              `json:"lease_seconds,omitempty"`
	Bridge              *ideBridgeReport `json:"bridge,omitempty"`
	StatePath           string           `json:"state_path,omitempty"`
	Supported           bool             `json:"supported,omitempty"`
	CreatedAt           time.Time        `json:"created_at"`
	ExpiresAt           *time.Time       `json:"expires_at,omitempty"`
	DeepLink            string           `json:"deep_link,omitempty"`
	Path                string           `json:"path,omitempty"`
	Messages            []string         `json:"messages,omitempty"`
}

type handoffStatusReport struct {
	Kind      string            `json:"kind"`
	Action    string            `json:"action"`
	Status    string            `json:"status"`
	Surface   string            `json:"surface"`
	Platform  string            `json:"platform,omitempty"`
	Workspace string            `json:"workspace,omitempty"`
	Count     int               `json:"count"`
	Removed   int               `json:"removed,omitempty"`
	Manifests []handoffManifest `json:"manifests,omitempty"`
	Message   string            `json:"message,omitempty"`
}

const desktopHandoffUsage = "codog desktop|app [handoff|status|clear] [--session ID|--resume ID] [--output-format text|json]"
const mobileHandoffUsage = "codog mobile|ios|android [handoff|status|clear] [--addr HOST:PORT] [--session ID|--resume ID] [--output-format text|json]"

func (a *App) Desktop(args []string, overrides config.FlagOverrides) error {
	req, err := parseDesktopHandoffArgs(args, overrides)
	if err != nil {
		return err
	}
	if req.Action == "status" {
		report, err := buildHandoffStatusReport(a.Config.ConfigHome, "desktop", "", a.Workspace)
		if err != nil {
			return err
		}
		return renderHandoffStatusReport(a.Out, report, req.Format)
	}
	if req.Action == "clear" {
		removed, err := clearHandoffManifests(a.Config.ConfigHome, "desktop", "")
		if err != nil {
			return err
		}
		report := handoffStatusReport{
			Kind:      "handoff_status",
			Action:    "clear",
			Status:    "ok",
			Surface:   "desktop",
			Workspace: a.Workspace,
			Removed:   removed,
			Message:   fmt.Sprintf("Removed %d desktop handoff manifest(s).", removed),
		}
		return renderHandoffStatusReport(a.Out, report, req.Format)
	}
	sessionID, err := resolveHandoffSessionID(a.Sessions, req.SessionID)
	if err != nil {
		return err
	}
	server := bridge.Server{
		Sessions:   a.Sessions,
		Version:    version,
		Workspace:  a.Workspace,
		ConfigHome: a.Config.ConfigHome,
		TrustToken: a.Config.Future.EditorBridgeToken,
	}
	state, err := server.EditorState()
	if err != nil {
		return err
	}
	statePath, err := server.EditorStatePath()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	report := desktopHandoffReport{
		Kind:      "desktop_handoff",
		Action:    req.Action,
		Surface:   "desktop",
		Workspace: a.Workspace,
		SessionID: sessionID,
		Supported: desktopHandoffSupported(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
		Bridge: ideBridgeReport{
			Command:         "codog bridge serve",
			Socket:          a.Config.Future.EditorBridgeSocket,
			TokenConfigured: a.Config.Future.EditorBridgeToken != "",
		},
		StatePath: statePath,
		State:     state,
		CreatedAt: now,
		Messages: []string{
			"Start the bridge command, then connect a trusted desktop or editor client to the stdio bridge.",
			"Use `codog ide status` to inspect the currently trusted client.",
		},
	}
	report.DeepLink = buildHandoffDeepLink(report.Surface, sessionID, "", report.Bridge.Socket, "")
	manifest := handoffManifest{
		Kind:      "handoff_manifest",
		Version:   1,
		Surface:   report.Surface,
		Platform:  report.Platform,
		Workspace: report.Workspace,
		SessionID: report.SessionID,
		Command:   report.Bridge.Command,
		Bridge: &ideBridgeReport{
			Command:         report.Bridge.Command,
			Socket:          report.Bridge.Socket,
			TokenConfigured: report.Bridge.TokenConfigured,
		},
		StatePath: report.StatePath,
		Supported: report.Supported,
		CreatedAt: now,
		DeepLink:  report.DeepLink,
		Messages:  append([]string(nil), report.Messages...),
	}
	manifest, err = saveHandoffManifest(a.Config.ConfigHome, manifest)
	if err != nil {
		return err
	}
	report.HandoffID = manifest.ID
	report.ManifestPath = manifest.Path
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderDesktopHandoffReport(a.Out, report)
	return nil
}

func parseDesktopHandoffArgs(args []string, overrides config.FlagOverrides) (desktopHandoffRequest, error) {
	req := desktopHandoffRequest{Action: "handoff", Format: "text"}
	req.SessionID = firstNonEmpty(overrides.Resume, overrides.SessionID)
	actionSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "desktop", Flag: arg, Usage: desktopHandoffUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "desktop", Flag: arg, Usage: desktopHandoffUsage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "desktop", Flag: arg, Usage: desktopHandoffUsage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "desktop", Option: arg, Usage: desktopHandoffUsage}
		default:
			if actionSet {
				return req, unexpectedExtraArgsError{Command: "desktop", Args: []string{arg}, Usage: desktopHandoffUsage}
			}
			switch strings.ToLower(arg) {
			case "handoff", "show":
				req.Action = "handoff"
			case "status", "list":
				req.Action = "status"
			case "clear", "remove":
				req.Action = "clear"
			default:
				return req, unexpectedExtraArgsError{Command: "desktop", Args: []string{arg}, Usage: desktopHandoffUsage}
			}
			actionSet = true
		}
	}
	normalizedFormat, err := normalizeOutputFormat("desktop", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func renderDesktopHandoffReport(out io.Writer, report desktopHandoffReport) {
	fmt.Fprintln(out, "Desktop Handoff")
	fmt.Fprintf(out, "  Workspace        %s\n", emptyAsNone(report.Workspace))
	fmt.Fprintf(out, "  Platform         %s\n", report.Platform)
	fmt.Fprintf(out, "  Supported        %t\n", report.Supported)
	if report.SessionID != "" {
		fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	}
	if report.HandoffID != "" {
		fmt.Fprintf(out, "  Handoff ID       %s\n", report.HandoffID)
	}
	if report.ManifestPath != "" {
		fmt.Fprintf(out, "  Manifest         %s\n", report.ManifestPath)
	}
	fmt.Fprintf(out, "  Bridge command   %s\n", report.Bridge.Command)
	fmt.Fprintf(out, "  Socket           %s\n", emptyAsNone(report.Bridge.Socket))
	fmt.Fprintf(out, "  Token configured %t\n", report.Bridge.TokenConfigured)
	if report.DeepLink != "" {
		fmt.Fprintf(out, "  Deep link        %s\n", report.DeepLink)
	}
	if report.State.Identity == nil {
		fmt.Fprintln(out, "  Trusted client   none")
	} else {
		identity := report.State.Identity.Editor
		if report.State.Identity.Version != "" {
			identity += " " + report.State.Identity.Version
		}
		fmt.Fprintf(out, "  Trusted client   %s\n", identity)
	}
	for _, message := range report.Messages {
		fmt.Fprintf(out, "  Note             %s\n", message)
	}
}

func (a *App) Mobile(args []string, overrides config.FlagOverrides) error {
	req, err := parseMobileHandoffArgs(args, overrides)
	if err != nil {
		return err
	}
	if req.Action == "status" {
		report, err := buildHandoffStatusReport(a.Config.ConfigHome, "mobile", req.Platform, a.Workspace)
		if err != nil {
			return err
		}
		return renderHandoffStatusReport(a.Out, report, req.Format)
	}
	if req.Action == "clear" {
		removed, err := clearHandoffManifests(a.Config.ConfigHome, "mobile", req.Platform)
		if err != nil {
			return err
		}
		report := handoffStatusReport{
			Kind:      "handoff_status",
			Action:    "clear",
			Status:    "ok",
			Surface:   "mobile",
			Platform:  req.Platform,
			Workspace: a.Workspace,
			Removed:   removed,
			Message:   fmt.Sprintf("Removed %d mobile handoff manifest(s).", removed),
		}
		return renderHandoffStatusReport(a.Out, report, req.Format)
	}
	sessionID, err := resolveHandoffSessionID(a.Sessions, req.SessionID)
	if err != nil {
		return err
	}
	addr, remoteURL, err := normalizeRemoteHandoffAddr(req.Addr)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var expiresAt *time.Time
	if a.Config.Future.RemoteLeaseSeconds > 0 {
		next := now.Add(time.Duration(a.Config.Future.RemoteLeaseSeconds) * time.Second)
		expiresAt = &next
	}
	report := mobileHandoffReport{
		Kind:                "mobile_handoff",
		Action:              "handoff",
		Surface:             "mobile",
		Workspace:           a.Workspace,
		SessionID:           sessionID,
		Platform:            req.Platform,
		RemoteCommand:       "codog remote serve " + addr,
		RemoteAddr:          addr,
		RemoteURL:           remoteURL,
		RemoteEnabled:       a.Config.Future.RemoteEnabled,
		AuthTokenConfigured: strings.TrimSpace(a.Config.Future.RemoteAuthToken) != "",
		LeaseSeconds:        a.Config.Future.RemoteLeaseSeconds,
		CreatedAt:           now,
		ExpiresAt:           expiresAt,
		Messages: []string{
			"Start the remote command, then connect a mobile or remote client to the local control API.",
			"Use `codog remote-env set --auth-token TOKEN` when the endpoint is reachable outside localhost.",
		},
	}
	report.DeepLink = buildHandoffDeepLink(report.Surface, sessionID, remoteURL, "", report.Platform)
	manifest := handoffManifest{
		Kind:                "handoff_manifest",
		Version:             1,
		Surface:             report.Surface,
		Platform:            report.Platform,
		Workspace:           report.Workspace,
		SessionID:           report.SessionID,
		Command:             report.RemoteCommand,
		RemoteAddr:          report.RemoteAddr,
		RemoteURL:           report.RemoteURL,
		RemoteEnabled:       report.RemoteEnabled,
		AuthTokenConfigured: report.AuthTokenConfigured,
		LeaseSeconds:        report.LeaseSeconds,
		CreatedAt:           now,
		ExpiresAt:           expiresAt,
		DeepLink:            report.DeepLink,
		Messages:            append([]string(nil), report.Messages...),
	}
	manifest, err = saveHandoffManifest(a.Config.ConfigHome, manifest)
	if err != nil {
		return err
	}
	report.HandoffID = manifest.ID
	report.ManifestPath = manifest.Path
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderMobileHandoffReport(a.Out, report)
	return nil
}

func parseMobileHandoffArgs(args []string, overrides config.FlagOverrides) (mobileHandoffRequest, error) {
	req := mobileHandoffRequest{Action: "handoff", Platform: "all", Format: "text", Addr: "127.0.0.1:8791"}
	req.SessionID = firstNonEmpty(overrides.Resume, overrides.SessionID)
	platformSet := false
	actionSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "mobile", Flag: arg, Usage: mobileHandoffUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--addr":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "mobile", Flag: arg, Usage: mobileHandoffUsage}
			}
			req.Addr = args[index]
		case strings.HasPrefix(arg, "--addr="):
			req.Addr = strings.TrimPrefix(arg, "--addr=")
		case arg == "--session":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "mobile", Flag: arg, Usage: mobileHandoffUsage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "mobile", Flag: arg, Usage: mobileHandoffUsage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "mobile", Option: arg, Usage: mobileHandoffUsage}
		default:
			normalized := strings.ToLower(arg)
			switch normalized {
			case "handoff", "show":
				if actionSet {
					return req, unexpectedExtraArgsError{Command: "mobile", Args: []string{arg}, Usage: mobileHandoffUsage}
				}
				req.Action = "handoff"
				actionSet = true
			case "status", "list":
				if actionSet {
					return req, unexpectedExtraArgsError{Command: "mobile", Args: []string{arg}, Usage: mobileHandoffUsage}
				}
				req.Action = "status"
				actionSet = true
			case "clear", "remove":
				if actionSet {
					return req, unexpectedExtraArgsError{Command: "mobile", Args: []string{arg}, Usage: mobileHandoffUsage}
				}
				req.Action = "clear"
				actionSet = true
			case "all", "ios", "android":
				if platformSet {
					return req, unexpectedExtraArgsError{Command: "mobile", Args: []string{arg}, Usage: mobileHandoffUsage}
				}
				req.Platform = normalized
				platformSet = true
			default:
				return req, unexpectedExtraArgsError{Command: "mobile", Args: []string{arg}, Usage: mobileHandoffUsage}
			}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("mobile", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func renderMobileHandoffReport(out io.Writer, report mobileHandoffReport) {
	fmt.Fprintln(out, "Mobile Handoff")
	fmt.Fprintf(out, "  Workspace        %s\n", emptyAsNone(report.Workspace))
	fmt.Fprintf(out, "  Platform         %s\n", report.Platform)
	if report.SessionID != "" {
		fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	}
	if report.HandoffID != "" {
		fmt.Fprintf(out, "  Handoff ID       %s\n", report.HandoffID)
	}
	if report.ManifestPath != "" {
		fmt.Fprintf(out, "  Manifest         %s\n", report.ManifestPath)
	}
	fmt.Fprintf(out, "  Remote command   %s\n", report.RemoteCommand)
	fmt.Fprintf(out, "  Remote URL       %s\n", report.RemoteURL)
	fmt.Fprintf(out, "  Remote enabled   %t\n", report.RemoteEnabled)
	fmt.Fprintf(out, "  Token configured %t\n", report.AuthTokenConfigured)
	if report.LeaseSeconds > 0 {
		fmt.Fprintf(out, "  Lease seconds    %d\n", report.LeaseSeconds)
	}
	if report.ExpiresAt != nil {
		fmt.Fprintf(out, "  Expires at       %s\n", report.ExpiresAt.Format(time.RFC3339))
	}
	if report.DeepLink != "" {
		fmt.Fprintf(out, "  Deep link        %s\n", report.DeepLink)
	}
	for _, message := range report.Messages {
		fmt.Fprintf(out, "  Note             %s\n", message)
	}
}

func desktopHandoffSupported() bool {
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	default:
		return false
	}
}

func resolveHandoffSessionID(store *session.Store, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", nil
	}
	if !session.IsSessionReferenceAlias(id) {
		return id, nil
	}
	if store == nil {
		return "", errors.New("session store is unavailable")
	}
	return store.LatestID()
}

func normalizeRemoteHandoffAddr(value string) (string, string, error) {
	addr := strings.TrimSpace(value)
	if addr == "" {
		addr = "127.0.0.1:8791"
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		parsed, err := url.Parse(addr)
		if err != nil || parsed.Host == "" {
			return "", "", fmt.Errorf("invalid mobile remote URL %q", value)
		}
		parsed.Path = strings.TrimRight(parsed.Path, "/")
		return parsed.Host, parsed.String(), nil
	}
	if strings.Contains(addr, "://") {
		return "", "", fmt.Errorf("mobile remote address must use http or https: %q", value)
	}
	displayHost := addr
	switch {
	case strings.HasPrefix(addr, ":"):
		displayHost = "127.0.0.1" + addr
	case strings.HasPrefix(addr, "0.0.0.0:"):
		displayHost = "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	case strings.HasPrefix(addr, "[::]:"):
		displayHost = "127.0.0.1:" + strings.TrimPrefix(addr, "[::]:")
	}
	return addr, "http://" + displayHost, nil
}

func remoteProxyPortFromAddr(addr string) uint16 {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return 8791
	}
	if parsed, err := url.Parse(addr); err == nil && parsed.Host != "" {
		addr = parsed.Host
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	index := strings.LastIndex(addr, ":")
	if index < 0 || index == len(addr)-1 {
		return 0
	}
	port, err := strconv.Atoi(addr[index+1:])
	if err != nil || port < 0 || port > 65535 {
		return 0
	}
	return uint16(port)
}

func saveHandoffManifest(configHome string, manifest handoffManifest) (handoffManifest, error) {
	configHome = strings.TrimSpace(configHome)
	if configHome == "" {
		return handoffManifest{}, errors.New("config home is required for handoff manifests")
	}
	if manifest.CreatedAt.IsZero() {
		manifest.CreatedAt = time.Now().UTC()
	}
	manifest.ID = firstNonEmpty(manifest.ID, newHandoffID(manifest.Surface, manifest.SessionID, manifest.CreatedAt))
	dir := handoffManifestRoot(configHome)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return handoffManifest{}, err
	}
	manifest.Path = filepath.Join(dir, manifest.ID+".json")
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return handoffManifest{}, err
	}
	if err := os.WriteFile(manifest.Path, append(data, '\n'), 0o644); err != nil {
		return handoffManifest{}, err
	}
	return manifest, nil
}

func buildHandoffStatusReport(configHome, surface, platform, workspace string) (handoffStatusReport, error) {
	manifests, err := loadHandoffManifests(configHome, surface, platform)
	if err != nil {
		return handoffStatusReport{}, err
	}
	return handoffStatusReport{
		Kind:      "handoff_status",
		Action:    "status",
		Status:    "ok",
		Surface:   surface,
		Platform:  normalizedHandoffPlatform(platform),
		Workspace: workspace,
		Count:     len(manifests),
		Manifests: manifests,
	}, nil
}

func loadHandoffManifests(configHome, surface, platform string) ([]handoffManifest, error) {
	configHome = strings.TrimSpace(configHome)
	if configHome == "" {
		return nil, errors.New("config home is required for handoff manifests")
	}
	dir := handoffManifestRoot(configHome)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []handoffManifest{}, nil
		}
		return nil, err
	}
	manifests := []handoffManifest{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var manifest handoffManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil, err
		}
		manifest.Path = path
		if handoffManifestMatches(manifest, surface, platform) {
			manifests = append(manifests, manifest)
		}
	}
	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].CreatedAt.After(manifests[j].CreatedAt)
	})
	return manifests, nil
}

func clearHandoffManifests(configHome, surface, platform string) (int, error) {
	manifests, err := loadHandoffManifests(configHome, surface, platform)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, manifest := range manifests {
		if manifest.Path == "" {
			continue
		}
		if err := os.Remove(manifest.Path); err != nil && !os.IsNotExist(err) {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func renderHandoffStatusReport(out io.Writer, report handoffStatusReport, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Handoff Manifests")
	fmt.Fprintf(out, "  Surface          %s\n", report.Surface)
	if report.Platform != "" && report.Platform != "all" {
		fmt.Fprintf(out, "  Platform         %s\n", report.Platform)
	}
	if report.Workspace != "" {
		fmt.Fprintf(out, "  Workspace        %s\n", report.Workspace)
	}
	if report.Action == "clear" {
		fmt.Fprintf(out, "  Removed          %d\n", report.Removed)
		if report.Message != "" {
			fmt.Fprintf(out, "  Message          %s\n", report.Message)
		}
		return nil
	}
	fmt.Fprintf(out, "  Count            %d\n", report.Count)
	for _, manifest := range report.Manifests {
		fmt.Fprintf(out, "  - %s\n", manifest.ID)
		if manifest.SessionID != "" {
			fmt.Fprintf(out, "    Session        %s\n", manifest.SessionID)
		}
		if manifest.Platform != "" {
			fmt.Fprintf(out, "    Platform       %s\n", manifest.Platform)
		}
		if manifest.Command != "" {
			fmt.Fprintf(out, "    Command        %s\n", manifest.Command)
		}
		if manifest.RemoteURL != "" {
			fmt.Fprintf(out, "    Remote URL     %s\n", manifest.RemoteURL)
		}
		if manifest.DeepLink != "" {
			fmt.Fprintf(out, "    Deep link      %s\n", manifest.DeepLink)
		}
		if manifest.ExpiresAt != nil {
			fmt.Fprintf(out, "    Expires at     %s\n", manifest.ExpiresAt.Format(time.RFC3339))
		}
		if manifest.Path != "" {
			fmt.Fprintf(out, "    Manifest       %s\n", manifest.Path)
		}
	}
	return nil
}

func handoffManifestRoot(configHome string) string {
	return filepath.Join(configHome, "handoffs")
}

func handoffManifestMatches(manifest handoffManifest, surface, platform string) bool {
	if strings.TrimSpace(surface) != "" && manifest.Surface != surface {
		return false
	}
	platform = normalizedHandoffPlatform(platform)
	if platform == "" || platform == "all" {
		return true
	}
	return manifest.Platform == platform
}

func normalizedHandoffPlatform(platform string) string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		return ""
	}
	return platform
}

func newHandoffID(surface, sessionID string, now time.Time) string {
	entropy := make([]byte, 4)
	if _, err := rand.Read(entropy); err != nil {
		copy(entropy, []byte(now.Format("15040500")))
	}
	parts := []string{"handoff", handoffIDComponent(surface)}
	if session := handoffIDComponent(sessionID); session != "" {
		parts = append(parts, session)
	}
	parts = append(parts, now.Format("20060102T150405.000000000Z"), hex.EncodeToString(entropy))
	return strings.Join(parts, "-")
}

func handoffIDComponent(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, ch := range value {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
			builder.WriteRune(ch)
			lastDash = false
		case ch == '-' || ch == '_' || ch == '.':
			builder.WriteRune(ch)
			lastDash = false
		default:
			if builder.Len() > 0 && !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-_.")
}

func buildHandoffDeepLink(surface, sessionID, remoteURL, socket, platform string) string {
	values := url.Values{}
	if sessionID != "" {
		values.Set("session", sessionID)
	}
	if remoteURL != "" {
		values.Set("url", remoteURL)
	}
	if socket != "" {
		values.Set("socket", socket)
	}
	if platform != "" && platform != "all" {
		values.Set("platform", platform)
	}
	link := url.URL{Scheme: "codog", Host: "handoff", Path: "/" + surface, RawQuery: values.Encode()}
	return link.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

type briefRequest struct {
	Message     string
	Status      string
	Attachments []string
	Format      string
}

type briefReport struct {
	Message     string                  `json:"message"`
	Status      string                  `json:"status"`
	Attachments []briefAttachmentReport `json:"attachments"`
	SentAt      string                  `json:"sent_at"`
}

type briefAttachmentReport struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	IsImage bool   `json:"is_image"`
}

type briefStatusReport struct {
	Kind           string   `json:"kind"`
	Action         string   `json:"action"`
	Status         string   `json:"status"`
	Workspace      string   `json:"workspace,omitempty"`
	AdditionalDirs []string `json:"additional_dirs,omitempty"`
	Usage          string   `json:"usage"`
	NextCommand    string   `json:"next_command"`
}

func (a *App) Brief(args []string) error {
	return a.BriefWithFormat(args, "text")
}

func (a *App) BriefWithFormat(args []string, defaultFormat string) error {
	req, err := parseBriefArgs(args, defaultFormat)
	if err != nil {
		return err
	}
	if req.Message == "" {
		report := briefStatusReport{
			Kind:           "brief",
			Action:         "status",
			Status:         "ready",
			Workspace:      a.Workspace,
			AdditionalDirs: append([]string(nil), a.Config.AdditionalDirs...),
			Usage:          "codog brief MESSAGE [--status normal|proactive] [--attach PATH] [--json]",
			NextCommand:    "codog brief MESSAGE",
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		renderBriefStatus(a.Out, report)
		return nil
	}
	input, err := json.Marshal(map[string]any{
		"message":     req.Message,
		"status":      req.Status,
		"attachments": req.Attachments,
	})
	if err != nil {
		return err
	}
	result, err := (tools.BriefTool{
		Workspace:      a.Workspace,
		AdditionalDirs: a.Config.AdditionalDirs,
	}).Execute(context.Background(), input)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		fmt.Fprintln(a.Out, result)
		return nil
	}
	var report briefReport
	if err := json.Unmarshal([]byte(result), &report); err != nil {
		return err
	}
	renderBriefReport(a.Out, report)
	return nil
}

func renderBriefStatus(out io.Writer, report briefStatusReport) {
	fmt.Fprintln(out, "Brief")
	fmt.Fprintf(out, "  Status      %s\n", report.Status)
	if report.Workspace != "" {
		fmt.Fprintf(out, "  Workspace   %s\n", report.Workspace)
	}
	if len(report.AdditionalDirs) > 0 {
		fmt.Fprintf(out, "  Extra dirs   %s\n", strings.Join(report.AdditionalDirs, ", "))
	}
	fmt.Fprintf(out, "  Usage       %s\n", report.Usage)
}

const briefUsage = "codog brief MESSAGE [--status normal|proactive] [--attach PATH] [--json|--output-format text|json]"

func parseBriefArgs(args []string, defaultFormat string) (briefRequest, error) {
	if strings.TrimSpace(defaultFormat) == "" {
		defaultFormat = "text"
	}
	req := briefRequest{Status: "normal", Format: defaultFormat}
	var message []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "brief", Flag: arg, Usage: briefUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--status":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "brief", Flag: arg, Usage: briefUsage}
			}
			req.Status = args[index]
		case strings.HasPrefix(arg, "--status="):
			req.Status = strings.TrimPrefix(arg, "--status=")
		case arg == "--attach" || arg == "--attachment" || arg == "--file":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "brief", Flag: arg, Usage: briefUsage}
			}
			req.Attachments = append(req.Attachments, args[index])
		case strings.HasPrefix(arg, "--attach="):
			req.Attachments = append(req.Attachments, strings.TrimPrefix(arg, "--attach="))
		case strings.HasPrefix(arg, "--attachment="):
			req.Attachments = append(req.Attachments, strings.TrimPrefix(arg, "--attachment="))
		case strings.HasPrefix(arg, "--file="):
			req.Attachments = append(req.Attachments, strings.TrimPrefix(arg, "--file="))
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "brief", Option: arg, Usage: briefUsage}
		default:
			message = append(message, arg)
		}
	}
	req.Message = strings.TrimSpace(strings.Join(message, " "))
	req.Status = strings.ToLower(strings.TrimSpace(req.Status))
	switch req.Status {
	case "normal", "proactive":
	default:
		return req, invalidFlagValueError{
			Flag:    "--status",
			Value:   req.Status,
			Message: "brief status must be normal or proactive",
			Usage:   briefUsage,
		}
	}
	normalizedFormat, err := normalizeOutputFormat("brief", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func renderBriefReport(out io.Writer, report briefReport) {
	fmt.Fprintln(out, report.Message)
	fmt.Fprintf(out, "status: %s\n", report.Status)
	if len(report.Attachments) > 0 {
		fmt.Fprintln(out, "attachments:")
		for _, attachment := range report.Attachments {
			image := ""
			if attachment.IsImage {
				image = " image"
			}
			fmt.Fprintf(out, "- %s (%d bytes%s)\n", attachment.Path, attachment.Size, image)
		}
	}
}

func (a *App) Updater(ctx context.Context, args []string) error {
	var err error
	args, err = stripJSONStatusFlags("updater", args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		args = []string{"status"}
	}
	var payload any
	action := strings.ToLower(strings.TrimSpace(args[0]))
	switch action {
	case "status", "show":
		payload = a.updaterStatusReport(action)
	case "check":
		manifestURL := ""
		if len(args) > 1 {
			manifestURL = args[1]
		} else {
			manifestURL = strings.TrimSpace(a.Config.Future.UpdaterManifestURL)
		}
		if manifestURL == "" {
			return requiredArgumentError{Command: "updater check", Argument: "URL", Usage: "codog updater check [URL] [PUBLIC_KEY]"}
		}
		var result updater.CheckResult
		var err error
		if len(args) > 2 {
			result, err = updater.CheckSigned(ctx, version, manifestURL, args[2])
		} else {
			result, err = updater.Check(ctx, version, manifestURL)
		}
		if err != nil {
			return err
		}
		payload = result
	case "verify":
		if len(args) < 3 {
			return requiredArgumentError{Command: "updater verify", Argument: "URL PUBLIC_KEY", Usage: "codog updater verify URL PUBLIC_KEY"}
		}
		result, err := updater.CheckSigned(ctx, version, args[1], args[2])
		if err != nil {
			return err
		}
		payload = result
	case "download":
		manifestURL := ""
		if len(args) > 1 {
			manifestURL = args[1]
		} else {
			manifestURL = strings.TrimSpace(a.Config.Future.UpdaterManifestURL)
		}
		if manifestURL == "" {
			return requiredArgumentError{Command: "updater download", Argument: "URL", Usage: "codog updater download [URL] [PLATFORM] [DEST] [PUBLIC_KEY]"}
		}
		platform := ""
		if len(args) > 2 {
			platform = args[2]
		}
		dest := filepath.Join(a.Config.ConfigHome, "updater")
		if len(args) > 3 {
			dest = args[3]
		}
		var result updater.DownloadResult
		var err error
		if len(args) > 4 {
			result, err = updater.DownloadSigned(ctx, manifestURL, platform, dest, args[4])
		} else {
			result, err = updater.Download(ctx, manifestURL, platform, dest)
		}
		if err != nil {
			return err
		}
		payload = result
	case "install":
		if len(args) < 2 {
			return requiredArgumentError{Command: "updater install", Argument: "ARTIFACT", Usage: "codog updater install ARTIFACT [TARGET]"}
		}
		target := ""
		if len(args) > 2 {
			target = args[2]
		} else {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			target = exe
		}
		result, err := updater.Install(args[1], target)
		if err != nil {
			return err
		}
		payload = result
	case "rollback":
		target := ""
		if len(args) > 1 {
			target = args[1]
		} else {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			target = exe
		}
		result, err := updater.Rollback(target)
		if err != nil {
			return err
		}
		payload = result
	default:
		return unexpectedExtraArgsError{
			Command: "updater",
			Args:    []string{args[0]},
			Usage:   "codog updater [status|check|verify|download|install|rollback] [ARGS...]",
		}
	}
	report := updaterCommandReport{
		Kind:          "updater",
		Action:        action,
		Status:        "ok",
		SchemaVersion: 1,
		OutputFields:  []string{"kind", "action", "status", "schema_version", "output_fields", "status_values", "result"},
		StatusValues:  []string{"ok", "error"},
		Result:        payload,
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

type updaterCommandReport struct {
	Kind          string   `json:"kind"`
	Action        string   `json:"action"`
	Status        string   `json:"status"`
	SchemaVersion int      `json:"schema_version"`
	OutputFields  []string `json:"output_fields"`
	StatusValues  []string `json:"status_values"`
	Result        any      `json:"result"`
}

type updaterStatusReport struct {
	CurrentVersion string                 `json:"current_version"`
	Platform       string                 `json:"platform"`
	Executable     string                 `json:"executable,omitempty"`
	ConfigHome     string                 `json:"config_home,omitempty"`
	UpdateDir      string                 `json:"update_dir,omitempty"`
	DefaultTarget  string                 `json:"default_target,omitempty"`
	BackupPath     string                 `json:"backup_path,omitempty"`
	BackupPresent  bool                   `json:"backup_present"`
	TargetPresent  bool                   `json:"target_present"`
	Artifacts      []updater.ArtifactInfo `json:"artifacts"`
	ArtifactCount  int                    `json:"artifact_count"`
	Warnings       []string               `json:"warnings,omitempty"`
	ManifestURL    string                 `json:"manifest_url,omitempty"`
	ManifestSet    bool                   `json:"manifest_configured"`
	Commands       []string               `json:"commands"`
}

type installerStatusReport struct {
	Usage            string              `json:"usage"`
	RequiresArtifact bool                `json:"requires_artifact"`
	NextCommand      string              `json:"next_command"`
	Updater          updaterStatusReport `json:"updater"`
}

func stripJSONStatusFlags(command string, args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json" || arg == "--output-format=json":
			continue
		case arg == "--output-format":
			index++
			if index >= len(args) {
				return nil, missingFlagValueError{
					Command: command,
					Flag:    "--output-format",
					Usage:   fmt.Sprintf("codog %s [--json|--output-format json]", command),
				}
			}
			if !strings.EqualFold(args[index], "json") {
				return nil, outputFormatError{Command: command, Value: args[index], Expected: []string{"json"}}
			}
		default:
			out = append(out, arg)
		}
	}
	return out, nil
}

func (a *App) updaterStatusReport(action string) updaterStatusReport {
	executable := strings.TrimSpace(a.Executable)
	if executable == "" {
		if path, err := os.Executable(); err == nil {
			executable = path
		}
	}
	target := executable
	backupPath := ""
	backupPresent := false
	targetPresent := false
	if target != "" {
		backupPath = target + ".bak"
		if _, err := os.Stat(target); err == nil {
			targetPresent = true
		}
		if _, err := os.Stat(backupPath); err == nil {
			backupPresent = true
		}
	}
	updateDir := ""
	if strings.TrimSpace(a.Config.ConfigHome) != "" {
		updateDir = filepath.Join(a.Config.ConfigHome, "updater")
	}
	artifacts, err := updater.ListArtifacts(updateDir)
	warnings := []string{}
	if err != nil {
		warnings = append(warnings, "Could not list updater artifacts: "+err.Error())
		artifacts = []updater.ArtifactInfo{}
	}
	return updaterStatusReport{
		CurrentVersion: version,
		Platform:       updater.PlatformKey(),
		Executable:     executable,
		ConfigHome:     a.Config.ConfigHome,
		UpdateDir:      updateDir,
		DefaultTarget:  target,
		BackupPath:     backupPath,
		BackupPresent:  backupPresent,
		TargetPresent:  targetPresent,
		Artifacts:      artifacts,
		ArtifactCount:  len(artifacts),
		Warnings:       warnings,
		ManifestURL:    strings.TrimSpace(a.Config.Future.UpdaterManifestURL),
		ManifestSet:    strings.TrimSpace(a.Config.Future.UpdaterManifestURL) != "",
		Commands:       []string{"status", "show", "check", "verify", "download", "install", "rollback"},
	}
}

type enterpriseAuditReport struct {
	Kind                    string                 `json:"kind"`
	Action                  string                 `json:"action"`
	Status                  string                 `json:"status"`
	ConfigHome              string                 `json:"config_home,omitempty"`
	PolicyPath              string                 `json:"policy_path,omitempty"`
	PolicyConfigured        bool                   `json:"policy_configured"`
	PolicyPublicKeyPresent  bool                   `json:"policy_public_key_present"`
	PolicySignatureRequired bool                   `json:"policy_signature_required"`
	PolicySignatureValid    bool                   `json:"policy_signature_valid,omitempty"`
	Policy                  *config.ManagedPolicy  `json:"policy,omitempty"`
	EffectivePermissionMode string                 `json:"effective_permission_mode,omitempty"`
	PermissionRules         config.PermissionRules `json:"permission_rules,omitempty"`
	Summary                 enterpriseAuditSummary `json:"summary"`
	Events                  []audit.Event          `json:"events"`
	Warnings                []string               `json:"warnings,omitempty"`
}

type enterpriseAuditSummary struct {
	Limit            int            `json:"limit"`
	EventsReturned   int            `json:"events_returned"`
	PermissionEvents int            `json:"permission_events"`
	DeniedEvents     int            `json:"denied_events"`
	ErrorEvents      int            `json:"error_events"`
	Tools            map[string]int `json:"tools,omitempty"`
	EventTypes       map[string]int `json:"event_types,omitempty"`
}

type enterpriseVerifyReport struct {
	Kind           string               `json:"kind"`
	Action         string               `json:"action"`
	Status         string               `json:"status"`
	Path           string               `json:"path"`
	SignatureValid bool                 `json:"signature_valid"`
	Policy         config.ManagedPolicy `json:"policy"`
}

func (a *App) Enterprise(args []string) error {
	args = stripEnterpriseOutputFormatFlags(args)
	var payload any
	action := "audit"
	if len(args) > 0 {
		action = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch action {
	case "", "audit", "status", "show":
		limit := audit.DefaultLimit
		if len(args) > 1 {
			parsed, err := strconv.Atoi(args[1])
			if err != nil {
				return invalidFlagValueError{
					Flag:    "limit",
					Value:   args[1],
					Message: "enterprise audit limit must be an integer",
					Usage:   "codog enterprise [audit|status|show] [limit] [--json|--output-format json]",
				}
			}
			limit = parsed
		}
		report, err := a.enterpriseAuditReport(limit)
		if err != nil {
			return err
		}
		payload = report
	case "verify":
		return enterpriseVerify(a.Out, args)
	default:
		return unexpectedExtraArgsError{
			Command: "enterprise",
			Args:    []string{args[0]},
			Usage:   "codog enterprise [audit|status|show] [limit] | enterprise verify POLICY PUBLIC_KEY",
		}
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func stripEnterpriseOutputFormatFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--json" || strings.HasPrefix(arg, "--output-format=") {
			continue
		}
		if arg == "--output-format" && index+1 < len(args) {
			index++
			continue
		}
		out = append(out, arg)
	}
	return out
}

func (a *App) enterpriseAuditReport(limit int) (enterpriseAuditReport, error) {
	events, err := audit.NewStore(a.Config.ConfigHome).List(limit)
	if err != nil {
		return enterpriseAuditReport{}, err
	}
	report := enterpriseAuditReport{
		Kind:                    "enterprise",
		Action:                  "audit",
		Status:                  "ok",
		ConfigHome:              a.Config.ConfigHome,
		PolicyPath:              strings.TrimSpace(a.Config.Future.EnterprisePolicy),
		PolicyConfigured:        strings.TrimSpace(a.Config.Future.EnterprisePolicy) != "",
		PolicyPublicKeyPresent:  strings.TrimSpace(a.Config.Future.EnterprisePolicyPublicKey) != "",
		PolicySignatureRequired: strings.TrimSpace(a.Config.Future.EnterprisePolicyPublicKey) != "",
		EffectivePermissionMode: a.Config.PermissionMode,
		PermissionRules:         a.Config.PermissionRules,
		Summary: enterpriseAuditSummary{
			Limit:          limit,
			EventsReturned: len(events),
			Tools:          map[string]int{},
			EventTypes:     map[string]int{},
		},
		Events: events,
	}
	for _, event := range events {
		if event.Type != "" {
			report.Summary.EventTypes[event.Type]++
		}
		if event.ToolName != "" {
			report.Summary.Tools[event.ToolName]++
		}
		if event.Type == "permission" {
			report.Summary.PermissionEvents++
		}
		if event.Allowed != nil && !*event.Allowed {
			report.Summary.DeniedEvents++
		}
		if event.IsError {
			report.Summary.ErrorEvents++
		}
	}
	if report.PolicyConfigured {
		policy, err := config.LoadManagedPolicyFile(report.PolicyPath)
		if err != nil {
			report.Status = "warn"
			report.Warnings = append(report.Warnings, "managed policy could not be loaded: "+err.Error())
		} else {
			if report.PolicyPublicKeyPresent {
				if err := config.VerifyManagedPolicy(policy, a.Config.Future.EnterprisePolicyPublicKey); err != nil {
					report.Status = "warn"
					report.Warnings = append(report.Warnings, "managed policy signature verification failed: "+err.Error())
				} else {
					report.PolicySignatureValid = true
				}
			}
			policy.Signature = ""
			report.Policy = &policy
		}
	}
	if len(report.Summary.Tools) == 0 {
		report.Summary.Tools = nil
	}
	if len(report.Summary.EventTypes) == 0 {
		report.Summary.EventTypes = nil
	}
	return report, nil
}

func enterpriseVerify(out io.Writer, args []string) error {
	if len(args) < 3 {
		return requiredArgumentError{
			Command:  "enterprise verify",
			Argument: "POLICY PUBLIC_KEY",
			Usage:    "codog enterprise verify POLICY PUBLIC_KEY",
		}
	}
	policy, err := config.VerifyManagedPolicyFile(args[1], args[2])
	if err != nil {
		return err
	}
	policy.Signature = ""
	payload := enterpriseVerifyReport{
		Kind:           "enterprise",
		Action:         "verify",
		Status:         "ok",
		Path:           args[1],
		SignatureValid: true,
		Policy:         policy,
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(out, string(data))
	return nil
}

type dumpManifestsRequest struct {
	Format       string
	ManifestsDir string
}

func (a *App) DumpManifests(args []string) error {
	req, err := parseDumpManifestsArgs(args)
	if err != nil {
		return renderCLIErrorWhenStructured(a.Out, err, req.Format)
	}
	workspace := a.Workspace
	registry := a.Tools
	if req.ManifestsDir != "" {
		workspace, err = resolveManifestDiscoveryRoot(req.ManifestsDir)
		if err != nil {
			return renderCLIErrorWhenStructured(a.Out, err, req.Format)
		}
		registry = tools.NewRegistry(workspace)
	}
	report, err := manifests.Build(workspace, a.Config.ConfigHome, registry)
	if err != nil {
		return renderCLIErrorWhenStructured(a.Out, err, req.Format)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderManifestDump(a.Out, report)
	return nil
}

func parseDumpManifestsArgs(args []string) (dumpManifestsRequest, error) {
	req := dumpManifestsRequest{Format: "text"}
	if format, ok := scanDumpManifestsOutputFormat(args); ok {
		req.Format = format
	}
	const usage = "codog dump-manifests [--manifests-dir PATH] [--json|--output-format text|json]"
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "dump-manifests", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--manifests-dir":
			index++
			if index >= len(args) || strings.TrimSpace(args[index]) == "" || isDumpManifestsOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "dump-manifests",
					Flag:    "--manifests-dir",
					Usage:   usage,
				}
			}
			req.ManifestsDir = args[index]
		case strings.HasPrefix(arg, "--manifests-dir="):
			value := strings.TrimPrefix(arg, "--manifests-dir=")
			if strings.TrimSpace(value) == "" {
				return req, missingFlagValueError{
					Command: "dump-manifests",
					Flag:    "--manifests-dir",
					Usage:   usage,
				}
			}
			req.ManifestsDir = value
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "dump-manifests", Option: arg, Usage: usage}
			}
			return req, unexpectedExtraArgsError{Command: "dump-manifests", Args: []string{arg}, Usage: usage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("dump-manifests", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func scanDumpManifestsOutputFormat(args []string) (string, bool) {
	format := ""
	ok := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			format = "json"
			ok = true
		case arg == "--output-format" || arg == "-o":
			if index+1 >= len(args) {
				continue
			}
			index++
			format = args[index]
			ok = true
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
			ok = true
		}
	}
	return format, ok
}

func isDumpManifestsOutputFormatFlag(arg string) bool {
	return arg == "--json" || arg == "--output-format" || arg == "-o" || strings.HasPrefix(arg, "--output-format=")
}

func resolveManifestDiscoveryRoot(path string) (string, error) {
	root, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("missing_manifests: manifest discovery directory does not exist: %s", root)
		}
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("missing_manifests: manifest discovery path is not a directory: %s", root)
	}
	return root, nil
}

func renderManifestDump(out io.Writer, report manifests.Report) {
	fmt.Fprintln(out, "Manifest Dump")
	fmt.Fprintf(out, "  Source           %s\n", report.Source)
	fmt.Fprintf(out, "  Workspace        %s\n", report.Workspace)
	fmt.Fprintf(out, "  Commands         %d\n", report.Commands)
	fmt.Fprintf(out, "  Tools            %d\n", report.Tools)
	fmt.Fprintf(out, "  Agents           %d\n", report.Agents)
	fmt.Fprintf(out, "  Skills           %d\n", report.Skills)
}

func (a *App) SystemPromptCommand(args []string) error {
	format, err := parseSimpleOutputFormat("system-prompt", args)
	if err != nil {
		return err
	}
	prompt := a.systemPrompt()
	if format == "json" {
		data, _ := json.MarshalIndent(map[string]any{
			"kind":          "system-prompt",
			"action":        "show",
			"status":        "ok",
			"system_prompt": prompt,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, prompt)
	return nil
}

type toolDetailsRequest struct {
	Tool   string
	Format string
}

type toolDetailsReport struct {
	Kind    string         `json:"kind"`
	Action  string         `json:"action"`
	Status  string         `json:"status"`
	Tool    tools.ToolInfo `json:"tool"`
	Aliases []string       `json:"aliases,omitempty"`
}

func (a *App) ToolDetails(args []string) error {
	return a.ToolDetailsWithFormat(args, "text")
}

func (a *App) ToolDetailsWithFormat(args []string, defaultFormat string) error {
	req, err := parseToolDetailsArgs(args, defaultFormat)
	if err != nil {
		return renderCLIError(a.Out, err, req.Format)
	}
	if a.Tools == nil {
		return renderCLIError(a.Out, errors.New("tool registry is not initialized"), req.Format)
	}
	info, ok := a.Tools.Info(req.Tool)
	if !ok {
		return renderCLIError(a.Out, toolNameError{
			Argument:  "tool-details",
			ToolName:  req.Tool,
			Available: toolDetailAvailableNames(a.Tools),
			Aliases:   tools.ClaudeToolAliases(),
		}, req.Format)
	}
	report := toolDetailsReport{
		Kind:    "tool_details",
		Action:  "show",
		Status:  "ok",
		Tool:    info,
		Aliases: toolDetailAliases(info.Name),
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderToolDetailsText(a.Out, report)
	return nil
}

const toolDetailsUsage = "codog tool-details TOOL [--output-format text|json]"

func parseToolDetailsArgs(args []string, defaultFormat string) (toolDetailsRequest, error) {
	if strings.TrimSpace(defaultFormat) == "" {
		defaultFormat = "text"
	}
	req := toolDetailsRequest{Format: defaultFormat}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "tool-details", Flag: arg, Usage: toolDetailsUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{
				Command: "tool-details",
				Option:  arg,
				Usage:   toolDetailsUsage,
			}
		default:
			positionals = append(positionals, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("tool-details", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	switch len(positionals) {
	case 0:
		return req, missingToolNameError{
			Command: "tool-details",
			Usage:   toolDetailsUsage,
		}
	case 1:
		req.Tool = positionals[0]
	default:
		return req, unexpectedExtraArgsError{
			Command: "tool-details",
			Args:    positionals[1:],
			Usage:   toolDetailsUsage,
		}
	}
	return req, nil
}

func renderToolDetailsText(out io.Writer, report toolDetailsReport) {
	renderToolInfo(out, report.Tool)
	if len(report.Aliases) > 0 {
		fmt.Fprintf(out, "  Aliases          %s\n", strings.Join(report.Aliases, ", "))
	}
}

func toolDetailAvailableNames(registry *tools.Registry) []string {
	if registry == nil {
		return nil
	}
	infos := registry.Infos()
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	sort.Strings(names)
	return names
}

func toolDetailAliases(canonical string) []string {
	aliases := []string{}
	for alias, target := range tools.ClaudeToolAliases() {
		if target == canonical {
			aliases = append(aliases, alias)
		}
	}
	sort.Strings(aliases)
	return aliases
}

func (a *App) Upgrade(ctx context.Context, args []string) error {
	var err error
	args, err = stripJSONStatusFlags("upgrade", args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return a.Updater(ctx, []string{"status"})
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "status", "show", "check", "verify", "download", "install", "rollback":
		return a.Updater(ctx, args)
	default:
		return a.Updater(ctx, append([]string{"check"}, args...))
	}
}

func (a *App) Install(ctx context.Context, args []string) error {
	var err error
	args, err = stripJSONStatusFlags("install", args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		report := updaterCommandReport{
			Kind:   "install",
			Action: "status",
			Status: "ok",
			Result: installerStatusReport{
				Usage:            "codog install ARTIFACT [TARGET]",
				RequiresArtifact: true,
				NextCommand:      "codog install ARTIFACT [TARGET]",
				Updater:          a.updaterStatusReport("status"),
			},
		}
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	return a.Updater(ctx, append([]string{"install"}, args...))
}

func (a *App) Background(args []string) error {
	return a.BackgroundWithOverrides(args, config.FlagOverrides{})
}

const backgroundUsage = "codog background list [session-id] | run [--restart[=on-failure|always]] [--restart-limit N] [--restart-delay SECONDS] COMMAND | board [stalled-after-seconds] | heartbeat ID [--status STATUS] [--transport-alive true|false] [--observed-at RFC3339] | status ID | stop ID | restart ID | logs ID [bytes|--bytes N] | watch ID [offset|--offset N] [--max-events N] | prune [days] [keep] | supervise [--json|--output-format text|json]"

type backgroundCommandReport struct {
	Kind      string                      `json:"kind"`
	Action    string                      `json:"action"`
	Status    string                      `json:"status"`
	Count     int                         `json:"count,omitempty"`
	SessionID string                      `json:"session_id,omitempty"`
	TaskID    string                      `json:"task_id,omitempty"`
	Tasks     []background.Task           `json:"tasks,omitempty"`
	Task      *background.Task            `json:"task,omitempty"`
	Board     *background.LaneBoard       `json:"board,omitempty"`
	Prune     *background.PruneResult     `json:"prune,omitempty"`
	Supervise *background.SuperviseResult `json:"supervise,omitempty"`
	Log       string                      `json:"log,omitempty"`
	Bytes     int                         `json:"bytes,omitempty"`
	Message   string                      `json:"message,omitempty"`
}

type cronRequest struct {
	Action      string
	Format      string
	Schedule    string
	Prompt      string
	Description string
	ID          string
	Now         time.Time
}

type cronCommandReport struct {
	Kind    string            `json:"kind"`
	Action  string            `json:"action"`
	Status  string            `json:"status"`
	Count   int               `json:"count,omitempty"`
	Entries []cron.Entry      `json:"entries,omitempty"`
	Entry   *cron.Entry       `json:"entry,omitempty"`
	Tasks   []background.Task `json:"tasks,omitempty"`
	Message string            `json:"message,omitempty"`
}

func (a *App) Cron(args []string) error {
	return a.CronWithFormat(args, "text")
}

func (a *App) CronWithFormat(args []string, defaultFormat string) error {
	req, err := parseCronArgsWithDefault(args, defaultFormat)
	if err != nil {
		return err
	}
	store := cron.NewStore(a.Config.ConfigHome)
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	report := cronCommandReport{Kind: "cron", Action: req.Action, Status: "ok"}
	switch req.Action {
	case "list":
		entries, err := store.List()
		if err != nil {
			return err
		}
		report.Entries = entries
		report.Count = len(entries)
	case "show":
		entry, err := store.Get(req.ID)
		if err != nil {
			return err
		}
		report.Entry = &entry
		report.Count = 1
	case "create":
		entry, err := store.Create(req.Schedule, req.Prompt, req.Description)
		if err != nil {
			return err
		}
		report.Entry = &entry
		report.Count = 1
		report.Message = "Cron entry created"
	case "delete":
		entry, err := store.Delete(req.ID)
		if err != nil {
			return err
		}
		report.Entry = &entry
		report.Count = 1
		report.Message = "Cron entry deleted"
	case "enable", "disable":
		entry, err := store.SetEnabled(req.ID, req.Action == "enable")
		if err != nil {
			return err
		}
		report.Entry = &entry
		report.Count = 1
		report.Message = "Cron entry " + req.Action + "d"
	case "due":
		entries, err := store.Due(now)
		if err != nil {
			return err
		}
		report.Entries = entries
		report.Count = len(entries)
	case "mark-run":
		entry, err := store.MarkRun(req.ID, now)
		if err != nil {
			return err
		}
		report.Entry = &entry
		report.Count = 1
		report.Message = "Cron entry marked as run"
	case "run-due":
		entries, err := store.Due(now)
		if err != nil {
			return err
		}
		exe, err := a.executablePath()
		if err != nil {
			return err
		}
		taskStore := background.NewStore(a.Config.ConfigHome)
		for _, entry := range entries {
			task, err := taskStore.RunWithOptions(buildCronPromptCommand(exe, entry.Prompt), a.Workspace, background.RunOptions{Kind: "cron"})
			if err != nil {
				return err
			}
			updated, err := store.MarkRun(entry.ID, now)
			if err != nil {
				return err
			}
			report.Tasks = append(report.Tasks, task)
			report.Entries = append(report.Entries, updated)
			a.runTaskCreatedHook(context.Background(), task)
			a.runNotificationHook(context.Background(), "cron_task_started", "Cron task started", fmt.Sprintf("Cron entry %s started background task %s", entry.ID, task.ID))
		}
		report.Count = len(report.Tasks)
	default:
		return fmt.Errorf("unknown cron command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderCronReport(a.Out, report)
	return nil
}

func parseCronArgsWithDefault(args []string, defaultFormat string) (cronRequest, error) {
	if strings.TrimSpace(defaultFormat) == "" {
		defaultFormat = "text"
	}
	req := cronRequest{Action: "list", Format: defaultFormat}
	actionSet := false
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, missingFlagValueError{Command: "cron", Flag: arg, Usage: cronUsage}
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--description" || arg == "-d":
			i++
			if i >= len(args) || isOutputFormatFlag(args[i]) {
				return req, missingFlagValueError{Command: "cron", Flag: arg, Usage: cronUsage}
			}
			req.Description = args[i]
		case strings.HasPrefix(arg, "--description="):
			req.Description = strings.TrimPrefix(arg, "--description=")
		case arg == "--now":
			i++
			if i >= len(args) || isOutputFormatFlag(args[i]) {
				return req, missingFlagValueError{Command: "cron", Flag: arg, Usage: cronUsage}
			}
			parsed, err := time.Parse(time.RFC3339, args[i])
			if err != nil {
				return req, invalidFlagValueError{Flag: "--now", Value: args[i], Message: "cron now timestamp must be RFC3339", Usage: cronUsage}
			}
			req.Now = parsed
		case strings.HasPrefix(arg, "--now="):
			value := strings.TrimPrefix(arg, "--now=")
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return req, invalidFlagValueError{Flag: "--now", Value: value, Message: "cron now timestamp must be RFC3339", Usage: cronUsage}
			}
			req.Now = parsed
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "cron", Option: arg, Usage: cronUsage}
		case !actionSet && isCronAction(arg):
			req.Action = normalizeCronAction(arg)
			actionSet = true
		case !actionSet:
			return req, unknownActionError{
				Command:     "cron",
				Action:      arg,
				Expected:    append([]string(nil), cronActionCandidates...),
				Suggestions: toolnames.Suggestions(arg, cronActionCandidates, 4),
				Usage:       cronUsage,
			}
		default:
			positionals = append(positionals, arg)
		}
	}
	format, err := normalizeOutputFormat("cron", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = format
	switch req.Action {
	case "list":
		if len(positionals) != 0 {
			command := "cron list"
			if !actionSet {
				command = "cron"
			}
			return req, unexpectedExtraArgsError{
				Command: command,
				Args:    append([]string(nil), positionals...),
				Usage:   cronUsage,
			}
		}
	case "create":
		if len(positionals) < 2 {
			return req, requiredArgumentError{Command: "cron create", Argument: "SCHEDULE PROMPT", Usage: cronUsage}
		}
		req.Schedule = positionals[0]
		req.Prompt = strings.Join(positionals[1:], " ")
	case "show", "delete", "enable", "disable", "mark-run":
		if len(positionals) != 1 {
			return req, requiredArgumentError{Command: "cron " + req.Action, Argument: "CRON_ID", Usage: cronUsage}
		}
		req.ID = positionals[0]
	case "due", "run-due":
		if len(positionals) != 0 {
			return req, unexpectedExtraArgsError{Command: "cron " + req.Action, Args: append([]string(nil), positionals...), Usage: cronUsage}
		}
	default:
		return req, unexpectedExtraArgsError{
			Command: "cron",
			Args:    []string{req.Action},
			Usage:   cronUsage,
		}
	}
	return req, nil
}

const cronUsage = "codog cron [list|ls|show|get|create|add|new|delete|remove|rm|enable|disable|due|mark-run|mark|touch|run-due|run] [ARGS...] [--json|--output-format text|json]"

var cronActionCandidates = []string{"list", "ls", "show", "get", "create", "add", "new", "delete", "remove", "rm", "enable", "disable", "due", "mark-run", "mark", "touch", "run-due", "run"}

func isCronAction(value string) bool {
	switch normalizeCronAction(value) {
	case "list", "show", "create", "delete", "enable", "disable", "due", "mark-run", "run-due":
		return true
	default:
		return false
	}
}

func normalizeCronAction(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "list", "ls":
		return "list"
	case "show", "get":
		return "show"
	case "create", "add", "new":
		return "create"
	case "delete", "remove", "rm":
		return "delete"
	case "enable", "disable":
		return strings.ToLower(strings.TrimSpace(value))
	case "due":
		return "due"
	case "mark-run", "mark", "touch":
		return "mark-run"
	case "run-due", "run":
		return "run-due"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func renderCronReport(out io.Writer, report cronCommandReport) {
	fmt.Fprintln(out, "Cron")
	switch report.Action {
	case "list":
		fmt.Fprintf(out, "  Entries          %d\n", report.Count)
		for _, entry := range report.Entries {
			state := "disabled"
			if entry.Enabled {
				state = "enabled"
			}
			description := strings.TrimSpace(entry.Description)
			if description == "" {
				description = strings.TrimSpace(entry.Prompt)
			}
			fmt.Fprintf(out, "  %s  %s  %s  %s\n", entry.ID, entry.Schedule, state, description)
		}
	case "show", "create", "delete", "enable", "disable", "mark-run":
		if report.Entry == nil {
			return
		}
		fmt.Fprintf(out, "  Action           %s\n", report.Action)
		fmt.Fprintf(out, "  ID               %s\n", report.Entry.ID)
		fmt.Fprintf(out, "  Schedule         %s\n", report.Entry.Schedule)
		state := "disabled"
		if report.Entry.Enabled {
			state = "enabled"
		}
		fmt.Fprintf(out, "  Status           %s\n", state)
		if report.Entry.Description != "" {
			fmt.Fprintf(out, "  Description      %s\n", report.Entry.Description)
		}
		if report.Entry.Prompt != "" {
			fmt.Fprintf(out, "  Prompt           %s\n", report.Entry.Prompt)
		}
		fmt.Fprintf(out, "  Runs             %d\n", report.Entry.RunCount)
		if report.Message != "" {
			fmt.Fprintf(out, "  Message          %s\n", report.Message)
		}
	case "due":
		fmt.Fprintf(out, "  Due              %d\n", report.Count)
		for _, entry := range report.Entries {
			fmt.Fprintf(out, "  %s  %s  %s\n", entry.ID, entry.Schedule, entry.Description)
		}
	case "run-due":
		fmt.Fprintf(out, "  Started          %d\n", report.Count)
		for _, task := range report.Tasks {
			fmt.Fprintf(out, "  %s  %s\n", task.ID, task.Status)
		}
	}
}

func (a *App) executablePath() (string, error) {
	if strings.TrimSpace(a.Executable) != "" {
		return a.Executable, nil
	}
	return resolveExecutablePath()
}

func buildCronPromptCommand(exe string, prompt string) string {
	return strings.Join([]string{shellQuote(exe), "prompt", shellQuote(prompt)}, " ")
}

func buildDetachedPromptCommand(configHome string, exe string, prompt string) string {
	parts := []string{}
	if strings.TrimSpace(configHome) != "" {
		parts = append(parts, "CODOG_CONFIG_HOME="+shellQuote(configHome))
	}
	parts = append(parts, shellQuote(exe), "prompt", shellQuote(prompt))
	return strings.Join(parts, " ")
}

type teamRequest struct {
	Action    string
	Format    string
	Name      string
	ID        string
	Tasks     []team.TaskSpec
	Status    string
	SessionID string
	Limit     int64
	Offset    int64
	MaxEvents int
}

type teamCommandReport struct {
	Kind         string            `json:"kind"`
	Action       string            `json:"action"`
	Status       string            `json:"status"`
	Count        int               `json:"count,omitempty"`
	Teams        []team.Team       `json:"teams,omitempty"`
	Team         *team.Team        `json:"team,omitempty"`
	Tasks        []background.Task `json:"tasks,omitempty"`
	Logs         []teamTaskLog     `json:"logs,omitempty"`
	MissingTasks []string          `json:"missing_tasks,omitempty"`
	StoppedTasks []string          `json:"stopped_tasks,omitempty"`
	Message      string            `json:"message,omitempty"`
}

type teamTaskLog struct {
	TaskID string `json:"task_id"`
	Log    string `json:"log,omitempty"`
	Bytes  int    `json:"bytes"`
	Error  string `json:"error,omitempty"`
}

type teamWatchEvent struct {
	Kind     string           `json:"kind"`
	TeamID   string           `json:"team_id"`
	TeamName string           `json:"team_name,omitempty"`
	Type     string           `json:"type"`
	TaskID   string           `json:"task_id"`
	Offset   int64            `json:"offset,omitempty"`
	Data     string           `json:"data,omitempty"`
	Status   string           `json:"status,omitempty"`
	Error    string           `json:"error,omitempty"`
	Task     *background.Task `json:"task,omitempty"`
}

func (a *App) Team(args []string) error {
	return a.TeamWithFormat(args, "text")
}

func (a *App) TeamWithFormat(args []string, defaultFormat string) error {
	req, err := parseTeamArgsWithDefault(args, defaultFormat)
	if err != nil {
		return err
	}
	store := team.NewStore(a.Config.ConfigHome)
	taskStore := background.NewStore(a.Config.ConfigHome)
	report := teamCommandReport{Kind: "team", Action: req.Action, Status: "ok"}
	switch req.Action {
	case "list":
		teams, err := store.List()
		if err != nil {
			return err
		}
		for _, item := range teams {
			if req.Status != "" && !strings.EqualFold(item.Status, req.Status) {
				continue
			}
			report.Teams = append(report.Teams, item)
		}
		report.Count = len(report.Teams)
	case "get":
		item, err := store.Get(req.ID)
		if err != nil {
			return err
		}
		report.Team = &item
		report.Count = 1
	case "status":
		item, tasks, missing, err := refreshTeamStatus(store, taskStore, req.ID)
		if err != nil {
			return err
		}
		report.Team = &item
		report.Tasks = tasks
		report.MissingTasks = missing
		report.Count = len(tasks)
		if len(missing) > 0 {
			report.Message = fmt.Sprintf("%d team tasks were missing from the background store", len(missing))
		}
	case "logs":
		item, err := store.Get(req.ID)
		if err != nil {
			return err
		}
		report.Team = &item
		report.Logs = readTeamLogs(taskStore, item, req.Limit)
		report.Count = len(report.Logs)
	case "watch":
		item, err := store.Get(req.ID)
		if err != nil {
			return err
		}
		return a.watchTeam(context.Background(), taskStore, item, req)
	case "create":
		exe, err := a.executablePath()
		if err != nil {
			return err
		}
		taskIDs := make([]string, 0, len(req.Tasks))
		taskSpecs := append([]team.TaskSpec(nil), req.Tasks...)
		for index, spec := range taskSpecs {
			prompt := strings.TrimSpace(spec.Prompt)
			if spec.Description != "" {
				prompt = "Task: " + strings.TrimSpace(spec.Description) + "\n\n" + prompt
			}
			task, err := taskStore.RunWithOptions(buildCronPromptCommand(exe, prompt), a.Workspace, background.RunOptions{Kind: "team", SessionID: req.SessionID})
			if err != nil {
				return err
			}
			taskIDs = append(taskIDs, task.ID)
			taskSpecs[index].TaskID = task.ID
			report.Tasks = append(report.Tasks, task)
			a.runTaskCreatedHook(context.Background(), task)
			a.runNotificationHook(context.Background(), "team_task_started", "Team task started", fmt.Sprintf("Team %s started background task %s", req.Name, task.ID))
		}
		item, err := store.Create(req.Name, taskSpecs, taskIDs)
		if err != nil {
			return err
		}
		report.Team = &item
		report.Count = len(taskIDs)
		report.Message = "Team created"
	case "delete":
		existing, err := store.Get(req.ID)
		if err != nil {
			return err
		}
		for _, id := range existing.TaskIDs {
			task, err := taskStore.Stop(id)
			if err != nil {
				continue
			}
			report.StoppedTasks = append(report.StoppedTasks, task.ID)
			a.runTaskCompletedHook(context.Background(), task, "team_deleted")
		}
		item, err := store.MarkDeleted(req.ID)
		if err != nil {
			return err
		}
		report.Team = &item
		report.Count = 1
		report.Message = "Team deleted"
	default:
		return fmt.Errorf("unknown team command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderTeamReport(a.Out, report)
	return nil
}

func parseTeamArgsWithDefault(args []string, defaultFormat string) (teamRequest, error) {
	if strings.TrimSpace(defaultFormat) == "" {
		defaultFormat = "text"
	}
	req := teamRequest{Action: "list", Format: defaultFormat, Limit: 64 * 1024}
	actionSet := false
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, missingFlagValueError{Command: "team", Flag: arg, Usage: teamUsage}
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--task":
			i++
			if i >= len(args) || isOutputFormatFlag(args[i]) {
				return req, missingFlagValueError{Command: "team", Flag: arg, Usage: teamUsage}
			}
			req.Tasks = append(req.Tasks, parseTeamTaskSpec(args[i]))
		case strings.HasPrefix(arg, "--task="):
			req.Tasks = append(req.Tasks, parseTeamTaskSpec(strings.TrimPrefix(arg, "--task=")))
		case arg == "--status":
			i++
			if i >= len(args) || isOutputFormatFlag(args[i]) {
				return req, missingFlagValueError{Command: "team", Flag: arg, Usage: teamUsage}
			}
			req.Status = strings.TrimSpace(args[i])
		case strings.HasPrefix(arg, "--status="):
			req.Status = strings.TrimSpace(strings.TrimPrefix(arg, "--status="))
		case arg == "--session-id" || arg == "--session":
			i++
			if i >= len(args) || isOutputFormatFlag(args[i]) {
				return req, missingFlagValueError{Command: "team", Flag: arg, Usage: teamUsage}
			}
			req.SessionID = strings.TrimSpace(args[i])
		case strings.HasPrefix(arg, "--session-id="):
			req.SessionID = strings.TrimSpace(strings.TrimPrefix(arg, "--session-id="))
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimSpace(strings.TrimPrefix(arg, "--session="))
		case arg == "--bytes" || arg == "--limit":
			i++
			if i >= len(args) || isOutputFormatFlag(args[i]) {
				return req, missingFlagValueError{Command: "team", Flag: arg, Usage: teamUsage}
			}
			limit, err := strconv.ParseInt(args[i], 10, 64)
			if err != nil || limit < 0 {
				return req, invalidFlagValueError{Flag: arg, Value: args[i], Message: "team log byte limit must be a non-negative integer", Usage: teamUsage}
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--bytes="):
			value := strings.TrimPrefix(arg, "--bytes=")
			limit, err := strconv.ParseInt(value, 10, 64)
			if err != nil || limit < 0 {
				return req, invalidFlagValueError{Flag: "--bytes", Value: value, Message: "team log byte limit must be a non-negative integer", Usage: teamUsage}
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			value := strings.TrimPrefix(arg, "--limit=")
			limit, err := strconv.ParseInt(value, 10, 64)
			if err != nil || limit < 0 {
				return req, invalidFlagValueError{Flag: "--limit", Value: value, Message: "team log byte limit must be a non-negative integer", Usage: teamUsage}
			}
			req.Limit = limit
		case arg == "--offset":
			i++
			if i >= len(args) || isOutputFormatFlag(args[i]) {
				return req, missingFlagValueError{Command: "team", Flag: arg, Usage: teamUsage}
			}
			offset, err := strconv.ParseInt(args[i], 10, 64)
			if err != nil || offset < 0 {
				return req, invalidFlagValueError{Flag: arg, Value: args[i], Message: "team watch offset must be a non-negative integer", Usage: teamUsage}
			}
			req.Offset = offset
		case strings.HasPrefix(arg, "--offset="):
			value := strings.TrimPrefix(arg, "--offset=")
			offset, err := strconv.ParseInt(value, 10, 64)
			if err != nil || offset < 0 {
				return req, invalidFlagValueError{Flag: "--offset", Value: value, Message: "team watch offset must be a non-negative integer", Usage: teamUsage}
			}
			req.Offset = offset
		case arg == "--max-events":
			i++
			if i >= len(args) || isOutputFormatFlag(args[i]) {
				return req, missingFlagValueError{Command: "team", Flag: arg, Usage: teamUsage}
			}
			events, err := strconv.Atoi(args[i])
			if err != nil || events < 0 {
				return req, invalidFlagValueError{Flag: arg, Value: args[i], Message: "team watch max events must be a non-negative integer", Usage: teamUsage}
			}
			req.MaxEvents = events
		case strings.HasPrefix(arg, "--max-events="):
			value := strings.TrimPrefix(arg, "--max-events=")
			events, err := strconv.Atoi(value)
			if err != nil || events < 0 {
				return req, invalidFlagValueError{Flag: "--max-events", Value: value, Message: "team watch max events must be a non-negative integer", Usage: teamUsage}
			}
			req.MaxEvents = events
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "team", Option: arg, Usage: teamUsage}
		case !actionSet && isTeamAction(arg):
			req.Action = normalizeTeamAction(arg)
			actionSet = true
		case !actionSet:
			return req, unknownActionError{
				Command:     "team",
				Action:      arg,
				Expected:    append([]string(nil), teamActionCandidates...),
				Suggestions: toolnames.Suggestions(arg, teamActionCandidates, 4),
				Usage:       teamUsage,
			}
		default:
			positionals = append(positionals, arg)
		}
	}
	format, err := normalizeOutputFormat("team", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = format
	switch req.Action {
	case "list":
		if len(positionals) != 0 {
			command := "team list"
			if !actionSet {
				command = "team"
			}
			return req, unexpectedExtraArgsError{
				Command: command,
				Args:    append([]string(nil), positionals...),
				Usage:   teamUsage,
			}
		}
	case "get", "status", "logs", "watch", "delete":
		if len(positionals) != 1 {
			return req, requiredArgumentError{Command: "team " + req.Action, Argument: "TEAM_ID", Usage: teamUsage}
		}
		req.ID = positionals[0]
	case "create":
		if len(positionals) < 1 {
			return req, requiredArgumentError{Command: "team create", Argument: "NAME", Usage: teamUsage}
		}
		req.Name = positionals[0]
		if len(req.Tasks) == 0 && len(positionals) > 1 {
			req.Tasks = append(req.Tasks, team.TaskSpec{Prompt: strings.Join(positionals[1:], " ")})
		}
		if len(req.Tasks) == 0 {
			return req, requiredArgumentError{Command: "team create", Argument: "TASK", Usage: teamUsage}
		}
	default:
		return req, unexpectedExtraArgsError{
			Command: "team",
			Args:    []string{req.Action},
			Usage:   teamUsage,
		}
	}
	return req, nil
}

func parseTeamTaskSpec(value string) team.TaskSpec {
	value = strings.TrimSpace(value)
	if description, prompt, ok := strings.Cut(value, "="); ok && strings.TrimSpace(description) != "" && strings.TrimSpace(prompt) != "" {
		return team.TaskSpec{Description: strings.TrimSpace(description), Prompt: strings.TrimSpace(prompt)}
	}
	return team.TaskSpec{Prompt: value}
}

func refreshTeamStatus(store team.Store, taskStore background.Store, id string) (team.Team, []background.Task, []string, error) {
	item, err := store.Get(id)
	if err != nil {
		return team.Team{}, nil, nil, err
	}
	tasks, missing := loadTeamTasks(taskStore, item.TaskIDs, true)
	nextStatus := aggregateTeamStatus(item.Status, tasks, missing)
	if item.Status != nextStatus {
		item.Status = nextStatus
		item.UpdatedAt = time.Now().UTC()
		if err := store.Save(item); err != nil {
			return team.Team{}, nil, nil, err
		}
	}
	return item, tasks, missing, nil
}

func loadTeamTasks(taskStore background.Store, taskIDs []string, refresh bool) ([]background.Task, []string) {
	tasks := make([]background.Task, 0, len(taskIDs))
	missing := []string{}
	for _, id := range taskIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		var (
			task background.Task
			err  error
		)
		if refresh {
			task, err = taskStore.Status(id)
		} else {
			task, err = taskStore.Get(id)
		}
		if err != nil {
			missing = append(missing, id)
			continue
		}
		tasks = append(tasks, task)
	}
	return tasks, missing
}

func aggregateTeamStatus(current string, tasks []background.Task, missing []string) string {
	if strings.EqualFold(current, "deleted") {
		return "deleted"
	}
	if len(missing) > 0 {
		return "degraded"
	}
	if len(tasks) == 0 {
		return "created"
	}
	running := false
	failed := false
	stopped := false
	completed := false
	for _, task := range tasks {
		switch task.Status {
		case "running":
			running = true
		case "failed", "exited":
			failed = true
		case "stopped":
			stopped = true
		case "completed":
			completed = true
		}
	}
	switch {
	case running:
		return "running"
	case failed:
		return "failed"
	case stopped:
		return "stopped"
	case completed:
		return "completed"
	default:
		return strings.TrimSpace(current)
	}
}

func readTeamLogs(taskStore background.Store, item team.Team, limit int64) []teamTaskLog {
	logs := make([]teamTaskLog, 0, len(item.TaskIDs))
	for _, id := range item.TaskIDs {
		log, err := taskStore.Logs(id, limit)
		entry := teamTaskLog{TaskID: id, Log: log, Bytes: len([]byte(log))}
		if err != nil {
			entry.Error = err.Error()
		}
		logs = append(logs, entry)
	}
	return logs
}

func (a *App) watchTeam(ctx context.Context, taskStore background.Store, item team.Team, req teamRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	offsets := map[string]int64{}
	lastStatus := map[string]string{}
	for _, id := range item.TaskIDs {
		offsets[id] = req.Offset
	}
	events := 0
	emit := func(event teamWatchEvent) error {
		if req.Format == "json" {
			return json.NewEncoder(a.Out).Encode(event)
		}
		switch event.Type {
		case "status":
			fmt.Fprintf(a.Out, "%s status %s\n", event.TaskID, event.Status)
		case "log":
			if event.Data != "" {
				fmt.Fprintf(a.Out, "%s log %s", event.TaskID, event.Data)
				if !strings.HasSuffix(event.Data, "\n") {
					fmt.Fprintln(a.Out)
				}
			}
		case "error":
			fmt.Fprintf(a.Out, "%s error %s\n", event.TaskID, event.Error)
		}
		return nil
	}
	for {
		running := false
		for _, id := range item.TaskIDs {
			task, err := taskStore.Status(id)
			if err != nil {
				if lastStatus[id] != "error" {
					event := teamWatchEvent{Kind: "team_watch", TeamID: item.ID, TeamName: item.Name, Type: "error", TaskID: id, Error: err.Error()}
					if err := emit(event); err != nil {
						return err
					}
					events++
					lastStatus[id] = "error"
				}
				if req.MaxEvents > 0 && events >= req.MaxEvents {
					return nil
				}
				continue
			}
			if background.IsActiveStatus(task.Status) {
				running = true
			}
			if lastStatus[id] == "" || lastStatus[id] != task.Status {
				event := teamWatchEvent{Kind: "team_watch", TeamID: item.ID, TeamName: item.Name, Type: "status", TaskID: id, Status: task.Status, Error: task.Error, Task: &task}
				if err := emit(event); err != nil {
					return err
				}
				events++
				lastStatus[id] = task.Status
				if req.MaxEvents > 0 && events >= req.MaxEvents {
					return nil
				}
			}
			nextOffset, data, err := taskStore.LogFrom(id, offsets[id])
			if err != nil {
				event := teamWatchEvent{Kind: "team_watch", TeamID: item.ID, TeamName: item.Name, Type: "error", TaskID: id, Error: err.Error()}
				if err := emit(event); err != nil {
					return err
				}
				events++
				if req.MaxEvents > 0 && events >= req.MaxEvents {
					return nil
				}
			} else if data != "" {
				offsets[id] = nextOffset
				event := teamWatchEvent{Kind: "team_watch", TeamID: item.ID, TeamName: item.Name, Type: "log", TaskID: id, Offset: nextOffset, Data: data}
				if err := emit(event); err != nil {
					return err
				}
				events++
				if req.MaxEvents > 0 && events >= req.MaxEvents {
					return nil
				}
			}
		}
		if !running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func isTeamAction(value string) bool {
	switch normalizeTeamAction(value) {
	case "list", "get", "status", "logs", "watch", "create", "delete":
		return true
	default:
		return false
	}
}

const teamUsage = "codog team [list|ls|get|show|status|stat|logs|log|watch|tail|follow|create|add|new|delete|remove|rm] [ARGS...] [--json|--output-format text|json]"

var teamActionCandidates = []string{"list", "ls", "get", "show", "status", "stat", "logs", "log", "watch", "tail", "follow", "create", "add", "new", "delete", "remove", "rm"}

func normalizeTeamAction(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "list", "ls":
		return "list"
	case "get", "show":
		return "get"
	case "status", "stat":
		return "status"
	case "logs", "log":
		return "logs"
	case "watch", "tail", "follow":
		return "watch"
	case "create", "add", "new":
		return "create"
	case "delete", "remove", "rm":
		return "delete"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func renderTeamReport(out io.Writer, report teamCommandReport) {
	fmt.Fprintln(out, "Team")
	switch report.Action {
	case "list":
		fmt.Fprintf(out, "  Teams            %d\n", report.Count)
		for _, item := range report.Teams {
			fmt.Fprintf(out, "  %s  %s  %s  tasks=%d\n", item.ID, item.Name, item.Status, len(item.Tasks))
		}
	case "get", "create", "delete", "status":
		if report.Team == nil {
			return
		}
		fmt.Fprintf(out, "  Action           %s\n", report.Action)
		fmt.Fprintf(out, "  ID               %s\n", report.Team.ID)
		fmt.Fprintf(out, "  Name             %s\n", report.Team.Name)
		fmt.Fprintf(out, "  Status           %s\n", report.Team.Status)
		fmt.Fprintf(out, "  Tasks            %d\n", len(report.Team.Tasks))
		if len(report.StoppedTasks) > 0 {
			fmt.Fprintf(out, "  Stopped tasks    %d\n", len(report.StoppedTasks))
		}
		for _, task := range report.Tasks {
			fmt.Fprintf(out, "  Task             %s  %s  pid=%d\n", task.ID, task.Status, task.PID)
		}
		for _, id := range report.MissingTasks {
			fmt.Fprintf(out, "  Missing task     %s\n", id)
		}
		if report.Message != "" {
			fmt.Fprintf(out, "  Message          %s\n", report.Message)
		}
	case "logs":
		if report.Team == nil {
			return
		}
		fmt.Fprintf(out, "  ID               %s\n", report.Team.ID)
		fmt.Fprintf(out, "  Name             %s\n", report.Team.Name)
		for _, log := range report.Logs {
			fmt.Fprintf(out, "\n--- task %s (%d bytes) ---\n", log.TaskID, log.Bytes)
			if log.Error != "" {
				fmt.Fprintf(out, "error: %s\n", log.Error)
				continue
			}
			fmt.Fprint(out, log.Log)
			if log.Log != "" && !strings.HasSuffix(log.Log, "\n") {
				fmt.Fprintln(out)
			}
		}
	}
}

func (a *App) BackgroundWithOverrides(args []string, overrides config.FlagOverrides) error {
	return a.BackgroundWithFormat(args, overrides, "text")
}

func (a *App) BackgroundWithFormat(args []string, overrides config.FlagOverrides, defaultFormat string) error {
	cleanArgs, format, err := parseBackgroundOutputFormat(args, defaultFormat)
	if err != nil {
		return err
	}
	args = cleanArgs
	if len(args) > 0 {
		normalizedAction := normalizeBackgroundAction(args[0])
		if normalizedAction != args[0] {
			args = append([]string{normalizedAction}, args[1:]...)
		}
	}
	store := background.NewStore(a.Config.ConfigHome)
	if len(args) == 0 {
		return a.backgroundListCommand(store, args, overrides, format)
	}
	if len(args) < 2 && backgroundActionRequiresID(args[0]) {
		return errors.New("usage: " + backgroundUsage)
	}
	switch args[0] {
	case "list":
		return a.backgroundListCommand(store, args, overrides, format)
	case "run":
		return a.backgroundRunCommand(store, args, overrides, format)
	case "board":
		return a.backgroundBoardCommand(store, args, overrides, format)
	case "heartbeat":
		return a.backgroundHeartbeatCommand(store, args, overrides, format)
	case "status":
		return a.backgroundStatusCommand(store, args, overrides, format)
	case "stop":
		return a.backgroundStopCommand(store, args, overrides, format)
	case "restart":
		return a.backgroundRestartCommand(store, args, overrides, format)
	case "logs":
		return a.backgroundLogsCommand(store, args, overrides, format)
	case "watch":
		return a.backgroundWatchCommand(store, args, overrides, format)
	case "prune":
		return a.backgroundPruneCommand(store, args, overrides, format)
	case "supervise":
		return a.backgroundSuperviseCommand(store, args, overrides, format)
	default:
		return unexpectedExtraArgsError{Command: "background", Args: []string{args[0]}, Usage: backgroundUsage}
	}
}

func (a *App) backgroundBoardCommand(store background.Store, args []string, overrides config.FlagOverrides, format string) error {
	stalledAfter, err := parseBackgroundBoardArgs(args[1:])
	if err != nil {
		return err
	}
	board, err := store.LaneBoard(stalledAfter)
	if err != nil {
		return err
	}
	if format == "json" {
		return renderBackgroundReport(a.Out, backgroundCommandReport{
			Kind:   "background",
			Action: "board",
			Status: "ok",
			Board:  &board,
		})
	}
	data, _ := json.MarshalIndent(board, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) backgroundHeartbeatCommand(store background.Store, args []string, overrides config.FlagOverrides, format string) error {
	id, heartbeat, err := parseBackgroundHeartbeatArgs(args[1:])
	if err != nil {
		return err
	}
	task, err := store.UpdateHeartbeat(id, heartbeat)
	if err != nil {
		return err
	}
	if format == "json" {
		return renderBackgroundReport(a.Out, backgroundCommandReport{
			Kind:   "background",
			Action: "heartbeat",
			Status: "ok",
			Count:  1,
			TaskID: task.ID,
			Task:   &task,
		})
	}
	data, _ := json.MarshalIndent(task, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) backgroundListCommand(store background.Store, args []string, overrides config.FlagOverrides, format string) error {
	if len(args) > 2 {
		return unexpectedExtraArgsError{Command: "background list", Args: append([]string(nil), args[2:]...), Usage: backgroundUsage}
	}
	tasks, err := store.List()
	if err != nil {
		return err
	}
	sessionID, err := a.sessionIDFromOverrides(overrides)
	if err != nil {
		return err
	}
	if len(args) > 1 {
		sessionID = args[1]
	}
	tasks = background.FilterBySession(tasks, sessionID)
	if format == "json" {
		return renderBackgroundReport(a.Out, backgroundCommandReport{
			Kind:      "background",
			Action:    "list",
			Status:    "ok",
			Count:     len(tasks),
			SessionID: sessionID,
			Tasks:     tasks,
		})
	}
	data, _ := json.MarshalIndent(tasks, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) backgroundLogsCommand(store background.Store, args []string, overrides config.FlagOverrides, format string) error {
	id, limit, err := parseBackgroundLogsArgs(args[1:])
	if err != nil {
		return err
	}
	logs, err := store.Logs(id, limit)
	if err != nil {
		return err
	}
	if format == "json" {
		return renderBackgroundReport(a.Out, backgroundCommandReport{
			Kind:   "background",
			Action: "logs",
			Status: "ok",
			TaskID: id,
			Log:    logs,
			Bytes:  len([]byte(logs)),
		})
	}
	fmt.Fprint(a.Out, logs)
	return nil
}

func (a *App) backgroundPruneCommand(store background.Store, args []string, overrides config.FlagOverrides, format string) error {
	options, err := parseBackgroundPruneArgs(args[1:])
	if err != nil {
		return err
	}
	result, err := store.Prune(options)
	if err != nil {
		return err
	}
	if format == "json" {
		return renderBackgroundReport(a.Out, backgroundCommandReport{
			Kind:    "background",
			Action:  "prune",
			Status:  "ok",
			Count:   result.RemovedCount,
			Prune:   &result,
			Message: "Background task metadata pruned",
		})
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) backgroundRestartCommand(store background.Store, args []string, overrides config.FlagOverrides, format string) error {
	if len(args) > 2 {
		return unexpectedExtraArgsError{Command: "background restart", Args: append([]string(nil), args[2:]...), Usage: backgroundUsage}
	}
	task, err := store.Restart(args[1], a.Workspace)
	if err != nil {
		return err
	}
	if format == "json" {
		if err := renderBackgroundReport(a.Out, backgroundCommandReport{
			Kind:    "background",
			Action:  "restart",
			Status:  "ok",
			Count:   1,
			TaskID:  task.ID,
			Task:    &task,
			Message: "Background task restarted",
		}); err != nil {
			return err
		}
		a.runTaskCreatedHook(context.Background(), task)
		a.runNotificationHook(context.Background(), "background_task_restarted", "Background task restarted", fmt.Sprintf("Background task %s restarted: %s", task.ID, task.Command))
		return nil
	}
	data, _ := json.MarshalIndent(task, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	a.runTaskCreatedHook(context.Background(), task)
	a.runNotificationHook(context.Background(), "background_task_restarted", "Background task restarted", fmt.Sprintf("Background task %s restarted: %s", task.ID, task.Command))
	return nil
}

func (a *App) backgroundRunCommand(store background.Store, args []string, overrides config.FlagOverrides, format string) error {
	command, options, err := parseBackgroundRunArgs(args[1:])
	if err != nil {
		return err
	}
	sessionID, err := a.sessionIDFromOverrides(overrides)
	if err != nil {
		return err
	}
	options.SessionID = sessionID
	task, err := store.RunWithOptions(command, a.Workspace, options)
	if err != nil {
		return err
	}
	if format == "json" {
		if err := renderBackgroundReport(a.Out, backgroundCommandReport{
			Kind:      "background",
			Action:    "run",
			Status:    "ok",
			Count:     1,
			SessionID: task.SessionID,
			TaskID:    task.ID,
			Task:      &task,
			Message:   "Background task started",
		}); err != nil {
			return err
		}
		a.runTaskCreatedHook(context.Background(), task)
		a.runNotificationHook(context.Background(), "background_task_started", "Background task started", fmt.Sprintf("Background task %s started: %s", task.ID, task.Command))
		return nil
	}
	data, _ := json.MarshalIndent(task, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	a.runTaskCreatedHook(context.Background(), task)
	a.runNotificationHook(context.Background(), "background_task_started", "Background task started", fmt.Sprintf("Background task %s started: %s", task.ID, task.Command))
	return nil
}

func (a *App) backgroundStatusCommand(store background.Store, args []string, overrides config.FlagOverrides, format string) error {
	if len(args) > 2 {
		return unexpectedExtraArgsError{Command: "background status", Args: append([]string(nil), args[2:]...), Usage: backgroundUsage}
	}
	task, err := store.Status(args[1])
	if err != nil {
		return err
	}
	if format == "json" {
		return renderBackgroundReport(a.Out, backgroundCommandReport{
			Kind:   "background",
			Action: "status",
			Status: "ok",
			Count:  1,
			TaskID: task.ID,
			Task:   &task,
		})
	}
	data, _ := json.MarshalIndent(task, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) backgroundStopCommand(store background.Store, args []string, overrides config.FlagOverrides, format string) error {
	if len(args) > 2 {
		return unexpectedExtraArgsError{Command: "background stop", Args: append([]string(nil), args[2:]...), Usage: backgroundUsage}
	}
	task, err := store.Stop(args[1])
	if err != nil {
		return err
	}
	if format == "json" {
		if err := renderBackgroundReport(a.Out, backgroundCommandReport{
			Kind:    "background",
			Action:  "stop",
			Status:  "ok",
			Count:   1,
			TaskID:  task.ID,
			Task:    &task,
			Message: "Background task stopped",
		}); err != nil {
			return err
		}
		a.runTaskCompletedHook(context.Background(), task, "manual")
		a.runNotificationHook(context.Background(), "background_task_stopped", "Background task stopped", fmt.Sprintf("Background task %s stopped: %s", task.ID, task.Command))
		if task.Kind == "agent" {
			a.runSubagentStopHook(context.Background(), task.ID, subagentTypeForTask(task), task.LogPath, lastBackgroundLogLine(store, task), false)
		}
		return nil
	}
	data, _ := json.MarshalIndent(task, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	a.runTaskCompletedHook(context.Background(), task, "manual")
	a.runNotificationHook(context.Background(), "background_task_stopped", "Background task stopped", fmt.Sprintf("Background task %s stopped: %s", task.ID, task.Command))
	if task.Kind == "agent" {
		a.runSubagentStopHook(context.Background(), task.ID, subagentTypeForTask(task), task.LogPath, lastBackgroundLogLine(store, task), false)
	}
	return nil
}

func (a *App) backgroundSuperviseCommand(store background.Store, args []string, overrides config.FlagOverrides, format string) error {
	if len(args) > 1 {
		return unexpectedExtraArgsError{Command: "background supervise", Args: append([]string(nil), args[1:]...), Usage: backgroundUsage}
	}
	result, err := store.SuperviseOnce(time.Now().UTC())
	if err != nil {
		return err
	}
	if format == "json" {
		if err := renderBackgroundReport(a.Out, backgroundCommandReport{
			Kind:      "background",
			Action:    "supervise",
			Status:    "ok",
			Count:     len(result.Restarted),
			Supervise: &result,
		}); err != nil {
			return err
		}
		for _, task := range result.Restarted {
			a.runTaskCreatedHook(context.Background(), task)
			a.runNotificationHook(context.Background(), "background_task_restarted", "Background task restarted", fmt.Sprintf("Background task %s restarted: %s", task.ID, task.Command))
		}
		return nil
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	for _, task := range result.Restarted {
		a.runTaskCreatedHook(context.Background(), task)
		a.runNotificationHook(context.Background(), "background_task_restarted", "Background task restarted", fmt.Sprintf("Background task %s restarted: %s", task.ID, task.Command))
	}
	return nil
}

func (a *App) backgroundWatchCommand(store background.Store, args []string, overrides config.FlagOverrides, format string) error {
	id, offset, maxEvents, err := parseBackgroundWatchArgs(args[1:])
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(a.Out)
	return store.Watch(context.Background(), id, background.WatchOptions{Offset: offset, MaxEvents: maxEvents}, func(event background.WatchEvent) error {
		return encoder.Encode(event)
	})
}

func backgroundActionRequiresID(action string) bool {
	switch normalizeBackgroundAction(action) {
	case "heartbeat", "status", "stop", "restart", "logs", "watch":
		return true
	default:
		return false
	}
}

func normalizeBackgroundAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "list", "ls":
		return "list"
	case "run", "start", "new":
		return "run"
	case "board", "lane-board", "lanes":
		return "board"
	case "heartbeat", "beat":
		return "heartbeat"
	case "status", "stat", "get", "show":
		return "status"
	case "stop", "kill", "cancel":
		return "stop"
	case "restart", "rerun":
		return "restart"
	case "logs", "log", "output":
		return "logs"
	case "watch", "tail", "follow":
		return "watch"
	case "prune", "gc":
		return "prune"
	case "supervise", "supervisor":
		return "supervise"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

func renderBackgroundReport(out io.Writer, report backgroundCommandReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(out, string(data))
	return nil
}

func parseBackgroundOutputFormat(args []string, defaultFormat string) ([]string, string, error) {
	format := strings.TrimSpace(defaultFormat)
	if format == "" {
		format = "text"
	}
	remaining := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			remaining = append(remaining, args[index:]...)
			break
		}
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return nil, "", missingFlagValueError{Command: "background", Flag: arg, Usage: backgroundUsage}
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		default:
			remaining = append(remaining, arg)
		}
	}
	normalized, err := normalizeOutputFormat("background", format, []string{"text", "json"})
	if err != nil {
		return nil, "", err
	}
	return remaining, normalized, nil
}

func parseBackgroundRunArgs(args []string) (string, background.RunOptions, error) {
	var options background.RunOptions
	var policy *background.RestartPolicy
	for len(args) > 0 {
		arg := args[0]
		if arg == "--" {
			args = args[1:]
			break
		}
		if arg == "--restart" {
			policy = ensureRestartPolicy(policy)
			args = args[1:]
			continue
		}
		if strings.HasPrefix(arg, "--restart=") {
			policy = ensureRestartPolicy(policy)
			mode := strings.TrimPrefix(arg, "--restart=")
			if mode != "on-failure" && mode != "always" {
				return "", options, errors.New("restart mode must be on-failure or always")
			}
			policy.Mode = mode
			args = args[1:]
			continue
		}
		if arg == "--restart-limit" {
			if len(args) < 2 {
				return "", options, errors.New("missing value for --restart-limit")
			}
			limit, err := strconv.Atoi(args[1])
			if err != nil {
				return "", options, err
			}
			if limit < 0 {
				return "", options, errors.New("restart limit must be non-negative")
			}
			policy = ensureRestartPolicy(policy)
			policy.MaxAttempts = limit
			args = args[2:]
			continue
		}
		if arg == "--restart-delay" {
			if len(args) < 2 {
				return "", options, errors.New("missing value for --restart-delay")
			}
			delay, err := strconv.Atoi(args[1])
			if err != nil {
				return "", options, err
			}
			if delay < 0 {
				return "", options, errors.New("restart delay must be non-negative")
			}
			policy = ensureRestartPolicy(policy)
			policy.DelaySeconds = delay
			args = args[2:]
			continue
		}
		if arg == "--owner" {
			if len(args) < 2 {
				return "", options, errors.New("missing value for --owner")
			}
			options.ScopeBinding.Owner = args[1]
			args = args[2:]
			continue
		}
		if strings.HasPrefix(arg, "--owner=") {
			options.ScopeBinding.Owner = strings.TrimPrefix(arg, "--owner=")
			args = args[1:]
			continue
		}
		if arg == "--workflow-scope" || arg == "--scope" {
			if len(args) < 2 {
				return "", options, fmt.Errorf("missing value for %s", arg)
			}
			options.ScopeBinding.WorkflowScope = args[1]
			args = args[2:]
			continue
		}
		if strings.HasPrefix(arg, "--workflow-scope=") {
			options.ScopeBinding.WorkflowScope = strings.TrimPrefix(arg, "--workflow-scope=")
			args = args[1:]
			continue
		}
		if strings.HasPrefix(arg, "--scope=") {
			options.ScopeBinding.WorkflowScope = strings.TrimPrefix(arg, "--scope=")
			args = args[1:]
			continue
		}
		if arg == "--watcher-action" {
			if len(args) < 2 {
				return "", options, errors.New("missing value for --watcher-action")
			}
			options.ScopeBinding.WatcherAction = args[1]
			args = args[2:]
			continue
		}
		if strings.HasPrefix(arg, "--watcher-action=") {
			options.ScopeBinding.WatcherAction = strings.TrimPrefix(arg, "--watcher-action=")
			args = args[1:]
			continue
		}
		break
	}
	command := strings.Join(args, " ")
	if strings.TrimSpace(command) == "" {
		return "", options, errors.New("background command is required")
	}
	options.RestartPolicy = policy
	return command, options, nil
}

func ensureRestartPolicy(policy *background.RestartPolicy) *background.RestartPolicy {
	if policy != nil {
		return policy
	}
	return &background.RestartPolicy{Enabled: true, Mode: "on-failure"}
}

func parseBackgroundBoardArgs(args []string) (time.Duration, error) {
	if len(args) == 0 {
		return 30 * time.Second, nil
	}
	if len(args) > 1 {
		return 0, errors.New("usage: codog background board [stalled-after-seconds]")
	}
	value := strings.TrimSpace(args[0])
	value = strings.TrimPrefix(value, "--stalled-after-seconds=")
	value = strings.TrimPrefix(value, "--stalled-after-secs=")
	seconds, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	if seconds <= 0 {
		return 0, errors.New("stalled-after-seconds must be positive")
	}
	return time.Duration(seconds) * time.Second, nil
}

func parseBackgroundHeartbeatArgs(args []string) (string, background.LaneHeartbeat, error) {
	if len(args) == 0 {
		return "", background.LaneHeartbeat{}, errors.New("usage: codog background heartbeat ID [--status STATUS] [--transport-alive true|false] [--observed-at RFC3339] [--source-kind KIND] [--environment ENV] [--channel CHANNEL] [--emitter EMITTER] [--confidence LEVEL]")
	}
	id := strings.TrimSpace(args[0])
	if id == "" {
		return "", background.LaneHeartbeat{}, errors.New("task id is required")
	}
	heartbeat := background.LaneHeartbeat{TransportAlive: true}
	args = args[1:]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--status":
			i++
			if i >= len(args) {
				return "", heartbeat, errors.New("missing value for --status")
			}
			heartbeat.Status = args[i]
		case strings.HasPrefix(arg, "--status="):
			heartbeat.Status = strings.TrimPrefix(arg, "--status=")
		case arg == "--transport-alive":
			i++
			if i >= len(args) {
				return "", heartbeat, errors.New("missing value for --transport-alive")
			}
			parsed, err := strconv.ParseBool(args[i])
			if err != nil {
				return "", heartbeat, err
			}
			heartbeat.TransportAlive = parsed
		case strings.HasPrefix(arg, "--transport-alive="):
			parsed, err := strconv.ParseBool(strings.TrimPrefix(arg, "--transport-alive="))
			if err != nil {
				return "", heartbeat, err
			}
			heartbeat.TransportAlive = parsed
		case arg == "--dead":
			heartbeat.TransportAlive = false
		case arg == "--observed-at":
			i++
			if i >= len(args) {
				return "", heartbeat, errors.New("missing value for --observed-at")
			}
			observedAt, err := time.Parse(time.RFC3339, args[i])
			if err != nil {
				return "", heartbeat, err
			}
			heartbeat.ObservedAt = observedAt
		case strings.HasPrefix(arg, "--observed-at="):
			observedAt, err := time.Parse(time.RFC3339, strings.TrimPrefix(arg, "--observed-at="))
			if err != nil {
				return "", heartbeat, err
			}
			heartbeat.ObservedAt = observedAt
		case arg == "--source-kind":
			i++
			if i >= len(args) {
				return "", heartbeat, errors.New("missing value for --source-kind")
			}
			heartbeat.Provenance.SourceKind = args[i]
		case strings.HasPrefix(arg, "--source-kind="):
			heartbeat.Provenance.SourceKind = strings.TrimPrefix(arg, "--source-kind=")
		case arg == "--environment":
			i++
			if i >= len(args) {
				return "", heartbeat, errors.New("missing value for --environment")
			}
			heartbeat.Provenance.Environment = args[i]
		case strings.HasPrefix(arg, "--environment="):
			heartbeat.Provenance.Environment = strings.TrimPrefix(arg, "--environment=")
		case arg == "--channel":
			i++
			if i >= len(args) {
				return "", heartbeat, errors.New("missing value for --channel")
			}
			heartbeat.Provenance.Channel = args[i]
		case strings.HasPrefix(arg, "--channel="):
			heartbeat.Provenance.Channel = strings.TrimPrefix(arg, "--channel=")
		case arg == "--emitter":
			i++
			if i >= len(args) {
				return "", heartbeat, errors.New("missing value for --emitter")
			}
			heartbeat.Provenance.Emitter = args[i]
		case strings.HasPrefix(arg, "--emitter="):
			heartbeat.Provenance.Emitter = strings.TrimPrefix(arg, "--emitter=")
		case arg == "--confidence":
			i++
			if i >= len(args) {
				return "", heartbeat, errors.New("missing value for --confidence")
			}
			heartbeat.Provenance.Confidence = args[i]
		case strings.HasPrefix(arg, "--confidence="):
			heartbeat.Provenance.Confidence = strings.TrimPrefix(arg, "--confidence=")
		default:
			return "", heartbeat, fmt.Errorf("unknown heartbeat argument %q", arg)
		}
	}
	return id, heartbeat, nil
}
