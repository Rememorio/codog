package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Manifest struct {
	Version   string            `json:"version"`
	Notes     string            `json:"notes,omitempty"`
	Downloads map[string]string `json:"downloads,omitempty"`
	Checksums map[string]string `json:"checksums,omitempty"`
	Signature string            `json:"signature,omitempty"`
}

type CheckResult struct {
	CurrentVersion  string   `json:"current_version"`
	LatestVersion   string   `json:"latest_version"`
	UpdateAvailable bool     `json:"update_available"`
	Manifest        Manifest `json:"manifest"`
	SignatureValid  bool     `json:"signature_valid,omitempty"`
}

type DownloadResult struct {
	Version  string `json:"version"`
	Platform string `json:"platform"`
	URL      string `json:"url"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Verified bool   `json:"verified"`
}

type InstallResult struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	BackupPath string `json:"backup_path,omitempty"`
	Installed  bool   `json:"installed"`
	RolledBack bool   `json:"rolled_back,omitempty"`
}

type RollbackResult struct {
	Target     string `json:"target"`
	BackupPath string `json:"backup_path"`
	RolledBack bool   `json:"rolled_back"`
}

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
	return manifest, nil
}

func VerifyManifest(manifest Manifest, publicKey string) error {
	if manifest.Signature == "" {
		return fmt.Errorf("manifest signature is required")
	}
	key, err := decodePublicKey(publicKey)
	if err != nil {
		return err
	}
	signature, err := decodeSignature(manifest.Signature)
	if err != nil {
		return err
	}
	payload, err := canonicalManifest(manifest)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, payload, signature) {
		return fmt.Errorf("manifest signature verification failed")
	}
	return nil
}

func Download(ctx context.Context, manifestURL, platform, destDir string) (DownloadResult, error) {
	manifest, err := FetchManifest(ctx, manifestURL)
	if err != nil {
		return DownloadResult{}, err
	}
	return DownloadManifest(ctx, manifest, platform, destDir)
}

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

func DownloadManifest(ctx context.Context, manifest Manifest, platform, destDir string) (DownloadResult, error) {
	key, url, checksum, err := selectDownload(manifest, platform)
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

func PlatformKey() string {
	return runtime.GOOS + "-" + runtime.GOARCH
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

func decodePublicKey(value string) (ed25519.PublicKey, error) {
	data, err := decodeBase64OrHex(value)
	if err != nil {
		return nil, fmt.Errorf("invalid public key: %w", err)
	}
	if len(data) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key length: got %d want %d", len(data), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(data), nil
}

func decodeSignature(value string) ([]byte, error) {
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
