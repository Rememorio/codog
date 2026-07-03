package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// VerifyEd25519 verifies payload against a base64 or hex public key and
// signature.
func VerifyEd25519(publicKeyValue, signatureValue string, payload []byte) error {
	publicKey, err := DecodePublicKey(publicKeyValue)
	if err != nil {
		return err
	}
	signature, err := DecodeSignature(signatureValue)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return fmt.Errorf("signature verification failed")
	}
	return nil
}

// DecodePublicKey decodes a base64 or hex Ed25519 public key.
func DecodePublicKey(value string) (ed25519.PublicKey, error) {
	data, err := decodeBase64OrHex(value, ed25519.PublicKeySize)
	if err != nil {
		return nil, fmt.Errorf("invalid public key: %w", err)
	}
	return ed25519.PublicKey(data), nil
}

// DecodeSignature decodes a base64 or hex Ed25519 signature, accepting an
// optional ed25519: prefix.
func DecodeSignature(value string) ([]byte, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "ed25519:")
	data, err := decodeBase64OrHex(value, ed25519.SignatureSize)
	if err != nil {
		return nil, fmt.Errorf("invalid signature: %w", err)
	}
	return data, nil
}

func decodeBase64OrHex(value string, expectedLen int) ([]byte, error) {
	value = strings.TrimSpace(value)
	var firstErr error
	for _, decode := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
		hex.DecodeString,
	} {
		data, err := decode(value)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(data) == expectedLen {
			return data, nil
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("decoded length: got %d want %d", len(data), expectedLen)
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, fmt.Errorf("decoded length: got 0 want %d", expectedLen)
}
