// Package mcpauthdiag builds recovery-oriented MCP authentication reports.
package mcpauthdiag

import (
	"context"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/oauth"
)

// NextAction is a concrete command the user or model can run to repair MCP
// authentication or validate readiness.
type NextAction struct {
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Command string `json:"command"`
}

// Report combines MCP lifecycle health with local OAuth readiness and safe
// recovery actions.
type Report struct {
	mcp.AuthStatusResult
	OAuthProfile string           `json:"oauth_profile,omitempty"`
	OAuthStatus  *oauth.Status    `json:"oauth_status,omitempty"`
	NextActions  []NextAction     `json:"next_actions,omitempty"`
	Refreshed    bool             `json:"refreshed,omitempty"`
	RefreshError string           `json:"refresh_error,omitempty"`
	Token        *oauth.TokenView `json:"token,omitempty"`
}

// Build returns a recovery-oriented report without mutating local state.
func Build(result mcp.AuthStatusResult, configHome string, profileName string, now time.Time) Report {
	report := Report{AuthStatusResult: result}
	report.attachOAuth(configHome, profileName, now)
	report.NextActions = nextActions(report, strings.TrimSpace(profileName))
	return report
}

// Refresh refreshes the saved OAuth token when possible, then returns a fresh
// diagnostic report. The token view is redacted.
func Refresh(ctx context.Context, result mcp.AuthStatusResult, configHome string, profileName string, now time.Time) Report {
	report := Build(result, configHome, profileName, now)
	if report.OAuthStatus == nil {
		report.RefreshError = "oauth status is unavailable"
		report.NextActions = nextActions(report, strings.TrimSpace(profileName))
		return report
	}
	if !report.OAuthStatus.CanRefresh {
		if report.OAuthStatus.Issue != "" {
			report.RefreshError = report.OAuthStatus.Issue
		} else {
			report.RefreshError = "saved oauth token cannot be refreshed"
		}
		report.NextActions = nextActions(report, strings.TrimSpace(profileName))
		return report
	}
	token, err := oauth.RefreshStoredToken(ctx, configHome, profileName)
	if err != nil {
		report.RefreshError = err.Error()
		report.NextActions = nextActions(report, strings.TrimSpace(profileName))
		return report
	}
	view := token.View(now)
	report.Refreshed = true
	report.Token = &view
	report.attachOAuth(configHome, profileName, now)
	report.NextActions = nextActions(report, strings.TrimSpace(profileName))
	return report
}

func (r *Report) attachOAuth(configHome string, profileName string, now time.Time) {
	profileName = strings.TrimSpace(profileName)
	if profileName != "" {
		r.OAuthProfile = profileName
	}
	if strings.TrimSpace(configHome) == "" && profileName == "" {
		return
	}
	status := oauth.InspectStatus(configHome, profileName, now)
	r.OAuthStatus = &status
	if r.OAuthProfile == "" {
		r.OAuthProfile = status.ProfileName
	}
}

func nextActions(report Report, requestedProfile string) []NextAction {
	actions := []NextAction{}
	server := strings.TrimSpace(report.Server)
	profile := strings.TrimSpace(report.OAuthProfile)
	if profile == "" {
		profile = strings.TrimSpace(requestedProfile)
	}
	if profile == "" {
		profile = "default"
	}
	add := func(kind string, label string, command string) {
		if command == "" {
			return
		}
		for _, existing := range actions {
			if existing.Command == command {
				return
			}
		}
		actions = append(actions, NextAction{Kind: kind, Label: label, Command: command})
	}
	if report.Error != "" || report.ResourceError != "" {
		if server != "" {
			add("inspect", "Inspect MCP server configuration", "codog mcp show "+server)
			add("retry", "Retry MCP authentication check", "codog mcp auth "+server)
		} else {
			add("inspect", "List configured MCP servers", "codog mcp list")
		}
	}
	if report.OAuthStatus == nil {
		add("oauth_status", "Inspect OAuth status", "codog oauth status")
		return actions
	}
	status := report.OAuthStatus
	switch {
	case !status.ProfileConfigured:
		add("oauth_provider", "Configure an OAuth provider profile", "codog oauth provider save "+profile+" ISSUER_URL CLIENT_ID [SCOPE...]")
		add("oauth_login", "Complete browser OAuth login", "codog oauth browser login "+profile)
	case !status.TokenPresent:
		add("oauth_login", "Complete browser OAuth login", "codog oauth browser login "+profile)
		add("oauth_device_login", "Complete device OAuth login", "codog oauth device login "+profile)
	case status.Expired && status.CanRefresh:
		if server != "" {
			add("mcp_auth_refresh", "Refresh OAuth token and retry MCP auth", "codog mcp auth --refresh "+server)
		}
		add("oauth_refresh", "Refresh the saved OAuth token", "codog oauth token refresh "+profile)
	case status.Expired:
		add("oauth_login", "Renew OAuth login", "codog oauth browser login "+profile)
		add("oauth_device_login", "Renew OAuth login with device flow", "codog oauth device login "+profile)
	}
	if server != "" && report.Error == "" && report.ResourceError == "" {
		add("verify", "Verify MCP tools are discoverable", "codog mcp tools "+server)
	}
	return actions
}
