package branchlock

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectCollisionsSameBranchSameModule(t *testing.T) {
	collisions := DetectCollisions([]Intent{
		{LaneID: "lane-a", Branch: "feature/shared", Worktree: "wt-a", Modules: []string{"runtime/mcp"}},
		{LaneID: "lane-b", Branch: "feature/shared", Worktree: "wt-b", Modules: []string{"runtime/mcp"}},
	})

	require.Equal(t, []Collision{{
		Branch:  "feature/shared",
		Module:  "runtime/mcp",
		LaneIDs: []string{"lane-a", "lane-b"},
	}}, collisions)
}

func TestDetectCollisionsNestedModuleScope(t *testing.T) {
	collisions := DetectCollisions([]Intent{
		{LaneID: "lane-a", Branch: "feature/shared", Modules: []string{"runtime"}},
		{LaneID: "lane-b", Branch: "feature/shared", Modules: []string{"runtime/mcp"}},
	})

	require.Equal(t, "runtime", collisions[0].Module)
}

func TestDetectCollisionsIgnoresDifferentBranchesAndEmptyModules(t *testing.T) {
	collisions := DetectCollisions([]Intent{
		{LaneID: "lane-a", Branch: "feature/a", Modules: []string{"runtime/mcp"}},
		{LaneID: "lane-b", Branch: "feature/b", Modules: []string{"runtime/mcp"}},
		{LaneID: "lane-c", Branch: "feature/a"},
	})

	require.Empty(t, collisions)
}

func TestDecodeAcceptsArrayEnvelopeAndCamelLaneID(t *testing.T) {
	intents, err := Decode([]byte(`{"intents":[{"laneId":"lane-a","branch":"topic","modules":["./runtime//mcp"]}]}`))

	require.NoError(t, err)
	require.Equal(t, []Intent{{
		LaneID:  "lane-a",
		Branch:  "topic",
		Modules: []string{"runtime/mcp"},
	}}, intents)
}

func TestDecodeRejectsObjectWithoutIntents(t *testing.T) {
	_, err := Decode([]byte(`{"branch":"topic"}`))

	require.Error(t, err)
	require.Contains(t, err.Error(), "intents array")
}
