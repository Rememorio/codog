package oauth

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
)

func RefreshToken(ctx context.Context, metadata ProviderMetadata, clientID string, refreshToken string) (Token, error) {
	if metadata.TokenEndpoint == "" {
		return Token{}, errors.New("token endpoint is required")
	}
	if strings.TrimSpace(clientID) == "" {
		return Token{}, errors.New("client id is required")
	}
	if strings.TrimSpace(refreshToken) == "" {
		return Token{}, errors.New("refresh token is required")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)
	var response tokenEndpointResponse
	if err := postFormJSON(ctx, metadata.TokenEndpoint, form, &response); err != nil {
		return Token{}, err
	}
	if response.AccessToken == "" {
		return Token{}, errors.New("token response is missing access_token")
	}
	now := time.Now().UTC()
	next := Token{
		AccessToken:  response.AccessToken,
		RefreshToken: response.RefreshToken,
		TokenType:    response.TokenType,
		CreatedAt:    now,
	}
	if next.RefreshToken == "" {
		next.RefreshToken = refreshToken
	}
	if next.TokenType == "" {
		next.TokenType = "Bearer"
	}
	if response.ExpiresIn > 0 {
		next.ExpiresAt = now.Add(time.Duration(response.ExpiresIn) * time.Second)
	}
	return next, nil
}

func RefreshStoredToken(ctx context.Context, configHome string, profileName string) (Token, error) {
	current, err := LoadToken(configHome)
	if err != nil {
		return Token{}, err
	}
	if current.RefreshToken == "" {
		return Token{}, errors.New("stored token does not include a refresh token")
	}
	profile, err := ResolveProviderProfile(configHome, profileName)
	if err != nil {
		return Token{}, err
	}
	refreshed, err := RefreshToken(ctx, profile.Metadata, profile.ClientID, current.RefreshToken)
	if err != nil {
		return Token{}, err
	}
	return SaveToken(configHome, refreshed)
}
