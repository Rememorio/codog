package nudges

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestObserveAndAcknowledgeNudgeCycle(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	delivery := Delivery{
		NudgeID:     "dogfood",
		CycleID:     "cycle-1",
		Prompt:      "check status",
		DeliveredAt: now,
	}

	first, err := store.Observe(delivery)
	require.NoError(t, err)
	require.Equal(t, StateNew, first.State)
	require.False(t, first.Acknowledged)
	require.False(t, first.Stale)
	require.Equal(t, 1, first.DeliveryCount)
	require.NotEmpty(t, first.Fingerprint)

	retry, err := store.Observe(Delivery{
		NudgeID:     "dogfood",
		CycleID:     "cycle-1",
		Prompt:      "check status",
		DeliveredAt: now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, StateRetry, retry.State)
	require.Equal(t, 2, retry.DeliveryCount)
	require.False(t, retry.Acknowledged)

	ack, err := store.Acknowledge(Delivery{
		NudgeID:     "dogfood",
		CycleID:     "cycle-1",
		ResponseID:  "response-1",
		DeliveredAt: now.Add(2 * time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, StateRetry, ack.State)
	require.True(t, ack.Acknowledged)
	require.False(t, ack.Stale)
	require.Equal(t, "response-1", ack.ResponseID)

	stale, err := store.Observe(Delivery{
		NudgeID:     "dogfood",
		CycleID:     "cycle-1",
		DeliveredAt: now.Add(3 * time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, StateStaleDuplicate, stale.State)
	require.True(t, stale.AlreadyAcknowledged)
	require.True(t, stale.Stale)
	require.Equal(t, 4, stale.DeliveryCount)

	records, err := store.List()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "dogfood", records[0].NudgeID)
	require.True(t, records[0].Acknowledged)
}

func TestRejectsUnsafeNudgeIDs(t *testing.T) {
	store := NewStore(t.TempDir())

	_, err := store.Observe(Delivery{NudgeID: "../bad", CycleID: "cycle"})
	require.ErrorContains(t, err, "path components")
}
