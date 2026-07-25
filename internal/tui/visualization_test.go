package tui

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFinishStreamingOutputReplacesVisualizationDirective(t *testing.T) {
	current := "Result\n\n" + `::codog-inline-vis{"file":"chart.html"}`
	m := model{
		transcript:     []transcriptEntry{{Role: "assistant", Text: current}},
		streamingIndex: 0,
	}

	m.finishStreamingOutput("assistant", "Result\n\n[Open visualization: chart](file:///viewer.html)")

	require.Len(t, m.transcript, 1)
	require.NotContains(t, m.transcript[0].Text, "::codog-inline-vis")
	require.Contains(t, m.transcript[0].Text, "file:///viewer.html")
}
