package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Manifest struct {
	Version   string            `json:"version"`
	Notes     string            `json:"notes,omitempty"`
	Downloads map[string]string `json:"downloads,omitempty"`
}

type CheckResult struct {
	CurrentVersion  string   `json:"current_version"`
	LatestVersion   string   `json:"latest_version"`
	UpdateAvailable bool     `json:"update_available"`
	Manifest        Manifest `json:"manifest"`
}

func Check(ctx context.Context, currentVersion, manifestURL string) (CheckResult, error) {
	if manifestURL == "" {
		return CheckResult{}, fmt.Errorf("manifest URL is required")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return CheckResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return CheckResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CheckResult{}, fmt.Errorf("manifest request failed: %s", resp.Status)
	}
	var manifest Manifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return CheckResult{}, err
	}
	return CheckResult{
		CurrentVersion:  currentVersion,
		LatestVersion:   manifest.Version,
		UpdateAvailable: manifest.Version != "" && manifest.Version != currentVersion,
		Manifest:        manifest,
	}, nil
}
