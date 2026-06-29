package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type LogoutResult struct {
	Deleted        bool   `json:"deleted"`
	AccessRevoked  bool   `json:"access_revoked,omitempty"`
	RefreshRevoked bool   `json:"refresh_revoked,omitempty"`
	Revocation     string `json:"revocation,omitempty"`
}

func RevokeToken(ctx context.Context, metadata ProviderMetadata, clientID string, tokenValue string, hint string) error {
	if metadata.RevocationEndpoint == "" {
		return errors.New("revocation endpoint is required")
	}
	if strings.TrimSpace(tokenValue) == "" {
		return errors.New("token is required")
	}
	form := url.Values{}
	form.Set("token", tokenValue)
	if clientID != "" {
		form.Set("client_id", clientID)
	}
	if hint != "" {
		form.Set("token_type_hint", hint)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, metadata.RevocationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return endpointError{Code: "revocation_failed", Status: resp.StatusCode}
	}
	return nil
}

func Logout(ctx context.Context, configHome string, profileName string) (LogoutResult, error) {
	token, err := LoadToken(configHome)
	if err != nil {
		if errors.Is(err, ErrNoToken) {
			return LogoutResult{Deleted: true, Revocation: "no_token"}, nil
		}
		return LogoutResult{}, err
	}
	result := LogoutResult{}
	profile, err := ResolveProviderProfile(configHome, profileName)
	if err == nil && profile.Metadata.RevocationEndpoint != "" {
		if token.AccessToken != "" {
			if err := RevokeToken(ctx, profile.Metadata, profile.ClientID, token.AccessToken, "access_token"); err != nil {
				return result, err
			}
			result.AccessRevoked = true
		}
		if token.RefreshToken != "" {
			if err := RevokeToken(ctx, profile.Metadata, profile.ClientID, token.RefreshToken, "refresh_token"); err != nil {
				return result, err
			}
			result.RefreshRevoked = true
		}
		result.Revocation = "revoked"
	} else {
		result.Revocation = "unavailable"
	}
	if err := DeleteToken(configHome); err != nil {
		return result, err
	}
	result.Deleted = true
	return result, nil
}
