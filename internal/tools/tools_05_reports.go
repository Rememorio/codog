package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	// Register image decoders for read_image and notebook image outputs.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/reporting"
	"github.com/Rememorio/codog/internal/reportschema"
	"github.com/Rememorio/codog/internal/skills"
	"github.com/Rememorio/codog/internal/todos"
)

func (ReportBackpressureTool) Permission() Permission { return PermissionReadOnly }

func (t ReportBackpressureTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Action          string   `json:"action"`
		Channel         string   `json:"channel"`
		TriggerID       string   `json:"trigger_id"`
		CheckedSurfaces []string `json:"checked_surfaces"`
		CheckedWindow   string   `json:"checked_window"`
		NegativeQueries []struct {
			ID              string   `json:"id"`
			Query           string   `json:"query"`
			CheckedSurfaces []string `json:"checked_surfaces"`
			Window          string   `json:"window"`
		} `json:"negative_queries"`
		FreshnessTTLSeconds int      `json:"freshness_ttl_seconds"`
		SnapshotID          string   `json:"snapshot_id"`
		Now                 string   `json:"now"`
		Consumer            string   `json:"consumer"`
		SchemaVersions      []string `json:"schema_versions"`
		FieldFamilies       []string `json:"field_families"`
		ProjectionView      string   `json:"projection_view"`
		ProjectionVerbosity string   `json:"projection_verbosity"`
		MaxSensitivity      string   `json:"max_sensitivity"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	action := strings.ToLower(strings.TrimSpace(payload.Action))
	if action == "" {
		action = "generate"
	}
	store := reporting.NewStore(t.ConfigHome)
	switch action {
	case "generate":
		now, err := parseOptionalRFC3339(payload.Now)
		if err != nil {
			return "", err
		}
		negativeQueries := make([]reporting.NegativeQuery, 0, len(payload.NegativeQueries))
		for _, query := range payload.NegativeQueries {
			negativeQueries = append(negativeQueries, reporting.NegativeQuery{
				ID:              query.ID,
				Query:           query.Query,
				CheckedSurfaces: query.CheckedSurfaces,
				Window:          query.Window,
			})
		}
		report, err := store.GenerateWithOptions(payload.Channel, now, reporting.GenerateOptions{
			TriggerID:       payload.TriggerID,
			CheckedSurfaces: payload.CheckedSurfaces,
			CheckedWindow:   payload.CheckedWindow,
			NegativeQueries: negativeQueries,
			FreshnessTTL:    time.Duration(payload.FreshnessTTLSeconds) * time.Second,
		})
		if err != nil {
			return "", err
		}
		if payload.Consumer != "" || len(payload.SchemaVersions) > 0 || len(payload.FieldFamilies) > 0 || payload.ProjectionView != "" || payload.ProjectionVerbosity != "" {
			projection, err := store.ProjectReportCached(report, reportschema.ConsumerCapabilities{
				Consumer:       payload.Consumer,
				SchemaVersions: payload.SchemaVersions,
				FieldFamilies:  payload.FieldFamilies,
				MaxSensitivity: payload.MaxSensitivity,
			}, payload.ProjectionView, payload.ProjectionVerbosity)
			if err != nil {
				return "", err
			}
			return pretty(projection), nil
		}
		return pretty(report), nil
	case "snapshot":
		snapshot, err := store.GetSnapshot(payload.SnapshotID)
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"kind": "report_backpressure_snapshot", "snapshot": snapshot}), nil
	default:
		return "", unknownToolActionError("report_backpressure", payload.Action, reportBackpressureActionNames)
	}
}

func parseOptionalRFC3339(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, value)
}

type TaskLaneBoardTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskLaneBoardTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_lane_board",
		Description: "Group background tasks into active, blocked, and finished lanes with heartbeat freshness.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"stalled_after_seconds": map[string]any{"type": "integer", "minimum": 1},
				"stalled_after_secs":    map[string]any{"type": "integer", "minimum": 1},
				"stalled_after_ms":      map[string]any{"type": "integer", "minimum": 1},
			},
			"additionalProperties": false,
		},
	}
}

func (TaskLaneBoardTool) Permission() Permission { return PermissionReadOnly }

func (t TaskLaneBoardTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		StalledAfterSeconds int `json:"stalled_after_seconds"`
		StalledAfterSecs    int `json:"stalled_after_secs"`
		StalledAfterMS      int `json:"stalled_after_ms"`
	}
	if len(input) != 0 && string(input) != "null" {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	stalledAfter := taskLaneBoardStalledAfter(payload.StalledAfterSeconds, payload.StalledAfterSecs, payload.StalledAfterMS)
	board, err := taskStore(t.ConfigHome, t.Workspace).LaneBoard(stalledAfter)
	if err != nil {
		return "", err
	}
	return pretty(board), nil
}

func taskLaneBoardStalledAfter(seconds int, secs int, ms int) time.Duration {
	if ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	if seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 30 * time.Second
}

type TaskStopTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskStopTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_stop",
		Description: "Stop a running background task by task id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":       map[string]any{"type": "string"},
				"task_id":  map[string]any{"type": "string"},
				"taskId":   map[string]any{"type": "string"},
				"shell_id": map[string]any{"type": "string"},
			},
			"anyOf":                taskIDRequirement("shell_id"),
			"additionalProperties": false,
		},
	}
}

func (TaskStopTool) Permission() Permission { return PermissionWorkspace }

func (t TaskStopTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		ID          string `json:"id"`
		TaskID      string `json:"task_id"`
		TaskIDAlias string `json:"taskId"`
		ShellID     string `json:"shell_id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	id := firstNonEmpty(payload.ID, payload.TaskID, payload.TaskIDAlias, payload.ShellID)
	if id == "" {
		return "", errors.New("task_id is required")
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).Stop(id)
	if err != nil {
		return "", err
	}
	fields := taskCompatibilityFields(task)
	fields["message"] = "Task stopped"
	return pretty(fields), nil
}

type TaskOutputTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskOutputTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_output",
		Description: "Read recent background task log output by task id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":          map[string]any{"type": "string"},
				"task_id":     map[string]any{"type": "string"},
				"taskId":      map[string]any{"type": "string"},
				"limit_bytes": map[string]any{"type": "integer", "minimum": 1},
				"limit":       map[string]any{"type": "integer", "minimum": 1},
				"offset":      map[string]any{"type": "integer", "minimum": 0},
				"block":       map[string]any{"type": "boolean"},
				"timeout":     map[string]any{"type": "integer", "minimum": 0},
				"timeout_ms":  map[string]any{"type": "integer", "minimum": 0},
			},
			"anyOf":                taskIDRequirement(),
			"additionalProperties": false,
		},
	}
}

func (TaskOutputTool) Permission() Permission { return PermissionReadOnly }

func (t TaskOutputTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		ID          string `json:"id"`
		TaskID      string `json:"task_id"`
		TaskIDAlias string `json:"taskId"`
		LimitBytes  int64  `json:"limit_bytes"`
		Limit       int64  `json:"limit"`
		Offset      *int64 `json:"offset"`
		Block       bool   `json:"block"`
		Timeout     int    `json:"timeout"`
		TimeoutMS   int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	limitBytes := payload.LimitBytes
	if limitBytes <= 0 {
		limitBytes = payload.Limit
	}
	if limitBytes <= 0 {
		limitBytes = 64 * 1024
	}
	store := taskStore(t.ConfigHome, t.Workspace)
	id := firstNonEmpty(payload.ID, payload.TaskID, payload.TaskIDAlias)
	if id == "" {
		return "", errors.New("task_id is required")
	}
	task, err := store.Status(id)
	if err != nil {
		return "", err
	}
	logRead, task, err := readBackgroundLog(store, id, task, backgroundLogReadOptions{
		LimitBytes: limitBytes,
		Offset:     payload.Offset,
		Block:      payload.Block,
		TimeoutMS:  firstPositiveInt(payload.TimeoutMS, payload.Timeout),
	})
	if err != nil {
		return "", err
	}
	result := map[string]any{
		"id":               id,
		"task_id":          id,
		"status":           task.Status,
		"exit_code":        task.ExitCode,
		"error":            task.Error,
		"output":           logRead.Output,
		"stdout":           logRead.Output,
		"stderr":           "",
		"has_output":       logRead.Output != "",
		"task":             task,
		"kind":             task.Kind,
		"command":          task.Command,
		"logPath":          task.LogPath,
		"rawOutputPath":    task.LogPath,
		"interrupted":      task.Status == "stopped",
		"noOutputExpected": strings.TrimSpace(logRead.Output) == "",
		"offset":           logRead.Offset,
		"nextOffset":       logRead.NextOffset,
		"bytesRead":        logRead.BytesRead,
		"logSize":          logRead.LogSize,
		"truncated":        logRead.Truncated,
		"timedOut":         logRead.TimedOut,
		"timeoutMs":        logRead.TimeoutMS,
	}
	if logRead.Truncated {
		result["persistedOutputPath"] = task.LogPath
		result["persistedOutputSize"] = logRead.LogSize
	}
	return pretty(result), nil
}

type TaskSuperviseTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskSuperviseTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_supervise",
		Description: "Run one background task supervisor pass and restart eligible tasks with restart policies.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
		},
	}
}

func (TaskSuperviseTool) Permission() Permission { return PermissionDanger }

func (t TaskSuperviseTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	if len(input) != 0 && string(input) != "null" {
		var payload map[string]any
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
		if len(payload) != 0 {
			return "", errors.New("task_supervise does not accept input fields")
		}
	}
	result, err := taskStore(t.ConfigHome, t.Workspace).SuperviseOnce(time.Now().UTC())
	if err != nil {
		return "", err
	}
	return pretty(result), nil
}

func taskStore(configHome string, workspace string) background.Store {
	configHome = strings.TrimSpace(configHome)
	if configHome == "" {
		if workspace == "" {
			workspace = "."
		}
		configHome = filepath.Join(workspace, ".codog")
	}
	return background.NewStore(configHome)
}

// UserQuestionRequest is the normalized question presented by
// AskUserQuestionTool before it waits for an answer.
type UserQuestionRequest struct {
	Question  string
	Choices   []string
	Default   string
	Questions []UserQuestion
}

// UserQuestion is one question in the Claude-compatible multi-question
// request shape.
type UserQuestion struct {
	Question    string               `json:"question"`
	Header      string               `json:"header"`
	Options     []UserQuestionOption `json:"options"`
	MultiSelect bool                 `json:"multiSelect"`
}

// UserQuestionOption is one labeled answer with optional supporting preview.
type UserQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Preview     string `json:"preview,omitempty"`
}

type AskUserQuestionTool struct {
	In        io.Reader
	Out       io.Writer
	OnRequest func(UserQuestionRequest)
}

func (AskUserQuestionTool) Definition() anthropic.ToolDefinition {
	questionOptionSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"label":       map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
			"preview":     map[string]any{"type": "string"},
		},
		"required":             []string{"label", "description"},
		"additionalProperties": false,
	}
	questionSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question":    map[string]any{"type": "string"},
			"header":      map[string]any{"type": "string", "maxLength": 12},
			"options":     map[string]any{"type": "array", "items": questionOptionSchema, "minItems": 2, "maxItems": 4},
			"multiSelect": map[string]any{"type": "boolean"},
		},
		"required":             []string{"question", "header", "options"},
		"additionalProperties": false,
	}
	return anthropic.ToolDefinition{
		Name:        "ask_user_question",
		Description: "Ask the user one to four concise questions and return their selected or free-text answers.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question":  map[string]any{"type": "string"},
				"choices":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"options":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"default":   map[string]any{"type": "string"},
				"questions": map[string]any{"type": "array", "items": questionSchema, "minItems": 1, "maxItems": 4},
			},
			"anyOf": []any{
				map[string]any{"required": []string{"question"}},
				map[string]any{"required": []string{"questions"}},
			},
			"additionalProperties": false,
		},
	}
}

func (AskUserQuestionTool) Permission() Permission { return PermissionReadOnly }

