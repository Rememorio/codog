package oauth

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneratePKCE(t *testing.T) {
	pkce, err := GeneratePKCE()
	require.NoError(t, err)
	require.NotEmpty(t, pkce.CodeVerifier)
	require.NotEmpty(t, pkce.CodeChallenge)
	require.Equal(t, "S256", pkce.Method)
	require.NotEqual(t, pkce.CodeVerifier, pkce.CodeChallenge)
}
