package updater

import (
	"context"
	"crypto/sha256"
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
}

type CheckResult struct {
	CurrentVersion  string   `json:"current_version"`
	LatestVersion   string   `json:"latest_version"`
	UpdateAvailable bool     `json:"update_available"`
	Manifest        Manifest `json:"manifest"`
}

type DownloadResult struct {
	Version  string `json:"version"`
	Platform string `json:"platform"`
	URL      string `json:"url"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Verified bool   `json:"verified"`
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

func Download(ctx context.Context, manifestURL, platform, destDir string) (DownloadResult, error) {
	manifest, err := FetchManifest(ctx, manifestURL)
	if err != nil {
		return DownloadResult{}, err
	}
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
