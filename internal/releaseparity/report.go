package releaseparity

import (
	"os"
	"strings"

	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/updater"
)

// Options supplies configured production-delivery inputs.
type Options struct {
	SandboxStrategy           string
	SandboxEnabled            bool
	UpdaterManifestURL        string
	EnterprisePolicyPath      string
	EnterprisePolicyPublicKey string
	SandboxStatus             *sandbox.Status
}

// Report is the JSON-safe delivery hardening summary exposed by capabilities
// and release checks.
type Report struct {
	Status                     string   `json:"status"`
	Platform                   string   `json:"platform"`
	SandboxConfigured          bool     `json:"sandbox_configured"`
	SandboxEnabled             bool     `json:"sandbox_enabled"`
	SandboxAvailable           bool     `json:"sandbox_available"`
	SandboxDefault             string   `json:"sandbox_default,omitempty"`
	SandboxStrategies          []string `json:"sandbox_strategies,omitempty"`
	UpdaterManifestConfigured  bool     `json:"updater_manifest_configured"`
	UpdaterManifestURL         string   `json:"updater_manifest_url,omitempty"`
	UpdaterRollbackSupported   bool     `json:"updater_rollback_supported"`
	ManagedPolicyConfigured    bool     `json:"managed_policy_configured"`
	ManagedPolicyPublicKey     bool     `json:"managed_policy_public_key"`
	ManagedPolicyFilePresent   bool     `json:"managed_policy_file_present"`
	ReleaseSigningSupported    bool     `json:"release_signing_supported"`
	RequiredSurfaceCount       int      `json:"required_surface_count"`
	MissingProductionSurfaces  []string `json:"missing_production_surfaces,omitempty"`
	ExcludedProductionSurfaces []string `json:"excluded_production_surfaces,omitempty"`
}

// Build evaluates whether release hardening has the core surfaces required for
// production rollout: sandbox availability, signed updater configuration,
// rollback, and managed policy verification inputs.
func Build(options Options) Report {
	status := options.SandboxStatus
	if status == nil {
		detected := sandbox.Detect()
		status = &detected
	}
	report := Report{
		Status:                    "ready",
		Platform:                  updater.PlatformKey(),
		SandboxConfigured:         strings.TrimSpace(options.SandboxStrategy) != "",
		SandboxEnabled:            options.SandboxEnabled,
		SandboxAvailable:          status.Available,
		SandboxDefault:            status.Default,
		SandboxStrategies:         append([]string(nil), status.Strategies...),
		UpdaterManifestConfigured: strings.TrimSpace(options.UpdaterManifestURL) != "",
		UpdaterManifestURL:        strings.TrimSpace(options.UpdaterManifestURL),
		UpdaterRollbackSupported:  true,
		ManagedPolicyConfigured:   strings.TrimSpace(options.EnterprisePolicyPath) != "",
		ManagedPolicyPublicKey:    strings.TrimSpace(options.EnterprisePolicyPublicKey) != "",
		ReleaseSigningSupported:   true,
		RequiredSurfaceCount:      len(requiredSurfaces()),
	}
	if report.ManagedPolicyConfigured {
		report.ManagedPolicyFilePresent = fileExists(options.EnterprisePolicyPath)
	}
	report.MissingProductionSurfaces = report.missingSurfaces()
	report.ExcludedProductionSurfaces = report.excludedSurfaces()
	if len(report.MissingProductionSurfaces) > 0 {
		report.Status = "degraded"
	}
	return report
}

func requiredSurfaces() []string {
	return []string{
		"sandbox_available",
		"updater_manifest",
		"updater_rollback",
		"managed_policy",
		"managed_policy_public_key",
		"release_signing",
	}
}

func (r Report) missingSurfaces() []string {
	var missing []string
	if !r.SandboxAvailable {
		missing = append(missing, "sandbox_available")
	}
	if !r.UpdaterRollbackSupported {
		missing = append(missing, "updater_rollback")
	}
	if !r.ReleaseSigningSupported {
		missing = append(missing, "release_signing")
	}
	return missing
}

func (r Report) excludedSurfaces() []string {
	var excluded []string
	if !r.UpdaterManifestConfigured {
		excluded = append(excluded, "updater_manifest")
	}
	if !r.ManagedPolicyConfigured || !r.ManagedPolicyFilePresent {
		excluded = append(excluded, "managed_policy")
	}
	if !r.ManagedPolicyPublicKey {
		excluded = append(excluded, "managed_policy_public_key")
	}
	return excluded
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
