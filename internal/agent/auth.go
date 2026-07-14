package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/hooks"
	"github.com/Rememorio/codog/internal/memory"
	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/projectinit"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/toolnames"
	"github.com/Rememorio/codog/internal/verifiers"
)

func parseSetupTokenArgs(args []string) (setupTokenRequest, error) {
	req := setupTokenRequest{Target: "user", Format: "text"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "setup-token", Flag: arg, Usage: setupTokenUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "setup-token", Flag: arg, Usage: setupTokenUsage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "setup-token", Flag: arg, Usage: setupTokenUsage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--token":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "setup-token", Flag: arg, Usage: setupTokenUsage}
			}
			req.Token = strings.TrimSpace(args[index])
		case strings.HasPrefix(arg, "--token="):
			req.Token = strings.TrimSpace(strings.TrimPrefix(arg, "--token="))
		case arg == "--stdin":
			req.Stdin = true
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "setup-token", Option: arg, Usage: setupTokenUsage}
			}
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("setup-token", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	switch len(rest) {
	case 0:
	case 1:
		if req.Token != "" {
			return req, unexpectedExtraArgsError{Command: "setup-token", Args: rest, Usage: setupTokenUsage}
		}
		req.Token = strings.TrimSpace(rest[0])
	default:
		return req, unexpectedExtraArgsError{Command: "setup-token", Args: rest[1:], Usage: setupTokenUsage}
	}
	if req.Token != "" && req.Stdin {
		return req, unexpectedExtraArgsError{Command: "setup-token", Args: []string{"--stdin"}, Usage: setupTokenUsage}
	}
	return req, nil
}

func (a *App) setupTokenValue(req setupTokenRequest) (string, bool, error) {
	if token := strings.TrimSpace(req.Token); token != "" {
		return token, false, nil
	}
	in := a.In
	if in == nil {
		in = os.Stdin
	}
	if req.Stdin {
		data, err := io.ReadAll(in)
		if err != nil {
			return "", false, err
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", false, requiredArgumentError{Command: "setup-token", Argument: "TOKEN", Usage: setupTokenUsage}
		}
		return token, false, nil
	}
	if a.Err != nil {
		fmt.Fprint(a.Err, "Long-lived authentication token: ")
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", true, err
	}
	token := strings.TrimSpace(line)
	if token == "" {
		return "", true, requiredArgumentError{Command: "setup-token", Argument: "TOKEN", Usage: setupTokenUsage}
	}
	return token, true, nil
}

