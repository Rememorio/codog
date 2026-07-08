package team

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStoreCreateListAndMarkDeleted(t *testing.T) {
	store := NewStore(t.TempDir())

	created, err := store.Create("review", []TaskSpec{{Prompt: "check auth", Description: "auth review"}}, []string{"task-1"})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	require.Equal(t, "running", created.Status)
	require.Equal(t, []string{"task-1"}, created.TaskIDs)

	all, err := store.List()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, created.ID, all[0].ID)

	deleted, err := store.MarkDeleted(created.ID)
	require.NoError(t, err)
	require.Equal(t, "deleted", deleted.Status)
	loaded, err := store.Get(created.ID)
	require.NoError(t, err)
	require.Equal(t, "deleted", loaded.Status)
}

func TestStoreRejectsInvalidTeamInput(t *testing.T) {
	store := NewStore(t.TempDir())

	_, err := store.Create("", []TaskSpec{{Prompt: "prompt"}}, nil)
	require.Error(t, err)
	_, err = store.Create("team", nil, nil)
	require.Error(t, err)
	_, err = store.Create("team", []TaskSpec{{Prompt: ""}}, nil)
	require.Error(t, err)
	_, err = store.Get("../bad")
	require.Error(t, err)
}

func TestStoreCreateTrimsInputAndDefaultsCreatedStatus(t *testing.T) {
	store := NewStore(t.TempDir())
	taskIDs := []string{"task-1"}

	created, err := store.Create("  docs  ", []TaskSpec{{
		Prompt:      "  write docs  ",
		Description: "  release notes  ",
		TaskID:      "  task-a  ",
	}}, nil)
	require.NoError(t, err)
	require.Equal(t, "docs", created.Name)
	require.Equal(t, "created", created.Status)
	require.Equal(t, TaskSpec{Prompt: "write docs", Description: "release notes", TaskID: "task-a"}, created.Tasks[0])

	running, err := store.Create("runner", []TaskSpec{{Prompt: "ship"}}, taskIDs)
	require.NoError(t, err)
	require.Equal(t, "running", running.Status)
	taskIDs[0] = "mutated"
	require.Equal(t, []string{"task-1"}, running.TaskIDs)
}

func TestStoreListSortsNewestFirstAndIgnoresNonTeamEntries(t *testing.T) {
	store := NewStore(t.TempDir())
	older := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	require.NoError(t, store.Save(Team{ID: "same_b", Name: "same b", CreatedAt: newer}))
	require.NoError(t, store.Save(Team{ID: "same_a", Name: "same a", CreatedAt: newer}))
	require.NoError(t, store.Save(Team{ID: "old", Name: "old", CreatedAt: older}))
	require.NoError(t, os.WriteFile(filepath.Join(store.dir(), "notes.txt"), []byte("ignore"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(store.dir(), "nested.json"), 0o755))

	teams, err := store.List()
	require.NoError(t, err)
	require.Equal(t, []string{"same_a", "same_b", "old"}, []string{teams[0].ID, teams[1].ID, teams[2].ID})
}

func TestStoreSaveRejectsUnsafeTeamID(t *testing.T) {
	root := t.TempDir()
	store := NewStore(filepath.Join(root, "config"))

	require.ErrorContains(t, store.Save(Team{}), "team_id is required")
	require.ErrorContains(t, store.Save(Team{ID: "../outside"}), "invalid team_id")
	require.NoFileExists(t, filepath.Join(root, "outside.json"))
}

func TestStoreMarkDeletedReportsMissingTeam(t *testing.T) {
	store := NewStore(t.TempDir())

	_, err := store.MarkDeleted("missing")
	require.ErrorContains(t, err, "team not found: missing")
}
