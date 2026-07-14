package provisional

import (
	"bufio"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestObserveSuppressesRepeatedProvisionalInsideWindow(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 7, 17, 0, 0, 0, time.UTC)

	first, err := store.Observe(Update{
		Channel:       "dogfood",
		Owner:         "worker-1",
		ProgressState: "implementing",
		Message:       "working on it",
		ObservedAt:    now,
		Window:        5 * time.Minute,
	})
	require.NoError(t, err)
	require.True(t, first.Exposed)
	require.Equal(t, DecisionNew, first.Decision)
	require.Equal(t, "in_flight", first.Event.Status)

	second, err := store.Observe(Update{
		Channel:       "dogfood",
		Owner:         "worker-1",
		ProgressState: "implementing",
		Message:       "please wait while I continue",
		ObservedAt:    now.Add(time.Minute),
		Window:        5 * time.Minute,
	})
	require.NoError(t, err)
	require.False(t, second.Exposed)
	require.Equal(t, DecisionSuppressedDuplicate, second.Decision)
	require.Equal(t, first.Fingerprint, second.Fingerprint)
	require.Equal(t, 2, second.State.RawEventCount)
	require.Equal(t, 1, second.State.SuppressedCount)
	require.Equal(t, first.Event.EventID, second.State.LastExposedEvent.EventID)
	require.NotEqual(t, first.Event.EventID, second.Event.EventID)

	auditPath, err := store.auditPath("dogfood")
	require.NoError(t, err)
	require.Equal(t, 2, countLines(t, auditPath))
}

func TestObserveExposesMaterialChangesAndAfterWindowRepeats(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 7, 17, 0, 0, 0, time.UTC)
	base := Update{
		Channel:       "dogfood",
		Owner:         "worker-1",
		ProgressState: "implementing",
		Status:        "working_on_it",
		ObservedAt:    now,
		Window:        5 * time.Minute,
	}
	first, err := store.Observe(base)
	require.NoError(t, err)

	changed := base
	changed.Blocker = "waiting for CI"
	changed.ObservedAt = now.Add(2 * time.Minute)
	second, err := store.Observe(changed)
	require.NoError(t, err)
	require.True(t, second.Exposed)
	require.Equal(t, DecisionMaterialChange, second.Decision)
	require.NotEqual(t, first.Fingerprint, second.Fingerprint)
	require.Equal(t, 1, second.State.RepeatCount)

	repeated := changed
	repeated.ObservedAt = now.Add(10 * time.Minute)
	third, err := store.Observe(repeated)
	require.NoError(t, err)
	require.True(t, third.Exposed)
	require.Equal(t, DecisionRepeatedAfterWindow, third.Decision)
	require.Equal(t, second.Fingerprint, third.Fingerprint)
	require.Equal(t, 2, third.State.RepeatCount)
	require.Equal(t, third.Event.EventID, third.State.LastExposedEvent.EventID)
}

func TestObserveEscalatesUnchangedProvisionalAfterTimeout(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 7, 17, 0, 0, 0, time.UTC)

	first, err := store.Observe(Update{
		Channel:       "dogfood",
		Owner:         "worker-1",
		ProgressState: "implementing",
		Message:       "working on it",
		ObservedAt:    now,
		Window:        5 * time.Minute,
		Timeout:       2 * time.Minute,
		TimeoutPolicy: "dogfood-fast-ttl",
	})
	require.NoError(t, err)
	require.False(t, first.Stale)
	require.Equal(t, "dogfood-fast-ttl", first.State.TimeoutPolicy)
	require.Equal(t, int64(120), first.TimeoutSeconds)

	freshDuplicate, err := store.Observe(Update{
		Channel:       "dogfood",
		Owner:         "worker-1",
		ProgressState: "implementing",
		Message:       "please wait",
		ObservedAt:    now.Add(time.Minute),
		Window:        5 * time.Minute,
		Timeout:       2 * time.Minute,
		TimeoutPolicy: "dogfood-fast-ttl",
	})
	require.NoError(t, err)
	require.False(t, freshDuplicate.Stale)
	require.False(t, freshDuplicate.Exposed)
	require.Equal(t, DecisionSuppressedDuplicate, freshDuplicate.Decision)

	stale, err := store.Observe(Update{
		Channel:       "dogfood",
		Owner:         "worker-1",
		ProgressState: "implementing",
		Message:       "working on it",
		ObservedAt:    now.Add(3 * time.Minute),
		Window:        5 * time.Minute,
		Timeout:       2 * time.Minute,
		TimeoutPolicy: "dogfood-fast-ttl",
	})
	require.NoError(t, err)
	require.True(t, stale.Stale)
	require.True(t, stale.Exposed)
	require.Equal(t, DecisionStaleProvisional, stale.Decision)
	require.NotNil(t, stale.Escalation)
	require.Equal(t, "provisional_status_stale", stale.Escalation.Kind)
	require.Equal(t, "blocker", stale.Escalation.Signal)
	require.Equal(t, "dogfood-fast-ttl", stale.Escalation.Policy.ID)
	require.Equal(t, int64(60), stale.Escalation.StaleForSeconds)
	require.True(t, stale.State.Stale)
	require.Equal(t, 1, stale.State.EscalationCount)
	require.Equal(t, now, stale.State.FingerprintFirstObservedAt)

	changed := Update{
		Channel:       "dogfood",
		Owner:         "worker-1",
		ProgressState: "verifying",
		Message:       "working on it",
		ObservedAt:    now.Add(4 * time.Minute),
		Window:        5 * time.Minute,
		Timeout:       2 * time.Minute,
		TimeoutPolicy: "dogfood-fast-ttl",
	}
	changedObservation, err := store.Observe(changed)
	require.NoError(t, err)
	require.False(t, changedObservation.Stale)
	require.False(t, changedObservation.State.Stale)
	require.Equal(t, DecisionMaterialChange, changedObservation.Decision)
	require.Equal(t, changed.ObservedAt, changedObservation.State.FingerprintFirstObservedAt)
}

func TestListReturnsMostRecentlyObservedStates(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 7, 17, 0, 0, 0, time.UTC)
	_, err := store.Observe(Update{Channel: "older", Message: "working on it", ObservedAt: now})
	require.NoError(t, err)
	_, err = store.Observe(Update{Channel: "newer", Message: "working on it", ObservedAt: now.Add(time.Minute)})
	require.NoError(t, err)

	states, err := store.List()
	require.NoError(t, err)
	require.Len(t, states, 2)
	require.Equal(t, "newer", states[0].Channel)
	require.Equal(t, "older", states[1].Channel)
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = file.Close() }()
	count := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		count++
	}
	require.NoError(t, scanner.Err())
	return count
}