func renderSetupTokenReport(out io.Writer, report setupTokenReport) {
	fmt.Fprintln(out, "Setup Token")
	fmt.Fprintf(out, "  Configured       %t\n", report.Configured)
	if report.RedactedValue != "" {
		fmt.Fprintf(out, "  Value            %s\n", report.RedactedValue)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if len(report.EnvVars) > 0 {
		fmt.Fprintf(out, "  Env vars         %s\n", strings.Join(report.EnvVars, ", "))
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func (a *App) Login(args []string) error {
	flow, rest, err := parseLoginArgs(args)
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(a.Config.ForceLoginMethod)) {
	case "claudeai":
		flow = "browser"
	case "console":
		flow = "device"
	}
	return a.OAuth(append([]string{flow, "login"}, rest...))
}

func parseLoginArgs(args []string) (string, []string, error) {
	flow := "browser"
	rest := []string{}
	claudeAI := false
	console := false
	for index, arg := range args {
		switch strings.ToLower(arg) {
		case "browser":
			if index == 0 {
				flow = "browser"
				continue
			}
		case "device":
			if index == 0 {
				flow = "device"
				continue
			}
		case "--claudeai":
			claudeAI = true
			continue
		case "--console":
			console = true
			continue
		}
		rest = append(rest, arg)
	}
	if claudeAI && console {
		return "", nil, errors.New("login --console and --claudeai cannot be used together")
	}
	if claudeAI {
		flow = "browser"
	}
	if console {
		flow = "device"
	}
	return flow, rest, nil
}

func (a *App) Logout(args []string) error {
	return a.OAuth(append([]string{"logout"}, args...))
}

func (a *App) OAuthRefresh(args []string) error {
	profile, err := parseOAuthRefreshArgs(args)
	if err != nil {
		return err
	}
	token, err := oauth.RefreshStoredToken(context.Background(), a.Config.ConfigHome, profile)
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(token.View(time.Now().UTC()), "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func parseOAuthRefreshArgs(args []string) (string, error) {
	profile := ""
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
		case arg == "--output-format":
			index++
			if index >= len(args) {
				return "", errors.New("oauth-refresh output format is required")
			}
			if args[index] != "json" {
				return "", fmt.Errorf("unknown oauth-refresh output format %q", args[index])
			}
		case strings.HasPrefix(arg, "--output-format="):
			format := strings.TrimPrefix(arg, "--output-format=")
			if format != "json" {
				return "", fmt.Errorf("unknown oauth-refresh output format %q", format)
			}
		case strings.HasPrefix(arg, "-"):
			return "", fmt.Errorf("unknown oauth-refresh flag %q", arg)
		default:
			if profile != "" {
				return "", fmt.Errorf("unexpected oauth-refresh argument %q", arg)
			}
			profile = arg
		}
	}
	return profile, nil
}

const (
	oauthUsage         = "codog oauth pkce | oauth discover ISSUER_URL | oauth provider save|list|show|delete | oauth device start|poll|login | oauth browser start|exchange|login | oauth status [PROFILE] | oauth logout [PROFILE] | oauth token save|show|refresh|revoke|delete"
	oauthProviderUsage = "codog oauth provider save|list|show|delete"
	oauthTokenUsage    = "codog oauth token save|show|status|refresh|revoke|delete"
	oauthDeviceUsage   = "codog oauth device start|poll|login|status"
	oauthBrowserUsage  = "codog oauth browser start|exchange|login|status"
)

type oauthTokenDeleteReport struct {
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Deleted bool   `json:"deleted"`
}

type oauthTokenRevokeReport struct {
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Revoked bool   `json:"revoked"`
	Profile string `json:"profile"`
	Token   string `json:"token"`
}

type oauthProviderDeleteReport struct {
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Deleted bool   `json:"deleted"`
	Name    string `json:"name"`
}

// OAuth runs local OAuth helper commands for provider profiles and tokens.
func (a *App) OAuth(args []string) error {
	var err error
	args, err = normalizeOAuthJSONArgs(args)
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "pkce" {
		pkce, err := oauth.GeneratePKCE()
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(pkce, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if args[0] == "discover" {
		if len(args) < 2 {
			return renderMissingActionArgument(a.Out, "oauth", "discover", "issuer_url", "oauth discover requires an issuer URL", "Usage: codog oauth discover ISSUER_URL [--json|--output-format json].", "json")
		}
		metadata, err := oauth.DiscoverProvider(context.Background(), args[1])
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(metadata, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if args[0] == "device" {
		return a.oauthDevice(args[1:])
	}
	if args[0] == "browser" {
		return a.oauthBrowser(args[1:])
	}
	if args[0] == "provider" {
		return a.oauthProvider(args[1:])
	}
	if args[0] == "status" {
		profile := ""
		if len(args) > 1 {
			profile = args[1]
		}
		status := oauth.InspectStatus(a.Config.ConfigHome, profile, time.Now().UTC())
		data, _ := json.MarshalIndent(status, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if args[0] == "logout" {
		profile := ""
		if len(args) > 1 {
			profile = args[1]
		}
		result, err := oauth.Logout(context.Background(), a.Config.ConfigHome, profile)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if args[0] != "token" {
		return unexpectedExtraArgsError{
			Command: "oauth",
			Args:    []string{args[0]},
			Usage:   oauthUsage,
		}
	}
	if len(args) < 2 {
		status := oauth.InspectStatus(a.Config.ConfigHome, "", time.Now().UTC())
		data, _ := json.MarshalIndent(status, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	switch args[1] {
	case "save":
		if len(args) < 3 {
			return renderMissingActionArgument(a.Out, "oauth", "token_save", "access_token", "oauth token save requires an access token", "Usage: codog oauth token save ACCESS_TOKEN [REFRESH_TOKEN] [EXPIRES_AT].", "json")
		}
		token := oauth.Token{AccessToken: args[2]}
		if len(args) > 3 {
			token.RefreshToken = args[3]
		}
		if len(args) > 4 {
			expiresAt, err := time.Parse(time.RFC3339, args[4])
			if err != nil {
				return err
			}
			token.ExpiresAt = expiresAt
		}
		saved, err := oauth.SaveToken(a.Config.ConfigHome, token)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(saved.View(time.Now().UTC()), "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "show":
		token, err := oauth.LoadToken(a.Config.ConfigHome)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(token.View(time.Now().UTC()), "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "status":
		profile := ""
		if len(args) > 2 {
			profile = args[2]
		}
		status := oauth.InspectStatus(a.Config.ConfigHome, profile, time.Now().UTC())
		data, _ := json.MarshalIndent(status, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "refresh":
		profile := ""
		if len(args) > 2 {
			profile = args[2]
		}
		token, err := oauth.RefreshStoredToken(context.Background(), a.Config.ConfigHome, profile)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(token.View(time.Now().UTC()), "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "revoke":
		result, err := a.oauthTokenRevoke(args[2:])
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "delete":
		if err := oauth.DeleteToken(a.Config.ConfigHome); err != nil {
			return err
		}
		report := oauthTokenDeleteReport{Kind: "oauth_token", Action: "delete", Status: "ok", Deleted: true}
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	default:
		return unexpectedExtraArgsError{
			Command: "oauth token",
			Args:    []string{args[1]},
			Usage:   oauthTokenUsage,
		}
	}
}

func normalizeOAuthJSONArgs(args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			continue
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return nil, missingFlagValueError{
					Command: "oauth",
					Flag:    arg,
					Usage:   oauthUsage,
				}
			}
			if !strings.EqualFold(strings.TrimSpace(args[index]), "json") {
				return nil, outputFormatError{
					Command:  "oauth",
					Value:    args[index],
					Expected: []string{"json"},
				}
			}
		case strings.HasPrefix(arg, "--output-format="):
			format := strings.TrimPrefix(arg, "--output-format=")
			if !strings.EqualFold(strings.TrimSpace(format), "json") {
				return nil, outputFormatError{
					Command:  "oauth",
					Value:    format,
					Expected: []string{"json"},
				}
			}
		default:
			out = append(out, arg)
		}
	}
	return out, nil
}

func (a *App) oauthTokenRevoke(args []string) (oauthTokenRevokeReport, error) {
	profileName := ""
	tokenKind := "access"
	if len(args) > 0 {
		profileName = args[0]
	}
	if len(args) > 1 {
		tokenKind = args[1]
	}
	profile, err := oauth.ResolveProviderProfile(a.Config.ConfigHome, profileName)
	if err != nil {
		return oauthTokenRevokeReport{}, err
	}
	token, err := oauth.LoadToken(a.Config.ConfigHome)
	if err != nil {
		return oauthTokenRevokeReport{}, err
	}
	tokenValue := token.AccessToken
	hint := "access_token"
	if tokenKind == "refresh" {
		tokenValue = token.RefreshToken
		hint = "refresh_token"
	} else if tokenKind != "access" {
		return oauthTokenRevokeReport{}, errors.New("token kind must be access or refresh")
	}
	if err := oauth.RevokeToken(context.Background(), profile.Metadata, profile.ClientID, tokenValue, hint); err != nil {
		return oauthTokenRevokeReport{}, err
	}
	return oauthTokenRevokeReport{Kind: "oauth_token", Action: "revoke", Status: "ok", Revoked: true, Profile: profile.Name, Token: tokenKind}, nil
}

func (a *App) oauthProvider(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	var payload any
	switch args[0] {
	case "save":
		if len(args) < 4 {
			return renderMissingActionArgument(a.Out, "oauth", "provider_save", "profile", "oauth provider save requires NAME, ISSUER_URL, and CLIENT_ID", "Usage: codog oauth provider save NAME ISSUER_URL CLIENT_ID [SCOPE...].", "json")
		}
		profile, err := oauth.SaveProviderProfile(context.Background(), a.Config.ConfigHome, args[1], args[2], args[3], args[4:])
		if err != nil {
			return err
		}
		payload = profile
	case "list":
		profiles, err := oauth.ListProviderProfiles(a.Config.ConfigHome)
		if err != nil {
			return err
		}
		payload = profiles
	case "show":
		if len(args) < 2 {
			return renderMissingActionArgument(a.Out, "oauth", "provider_show", "profile", "oauth provider show requires a profile name", "Usage: codog oauth provider show NAME.", "json")
		}
		profile, err := oauth.LoadProviderProfile(a.Config.ConfigHome, args[1])
		if err != nil {
			return err
		}
		payload = profile
	case "delete":
		if len(args) < 2 {
			return renderMissingActionArgument(a.Out, "oauth", "provider_delete", "profile", "oauth provider delete requires a profile name", "Usage: codog oauth provider delete NAME.", "json")
		}
		if err := oauth.DeleteProviderProfile(a.Config.ConfigHome, args[1]); err != nil {
			return err
		}
		payload = oauthProviderDeleteReport{Kind: "oauth_provider", Action: "delete", Status: "ok", Deleted: true, Name: args[1]}
	default:
		return unexpectedExtraArgsError{
			Command: "oauth provider",
			Args:    []string{args[0]},
			Usage:   oauthProviderUsage,
		}
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

type profileRequest struct {
	Action string
	Name   string
	Format string
	Target string
	Path   string
}

type profileReport struct {
	Kind             string                 `json:"kind"`
	Action           string                 `json:"action"`
	Status           string                 `json:"status"`
	ActiveProfile    string                 `json:"active_profile,omitempty"`
	ActiveConfigured bool                   `json:"active_configured"`
	ResolvedProfile  string                 `json:"resolved_profile,omitempty"`
	ResolvedSource   string                 `json:"resolved_source,omitempty"`
	ProfileCount     int                    `json:"profile_count"`
	Profile          *oauthProviderSummary  `json:"profile,omitempty"`
	Profiles         []oauthProviderSummary `json:"profiles,omitempty"`
	OAuthStatus      *oauth.Status          `json:"oauth_status,omitempty"`
	Path             string                 `json:"path,omitempty"`
	Message          string                 `json:"message,omitempty"`
}

func (a *App) Profile(args []string) error {
	req, err := parseProfileArgs(args)
	if err != nil {
		return err
	}
	var selected *oauth.ProviderProfile
	selectedSource := ""
	path := ""
	switch req.Action {
	case "list":
	case "show":
		if strings.TrimSpace(req.Name) != "" {
			profile, err := oauth.LoadProviderProfile(a.Config.ConfigHome, req.Name)
			if err != nil {
				return err
			}
			selected = &profile
			selectedSource = "requested"
		} else if strings.TrimSpace(a.Config.OAuthProfile) != "" {
			profile, err := oauth.LoadProviderProfile(a.Config.ConfigHome, a.Config.OAuthProfile)
			if err != nil {
				return err
			}
			selected = &profile
			selectedSource = "active"
		} else if profile, err := oauth.ResolveProviderProfile(a.Config.ConfigHome, ""); err == nil {
			selected = &profile
			selectedSource = "default_resolution"
		}
	case "set":
		if strings.TrimSpace(req.Name) == "" {
			return requiredArgumentError{Command: "profile set", Argument: "NAME", Usage: profileUsage}
		}
		profile, err := oauth.LoadProviderProfile(a.Config.ConfigHome, req.Name)
		if err != nil {
			return err
		}
		selected = &profile
		selectedSource = "active"
		path, err = a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "oauth_profile", profile.Name); err != nil {
			return err
		}
		a.Config.OAuthProfile = profile.Name
	case "clear":
		path, err = a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "oauth_profile"); err != nil {
			return err
		}
		a.Config.OAuthProfile = ""
	default:
		return fmt.Errorf("unknown profile action %q", req.Action)
	}
	report, err := a.buildProfileReport(req.Action, path, selected, selectedSource)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderProfileReport(a.Out, report)
	return nil
}

const profileUsage = "codog profile [show|list|set|clear] [NAME] [--target user|project|local] [--path PATH] [--output-format text|json]"

func parseProfileArgs(args []string) (profileRequest, error) {
	req := profileRequest{Action: "show", Format: "text", Target: "user"}
	rest := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "profile", Flag: arg, Usage: profileUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "profile", Flag: arg, Usage: profileUsage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "profile", Flag: arg, Usage: profileUsage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "profile", Option: arg, Usage: profileUsage}
		default:
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("profile", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(strings.TrimSpace(rest[0])) {
	case "status", "show", "current":
		req.Action = "show"
		if len(rest) > 1 {
			req.Name = rest[1]
		}
		if len(rest) > 2 {
			return req, unexpectedExtraArgsError{Command: "profile " + strings.ToLower(rest[0]), Args: rest[2:], Usage: profileUsage}
		}
	case "list":
		req.Action = "list"
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "profile list", Args: rest[1:], Usage: profileUsage}
		}
	case "set", "switch", "use":
		req.Action = "set"
		if len(rest) < 2 {
			return req, requiredArgumentError{Command: "profile set", Argument: "NAME", Usage: profileUsage}
		}
		req.Name = rest[1]
		if len(rest) > 2 {
			return req, unexpectedExtraArgsError{Command: "profile " + strings.ToLower(rest[0]), Args: rest[2:], Usage: profileUsage}
		}
	case "clear", "reset", "unset":
		req.Action = "clear"
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "profile " + strings.ToLower(rest[0]), Args: rest[1:], Usage: profileUsage}
		}
	default:
		req.Action = "set"
		req.Name = rest[0]
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "profile", Args: rest[1:], Usage: profileUsage}
		}
	}
	return req, nil
}

func (a *App) buildProfileReport(action string, path string, selected *oauth.ProviderProfile, selectedSource string) (profileReport, error) {
	profiles, err := oauth.ListProviderProfiles(a.Config.ConfigHome)
	if err != nil {
		return profileReport{}, err
	}
	summaries := make([]oauthProviderSummary, 0, len(profiles))
	for _, profile := range profiles {
		summaries = append(summaries, oauthProfileSummary(profile))
	}
	var summary *oauthProviderSummary
	var status *oauth.Status
	if selected != nil {
		value := oauthProfileSummary(*selected)
		summary = &value
		inspected := oauth.InspectStatus(a.Config.ConfigHome, selected.Name, time.Now().UTC())
		status = &inspected
	}
	report := profileReport{
		Kind:             "profile",
		Action:           action,
		Status:           "ok",
		ActiveProfile:    a.Config.OAuthProfile,
		ActiveConfigured: strings.TrimSpace(a.Config.OAuthProfile) != "",
		ProfileCount:     len(summaries),
		Profile:          summary,
		Profiles:         summaries,
		OAuthStatus:      status,
		Path:             path,
	}
	if summary != nil {
		report.ResolvedProfile = summary.Name
		report.ResolvedSource = selectedSource
	}
	switch action {
	case "set":
		report.Message = "Active OAuth profile saved."
	case "clear":
		report.Message = "Active OAuth profile cleared; default profile resolution will be used."
	case "list":
		report.Message = "Configured OAuth provider profiles."
	case "show":
		if summary == nil {
			report.Message = "No active OAuth profile is selected."
		} else {
			report.Message = "Active or resolved OAuth profile."
		}
	}
	return report, nil
}

func oauthProfileSummary(profile oauth.ProviderProfile) oauthProviderSummary {
	return oauthProviderSummary{
		Name:     profile.Name,
		Issuer:   profile.Issuer,
		ClientID: profile.ClientID,
		Scopes:   append([]string(nil), profile.Scopes...),
	}
}

func renderProfileReport(out io.Writer, report profileReport) {
	fmt.Fprintln(out, "Profile")
	if report.ActiveProfile != "" {
		fmt.Fprintf(out, "  Active profile   %s\n", report.ActiveProfile)
	} else {
		fmt.Fprintln(out, "  Active profile   unset")
	}
	fmt.Fprintf(out, "  Active set       %t\n", report.ActiveConfigured)
	if report.Profile != nil {
		fmt.Fprintf(out, "  Resolved profile %s\n", report.Profile.Name)
		if report.ResolvedSource != "" {
			fmt.Fprintf(out, "  Resolved source  %s\n", report.ResolvedSource)
		}
		if report.Profile.Issuer != "" {
			fmt.Fprintf(out, "  Issuer           %s\n", report.Profile.Issuer)
		}
		if report.Profile.ClientID != "" {
			fmt.Fprintf(out, "  Client ID        %s\n", report.Profile.ClientID)
		}
	}
	if len(report.Profiles) != 0 {
		fmt.Fprintf(out, "  Profile count    %d\n", report.ProfileCount)
		fmt.Fprintln(out, "  Profiles")
		for _, profile := range report.Profiles {
			fmt.Fprintf(out, "    %s", profile.Name)
			if profile.Issuer != "" {
				fmt.Fprintf(out, "  %s", profile.Issuer)
			}
			fmt.Fprintln(out)
		}
	}
	if report.OAuthStatus != nil {
		fmt.Fprintf(out, "  Token ready      %t\n", report.OAuthStatus.Ready)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

type providerPreset struct {
	Name                                  string   `json:"name"`
	Protocol                              string   `json:"protocol"`
	BaseURL                               string   `json:"base_url,omitempty"`
	DefaultModel                          string   `json:"default_model,omitempty"`
	AuthEnv                               []string `json:"auth_env,omitempty"`
	OpenAICompatible                      bool     `json:"openai_compatible"`
	ReasoningModel                        bool     `json:"reasoning_model"`
	PreservesReasoningContentInHistory    bool     `json:"preserves_reasoning_content_in_history"`
	StripsTuningParams                    bool     `json:"strips_tuning_params"`
	SupportsStreamUsage                   bool     `json:"supports_stream_usage"`
	HonorsProxyEnv                        bool     `json:"honors_proxy_env"`
	SupportsExtraBodyParams               bool     `json:"supports_extra_body_params"`
	PreservesSlashModelIDsOnCustomBaseURL bool     `json:"preserves_slash_model_ids_on_custom_base_url,omitempty"`
	ProtectedExtraBodyKeys                []string `json:"protected_extra_body_keys,omitempty"`
	Description                           string   `json:"description,omitempty"`
}

type providerAuthReport struct {
	Configured     bool     `json:"configured"`
	Sources        []string `json:"sources"`
	APIKey         bool     `json:"api_key"`
	AuthToken      bool     `json:"auth_token"`
	StoredOAuth    bool     `json:"stored_oauth"`
	PreferredToken string   `json:"preferred_token,omitempty"`
}

type providerDiagnosticReport struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Action   string `json:"action,omitempty"`
}

type activeProviderReport struct {
	Name                                  string                     `json:"name"`
	Protocol                              string                     `json:"protocol"`
	BaseURL                               string                     `json:"base_url"`
	Model                                 string                     `json:"model"`
	MaxTokens                             int                        `json:"max_tokens"`
	MaxTurns                              int                        `json:"max_turns"`
	OpenAICompatible                      bool                       `json:"openai_compatible"`
	ReasoningModel                        bool                       `json:"reasoning_model"`
	PreservesReasoningContentInHistory    bool                       `json:"preserves_reasoning_content_in_history"`
	StripsTuningParams                    bool                       `json:"strips_tuning_params"`
	SupportsStreamUsage                   bool                       `json:"supports_stream_usage"`
	HonorsProxyEnv                        bool                       `json:"honors_proxy_env"`
	SupportsExtraBodyParams               bool                       `json:"supports_extra_body_params"`
	ExtraBodyConfigured                   bool                       `json:"extra_body_configured"`
	ExtraBodyKeys                         []string                   `json:"extra_body_keys,omitempty"`
	ExtraBodyForwardedKeys                []string                   `json:"extra_body_forwarded_keys,omitempty"`
	ExtraBodyIgnoredKeys                  []string                   `json:"extra_body_ignored_keys,omitempty"`
	PreservesSlashModelIDsOnCustomBaseURL bool                       `json:"preserves_slash_model_ids_on_custom_base_url,omitempty"`
	ProtectedExtraBodyKeys                []string                   `json:"protected_extra_body_keys,omitempty"`
	Diagnostics                           []providerDiagnosticReport `json:"diagnostics,omitempty"`
	Auth                                  providerAuthReport         `json:"auth"`
	ConfigLoadError                       *string                    `json:"config_load_error,omitempty"`
	ConfigLoadErrorKind                   string                     `json:"config_load_error_kind,omitempty"`
}

type oauthProviderSummary struct {
	Name     string   `json:"name"`
	Issuer   string   `json:"issuer"`
	ClientID string   `json:"client_id"`
	Scopes   []string `json:"scopes,omitempty"`
}

type providersReport struct {
	Kind                string                 `json:"kind"`
	Action              string                 `json:"action"`
	Status              string                 `json:"status"`
	Active              activeProviderReport   `json:"active"`
	Presets             []providerPreset       `json:"presets,omitempty"`
	OAuthProfiles       []oauthProviderSummary `json:"oauth_profiles,omitempty"`
	ConfigLoadError     *string                `json:"config_load_error"`
	ConfigLoadErrorKind string                 `json:"config_load_error_kind,omitempty"`
}

type providerSetReport struct {
	Kind     string                  `json:"kind"`
	Action   string                  `json:"action"`
	Status   string                  `json:"status"`
	Provider string                  `json:"provider"`
	BaseURL  string                  `json:"base_url,omitempty"`
	Model    string                  `json:"model,omitempty"`
	Target   string                  `json:"target,omitempty"`
	Path     string                  `json:"path,omitempty"`
	Changes  []config.MutationReport `json:"changes"`
}

type providerCommandRequest struct {
	Action  string
	Format  string
	Name    string
	BaseURL string
	Model   string
	Path    string
	Target  string
}

func (a *App) ConfigCommand(args []string) error {
	paths := []string{
		filepath.Join(a.Config.ConfigHome, "config.json"),
		".codog.json",
		".codog.local.json",
	}
	return renderConfigInspection(a.Out, redactedConfig(a.Config), paths, args)
}

func (a *App) Providers(args []string) error {
	paths := []string{
		filepath.Join(a.Config.ConfigHome, "config.json"),
		".codog.json",
		".codog.local.json",
	}
	return renderProvidersCommand(a.Out, a.Config, paths, args)
}

func renderProvidersCommand(out io.Writer, cfg config.Config, paths []string, args []string) error {
	req, err := parseProviderCommandArgs(args)
	if err != nil {
		return err
	}
	if req.Action == "set" {
		report, err := setProviderConfig(paths, req)
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(out, string(data))
			return nil
		}
		renderProviderSetText(out, report)
		return nil
	}
	report, err := buildProvidersReport(cfg, req.Action)
	if err != nil {
		return err
	}
	if req.Action == "show" {
		payload, err := providerShowPayload(report, req.Name)
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(payload, "", "  ")
			fmt.Fprintln(out, string(data))
			return nil
		}
		renderProviderShowText(out, payload)
		return nil
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	renderProvidersText(out, report)
	return nil
}

func parseProviderCommandArgs(args []string) (providerCommandRequest, error) {
	req := providerCommandRequest{Action: "status", Format: "text"}
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("providers output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--base-url":
			i++
			if i >= len(args) || strings.TrimSpace(args[i]) == "" {
				return req, errors.New("provider base URL is required")
			}
			req.BaseURL = args[i]
		case strings.HasPrefix(arg, "--base-url="):
			req.BaseURL = strings.TrimPrefix(arg, "--base-url=")
		case arg == "--model":
			i++
			if i >= len(args) || strings.TrimSpace(args[i]) == "" {
				return req, errors.New("provider model is required")
			}
			req.Model = args[i]
		case strings.HasPrefix(arg, "--model="):
			req.Model = strings.TrimPrefix(arg, "--model=")
		case arg == "--target":
			i++
			if i >= len(args) || strings.TrimSpace(args[i]) == "" {
				return req, errors.New("provider config target is required")
			}
			req.Target = args[i]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			i++
			if i >= len(args) || strings.TrimSpace(args[i]) == "" {
				return req, errors.New("provider config path is required")
			}
			req.Path = args[i]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 0 {
		switch strings.ToLower(positionals[0]) {
		case "status", "list", "show", "set":
			req.Action = strings.ToLower(positionals[0])
			positionals = positionals[1:]
		default:
			req.Name = positionals[0]
			req.Action = "show"
			positionals = positionals[1:]
		}
	}
	switch req.Action {
	case "status", "list":
		if len(positionals) > 0 {
			return req, unexpectedExtraArgsError{
				Command: "providers " + req.Action,
				Args:    append([]string(nil), positionals...),
				Usage:   "codog providers [status|list|show NAME|set NAME [BASE_URL] [MODEL]] [--json|--output-format text|json]",
			}
		}
	case "show":
		if req.Name == "" {
			if len(positionals) == 0 {
				return req, requiredArgumentError{Command: "providers show", Argument: "NAME", Usage: "codog providers show NAME [--json|--output-format text|json]"}
			}
			req.Name = positionals[0]
			positionals = positionals[1:]
		}
		if len(positionals) > 0 {
			return req, unexpectedExtraArgsError{
				Command: "providers show",
				Args:    append([]string(nil), positionals...),
				Usage:   "codog providers show NAME [--json|--output-format text|json]",
			}
		}
	case "set":
		if len(positionals) == 0 {
			return req, requiredArgumentError{Command: "providers set", Argument: "provider", Usage: "codog providers set anthropic|openai|xai|dashscope|custom [BASE_URL] [MODEL] [--target user|project|local|--path PATH]"}
		}
		req.Name = positionals[0]
		if len(positionals) > 1 && req.BaseURL == "" {
			req.BaseURL = positionals[1]
		}
		if len(positionals) > 2 && req.Model == "" {
			req.Model = positionals[2]
		}
		if len(positionals) > 3 {
			return req, unexpectedExtraArgsError{
				Command: "providers set",
				Args:    append([]string(nil), positionals[3:]...),
				Usage:   "codog providers set anthropic|openai|xai|dashscope|custom [BASE_URL] [MODEL] [--target user|project|local|--path PATH]",
			}
		}
	default:
		return req, unexpectedExtraArgsError{
			Command: "providers",
			Args:    []string{req.Action},
			Usage:   "codog providers [status|list|show NAME|set NAME [BASE_URL] [MODEL]] [--json|--output-format text|json]",
		}
	}
	switch req.Format {
	case "text", "json":
		return req, nil
	default:
		return req, outputFormatError{Command: "providers", Value: req.Format, Expected: []string{"text", "json"}}
	}
}

func buildProvidersReport(cfg config.Config, action string) (providersReport, error) {
	profiles, err := oauth.ListProviderProfiles(cfg.ConfigHome)
	if err != nil {
		return providersReport{}, err
	}
	oauthProfiles := make([]oauthProviderSummary, 0, len(profiles))
	for _, profile := range profiles {
		oauthProfiles = append(oauthProfiles, oauthProviderSummary{
			Name:     profile.Name,
			Issuer:   profile.Issuer,
			ClientID: profile.ClientID,
			Scopes:   append([]string(nil), profile.Scopes...),
		})
	}
	return providersReport{
		Kind:          "providers",
		Action:        action,
		Status:        "ok",
		Active:        activeProvider(cfg),
		Presets:       providerPresets(),
		OAuthProfiles: oauthProfiles,
	}, nil
}

func withProviderConfigLoadError(report providersReport, loadErr error) providersReport {
	if loadErr == nil {
		return report
	}
	message := strings.TrimSpace(loadErr.Error())
	if message == "" {
		return report
	}
	kind := buildCLIErrorReport(loadErr).ErrorKind
	if strings.TrimSpace(kind) == "" {
		kind = "config_load_failed"
	}
	report.Status = "degraded"
	report.ConfigLoadError = &message
	report.ConfigLoadErrorKind = kind
	report.Active.ConfigLoadError = &message
	report.Active.ConfigLoadErrorKind = kind
	return report
}

func activeProvider(cfg config.Config) activeProviderReport {
	name := "custom"
	protocol := "anthropic-compatible"
	if sameProviderURL(cfg.BaseURL, config.DefaultBaseURL) {
		name = "anthropic"
	}
	switch modelrouting.ProviderForModel(cfg.Model) {
	case modelrouting.ProviderOpenAI:
		name = "openai"
		protocol = "openai-compatible"
	case modelrouting.ProviderXAI:
		name = "xai"
		protocol = "openai-compatible"
	case modelrouting.ProviderDashScope:
		name = "dashscope"
		protocol = "openai-compatible"
	}
	openAICompatible := providerProtocolOpenAICompatible(protocol)
	extraBodyKeys, extraBodyForwardedKeys, extraBodyIgnoredKeys := providerExtraBodyKeyDiagnostics(cfg.ExtraBody, openAICompatible)
	wireModel := modelrouting.ResolveAlias(cfg.Model)
	if openAICompatible {
		wireModel = modelrouting.WireModelForBaseURL(wireModel, cfg.BaseURL)
	}
	reasoningModel := openAICompatible && modelrouting.IsReasoningModel(wireModel)
	preservesReasoningContent := openAICompatible && modelrouting.RequiresReasoningContentHistory(wireModel)
	stripsTuningParams := reasoningModel
	supportsStreamUsage := providerSupportsStreamUsage(name, openAICompatible)
	return activeProviderReport{
		Name:                                  name,
		Protocol:                              protocol,
		BaseURL:                               cfg.BaseURL,
		Model:                                 cfg.Model,
		MaxTokens:                             cfg.MaxTokens,
		MaxTurns:                              cfg.MaxTurns,
		OpenAICompatible:                      openAICompatible,
		ReasoningModel:                        reasoningModel,
		PreservesReasoningContentInHistory:    preservesReasoningContent,
		StripsTuningParams:                    stripsTuningParams,
		SupportsStreamUsage:                   supportsStreamUsage,
		HonorsProxyEnv:                        true,
		SupportsExtraBodyParams:               openAICompatible,
		ExtraBodyConfigured:                   len(cfg.ExtraBody) != 0,
		ExtraBodyKeys:                         extraBodyKeys,
		ExtraBodyForwardedKeys:                extraBodyForwardedKeys,
		ExtraBodyIgnoredKeys:                  extraBodyIgnoredKeys,
		PreservesSlashModelIDsOnCustomBaseURL: name == "openai",
		ProtectedExtraBodyKeys:                providerProtectedExtraBodyKeys(openAICompatible),
		Diagnostics:                           providerDiagnosticsForActiveConfig(cfg, name, wireModel, reasoningModel, preservesReasoningContent, stripsTuningParams, extraBodyIgnoredKeys),
		Auth:                                  providerAuthStatus(cfg),
	}
}

func providerDiagnosticsForActiveConfig(cfg config.Config, providerName string, wireModel string, reasoningModel bool, preservesReasoningContent bool, stripsTuningParams bool, ignoredExtraBodyKeys []string) []providerDiagnosticReport {
	diagnostics := []providerDiagnosticReport{}
	if strings.TrimSpace(cfg.ReasoningEffort) != "" && !(strings.EqualFold(providerName, "openai") && reasoningModel) {
		diagnostics = append(diagnostics, providerDiagnosticReport{
			Code:     "reasoning_effort_unsupported",
			Severity: "warning",
			Message:  fmt.Sprintf("%s does not map reasoning_effort for model %q.", providerName, cfg.Model),
			Action:   "Remove reasoning_effort or route to an OpenAI-compatible reasoning model such as openai/o4-mini.",
		})
	}
	if stripsTuningParams && cfg.Temperature != nil {
		diagnostics = append(diagnostics, providerDiagnosticReport{
			Code:     "reasoning_model_fixed_sampling",
			Severity: "info",
			Message:  fmt.Sprintf("Model %q is treated as a fixed-sampling reasoning model; tuning parameters are omitted before the provider call.", cfg.Model),
			Action:   "Leave temperature unset for reasoning models to match provider validation rules.",
		})
	}
	if preservesReasoningContent {
		diagnostics = append(diagnostics, providerDiagnosticReport{
			Code:     "reasoning_history_required",
			Severity: "info",
			Message:  fmt.Sprintf("Model %q requires assistant thinking history to be echoed as reasoning_content.", wireModel),
			Action:   "Keep prior assistant thinking blocks in history; Codog will serialize them as reasoning_content.",
		})
	}
	if len(ignoredExtraBodyKeys) != 0 {
		diagnostics = append(diagnostics, providerDiagnosticReport{
			Code:     "extra_body_keys_ignored",
			Severity: "info",
			Message:  fmt.Sprintf("Extra body keys ignored before the provider call: %s.", strings.Join(ignoredExtraBodyKeys, ", ")),
			Action:   "Remove protected keys from extra_body or set the corresponding first-class Codog option.",
		})
	}
	return diagnostics
}

func providerExtraBodyKeyDiagnostics(extraBody map[string]any, supported bool) ([]string, []string, []string) {
	if len(extraBody) == 0 {
		return nil, nil, nil
	}
	all := []string{}
	forwarded := []string{}
	ignored := []string{}
	protected := map[string]bool{}
	for _, key := range providerProtectedExtraBodyKeys(supported) {
		protected[key] = true
	}
	for key := range extraBody {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		all = append(all, key)
		if !supported || protected[key] {
			ignored = append(ignored, key)
			continue
		}
		forwarded = append(forwarded, key)
	}
	return sortedUniqueStrings(all), sortedUniqueStrings(forwarded), sortedUniqueStrings(ignored)
}

func providerSupportsStreamUsage(name string, openAICompatible bool) bool {
	if !openAICompatible {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(name), "xai")
}

func providerAuthStatus(cfg config.Config) providerAuthReport {
	storedOAuth := false
	if token, err := oauth.LoadToken(cfg.ConfigHome); err == nil && token.AccessToken != "" && token.AccessToken == cfg.AuthToken {
		storedOAuth = true
	}
	sources := []string{}
	if cfg.APIKey != "" {
		sources = append(sources, "api_key")
	}
	if cfg.AuthToken != "" {
		if storedOAuth {
			sources = append(sources, "stored_oauth")
		} else {
			sources = append(sources, "auth_token")
		}
	}
	preferred := ""
	if cfg.AuthToken != "" {
		preferred = "auth_token"
		if storedOAuth {
			preferred = "stored_oauth"
		}
	} else if cfg.APIKey != "" {
		preferred = "api_key"
	}
	return providerAuthReport{
		Configured:     len(sources) != 0,
		Sources:        sources,
		APIKey:         cfg.APIKey != "",
		AuthToken:      cfg.AuthToken != "",
		StoredOAuth:    storedOAuth,
		PreferredToken: preferred,
	}
}

func providerPresets() []providerPreset {
	return []providerPreset{
		{
			Name:           "anthropic",
			Protocol:       "anthropic-compatible",
			BaseURL:        config.DefaultBaseURL,
			DefaultModel:   config.DefaultModel,
			AuthEnv:        []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
			HonorsProxyEnv: true,
			Description:    "Anthropic Messages API.",
		},
		{
			Name:                                  "openai",
			Protocol:                              "openai-compatible",
			BaseURL:                               modelrouting.DefaultOpenAIBaseURL,
			DefaultModel:                          "openai/gpt-4o-mini",
			AuthEnv:                               []string{"CODOG_API_KEY", "CODOG_AUTH_TOKEN", "OPENAI_API_KEY"},
			OpenAICompatible:                      true,
			SupportsStreamUsage:                   true,
			HonorsProxyEnv:                        true,
			SupportsExtraBodyParams:               true,
			PreservesSlashModelIDsOnCustomBaseURL: true,
			ProtectedExtraBodyKeys:                providerProtectedExtraBodyKeys(true),
			Description:                           "OpenAI-compatible Chat Completions API selected by the openai/ model prefix.",
		},
		{
			Name:                    "xai",
			Protocol:                "openai-compatible",
			BaseURL:                 modelrouting.DefaultXAIBaseURL,
			DefaultModel:            "grok",
			AuthEnv:                 []string{"XAI_API_KEY"},
			OpenAICompatible:        true,
			HonorsProxyEnv:          true,
			SupportsExtraBodyParams: true,
			ProtectedExtraBodyKeys:  providerProtectedExtraBodyKeys(true),
			Description:             "xAI Chat Completions API selected by Grok model aliases or the xai/ model prefix.",
		},
		{
			Name:                    "dashscope",
			Protocol:                "openai-compatible",
			BaseURL:                 modelrouting.DefaultDashScopeBaseURL,
			DefaultModel:            "qwen-plus",
			AuthEnv:                 []string{"DASHSCOPE_API_KEY"},
			OpenAICompatible:        true,
			SupportsStreamUsage:     true,
			HonorsProxyEnv:          true,
			SupportsExtraBodyParams: true,
			ProtectedExtraBodyKeys:  providerProtectedExtraBodyKeys(true),
			Description:             "Alibaba DashScope compatible mode selected by Qwen and Kimi model aliases or prefixes.",
		},
		{
			Name:           "custom",
			Protocol:       "anthropic-compatible",
			AuthEnv:        []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
			HonorsProxyEnv: true,
			Description:    "Any endpoint that implements the Anthropic Messages API.",
		},
	}
}

func providerProtocolOpenAICompatible(protocol string) bool {
	return strings.EqualFold(strings.TrimSpace(protocol), "openai-compatible")
}

func providerProtectedExtraBodyKeys(enabled bool) []string {
	if !enabled {
		return nil
	}
	return []string{"model", "messages", "stream", "tools", "tool_choice", "max_tokens", "max_completion_tokens"}
}

func providerShowPayload(report providersReport, name string) (any, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "current", "active", "":
		return report.Active, nil
	case "oauth":
		return report.OAuthProfiles, nil
	}
	for _, preset := range report.Presets {
		if strings.EqualFold(preset.Name, name) {
			return preset, nil
		}
	}
	for _, profile := range report.OAuthProfiles {
		if strings.EqualFold(profile.Name, name) {
			return profile, nil
		}
	}
	return nil, invalidFlagValueError{
		Flag:    "provider",
		Value:   name,
		Message: fmt.Sprintf("unknown provider %q", name),
		Hint:    unknownProviderNameHint(name, "show"),
		Usage:   "codog providers show NAME [--json|--output-format text|json]",
	}
}

var providerNameCandidates = []string{
	"current", "active", "oauth", "anthropic", "default", "custom", "compatible",
	"anthropic-compatible", "openai", "openai-compatible", "xai", "grok",
	"dashscope", "qwen", "kimi",
}

func unknownProviderNameHint(name string, action string) string {
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		action = "show"
	}
	suggestions := toolnames.Suggestions(name, providerNameCandidates, 4)
	switch len(suggestions) {
	case 1:
		return fmt.Sprintf("Did you mean `codog providers %s %s`? Use `codog providers status` to inspect configured providers.", action, suggestions[0])
	case 0:
		return "Use `codog providers status` to inspect configured providers, or choose anthropic, openai, xai, dashscope, custom, current, or oauth."
	default:
		return fmt.Sprintf("Did you mean one of: %s? Use `codog providers status` to inspect configured providers.", strings.Join(suggestions, ", "))
	}
}

func setProviderConfig(paths []string, req providerCommandRequest) (providerSetReport, error) {
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if name == "" {
		return providerSetReport{}, errors.New("provider name is required")
	}
	requestedName := name
	baseURL := strings.TrimSpace(req.BaseURL)
	model := strings.TrimSpace(req.Model)
	switch name {
	case "anthropic", "default":
		name = "anthropic"
		if baseURL == "" {
			baseURL = config.DefaultBaseURL
		}
		if model == "" {
			model = config.DefaultModel
		}
	case "custom", "compatible", "anthropic-compatible":
		name = "custom"
		if baseURL == "" {
			return providerSetReport{}, errors.New("custom provider requires --base-url or a BASE_URL positional argument")
		}
	case "openai", "openai-compatible":
		name = "openai"
		if baseURL == "" {
			baseURL = modelrouting.DefaultOpenAIBaseURL
		}
		if model == "" {
			model = "openai/gpt-4o-mini"
		}
	case "xai", "grok":
		name = "xai"
		if baseURL == "" {
			baseURL = modelrouting.DefaultXAIBaseURL
		}
		if model == "" {
			model = "grok"
		}
	case "dashscope", "qwen", "kimi":
		name = "dashscope"
		if baseURL == "" {
			baseURL = modelrouting.DefaultDashScopeBaseURL
		}
		if model == "" {
			model = "qwen-plus"
			if requestedName == "kimi" {
				model = "kimi"
			}
		}
	default:
		if baseURL == "" {
			return providerSetReport{}, invalidFlagValueError{
				Flag:    "provider",
				Value:   req.Name,
				Message: fmt.Sprintf("unknown provider %q; use anthropic, openai, xai, dashscope, or custom --base-url URL", req.Name),
				Hint:    unknownProviderNameHint(req.Name, "set"),
				Usage:   "codog providers set anthropic|openai|xai|dashscope|custom [BASE_URL] [MODEL] [--target user|project|local|--path PATH]",
			}
		}
	}
	if err := validateProviderBaseURL(baseURL); err != nil {
		return providerSetReport{}, err
	}
	mutationReq := configMutationRequest{Target: req.Target, Path: req.Path}
	path, err := configMutationPath(mutationReq, paths)
	if err != nil {
		return providerSetReport{}, err
	}
	changes := []config.MutationReport{}
	baseReport, err := config.SetFileValue(path, "base_url", baseURL)
	if err != nil {
		return providerSetReport{}, err
	}
	changes = append(changes, baseReport)
	if model != "" {
		modelReport, err := config.SetFileValue(path, "model", model)
		if err != nil {
			return providerSetReport{}, err
		}
		changes = append(changes, modelReport)
	}
	return providerSetReport{
		Kind:     "provider",
		Action:   "set",
		Status:   "ok",
		Provider: name,
		BaseURL:  baseURL,
		Model:    model,
		Target:   req.Target,
		Path:     path,
		Changes:  changes,
	}, nil
}

func validateProviderBaseURL(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("provider base URL is required")
	}
	if strings.Contains(value, "://") {
		if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
			return nil
		}
		return errors.New("provider base URL must use http or https")
	}
	return errors.New("provider base URL must include a scheme")
}

func sameProviderURL(left, right string) bool {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(left)), "/") == strings.TrimRight(strings.ToLower(strings.TrimSpace(right)), "/")
}