func (t AskUserQuestionTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Question  string         `json:"question"`
		Choices   []string       `json:"choices"`
		Options   []string       `json:"options"`
		Default   string         `json:"default"`
		Questions []UserQuestion `json:"questions"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	payload.Question = strings.TrimSpace(payload.Question)
	if len(payload.Questions) > 0 {
		if payload.Question != "" || len(payload.Choices) > 0 || len(payload.Options) > 0 || strings.TrimSpace(payload.Default) != "" {
			return "", errors.New("questions cannot be combined with legacy question fields")
		}
		questions, err := normalizeUserQuestions(payload.Questions)
		if err != nil {
			return "", err
		}
		return t.executeUserQuestions(ctx, questions)
	}
	if payload.Question == "" {
		return "", errors.New("question or questions is required")
	}
	in := t.In
	if in == nil {
		in = os.Stdin
	}
	out := t.Out
	if out == nil {
		out = os.Stderr
	}
	choices := normalizeQuestionChoices(append(payload.Choices, payload.Options...))
	defaultAnswer := strings.TrimSpace(payload.Default)
	if t.OnRequest != nil {
		t.OnRequest(UserQuestionRequest{
			Question: payload.Question,
			Choices:  append([]string(nil), choices...),
			Default:  defaultAnswer,
		})
	}
	fmt.Fprintf(out, "\n%s\n", payload.Question)
	for index, choice := range choices {
		fmt.Fprintf(out, "  %d. %s\n", index+1, choice)
	}
	if defaultAnswer != "" {
		fmt.Fprintf(out, "Default: %s\n", defaultAnswer)
	}
	fmt.Fprint(out, "Answer: ")

	answer, err := readUserQuestionAnswer(ctx, in)
	if err != nil {
		return "", err
	}
	if answer == "" {
		answer = defaultAnswer
	}
	answer = resolveQuestionChoice(answer, choices)
	return pretty(map[string]any{
		"question": payload.Question,
		"answer":   answer,
	}), nil
}

func (t AskUserQuestionTool) executeUserQuestions(ctx context.Context, questions []UserQuestion) (string, error) {
	in := t.In
	if in == nil {
		in = os.Stdin
	}
	out := t.Out
	if out == nil {
		out = os.Stderr
	}
	if t.OnRequest != nil {
		t.OnRequest(UserQuestionRequest{Questions: cloneUserQuestions(questions)})
		line, err := readUserQuestionAnswer(ctx, in)
		if err != nil {
			return "", err
		}
		answers := map[string]string{}
		if strings.TrimSpace(line) != "" {
			if err := json.Unmarshal([]byte(line), &answers); err != nil {
				if len(questions) != 1 {
					return "", errors.New("multi-question answers must be a JSON object")
				}
				answers[questions[0].Question] = resolveModernQuestionAnswer(line, questions[0])
			}
		}
		return pretty(map[string]any{"questions": questions, "answers": answers}), nil
	}

	answers := make(map[string]string, len(questions))
	reader := bufio.NewReader(in)
	for index, question := range questions {
		fmt.Fprintf(out, "\n[%d/%d] %s\n", index+1, len(questions), question.Question)
		for optionIndex, option := range question.Options {
			fmt.Fprintf(out, "  %d. %s - %s\n", optionIndex+1, option.Label, option.Description)
		}
		if question.MultiSelect {
			fmt.Fprintln(out, "Select one or more choices separated by commas, or type another answer.")
		}
		fmt.Fprint(out, "Answer: ")
		answer, err := readUserQuestionAnswer(ctx, reader)
		if err != nil {
			return "", err
		}
		answers[question.Question] = resolveModernQuestionAnswer(answer, question)
	}
	return pretty(map[string]any{"questions": questions, "answers": answers}), nil
}

func readUserQuestionAnswer(ctx context.Context, in io.Reader) (string, error) {
	answerCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		reader, ok := in.(*bufio.Reader)
		if !ok {
			reader = bufio.NewReader(in)
		}
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			errCh <- err
			return
		}
		answerCh <- strings.TrimSpace(line)
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-errCh:
		return "", err
	case answer := <-answerCh:
		return answer, nil
	}
}

func normalizeUserQuestions(questions []UserQuestion) ([]UserQuestion, error) {
	if len(questions) < 1 || len(questions) > 4 {
		return nil, errors.New("questions must contain between 1 and 4 items")
	}
	out := make([]UserQuestion, 0, len(questions))
	seenQuestions := map[string]struct{}{}
	for index, question := range questions {
		question.Question = strings.TrimSpace(question.Question)
		question.Header = strings.TrimSpace(question.Header)
		if question.Question == "" {
			return nil, fmt.Errorf("questions[%d].question is required", index)
		}
		if question.Header == "" {
			return nil, fmt.Errorf("questions[%d].header is required", index)
		}
		if utf8.RuneCountInString(question.Header) > 12 {
			return nil, fmt.Errorf("questions[%d].header must be at most 12 characters", index)
		}
		questionKey := strings.ToLower(question.Question)
		if _, ok := seenQuestions[questionKey]; ok {
			return nil, errors.New("question texts must be unique")
		}
		seenQuestions[questionKey] = struct{}{}
		if len(question.Options) < 2 || len(question.Options) > 4 {
			return nil, fmt.Errorf("questions[%d].options must contain between 2 and 4 items", index)
		}
		seenOptions := map[string]struct{}{}
		for optionIndex := range question.Options {
			option := &question.Options[optionIndex]
			option.Label = strings.TrimSpace(option.Label)
			option.Description = strings.TrimSpace(option.Description)
			option.Preview = strings.TrimSpace(option.Preview)
			if option.Label == "" || option.Description == "" {
				return nil, fmt.Errorf("questions[%d].options[%d] requires label and description", index, optionIndex)
			}
			optionKey := strings.ToLower(option.Label)
			if _, ok := seenOptions[optionKey]; ok {
				return nil, fmt.Errorf("questions[%d] option labels must be unique", index)
			}
			seenOptions[optionKey] = struct{}{}
		}
		out = append(out, question)
	}
	return out, nil
}

func cloneUserQuestions(questions []UserQuestion) []UserQuestion {
	out := make([]UserQuestion, len(questions))
	for index, question := range questions {
		out[index] = question
		out[index].Options = append([]UserQuestionOption(nil), question.Options...)
	}
	return out
}

func resolveModernQuestionAnswer(answer string, question UserQuestion) string {
	parts := []string{strings.TrimSpace(answer)}
	if question.MultiSelect {
		parts = strings.Split(answer, ",")
	}
	resolved := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if index, err := strconv.Atoi(part); err == nil && index >= 1 && index <= len(question.Options) {
			part = question.Options[index-1].Label
		} else {
			for _, option := range question.Options {
				if strings.EqualFold(part, option.Label) {
					part = option.Label
					break
				}
			}
		}
		key := strings.ToLower(part)
		if part == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		resolved = append(resolved, part)
		if !question.MultiSelect {
			break
		}
	}
	return strings.Join(resolved, ", ")
}

func normalizeQuestionChoices(choices []string) []string {
	out := make([]string, 0, len(choices))
	seen := map[string]struct{}{}
	for _, choice := range choices {
		choice = strings.TrimSpace(choice)
		if choice == "" {
			continue
		}
		key := strings.ToLower(choice)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, choice)
	}
	return out
}

func resolveQuestionChoice(answer string, choices []string) string {
	if answer == "" || len(choices) == 0 {
		return answer
	}
	if index, err := strconv.Atoi(answer); err == nil && index >= 1 && index <= len(choices) {
		return choices[index-1]
	}
	for _, choice := range choices {
		if strings.EqualFold(answer, choice) {
			return choice
		}
	}
	return answer
}

type BriefTool struct {
	Workspace      string
	AdditionalDirs []string
}

type briefAttachment struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	IsImage bool   `json:"is_image"`
}

var briefStatusNames = []string{"normal", "proactive"}

func (BriefTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "brief",
		Description: "Return a user-facing brief message with optional workspace attachment metadata.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
				"attachments": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"status": map[string]any{"type": "string", "enum": append([]string(nil), briefStatusNames...)},
			},
			"required":             []string{"message", "status"},
			"additionalProperties": false,
		},
	}
}

func (BriefTool) Permission() Permission { return PermissionReadOnly }

func (t BriefTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Message     string   `json:"message"`
		Attachments []string `json:"attachments"`
		Status      string   `json:"status"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	payload.Message = strings.TrimSpace(payload.Message)
	if payload.Message == "" {
		return "", errors.New("message is required")
	}
	status := strings.ToLower(strings.TrimSpace(payload.Status))
	switch status {
	case "normal", "proactive":
	default:
		return "", suggestedValueError("unknown brief status", payload.Status, briefStatusNames)
	}
	attachments := make([]briefAttachment, 0, len(payload.Attachments))
	for _, attachment := range payload.Attachments {
		path, err := safePathInScope(t.Workspace, t.AdditionalDirs, attachment, false)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		attachments = append(attachments, briefAttachment{
			Path:    path,
			Size:    info.Size(),
			IsImage: isImageAttachment(path),
		})
	}
	return pretty(map[string]any{
		"message":     payload.Message,
		"status":      status,
		"attachments": attachments,
		"sent_at":     time.Now().UTC().Format(time.RFC3339),
	}), nil
}

type SendUserMessageTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (SendUserMessageTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "send_user_message",
		Description: "Send a user-facing message with optional workspace attachment metadata.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
				"attachments": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"status": map[string]any{"type": "string", "enum": append([]string(nil), briefStatusNames...)},
			},
			"required":             []string{"message", "status"},
			"additionalProperties": false,
		},
	}
}

func (SendUserMessageTool) Permission() Permission { return PermissionReadOnly }

func (t SendUserMessageTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	return BriefTool(t).Execute(ctx, input)
}

func isImageAttachment(path string) bool {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "png", "jpg", "jpeg", "gif", "webp", "bmp", "svg":
		return true
	default:
		return false
	}
}

type StructuredOutputTool struct{}

func (StructuredOutputTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "structured_output",
		Description: "Return the provided non-empty JSON object as structured output.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		},
	}
}

func (StructuredOutputTool) Permission() Permission { return PermissionReadOnly }

func (StructuredOutputTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload map[string]any
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if len(payload) == 0 {
		return "", errors.New("structured output payload must not be empty")
	}
	return pretty(map[string]any{
		"data":              "Structured output provided successfully",
		"structured_output": payload,
	}), nil
}

type SleepTool struct{}

func (SleepTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "sleep",
		Description: "Sleep for a bounded duration in milliseconds.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"duration_ms": map[string]any{"type": "integer", "minimum": 0},
			},
			"required":             []string{"duration_ms"},
			"additionalProperties": false,
		},
	}
}

func (SleepTool) Permission() Permission { return PermissionReadOnly }

