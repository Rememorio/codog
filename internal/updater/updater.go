// Package updater implements manifest-based Codog binary update operations.
//
// The updater intentionally keeps policy outside the package: callers decide
// which manifest URL and public key to trust, while this package fetches,
// verifies, downloads, installs, and rolls back artifacts.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/signing"
)

// Manifest describes a release index served by an update source.
//
// Downloads maps platform keys such as "darwin-arm64" to artifact URLs. Relative
// URLs are resolved against Source, which is set by FetchManifest.
type Manifest struct {
	Version   string            `json:"version"`
	Notes     string            `json:"notes,omitempty"`
	Downloads map[string]string `json:"downloads,omitempty"`
	Checksums map[string]string `json:"checksums,omitempty"`
	Signature string            `json:"signature,omitempty"`
	Source    string            `json:"-"`
}

// CheckResult reports whether a manifest advertises a newer version.
type CheckResult struct {
	CurrentVersion  string   `json:"current_version"`
	LatestVersion   string   `json:"latest_version"`
	UpdateAvailable bool     `json:"update_available"`
	Manifest        Manifest `json:"manifest"`
	SignatureValid  bool     `json:"signature_valid,omitempty"`
}

// DownloadResult describes a downloaded updater artifact and its verification.
type DownloadResult struct {
	Version  string `json:"version"`
	Platform string `json:"platform"`
	URL      string `json:"url"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Verified bool   `json:"verified"`
}

// InstallResult describes an installed artifact and any created rollback backup.
type InstallResult struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	BackupPath string `json:"backup_path,omitempty"`
	Installed  bool   `json:"installed"`
	RolledBack bool   `json:"rolled_back,omitempty"`
}

// RollbackResult describes a successful restore from a previous install backup.
type RollbackResult struct {
	Target     string `json:"target"`
	BackupPath string `json:"backup_path"`
	RolledBack bool   `json:"rolled_back"`
}

// ArtifactInfo describes one downloaded updater artifact on disk.
type ArtifactInfo struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modified_at"`
	Executable bool   `json:"executable"`
}

// Check fetches an update manifest and compares it to the current version.
func Check(ctx context.Context, currentVersion, manifestURL string) (CheckResult, error) {
	manifest, err := FetchManifest(ctx, manifestURL)
	if err != nil {
		return CheckResult{}, err
	}
	return CheckResult{
		CurrentVersion:  currentVersion,
		LatestVersion:   manifest.Version,
		UpdateAvailable: manifest.Version != "" && manifest.Version != currentVersion,
		Manifest:        manifest,
	}, nil
}

// CheckSigned fetches an update manifest, verifies its signature, and compares
// it to the current version.
func CheckSigned(ctx context.Context, currentVersion, manifestURL, publicKey string) (CheckResult, error) {
	manifest, err := FetchManifest(ctx, manifestURL)
	if err != nil {
		return CheckResult{}, err
	}
	if err := VerifyManifest(manifest, publicKey); err != nil {
		return CheckResult{}, err
	}
	return CheckResult{
		CurrentVersion:  currentVersion,
		LatestVersion:   manifest.Version,
		UpdateAvailable: manifest.Version != "" && manifest.Version != currentVersion,
		Manifest:        manifest,
		SignatureValid:  true,
	}, nil
}

