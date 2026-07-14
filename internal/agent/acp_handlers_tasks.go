package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/Rememorio/codog/internal/acpserver"
	"github.com/Rememorio/codog/internal/agentruns"
	"github.com/Rememorio/codog/internal/background"
)

func (a *App) addACPBackgroundLookupHandlers(h *acpserver.Handlers) {
	h.BackgroundList = func(_ context.Context, req acpserver.BackgroundListRequest) (any, error) {
		store, err := a.acpBackgroundStore()
		if err != nil {
			return nil, err
		}
		tasks, err := store.List()
		if err != nil {
			return nil, err
		}
		tasks = background.FilterBySession(tasks, req.SessionID)
		tasks = background.FilterByKind(tasks, req.Kind)
		return tasks, nil
	}
	h.BackgroundRun = func(_ context.Context, req acpserver.BackgroundRunRequest) (any, error) {
		store, err := a.acpBackgroundStore()
		if err != nil {
			return nil, err
		}
		task, err := store.RunWithOptions(req.Command, a.Workspace, background.RunOptions{
			Kind:          req.Kind,
			SessionID:     req.SessionID,
			RestartPolicy: req.RestartPolicy,
			ScopeBinding:  acpScopeBinding(req.ScopeBinding, req.Owner, req.WorkflowScope, req.WatcherAction),
		})
		if err != nil {
			return nil, err
		}
		a.runTaskCreatedHook(context.Background(), task)
		a.runNotificationHook(context.Background(), "background_task_started", "Background task started", fmt.Sprintf("Background task %s started: %s", task.ID, task.Command))
		return task, nil
	}
}