func (SleepTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		DurationMS int `json:"duration_ms"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.DurationMS < 0 {
		return "", errors.New("duration_ms must be non-negative")
	}
	if payload.DurationMS > 300000 {
		return "", errors.New("duration_ms must be 300000 or less")
	}
	timer := time.NewTimer(time.Duration(payload.DurationMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
	}
	return pretty(map[string]any{
		"duration_ms": payload.DurationMS,
		"message":     fmt.Sprintf("Slept for %dms", payload.DurationMS),
	}), nil
}

type REPLTool struct {
	Workspace string
	ConfigEnv map[string]string
}

var replLanguageNames = []string{"sh", "shell", "bash", "python", "python3", "py", "javascript", "js", "node"}

func (REPLTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "repl",
		Description: "Execute code in a REPL-like subprocess for shell, python, or node.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"code":       map[string]any{"type": "string"},
				"language":   map[string]any{"type": "string", "enum": append([]string(nil), replLanguageNames...)},
				"timeout_ms": map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{"code", "language"},
			"additionalProperties": false,
		},
	}
}

func (REPLTool) Permission() Permission { return PermissionDanger }

func (t REPLTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Code      string `json:"code"`
		Language  string `json:"language"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	payload.Code = strings.TrimSpace(payload.Code)
	if payload.Code == "" {
		return "", errors.New("code is required")
	}
	args, err := replCommand(payload.Language, payload.Code)
	if err != nil {
		return "", err
	}
	timeout := time.Duration(payload.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if timeout > 5*time.Minute {
		return "", errors.New("timeout_ms must be 300000 or less")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = t.Workspace
	cmd.Env = toolEnvironmentFromConfig(t.ConfigEnv, nil)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	return pretty(map[string]any{
		"language":    strings.ToLower(strings.TrimSpace(payload.Language)),
		"stdout":      stdout.String(),
		"stderr":      stderr.String(),
		"exit_code":   exitCode,
		"duration_ms": time.Since(start).Milliseconds(),
		"timed_out":   ctx.Err() == context.DeadlineExceeded,
	}), nil
}

func replCommand(language string, code string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "sh", "shell", "bash":
		return []string{"sh", "-c", code}, nil
	case "python", "python3", "py":
		return []string{"python3", "-c", code}, nil
	case "javascript", "js", "node":
		return []string{"node", "-e", code}, nil
	default:
		return nil, suggestedValueError("unsupported repl language", language, replLanguageNames)
	}
}

func replPayloadCommand(input json.RawMessage) (string, string) {
	var payload struct {
		Code     string `json:"code"`
		Language string `json:"language"`
	}
	_ = json.Unmarshal(input, &payload)
	return strings.TrimSpace(payload.Language), strings.TrimSpace(payload.Code)
}

func isShellREPLLanguage(language string) bool {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "sh", "shell", "bash":
		return true
	default:
		return false
	}
}

type SkillTool struct {
	Workspace  string
	ConfigHome string
}

type skillToolRequest struct {
	Action     string `json:"action"`
	Skill      string `json:"skill"`
	Args       string `json:"args"`
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

type skillToolEntry struct {
	Name          string         `json:"name"`
	Source        string         `json:"source"`
	Path          string         `json:"path"`
	Description   string         `json:"description,omitempty"`
	WhenToUse     string         `json:"when_to_use,omitempty"`
	UserInvocable bool           `json:"user_invocable"`
	Origin        *skills.Origin `json:"origin,omitempty"`
}

var skillToolActionNames = []string{"list", "ls", "search", "find", "show", "info", "describe", "view", "inspect", "read", "invoke", "run", "use", "load", "activate"}

func (SkillTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "skill",
		Description: "List, inspect, or invoke local Codog and Claude-style skills available to the model.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        append([]string(nil), skillToolActionNames...),
					"description": "Action to run. Defaults to invoke when skill is set, otherwise list.",
				},
				"skill": map[string]any{
					"type":        "string",
					"description": "Skill name, such as review or team:audit.",
				},
				"args": map[string]any{
					"type":        "string",
					"description": "Optional user request or arguments to render with the skill.",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Optional list filter matched against name, source, description, and when_to_use.",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     100,
					"description": "Maximum skills to return for list.",
				},
			},
			"additionalProperties": false,
		},
	}
}

func (SkillTool) Permission() Permission {
	return PermissionReadOnly
}

func (t SkillTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload skillToolRequest
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	action := normalizeSkillToolAction(payload.Action, payload.Skill)
	if action == "list" {
		entries, total, err := t.listSkills(payload.Query, payload.MaxResults)
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{
			"kind":     "skill",
			"action":   "list",
			"query":    strings.TrimSpace(payload.Query),
			"total":    total,
			"returned": len(entries),
			"skills":   entries,
		}), nil
	}
	requested := normalizeSkillToolName(payload.Skill)
	if requested == "" {
		return "", errors.New("skill is required")
	}
	skill, err := skills.Find(t.ConfigHome, t.Workspace, requested)
	if err != nil {
		return "", err
	}
	if skill.DisableModelInvocation {
		return "", fmt.Errorf("skill %q cannot be used by the model because disable-model-invocation is true", skill.Name)
	}
	if action == "show" {
		return pretty(map[string]any{
			"kind":           "skill",
			"action":         "show",
			"skill":          skill.Name,
			"source":         skill.Source,
			"origin":         skill.Origin,
			"path":           skill.Path,
			"description":    skill.Description,
			"when_to_use":    skill.WhenToUse,
			"user_invocable": skill.UserInvocable,
			"prompt":         skill.Body,
			"metadata":       skills.RenderPromptBlock(skill),
		}), nil
	}
	if action != "invoke" {
		return "", unknownToolActionError("skill", payload.Action, skillToolActionNames)
	}
	return pretty(map[string]any{
		"kind":        "skill",
		"action":      "invoke",
		"skill":       skill.Name,
		"source":      skill.Source,
		"path":        skill.Path,
		"args":        strings.TrimSpace(payload.Args),
		"description": skill.Description,
		"prompt":      skill.Body,
		"rendered":    skills.RenderInvocation(skill, payload.Args),
	}), nil
}

func normalizeSkillToolAction(action string, skill string) string {
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "":
		if strings.TrimSpace(skill) == "" {
			return "list"
		}
		return "invoke"
	case "ls", "search", "find":
		return "list"
	case "run", "use", "load", "activate":
		return "invoke"
	case "info", "describe", "view", "inspect", "read":
		return "show"
	default:
		return action
	}
}

func (t SkillTool) listSkills(query string, limit int) ([]skillToolEntry, int, error) {
	all, err := skills.Load(t.ConfigHome, t.Workspace)
	if err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	query = strings.ToLower(strings.TrimSpace(query))
	entries := []skillToolEntry{}
	total := 0
	for _, skill := range all {
		if !skill.Active || skill.DisableModelInvocation {
			continue
		}
		entry := skillToolEntry{
			Name:          skill.Name,
			Source:        skill.Source,
			Path:          skill.Path,
			Description:   skill.Description,
			WhenToUse:     skill.WhenToUse,
			UserInvocable: skill.UserInvocable,
			Origin:        skill.Origin,
		}
		if query != "" && !skillToolEntryMatches(entry, query) {
			continue
		}
		total++
		if len(entries) < limit {
			entries = append(entries, entry)
		}
	}
	return entries, total, nil
}

