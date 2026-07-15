package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/reportconformance"
	"github.com/Rememorio/codog/internal/reportschema"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/tui"
	"github.com/stretchr/testify/require"
)

func TestBranchArgParserPaths(t *testing.T) {
	req, err := parseBranchArgs([]string{"--json", "--switch", "--force", "--base=main", "create", "feature", "start"})
	require.NoError(t, err)
	require.Equal(t, branchRequest{Format: "json", Action: "create", Name: "feature", Base: "main", StartPoint: "start", Switch: true, Force: true}, req)

	req, err = parseBranchArgs([]string{"-o", "text", "--checkout", "-f", "--base", "trunk", "show"})
	require.NoError(t, err)
	require.Equal(t, branchRequest{Format: "text", Action: "list", Base: "trunk", Switch: true, Force: true}, req)

	for _, test := range []struct {
		args   []string
		action string
		name   string
		new    string
		base   string
	}{
		{args: []string{"current"}, action: "current"},
		{args: []string{"new", "topic"}, action: "create", name: "topic"},
		{args: []string{"checkout", "topic"}, action: "switch", name: "topic"},
		{args: []string{"rm", "topic"}, action: "delete", name: "topic"},
		{args: []string{"rename", "new-name"}, action: "rename", new: "new-name"},
		{args: []string{"mv", "old-name", "new-name"}, action: "rename", name: "old-name", new: "new-name"},
		{args: []string{"fresh"}, action: "freshness"},
		{args: []string{"stale", "topic"}, action: "freshness", name: "topic"},
		{args: []string{"freshness", "topic", "trunk"}, action: "freshness", name: "topic", base: "trunk"},
	} {
		req, err = parseBranchArgs(test.args)
		require.NoError(t, err)
		require.Equal(t, test.action, req.Action)
		require.Equal(t, test.name, req.Name)
		require.Equal(t, test.new, req.NewName)
		require.Equal(t, test.base, req.Base)
	}

	for _, args := range [][]string{
		{"--output-format"}, {"--base"}, {"--unknown"}, {"create"}, {"switch"}, {"delete"}, {"rename"}, {"unknown"},
	} {
		_, err = parseBranchArgs(args)
		require.Error(t, err)
	}
	_, err = parseBranchArgs([]string{"--output-format=yaml"})
	require.Error(t, err)
}

func TestTrustArgParserPaths(t *testing.T) {
	req, err := parseTrustArgs([]string{
		"--json", "--cwd", "/workspace", "--worktree", "/worktree", "--screen", "safe",
		"--allow", "Bash(git:*)", "--deny", "/private", "--no-events", "check",
	}, "/default")
	require.NoError(t, err)
	require.Equal(t, trustRequest{
		Format: "json", Action: "resolve", CWD: "/workspace", Worktree: "/worktree", Screen: "safe",
		Allow: []string{"Bash(git:*)"}, Deny: []string{"/private"}, NoEvents: true,
	}, req)

	req, err = parseTrustArgs([]string{
		"--output-format=text", "--cwd=/inline", "--worktree=/tree", "--screen=inline",
		"--allow=Read(*)", "--deny=/secret", "status",
	}, "/default")
	require.NoError(t, err)
	require.Equal(t, "/inline", req.CWD)
	require.Equal(t, "inline", req.Screen)
	require.Equal(t, []string{"Read(*)"}, req.Allow)
	require.Equal(t, []string{"/secret"}, req.Deny)

	req, err = parseTrustArgs([]string{"resolve", "screen", "words"}, "/default")
	require.NoError(t, err)
	require.Equal(t, "screen words", req.Screen)

	for _, args := range [][]string{
		{"--output-format"}, {"--cwd"}, {"--worktree"}, {"--screen"}, {"--allow"}, {"--deny"}, {"--unknown"}, {"--output-format=yaml"},
	} {
		_, err = parseTrustArgs(args, "/default")
		require.Error(t, err)
	}
	_, err = parseTrustArgs(nil, " ")
	require.Error(t, err)
}

func TestExtraUsageArgParserPaths(t *testing.T) {
	req, err := parseExtraUsageArgs([]string{"--json", "--target", "project", "--path", "settings.json", "--no-open", "--admin", "status"})
	require.NoError(t, err)
	require.Equal(t, extraUsageRequest{Action: "status", Format: "json", Target: "project", Path: "settings.json", Open: false, Mode: "admin"}, req)

	req, err = parseExtraUsageArgs([]string{"--output-format=text", "--target=local", "--path=local.json", "--open", "--personal", "personal"})
	require.NoError(t, err)
	require.Equal(t, extraUsageRequest{Action: "open", Format: "text", Target: "local", Path: "local.json", Open: true, Mode: "personal"}, req)

	for _, args := range [][]string{{"list"}, {"team"}, {"individual"}} {
		_, err = parseExtraUsageArgs(args)
		require.NoError(t, err)
	}
	for _, args := range [][]string{
		{"--output-format"}, {"--target"}, {"--target", "--json"}, {"--path"}, {"--path", "-o"},
		{"--unknown"}, {"unexpected"}, {"--output-format=yaml"},
	} {
		_, err = parseExtraUsageArgs(args)
		require.Error(t, err)
	}
}

