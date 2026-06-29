package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ProviderMetadata struct {
	Issuer                            string   `json:"issuer,omitempty"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint,omitempty"`
	TokenEndpoint                     string   `json:"token_endpoint,omitempty"`
	DeviceAuthorizationEndpoint       string   `json:"device_authorization_endpoint,omitempty"`
	RevocationEndpoint                string   `json:"revocation_endpoint,omitempty"`
	JWKSURI                           string   `json:"jwks_uri,omitempty"`
	ScopesSupported                   []string `json:"scopes_supported,omitempty"`
	GrantTypesSupported               []string `json:"grant_types_supported,omitempty"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported,omitempty"`
	ResponseTypesSupported            []string `json:"response_types_supported,omitempty"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	SourceURL                         string   `json:"source_url,omitempty"`
}

func DiscoverProvider(ctx context.Context, issuer string) (ProviderMetadata, error) {
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		return ProviderMetadata{}, errors.New("issuer URL is required")
	}
	base, err := url.Parse(issuer)
	if err != nil {
		return ProviderMetadata{}, err
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return ProviderMetadata{}, errors.New("issuer URL must use http or https")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	var lastErr error
	for _, candidate := range metadataURLs(base) {
		metadata, err := fetchMetadata(ctx, client, candidate)
		if err == nil {
			metadata.SourceURL = candidate
			return metadata, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("metadata was not found")
	}
	return ProviderMetadata{}, lastErr
}

func metadataURLs(base *url.URL) []string {
	normalized := *base
	normalized.RawQuery = ""
	normalized.Fragment = ""
	path := strings.TrimRight(normalized.Path, "/")
	if strings.Contains(path, "/.well-known/") {
		normalized.Path = path
		return []string{normalized.String()}
	}
	normalized.Path = path + "/.well-known/oauth-authorization-server"
	oauthURL := normalized.String()
	normalized.Path = path + "/.well-known/openid-configuration"
	openIDURL := normalized.String()
	return []string{oauthURL, openIDURL}
}

func fetchMetadata(ctx context.Context, client *http.Client, endpoint string) (ProviderMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ProviderMetadata{}, err
	}
	req.Header.Set("accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return ProviderMetadata{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ProviderMetadata{}, fmt.Errorf("metadata %s returned %s", endpoint, resp.Status)
	}
	var metadata ProviderMetadata
	if err := json.NewDecoder(resp.Body).Decode(&metadata); err != nil {
		return ProviderMetadata{}, err
	}
	if metadata.AuthorizationEndpoint == "" && metadata.TokenEndpoint == "" && metadata.DeviceAuthorizationEndpoint == "" {
		return ProviderMetadata{}, errors.New("metadata does not expose OAuth endpoints")
	}
	return metadata, nil
}