func skillToolEntryMatches(entry skillToolEntry, query string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		entry.Name,
		entry.Source,
		entry.Description,
		entry.WhenToUse,
	}, "\n"))
	return strings.Contains(haystack, query)
}

func normalizeSkillToolName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimLeft(name, "/$")
	return strings.TrimSpace(name)
}

type ConfigTool struct {
	Workspace  string
	ConfigHome string
}

func (ConfigTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "config",
		Description: "Get or set a Codog user config setting in the user config file.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"setting": map[string]any{
					"type":        "string",
					"description": "Dotted config key, such as model, max_tokens, permission_mode, or sandbox.strategy.",
				},
				"value": map[string]any{
					"description": "When present, sets the setting to this JSON value. When omitted, reads the current user config value.",
				},
			},
			"required":             []string{"setting"},
			"additionalProperties": false,
		},
	}
}

func (ConfigTool) Permission() Permission {
	return PermissionWorkspace
}

func (t ConfigTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(input, &raw); err != nil {
		return "", err
	}
	var setting string
	if data := raw["setting"]; len(data) != 0 {
		if err := json.Unmarshal(data, &setting); err != nil {
			return "", err
		}
	}
	setting = strings.TrimSpace(setting)
	if err := validateConfigToolSetting(setting); err != nil {
		return "", err
	}
	path := configToolPath(t.ConfigHome, t.Workspace)
	current, err := readConfigToolFile(path)
	if err != nil {
		return "", err
	}
	previous, _ := nestedConfigToolValue(current, setting)
	valueData, hasValue := raw["value"]
	if !hasValue {
		return pretty(map[string]any{
			"success":   true,
			"operation": "get",
			"setting":   setting,
			"value":     redactConfigToolValue(setting, previous),
			"path":      path,
		}), nil
	}
	var value any
	if err := json.Unmarshal(valueData, &value); err != nil {
		return "", err
	}
	report, err := config.SetFileValue(path, setting, value)
	if err != nil {
		return "", err
	}
	updated, err := readConfigToolFile(path)
	if err != nil {
		return "", err
	}
	newValue, _ := nestedConfigToolValue(updated, setting)
	return pretty(map[string]any{
		"success":        true,
		"operation":      report.Action,
		"setting":        setting,
		"previous_value": redactConfigToolValue(setting, previous),
		"new_value":      redactConfigToolValue(setting, newValue),
		"path":           report.Path,
	}), nil
}

func configToolPath(configHome string, workspace string) string {
	configHome = strings.TrimSpace(configHome)
	if configHome == "" {
		if workspace == "" {
			workspace = "."
		}
		configHome = filepath.Join(workspace, ".codog")
	}
	return filepath.Join(configHome, "config.json")
}

