package oauth

import (
	"errors"
	"time"
)

type Status struct {
	ProfileName       string           `json:"profile_name,omitempty"`
	ProfileConfigured bool             `json:"profile_configured"`
	Profile           *ProviderProfile `json:"profile,omitempty"`
	TokenPresent      bool             `json:"token_present"`
	Token             *TokenView       `json:"token,omitempty"`
	Expired           bool             `json:"expired"`
	CanRefresh        bool             `json:"can_refresh"`
	Ready             bool             `json:"ready"`
	Issue             string           `json:"issue,omitempty"`
}

func InspectStatus(configHome string, profileName string, now time.Time) Status {
	status := Status{}
	profile, err := ResolveProviderProfile(configHome, profileName)
	if err == nil {
		status.ProfileName = profile.Name
		status.ProfileConfigured = true
		status.Profile = &profile
	} else if profileName != "" {
		status.ProfileName = profileName
		status.Issue = err.Error()
	}
	token, err := LoadToken(configHome)
	if err == nil {
		view := token.View(now)
		status.TokenPresent = true
		status.Token = &view
		status.Expired = view.Expired
		status.CanRefresh = token.RefreshToken != "" && status.ProfileConfigured && profile.Metadata.TokenEndpoint != "" && profile.ClientID != ""
		status.Ready = !view.Expired || status.CanRefresh
		if view.Expired && !status.CanRefresh {
			status.Issue = "token is expired and cannot be refreshed"
		}
		return status
	}
	if errors.Is(err, ErrNoToken) {
		status.Issue = "no oauth token saved"
	} else {
		status.Issue = err.Error()
	}
	return status
}
