package companion

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadBuiltinAndDisabled(t *testing.T) {
	manifest, err := Load(t.TempDir(), BuiltinID)
	require.NoError(t, err)
	require.Equal(t, "Codog", manifest.Name)
	require.NotEmpty(t, manifest.Frame("ready"))
	require.NotEqual(t, manifest.Frame("ready"), manifest.Frame("failed"))

	manifest, err = Load(t.TempDir(), DisabledID)
	require.NoError(t, err)
	require.Nil(t, manifest)
}

func TestLoadCustomAndList(t *testing.T) {
	home := t.TempDir()
	writeManifest(t, home, Manifest{
		ID:   "helper",
		Name: "Local Helper",
		Frames: Frames{
			Ready: []string{"[o_o]", " /|\\"},
		},
	})
	writeManifest(t, home, Manifest{ID: "bad", Name: "Bad", Frames: Frames{}})

	manifest, err := Load(home, "helper")
	require.NoError(t, err)
	require.Equal(t, "Local Helper", manifest.Name)
	require.Equal(t, manifest.Frame("ready"), manifest.Frame("running"))

	require.Equal(t, []CatalogEntry{
		{ID: "codog", Name: "Codog", Builtin: true},
		{ID: "helper", Name: "Local Helper"},
	}, List(home))
}

func TestLoadRejectsUnsafeManifests(t *testing.T) {
	home := t.TempDir()
	_, err := Load(home, "../escape")
	require.ErrorIs(t, err, ErrInvalidID)

	writeManifest(t, home, Manifest{ID: "mismatch", Frames: Frames{Ready: []string{"ok"}}})
	_, err = Load(home, "other")
	require.Error(t, err)

	path := filepath.Join(home, "pets", "link")
	require.NoError(t, os.MkdirAll(path, 0o755))
	target := filepath.Join(t.TempDir(), "pet.json")
	require.NoError(t, os.WriteFile(target, []byte(`{}`), 0o644))
	require.NoError(t, os.Symlink(target, filepath.Join(path, "pet.json")))
	_, err = Load(home, "link")
	require.ErrorIs(t, err, ErrInvalidManifest)

	writeManifest(t, home, Manifest{ID: "escape", Frames: Frames{Ready: []string{"\x1b[31m"}}})
	_, err = Load(home, "escape")
	require.ErrorIs(t, err, ErrInvalidManifest)
}

func TestLoadRejectsManifestThroughEscapingDirectorySymlink(t *testing.T) {
	home := t.TempDir()
	external := t.TempDir()
	path := filepath.Join(external, "outside")
	require.NoError(t, os.MkdirAll(path, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(path, "pet.json"), []byte(`{
		"id":"outside",
		"name":"Outside",
		"frames":{"ready":["no"]}
	}`), 0o600))
	require.NoError(t, os.Symlink(external, filepath.Join(home, "pets")))

	loaded, err := Load(home, "outside")
	require.ErrorIs(t, err, ErrInvalidManifest)
	require.Nil(t, loaded)
}

func TestWriteExampleProducesLoadableShape(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, WriteExample(&out))
	var manifest Manifest
	require.NoError(t, json.Unmarshal(out.Bytes(), &manifest))
	require.Equal(t, "helper", manifest.ID)
	require.NoError(t, validateFrames(manifest.Frames))
}

func writeManifest(t *testing.T, home string, manifest Manifest) {
	t.Helper()
	path := filepath.Join(home, "pets", manifest.ID)
	require.NoError(t, os.MkdirAll(path, 0o755))
	data, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(path, "pet.json"), data, 0o600))
}
