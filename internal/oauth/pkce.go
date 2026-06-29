package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

type PKCE struct {
	CodeVerifier  string `json:"code_verifier"`
	CodeChallenge string `json:"code_challenge"`
	Method        string `json:"method"`
}

func GeneratePKCE() (PKCE, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return PKCE{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{
		CodeVerifier:  verifier,
		CodeChallenge: base64.RawURLEncoding.EncodeToString(sum[:]),
		Method:        "S256",
	}, nil
}
