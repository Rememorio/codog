package cron

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStoreCreateListDeleteCronEntries(t *testing.T) {
	store := NewStore(t.TempDir())

	entry, err := store.Create("0 9 * * 1", "review weekly status", "weekly review")
	require.NoError(t, err)
	require.NotEmpty(t, entry.ID)
	require.True(t, entry.Enabled)
	require.Equal(t, "0 9 * * 1", entry.Schedule)
	require.Equal(t, "review weekly status", entry.Prompt)

	entries, err := store.List()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, entry.ID, entries[0].ID)

	deleted, err := store.Delete(entry.ID)
	require.NoError(t, err)
	require.Equal(t, entry.ID, deleted.ID)
	entries, err = store.List()
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestStoreRejectsInvalidCronInput(t *testing.T) {
	store := NewStore(t.TempDir())

	_, err := store.Create("", "prompt", "")
	require.Error(t, err)
	_, err = store.Create("* * * * *", "", "")
	require.Error(t, err)
	_, err = store.Delete("../bad")
	require.Error(t, err)
}

func TestStoreDueAndMarkRun(t *testing.T) {
	store := NewStore(t.TempDir())
	entry, err := store.Create("*/15 * * * *", "review status", "")
	require.NoError(t, err)
	now := time.Date(2026, 6, 30, 9, 30, 12, 0, time.UTC)

	due, err := store.Due(now)
	require.NoError(t, err)
	require.Len(t, due, 1)
	require.Equal(t, entry.ID, due[0].ID)

	updated, err := store.MarkRun(entry.ID, now)
	require.NoError(t, err)
	require.Equal(t, 1, updated.RunCount)
	require.NotNil(t, updated.LastRunAt)
	due, err = store.Due(now.Add(30 * time.Second))
	require.NoError(t, err)
	require.Empty(t, due)
	due, err = store.Due(now.Add(15 * time.Minute))
	require.NoError(t, err)
	require.Len(t, due, 1)
}

func TestIsDueSupportsDescriptorsAndEvery(t *testing.T) {
	now := time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC)
	require.True(t, IsDue(Entry{Schedule: "@hourly", Enabled: true}, now))
	require.False(t, IsDue(Entry{Schedule: "@daily", Enabled: true}, now))
	lastRun := now.Add(-2 * time.Hour)
	require.True(t, IsDue(Entry{Schedule: "@every 1h", Enabled: true, LastRunAt: &lastRun}, now))
	lastRun = now.Add(-30 * time.Minute)
	require.False(t, IsDue(Entry{Schedule: "@every 1h", Enabled: true, LastRunAt: &lastRun}, now))
	require.True(t, IsDue(Entry{Schedule: "0 10 * * 2", Enabled: true}, now))
	require.True(t, IsDue(Entry{Schedule: "0 10 * * 1-3", Enabled: true}, now))
	require.False(t, IsDue(Entry{Schedule: "5 10 * * 2", Enabled: true}, now))
	require.False(t, IsDue(Entry{Schedule: "0 10 * * 2", Enabled: false}, now))
}
