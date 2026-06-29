package bashvalidation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateReadOnlyAllowsInspection(t *testing.T) {
	result := Validate("grep -R TODO internal", "read-only", "")
	require.Equal(t, SeverityAllow, result.Severity)
	require.Equal(t, IntentReadOnly, result.Intent)
}

func TestValidateReadOnlyBlocksWrites(t *testing.T) {
	result := Validate("touch created.txt", "read-only", "")
	require.Equal(t, SeverityBlock, result.Severity)
	require.Equal(t, IntentWrite, result.Intent)
	require.Contains(t, result.Reason, "not read-only")
}

func TestValidateFlagsDestructiveCommands(t *testing.T) {
	result := Validate("rm -rf tmp", "workspace-write", "")
	require.Equal(t, SeverityConfirm, result.Severity)
	require.Equal(t, IntentDestructive, result.Intent)
	require.Contains(t, result.Reason, "recursive forced deletion")
}

func TestValidateBlocksSedInPlaceInReadOnly(t *testing.T) {
	result := Validate("sed -i 's/a/b/' file.txt", "read-only", "")
	require.Equal(t, SeverityBlock, result.Severity)
	require.Contains(t, result.Reason, "sed in-place")
}

func TestValidateWarnsOnSuspiciousPaths(t *testing.T) {
	result := Validate("cp ../secret.txt ./secret.txt", "workspace-write", "")
	require.Equal(t, SeverityConfirm, result.Severity)
	require.Contains(t, result.Reason, "directory traversal")
}

func TestCommandFromInput(t *testing.T) {
	require.Equal(t, "pwd", CommandFromInput([]byte(`{"command":"pwd"}`)))
}
