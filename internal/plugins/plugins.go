package plugins

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/signing"
)

const DisabledMarker = ".disabled"

type Manifest struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Version     string         `json:"version,omitempty"`
	Description string         `json:"description,omitempty"`
	Tools       []ToolManifest `json:"tools,omitempty"`
	Commands    []string       `json:"commands,omitempty"`
	Hooks       []string       `json:"hooks,omitempty"`
	Path        string         `json:"path,omitempty"`
	Root        string         `json:"root,omitempty"`
	Enabled     bool           `json:"enabled"`
}

type ToolManifest struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Command     string         `json:"command,omitempty"`
	Args        []string       `json:"args,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	Permission  string         `json:"permission,omitempty"`
}

type MarketplaceIndex struct {
	Name           string         `json:"name,omitempty"`
	Plugins        []RemotePlugin `json:"plugins,omitempty"`
	Signature      string         `json:"signature,omitempty"`
	Source         string         `json:"source,omitempty"`
	SignatureValid bool           `json:"signature_valid,omitempty"`
}

type RemotePlugin struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
}

type MarketplaceSource struct {
	URL       string
	PublicKey string
}

type RemoteInstallResult struct {
	MarketplaceURL string   `json:"marketplace_url"`
	ID             string   `json:"id"`
	Version        string   `json:"version,omitempty"`
	URL            string   `json:"url"`
	SHA256         string   `json:"sha256"`
	ChecksumValid  bool     `json:"checksum_valid"`
	SignatureValid bool     `json:"signature_valid,omitempty"`
	Manifest       Manifest `json:"manifest"`
}

type MarketplaceUpdate struct {
	MarketplaceURL  string `json:"marketplace_url"`
	ID              string `json:"id"`
	CurrentVersion  string `json:"current_version,omitempty"`
	LatestVersion   string `json:"latest_version,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	URL             string `json:"url"`
	SHA256          string `json:"sha256"`
	SignatureValid  bool   `json:"signature_valid,omitempty"`
}

type RemoteUpdateResult struct {
	MarketplaceURL  string   `json:"marketplace_url"`
	ID              string   `json:"id"`
	PreviousVersion string   `json:"previous_version,omitempty"`
	Version         string   `json:"version,omitempty"`
	URL             string   `json:"url"`
	SHA256          string   `json:"sha256"`
	ChecksumValid   bool     `json:"checksum_valid"`
	SignatureValid  bool     `json:"signature_valid,omitempty"`
	BackupPath      string   `json:"backup_path"`
	Manifest        Manifest `json:"manifest"`
	Updated         bool     `json:"updated"`
}

type preparedRemotePlugin struct {
	Entry       RemotePlugin
	ResolvedURL string
	SHA256      string
	SourceDir   string
}

type rawManifest struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Version     string            `json:"version,omitempty"`
	Description string            `json:"description,omitempty"`
	Tools       []json.RawMessage `json:"tools,omitempty"`
	Commands    []string          `json:"commands,omitempty"`
	Hooks       []string          `json:"hooks,omitempty"`
}

func Load(workspace string) ([]Manifest, error) {
	root := Root(workspace)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []Manifest{}, nil
		}
		return nil, err
	}
	manifests := []Manifest{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		manifest, err := LoadManifest(filepath.Join(root, entry.Name()))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if manifest.ID == "" {
			manifest.ID = entry.Name()
		}
		manifests = append(manifests, manifest)
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].ID < manifests[j].ID })
	return manifests, nil
}

