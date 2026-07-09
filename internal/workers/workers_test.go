package workers

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStoreManagesWorkerLifecycle(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config"))
	worker, err := store.Create(t.TempDir(), []string{"repo", "repo", " extra "}, true)
	require.NoError(t, err)
	require.NotEmpty(t, worker.ID)
	require.Equal(t, "ready_for_prompt", worker.Status)
	require.True(t, worker.ReadyForPrompt)
	require.Equal(t, []string{"repo", "extra"}, worker.TrustedRoots)
	require.Len(t, worker.Events, 1)
	require.Equal(t, int64(1), worker.Events[0].Sequence)
	require.Equal(t, "lane.started", worker.Events[0].LaneEvent)
	require.Equal(t, "live_lane", worker.Events[0].Provenance.Source)
	require.Equal(t, "codog-worker", worker.Events[0].Provenance.Emitter)

	worker, err = store.Observe(worker.ID, "trust this folder?")
	require.NoError(t, err)
	require.Equal(t, "trust_prompt", worker.Status)
	require.False(t, worker.ReadyForPrompt)
	require.Equal(t, int64(2), worker.Events[len(worker.Events)-1].Sequence)
	require.Equal(t, "lane.blocked", worker.Events[len(worker.Events)-1].LaneEvent)

	worker, err = store.ResolveTrust(worker.ID)
	require.NoError(t, err)
	require.True(t, worker.ReadyForPrompt)

	receipt := &TaskReceipt{Repo: "codog", TaskKind: "test", SourceSurface: "tool", ObjectivePreview: "run tests"}
	worker, err = store.SendPrompt(worker.ID, "run tests", receipt, "task-1")
	require.NoError(t, err)
	require.Equal(t, "running", worker.Status)
	require.Equal(t, "task-1", worker.TaskID)
	require.Equal(t, receipt, worker.TaskReceipt)
	require.Equal(t, "codog", worker.Events[len(worker.Events)-1].Binding.Owner)
	require.Equal(t, "test", worker.Events[len(worker.Events)-1].Binding.Scope)
	require.Equal(t, "act", worker.Events[len(worker.Events)-1].Binding.WatcherAction)

	snapshot, err := store.AwaitReady(worker.ID)
	require.NoError(t, err)
	require.False(t, snapshot.ReadyForPrompt)

	worker, err = store.Complete(worker.ID, "stop", 42)
	require.NoError(t, err)
	require.Equal(t, "finished", worker.Status)
	require.NotNil(t, worker.Terminal)
	require.Equal(t, "lane.finished", worker.Terminal.LaneEvent)
	require.Equal(t, "stop", worker.Terminal.FinishReason)
	require.Equal(t, 0, worker.Terminal.DuplicateCount)
	require.NotEmpty(t, worker.Events)

	list, err := store.List()
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, worker.ID, list[0].ID)
}

func TestWorkerTerminalEventsAreDedupedWithMaterialDifference(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config"))
	worker, err := store.Create(t.TempDir(), nil, false)
	require.NoError(t, err)

	worker, err = store.Complete(worker.ID, "stop", 12)
	require.NoError(t, err)
	require.NotNil(t, worker.Terminal)
	firstFingerprint := worker.Terminal.Fingerprint
	firstSequence := worker.Terminal.Sequence

	worker, err = store.Complete(worker.ID, "stop", 12)
	require.NoError(t, err)
	require.Equal(t, firstFingerprint, worker.Terminal.Fingerprint)
	require.Equal(t, firstSequence, worker.Terminal.Sequence)
	require.Equal(t, 1, worker.Terminal.DuplicateCount)
	require.False(t, worker.Events[len(worker.Events)-1].MateriallyDiffers)
	require.Equal(t, firstFingerprint, worker.Events[len(worker.Events)-1].DuplicateOf)

	worker, err = store.Complete(worker.ID, "stop", 13)
	require.NoError(t, err)
	require.Equal(t, firstFingerprint, worker.Terminal.Fingerprint)
	require.Equal(t, firstSequence, worker.Terminal.Sequence)
	require.Equal(t, 2, worker.Terminal.DuplicateCount)
	require.Equal(t, 1, worker.Terminal.MaterialDifferenceCount)
	require.True(t, worker.Events[len(worker.Events)-1].MateriallyDiffers)

	worker, err = store.Complete(worker.ID, "tool_error", 13)
	require.NoError(t, err)
	require.Equal(t, "failed", worker.Status)
	require.Equal(t, "lane.failed", worker.Terminal.LaneEvent)
	require.NotEqual(t, firstFingerprint, worker.Terminal.Fingerprint)
	require.Greater(t, worker.Terminal.Sequence, firstSequence)
}