func renderProvidersText(out io.Writer, report providersReport) {
	active := report.Active
	if report.ConfigLoadError != nil {
		fmt.Fprintf(out, "Status: %s\n", report.Status)
		fmt.Fprintf(out, "Config load: degraded: %s\n", *report.ConfigLoadError)
		fmt.Fprintln(out, "Hint: Fix the listed config file or run `codog doctor` for details.")
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "Provider: %s (%s)\n", active.Name, active.Protocol)
	fmt.Fprintf(out, "Model: %s\n", active.Model)
	fmt.Fprintf(out, "Base URL: %s\n", active.BaseURL)
	fmt.Fprintf(out, "Reasoning model: %t\n", active.ReasoningModel)
	fmt.Fprintf(out, "Stream usage: %t\n", active.SupportsStreamUsage)
	if active.SupportsExtraBodyParams {
		extraBody := "supported"
		if active.ExtraBodyConfigured {
			extraBody = "configured"
		}
		fmt.Fprintf(out, "Extra body: %s\n", extraBody)
	} else {
		fmt.Fprintln(out, "Extra body: unsupported")
	}
	if len(active.ExtraBodyForwardedKeys) != 0 {
		fmt.Fprintf(out, "Extra body forwarded: %s\n", strings.Join(active.ExtraBodyForwardedKeys, ", "))
	}
	if len(active.ExtraBodyIgnoredKeys) != 0 {
		fmt.Fprintf(out, "Extra body ignored: %s\n", strings.Join(active.ExtraBodyIgnoredKeys, ", "))
	}
	for _, diagnostic := range active.Diagnostics {
		fmt.Fprintf(out, "Diagnostic: %s %s\n", diagnostic.Severity, diagnostic.Code)
	}
	auth := "not configured"
	if active.Auth.Configured {
		auth = strings.Join(active.Auth.Sources, ", ")
	}
	fmt.Fprintf(out, "Auth: %s\n", auth)
	if len(report.Presets) != 0 {
		fmt.Fprintln(out, "\nPresets:")
		for _, preset := range report.Presets {
			baseURL := preset.BaseURL
			if baseURL == "" {
				baseURL = "<custom>"
			}
			fmt.Fprintf(out, "  %s: %s (%s)\n", preset.Name, baseURL, preset.Protocol)
		}
	}
	if len(report.OAuthProfiles) != 0 {
		fmt.Fprintln(out, "\nOAuth profiles:")
		for _, profile := range report.OAuthProfiles {
			fmt.Fprintf(out, "  %s: %s\n", profile.Name, profile.Issuer)
		}
	}
}