func FetchMarketplace(ctx context.Context, indexURL, publicKey string) (MarketplaceIndex, error) {
	if strings.TrimSpace(indexURL) == "" {
		return MarketplaceIndex{}, errors.New("marketplace URL is required")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return MarketplaceIndex{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return MarketplaceIndex{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return MarketplaceIndex{}, fmt.Errorf("marketplace request failed: %s", resp.Status)
	}
	var index MarketplaceIndex
	if err := json.NewDecoder(resp.Body).Decode(&index); err != nil {
		return MarketplaceIndex{}, err
	}
	index.Source = indexURL
	if publicKey != "" {
		if err := VerifyMarketplace(index, publicKey); err != nil {
			return MarketplaceIndex{}, err
		}
		index.SignatureValid = true
	}
	sort.Slice(index.Plugins, func(i, j int) bool { return index.Plugins[i].ID < index.Plugins[j].ID })
	return index, nil
}

func VerifyMarketplace(index MarketplaceIndex, publicKey string) error {
	if index.Signature == "" {
		return errors.New("marketplace signature is required")
	}
	payload, err := canonicalMarketplace(index)
	if err != nil {
		return err
	}
	if err := signing.VerifyEd25519(publicKey, index.Signature, payload); err != nil {
		if strings.Contains(err.Error(), "signature verification failed") {
			return fmt.Errorf("marketplace %w", err)
		}
		return err
	}
	return nil
}

func (index MarketplaceIndex) Find(id string) (RemotePlugin, bool) {
	for _, plugin := range index.Plugins {
		if strings.EqualFold(plugin.ID, id) {
			return plugin, true
		}
	}
	return RemotePlugin{}, false
}

func InstallRemote(ctx context.Context, workspace, indexURL, id, publicKey string) (RemoteInstallResult, error) {
	index, err := FetchMarketplace(ctx, indexURL, publicKey)
	if err != nil {
		return RemoteInstallResult{}, err
	}
	return InstallRemoteFromIndex(ctx, workspace, index, id)
}

func InstallRemoteFromIndex(ctx context.Context, workspace string, index MarketplaceIndex, id string) (RemoteInstallResult, error) {
	prepared, cleanup, err := prepareRemotePlugin(ctx, index, id)
	if err != nil {
		return RemoteInstallResult{}, err
	}
	defer cleanup()
	manifest, err := Install(workspace, prepared.SourceDir)
	if err != nil {
		return RemoteInstallResult{}, err
	}
	return RemoteInstallResult{
		MarketplaceURL: index.Source,
		ID:             prepared.Entry.ID,
		Version:        prepared.Entry.Version,
		URL:            prepared.ResolvedURL,
		SHA256:         prepared.SHA256,
		ChecksumValid:  true,
		SignatureValid: index.SignatureValid,
		Manifest:       manifest,
	}, nil
}

func CheckUpdates(ctx context.Context, workspace string, sources []MarketplaceSource) ([]MarketplaceUpdate, error) {
	installed, err := Load(workspace)
	if err != nil {
		return nil, err
	}
	updates := []MarketplaceUpdate{}
	for _, source := range sources {
		index, err := FetchMarketplace(ctx, source.URL, source.PublicKey)
		if err != nil {
			return nil, err
		}
		for _, local := range installed {
			remote, ok := index.Find(local.ID)
			if !ok || !versionNewer(remote.Version, local.Version) {
				continue
			}
			resolvedURL, err := resolveMarketplaceURL(index.Source, remote.URL)
			if err != nil {
				return nil, err
			}
			updates = append(updates, MarketplaceUpdate{
				MarketplaceURL:  index.Source,
				ID:              local.ID,
				CurrentVersion:  local.Version,
				LatestVersion:   remote.Version,
				UpdateAvailable: true,
				URL:             resolvedURL,
				SHA256:          remote.SHA256,
				SignatureValid:  index.SignatureValid,
			})
		}
	}
	sort.Slice(updates, func(i, j int) bool {
		if updates[i].ID == updates[j].ID {
			return updates[i].MarketplaceURL < updates[j].MarketplaceURL
		}
		return updates[i].ID < updates[j].ID
	})
	return updates, nil
}

func UpdateRemote(ctx context.Context, workspace string, sources []MarketplaceSource, id string) (RemoteUpdateResult, error) {
	if strings.TrimSpace(id) == "" {
		return RemoteUpdateResult{}, errors.New("plugin id is required")
	}
	if err := validateID(id); err != nil {
		return RemoteUpdateResult{}, err
	}
	local, err := LoadManifest(filepath.Join(Root(workspace), id))
	if err != nil {
		return RemoteUpdateResult{}, err
	}
	foundCurrentOrOlder := false
	for _, source := range sources {
		index, err := FetchMarketplace(ctx, source.URL, source.PublicKey)
		if err != nil {
			return RemoteUpdateResult{}, err
		}
		remote, ok := index.Find(id)
		if !ok {
			continue
		}
		if !versionNewer(remote.Version, local.Version) {
			foundCurrentOrOlder = true
			continue
		}
		return UpdateRemoteFromIndex(ctx, workspace, index, id, local.Version)
	}
	if foundCurrentOrOlder {
		return RemoteUpdateResult{}, fmt.Errorf("plugin %q is already up to date", id)
	}
	return RemoteUpdateResult{}, fmt.Errorf("plugin %q not found in configured marketplaces", id)
}

func UpdateRemoteFromIndex(ctx context.Context, workspace string, index MarketplaceIndex, id, previousVersion string) (RemoteUpdateResult, error) {
	prepared, cleanup, err := prepareRemotePlugin(ctx, index, id)
	if err != nil {
		return RemoteUpdateResult{}, err
	}
	defer cleanup()
	manifest, backupPath, err := replaceInstalled(workspace, prepared.SourceDir, prepared.Entry.ID)
	if err != nil {
		return RemoteUpdateResult{}, err
	}
	return RemoteUpdateResult{
		MarketplaceURL:  index.Source,
		ID:              prepared.Entry.ID,
		PreviousVersion: previousVersion,
		Version:         prepared.Entry.Version,
		URL:             prepared.ResolvedURL,
		SHA256:          prepared.SHA256,
		ChecksumValid:   true,
		SignatureValid:  index.SignatureValid,
		BackupPath:      backupPath,
		Manifest:        manifest,
		Updated:         true,
	}, nil
}

func prepareRemotePlugin(ctx context.Context, index MarketplaceIndex, id string) (preparedRemotePlugin, func(), error) {
	cleanup := func() {}
	entry, ok := index.Find(id)
	if !ok {
		return preparedRemotePlugin{}, cleanup, fmt.Errorf("plugin %q not found in marketplace", id)
	}
	if err := validateID(entry.ID); err != nil {
		return preparedRemotePlugin{}, cleanup, err
	}
	if strings.TrimSpace(entry.URL) == "" {
		return preparedRemotePlugin{}, cleanup, fmt.Errorf("remote plugin %q URL is required", entry.ID)
	}
	if strings.TrimSpace(entry.SHA256) == "" {
		return preparedRemotePlugin{}, cleanup, fmt.Errorf("remote plugin %q sha256 is required", entry.ID)
	}
	resolvedURL, err := resolveMarketplaceURL(index.Source, entry.URL)
	if err != nil {
		return preparedRemotePlugin{}, cleanup, err
	}
	tmpDir, err := os.MkdirTemp("", "codog-plugin-*")
	if err != nil {
		return preparedRemotePlugin{}, cleanup, err
	}
	cleanup = func() { _ = os.RemoveAll(tmpDir) }

	archivePath := filepath.Join(tmpDir, "plugin.zip")
	actual, err := downloadArchive(ctx, resolvedURL, archivePath)
	if err != nil {
		return preparedRemotePlugin{}, cleanup, err
	}
	expected := normalizeChecksum(entry.SHA256)
	if !strings.EqualFold(expected, actual) {
		return preparedRemotePlugin{}, cleanup, fmt.Errorf("checksum mismatch: expected %s got %s", expected, actual)
	}
	extractDir := filepath.Join(tmpDir, "extract")
	if err := extractZip(archivePath, extractDir); err != nil {
		return preparedRemotePlugin{}, cleanup, err
	}
	sourceDir, err := findPluginDir(extractDir)
	if err != nil {
		return preparedRemotePlugin{}, cleanup, err
	}
	sourceManifest, err := LoadManifest(sourceDir)
	if err != nil {
		return preparedRemotePlugin{}, cleanup, err
	}
	if !strings.EqualFold(sourceManifest.ID, entry.ID) {
		return preparedRemotePlugin{}, cleanup, fmt.Errorf("remote plugin id mismatch: index %q archive %q", entry.ID, sourceManifest.ID)
	}
	if entry.Version != "" && sourceManifest.Version != "" && entry.Version != sourceManifest.Version {
		return preparedRemotePlugin{}, cleanup, fmt.Errorf("remote plugin version mismatch: index %q archive %q", entry.Version, sourceManifest.Version)
	}
	return preparedRemotePlugin{
		Entry:       entry,
		ResolvedURL: resolvedURL,
		SHA256:      actual,
		SourceDir:   sourceDir,
	}, cleanup, nil
}

func replaceInstalled(workspace, sourceDir, id string) (Manifest, string, error) {
	root := Root(workspace)
	target := filepath.Join(root, id)
	info, err := os.Stat(target)
	if err != nil {
		return Manifest{}, "", err
	}
	if !info.IsDir() {
		return Manifest{}, "", fmt.Errorf("installed plugin target is not a directory")
	}
	wasDisabled := Disabled(target)
	staging, err := os.MkdirTemp(root, ".update-"+id+"-*")
	if err != nil {
		return Manifest{}, "", err
	}
	if err := copyDir(sourceDir, staging); err != nil {
		_ = os.RemoveAll(staging)
		return Manifest{}, "", err
	}
	backupPath, err := nextBackupPath(workspace, id)
	if err != nil {
		_ = os.RemoveAll(staging)
		return Manifest{}, "", err
	}
	if err := os.Rename(target, backupPath); err != nil {
		_ = os.RemoveAll(staging)
		return Manifest{}, "", err
	}
	if err := os.Rename(staging, target); err != nil {
		_ = os.Rename(backupPath, target)
		_ = os.RemoveAll(staging)
		return Manifest{}, "", err
	}
	if wasDisabled {
		if err := os.WriteFile(filepath.Join(target, DisabledMarker), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
			_ = os.RemoveAll(target)
			_ = os.Rename(backupPath, target)
			return Manifest{}, "", err
		}
	}
	manifest, err := LoadManifest(target)
	if err != nil {
		_ = os.RemoveAll(target)
		_ = os.Rename(backupPath, target)
		return Manifest{}, "", err
	}
	return manifest, backupPath, nil
}

func nextBackupPath(workspace, id string) (string, error) {
	root := filepath.Join(workspace, ".codog", "plugin-backups")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	stamp := time.Now().UTC().Format("20060102150405")
	for i := 0; i < 100; i++ {
		name := id + "-" + stamp
		if i > 0 {
			name = fmt.Sprintf("%s-%02d", name, i)
		}
		path := filepath.Join(root, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("could not allocate plugin backup path")
}

func Root(workspace string) string {
	return filepath.Join(workspace, ".codog", "plugins")
}

func LoadManifest(dir string) (Manifest, error) {
	path := filepath.Join(dir, "plugin.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var raw rawManifest
	if err := json.Unmarshal(data, &raw); err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		ID:          raw.ID,
		Name:        raw.Name,
		Version:     raw.Version,
		Description: raw.Description,
		Commands:    raw.Commands,
		Hooks:       raw.Hooks,
		Path:        path,
		Root:        dir,
		Enabled:     !Disabled(dir),
	}
	for _, rawTool := range raw.Tools {
		var name string
		if err := json.Unmarshal(rawTool, &name); err == nil {
			manifest.Tools = append(manifest.Tools, ToolManifest{Name: name})
			continue
		}
		var tool ToolManifest
		if err := json.Unmarshal(rawTool, &tool); err != nil {
			return Manifest{}, err
		}
		manifest.Tools = append(manifest.Tools, tool)
	}
	if manifest.ID == "" {
		manifest.ID = filepath.Base(dir)
	}
	if manifest.Name == "" {
		manifest.Name = manifest.ID
	}
	return manifest, nil
}

func Disabled(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, DisabledMarker))
	return err == nil
}

func Install(workspace, source string) (Manifest, error) {
	sourceDir, err := sourceDir(source)
	if err != nil {
		return Manifest{}, err
	}
	manifest, err := LoadManifest(sourceDir)
	if err != nil {
		return Manifest{}, err
	}
	if err := validateID(manifest.ID); err != nil {
		return Manifest{}, err
	}
	target := filepath.Join(Root(workspace), manifest.ID)
	if _, err := os.Stat(target); err == nil {
		return Manifest{}, errors.New("plugin is already installed")
	} else if err != nil && !os.IsNotExist(err) {
		return Manifest{}, err
	}
	if err := copyDir(sourceDir, target); err != nil {
		return Manifest{}, err
	}
	return LoadManifest(target)
}

func Enable(workspace, id string) (Manifest, error) {
	dir, err := pluginDir(workspace, id)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.Remove(filepath.Join(dir, DisabledMarker)); err != nil && !os.IsNotExist(err) {
		return Manifest{}, err
	}
	return LoadManifest(dir)
}

func Disable(workspace, id string) (Manifest, error) {
	dir, err := pluginDir(workspace, id)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, DisabledMarker), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return Manifest{}, err
	}
	return LoadManifest(dir)
}