func TestCapabilitiesArgParserPaths(t *testing.T) {
	req, err := parseCapabilitiesArgs([]string{"--json", "--commands-snapshot", "commands.json", "--tools-snapshot", "tools.json", "audit"})
	require.NoError(t, err)
	require.Equal(t, capabilitiesRequest{Action: "audit", Format: "json", CommandSnapshot: "commands.json", ToolSnapshot: "tools.json"}, req)

	req, err = parseCapabilitiesArgs([]string{"", "--output-format=text", "--command-snapshot=commands.json", "--tool-snapshot=tools.json", "audit"})
	require.NoError(t, err)
	require.Equal(t, "commands.json", req.CommandSnapshot)
	require.Equal(t, "tools.json", req.ToolSnapshot)

	for _, test := range []struct {
		args   []string
		action string
		query  string
	}{
		{args: nil, action: "show"},
		{args: []string{"list"}, action: "show"},
		{args: []string{"lookup", "mcp"}, action: "resolve", query: "mcp"},
		{args: []string{"find", "skills"}, action: "resolve", query: "skills"},
	} {
		req, err = parseCapabilitiesArgs(test.args)
		require.NoError(t, err)
		require.Equal(t, test.action, req.Action)
		require.Equal(t, test.query, req.Query)
	}

	for _, args := range [][]string{
		{"--output-format"}, {"--commands-snapshot"}, {"--tools-snapshot"}, {"--unknown"}, {"--output-format=yaml"},
		{"show", "extra"}, {"resolve"}, {"resolve", "mcp", "extra"}, {"audit"}, {"audit", "extra"}, {"unknown"},
	} {
		_, err = parseCapabilitiesArgs(args)
		require.Error(t, err)
	}
}

func TestTUITurnSubmissionEventsAndResponses(t *testing.T) {
	entries := []tui.Entry{}
	submission := tuiTurnSubmission{
		submitter: tuiTurnSubmitter{
			sess:              &session.Session{Messages: []anthropic.Message{anthropic.TextMessage("assistant", "fallback")}},
			permissionAnswers: make(chan string, 1),
		},
		ctx:  context.Background(),
		emit: func(entry tui.Entry) { entries = append(entries, entry) },
	}

	submission.onQuestion(tools.UserQuestionRequest{Question: "Continue?", Choices: []string{"yes", "no"}, Default: "yes"})
	require.True(t, submission.liveToolEvents)
	require.Equal(t, "question", entries[len(entries)-1].Role)

	prompter := &tools.Prompter{}
	submission.configurePrompter(prompter)
	require.NotNil(t, prompter.In)
	require.Equal(t, io.Discard, prompter.Err)
	require.NotNil(t, prompter.OnRequest)
	require.NotNil(t, prompter.OnDecision)
	prompter.OnRequest(tools.PermissionDecision{ToolName: "Bash", Required: tools.PermissionDanger})
	prompter.OnDecision(tools.PermissionDecision{ToolName: "Bash", Allowed: true})

	submission.onToolStart(runloop.ToolCall{ID: "1", Name: "Bash", Input: `{"command":"pwd"}`})
	submission.onToolUse(runloop.ToolCall{ID: "1", Name: "Bash", Output: "ok"})
	submission.onToolUse(runloop.ToolCall{ID: "2", Name: "Write", Output: "denied", IsError: true})
	require.Len(t, submission.toolCalls, 2)
	require.Equal(t, "error", entries[len(entries)-1].Tool.Status)

	submission.liveToolEvents = false
	submission.out = bytes.Buffer{}
	response, err := submission.response(errors.New("turn failed"))
	require.Error(t, err)
	require.Contains(t, response, "Tools")
	require.Contains(t, response, "fallback")

	submission.streamOut.emitted = true
	response, err = submission.response(nil)
	require.NoError(t, err)
	require.Contains(t, response, "Tools")

	submission.liveToolEvents = true
	response, err = submission.response(nil)
	require.NoError(t, err)
	require.Empty(t, response)
}