func validateConfigToolSetting(setting string) error {
	if setting == "" {
		return errors.New("setting is required")
	}
	if strings.ContainsAny(setting, `/\`) {
		return fmt.Errorf("invalid config setting %q", setting)
	}
	for _, part := range strings.Split(setting, ".") {
		if strings.TrimSpace(part) == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid config setting %q", setting)
		}
	}
	return nil
}

func readConfigToolFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return map[string]any{}, nil
	}
	return out, nil
}

func nestedConfigToolValue(root map[string]any, setting string) (any, bool) {
	var current any = root
	for _, part := range strings.Split(setting, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func redactConfigToolValue(setting string, value any) any {
	key := strings.ToLower(setting)
	if !strings.Contains(key, "token") && !strings.Contains(key, "api_key") && !strings.Contains(key, "apikey") && !strings.Contains(key, "secret") {
		return value
	}
	if value == nil {
		return nil
	}
	return "[redacted]"
}

type ToolSearchTool struct {
	Registry *Registry
}

type toolSearchMCPDegradedReport struct {
	FailedServers  []toolSearchMCPFailure `json:"failed_servers,omitempty"`
	AvailableTools []string               `json:"available_tools,omitempty"`
}

type toolSearchMCPFailure struct {
	ServerName string                    `json:"server_name"`
	Phase      string                    `json:"phase"`
	Error      toolSearchMCPFailureError `json:"error"`
}

type toolSearchMCPFailureError struct {
	Message     string            `json:"message"`
	Context     map[string]string `json:"context,omitempty"`
	Recoverable bool              `json:"recoverable"`
}

func (ToolSearchTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "tool_search",
		Description: "Load full schemas for deferred Codog tools by name, description, or permission before calling them.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":       map[string]any{"type": "string"},
				"max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
	}
}

func (ToolSearchTool) Permission() Permission { return PermissionReadOnly }

func (t ToolSearchTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	if t.Registry == nil {
		return "", errors.New("tool registry is not available")
	}
	var payload struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	limit := payload.MaxResults
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	query := strings.TrimSpace(payload.Query)
	deferred := t.Registry.DeferredInfos()
	matches := searchToolInfos(deferred, query, limit)
	if selected, ok := selectToolInfos(t.Registry, query, limit); ok {
		matches = selected
	}
	pendingMCPServers, mcpDegraded := t.Registry.mcpDiscoveryReport()
	report := map[string]any{
		"query":                query,
		"normalized_query":     normalizeToolSearchQuery(query),
		"matches":              matches,
		"match_names":          toolInfoNames(matches),
		"total":                len(matches),
		"total_deferred_tools": len(deferred),
	}
	if len(pendingMCPServers) != 0 {
		report["pending_mcp_servers"] = pendingMCPServers
	}
	if len(mcpDegraded.FailedServers) != 0 || len(mcpDegraded.AvailableTools) != 0 {
		report["mcp_degraded"] = mcpDegraded
	}
	return pretty(report), nil
}

func (r *Registry) mcpDiscoveryReport() ([]string, toolSearchMCPDegradedReport) {
	if r == nil || len(r.mcpServers) == 0 {
		return nil, toolSearchMCPDegradedReport{}
	}
	availableTools := r.mcpToolNames()
	availableByServer := map[string]bool{}
	for _, name := range availableTools {
		parts := strings.Split(name, "__")
		if len(parts) >= 3 {
			availableByServer[parts[1]] = true
		}
	}
	pending := []string{}
	failures := []toolSearchMCPFailure{}
	for _, name := range sortedMCPServerNames(r.mcpServers) {
		if !availableByServer[mcp.NormalizeNameForTooling(name)] {
			pending = append(pending, name)
		}
		if failure, ok := classifyMCPServerDiscoveryFailure(name, r.mcpServers[name]); ok {
			failures = append(failures, failure)
		}
	}
	report := toolSearchMCPDegradedReport{FailedServers: failures}
	if len(failures) != 0 {
		report.AvailableTools = availableTools
	}
	return pending, report
}

func (r *Registry) mcpToolNames() []string {
	names := []string{}
	if r == nil {
		return names
	}
	for name := range r.tools {
		if strings.HasPrefix(name, "mcp__") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func classifyMCPServerDiscoveryFailure(name string, server config.MCPServerConfig) (toolSearchMCPFailure, bool) {
	context := map[string]string{"server": name}
	if strings.TrimSpace(server.Command) == "" && strings.TrimSpace(server.URL) == "" {
		return mcpDiscoveryFailure(name, "server_registration", "missing command or url", context, false), true
	}
	if strings.TrimSpace(server.URL) != "" {
		parsed, err := url.Parse(strings.TrimSpace(server.URL))
		if err != nil {
			context["transport"] = "http"
			return mcpDiscoveryFailure(name, "server_registration", err.Error(), context, false), true
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			context["transport"] = "http"
			context["scheme"] = parsed.Scheme
			return mcpDiscoveryFailure(name, "server_registration", "mcp url must use http or https", context, false), true
		}
		return toolSearchMCPFailure{}, false
	}
	command := strings.TrimSpace(server.Command)
	if _, err := exec.LookPath(command); err != nil {
		context["command"] = command
		return mcpDiscoveryFailure(name, "spawn_connect", err.Error(), context, true), true
	}
	return toolSearchMCPFailure{}, false
}

func mcpDiscoveryFailure(name string, phase string, message string, context map[string]string, recoverable bool) toolSearchMCPFailure {
	return toolSearchMCPFailure{
		ServerName: name,
		Phase:      phase,
		Error: toolSearchMCPFailureError{
			Message:     message,
			Context:     context,
			Recoverable: recoverable,
		},
	}
}

func selectToolInfos(registry *Registry, query string, limit int) ([]ToolInfo, bool) {
	query = strings.TrimSpace(query)
	if !strings.HasPrefix(strings.ToLower(query), "select:") {
		return nil, false
	}
	selection := strings.TrimSpace(query[len("select:"):])
	if selection == "" {
		return []ToolInfo{}, true
	}
	parts := strings.Split(selection, ",")
	out := make([]ToolInfo, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		if limit > 0 && len(out) >= limit {
			break
		}
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		info, ok := registry.modelInfo(name)
		if !ok {
			continue
		}
		key := strings.ToLower(info.Name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, info)
	}
	return out, true
}

func searchToolInfos(infos []ToolInfo, query string, limit int) []ToolInfo {
	query = strings.ToLower(strings.TrimSpace(query))
	terms := strings.Fields(query)
	type scored struct {
		info  ToolInfo
		score int
	}
	scoredMatches := make([]scored, 0, len(infos))
	for _, info := range infos {
		score := 1
		if query != "" {
			score = toolInfoScore(info, terms, query)
			if score == 0 {
				continue
			}
		}
		scoredMatches = append(scoredMatches, scored{info: info, score: score})
	}
	sort.Slice(scoredMatches, func(i, j int) bool {
		if scoredMatches[i].score != scoredMatches[j].score {
			return scoredMatches[i].score > scoredMatches[j].score
		}
		return scoredMatches[i].info.Name < scoredMatches[j].info.Name
	})
	if len(scoredMatches) > limit {
		scoredMatches = scoredMatches[:limit]
	}
	matches := make([]ToolInfo, 0, len(scoredMatches))
	for _, match := range scoredMatches {
		matches = append(matches, match.info)
	}
	return matches
}

func toolInfoNames(infos []ToolInfo) []string {
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return names
}

func normalizeToolSearchQuery(query string) string {
	terms := strings.FieldsFunc(strings.TrimSpace(query), func(r rune) bool {
		return r == ',' || r == '\t' || r == '\n' || r == '\r' || r == ' '
	})
	normalized := make([]string, 0, len(terms))
	for _, term := range terms {
		if token := toolAliasKey(term); token != "" {
			normalized = append(normalized, token)
		}
	}
	return strings.Join(normalized, " ")
}

func toolInfoScore(info ToolInfo, terms []string, query string) int {
	haystack := strings.ToLower(info.Name + " " + info.Description + " " + string(info.Permission))
	score := 0
	if strings.EqualFold(info.Name, query) {
		score += 20
	}
	if strings.Contains(strings.ToLower(info.Name), query) {
		score += 10
	}
	for _, term := range terms {
		if strings.Contains(strings.ToLower(info.Name), term) {
			score += 6
		}
		if strings.Contains(haystack, term) {
			score += 2
		} else {
			return 0
		}
	}
	return score
}

type TodoReadTool struct {
	Workspace string
}

func (TodoReadTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "todo_read",
		Description: "Read the workspace todo list for the current task.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
	}
}

func (TodoReadTool) Permission() Permission { return PermissionReadOnly }

func (t TodoReadTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	report, err := todos.List(t.Workspace)
	if err != nil {
		return "", err
	}
	return pretty(report), nil
}

type TodoWriteTool struct {
	Workspace string
}

func (TodoWriteTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "todo_write",
		Description: "Replace the workspace todo list. Use pending, in_progress, or completed status.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":         map[string]any{"type": "string"},
							"content":    map[string]any{"type": "string"},
							"activeForm": map[string]any{"type": "string"},
							"status":     map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
							"priority":   map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
						},
						"required":             []string{"content", "status", "activeForm"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"todos"},
			"additionalProperties": false,
		},
	}
}

func (TodoWriteTool) Permission() Permission { return PermissionWorkspace }

func (t TodoWriteTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Todos []todos.Item `json:"todos"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if err := validateTodoWriteItems(payload.Todos); err != nil {
		return "", err
	}
	oldReport, err := todos.List(t.Workspace)
	if err != nil {
		return "", err
	}
	submitted := todos.NormalizeItems(payload.Todos)
	persisted := submitted
	allCompleted := todoItemsAllCompleted(submitted)
	if allCompleted {
		persisted = nil
	}
	report, err := todos.Replace(t.Workspace, persisted)
	if err != nil {
		return "", err
	}
	output := todoWriteOutput{
		Kind:                    report.Kind,
		Action:                  report.Action,
		Status:                  report.Status,
		Total:                   report.Total,
		Items:                   report.Items,
		OldTodos:                todoWriteListItems(oldReport.Items),
		NewTodos:                todoWriteListItems(submitted),
		VerificationNudgeNeeded: todoWriteVerificationNudgeNeeded(submitted, allCompleted),
	}
	return pretty(output), nil
}

func validateTodoWriteItems(items []todos.Item) error {
	if len(items) == 0 {
		return errors.New("todos must not be empty")
	}
	for _, item := range items {
		if strings.TrimSpace(item.Content) == "" {
			return errors.New("todo content must not be empty")
		}
		if strings.TrimSpace(item.ActiveForm) == "" {
			return errors.New("todo activeForm must not be empty")
		}
	}
	return nil
}

func todoItemsAllCompleted(items []todos.Item) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if strings.TrimSpace(item.Status) != "completed" {
			return false
		}
	}
	return true
}

