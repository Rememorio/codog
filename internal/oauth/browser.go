package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type BrowserAuthorization struct {
	AuthorizationURL string `json:"authorization_url"`
	RedirectURI      string `json:"redirect_uri"`
	State            string `json:"state"`
	CodeVerifier     string `json:"code_verifier"`
	CodeChallenge    string `json:"code_challenge"`
	Method           string `json:"method"`
}

type BrowserCallback struct {
	Code  string `json:"code"`
	State string `json:"state,omitempty"`
}

type BrowserCallbackResult struct {
	Callback BrowserCallback
	Err      error
}

type BrowserCallbackServer struct {
	RedirectURI string
	Results     <-chan BrowserCallbackResult
	Close       func() error
}

func BuildBrowserAuthorization(metadata ProviderMetadata, clientID, redirectURI string, scopes []string, state string, pkce PKCE) (BrowserAuthorization, error) {
	if metadata.AuthorizationEndpoint == "" {
		return BrowserAuthorization{}, errors.New("authorization endpoint is required")
	}
	if strings.TrimSpace(clientID) == "" {
		return BrowserAuthorization{}, errors.New("client id is required")
	}
	if strings.TrimSpace(redirectURI) == "" {
		return BrowserAuthorization{}, errors.New("redirect uri is required")
	}
	if pkce.CodeVerifier == "" || pkce.CodeChallenge == "" {
		next, err := GeneratePKCE()
		if err != nil {
			return BrowserAuthorization{}, err
		}
		pkce = next
	}
	if state == "" {
		generated, err := GenerateState()
		if err != nil {
			return BrowserAuthorization{}, err
		}
		state = generated
	}
	endpoint, err := url.Parse(metadata.AuthorizationEndpoint)
	if err != nil {
		return BrowserAuthorization{}, err
	}
	query := endpoint.Query()
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("state", state)
	query.Set("code_challenge", pkce.CodeChallenge)
	query.Set("code_challenge_method", pkce.Method)
	if len(scopes) != 0 {
		query.Set("scope", strings.Join(scopes, " "))
	}
	endpoint.RawQuery = query.Encode()
	return BrowserAuthorization{
		AuthorizationURL: endpoint.String(),
		RedirectURI:      redirectURI,
		State:            state,
		CodeVerifier:     pkce.CodeVerifier,
		CodeChallenge:    pkce.CodeChallenge,
		Method:           pkce.Method,
	}, nil
}

func ExchangeAuthorizationCode(ctx context.Context, metadata ProviderMetadata, clientID, code, codeVerifier, redirectURI string) (Token, error) {
	if metadata.TokenEndpoint == "" {
		return Token{}, errors.New("token endpoint is required")
	}
	if strings.TrimSpace(clientID) == "" {
		return Token{}, errors.New("client id is required")
	}
	if strings.TrimSpace(code) == "" {
		return Token{}, errors.New("authorization code is required")
	}
	if strings.TrimSpace(codeVerifier) == "" {
		return Token{}, errors.New("code verifier is required")
	}
	if strings.TrimSpace(redirectURI) == "" {
		return Token{}, errors.New("redirect uri is required")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", clientID)
	form.Set("code_verifier", codeVerifier)
	form.Set("redirect_uri", redirectURI)
	var response tokenEndpointResponse
	if err := postFormJSON(ctx, metadata.TokenEndpoint, form, &response); err != nil {
		return Token{}, err
	}
	if response.AccessToken == "" {
		return Token{}, errors.New("token response is missing access_token")
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
	return token, nil
}

func StartBrowserCallbackServer(ctx context.Context, addr, path, expectedState string) (BrowserCallbackServer, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	if path == "" {
		path = "/oauth/callback"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return BrowserCallbackServer{}, err
	}
	results := make(chan BrowserCallbackResult, 1)
	var once sync.Once
	send := func(result BrowserCallbackResult) {
		once.Do(func() { results <- result })
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if value := query.Get("error"); value != "" {
			description := query.Get("error_description")
			http.Error(w, value, http.StatusBadRequest)
			send(BrowserCallbackResult{Err: endpointError{Code: value, Description: description, Status: http.StatusBadRequest}})
			return
		}
		state := query.Get("state")
		if expectedState != "" && state != expectedState {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			send(BrowserCallbackResult{Err: errors.New("oauth state mismatch")})
			return
		}
		code := query.Get("code")
		if code == "" {
			http.Error(w, "authorization code is required", http.StatusBadRequest)
			send(BrowserCallbackResult{Err: errors.New("authorization code is required")})
			return
		}
		w.Header().Set("content-type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Codog authorization complete. You can close this window.\n"))
		send(BrowserCallbackResult{Callback: BrowserCallback{Code: code, State: state}})
	})
	server := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		send(BrowserCallbackResult{Err: ctx.Err()})
		_ = server.Shutdown(context.Background())
	}()
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			send(BrowserCallbackResult{Err: err})
		}
	}()
	return BrowserCallbackServer{
		RedirectURI: "http://" + listener.Addr().String() + path,
		Results:     results,
		Close: func() error {
			return server.Shutdown(context.Background())
		},
	}, nil
}

func GenerateState() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