func TestRenderReportSchemaDetails(t *testing.T) {
	var out bytes.Buffer
	renderReportSchemaReport(&out, reportSchemaReport{
		Action: "project",
		Status: "ok",
		Registry: &reportschema.Registry{
			SchemaVersion: reportschema.SchemaV1,
			Fields: []reportschema.RegistryField{{
				ID: "claims[].kind", Required: true, FieldFamily: "claims",
				EnumValues: []string{"fact", "inference"}, Deprecated: true,
			}},
			Reports: []reportschema.RegistryReport{{ID: "review", SchemaVersion: reportschema.SchemaV1}},
		},
		Report: &reportschema.CanonicalReport{
			SchemaVersion: reportschema.SchemaV1,
			Identity:      reportschema.Identity{ReportID: "report-1", ContentHash: "hash-1"},
			Claims:        []reportschema.Claim{{ID: "claim-1"}},
		},
		Projection: &reportschema.Projection{
			SchemaVersion: reportschema.SchemaV1,
			ProjectionID:  "projection-1",
			View:          "brief",
			Provenance: reportschema.ProjectionProvenance{
				Consumer: "consumer-1", Downgraded: true,
				OmittedFieldFamilies: []string{"negative_evidence"},
				Redactions:           []reportschema.RedactionProvenance{{FieldPath: "claims[0].text", Reason: "sensitive"}},
			},
		},
		Conformance: &reportconformance.Result{
			SchemaVersion: reportconformance.ResultSchemaVersion,
			FixtureSet:    reportconformance.FixtureSetVersion,
			Consumer:      reportconformance.ConsumerIdentity{Name: "consumer-1", Version: "1.0"},
			Valid:         true, ParsePassed: true, SemanticPassed: true,
			PassedCaseCount: 1, RequiredCaseCount: 2,
			LastPassed: &reportconformance.LastPassed{Consumer: "consumer-1", Version: "0.9", PassedAt: "2026-07-14T00:00:00Z"},
			Errors:     []reportconformance.Error{{Path: "/cases/missing", Kind: "semantic", Message: "missing case"}},
		},
		ConformanceCases: []reportconformance.RequiredCase{{Name: "case-1", View: "brief", ProjectionID: "projection-1"}},
	})

	text := out.String()
	require.Contains(t, text, "claims[].kind [required] claims enum=fact|inference deprecated")
	require.Contains(t, text, "review "+reportschema.SchemaV1)
	require.Contains(t, text, "Report ID        report-1")
	require.Contains(t, text, "Omitted          negative_evidence")
	require.Contains(t, text, "claims[0].text sensitive")
	require.Contains(t, text, "Last passed      consumer-1 0.9")
	require.Contains(t, text, "/cases/missing [semantic] missing case")
	require.Contains(t, text, "case-1 brief projection-1")
}

func TestVoiceActionHelpers(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{}\n"), 0o644))
	app := &App{Config: config.Config{ConfigHome: t.TempDir(), VoiceCommand: "echo"}, Out: io.Discard}

	terminal, err := app.applyVoiceAction(&voiceRequest{Action: "status"})
	require.NoError(t, err)
	require.False(t, terminal)
	terminal, err = app.applyVoiceAction(&voiceRequest{Action: "unknown"})
	require.Error(t, err)
	require.True(t, terminal)

	req := voiceRequest{Action: "off", Target: "user", Path: configPath}
	require.NoError(t, app.updateVoiceEnabled(&req))
	require.NotNil(t, app.Config.VoiceEnabled)
	require.False(t, *app.Config.VoiceEnabled)

	req = voiceRequest{Action: "set-command", Command: "  printf voice  ", Target: "user", Path: configPath}
	require.NoError(t, app.updateVoiceCommand(&req))
	require.Equal(t, "printf voice", app.Config.VoiceCommand)

	require.NoError(t, app.clearVoiceCommand(&voiceRequest{Target: "user", Path: configPath}))
	require.Empty(t, app.Config.VoiceCommand)
	require.NoError(t, app.clearVoice(&voiceRequest{Target: "user", Path: configPath}))
	require.Nil(t, app.Config.VoiceEnabled)

	app.Config.VoiceCommand = ""
	require.Error(t, app.updateVoiceEnabled(&voiceRequest{Action: "on", Target: "user", Path: configPath}))
	require.Error(t, app.updateVoiceCommand(&voiceRequest{Action: "set-command", Target: "user", Path: configPath}))
}

func TestTUITurnInstallsAndRestoresMCPQuestionInteraction(t *testing.T) {
	registry := tools.NewRegistry(t.TempDir())
	options := registry.MCPClientOptions()
	options.OnNotification = func(_ mcp.Notification) {}
	registry.SetMCPClientOptions(options)
	submission := tuiTurnSubmission{
		submitter: tuiTurnSubmitter{
			app:             &App{Tools: registry},
			questionAnswers: make(chan string),
		},
		ctx:  context.Background(),
		emit: func(tui.Entry) {},
	}
	restore := submission.installQuestionInteractions()
	interactive := registry.MCPClientOptions()
	require.NotNil(t, interactive.Elicit)
	require.NotNil(t, interactive.OnNotification)
	require.True(t, registry.Has("ask_user_question"))

	restore()
	restored := registry.MCPClientOptions()
	require.Nil(t, restored.Elicit)
	require.NotNil(t, restored.OnNotification)
}