type todoWriteOutput struct {
	Kind                    string              `json:"kind"`
	Action                  string              `json:"action"`
	Status                  string              `json:"status"`
	Total                   int                 `json:"total"`
	Items                   []todos.Item        `json:"items"`
	OldTodos                []todoWriteListItem `json:"oldTodos"`
	NewTodos                []todoWriteListItem `json:"newTodos"`
	VerificationNudgeNeeded bool                `json:"verificationNudgeNeeded,omitempty"`
}

type todoWriteListItem struct {
	Content    string `json:"content"`
	ActiveForm string `json:"activeForm"`
	Status     string `json:"status"`
}

func todoWriteListItems(items []todos.Item) []todoWriteListItem {
	out := make([]todoWriteListItem, 0, len(items))
	for _, item := range items {
		out = append(out, todoWriteListItem{
			Content:    item.Content,
			ActiveForm: item.ActiveForm,
			Status:     item.Status,
		})
	}
	return out
}

func todoWriteVerificationNudgeNeeded(items []todos.Item, allCompleted bool) bool {
	if !allCompleted || len(items) < 3 {
		return false
	}
	for _, item := range items {
		if strings.Contains(strings.ToLower(item.Content), "verif") {
			return false
		}
	}
	return true
}

func safePath(workspace, requested string, allowMissing bool) (string, error) {
	return safePathInScope(workspace, nil, requested, allowMissing)
}

func readFileLimited(path string, maxBytes int64) ([]byte, bool, error) {
	if maxBytes <= 0 {
		maxBytes = maxFileToolBytes
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > maxBytes {
		return data[:maxBytes], true, nil
	}
	return data, false, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func imageMediaType(path string, data []byte) (string, bool) {
	detected := strings.ToLower(http.DetectContentType(data[:min(len(data), 512)]))
	if strings.HasPrefix(detected, "image/") {
		return detected, true
	}
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "bmp":
		return "image/bmp", true
	case "gif":
		return "image/gif", true
	case "jpg", "jpeg":
		return "image/jpeg", true
	case "png":
		return "image/png", true
	case "svg":
		return "image/svg+xml", true
	case "webp":
		return "image/webp", true
	default:
		return "", false
	}
}

func imageReadResult(path string, data []byte, mediaType string) map[string]any {
	result := map[string]any{
		"kind":       "image",
		"path":       path,
		"bytes":      len(data),
		"media_type": mediaType,
		"encoding":   "base64",
		"base64":     base64.StdEncoding.EncodeToString(data),
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		result["width"] = cfg.Width
		result["height"] = cfg.Height
	}
	return result
}

func safePathInScope(workspace string, additionalDirs []string, requested string, allowMissing bool) (string, error) {
	if requested == "" {
		return "", errors.New("path is required")
	}
	if workspace == "" {
		workspace = "."
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	roots := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		roots[0] = resolved
	} else {
		return "", err
	}
	for _, dir := range additionalDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", err
		}
		roots = append(roots, resolved)
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(roots[0], candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	resolved := ""
	if allowMissing {
		resolved, err = resolveMissingCandidate(candidate)
		if err != nil {
			return "", err
		}
	} else {
		resolved, err = filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", err
		}
	}
	for _, root := range roots {
		if pathWithin(root, resolved) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("path escapes workspace scope: %s", requested)
}

func resolveMissingCandidate(candidate string) (string, error) {
	var missing []string
	cursor := candidate
	for {
		resolved, err := filepath.EvalSymlinks(cursor)
		if err == nil {
			parts := append([]string{resolved}, missing...)
			return filepath.Join(parts...), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return "", err
		}
		missing = append([]string{filepath.Base(cursor)}, missing...)
		cursor = parent
	}
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func displayPath(workspace string, path string) string {
	root, err := filepath.Abs(workspace)
	if err == nil {
		if resolved, evalErr := filepath.EvalSymlinks(root); evalErr == nil {
			root = resolved
		}
		displayCandidate := path
		if resolved, evalErr := filepath.EvalSymlinks(path); evalErr == nil {
			displayCandidate = resolved
		}
		if rel, relErr := filepath.Rel(root, displayCandidate); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel) {
			return rel
		}
	}
	return path
}

func ignoredDir(name string) bool {
	switch name {
	case ".git", "node_modules", "target", "dist", "coverage", ".next", ".cache":
		return true
	default:
		return false
	}
}

func pretty(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}