func renderProviderShowText(out io.Writer, payload any) {
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(out, string(data))
}

func renderProviderSetText(out io.Writer, report providerSetReport) {
	fmt.Fprintf(out, "Provider set: %s\n", report.Provider)
	fmt.Fprintf(out, "Base URL: %s\n", report.BaseURL)
	if report.Model != "" {
		fmt.Fprintf(out, "Model: %s\n", report.Model)
	}
	fmt.Fprintf(out, "Config: %s\n", report.Path)
}

type oauthFlowStatusReport struct {
	Kind         string                   `json:"kind"`
	Action       string                   `json:"action"`
	Status       string                   `json:"status"`
	Flow         string                   `json:"flow"`
	ProfileCount int                      `json:"profile_count"`
	ReadyCount   int                      `json:"ready_count"`
	Profiles     []oauthFlowProfileStatus `json:"profiles"`
}

type oauthFlowProfileStatus struct {
	Name      string   `json:"name"`
	Issuer    string   `json:"issuer"`
	ClientID  string   `json:"client_id,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
	Ready     bool     `json:"ready"`
	Missing   []string `json:"missing,omitempty"`
	Endpoints struct {
		Authorization       string `json:"authorization,omitempty"`
		DeviceAuthorization string `json:"device_authorization,omitempty"`
		Token               string `json:"token,omitempty"`
	} `json:"endpoints"`
}

func (a *App) oauthFlowStatus(flow string, profileName string) error {
	var profiles []oauth.ProviderProfile
	if strings.TrimSpace(profileName) != "" {
		profile, err := oauth.LoadProviderProfile(a.Config.ConfigHome, profileName)
		if err != nil {
			return err
		}
		profiles = []oauth.ProviderProfile{profile}
	} else {
		listed, err := oauth.ListProviderProfiles(a.Config.ConfigHome)
		if err != nil {
			return err
		}
		profiles = listed
	}
	report := buildOAuthFlowStatusReport(flow, profiles)
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func buildOAuthFlowStatusReport(flow string, profiles []oauth.ProviderProfile) oauthFlowStatusReport {
	flow = strings.ToLower(strings.TrimSpace(flow))
	report := oauthFlowStatusReport{
		Kind:         "oauth",
		Action:       "status",
		Status:       "ok",
		Flow:         flow,
		ProfileCount: len(profiles),
		Profiles:     []oauthFlowProfileStatus{},
	}
	for _, profile := range profiles {
		item := oauthFlowProfileStatus{
			Name:     profile.Name,
			Issuer:   profile.Issuer,
			ClientID: profile.ClientID,
			Scopes:   append([]string(nil), profile.Scopes...),
		}
		item.Endpoints.Authorization = profile.Metadata.AuthorizationEndpoint
		item.Endpoints.DeviceAuthorization = profile.Metadata.DeviceAuthorizationEndpoint
		item.Endpoints.Token = profile.Metadata.TokenEndpoint
		if strings.TrimSpace(profile.ClientID) == "" {
			item.Missing = append(item.Missing, "client_id")
		}
		if strings.TrimSpace(profile.Metadata.TokenEndpoint) == "" {
			item.Missing = append(item.Missing, "token_endpoint")
		}
		switch flow {
		case "device":
			if strings.TrimSpace(profile.Metadata.DeviceAuthorizationEndpoint) == "" {
				item.Missing = append(item.Missing, "device_authorization_endpoint")
			}
		default:
			if strings.TrimSpace(profile.Metadata.AuthorizationEndpoint) == "" {
				item.Missing = append(item.Missing, "authorization_endpoint")
			}
		}
		item.Ready = len(item.Missing) == 0
		if item.Ready {
			report.ReadyCount++
		}
		report.Profiles = append(report.Profiles, item)
	}
	return report
}

var startBrowserCallbackServer = oauth.StartBrowserCallbackServer

func (a *App) oauthBrowser(args []string) error {
	if len(args) == 0 {
		return a.oauthFlowStatus("browser", "")
	}
	if args[0] == "status" {
		profile := ""
		if len(args) > 1 {
			profile = args[1]
		}
		return a.oauthFlowStatus("browser", profile)
	}
	switch args[0] {
	case "start":
		if len(args) < 2 {
			return renderMissingActionArgument(a.Out, "oauth", "browser_start", "profile", "oauth browser start requires a profile name", "Usage: codog oauth browser start PROFILE REDIRECT_URI [SCOPE...].", "json")
		}
		if len(args) < 3 {
			return renderMissingActionArgument(a.Out, "oauth", "browser_start", "redirect_uri", "oauth browser start requires a redirect URI", "Usage: codog oauth browser start PROFILE REDIRECT_URI [SCOPE...].", "json")
		}
		source, err := a.oauthProfileSource(args[1], args[3:])
		if err != nil {
			return err
		}
		auth, err := oauth.BuildBrowserAuthorization(source.Metadata, source.ClientID, args[2], source.Scopes, "", oauth.PKCE{})
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(auth, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "exchange":
		if len(args) < 2 {
			return renderMissingActionArgument(a.Out, "oauth", "browser_exchange", "profile", "oauth browser exchange requires a profile name", "Usage: codog oauth browser exchange PROFILE CODE CODE_VERIFIER REDIRECT_URI.", "json")
		}
		if len(args) < 3 {
			return renderMissingActionArgument(a.Out, "oauth", "browser_exchange", "code", "oauth browser exchange requires an authorization code", "Usage: codog oauth browser exchange PROFILE CODE CODE_VERIFIER REDIRECT_URI.", "json")
		}
		if len(args) < 4 {
			return renderMissingActionArgument(a.Out, "oauth", "browser_exchange", "code_verifier", "oauth browser exchange requires a PKCE code verifier", "Usage: codog oauth browser exchange PROFILE CODE CODE_VERIFIER REDIRECT_URI.", "json")
		}
		if len(args) < 5 {
			return renderMissingActionArgument(a.Out, "oauth", "browser_exchange", "redirect_uri", "oauth browser exchange requires a redirect URI", "Usage: codog oauth browser exchange PROFILE CODE CODE_VERIFIER REDIRECT_URI.", "json")
		}
		source, err := a.oauthProfileSource(args[1], nil)
		if err != nil {
			return err
		}
		token, err := oauth.ExchangeAuthorizationCode(context.Background(), source.Metadata, source.ClientID, args[2], args[3], args[4])
		if err != nil {
			return err
		}
		saved, err := oauth.SaveToken(a.Config.ConfigHome, token)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(saved.View(time.Now().UTC()), "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "login":
		if len(args) < 2 {
			return renderMissingActionArgument(a.Out, "oauth", "browser_login", "profile", "oauth browser login requires a profile name", "Usage: codog oauth browser login PROFILE [ADDR].", "json")
		}
		source, err := a.oauthProfileSource(args[1], nil)
		if err != nil {
			return err
		}
		addr := "127.0.0.1:0"
		if len(args) > 2 {
			addr = args[2]
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		pkce, err := oauth.GeneratePKCE()
		if err != nil {
			return err
		}
		state, err := oauth.GenerateState()
		if err != nil {
			return err
		}
		callback, err := startBrowserCallbackServer(ctx, addr, "/oauth/callback", state)
		if err != nil {
			return err
		}
		defer func() { _ = callback.Close() }()
		auth, err := oauth.BuildBrowserAuthorization(source.Metadata, source.ClientID, callback.RedirectURI, source.Scopes, state, pkce)
		if err != nil {
			return err
		}
		if a.Err != nil {
			fmt.Fprintf(a.Err, "Open %s\n", auth.AuthorizationURL)
		}
		result := <-callback.Results
		if result.Err != nil {
			return result.Err
		}
		token, err := oauth.ExchangeAuthorizationCode(ctx, source.Metadata, source.ClientID, result.Callback.Code, auth.CodeVerifier, callback.RedirectURI)
		if err != nil {
			return err
		}
		saved, err := oauth.SaveToken(a.Config.ConfigHome, token)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(map[string]any{"redirect_uri": callback.RedirectURI, "token": saved.View(time.Now().UTC())}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	default:
		return unexpectedExtraArgsError{
			Command: "oauth browser",
			Args:    []string{args[0]},
			Usage:   oauthBrowserUsage,
		}
	}
}

func (a *App) oauthDevice(args []string) error {
	if len(args) == 0 {
		return a.oauthFlowStatus("device", "")
	}
	if args[0] == "status" {
		profile := ""
		if len(args) > 1 {
			profile = args[1]
		}
		return a.oauthFlowStatus("device", profile)
	}
	switch args[0] {
	case "start":
		if len(args) < 2 {
			return renderMissingActionArgument(a.Out, "oauth", "device_start", "profile_or_issuer", "oauth device start requires a profile name or issuer URL", "Usage: codog oauth device start ISSUER_URL CLIENT_ID [SCOPE...] or codog oauth device start PROFILE [SCOPE...].", "json")
		}
		if isURLish(args[1]) && len(args) < 3 {
			return renderMissingActionArgument(a.Out, "oauth", "device_start", "client_id", "oauth device start with an issuer URL requires a client id", "Usage: codog oauth device start ISSUER_URL CLIENT_ID [SCOPE...].", "json")
		}
		source, err := a.oauthDeviceSource(args[1:], true)
		if err != nil {
			return err
		}
		auth, err := oauth.StartDeviceAuthorization(context.Background(), source.Metadata, source.ClientID, source.Scopes)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(auth, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "poll":
		if len(args) < 2 {
			return renderMissingActionArgument(a.Out, "oauth", "device_poll", "profile_or_issuer", "oauth device poll requires a profile name or issuer URL", "Usage: codog oauth device poll ISSUER_URL CLIENT_ID DEVICE_CODE or codog oauth device poll PROFILE DEVICE_CODE.", "json")
		}
		if isURLish(args[1]) {
			if len(args) < 3 {
				return renderMissingActionArgument(a.Out, "oauth", "device_poll", "client_id", "oauth device poll with an issuer URL requires a client id", "Usage: codog oauth device poll ISSUER_URL CLIENT_ID DEVICE_CODE.", "json")
			}
			if len(args) < 4 {
				return renderMissingActionArgument(a.Out, "oauth", "device_poll", "device_code", "oauth device poll requires a device code", "Usage: codog oauth device poll ISSUER_URL CLIENT_ID DEVICE_CODE.", "json")
			}
		} else if len(args) < 3 {
			return renderMissingActionArgument(a.Out, "oauth", "device_poll", "device_code", "oauth device poll requires a device code", "Usage: codog oauth device poll PROFILE DEVICE_CODE.", "json")
		}
		source, deviceCode, err := a.oauthDevicePollSource(args[1:])
		if err != nil {
			return err
		}
		token, err := oauth.PollDeviceToken(context.Background(), source.Metadata, deviceCode, oauth.DevicePollOptions{ClientID: source.ClientID})
		if err != nil {
			return err
		}
		saved, err := oauth.SaveToken(a.Config.ConfigHome, token)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(saved.View(time.Now().UTC()), "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "login":
		if len(args) < 2 {
			return renderMissingActionArgument(a.Out, "oauth", "device_login", "profile_or_issuer", "oauth device login requires a profile name or issuer URL", "Usage: codog oauth device login ISSUER_URL CLIENT_ID [SCOPE...] or codog oauth device login PROFILE [SCOPE...].", "json")
		}
		if isURLish(args[1]) && len(args) < 3 {
			return renderMissingActionArgument(a.Out, "oauth", "device_login", "client_id", "oauth device login with an issuer URL requires a client id", "Usage: codog oauth device login ISSUER_URL CLIENT_ID [SCOPE...].", "json")
		}
		source, err := a.oauthDeviceSource(args[1:], true)
		if err != nil {
			return err
		}
		auth, err := oauth.StartDeviceAuthorization(context.Background(), source.Metadata, source.ClientID, source.Scopes)
		if err != nil {
			return err
		}
		if a.Err != nil {
			target := auth.VerificationURI
			if auth.VerificationURIComplete != "" {
				target = auth.VerificationURIComplete
			}
			fmt.Fprintf(a.Err, "Open %s and enter code %s\n", target, auth.UserCode)
		}
		token, err := oauth.PollDeviceToken(context.Background(), source.Metadata, auth.DeviceCode, oauth.DevicePollOptions{
			ClientID:  source.ClientID,
			Interval:  time.Duration(auth.Interval) * time.Second,
			ExpiresAt: auth.ExpiresAt,
		})
		if err != nil {
			return err
		}
		saved, err := oauth.SaveToken(a.Config.ConfigHome, token)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(map[string]any{"device": auth, "token": saved.View(time.Now().UTC())}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	default:
		return unexpectedExtraArgsError{
			Command: "oauth device",
			Args:    []string{args[0]},
			Usage:   oauthDeviceUsage,
		}
	}
}

type oauthDeviceSource struct {
	Metadata oauth.ProviderMetadata
	ClientID string
	Scopes   []string
}

func (a *App) oauthProfileSource(name string, overrideScopes []string) (oauthDeviceSource, error) {
	profile, err := oauth.ResolveProviderProfile(a.Config.ConfigHome, name)
	if err != nil {
		return oauthDeviceSource{}, err
	}
	scopes := append([]string(nil), profile.Scopes...)
	if len(overrideScopes) != 0 {
		scopes = append([]string(nil), overrideScopes...)
	}
	return oauthDeviceSource{Metadata: profile.Metadata, ClientID: profile.ClientID, Scopes: scopes}, nil
}

func (a *App) oauthDeviceSource(args []string, allowScopes bool) (oauthDeviceSource, error) {
	if len(args) == 0 {
		return oauthDeviceSource{}, errors.New("oauth device provider is required")
	}
	if isURLish(args[0]) {
		if len(args) < 2 {
			return oauthDeviceSource{}, errors.New("oauth device client id is required")
		}
		metadata, err := oauth.DiscoverProvider(context.Background(), args[0])
		if err != nil {
			return oauthDeviceSource{}, err
		}
		source := oauthDeviceSource{Metadata: metadata, ClientID: args[1]}
		if allowScopes {
			source.Scopes = append([]string(nil), args[2:]...)
		}
		return source, nil
	}
	profile, err := oauth.ResolveProviderProfile(a.Config.ConfigHome, args[0])
	if err != nil {
		return oauthDeviceSource{}, err
	}
	scopes := append([]string(nil), profile.Scopes...)
	if allowScopes && len(args) > 1 {
		scopes = append([]string(nil), args[1:]...)
	}
	return oauthDeviceSource{Metadata: profile.Metadata, ClientID: profile.ClientID, Scopes: scopes}, nil
}

func (a *App) oauthDevicePollSource(args []string) (oauthDeviceSource, string, error) {
	if len(args) == 0 {
		return oauthDeviceSource{}, "", errors.New("usage: codog oauth device poll ISSUER_URL CLIENT_ID DEVICE_CODE | poll PROFILE DEVICE_CODE")
	}
	if isURLish(args[0]) {
		if len(args) < 3 {
			return oauthDeviceSource{}, "", errors.New("usage: codog oauth device poll ISSUER_URL CLIENT_ID DEVICE_CODE")
		}
		source, err := a.oauthDeviceSource(args[:2], false)
		return source, args[2], err
	}
	if len(args) < 2 {
		return oauthDeviceSource{}, "", errors.New("usage: codog oauth device poll PROFILE DEVICE_CODE")
	}
	source, err := a.oauthDeviceSource(args[:1], false)
	return source, args[1], err
}

func isURLish(value string) bool {
	return strings.Contains(value, "://")
}

func (a *App) Sandbox() error {
	status := sandbox.Detect()
	strategy, options, err := sandboxReportRequestOptions(a.Config.Future)
	if err != nil {
		return err
	}
	execution, effective, executionErr := sandbox.ResolveSandboxExecutionStatusFor(strategy, a.Workspace, options, status)
	resolution := sandbox.ResolveStrategyReportFor(strategy, status)
	if resolution.Effective == "" {
		resolution.Effective = effective
	}
	data, _ := json.MarshalIndent(sandboxReport{
		Kind:               "sandbox",
		Action:             "status",
		Status:             sandboxRuntimeReportStatus(execution, firstNonEmptyAgentString(resolution.Error, errorString(executionErr))),
		OS:                 status.OS,
		Strategies:         append([]string(nil), status.Strategies...),
		Default:            status.Default,
		Available:          status.Available,
		ConfiguredStrategy: strategy,
		EffectiveStrategy:  resolution.Effective,
		Enabled:            resolution.Enabled,
		ResolutionStatus:   resolution.Status,
		FallbackReason:     firstNonEmptyAgentString(execution.FallbackReason, resolution.FallbackReason, status.FallbackReason),
		StrategyStatuses:   status.StrategyStatuses,
		Container:          status.Container,
		NamespaceSupported: status.NamespaceSupported,
		NetworkSupported:   status.NetworkSupported,
		Execution:          execution,
		Requested:          execution.Enabled,
		Active:             execution.Active,
		Supported:          execution.Supported,
		InContainer:        execution.InContainer,
		RequestedNamespace: execution.Requested.NamespaceRestrictions,
		ActiveNamespace:    execution.NamespaceActive,
		RequestedNetwork:   execution.Requested.NetworkIsolation,
		ActiveNetwork:      execution.NetworkActive,
		FilesystemMode:     execution.FilesystemMode,
		FilesystemActive:   execution.FilesystemActive,
		AllowedMounts:      jsonStringSlice(execution.AllowedMounts),
		Markers:            jsonStringSlice(execution.ContainerMarkers),
		ActiveComponents: map[string]bool{
			"namespace":  execution.NamespaceActive,
			"network":    execution.NetworkActive,
			"filesystem": execution.FilesystemActive,
		},
	}, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

type sandboxReport struct {
	Kind               string                         `json:"kind"`
	Action             string                         `json:"action"`
	Status             string                         `json:"status"`
	OS                 string                         `json:"os"`
	Strategies         []string                       `json:"strategies"`
	Default            string                         `json:"default"`
	Available          bool                           `json:"available"`
	ConfiguredStrategy string                         `json:"configured_strategy"`
	EffectiveStrategy  string                         `json:"effective_strategy,omitempty"`
	Enabled            bool                           `json:"enabled"`
	ResolutionStatus   string                         `json:"resolution_status"`
	FallbackReason     string                         `json:"fallback_reason,omitempty"`
	StrategyStatuses   []sandbox.StrategyStatus       `json:"strategy_statuses,omitempty"`
	Container          sandbox.ContainerStatus        `json:"container"`
	NamespaceSupported bool                           `json:"namespace_supported"`
	NetworkSupported   bool                           `json:"network_supported"`
	Execution          sandbox.SandboxExecutionStatus `json:"execution"`
	Requested          bool                           `json:"requested"`
	Active             bool                           `json:"active"`
	Supported          bool                           `json:"supported"`
	InContainer        bool                           `json:"in_container"`
	RequestedNamespace bool                           `json:"requested_namespace"`
	ActiveNamespace    bool                           `json:"active_namespace"`
	RequestedNetwork   bool                           `json:"requested_network"`
	ActiveNetwork      bool                           `json:"active_network"`
	FilesystemMode     string                         `json:"filesystem_mode"`
	FilesystemActive   bool                           `json:"filesystem_active"`
	AllowedMounts      []string                       `json:"allowed_mounts"`
	Markers            []string                       `json:"markers"`
	ActiveComponents   map[string]bool                `json:"active_components"`
}

func sandboxRuntimeReportStatus(status sandbox.SandboxExecutionStatus, resolutionError string) string {
	if strings.TrimSpace(resolutionError) != "" {
		if status.Enabled {
			return "error"
		}
		return "warn"
	}
	switch {
	case !status.Enabled:
		return "ok"
	case status.Active:
		return "ok"
	case status.Supported || status.FilesystemActive:
		return "warn"
	default:
		return "error"
	}
}

func jsonStringSlice(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string(nil), values...)
}

func sandboxReportRequestOptions(cfg config.FutureConfig) (string, sandbox.SandboxRequestOptions, error) {
	strategy := strings.TrimSpace(cfg.SandboxStrategy)
	if strategy == "" && cfg.Sandbox.Enabled != nil && *cfg.Sandbox.Enabled {
		strategy = "detect"
	}
	options, err := sandboxRequestOptionsFromConfig(cfg.Sandbox)
	if err != nil {
		return strategy, options, err
	}
	if !sandboxStrategyRequestsStatus(strategy) {
		disabled := false
		options.Enabled = &disabled
	}
	return strategy, options, nil
}

func sandboxRequestOptionsFromConfig(cfg config.SandboxConfig) (sandbox.SandboxRequestOptions, error) {
	options := sandbox.SandboxRequestOptions{
		Enabled:               cloneBoolAgent(cfg.Enabled),
		NamespaceRestrictions: cloneBoolAgent(cfg.NamespaceRestrictions),
		NetworkIsolation:      cloneBoolAgent(cfg.NetworkIsolation),
		AllowedMounts:         append([]string(nil), cfg.AllowedMounts...),
	}
	mode, err := sandbox.ParseFilesystemIsolationMode(cfg.FilesystemMode)
	if err != nil {
		return options, err
	}
	options.FilesystemMode = mode
	return options, nil
}

func sandboxStrategyRequestsStatus(strategy string) bool {
	switch strings.TrimSpace(strategy) {
	case "", "off", "none":
		return false
	default:
		return true
	}
}

func cloneBoolAgent(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func firstNonEmptyAgentString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

type sandboxToggleRequest struct {
	Action   string
	Format   string
	Strategy string
	Target   string
	Path     string
}

type sandboxToggleReport struct {
	Kind               string   `json:"kind"`
	Action             string   `json:"action"`
	Status             string   `json:"status"`
	OS                 string   `json:"os"`
	ConfiguredStrategy string   `json:"configured_strategy"`
	EffectiveStrategy  string   `json:"effective_strategy,omitempty"`
	ResolutionStatus   string   `json:"resolution_status"`
	Enabled            bool     `json:"enabled"`
	Available          bool     `json:"available"`
	DefaultStrategy    string   `json:"default_strategy,omitempty"`
	Strategies         []string `json:"strategies,omitempty"`
	Path               string   `json:"path,omitempty"`
	Error              string   `json:"error,omitempty"`
	FallbackReason     string   `json:"fallback_reason,omitempty"`
	NamespaceSupported bool     `json:"namespace_supported"`
	NetworkSupported   bool     `json:"network_supported"`
	InContainer        bool     `json:"in_container"`
	ContainerMarkers   []string `json:"container_markers,omitempty"`
}

func (a *App) SandboxToggle(args []string) error {
	req, err := parseSandboxToggleArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "status":
	case "set":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "sandbox.strategy", req.Strategy); err != nil {
			return err
		}
		a.Config.Future.SandboxStrategy = req.Strategy
		req.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "sandbox.strategy"); err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, legacySandboxStrategyKey); err != nil {
			return err
		}
		a.Config.Future.SandboxStrategy = ""
		req.Path = path
	default:
		return fmt.Errorf("unknown sandbox-toggle command %q", req.Action)
	}
	report := buildSandboxToggleReport(req, a.Config.Future.SandboxStrategy)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderSandboxToggleReport(a.Out, report)
	return nil
}

func parseSandboxToggleArgs(args []string) (sandboxToggleRequest, error) {
	req := sandboxToggleRequest{Action: "status", Format: "text", Target: "user"}
	actionSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("sandbox-toggle output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("sandbox-toggle target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("sandbox-toggle path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "sandbox-toggle", Option: arg, Usage: "codog sandbox-toggle [status|on|off|clear|sandbox-exec|bwrap|unshare|restricted-token] [--target user|project|local|--path PATH] [--json|--output-format text|json]"}
		default:
			if actionSet {
				return req, unexpectedExtraArgsError{
					Command: "sandbox-toggle",
					Args:    []string{arg},
					Usage:   "codog sandbox-toggle [status|on|off|clear|sandbox-exec|bwrap|unshare|restricted-token] [--target user|project|local|--path PATH] [--json|--output-format text|json]",
				}
			}
			action, strategy, err := normalizeSandboxToggleAction(arg)
			if err != nil {
				return req, err
			}
			req.Action = action
			req.Strategy = strategy
			actionSet = true
		}
	}
	if err := validateTextOrJSON(req.Format, "sandbox-toggle"); err != nil {
		return req, err
	}
	return req, nil
}

func normalizeSandboxToggleAction(value string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "status", "show", "list":
		return "status", "", nil
	case "clear", "reset", "unset":
		return "clear", "", nil
	case "on", "enable", "enabled", "auto", "detect":
		return "set", "detect", nil
	case "off", "disable", "disabled", "none":
		return "set", "off", nil
	case "sandbox-exec", "bwrap", "unshare", "restricted-token":
		return "set", strings.ToLower(strings.TrimSpace(value)), nil
	default:
		return "", "", unknownSandboxToggleStrategyError(value)
	}
}

var sandboxToggleStrategyCandidates = []string{"status", "show", "list", "clear", "reset", "unset", "on", "enable", "enabled", "auto", "detect", "off", "disable", "disabled", "none", "sandbox-exec", "bwrap", "unshare", "restricted-token"}

func unknownSandboxToggleStrategyError(value string) error {
	value = strings.TrimSpace(value)
	message := fmt.Sprintf("unknown sandbox-toggle strategy %q", value)
	suggestions := toolnames.Suggestions(value, sandboxToggleStrategyCandidates, 4)
	switch len(suggestions) {
	case 1:
		message += fmt.Sprintf("; did you mean %q?", suggestions[0])
	case 0:
	default:
		message += "; suggestions: " + strings.Join(suggestions, ", ")
	}
	return invalidFlagValueError{
		Flag:    "strategy",
		Value:   value,
		Message: message,
		Usage:   "codog sandbox-toggle [status|on|off|clear|sandbox-exec|bwrap|unshare|restricted-token] [--json|--output-format text|json]",
	}
}

func buildSandboxToggleReport(req sandboxToggleRequest, configured string) sandboxToggleReport {
	status := sandbox.Detect()
	configured = strings.TrimSpace(configured)
	resolution := sandbox.ResolveStrategyReportFor(configured, status)
	report := sandboxToggleReport{
		Kind:               "sandbox_toggle",
		Action:             req.Action,
		OS:                 status.OS,
		ConfiguredStrategy: configured,
		EffectiveStrategy:  resolution.Effective,
		ResolutionStatus:   resolution.Status,
		Enabled:            resolution.Enabled,
		Available:          status.Available,
		DefaultStrategy:    status.Default,
		Strategies:         status.Strategies,
		Path:               req.Path,
		FallbackReason:     firstNonEmptyAgentString(resolution.FallbackReason, status.FallbackReason),
		NamespaceSupported: status.NamespaceSupported,
		NetworkSupported:   status.NetworkSupported,
		InContainer:        status.Container.InContainer,
		ContainerMarkers:   status.Container.Markers,
	}
	switch {
	case resolution.Error != "":
		report.Status = "unavailable"
		report.Error = resolution.Error
	case resolution.Enabled:
		report.Status = "enabled"
	default:
		report.Status = resolution.Status
	}
	return report
}

func renderSandboxToggleReport(out io.Writer, report sandboxToggleReport) {
	fmt.Fprintln(out, "Sandbox Toggle")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  OS               %s\n", report.OS)
	fmt.Fprintf(out, "  Configured       %s\n", emptyAsNone(report.ConfiguredStrategy))
	fmt.Fprintf(out, "  Effective        %s\n", emptyAsNone(report.EffectiveStrategy))
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	fmt.Fprintf(out, "  Available        %t\n", report.Available)
	fmt.Fprintf(out, "  Namespace        %t\n", report.NamespaceSupported)
	fmt.Fprintf(out, "  Network          %t\n", report.NetworkSupported)
	fmt.Fprintf(out, "  Container        %t\n", report.InContainer)
	if report.DefaultStrategy != "" {
		fmt.Fprintf(out, "  Default          %s\n", report.DefaultStrategy)
	}
	if len(report.Strategies) > 0 {
		fmt.Fprintf(out, "  Strategies       %s\n", strings.Join(report.Strategies, ", "))
	}
	if report.FallbackReason != "" {
		fmt.Fprintf(out, "  Fallback         %s\n", report.FallbackReason)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Error != "" {
		fmt.Fprintf(out, "  Error            %s\n", report.Error)
	}
}

type heapDumpRequest struct {
	Path   string
	Format string
	GC     bool
}

type heapDumpReport struct {
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	GC        bool   `json:"gc"`
	WrittenAt string `json:"written_at"`
}

func (a *App) HeapDump(args []string) error {
	req, err := parseHeapDumpArgs(args)
	if err != nil {
		return err
	}
	path := req.Path
	if strings.TrimSpace(path) == "" {
		path = a.defaultHeapDumpPath(time.Now().UTC())
	} else {
		path = a.resolveOutputPath(path)
	}
	if req.GC {
		runtime.GC()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	writeErr := pprof.WriteHeapProfile(file)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}
	report := heapDumpReport{
		Kind:      "heapdump",
		Status:    "ok",
		Path:      path,
		Bytes:     stat.Size(),
		GC:        req.GC,
		WrittenAt: time.Now().UTC().Format(time.RFC3339),
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderHeapDumpReport(a.Out, report)
	return nil
}

func parseHeapDumpArgs(args []string) (heapDumpRequest, error) {
	req := heapDumpRequest{Format: "text", GC: true}
	pathSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("heapdump output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--gc":
			req.GC = true
		case arg == "--no-gc":
			req.GC = false
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown heapdump flag %q", arg)
		default:
			if pathSet {
				return req, fmt.Errorf("unexpected heapdump argument %q", arg)
			}
			req.Path = arg
			pathSet = true
		}
	}
	if err := validateTextOrJSON(req.Format, "heapdump"); err != nil {
		return req, err
	}
	return req, nil
}

func (a *App) defaultHeapDumpPath(now time.Time) string {
	name := "heap-" + now.Format("20060102-150405") + ".pprof"
	return a.resolveOutputPath(filepath.Join(".codog", "heap", name))
}

func renderHeapDumpReport(out io.Writer, report heapDumpReport) {
	fmt.Fprintln(out, "Heap Dump")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Path             %s\n", report.Path)
	fmt.Fprintf(out, "  Bytes            %d\n", report.Bytes)
	fmt.Fprintf(out, "  GC               %t\n", report.GC)
	fmt.Fprintf(out, "  Written at       %s\n", report.WrittenAt)
}

func (a *App) Init(args []string) error {
	return initProject(a.Out, a.Workspace, args, func(report projectinit.Report) error {
		return a.runSetupHook(context.Background(), "init", report.Status)
	})
}

type initVerifiersRequest struct {
	Format    string
	Target    string
	Workspace string
	Force     bool
	DryRun    bool
}

func (a *App) InitVerifiers(args []string) error {
	req, err := parseInitVerifiersArgs(args)
	if err != nil {
		return err
	}
	workspace := a.Workspace
	if req.Workspace != "" {
		workspace = a.resolveOutputPath(req.Workspace)
	}
	report, err := verifiers.Initialize(verifiers.Options{
		Workspace: workspace,
		Target:    req.Target,
		Force:     req.Force,
		DryRun:    req.DryRun,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, verifiers.RenderText(report))
	return nil
}

func parseInitVerifiersArgs(args []string) (initVerifiersRequest, error) {
	const usage = "codog init-verifiers [--target claude|codog] [--workspace PATH] [--force] [--dry-run] [--json|--output-format text|json]"
	req := initVerifiersRequest{Format: "text", Target: "claude"}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "init-verifiers", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "init-verifiers", Flag: arg, Usage: usage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--workspace":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "init-verifiers", Flag: arg, Usage: usage}
			}
			req.Workspace = args[index]
		case strings.HasPrefix(arg, "--workspace="):
			req.Workspace = strings.TrimPrefix(arg, "--workspace=")
		case arg == "--force":
			req.Force = true
		case arg == "--dry-run":
			req.DryRun = true
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "init-verifiers", Option: arg, Usage: usage}
			}
			return req, unexpectedExtraArgsError{Command: "init-verifiers", Args: []string{arg}, Usage: usage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("init-verifiers", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func (a *App) State(args []string) error {
	return renderWorkerStateFromPath(a.Out, a.Workspace, a.workerStatePath(), args)
}

func (a *App) Memory(args []string) error {
	return renderMemoryCommand(a.Out, a.Workspace, a.memoryRulesImportOptions(), args)
}

func (a *App) memoryRulesImportOptions() memory.RulesImportOptions {
	return memoryRulesImportOptionsFromConfig(a.Config)
}

func memoryRulesImportOptionsFromConfig(cfg config.Config) memory.RulesImportOptions {
	rules := cfg.EffectiveRulesImport()
	return memory.RulesImportOptions{
		Mode:       rules.Mode,
		Frameworks: append([]string(nil), rules.Frameworks...),
	}
}

type projectReport struct {
	Kind        string           `json:"kind"`
	Workspace   string           `json:"workspace"`
	Name        string           `json:"name"`
	Git         projectGitReport `json:"git"`
	GoModule    string           `json:"go_module,omitempty"`
	CodogDir    string           `json:"codog_dir,omitempty"`
	MemoryFiles []memory.Summary `json:"memory_files,omitempty"`
}

type projectGitReport struct {
	Available bool   `json:"available"`
	Root      string `json:"root,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Head      string `json:"head,omitempty"`
	Error     string `json:"error,omitempty"`
}

