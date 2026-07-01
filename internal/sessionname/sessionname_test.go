package sessionname

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSuggestBuildsReadableSlug(t *testing.T) {
	require.Equal(t, "fix-http-500-api-users", Suggest("Fix the HTTP 500 in /api/users", Options{}))
	require.Equal(t, "codog-add-provider-trace", Suggest("Add provider trace command now", Options{Prefix: "Codog", MaxWords: 3}))
	require.Equal(t, "session", Suggest("the and or", Options{}))
	require.Equal(t, "short", Suggest("short title with extra words", Options{MaxWords: 3, MaxLength: 5}))
}

func TestUniqueAppendsSuffixForCollisions(t *testing.T) {
	existing := map[string]bool{"fix-bug": true, "fix-bug-2": true}
	name, collisions, err := Unique("fix-bug", func(id string) (bool, error) {
		return existing[id], nil
	})
	require.NoError(t, err)
	require.Equal(t, "fix-bug-3", name)
	require.Equal(t, 2, collisions)
}

func TestRenderers(t *testing.T) {
	report := Report{
		Kind:        "session_name",
		Action:      "generate",
		Status:      "ok",
		SessionID:   "old",
		SuggestedID: "fix-bug",
		Source:      "first_prompt",
	}
	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "Session Name")
	require.Contains(t, out.String(), "fix-bug")

	out.Reset()
	require.NoError(t, RenderJSON(&out, report))
	require.Contains(t, out.String(), `"kind": "session_name"`)
}