// FetchManifest downloads and decodes a release manifest from manifestURL.
func FetchManifest(ctx context.Context, manifestURL string) (Manifest, error) {
	if manifestURL == "" {
		return Manifest{}, fmt.Errorf("manifest URL is required")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return Manifest{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Manifest{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Manifest{}, fmt.Errorf("manifest request failed: %s", resp.Status)
	}
	var manifest Manifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	manifest.Source = manifestURL
	return manifest, nil
}

// VerifyManifest validates the Ed25519 signature embedded in manifest.
func VerifyManifest(manifest Manifest, publicKey string) error {
	if manifest.Signature == "" {
		return fmt.Errorf("manifest signature is required")
	}
	payload, err := canonicalManifest(manifest)
	if err != nil {
		return err
	}
	if err := signing.VerifyEd25519(publicKey, manifest.Signature, payload); err != nil {
		if strings.Contains(err.Error(), "signature verification failed") {
			return fmt.Errorf("manifest %w", err)
		}
		return err
	}
	return nil
}

// Download fetches a manifest and downloads the artifact for platform.
func Download(ctx context.Context, manifestURL, platform, destDir string) (DownloadResult, error) {
	manifest, err := FetchManifest(ctx, manifestURL)
	if err != nil {
		return DownloadResult{}, err
	}
	return DownloadManifest(ctx, manifest, platform, destDir)
}

// DownloadSigned verifies a signed manifest before downloading its artifact.
func DownloadSigned(ctx context.Context, manifestURL, platform, destDir, publicKey string) (DownloadResult, error) {
	manifest, err := FetchManifest(ctx, manifestURL)
	if err != nil {
		return DownloadResult{}, err
	}
	if err := VerifyManifest(manifest, publicKey); err != nil {
		return DownloadResult{}, err
	}
	return DownloadManifest(ctx, manifest, platform, destDir)
}

// DownloadManifest downloads the artifact selected from an already loaded
// manifest. If platform is empty, PlatformKey is used.
func DownloadManifest(ctx context.Context, manifest Manifest, platform, destDir string) (DownloadResult, error) {
	key, url, checksum, err := selectDownload(manifest, platform)
	if err != nil {
		return DownloadResult{}, err
	}
	url, err = resolveDownloadURL(manifest.Source, url)
	if err != nil {
		return DownloadResult{}, err
	}
	if destDir == "" {
		destDir = "."
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return DownloadResult{}, err
	}
	target := filepath.Join(destDir, "codog-"+safeName(manifest.Version)+"-"+safeName(key))
	tmp := target + ".tmp"
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return DownloadResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return DownloadResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DownloadResult{}, fmt.Errorf("download request failed: %s", resp.Status)
	}
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return DownloadResult{}, err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, hash), resp.Body)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return DownloadResult{}, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return DownloadResult{}, closeErr
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if checksum != "" && !strings.EqualFold(normalizeChecksum(checksum), actual) {
		_ = os.Remove(tmp)
		return DownloadResult{}, fmt.Errorf("checksum mismatch: expected %s got %s", normalizeChecksum(checksum), actual)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return DownloadResult{}, err
	}
	return DownloadResult{
		Version:  manifest.Version,
		Platform: key,
		URL:      url,
		Path:     target,
		SHA256:   actual,
		Verified: checksum != "",
	}, nil
}

// Install atomically replaces targetPath with artifactPath where the platform
// permits rename-based replacement. Existing targets are backed up as .bak.
func Install(artifactPath, targetPath string) (InstallResult, error) {
	if artifactPath == "" {
		return InstallResult{}, fmt.Errorf("artifact path is required")
	}
	if targetPath == "" {
		return InstallResult{}, fmt.Errorf("target path is required")
	}
	artifactPath = filepath.Clean(artifactPath)
	targetPath = filepath.Clean(targetPath)
	sourceInfo, err := os.Stat(artifactPath)
	if err != nil {
		return InstallResult{}, err
	}
	if sourceInfo.IsDir() {
		return InstallResult{}, fmt.Errorf("artifact must be a file")
	}
	targetInfo, targetErr := os.Stat(targetPath)
	if targetErr != nil && !os.IsNotExist(targetErr) {
		return InstallResult{}, targetErr
	}
	mode := sourceInfo.Mode().Perm()
	hadTarget := targetErr == nil
	if hadTarget {
		if targetInfo.IsDir() {
			return InstallResult{}, fmt.Errorf("target must be a file")
		}
		mode = targetInfo.Mode().Perm()
	}
	targetDir := filepath.Dir(targetPath)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return InstallResult{}, err
	}
	tmpPath := targetPath + ".new"
	backupPath := targetPath + ".bak"
	if err := copyExecutable(artifactPath, tmpPath, mode); err != nil {
		return InstallResult{}, err
	}
	_ = os.Remove(backupPath)
	result := InstallResult{Source: artifactPath, Target: targetPath}
	if hadTarget {
		if err := os.Rename(targetPath, backupPath); err != nil {
			_ = os.Remove(tmpPath)
			return result, err
		}
		result.BackupPath = backupPath
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		result.RolledBack = true
		_ = os.Remove(tmpPath)
		if hadTarget {
			_ = os.Rename(backupPath, targetPath)
		}
		return result, err
	}
	if err := os.Chmod(targetPath, mode); err != nil {
		result.RolledBack = true
		if hadTarget {
			_ = os.Remove(targetPath)
			_ = os.Rename(backupPath, targetPath)
		}
		return result, err
	}
	result.Installed = true
	return result, nil
}