type envReport struct {
	Kind      string     `json:"kind"`
	Total     int        `json:"total"`
	Redacted  int        `json:"redacted"`
	Variables []envValue `json:"variables"`
}

type envValue struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Redacted bool   `json:"redacted,omitempty"`
}

func (a *App) Project(args []string) error {
	format, err := parseSimpleOutputFormat("project", args)
	if err != nil {
		return err
	}
	report := a.buildProjectReport()
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderProjectReport(a.Out, report)
	return nil
}

func (a *App) buildProjectReport() projectReport {
	workspace := a.Workspace
	if workspace == "" {
		workspace = "."
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}
	workspace = filepath.Clean(workspace)
	report := projectReport{
		Kind:      "project",
		Workspace: workspace,
		Name:      filepath.Base(workspace),
	}
	if root, err := gitops.Root(workspace); err == nil {
		report.Git.Available = true
		report.Git.Root = root
		if branch, err := gitops.Branch(workspace); err == nil {
			report.Git.Branch = branch
		}
		if head, err := gitops.Head(workspace); err == nil {
			report.Git.Head = head
		}
	} else {
		report.Git.Error = err.Error()
	}
	if path := findUp(workspace, "go.mod"); path != "" {
		report.GoModule = path
	}
	if path := filepath.Join(workspace, ".codog"); dirExists(path) {
		report.CodogDir = path
	}
	if files, err := memory.DiscoverWithRulesImport(workspace, a.memoryRulesImportOptions()); err == nil {
		report.MemoryFiles = memory.Summaries(files)
	}
	return report
}

