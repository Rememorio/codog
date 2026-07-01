package taskpacket

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseValidatesLegacyPacket(t *testing.T) {
	packet, err := Parse([]byte(`{
		"objective":"Update docs",
		"scope":"README only",
		"repo":"codog",
		"branch_policy":"use main",
		"acceptance_tests":["go test ./..."],
		"commit_policy":"commit changes",
		"reporting_contract":"summarize result",
		"escalation_policy":"ask if blocked"
	}`))
	require.NoError(t, err)
	require.NoError(t, Validate(packet))
	require.Equal(t, ScopeCustom, packet.Scope)
	require.Equal(t, "README only", packet.ScopePath)
}

func TestRichPacketRoundTripsAndValidates(t *testing.T) {
	packet, err := Parse([]byte(`{
		"objective":"Implement typed task packet",
		"scope":"module",
		"scope_path":"internal/taskpacket",
		"repo":"codog",
		"worktree":"/tmp/codog-wt",
		"branch_policy":"main only",
		"acceptance_criteria":["packet validates"],
		"resources":[{"kind":"file","value":"internal/taskpacket/taskpacket.go"}],
		"model":"claude-test",
		"provider":"anthropic",
		"permission_profile":"workspace-write",
		"commit_policy":"single commit",
		"reporting_targets":["owner"],
		"recovery_policy":"retry once",
		"verification_plan":["go test ./internal/taskpacket"]
	}`))
	require.NoError(t, err)
	require.NoError(t, Validate(packet))
	require.Equal(t, ScopeModule, packet.Scope)
	require.Equal(t, []string{"packet validates"}, packet.AcceptanceCriteria)
	require.Equal(t, []Resource{{Kind: "file", Value: "internal/taskpacket/taskpacket.go"}}, packet.Resources)

	data, err := json.Marshal(packet)
	require.NoError(t, err)
	var decoded Packet
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, packet, decoded)
}

func TestValidateAccumulatesErrors(t *testing.T) {
	packet := Packet{
		Scope:              ScopeModule,
		AcceptanceTests:    []string{"ok", " "},
		AcceptanceCriteria: []string{" "},
		Resources:          []Resource{{Kind: "", Value: "file"}},
	}
	err := Validate(packet)
	require.Error(t, err)
	var validationErr ValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Contains(t, validationErr.Errors, "objective must not be empty")
	require.Contains(t, validationErr.Errors, "scope_path is required for scope \"module\"")
	require.Contains(t, validationErr.Errors, "acceptance_tests contains an empty value at index 1")
	require.Contains(t, validationErr.Errors, "reporting_contract or reporting_targets must not be empty")
	require.Contains(t, validationErr.Errors, "escalation_policy or recovery_policy must not be empty")
}

func TestResolveScopeRejectsWorkspaceEscape(t *testing.T) {
	workspace := t.TempDir()
	packet := Packet{Scope: ScopeSingleFile, ScopePath: "../outside.go"}
	_, err := ResolveScope(workspace, packet)
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workspace")
}

func TestResolveScopeReturnsWorkspaceAbsolutePath(t *testing.T) {
	workspace := t.TempDir()
	packet := Packet{Scope: ScopeWorkspace}
	resolved, err := ResolveScope(workspace, packet)
	require.NoError(t, err)
	require.Equal(t, ScopeWorkspace, resolved.Scope)
	require.Equal(t, "", resolved.Path)
	require.Equal(t, filepath.Clean(workspace), resolved.AbsolutePath)
}
