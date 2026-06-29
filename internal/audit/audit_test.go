package audit

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStoreAppendAndList(t *testing.T) {
	store := &Store{Path: filepath.Join(t.TempDir(), "events.jsonl")}
	require.NoError(t, store.Append(Event{Type: "permission", Time: time.Unix(1, 0).UTC(), ToolName: "bash", Allowed: Bool(false)}))
	require.NoError(t, store.Append(Event{Type: "tool_use", Time: time.Unix(2, 0).UTC(), ToolName: "grep"}))

	events, err := store.List(2)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, "tool_use", events[0].Type)
	require.Equal(t, "grep", events[0].ToolName)
	require.NotNil(t, events[1].Allowed)
	require.False(t, *events[1].Allowed)
}

func TestStoreListMissingFileReturnsEmptySlice(t *testing.T) {
	store := &Store{Path: filepath.Join(t.TempDir(), "missing.jsonl")}
	events, err := store.List(10)
	require.NoError(t, err)
	require.NotNil(t, events)
	require.Empty(t, events)
}

func TestClip(t *testing.T) {
	require.Equal(t, "abc", Clip("abc", 10))
	require.Equal(t, "ab... [truncated]", Clip("abcd", 2))
}