func TestStoreRecordsStartupNoEvidenceReport(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config"))
	worker, err := store.Create(t.TempDir(), nil, true)
	require.NoError(t, err)

	worker, err = store.Observe(worker.ID, "Do you trust the files in this folder?")
	require.NoError(t, err)

	worker, err = store.ObserveStartupTimeout(worker.ID, StartupEvidence{
		PaneCommand:     "codog repl",
		TransportHealth: "transport:healthy",
		MCPHealth:       "mcp:healthy",
	})
	require.NoError(t, err)
	require.Equal(t, "failed", worker.Status)
	require.Empty(t, worker.TaskStatus)
	require.Equal(t, "startup_no_evidence: trust_required", worker.LastError)
	require.NotNil(t, worker.StartupNoEvidence)
	require.Equal(t, "worker.startup_no_evidence", worker.StartupNoEvidence.Kind)
	require.Equal(t, StartupTrustRequired, worker.StartupNoEvidence.Classification)
	require.Equal(t, []string{
		"resolve the workspace trust prompt in the worker pane",
		"restart the worker after trust is accepted",
	}, worker.StartupNoEvidence.NextActions)
	require.Equal(t, "trust_prompt", worker.StartupNoEvidence.Evidence.LastLifecycleState)
	require.Equal(t, "codog repl", worker.StartupNoEvidence.Evidence.PaneCommand)
	require.True(t, worker.StartupNoEvidence.Evidence.TrustPromptDetected)
	require.Equal(t, "transport", worker.StartupNoEvidence.Evidence.Transport.Name)
	require.Equal(t, "healthy", worker.StartupNoEvidence.Evidence.Transport.Status)
	require.Equal(t, "summary", worker.StartupNoEvidence.Evidence.Transport.Source)
	require.Equal(t, "transport:healthy", worker.StartupNoEvidence.Evidence.Transport.Summary)
	require.Equal(t, "mcp", worker.StartupNoEvidence.Evidence.MCP.Name)
	require.Equal(t, "healthy", worker.StartupNoEvidence.Evidence.MCP.Status)

	event := worker.Events[len(worker.Events)-1]
	require.Equal(t, "worker.startup_no_evidence", event.Type)
	require.Equal(t, "lane.blocked", event.LaneEvent)
	require.Equal(t, StartupTrustRequired, event.Classification)
	require.Equal(t, "trust_prompt", event.Evidence["last_lifecycle_state"])
	require.Equal(t, "healthcheck", event.Provenance.Source)
	require.NotEmpty(t, event.PayloadHash)

	reloaded, err := store.Get(worker.ID)
	require.NoError(t, err)
	require.NotNil(t, reloaded.StartupNoEvidence)
	require.Equal(t, StartupTrustRequired, reloaded.StartupNoEvidence.Classification)
	require.Equal(t, worker.StartupNoEvidence.NextActions, reloaded.StartupNoEvidence.NextActions)
}

func TestStoreStartupNoEvidenceUsesPromptSentTimestamp(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config"))
	worker, err := store.Create(t.TempDir(), nil, true)
	require.NoError(t, err)
	worker, err = store.SendPrompt(worker.ID, "run tests", nil, "")
	require.NoError(t, err)
	promptSentAt := worker.Events[len(worker.Events)-1].CreatedAt

	worker, err = store.ObserveStartupTimeout(worker.ID, StartupEvidence{
		TransportHealth: "transport:healthy",
		MCPHealth:       "mcp:healthy",
	})
	require.NoError(t, err)
	require.Equal(t, StartupPromptAcceptanceTimeout, worker.StartupNoEvidence.Classification)
	require.NotNil(t, worker.StartupNoEvidence.Evidence.PromptSentAt)
	require.True(t, promptSentAt.Equal(*worker.StartupNoEvidence.Evidence.PromptSentAt))
	require.Equal(t, "pending", worker.StartupNoEvidence.Evidence.PromptAcceptanceState)
	require.Equal(t, "summary", worker.StartupNoEvidence.Evidence.Transport.Source)
	require.Equal(t, "healthy", worker.StartupNoEvidence.Evidence.Transport.Status)
}

