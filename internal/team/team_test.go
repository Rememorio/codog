package team

import (
	"testing"

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
