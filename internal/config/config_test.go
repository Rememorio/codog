package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadAppliesManagedPolicy(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	require.NoError(t, os.WriteFile(policyPath, []byte(`{"max_permission_mode":"read-only","denied_tools":["bash"],"permission_rules":{"deny":["write_file"]}}`), 0o644))
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"permission_mode":"danger-full-access","future":{"enterprise_policy":"`+policyPath+`"}}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "read-only", cfg.PermissionMode)
	require.Contains(t, cfg.PermissionRules.DeniedTools, "bash")
	require.Contains(t, cfg.PermissionRules.Deny, "write_file")
}

func TestMergeAppendsPermissionRules(t *testing.T) {
	dst := Config{
		PermissionRules: PermissionRules{
			Allow:       []string{"read_file"},
			DeniedTools: []string{"bash"},
		},
	}
	merge(&dst, Config{
		PermissionRules: PermissionRules{
			Deny:        []string{"write_file"},
			Ask:         []string{"edit_file"},
			DeniedTools: []string{"plugin_tool"},
		},
	})

	require.Equal(t, []string{"read_file"}, dst.PermissionRules.Allow)
	require.Equal(t, []string{"write_file"}, dst.PermissionRules.Deny)
	require.Equal(t, []string{"edit_file"}, dst.PermissionRules.Ask)
	require.Equal(t, []string{"bash", "plugin_tool"}, dst.PermissionRules.DeniedTools)
}
