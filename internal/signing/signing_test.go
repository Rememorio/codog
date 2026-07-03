package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerifyEd25519AcceptsBase64AndHexValues(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	payload := []byte("trusted payload")
	signature := ed25519.Sign(privateKey, payload)

	require.NoError(t, VerifyEd25519(
		base64.StdEncoding.EncodeToString(publicKey),
		"ed25519:"+base64.RawStdEncoding.EncodeToString(signature),
		payload,
	))
	require.NoError(t, VerifyEd25519(
		hex.EncodeToString(publicKey),
		hex.EncodeToString(signature),
		payload,
	))
}

func TestVerifyEd25519RejectsInvalidPayload(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signature := ed25519.Sign(privateKey, []byte("expected"))

	err = VerifyEd25519(hex.EncodeToString(publicKey), hex.EncodeToString(signature), []byte("actual"))
	require.ErrorContains(t, err, "signature verification failed")
}

func TestDecodePublicKeyAndSignatureValidateLength(t *testing.T) {
	_, err := DecodePublicKey(base64.StdEncoding.EncodeToString([]byte("short")))
	require.ErrorContains(t, err, "invalid public key")

	_, err = DecodeSignature(base64.StdEncoding.EncodeToString([]byte("short")))
	require.ErrorContains(t, err, "invalid signature")
}
