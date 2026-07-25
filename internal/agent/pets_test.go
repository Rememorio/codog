package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rememorio/codog/internal/companion"
	"github.com/Rememorio/codog/internal/config"
	"github.com/stretchr/testify/require"
)

func TestParsePetsArgs(t *testing.T) {
	request, err := parsePetsArgs([]string{"use", "codog", "--target=local", "--json"})
	require.NoError(t, err)
	require.Equal(t, petsRequest{Action: "use", ID: "codog", Format: "json", Target: "local"}, request)

	request, err = parsePetsArgs([]string{
		"list",
		"--output-format", "json",
		"--output", "manifest.json",
		"--target", "project",
		"--path", "config.json",
	})
	require.NoError(t, err)
	require.Equal(t, petsRequest{
		Action: "list",
		Format: "json",
		Output: "manifest.json",
		Target: "project",
		Path:   "config.json",
	}, request)

	request, err = parsePetsArgs([]string{
		"example",
		"--output-format=text",
		"--output=manifest.json",
		"--target=user",
		"--path=config.json",
	})
	require.NoError(t, err)
	require.Equal(t, petsRequest{
		Action: "example",
		Format: "text",
		Output: "manifest.json",
		Target: "user",
		Path:   "config.json",
	}, request)

	for _, args := range [][]string{
		{"use"},
		{"off", "codog"},
		{"off", "extra", "argument"},
		{"unknown"},
		{"--output"},
		{"--target"},
		{"--path"},
		{"--output-format"},
		{"--output-format", "yaml"},
		{"--unknown"},
	} {
		_, err = parsePetsArgs(args)
		require.Error(t, err, args)
	}
}

func TestPetsUseOffAndExample(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{Workspace: t.TempDir(), Config: config.Config{ConfigHome: configHome}, Out: &out}

	require.NoError(t, app.Pets([]string{"use", companion.BuiltinID, "--json"}))
	require.Equal(t, companion.BuiltinID, app.Config.TUIPet)
	var selected petsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &selected))
	require.True(t, selected.Enabled)

	out.Reset()
	require.NoError(t, app.Pets([]string{"off"}))
	require.Equal(t, companion.DisabledID, app.Config.TUIPet)
	require.Contains(t, out.String(), "Enabled      false")

	out.Reset()
	require.NoError(t, app.Pets([]string{"example", "--json"}))
	var example petsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &example))
	require.FileExists(t, example.ManifestPath)
	loaded, err := companion.Load(configHome, "helper")
	require.NoError(t, err)
	require.Equal(t, "Helper", loaded.Name)
	require.Error(t, app.Pets([]string{"example"}))
}

func TestPetsLoadsCustomManifestAndBuildsPicker(t *testing.T) {
	configHome := t.TempDir()
	path := filepath.Join(configHome, "pets", "local", "pet.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{
		"id":"local",
		"name":"Local",
		"frames":{"ready":["[ok]"]}
	}`), 0o600))
	app := &App{Workspace: t.TempDir(), Config: config.Config{ConfigHome: configHome}, Out: &bytes.Buffer{}}

	require.NoError(t, app.Pets([]string{"use", "local"}))
	view := app.tuiPetPickerView()
	require.Len(t, view.Tabs, 1)
	require.Len(t, view.Tabs[0].Items, 3)
	require.Equal(t, "selected", view.Tabs[0].Items[2].Value)
}

func TestPetsStatusListAndErrors(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Workspace: t.TempDir(),
		Config: config.Config{
			ConfigHome: configHome,
			TUIPet:     companion.BuiltinID,
		},
		Out: &out,
	}

	require.NoError(t, app.Pets([]string{"status"}))
	require.Contains(t, out.String(), "Codog Terminal Companions")
	require.Contains(t, out.String(), "built-in")

	out.Reset()
	require.NoError(t, app.Pets([]string{"list", "--json"}))
	var report petsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "list", report.Action)
	require.NotEmpty(t, report.Companions)

	require.Error(t, app.Pets([]string{"--unknown"}))
	require.Error(t, app.Pets([]string{"use", "missing"}))
	_, _, err := app.applyPets(petsRequest{Action: "invalid"})
	require.Error(t, err)
	_, _, err = app.applyPets(petsRequest{Action: "off", Target: "invalid"})
	require.Error(t, err)
}

func TestPetsExampleAtExplicitOutputAndTextReportPaths(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Workspace: workspace,
		Config:    config.Config{ConfigHome: t.TempDir()},
		Out:       &out,
	}

	require.NoError(t, app.Pets([]string{"example", "--output", "custom/pet.json"}))
	require.FileExists(t, filepath.Join(workspace, "custom", "pet.json"))
	require.Contains(t, out.String(), "Manifest")

	out.Reset()
	require.NoError(t, renderPetsReport(&out, "text", petsReport{
		Selected:   companion.BuiltinID,
		Enabled:    true,
		ConfigPath: "config.json",
		Companions: []companion.CatalogEntry{
			{ID: "codog", Name: "Codog", Builtin: true},
			{ID: "local", Name: "Local"},
		},
	}))
	require.Contains(t, out.String(), "Config")
	require.Contains(t, out.String(), "local")
}
