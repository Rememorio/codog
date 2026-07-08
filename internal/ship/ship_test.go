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

func TestNewReportAppliesDefaultsAndPreservesExplicitRange(t *testing.T) {
	before := time.Now().UTC()
	report := NewReport("", Provenance{
		CommitRange: "custom-range",
		FirstCommit: "aaa",
		LastCommit:  "bbb",
		CommitCount: 3,
	}, Classification{}, time.Time{})
	after := time.Now().UTC()

	require.Equal(t, "planned", report.Status)
	require.Equal(t, "custom-range", report.Provenance.CommitRange)
	require.Contains(t, report.Summary, "3 intentional")
	require.Contains(t, report.Summary, "via unknown")
	require.Len(t, report.Events, 5)
	for _, event := range report.Events {
		require.Equal(t, "planned", event.Status)
		require.False(t, event.At.Before(before.Add(-time.Second)))
		require.False(t, event.At.After(after.Add(time.Second)))
		require.Equal(t, "custom-range", event.Provenance.CommitRange)
	}
}

func TestSummaryAndNormalizeRangeFallbacks(t *testing.T) {
	require.Equal(t, "", normalizeRange(Provenance{}))
	require.Equal(t, "bbb", normalizeRange(Provenance{LastCommit: "bbb"}))
	require.Equal(t, "aaa", normalizeRange(Provenance{FirstCommit: "aaa"}))
	require.Equal(t, "aaa", normalizeRange(Provenance{FirstCommit: "aaa", LastCommit: "aaa"}))
	require.Equal(t, "aaa..bbb", normalizeRange(Provenance{FirstCommit: " aaa ", LastCommit: " bbb "}))
	require.Equal(t, "manual", normalizeRange(Provenance{CommitRange: "manual", FirstCommit: "aaa", LastCommit: "bbb"}))

	require.Equal(t, "4 intentional commit(s), 0 rider(s), via squash", Summary(
		Provenance{CommitCount: 4, MergeMethod: "squash"},
		Classification{},
	))
	require.Equal(t, "1 intentional commit(s), 2 rider(s), via unknown", Summary(
		Provenance{},
		Classification{Intentional: 1, Riders: 2},
	))
}