func (a *App) addACPBackgroundStatusHandlers(h *acpserver.Handlers) {
	h.BackgroundGet = func(_ context.Context, req acpserver.BackgroundIDRequest) (any, error) {
		store, err := a.acpBackgroundStore()
		if err != nil {
			return nil, err
		}
		return store.Status(req.ID)
	}
	h.BackgroundLogs = func(_ context.Context, req acpserver.BackgroundLogsRequest) (any, error) {
		store, err := a.acpBackgroundStore()
		if err != nil {
			return nil, err
		}
		limit := req.Limit
		if limit <= 0 {
			limit = 64 * 1024
		}
		logs, err := store.Logs(req.ID, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": req.ID, "logs": logs}, nil
	}
	h.BackgroundBoard = func(_ context.Context, req acpserver.BackgroundBoardRequest) (any, error) {
		store, err := a.acpBackgroundStore()
		if err != nil {
			return nil, err
		}
		stalledAfter := 30 * time.Second
		switch {
		case req.StalledAfterMS > 0:
			stalledAfter = time.Duration(req.StalledAfterMS) * time.Millisecond
		case req.StalledAfterSeconds > 0:
			stalledAfter = time.Duration(req.StalledAfterSeconds) * time.Second
		}
		return store.LaneBoard(stalledAfter)
	}
}

func (a *App) addACPBackgroundLifecycleHandlers(h *acpserver.Handlers) {
	h.BackgroundHeartbeat = func(_ context.Context, req acpserver.BackgroundHeartbeatRequest) (any, error) {
		store, err := a.acpBackgroundStore()
		if err != nil {
			return nil, err
		}
		transportAlive := true
		if req.TransportAlive != nil {
			transportAlive = *req.TransportAlive
		}
		heartbeat := background.LaneHeartbeat{
			TransportAlive: transportAlive,
			Status:         req.Status,
			Provenance:     acpHeartbeatProvenance(req.Provenance, req.SourceKind, req.Environment, req.Channel, req.Emitter, req.Confidence),
		}
		if req.ObservedAt != nil {
			heartbeat.ObservedAt = *req.ObservedAt
		}
		return store.UpdateHeartbeat(req.ID, heartbeat)
	}
	h.BackgroundStop = func(_ context.Context, req acpserver.BackgroundIDRequest) (any, error) {
		store, err := a.acpBackgroundStore()
		if err != nil {
			return nil, err
		}
		task, err := store.Stop(req.ID)
		if err != nil {
			return nil, err
		}
		a.runTaskCompletedHook(context.Background(), task, "manual")
		a.runNotificationHook(context.Background(), "background_task_stopped", "Background task stopped", fmt.Sprintf("Background task %s stopped: %s", task.ID, task.Command))
		if task.Kind == "agent" {
			a.runSubagentStopHook(context.Background(), task.ID, subagentTypeForTask(task), task.LogPath, lastBackgroundLogLine(store, task), false)
		}
		return task, nil
	}
	h.BackgroundRestart = func(_ context.Context, req acpserver.BackgroundIDRequest) (any, error) {
		store, err := a.acpBackgroundStore()
		if err != nil {
			return nil, err
		}
		task, err := store.Restart(req.ID, a.Workspace)
		if err != nil {
			return nil, err
		}
		a.runTaskCreatedHook(context.Background(), task)
		a.runNotificationHook(context.Background(), "background_task_restarted", "Background task restarted", fmt.Sprintf("Background task %s restarted: %s", task.ID, task.Command))
		return task, nil
	}
}

func (a *App) addACPBackgroundMaintenanceHandlers(h *acpserver.Handlers) {
	h.BackgroundPrune = func(_ context.Context, req acpserver.BackgroundPruneRequest) (any, error) {
		store, err := a.acpBackgroundStore()
		if err != nil {
			return nil, err
		}
		options := background.DefaultPruneOptions()
		switch {
		case req.OlderThanSeconds > 0:
			options.OlderThan = time.Duration(req.OlderThanSeconds) * time.Second
		case req.OlderThanDays > 0:
			options.OlderThan = time.Duration(req.OlderThanDays) * 24 * time.Hour
		}
		if req.Keep != nil {
			options.Keep = *req.Keep
		}
		return store.Prune(options)
	}
	h.BackgroundSupervise = func(_ context.Context, req acpserver.BackgroundSuperviseRequest) (any, error) {
		store, err := a.acpBackgroundStore()
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		if req.Now != nil {
			now = req.Now.UTC()
		}
		result, err := store.SuperviseOnce(now)
		if err != nil {
			return nil, err
		}
		for _, task := range result.Restarted {
			a.runTaskCreatedHook(context.Background(), task)
			a.runNotificationHook(context.Background(), "background_task_restarted", "Background task restarted", fmt.Sprintf("Background task %s restarted: %s", task.ID, task.Command))
		}
		return result, nil
	}
	h.BackgroundWatch = func(ctx context.Context, req acpserver.BackgroundWatchRequest, emit func(background.WatchEvent) error) (any, error) {
		store, err := a.acpBackgroundStore()
		if err != nil {
			return nil, err
		}
		options := background.WatchOptions{
			Offset:    req.Offset,
			MaxEvents: req.MaxEvents,
		}
		if req.IntervalMS > 0 {
			options.Interval = time.Duration(req.IntervalMS) * time.Millisecond
		}
		events := 0
		err = store.Watch(ctx, req.ID, options, func(event background.WatchEvent) error {
			events++
			return emit(event)
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": req.ID, "events": events}, nil
	}
}

func (a *App) addACPAgentRunsLookupHandlers(h *acpserver.Handlers) {
	h.AgentRunsList = func(_ context.Context, req acpserver.AgentRunsListRequest) (any, error) {
		runStore, taskStore, err := a.acpAgentRunStores()
		if err != nil {
			return nil, err
		}
		runs, err := runStore.List()
		if err != nil {
			return nil, err
		}
		runs = filterACPAgentRuns(runs, req.Agent, req.SessionID)
		statuses := make([]agentruns.Status, 0, len(runs))
		for _, run := range runs {
			statuses = append(statuses, agentruns.StatusForTask(taskStore, run))
		}
		return statuses, nil
	}
	h.AgentRunsGet = func(_ context.Context, req acpserver.AgentRunIDRequest) (any, error) {
		runStore, taskStore, err := a.acpAgentRunStores()
		if err != nil {
			return nil, err
		}
		run, err := runStore.Get(req.ID)
		if err != nil {
			return nil, err
		}
		return agentruns.StatusForTask(taskStore, run), nil
	}
}

func (a *App) addACPAgentRunsStatusHandlers(h *acpserver.Handlers) {
	h.AgentRunsLogs = func(_ context.Context, req acpserver.AgentRunLogsRequest) (any, error) {
		runStore, taskStore, err := a.acpAgentRunStores()
		if err != nil {
			return nil, err
		}
		run, err := runStore.Get(req.ID)
		if err != nil {
			return nil, err
		}
		limit := req.Limit
		if limit <= 0 {
			limit = 64 * 1024
		}
		logs, err := taskStore.Logs(run.TaskID, limit)
		if err != nil {
			return nil, err
		}
		_, _ = runStore.Touch(run.ID)
		return map[string]any{"id": req.ID, "task_id": run.TaskID, "logs": logs}, nil
	}
	h.AgentRunsBoard = func(_ context.Context, req acpserver.AgentRunsBoardRequest) (any, error) {
		runStore, taskStore, err := a.acpAgentRunStores()
		if err != nil {
			return nil, err
		}
		runs, err := runStore.List()
		if err != nil {
			return nil, err
		}
		runs = filterACPAgentRuns(runs, req.Agent, req.SessionID)
		stalledAfter := 30 * time.Second
		switch {
		case req.StalledAfterMS > 0:
			stalledAfter = time.Duration(req.StalledAfterMS) * time.Millisecond
		case req.StalledAfterSeconds > 0:
			stalledAfter = time.Duration(req.StalledAfterSeconds) * time.Second
		}
		return agentruns.BuildBoard(taskStore, runs, time.Now().UTC(), stalledAfter), nil
	}
}

func (a *App) addACPAgentRunsHeartbeatHandler(h *acpserver.Handlers) {
	h.AgentRunsHeartbeat = func(_ context.Context, req acpserver.AgentRunHeartbeatRequest) (any, error) {
		runStore, taskStore, err := a.acpAgentRunStores()
		if err != nil {
			return nil, err
		}
		run, err := runStore.Get(req.ID)
		if err != nil {
			return nil, err
		}
		transportAlive := true
		if req.TransportAlive != nil {
			transportAlive = *req.TransportAlive
		}
		heartbeat := background.LaneHeartbeat{
			TransportAlive: transportAlive,
			Status:         req.Status,
			Provenance:     acpHeartbeatProvenance(req.Provenance, req.SourceKind, req.Environment, req.Channel, req.Emitter, req.Confidence),
		}
		if req.ObservedAt != nil {
			heartbeat.ObservedAt = *req.ObservedAt
		}
		task, err := taskStore.UpdateHeartbeat(run.TaskID, heartbeat)
		if err != nil {
			return nil, err
		}
		run, err = runStore.Touch(run.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"run": run, "task": task}, nil
	}
}

func (a *App) addACPAgentRunsMaintenanceHandlers(h *acpserver.Handlers) {
	h.AgentRunsStop = func(_ context.Context, req acpserver.AgentRunIDRequest) (any, error) {
		runStore, taskStore, err := a.acpAgentRunStores()
		if err != nil {
			return nil, err
		}
		run, err := runStore.Get(req.ID)
		if err != nil {
			return nil, err
		}
		task, err := taskStore.Stop(run.TaskID)
		if err != nil {
			return nil, err
		}
		run, err = runStore.Touch(run.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"run": run, "task": task}, nil
	}
	h.AgentRunsPrune = func(_ context.Context, req acpserver.AgentRunsPruneRequest) (any, error) {
		runStore, taskStore, err := a.acpAgentRunStores()
		if err != nil {
			return nil, err
		}
		options := background.DefaultPruneOptions()
		switch {
		case req.OlderThanSeconds > 0:
			options.OlderThan = time.Duration(req.OlderThanSeconds) * time.Second
		case req.OlderThanDays > 0:
			options.OlderThan = time.Duration(req.OlderThanDays) * 24 * time.Hour
		}
		if req.Keep != nil {
			options.Keep = *req.Keep
		}
		return agentruns.Prune(runStore, taskStore, options)
	}
}