func renderProjectReport(out io.Writer, report projectReport) {
	fmt.Fprintln(out, "Project")
	fmt.Fprintf(out, "  Workspace        %s\n", report.Workspace)
	fmt.Fprintf(out, "  Name             %s\n", report.Name)
	if report.Git.Available {
		fmt.Fprintf(out, "  Git root         %s\n", report.Git.Root)
		fmt.Fprintf(out, "  Git branch       %s\n", emptyAsNone(report.Git.Branch))
		fmt.Fprintf(out, "  Git head         %s\n", emptyAsNone(report.Git.Head))
	} else {
		fmt.Fprintf(out, "  Git              unavailable: %s\n", report.Git.Error)
	}
	fmt.Fprintf(out, "  Go module        %s\n", emptyAsNone(report.GoModule))
	fmt.Fprintf(out, "  Codog dir        %s\n", emptyAsNone(report.CodogDir))
	fmt.Fprintf(out, "  Memory files     %d\n", len(report.MemoryFiles))
	for index, file := range report.MemoryFiles {
		fmt.Fprintf(out, "  %d. %s\n", index+1, file.Path)
	}
}

func (a *App) Env(args []string) error {
	format, err := parseSimpleOutputFormat("env", args)
	if err != nil {
		return err
	}
	report := buildEnvReport(os.Environ())
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderEnvReport(a.Out, report)
	return nil
}

func buildEnvReport(environ []string) envReport {
	var variables []envValue
	redacted := 0
	for _, item := range environ {
		name, value, ok := strings.Cut(item, "=")
		if !ok || name == "" {
			continue
		}
		entry := envValue{Name: name, Value: value}
		if isSensitiveEnvName(name) {
			entry.Value = "[redacted]"
			entry.Redacted = true
			redacted++
		}
		variables = append(variables, entry)
	}
	sort.Slice(variables, func(i, j int) bool { return variables[i].Name < variables[j].Name })
	return envReport{
		Kind:      "env",
		Total:     len(variables),
		Redacted:  redacted,
		Variables: variables,
	}
}

