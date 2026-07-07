package ship

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewReportBuildsShipEventsAndLaneEvents(t *testing.T) {
	at := time.Date(2026, 7, 7, 1, 2, 3, 0, time.UTC)
	report := NewReport("confirmed", Provenance{
		SourceBranch: "topic",
		BaseBranch:   "main",
		FirstCommit:  "aaa",
		LastCommit:   "bbb",
		CommitCount:  2,
		MergeMethod:  "direct_push",
		Actor:        "Codog Test <codog@example.test>",
	}, Classification{Intentional: 2, Riders: 3}, at)

	require.Equal(t, "ship_provenance", report.Kind)
	require.Equal(t, "confirmed", report.Status)
	require.Equal(t, "aaa..bbb", report.Provenance.CommitRange)
	require.Contains(t, report.Summary, "2 intentional")
	require.Contains(t, report.Summary, "3 rider")
	require.Len(t, report.Events, 5)
	require.Equal(t, EventPrepared, report.Events[0].Event)
	require.Equal(t, EventProvenance, report.Events[4].Event)

	lane := LaneEvents(report)
	require.Len(t, lane, 5)
	require.Equal(t, EventPrepared, lane[0].LaneEvent)
	require.Equal(t, report.Summary, lane[0].Message)
	require.Equal(t, "codog", lane[0].Provenance.Emitter)
	require.Contains(t, lane[0].Evidence, "ship")
}
