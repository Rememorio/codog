package agent

import (
	"encoding/json"

	"github.com/Rememorio/codog/internal/hooks"
)

func hooksPayloadFromRequest(req hooksRequest) hooks.Payload {
	return hooks.Payload{
		Event:            req.Event,
		Tool:             req.Tool,
		ToolName:         req.Tool,
		ToolInput:        json.RawMessage(req.Input),
		Input:            req.Input,
		Output:           req.Output,
		IsError:          req.IsError,
		Reason:           req.Reason,
		Message:          req.Input,
		Title:            req.Title,
		NotificationType: req.NotificationType,
		AgentID:          req.AgentID,
		AgentType:        req.AgentType,
		TranscriptPath:   req.TranscriptPath,
		LastAssistant:    req.LastAssistant,
		WorktreeID:       req.WorktreeID,
		WorktreePath:     req.WorktreePath,
		Ref:              req.Ref,
		OldCWD:           req.OldCWD,
		NewCWD:           req.NewCWD,
		TaskID:           req.TaskID,
		TaskKind:         req.TaskKind,
		TaskStatus:       req.TaskStatus,
		FilePath:         req.FilePath,
		Operation:        req.Operation,
		MemoryType:       req.MemoryType,
		LoadReason:       req.LoadReason,
		Globs:            append([]string(nil), req.Globs...),
		TriggerFilePath:  req.TriggerFilePath,
		ParentFilePath:   req.ParentFilePath,
		StopHookActive:   req.StopHookActive,
	}
}

func hooksPayloadForRun(req hooksRequest) hooks.Payload {
	payload := hooksPayloadFromRequest(req)
	switch req.Event {
	case "notification":
		payload.NotificationType = firstNonEmpty(req.NotificationType, req.Tool, "generic")
		payload.Tool = payload.NotificationType
		payload.Message = req.Input
		clearHookAgentFields(&payload)
		payload.Reason = ""
		clearHookToolFields(&payload)
	case "subagent_start", "subagent_stop":
		payload.AgentType = firstNonEmpty(req.AgentType, req.Tool, "general")
		payload.Tool = payload.AgentType
		payload.Input = req.Input
		clearHookDisplayFields(&payload)
		payload.Reason = ""
		clearHookToolFields(&payload)
	case "permission_request", "permission_denied":
		payload.ToolName = req.Tool
		payload.Tool = req.Tool
		clearHookDisplayFields(&payload)
		clearHookAgentFields(&payload)
	case "session_end", "setup":
		payload.Tool = ""
		clearHookDisplayFields(&payload)
		clearHookAgentFields(&payload)
		clearHookToolFields(&payload)
		if req.Event == "setup" {
			payload.Reason = ""
		}
	case "stop_failure":
		payload.Tool = ""
		clearHookDisplayFields(&payload)
		clearHookAgentFields(&payload)
		clearHookToolFields(&payload)
		payload.IsError = true
	case "worktree_create", "worktree_remove":
		payload.WorktreeID = firstNonEmpty(req.WorktreeID, req.Tool)
		payload.Tool = payload.WorktreeID
		clearHookDisplayFields(&payload)
		clearHookAgentFields(&payload)
		clearHookToolFields(&payload)
		if req.Event == "worktree_create" {
			payload.Reason = ""
		}
	case "cwd_changed":
		payload.OldCWD = req.OldCWD
		payload.NewCWD = firstNonEmpty(req.NewCWD, req.Tool)
		payload.Tool = payload.NewCWD
		clearHookDisplayFields(&payload)
		clearHookAgentFields(&payload)
		clearHookToolFields(&payload)
		payload.Reason = ""
	case "task_created", "task_completed":
		payload.TaskID = firstNonEmpty(req.TaskID, req.Tool)
		payload.TaskKind = firstNonEmpty(req.TaskKind, req.AgentType, "background")
		payload.Tool = firstNonEmpty(payload.TaskKind, payload.TaskID)
		clearHookDisplayFields(&payload)
		clearHookAgentFields(&payload)
		clearHookToolFields(&payload)
		if req.Event == "task_created" {
			payload.Reason = ""
		}
	case "file_changed":
		payload.Operation = firstNonEmpty(req.Operation, req.Tool, "write_file")
		payload.Tool = payload.Operation
		payload.ToolName = payload.Operation
		payload.FilePath = req.FilePath
		clearHookDisplayFields(&payload)
		clearHookAgentFields(&payload)
		payload.Reason = ""
	case "instructions_loaded":
		payload.LoadReason = firstNonEmpty(req.LoadReason, req.Tool, "session_start")
		payload.Tool = payload.LoadReason
		payload.FilePath = req.FilePath
		payload.MemoryType = firstNonEmpty(req.MemoryType, "Project")
		payload.Globs = append([]string(nil), req.Globs...)
		payload.TriggerFilePath = req.TriggerFilePath
		payload.ParentFilePath = req.ParentFilePath
		clearHookDisplayFields(&payload)
		clearHookAgentFields(&payload)
		payload.Reason = ""
		clearHookToolFields(&payload)
	default:
		clearHookDisplayFields(&payload)
		clearHookAgentFields(&payload)
		payload.Reason = ""
		clearHookToolFields(&payload)
		clearHookScopedFields(&payload)
	}
	clearUnusedHookScopedFields(&payload, req.Event)
	return payload
}

func clearHookDisplayFields(payload *hooks.Payload) {
	payload.Message = ""
	payload.Title = ""
	payload.NotificationType = ""
}

func clearHookAgentFields(payload *hooks.Payload) {
	payload.AgentID = ""
	payload.AgentType = ""
	payload.TranscriptPath = ""
	payload.LastAssistant = ""
	payload.StopHookActive = false
}

func clearHookToolFields(payload *hooks.Payload) {
	payload.ToolName = ""
	payload.ToolInput = nil
}

func clearHookScopedFields(payload *hooks.Payload) {
	payload.WorktreeID = ""
	payload.WorktreePath = ""
	payload.Ref = ""
	payload.OldCWD = ""
	payload.NewCWD = ""
	payload.TaskID = ""
	payload.TaskKind = ""
	payload.TaskStatus = ""
	payload.FilePath = ""
	payload.Operation = ""
	payload.MemoryType = ""
	payload.LoadReason = ""
	payload.Globs = nil
	payload.TriggerFilePath = ""
	payload.ParentFilePath = ""
}

func clearUnusedHookScopedFields(payload *hooks.Payload, event string) {
	if event != "worktree_create" && event != "worktree_remove" {
		payload.WorktreeID = ""
		payload.WorktreePath = ""
		payload.Ref = ""
	}
	if event != "cwd_changed" {
		payload.OldCWD = ""
		payload.NewCWD = ""
	}
	if event != "task_created" && event != "task_completed" {
		payload.TaskID = ""
		payload.TaskKind = ""
		payload.TaskStatus = ""
	}
	if event != "file_changed" && event != "instructions_loaded" {
		payload.FilePath = ""
	}
	if event != "file_changed" {
		payload.Operation = ""
	}
	if event != "instructions_loaded" {
		payload.MemoryType = ""
		payload.LoadReason = ""
		payload.Globs = nil
		payload.TriggerFilePath = ""
		payload.ParentFilePath = ""
	}
}