func Remove(workspace, id string) error {
	dir, err := pluginDir(workspace, id)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func pluginDir(workspace, id string) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	dir := filepath.Join(Root(workspace), id)
	if _, err := os.Stat(filepath.Join(dir, "plugin.json")); err != nil {
		return "", err
	}
	return dir, nil
}

func validateID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("plugin id is required")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return errors.New("plugin id must be a single path component")
	}
	return nil
}

func sourceDir(source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", errors.New("plugin source is required")
	}
	info, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return source, nil
	}
	if filepath.Base(source) != "plugin.json" {
		return "", errors.New("plugin source must be a directory or plugin.json")
	}
	return filepath.Dir(source), nil
}

func copyDir(source, target string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(target, 0o755)
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(target, rel), 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		return copyFile(path, filepath.Join(target, rel))
	})
}

func copyFile(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func canonicalMarketplace(index MarketplaceIndex) ([]byte, error) {
	index.Signature = ""
	index.Source = ""
	index.SignatureValid = false
	return json.Marshal(index)
}

func resolveMarketplaceURL(indexURL, entryURL string) (string, error) {
	parsed, err := url.Parse(entryURL)
	if err != nil {
		return "", err
	}
	if parsed.IsAbs() {
		return parsed.String(), nil
	}
	base, err := url.Parse(indexURL)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(parsed).String(), nil
}

func downloadArchive(ctx context.Context, archiveURL, target string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("plugin archive request failed: %s", resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, hash), resp.Body)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(target)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func extractZip(archivePath, dest string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, file := range reader.File {
		target, err := safeArchivePath(dest, file.Name)
		if err != nil {
			return err
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if file.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := file.Open()
		if err != nil {
			return err
		}
		mode := file.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeInErr := in.Close()
		closeOutErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeInErr != nil {
			return closeInErr
		}
		if closeOutErr != nil {
			return closeOutErr
		}
	}
	return nil
}

func safeArchivePath(dest, name string) (string, error) {
	if strings.Contains(name, `\`) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	clean := filepath.Clean(name)
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || clean == ".." {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	target := filepath.Join(dest, clean)
	base := filepath.Clean(dest)
	if target != base && !strings.HasPrefix(target, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return target, nil
}

func findPluginDir(root string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "plugin.json" {
			return nil
		}
		found = filepath.Dir(path)
		return filepath.SkipAll
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", errors.New("plugin archive does not contain plugin.json")
	}
	return found, nil
}

func normalizeChecksum(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "sha256:")
}

func versionNewer(latest, current string) bool {
	latest = strings.TrimSpace(latest)
	current = strings.TrimSpace(current)
	if latest == "" || latest == current {
		return false
	}
	return compareVersions(latest, current) > 0
}

func compareVersions(a, b string) int {
	aParts, aOK := versionParts(a)
	bParts, bOK := versionParts(b)
	if aOK && bOK {
		maxLen := len(aParts)
		if len(bParts) > maxLen {
			maxLen = len(bParts)
		}
		for i := 0; i < maxLen; i++ {
			var av, bv int
			if i < len(aParts) {
				av = aParts[i]
			}
			if i < len(bParts) {
				bv = bParts[i]
			}
			if av > bv {
				return 1
			}
			if av < bv {
				return -1
			}
		}
		return 0
	}
	return strings.Compare(strings.TrimSpace(a), strings.TrimSpace(b))
}

func versionParts(value string) ([]int, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r < '0' || r > '9'
	})
	parts := make([]int, 0, len(fields))
	for _, field := range fields {
		if field == "" {
			continue
		}
		n, err := strconv.Atoi(field)
		if err != nil {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, len(parts) > 0
}
