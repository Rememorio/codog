package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const DeviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"

type DeviceAuthorization struct {
	DeviceCode              string    `json:"device_code"`
	UserCode                string    `json:"user_code"`
	VerificationURI         string    `json:"verification_uri"`
	VerificationURIComplete string    `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int       `json:"expires_in"`
	Interval                int       `json:"interval,omitempty"`
	Message                 string    `json:"message,omitempty"`
	ExpiresAt               time.Time `json:"expires_at,omitempty"`
}

type DevicePollOptions struct {
	ClientID  string
	Interval  time.Duration
	ExpiresAt time.Time
	Now       func() time.Time
	Sleep     func(context.Context, time.Duration) error
}

func StartDeviceAuthorization(ctx context.Context, metadata ProviderMetadata, clientID string, scopes []string) (DeviceAuthorization, error) {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return DeviceAuthorization{}, errors.New("client id is required")
	}
	if metadata.DeviceAuthorizationEndpoint == "" {
		return DeviceAuthorization{}, errors.New("device authorization endpoint is required")
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	if len(scopes) != 0 {
		form.Set("scope", strings.Join(scopes, " "))
	}
	var auth DeviceAuthorization
	if err := postFormJSON(ctx, metadata.DeviceAuthorizationEndpoint, form, &auth); err != nil {
		return DeviceAuthorization{}, err
	}
	if auth.DeviceCode == "" || auth.UserCode == "" || auth.VerificationURI == "" {
		return DeviceAuthorization{}, errors.New("device authorization response is missing required fields")
	}
	if auth.Interval <= 0 {
		auth.Interval = 5
	}
	if auth.ExpiresIn > 0 {
		auth.ExpiresAt = time.Now().UTC().Add(time.Duration(auth.ExpiresIn) * time.Second)
	}
	return auth, nil
}

func PollDeviceToken(ctx context.Context, metadata ProviderMetadata, deviceCode string, options DevicePollOptions) (Token, error) {
	if metadata.TokenEndpoint == "" {
		return Token{}, errors.New("token endpoint is required")
	}
	if strings.TrimSpace(deviceCode) == "" {
		return Token{}, errors.New("device code is required")
	}
	if strings.TrimSpace(options.ClientID) == "" {
		return Token{}, errors.New("client id is required")
	}
	interval := options.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	sleep := options.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	for {
		if !options.ExpiresAt.IsZero() && !now().Before(options.ExpiresAt) {
			return Token{}, errors.New("device code expired")
		}
		token, retry, err := pollDeviceTokenOnce(ctx, metadata.TokenEndpoint, deviceCode, options.ClientID)
		if err == nil {
			return token, nil
		}
		if !retry {
			return Token{}, err
		}
		if errors.Is(err, errSlowDown) {
			interval += 5 * time.Second
		}
		if err := sleep(ctx, interval); err != nil {
			return Token{}, err
		}
	}
}

var errSlowDown = errors.New("slow down polling")

func pollDeviceTokenOnce(ctx context.Context, tokenEndpoint string, deviceCode string, clientID string) (Token, bool, error) {
	form := url.Values{}
	form.Set("grant_type", DeviceCodeGrantType)
	form.Set("device_code", deviceCode)
	form.Set("client_id", clientID)
	var response tokenEndpointResponse
	err := postFormJSON(ctx, tokenEndpoint, form, &response)
	if err != nil {
		var endpointErr endpointError
		if errors.As(err, &endpointErr) {
			switch endpointErr.Code {
			case "authorization_pending":
				return Token{}, true, endpointErr
			case "slow_down":
				return Token{}, true, errSlowDown
			case "access_denied", "expired_token":
				return Token{}, false, endpointErr
			}
		}
		return Token{}, false, err
	}
	if response.AccessToken == "" {
		return Token{}, false, errors.New("token response is missing access_token")
	}
	now := time.Now().UTC()
	token := Token{
		AccessToken:  response.AccessToken,
		RefreshToken: response.RefreshToken,
		TokenType:    response.TokenType,
		CreatedAt:    now,
	}
	if token.TokenType == "" {
		token.TokenType = "Bearer"
	}
	if response.ExpiresIn > 0 {
		token.ExpiresAt = now.Add(time.Duration(response.ExpiresIn) * time.Second)
	}
	return token, false, nil
}

type tokenEndpointResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
}

type endpointError struct {
	Code        string `json:"error"`
	Description string `json:"error_description,omitempty"`
	Status      int    `json:"-"`
}

func (e endpointError) Error() string {
	if e.Description != "" {
		return e.Code + ": " + e.Description
	}
	if e.Code != "" {
		return e.Code
	}
	return "oauth endpoint error"
}

func postFormJSON(ctx context.Context, endpoint string, form url.Values, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	req.Header.Set("content-length", strconv.Itoa(len(form.Encode())))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var payload endpointError
		_ = json.NewDecoder(resp.Body).Decode(&payload)
		payload.Status = resp.StatusCode
		if payload.Code == "" {
			payload.Code = fmt.Sprintf("http_%d", resp.StatusCode)
		}
		return payload
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