// Rollback restores targetPath from the .bak file created by Install.
func Rollback(targetPath string) (RollbackResult, error) {
	if targetPath == "" {
		return RollbackResult{}, fmt.Errorf("target path is required")
	}
	targetPath = filepath.Clean(targetPath)
	backupPath := targetPath + ".bak"
	if _, err := os.Stat(backupPath); err != nil {
		return RollbackResult{}, err
	}
	tmpPath := targetPath + ".rollback"
	_ = os.Remove(tmpPath)
	if _, err := os.Stat(targetPath); err == nil {
		if err := os.Rename(targetPath, tmpPath); err != nil {
			return RollbackResult{}, err
		}
	}
	if err := os.Rename(backupPath, targetPath); err != nil {
		if _, tmpErr := os.Stat(tmpPath); tmpErr == nil {
			_ = os.Rename(tmpPath, targetPath)
		}
		return RollbackResult{}, err
	}
	_ = os.Remove(tmpPath)
	return RollbackResult{Target: targetPath, BackupPath: backupPath, RolledBack: true}, nil
}

// PlatformKey returns the manifest platform key for the current GOOS/GOARCH.
func PlatformKey() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

// ListArtifacts returns downloaded updater artifacts from dir.
func ListArtifacts(dir string) ([]ArtifactInfo, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return []ArtifactInfo{}, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []ArtifactInfo{}, nil
		}
		return nil, err
	}
	artifacts := []ArtifactInfo{}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		path := filepath.Join(dir, entry.Name())
		artifacts = append(artifacts, ArtifactInfo{
			Name:       entry.Name(),
			Path:       path,
			Size:       info.Size(),
			ModifiedAt: info.ModTime().UTC().Format(time.RFC3339Nano),
			Executable: info.Mode().Perm()&0o111 != 0,
		})
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].ModifiedAt == artifacts[j].ModifiedAt {
			return artifacts[i].Name < artifacts[j].Name
		}
		return artifacts[i].ModifiedAt > artifacts[j].ModifiedAt
	})
	return artifacts, nil
}

func selectDownload(manifest Manifest, platform string) (string, string, string, error) {
	if platform == "" {
		platform = PlatformKey()
	}
	if url := manifest.Downloads[platform]; url != "" {
		return platform, url, manifest.Checksums[platform], nil
	}
	if base, _, ok := strings.Cut(platform, "-"); ok {
		if url := manifest.Downloads[base]; url != "" {
			return base, url, manifest.Checksums[base], nil
		}
	}
	return "", "", "", fmt.Errorf("no download for platform %q", platform)
}

func resolveDownloadURL(manifestURL string, downloadURL string) (string, error) {
	downloadURL = strings.TrimSpace(downloadURL)
	if downloadURL == "" {
		return "", fmt.Errorf("download URL is required")
	}
	parsed, err := url.Parse(downloadURL)
	if err != nil {
		return "", err
	}
	if parsed.IsAbs() {
		return parsed.String(), nil
	}
	if strings.TrimSpace(manifestURL) == "" {
		return "", fmt.Errorf("relative download URL %q requires manifest source URL", downloadURL)
	}
	base, err := url.Parse(manifestURL)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(parsed).String(), nil
}

func normalizeChecksum(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "sha256:")
}

func safeName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	name := strings.Trim(builder.String(), "-")
	if name == "" {
		return "unknown"
	}
	return name
}

func canonicalManifest(manifest Manifest) ([]byte, error) {
	manifest.Signature = ""
	data, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func copyExecutable(source, target string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(target)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return closeErr
	}
	return os.Chmod(target, mode)
}