func renderEnvReport(out io.Writer, report envReport) {
	fmt.Fprintln(out, "Environment")
	fmt.Fprintf(out, "  Variables        %d\n", report.Total)
	fmt.Fprintf(out, "  Redacted         %d\n", report.Redacted)
	fmt.Fprintln(out)
	for _, variable := range report.Variables {
		fmt.Fprintf(out, "%s=%s\n", variable.Name, variable.Value)
	}
}

func isSensitiveEnvName(name string) bool {
	upper := strings.ToUpper(name)
	sensitive := []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "AUTH"}
	for _, marker := range sensitive {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

type hooksRequest struct {
	Format           string
	Action           string
	WatchAction      string
	SessionID        string
	Event            string
	Tool             string
	Input            string
	Output           string
	IsError          bool
	TimeoutMS        int
	NotificationType string
	Title            string
	AgentID          string
	AgentType        string
	TranscriptPath   string
	LastAssistant    string
	WorktreeID       string
	WorktreePath     string
	Ref              string
	OldCWD           string
	NewCWD           string
	TaskID           string
	TaskKind         string
	TaskStatus       string
	FilePath         string
	Operation        string
	MemoryType       string
	LoadReason       string
	Globs            []string
	TriggerFilePath  string
	ParentFilePath   string
	StopHookActive   bool
	Reason           string
}

type hooksListReport struct {
	Kind                       string               `json:"kind"`
	Action                     string               `json:"action"`
	Status                     string               `json:"status"`
	Disabled                   bool                 `json:"disabled,omitempty"`
	ManagedOnly                bool                 `json:"managed_only,omitempty"`
	PreToolUse                 []string             `json:"pre_tool_use"`
	PostToolUse                []string             `json:"post_tool_use"`
	PostToolUseFailure         []string             `json:"post_tool_use_failure"`
	PermissionRequest          []string             `json:"permission_request"`
	PermissionDenied           []string             `json:"permission_denied"`
	UserPromptSubmit           []string             `json:"user_prompt_submit"`
	SessionStart               []string             `json:"session_start"`
	SessionEnd                 []string             `json:"session_end"`
	Setup                      []string             `json:"setup"`
	Stop                       []string             `json:"stop"`
	StopFailure                []string             `json:"stop_failure"`
	PreCompact                 []string             `json:"pre_compact"`
	PostCompact                []string             `json:"post_compact"`
	Notification               []string             `json:"notification"`
	SubagentStart              []string             `json:"subagent_start"`
	SubagentStop               []string             `json:"subagent_stop"`
	WorktreeCreate             []string             `json:"worktree_create"`
	WorktreeRemove             []string             `json:"worktree_remove"`
	CwdChanged                 []string             `json:"cwd_changed"`
	TaskCreated                []string             `json:"task_created"`
	TaskCompleted              []string             `json:"task_completed"`
	InstructionsLoaded         []string             `json:"instructions_loaded"`
	FileChanged                []string             `json:"file_changed"`
	PreToolUseCommands         []hookCommandSummary `json:"pre_tool_use_commands,omitempty"`
	PostToolUseCommands        []hookCommandSummary `json:"post_tool_use_commands,omitempty"`
	PostToolUseFailureCommands []hookCommandSummary `json:"post_tool_use_failure_commands,omitempty"`
	PermissionRequestCommands  []hookCommandSummary `json:"permission_request_commands,omitempty"`
	PermissionDeniedCommands   []hookCommandSummary `json:"permission_denied_commands,omitempty"`
	UserPromptSubmitCommands   []hookCommandSummary `json:"user_prompt_submit_commands,omitempty"`
	SessionStartCommands       []hookCommandSummary `json:"session_start_commands,omitempty"`
	SessionEndCommands         []hookCommandSummary `json:"session_end_commands,omitempty"`
	SetupCommands              []hookCommandSummary `json:"setup_commands,omitempty"`
	StopCommands               []hookCommandSummary `json:"stop_commands,omitempty"`
	StopFailureCommands        []hookCommandSummary `json:"stop_failure_commands,omitempty"`
	PreCompactCommands         []hookCommandSummary `json:"pre_compact_commands,omitempty"`
	PostCompactCommands        []hookCommandSummary `json:"post_compact_commands,omitempty"`
	NotificationCommands       []hookCommandSummary `json:"notification_commands,omitempty"`
	SubagentStartCommands      []hookCommandSummary `json:"subagent_start_commands,omitempty"`
	SubagentStopCommands       []hookCommandSummary `json:"subagent_stop_commands,omitempty"`
	WorktreeCreateCommands     []hookCommandSummary `json:"worktree_create_commands,omitempty"`
	WorktreeRemoveCommands     []hookCommandSummary `json:"worktree_remove_commands,omitempty"`
	CwdChangedCommands         []hookCommandSummary `json:"cwd_changed_commands,omitempty"`
	TaskCreatedCommands        []hookCommandSummary `json:"task_created_commands,omitempty"`
	TaskCompletedCommands      []hookCommandSummary `json:"task_completed_commands,omitempty"`
	InstructionsLoadedCommands []hookCommandSummary `json:"instructions_loaded_commands,omitempty"`
	FileChangedCommands        []hookCommandSummary `json:"file_changed_commands,omitempty"`
}

type hooksHealthReport struct {
	Kind            string               `json:"kind"`
	Action          string               `json:"action"`
	Status          string               `json:"status"`
	Disabled        bool                 `json:"disabled,omitempty"`
	ManagedOnly     bool                 `json:"managed_only,omitempty"`
	Workspace       string               `json:"workspace"`
	Event           string               `json:"event"`
	MatcherTarget   string               `json:"matcher_target"`
	ConfiguredCount int                  `json:"configured_count"`
	MatchedCount    int                  `json:"matched_count"`
	Matched         []hookCommandSummary `json:"matched"`
	Events          []hookEventHealth    `json:"events"`
}

type hooksWatchPathsReport struct {
	Kind        string              `json:"kind"`
	Action      string              `json:"action"`
	Status      string              `json:"status"`
	SessionID   string              `json:"session_id,omitempty"`
	Paths       []string            `json:"paths,omitempty"`
	Changes     []watchPathChange   `json:"changes,omitempty"`
	HookReports []hooks.RunReport   `json:"hook_reports,omitempty"`
	Sessions    []sessionWatchPaths `json:"sessions,omitempty"`
}

type sessionWatchPaths struct {
	SessionID string   `json:"session_id"`
	Paths     []string `json:"paths"`
}

type watchPathChange struct {
	Path      string `json:"path"`
	Operation string `json:"operation"`
}

type hookEventHealth struct {
	Event      string `json:"event"`
	Configured int    `json:"configured"`
}

type hookCommandSummary struct {
	Matcher string `json:"matcher,omitempty"`
	Type    string `json:"type,omitempty"`
	If      string `json:"if,omitempty"`
	Command string `json:"command"`
}

func (a *App) Hooks(ctx context.Context, args []string) error {
	req, err := parseHooksArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "list":
		return a.hooksList(req)
	case "watch-paths":
		return a.hooksWatchPaths(ctx, req)
	case "health":
		return a.hooksHealth(req)
	case "run":
		return a.hooksRun(ctx, req)
	default:
		return unexpectedExtraArgsError{
			Command: "hooks",
			Args:    []string{req.Action},
			Usage:   "codog hooks [list|health|run|watch-paths] [ARGS...] [--json|--output-format text|json]",
		}
	}
}

func (a *App) hooksList(req hooksRequest) error {
	report := hooksListReport{
		Kind:                       "hooks",
		Action:                     "list",
		Status:                     hooksStatus(a.Config),
		Disabled:                   a.Config.EffectiveDisableAllHooks(),
		ManagedOnly:                a.Config.EffectiveAllowManagedHooksOnly(),
		PreToolUse:                 append([]string(nil), a.Config.Hooks.PreToolUse...),
		PostToolUse:                append([]string(nil), a.Config.Hooks.PostToolUse...),
		PostToolUseFailure:         append([]string(nil), a.Config.Hooks.PostToolUseFailure...),
		PermissionRequest:          append([]string(nil), a.Config.Hooks.PermissionRequest...),
		PermissionDenied:           append([]string(nil), a.Config.Hooks.PermissionDenied...),
		UserPromptSubmit:           append([]string(nil), a.Config.Hooks.UserPromptSubmit...),
		SessionStart:               append([]string(nil), a.Config.Hooks.SessionStart...),
		SessionEnd:                 append([]string(nil), a.Config.Hooks.SessionEnd...),
		Setup:                      append([]string(nil), a.Config.Hooks.Setup...),
		Stop:                       append([]string(nil), a.Config.Hooks.Stop...),
		StopFailure:                append([]string(nil), a.Config.Hooks.StopFailure...),
		PreCompact:                 append([]string(nil), a.Config.Hooks.PreCompact...),
		PostCompact:                append([]string(nil), a.Config.Hooks.PostCompact...),
		Notification:               append([]string(nil), a.Config.Hooks.Notification...),
		SubagentStart:              append([]string(nil), a.Config.Hooks.SubagentStart...),
		SubagentStop:               append([]string(nil), a.Config.Hooks.SubagentStop...),
		WorktreeCreate:             append([]string(nil), a.Config.Hooks.WorktreeCreate...),
		WorktreeRemove:             append([]string(nil), a.Config.Hooks.WorktreeRemove...),
		CwdChanged:                 append([]string(nil), a.Config.Hooks.CwdChanged...),
		TaskCreated:                append([]string(nil), a.Config.Hooks.TaskCreated...),
		TaskCompleted:              append([]string(nil), a.Config.Hooks.TaskCompleted...),
		InstructionsLoaded:         append([]string(nil), a.Config.Hooks.InstructionsLoaded...),
		FileChanged:                append([]string(nil), a.Config.Hooks.FileChanged...),
		PreToolUseCommands:         hookCommandsForList(a.Config.Hooks.PreToolUseCommands, a.Config.Hooks.PreToolUse),
		PostToolUseCommands:        hookCommandsForList(a.Config.Hooks.PostToolUseCommands, a.Config.Hooks.PostToolUse),
		PostToolUseFailureCommands: hookCommandsForList(a.Config.Hooks.PostToolUseFailureCommands, a.Config.Hooks.PostToolUseFailure),
		PermissionRequestCommands:  hookCommandsForList(a.Config.Hooks.PermissionRequestCommands, a.Config.Hooks.PermissionRequest),
		PermissionDeniedCommands:   hookCommandsForList(a.Config.Hooks.PermissionDeniedCommands, a.Config.Hooks.PermissionDenied),
		UserPromptSubmitCommands:   hookCommandsForList(a.Config.Hooks.UserPromptSubmitCommands, a.Config.Hooks.UserPromptSubmit),
		SessionStartCommands:       hookCommandsForList(a.Config.Hooks.SessionStartCommands, a.Config.Hooks.SessionStart),
		SessionEndCommands:         hookCommandsForList(a.Config.Hooks.SessionEndCommands, a.Config.Hooks.SessionEnd),
		SetupCommands:              hookCommandsForList(a.Config.Hooks.SetupCommands, a.Config.Hooks.Setup),
		StopCommands:               hookCommandsForList(a.Config.Hooks.StopCommands, a.Config.Hooks.Stop),
		StopFailureCommands:        hookCommandsForList(a.Config.Hooks.StopFailureCommands, a.Config.Hooks.StopFailure),
		PreCompactCommands:         hookCommandsForList(a.Config.Hooks.PreCompactCommands, a.Config.Hooks.PreCompact),
		PostCompactCommands:        hookCommandsForList(a.Config.Hooks.PostCompactCommands, a.Config.Hooks.PostCompact),
		NotificationCommands:       hookCommandsForList(a.Config.Hooks.NotificationCommands, a.Config.Hooks.Notification),
		SubagentStartCommands:      hookCommandsForList(a.Config.Hooks.SubagentStartCommands, a.Config.Hooks.SubagentStart),
		SubagentStopCommands:       hookCommandsForList(a.Config.Hooks.SubagentStopCommands, a.Config.Hooks.SubagentStop),
		WorktreeCreateCommands:     hookCommandsForList(a.Config.Hooks.WorktreeCreateCommands, a.Config.Hooks.WorktreeCreate),
		WorktreeRemoveCommands:     hookCommandsForList(a.Config.Hooks.WorktreeRemoveCommands, a.Config.Hooks.WorktreeRemove),
		CwdChangedCommands:         hookCommandsForList(a.Config.Hooks.CwdChangedCommands, a.Config.Hooks.CwdChanged),
		TaskCreatedCommands:        hookCommandsForList(a.Config.Hooks.TaskCreatedCommands, a.Config.Hooks.TaskCreated),
		TaskCompletedCommands:      hookCommandsForList(a.Config.Hooks.TaskCompletedCommands, a.Config.Hooks.TaskCompleted),
		InstructionsLoadedCommands: hookCommandsForList(a.Config.Hooks.InstructionsLoadedCommands, a.Config.Hooks.InstructionsLoaded),
		FileChangedCommands:        hookCommandsForList(a.Config.Hooks.FileChangedCommands, a.Config.Hooks.FileChanged),
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderHooksList(a.Out, report)
	return nil
}

func (a *App) hooksWatchPaths(ctx context.Context, req hooksRequest) error {
	report, err := a.runHooksWatchPaths(ctx, req)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
	} else {
		renderHooksWatchPaths(a.Out, report)
	}
	return err
}

func (a *App) hooksHealth(req hooksRequest) error {
	payload := hooksPayloadForHealth(req)
	matched := hooks.HooksForPayload(a.Config.Hooks, payload)
	report := buildHooksHealthReport(a.Config, a.Workspace, payload, matched)
	if a.Config.EffectiveDisableAllHooks() {
		report.Status = "disabled"
		report.Disabled = true
		report.MatchedCount = 0
		report.Matched = nil
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderHooksHealth(a.Out, report)
	return nil
}

func (a *App) hooksRun(ctx context.Context, req hooksRequest) error {
	payload := hooksPayloadForRun(req)
	hookList := hooks.HooksForPayload(a.Config.Hooks, payload)
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	runner := hooks.Runner{
		Config:                 a.Config.Hooks,
		Workspace:              a.Workspace,
		ConfigHome:             a.Config.ConfigHome,
		Timeout:                timeout,
		Disabled:               a.Config.EffectiveDisableAllHooks(),
		AllowedHTTPHookURLs:    a.Config.AllowedHTTPHookURLs,
		HTTPHookAllowedEnvVars: a.Config.HTTPHookAllowedEnvVars,
		PromptRunner:           a.hookPromptRunner(a.effectiveConfig()),
	}
	report, runErr := runner.RunHooks(ctx, hookList, payload)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
	} else {
		renderHooksRun(a.Out, report)
	}
	return runErr
}

