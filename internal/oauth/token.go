package oauth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

var ErrNoToken = errors.New("no oauth token")

type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type TokenView struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	Expired      bool      `json:"expired"`
}

func SaveToken(configHome string, token Token) (Token, error) {
	if token.AccessToken == "" {
		return Token{}, errors.New("access token is required")
	}
	if token.TokenType == "" {
		token.TokenType = "Bearer"
	}
	if token.CreatedAt.IsZero() {
		token.CreatedAt = time.Now().UTC()
	}
	path := tokenPath(configHome)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Token{}, err
	}
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return Token{}, err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return Token{}, err
	}
	return token, nil
}

func LoadToken(configHome string) (Token, error) {
	data, err := os.ReadFile(tokenPath(configHome))
	if err != nil {
		if os.IsNotExist(err) {
			return Token{}, ErrNoToken
		}
		return Token{}, err
	}
	var token Token
	if err := json.Unmarshal(data, &token); err != nil {
		return Token{}, err
	}
	if token.AccessToken == "" {
		return Token{}, ErrNoToken
	}
	return token, nil
}

func DeleteToken(configHome string) error {
	err := os.Remove(tokenPath(configHome))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (t Token) Expired(now time.Time) bool {
	if t.ExpiresAt.IsZero() {
		return false
	}
	return !now.Before(t.ExpiresAt.Add(-1 * time.Minute))
}

func (t Token) View(now time.Time) TokenView {
	return TokenView{
		AccessToken:  redact(t.AccessToken),
		RefreshToken: redact(t.RefreshToken),
		TokenType:    t.TokenType,
		ExpiresAt:    t.ExpiresAt,
		CreatedAt:    t.CreatedAt,
		Expired:      t.Expired(now),
	}
}

func tokenPath(configHome string) string {
	return filepath.Join(configHome, "oauth", "token.json")
}

func redact(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "[redacted]"
	}
	return value[:4] + "..." + value[len(value)-4:]
}
