package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ProviderProfile struct {
	Name      string           `json:"name"`
	Issuer    string           `json:"issuer"`
	ClientID  string           `json:"client_id"`
	Scopes    []string         `json:"scopes,omitempty"`
	Metadata  ProviderMetadata `json:"metadata"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

func SaveProviderProfile(ctx context.Context, configHome, name, issuer, clientID string, scopes []string) (ProviderProfile, error) {
	if err := validateProfileName(name); err != nil {
		return ProviderProfile{}, err
	}
	if strings.TrimSpace(issuer) == "" {
		return ProviderProfile{}, errors.New("issuer URL is required")
	}
	if strings.TrimSpace(clientID) == "" {
		return ProviderProfile{}, errors.New("client id is required")
	}
	metadata, err := DiscoverProvider(ctx, issuer)
	if err != nil {
		return ProviderProfile{}, err
	}
	now := time.Now().UTC()
	existing, err := LoadProviderProfile(configHome, name)
	if err == nil && !existing.CreatedAt.IsZero() {
		now = existing.CreatedAt
	}
	profile := ProviderProfile{
		Name:      name,
		Issuer:    issuer,
		ClientID:  clientID,
		Scopes:    append([]string(nil), scopes...),
		Metadata:  metadata,
		CreatedAt: now,
		UpdatedAt: time.Now().UTC(),
	}
	path := providerProfilePath(configHome, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ProviderProfile{}, err
	}
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return ProviderProfile{}, err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return ProviderProfile{}, err
	}
	return profile, nil
}

func LoadProviderProfile(configHome, name string) (ProviderProfile, error) {
	if err := validateProfileName(name); err != nil {
		return ProviderProfile{}, err
	}
	data, err := os.ReadFile(providerProfilePath(configHome, name))
	if err != nil {
		return ProviderProfile{}, err
	}
	var profile ProviderProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return ProviderProfile{}, err
	}
	return profile, nil
}

func ResolveProviderProfile(configHome, name string) (ProviderProfile, error) {
	if strings.TrimSpace(name) != "" {
		return LoadProviderProfile(configHome, name)
	}
	profile, err := LoadProviderProfile(configHome, "default")
	if err == nil {
		return profile, nil
	}
	profiles, listErr := ListProviderProfiles(configHome)
	if listErr != nil {
		return ProviderProfile{}, listErr
	}
	if len(profiles) == 1 {
		return profiles[0], nil
	}
	if len(profiles) == 0 {
		return ProviderProfile{}, errors.New("no oauth provider profiles configured")
	}
	return ProviderProfile{}, errors.New("multiple oauth provider profiles configured; pass a profile name")
}

func ListProviderProfiles(configHome string) ([]ProviderProfile, error) {
	dir := providerProfilesDir(configHome)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	profiles := []ProviderProfile{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		profile, err := LoadProviderProfile(configHome, name)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].Name < profiles[j].Name
	})
	return profiles, nil
}

func DeleteProviderProfile(configHome, name string) error {
	if err := validateProfileName(name); err != nil {
		return err
	}
	err := os.Remove(providerProfilePath(configHome, name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func providerProfilesDir(configHome string) string {
	return filepath.Join(configHome, "oauth", "providers")
}

func providerProfilePath(configHome, name string) string {
	return filepath.Join(providerProfilesDir(configHome), name+".json")
}

func validateProfileName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("provider profile name is required")
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return errors.New("provider profile name may only contain letters, digits, dash, underscore, or dot")
	}
	return nil
}
