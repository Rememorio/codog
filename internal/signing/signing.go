package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

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

func DecodePublicKey(value string) (ed25519.PublicKey, error) {
	data, err := decodeBase64OrHex(value)
	if err != nil {
		return nil, fmt.Errorf("invalid public key: %w", err)
	}
	if len(data) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key length: got %d want %d", len(data), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(data), nil
}

func DecodeSignature(value string) ([]byte, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "ed25519:")
	data, err := decodeBase64OrHex(value)
	if err != nil {
		return nil, fmt.Errorf("invalid signature: %w", err)
	}
	if len(data) != ed25519.SignatureSize {
		return nil, fmt.Errorf("invalid signature length: got %d want %d", len(data), ed25519.SignatureSize)
	}
	return data, nil
}

func decodeBase64OrHex(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if data, err := base64.StdEncoding.DecodeString(value); err == nil {
		return data, nil
	}
	if data, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return data, nil
	}
	if data, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return data, nil
	}
	return hex.DecodeString(value)
}