func hooksPayloadForHealth(req hooksRequest) hooks.Payload {
	payload := hooksPayloadFromRequest(req)
	switch req.Event {
	case "notification":
		payload.NotificationType = firstNonEmpty(req.NotificationType, req.Tool, "generic")
		payload.Tool = payload.NotificationType
	case "subagent_start", "subagent_stop":
		payload.AgentType = firstNonEmpty(req.AgentType, req.Tool, "general")
		payload.Tool = payload.AgentType
	case "worktree_create", "worktree_remove":
		payload.WorktreeID = firstNonEmpty(req.WorktreeID, req.Tool)
		payload.Tool = payload.WorktreeID
	case "cwd_changed":
		payload.NewCWD = firstNonEmpty(req.NewCWD, req.Tool)
		payload.Tool = payload.NewCWD
	case "task_created", "task_completed":
		payload.TaskID = firstNonEmpty(req.TaskID, req.Tool)
		payload.TaskKind = firstNonEmpty(req.TaskKind, req.AgentType, "background")
		payload.Tool = payload.TaskKind
	case "file_changed":
		payload.Operation = firstNonEmpty(req.Operation, req.Tool, "write_file")
		payload.Tool = payload.Operation
		payload.ToolName = payload.Operation
	case "instructions_loaded":
		payload.LoadReason = firstNonEmpty(req.LoadReason, req.Tool, "session_start")
		payload.Tool = payload.LoadReason
		payload.MemoryType = firstNonEmpty(req.MemoryType, "Project")
	}
	return payload
}

func buildHooksHealthReport(cfg config.Config, workspace string, payload hooks.Payload, matched []config.HookCommand) hooksHealthReport {
	events := make([]hookEventHealth, 0, len(allHookEvents()))
	configured := 0
	for _, event := range allHookEvents() {
		count := hookConfiguredCount(cfg.Hooks, event)
		configured += count
		events = append(events, hookEventHealth{Event: event, Configured: count})
	}
	return hooksHealthReport{
		Kind:            "hooks",
		Action:          "health",
		Status:          "ok",
		ManagedOnly:     cfg.EffectiveAllowManagedHooksOnly(),
		Workspace:       workspace,
		Event:           payload.Event,
		MatcherTarget:   hookMatcherTarget(payload),
		ConfiguredCount: configured,
		MatchedCount:    len(matched),
		Matched:         summarizeHookCommands(matched),
		Events:          events,
	}
}

func hooksStatus(cfg config.Config) string {
	if cfg.EffectiveDisableAllHooks() {
		return "disabled"
	}
	return "ok"
}

func hookMatcherTarget(payload hooks.Payload) string {
	switch payload.Event {
	case "notification":
		if strings.TrimSpace(payload.NotificationType) != "" {
			return payload.NotificationType
		}
	case "subagent_start", "subagent_stop":
		if strings.TrimSpace(payload.AgentType) != "" {
			return payload.AgentType
		}
	}
	return payload.Tool
}

func allHookEvents() []string {
	return []string{
		"pre_tool_use",
		"post_tool_use",
		"post_tool_use_failure",
		"permission_request",
		"permission_denied",
		"user_prompt_submit",
		"session_start",
		"session_end",
		"setup",
		"stop",
		"stop_failure",
		"pre_compact",
		"post_compact",
		"notification",
		"subagent_start",
		"subagent_stop",
		"worktree_create",
		"worktree_remove",
		"cwd_changed",
		"task_created",
		"task_completed",
		"instructions_loaded",
		"file_changed",
	}
}

func hookConfiguredCount(cfg config.HookConfig, event string) int {
	switch event {
	case "pre_tool_use":
		return len(hookCommandsForList(cfg.PreToolUseCommands, cfg.PreToolUse))
	case "post_tool_use":
		return len(hookCommandsForList(cfg.PostToolUseCommands, cfg.PostToolUse))
	case "post_tool_use_failure":
		return len(hookCommandsForList(cfg.PostToolUseFailureCommands, cfg.PostToolUseFailure))
	case "permission_request":
		return len(hookCommandsForList(cfg.PermissionRequestCommands, cfg.PermissionRequest))
	case "permission_denied":
		return len(hookCommandsForList(cfg.PermissionDeniedCommands, cfg.PermissionDenied))
	case "user_prompt_submit":
		return len(hookCommandsForList(cfg.UserPromptSubmitCommands, cfg.UserPromptSubmit))
	case "session_start":
		return len(hookCommandsForList(cfg.SessionStartCommands, cfg.SessionStart))
	case "session_end":
		return len(hookCommandsForList(cfg.SessionEndCommands, cfg.SessionEnd))
	case "setup":
		return len(hookCommandsForList(cfg.SetupCommands, cfg.Setup))
	case "stop":
		return len(hookCommandsForList(cfg.StopCommands, cfg.Stop))
	case "stop_failure":
		return len(hookCommandsForList(cfg.StopFailureCommands, cfg.StopFailure))
	case "pre_compact":
		return len(hookCommandsForList(cfg.PreCompactCommands, cfg.PreCompact))
	case "post_compact":
		return len(hookCommandsForList(cfg.PostCompactCommands, cfg.PostCompact))
	case "notification":
		return len(hookCommandsForList(cfg.NotificationCommands, cfg.Notification))
	case "subagent_start":
		return len(hookCommandsForList(cfg.SubagentStartCommands, cfg.SubagentStart))
	case "subagent_stop":
		return len(hookCommandsForList(cfg.SubagentStopCommands, cfg.SubagentStop))
	case "worktree_create":
		return len(hookCommandsForList(cfg.WorktreeCreateCommands, cfg.WorktreeCreate))
	case "worktree_remove":
		return len(hookCommandsForList(cfg.WorktreeRemoveCommands, cfg.WorktreeRemove))
	case "cwd_changed":
		return len(hookCommandsForList(cfg.CwdChangedCommands, cfg.CwdChanged))
	case "task_created":
		return len(hookCommandsForList(cfg.TaskCreatedCommands, cfg.TaskCreated))
	case "task_completed":
		return len(hookCommandsForList(cfg.TaskCompletedCommands, cfg.TaskCompleted))
	case "instructions_loaded":
		return len(hookCommandsForList(cfg.InstructionsLoadedCommands, cfg.InstructionsLoaded))
	case "file_changed":
		return len(hookCommandsForList(cfg.FileChangedCommands, cfg.FileChanged))
	default:
		return 0
	}
}

func renderHooksHealth(out io.Writer, report hooksHealthReport) {
	fmt.Fprintln(out, "Hooks Health")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	if report.Disabled {
		fmt.Fprintf(out, "  Disabled         true\n")
	}
	if report.ManagedOnly {
		fmt.Fprintf(out, "  Managed only     true\n")
	}
	fmt.Fprintf(out, "  Workspace        %s\n", emptyAsNone(report.Workspace))
	fmt.Fprintf(out, "  Event            %s\n", report.Event)
	fmt.Fprintf(out, "  Matcher target   %s\n", emptyAsNone(report.MatcherTarget))
	fmt.Fprintf(out, "  Configured       %d\n", report.ConfiguredCount)
	fmt.Fprintf(out, "  Matched          %d\n", report.MatchedCount)
	for _, event := range report.Events {
		if event.Configured > 0 {
			fmt.Fprintf(out, "  Event hook       %s=%d\n", event.Event, event.Configured)
		}
	}
	for _, hook := range report.Matched {
		fmt.Fprintf(out, "  Match            %s", hook.Command)
		if hook.Matcher != "" {
			fmt.Fprintf(out, " matcher=%s", hook.Matcher)
		}
		if hook.Type != "" {
			fmt.Fprintf(out, " type=%s", hook.Type)
		}
		if hook.If != "" {
			fmt.Fprintf(out, " if=%s", hook.If)
		}
		fmt.Fprintln(out)
	}
}

func parseHooksArgs(args []string) (hooksRequest, error) {
	return newHooksArgParser().parse(args)
}

func normalizeHookEvent(value string) (string, error) {
	return hooks.NormalizeHookEvent(value)
}

const hooksUsage = "codog hooks [list|show|health|status|match|matches|diagnose|run|test|watch-paths|watchpaths|watch] [ARGS...] [--json|--output-format text|json]"

var hooksActionCandidates = []string{"list", "show", "health", "status", "match", "matches", "diagnose", "run", "test", "watch-paths", "watchpaths", "watch"}

var hooksWatchPathsActionCandidates = []string{"list", "show", "check", "scan"}

func summarizeHookCommands(commands []config.HookCommand) []hookCommandSummary {
	out := make([]hookCommandSummary, 0, len(commands))
	for _, command := range commands {
		display := config.HookCommandDisplay(command)
		if display == "" {
			continue
		}
		out = append(out, hookCommandSummary{
			Matcher: strings.TrimSpace(command.Matcher),
			Type:    strings.TrimSpace(command.Type),
			If:      strings.TrimSpace(command.If),
			Command: display,
		})
	}
	return out
}

func hookCommandsForList(commands []config.HookCommand, legacy []string) []hookCommandSummary {
	summaries := summarizeHookCommands(commands)
	if len(summaries) != 0 || len(legacy) == 0 {
		return summaries
	}
	out := make([]hookCommandSummary, 0, len(legacy))
	for _, command := range legacy {
		command = strings.TrimSpace(command)
		if command != "" {
			out = append(out, hookCommandSummary{Command: command})
		}
	}
	return out
}

func renderHooksList(out io.Writer, report hooksListReport) {
	fmt.Fprintln(out, "Hooks")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	if report.Disabled {
		fmt.Fprintf(out, "  Disabled         true\n")
	}
	if report.ManagedOnly {
		fmt.Fprintf(out, "  Managed only     true\n")
	}
	fmt.Fprintf(out, "  Pre tool use     %d\n", len(report.PreToolUse))
	for _, command := range report.PreToolUseCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Post tool use    %d\n", len(report.PostToolUse))
	for _, command := range report.PostToolUseCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Post tool failure %d\n", len(report.PostToolUseFailure))
	for _, command := range report.PostToolUseFailureCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Permission request %d\n", len(report.PermissionRequest))
	for _, command := range report.PermissionRequestCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Permission denied %d\n", len(report.PermissionDenied))
	for _, command := range report.PermissionDeniedCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  User prompt submit %d\n", len(report.UserPromptSubmit))
	for _, command := range report.UserPromptSubmitCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Session start   %d\n", len(report.SessionStart))
	for _, command := range report.SessionStartCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Session end     %d\n", len(report.SessionEnd))
	for _, command := range report.SessionEndCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Setup           %d\n", len(report.Setup))
	for _, command := range report.SetupCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Stop             %d\n", len(report.Stop))
	for _, command := range report.StopCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Stop failure     %d\n", len(report.StopFailure))
	for _, command := range report.StopFailureCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Pre compact      %d\n", len(report.PreCompact))
	for _, command := range report.PreCompactCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Post compact     %d\n", len(report.PostCompact))
	for _, command := range report.PostCompactCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Notification     %d\n", len(report.Notification))
	for _, command := range report.NotificationCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Subagent start   %d\n", len(report.SubagentStart))
	for _, command := range report.SubagentStartCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Subagent stop    %d\n", len(report.SubagentStop))
	for _, command := range report.SubagentStopCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Worktree create  %d\n", len(report.WorktreeCreate))
	for _, command := range report.WorktreeCreateCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Worktree remove  %d\n", len(report.WorktreeRemove))
	for _, command := range report.WorktreeRemoveCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Cwd changed      %d\n", len(report.CwdChanged))
	for _, command := range report.CwdChangedCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Task created     %d\n", len(report.TaskCreated))
	for _, command := range report.TaskCreatedCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Task completed   %d\n", len(report.TaskCompleted))
	for _, command := range report.TaskCompletedCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Instructions loaded %d\n", len(report.InstructionsLoaded))
	for _, command := range report.InstructionsLoadedCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  File changed     %d\n", len(report.FileChanged))
	for _, command := range report.FileChangedCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
}

func renderHookCommandSummary(command hookCommandSummary) string {
	var labels []string
	if strings.TrimSpace(command.Matcher) != "" {
		labels = append(labels, strings.TrimSpace(command.Matcher))
	}
	if strings.TrimSpace(command.If) != "" {
		labels = append(labels, "if "+strings.TrimSpace(command.If))
	}
	if len(labels) == 0 {
		return command.Command
	}
	return fmt.Sprintf("[%s] %s", strings.Join(labels, ", "), command.Command)
}

func renderHooksRun(out io.Writer, report hooks.RunReport) {
	fmt.Fprintln(out, "Hook Run")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	if report.Disabled {
		fmt.Fprintf(out, "  Disabled         true\n")
	}
	fmt.Fprintf(out, "  Event            %s\n", report.Event)
	fmt.Fprintf(out, "  Tool             %s\n", report.Tool)
	fmt.Fprintf(out, "  Commands         %d\n", report.Count)
	for _, result := range report.Results {
		name := result.Command
		if result.Type == "http" && result.URL != "" {
			name = result.URL
		}
		fmt.Fprintf(out, "  %s success=%t duration_ms=%d\n", name, result.Success, result.DurationMS)
		if result.StatusCode != 0 {
			fmt.Fprintf(out, "    status: %d\n", result.StatusCode)
		}
		if result.Denied {
			fmt.Fprintf(out, "    denied: true\n")
		}
		for _, message := range result.Messages {
			if strings.TrimSpace(message) != "" {
				fmt.Fprintf(out, "    message: %s\n", strings.ReplaceAll(strings.TrimSpace(message), "\n", "\n             "))
			}
		}
		if strings.TrimSpace(result.Stdout) != "" {
			fmt.Fprintf(out, "    stdout: %s\n", strings.ReplaceAll(strings.TrimSpace(result.Stdout), "\n", "\n            "))
		}
		if strings.TrimSpace(result.Stderr) != "" {
			fmt.Fprintf(out, "    stderr: %s\n", strings.ReplaceAll(strings.TrimSpace(result.Stderr), "\n", "\n            "))
		}
		if result.Error != "" {
			fmt.Fprintf(out, "    error: %s\n", result.Error)
		}
	}
}
