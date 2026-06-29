package oauth

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var ErrNoToken = errors.New("no oauth token")

var (
	securityLookPath = exec.LookPath
	securityRun      = func(args ...string) ([]byte, error) {
		return exec.Command("security", args...).CombinedOutput()
	}
)

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
	return tokenStoreFor(configHome).Save(token)
}

func LoadToken(configHome string) (Token, error) {
	return tokenStoreFor(configHome).Load()
}

func DeleteToken(configHome string) error {
	return tokenStoreFor(configHome).Delete()
}

type tokenStore interface {
	Save(Token) (Token, error)
	Load() (Token, error)
	Delete() error
}

type fileTokenStore struct {
	path string
}

func (s fileTokenStore) Save(token Token) (Token, error) {
	if token.AccessToken == "" {
		return Token{}, errors.New("access token is required")
	}
	if token.TokenType == "" {
		token.TokenType = "Bearer"
	}
	if token.CreatedAt.IsZero() {
		token.CreatedAt = time.Now().UTC()
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return Token{}, err
	}
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return Token{}, err
	}
	if err := os.WriteFile(s.path, append(data, '\n'), 0o600); err != nil {
		return Token{}, err
	}
	return token, nil
}

func (s fileTokenStore) Load() (Token, error) {
	data, err := os.ReadFile(s.path)
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

func (s fileTokenStore) Delete() error {
	err := os.Remove(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

type keychainTokenStore struct {
	fallback fileTokenStore
	service  string
	account  string
}

func (s keychainTokenStore) Save(token Token) (Token, error) {
	normalized, err := normalizeToken(token)
	if err != nil {
		return Token{}, err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return Token{}, err
	}
	if _, err := securityRun("add-generic-password", "-a", s.account, "-s", s.service, "-w", string(data), "-U"); err != nil {
		return s.fallback.Save(normalized)
	}
	_ = s.fallback.Delete()
	return normalized, nil
}

func (s keychainTokenStore) Load() (Token, error) {
	data, err := securityRun("find-generic-password", "-a", s.account, "-s", s.service, "-w")
	if err != nil {
		return s.fallback.Load()
	}
	var token Token
	if err := json.Unmarshal(bytesTrimSpace(data), &token); err != nil {
		return Token{}, err
	}
	if token.AccessToken == "" {
		return Token{}, ErrNoToken
	}
	return token, nil
}

func (s keychainTokenStore) Delete() error {
	_, keychainErr := securityRun("delete-generic-password", "-a", s.account, "-s", s.service)
	fileErr := s.fallback.Delete()
	if keychainErr != nil && fileErr != nil {
		return keychainErr
	}
	return fileErr
}

func tokenStoreFor(configHome string) tokenStore {
	fileStore := fileTokenStore{path: tokenPath(configHome)}
	mode := strings.ToLower(os.Getenv("CODOG_OAUTH_STORAGE"))
	if mode == "file" {
		return fileStore
	}
	if mode == "keychain" || shouldUseKeychain(configHome) {
		if _, err := securityLookPath("security"); err == nil {
			return keychainTokenStore{
				fallback: fileStore,
				service:  "codog.oauth",
				account:  "default",
			}
		}
	}
	return fileStore
}

func shouldUseKeychain(configHome string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	abs, err := filepath.Abs(configHome)
	if err != nil {
		return false
	}
	temp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(temp, abs)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}

func normalizeToken(token Token) (Token, error) {
	if token.AccessToken == "" {
		return Token{}, errors.New("access token is required")
	}
	if token.TokenType == "" {
		token.TokenType = "Bearer"
	}
	if token.CreatedAt.IsZero() {
		token.CreatedAt = time.Now().UTC()
	}
	return token, nil
}

func bytesTrimSpace(data []byte) []byte {
	return []byte(strings.TrimSpace(string(data)))
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
