package releaseparity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/updater"
	"github.com/stretchr/testify/require"
)

func TestBuildReportsReadyWhenProductionSurfacesConfigured(t *testing.T) {
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	require.NoError(t, os.WriteFile(policyPath, []byte(`{"version":1}`), 0o644))
	sandboxStatus := sandbox.Status{
		Available:  true,
		Default:    "sandbox-exec",
		Strategies: []string{"sandbox-exec"},
	}

	report := Build(Options{
		SandboxStrategy:           "detect",
		SandboxEnabled:            true,
		UpdaterManifestURL:        "https://example.invalid/codog/manifest.json",
		EnterprisePolicyPath:      policyPath,
		EnterprisePolicyPublicKey: "public-key",
		SandboxStatus:             &sandboxStatus,
	})

	require.Equal(t, "ready", report.Status)
	require.Equal(t, updater.PlatformKey(), report.Platform)
	require.True(t, report.SandboxAvailable)
	require.True(t, report.UpdaterManifestConfigured)
	require.True(t, report.UpdaterRollbackSupported)
	require.True(t, report.ManagedPolicyConfigured)
	require.True(t, report.ManagedPolicyPublicKey)
	require.True(t, report.ManagedPolicyFilePresent)
	require.True(t, report.ReleaseSigningSupported)
	require.Empty(t, report.MissingProductionSurfaces)
}

func TestBuildReportsDegradedWhenProductionSurfacesAreMissing(t *testing.T) {
	sandboxStatus := sandbox.Status{Available: false}

	report := Build(Options{SandboxStatus: &sandboxStatus})

	require.Equal(t, "degraded", report.Status)
	require.Contains(t, report.MissingProductionSurfaces, "sandbox_available")
	require.Contains(t, report.MissingProductionSurfaces, "updater_manifest")
	require.Contains(t, report.MissingProductionSurfaces, "managed_policy")
	require.Contains(t, report.MissingProductionSurfaces, "managed_policy_public_key")
	require.NotContains(t, report.MissingProductionSurfaces, "updater_rollback")
	require.NotContains(t, report.MissingProductionSurfaces, "release_signing")
}