func TestStoreStartupNoEvidenceInfersStructuredHealth(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config"))
	worker, err := store.Create(t.TempDir(), nil, true)
	require.NoError(t, err)
	unhealthy := false

	worker, err = store.ObserveStartupTimeout(worker.ID, StartupEvidence{
		TransportHealthy: &unhealthy,
	})
	require.NoError(t, err)
	require.Equal(t, StartupTransportDead, worker.StartupNoEvidence.Classification)
	require.Equal(t, "transport:unhealthy", worker.StartupNoEvidence.Evidence.TransportHealth)
	require.Equal(t, "transport", worker.StartupNoEvidence.Evidence.Transport.Name)
	require.True(t, worker.StartupNoEvidence.Evidence.Transport.Checked)
	require.NotNil(t, worker.StartupNoEvidence.Evidence.Transport.Healthy)
	require.False(t, *worker.StartupNoEvidence.Evidence.Transport.Healthy)
	require.Equal(t, "unhealthy", worker.StartupNoEvidence.Evidence.Transport.Status)
	require.Equal(t, "inferred", worker.StartupNoEvidence.Evidence.Transport.Source)
	require.Equal(t, "transport:unhealthy", worker.StartupNoEvidence.Evidence.Transport.Summary)
	require.Equal(t, "mcp:not_checked", worker.StartupNoEvidence.Evidence.MCPHealth)
	require.Equal(t, "not_checked", worker.StartupNoEvidence.Evidence.MCP.Status)
	require.False(t, worker.StartupNoEvidence.Evidence.MCP.Checked)
	require.Contains(t, worker.StartupNoEvidence.NextActions, "restart the worker transport")

	event := worker.Events[len(worker.Events)-1]
	transport, ok := event.Evidence["transport"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "unhealthy", transport["status"])
	require.Equal(t, "inferred", transport["source"])
}

func TestStartupRecoveryActionsForUnknownIncompleteEvidence(t *testing.T) {
	actions := StartupRecoveryActions(StartupUnknown, StartupEvidence{
		Transport: StartupHealth{Name: "transport", Status: "not_checked"},
		MCP:       StartupHealth{Name: "mcp", Status: "not_checked"},
	})

	require.Contains(t, actions, "inspect worker events and pane output for the missing startup signal")
	require.Contains(t, actions, "rerun the startup healthcheck with transport and MCP probes enabled")
}

func TestClassifyStartupNoEvidence(t *testing.T) {
	now := time.Now().UTC()
	healthy := true
	unhealthy := false
	cases := []struct {
		name     string
		evidence StartupEvidence
		want     string
	}{
		{
			name: "transport dead",
			evidence: StartupEvidence{
				LastLifecycleState:    "spawning",
				PromptAcceptanceState: "unknown",
				TransportHealthy:      &unhealthy,
				MCPHealthy:            &healthy,
			},
			want: StartupTransportDead,
		},
		{
			name: "trust required",
			evidence: StartupEvidence{
				LastLifecycleState:    "trust_prompt",
				PromptAcceptanceState: "unknown",
				TrustPromptDetected:   true,
				TransportHealthy:      &healthy,
				MCPHealthy:            &healthy,
			},
			want: StartupTrustRequired,
		},
		{
			name: "prompt acceptance timeout",
			evidence: StartupEvidence{
				LastLifecycleState:    "running",
				PromptSentAt:          &now,
				PromptAcceptanceState: "pending",
				TransportHealthy:      &healthy,
				MCPHealthy:            &healthy,
			},
			want: StartupPromptAcceptanceTimeout,
		},
		{
			name: "prompt misdelivery",
			evidence: StartupEvidence{
				LastLifecycleState:    "ready_for_prompt",
				PromptSentAt:          &now,
				PromptAcceptanceState: "not_accepted",
				TransportHealthy:      &healthy,
				MCPHealthy:            &healthy,
				ElapsedSeconds:        45,
			},
			want: StartupPromptMisdelivery,
		},
		{
			name: "worker crashed",
			evidence: StartupEvidence{
				LastLifecycleState:    "spawning",
				PromptAcceptanceState: "unknown",
				TransportHealthy:      &healthy,
				MCPHealthy:            &unhealthy,
			},
			want: StartupWorkerCrashed,
		},
		{
			name: "unknown",
			evidence: StartupEvidence{
				LastLifecycleState:    "spawning",
				PromptAcceptanceState: "unknown",
				TransportHealthy:      &healthy,
				MCPHealthy:            &healthy,
			},
			want: StartupUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ClassifyStartupNoEvidence(tc.evidence))
		})
	}
}
