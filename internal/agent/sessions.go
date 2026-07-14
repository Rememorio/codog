package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/bookmarks"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/customcommands"
	"github.com/Rememorio/codog/internal/doctor"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/skills"
	"github.com/Rememorio/codog/internal/toolnames"
)

func normalizeSessionAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "list", "ls":
		return "list"
	case "show", "get", "info", "describe":
		return "show"
	case "exists", "has":
		return "exists"
	case "search", "find", "grep":
		return "search"
	case "audit", "doctor", "check", "hygiene":
		return "audit"
	case "repair", "fix", "heal":
		return "repair"
	case "export", "dump":
		return "export"
	case "import", "load":
		return "import"
	case "fork", "clone":
		return "fork"
	case "switch", "checkout", "use":
		return "switch"
	case "rename", "mv", "move":
		return "rename"
	case "prune", "gc", "clean":
		return "prune"
	case "pin", "bookmark-message", "keep-message":
		return "pin"
	case "unpin", "unbookmark-message", "drop-message":
		return "unpin"
	case "delete", "del", "remove", "rm":
		return "delete"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

type sessionsActionError struct {
	Action string
}

func (e sessionsActionError) Error() string {
	action := strings.TrimSpace(e.Action)
	if action == "" {
		action = "unknown"
	}
	return fmt.Sprintf("unsupported_sessions_action: unsupported sessions action %q", action)
}

func renderSessionsCommandError(out io.Writer, err error, format string) error {
	var actionErr sessionsActionError
	if errors.As(err, &actionErr) {
		action := strings.TrimSpace(actionErr.Action)
		if action == "" {
			action = "unknown"
		}
		return renderActionError(out, actionErrorReport{
			Kind:      "sessions",
			Action:    action,
			Status:    "error",
			ErrorKind: "unsupported_sessions_action",
			Message:   fmt.Sprintf("unsupported sessions action %q", action),
			Hint:      unknownSessionsActionHint(action),
		}, format)
	}
	return renderCLIError(out, err, format)
}

var sessionActionCandidates = []string{
	"list", "ls", "show", "get", "info", "describe", "exists", "has", "search", "find", "grep",
	"audit", "doctor", "check", "hygiene", "repair", "fix", "heal", "export", "dump", "import",
	"load", "fork", "clone", "switch", "checkout", "use", "rename", "mv", "move", "prune", "gc",
	"clean", "pin", "bookmark-message", "keep-message", "unpin", "unbookmark-message",
	"drop-message", "delete", "del", "remove", "rm",
}

func unknownSessionsActionHint(action string) string {
	suggestions := toolnames.Suggestions(action, sessionActionCandidates, 4)
	switch len(suggestions) {
	case 1:
		return fmt.Sprintf("Did you mean `codog sessions %s`? Use `codog sessions list` to inspect saved sessions.", suggestions[0])
	case 0:
		return "Use `codog sessions list`, `codog sessions show ID`, `codog sessions search QUERY`, `codog sessions audit`, `codog sessions repair`, `codog sessions export ID`, `codog sessions import PATH`, `codog sessions fork ID`, `codog sessions switch ID`, `codog sessions rename OLD_ID NEW_ID`, `codog sessions pin ID [message]`, `codog sessions unpin ID [message]`, `codog sessions prune`, or `codog sessions delete ID`. Common aliases include ls, get, has, find, doctor, fix, clone, checkout, mv, gc, and rm."
	default:
		return fmt.Sprintf("Did you mean one of: %s? Use `codog sessions list` to inspect saved sessions.", strings.Join(suggestions, ", "))
	}
}

type sessionShowReport struct {
	Kind                string                  `json:"kind"`
	Action              string                  `json:"action"`
	Status              string                  `json:"status"`
	SessionID           string                  `json:"session_id"`
	Path                string                  `json:"path"`
	MessageCount        int                     `json:"message_count"`
	CreatedAtMS         int64                   `json:"created_at_ms,omitempty"`
	UpdatedAtMS         int64                   `json:"updated_at_ms,omitempty"`
	ModifiedEpochMillis int64                   `json:"modified_epoch_millis,omitempty"`
	ParentSessionID     string                  `json:"parent_session_id,omitempty"`
	BranchName          string                  `json:"branch_name,omitempty"`
	PinnedMessages      []int                   `json:"pinned_messages,omitempty"`
	Lifecycle           sessionLifecycleReport  `json:"lifecycle"`
	Identity            session.SessionIdentity `json:"identity,omitempty"`
	Messages            []anthropic.Message     `json:"messages"`
}

func (a *App) SessionShow(args []string) error {
	const usage = "codog sessions show ID [--json|--output-format text|json]"
	format, remaining, err := parseTemplateOutputArgs("sessions show", args)
	if err != nil {
		return err
	}
	if len(remaining) == 0 {
		return requiredArgumentError{Command: "sessions show", Argument: "ID", Usage: usage}
	}
	if len(remaining) > 1 {
		return unexpectedExtraArgsError{Command: "sessions show", Args: append([]string(nil), remaining[1:]...), Usage: usage}
	}
	report, err := a.buildSessionShowReport(remaining[0])
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderSessionShowText(a.Out, report)
	return nil
}

func (a *App) SessionExport(args []string) error {
	req, err := parseSessionExportArgs(args)
	if err != nil {
		return err
	}
	return a.writeExport(req)
}

type sessionImportRequest struct {
	Source    string
	ID        string
	Overwrite bool
	Format    string
}

type sessionImportReport struct {
	Kind              string                  `json:"kind"`
	Action            string                  `json:"action"`
	Status            string                  `json:"status"`
	Source            string                  `json:"source"`
	OriginalSessionID string                  `json:"original_session_id,omitempty"`
	SessionID         string                  `json:"session_id"`
	Path              string                  `json:"path"`
	MessageCount      int                     `json:"message_count"`
	Overwritten       bool                    `json:"overwritten"`
	Identity          session.SessionIdentity `json:"identity"`
}

func parseSessionImportArgs(command string, args []string, defaultFormat string) (sessionImportRequest, error) {
	req := sessionImportRequest{Format: defaultFormat}
	if req.Format == "" {
		req.Format = "text"
	}
	positionals := []string{}
	usage := command + " PATH [--id ID|--name ID] [--force] [--json|--output-format text|json]"
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "":
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("%s output format is required", command)
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--id" || arg == "--name" || arg == "--session":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("%s session id is required", command)
			}
			req.ID = args[index]
		case strings.HasPrefix(arg, "--id="):
			req.ID = strings.TrimPrefix(arg, "--id=")
		case strings.HasPrefix(arg, "--name="):
			req.ID = strings.TrimPrefix(arg, "--name=")
		case strings.HasPrefix(arg, "--session="):
			req.ID = strings.TrimPrefix(arg, "--session=")
		case arg == "--force" || arg == "--overwrite":
			req.Overwrite = true
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: command, Option: arg, Usage: usage}
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 1 {
		return req, fmt.Errorf("usage: %s", usage)
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown %s output format %q", command, req.Format)
	}
	req.Source = positionals[0]
	return req, nil
}

func (a *App) importSessionWithReport(req sessionImportRequest) (sessionImportReport, error) {
	result, err := a.Sessions.Import(req.Source, session.ImportOptions{ID: req.ID, Overwrite: req.Overwrite})
	if err != nil {
		return sessionImportReport{}, err
	}
	return sessionImportReport{
		Kind:              "session_import",
		Action:            "import",
		Status:            "ok",
		Source:            result.Source,
		OriginalSessionID: result.OriginalSessionID,
		SessionID:         result.SessionID,
		Path:              result.Path,
		MessageCount:      result.MessageCount,
		Overwritten:       result.Overwritten,
		Identity:          result.Identity,
	}, nil
}

func renderSessionImportText(out io.Writer, report sessionImportReport) {
	fmt.Fprintln(out, "Session imported")
	fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	if report.OriginalSessionID != "" && report.OriginalSessionID != report.SessionID {
		fmt.Fprintf(out, "  Original         %s\n", report.OriginalSessionID)
	}
	fmt.Fprintf(out, "  Messages         %d\n", report.MessageCount)
	if report.Overwritten {
		fmt.Fprintln(out, "  Overwritten      yes")
	}
	fmt.Fprintf(out, "  Source           %s\n", report.Source)
	fmt.Fprintf(out, "  File             %s\n", report.Path)
}

func parseSessionExportArgs(args []string) (exportRequest, error) {
	req := exportRequest{Format: session.ExportMarkdown}
	usage := "codog sessions export ID [PATH] [--output PATH] [--format markdown|json|jsonl|html]"
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "sessions export", Flag: arg, Usage: usage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--output" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "sessions export", Flag: arg, Usage: usage}
			}
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		case arg == "--format" || arg == "--output-format":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "sessions export", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--format="):
			req.Format = strings.TrimPrefix(arg, "--format=")
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "sessions export", Option: arg, Usage: usage}
		default:
			positionals = append(positionals, arg)
		}
	}
	if strings.TrimSpace(req.SessionID) == "" {
		if len(positionals) == 0 {
			return req, errors.New("usage: " + usage)
		}
		req.SessionID = positionals[0]
		positionals = positionals[1:]
	}
	if req.Output == "" && len(positionals) > 0 {
		req.Output = positionals[0]
		positionals = positionals[1:]
	}
	if len(positionals) > 0 {
		return req, unexpectedExtraArgsError{Command: "sessions export", Args: positionals, Usage: usage}
	}
	if _, err := session.NormalizeExportFormat(req.Format); err != nil {
		return req, err
	}
	return req, nil
}

func (a *App) buildSessionShowReport(id string) (sessionShowReport, error) {
	sess, err := a.Sessions.OpenExisting(id)
	if err != nil {
		return sessionShowReport{}, err
	}
	return sessionShowReport{
		Kind:                "session_show",
		Action:              "show",
		Status:              "ok",
		SessionID:           sess.ID,
		Path:                sess.Path,
		MessageCount:        len(sess.Messages),
		CreatedAtMS:         timeMillis(sess.Metadata.CreatedAt),
		UpdatedAtMS:         timeMillis(sess.Metadata.UpdatedAt),
		ModifiedEpochMillis: timeMillis(sess.Metadata.ModifiedAt),
		ParentSessionID:     sess.Metadata.ParentSessionID,
		BranchName:          sess.Metadata.BranchName,
		PinnedMessages:      append([]int(nil), sess.Metadata.PinnedMessages...),
		Lifecycle:           lifecycleForStoredSession(sess),
		Identity:            sess.Identity,
		Messages:            append([]anthropic.Message(nil), sess.Messages...),
	}, nil
}

func renderSessionShowText(out io.Writer, report sessionShowReport) {
	fmt.Fprintln(out, "Session")
	fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	fmt.Fprintf(out, "  Messages         %d\n", report.MessageCount)
	fmt.Fprintf(out, "  Lifecycle        %s\n", report.Lifecycle.Signal)
	if report.ParentSessionID != "" {
		fmt.Fprintf(out, "  Parent           %s\n", report.ParentSessionID)
	}
	if report.BranchName != "" {
		fmt.Fprintf(out, "  Branch           %s\n", report.BranchName)
	}
	if len(report.PinnedMessages) > 0 {
		fmt.Fprintf(out, "  Pinned           %s\n", formatIntList(report.PinnedMessages))
	}
	fmt.Fprintf(out, "  File             %s\n", report.Path)
	if strings.TrimSpace(report.Identity.Title) != "" {
		fmt.Fprintf(out, "  Title            %s\n", report.Identity.Title)
	}
}

type sessionForkRequest struct {
	SourceID   string
	BranchName string
	Format     string
}

type sessionForkReport struct {
	Kind            string                  `json:"kind"`
	Action          string                  `json:"action"`
	Status          string                  `json:"status"`
	SessionID       string                  `json:"session_id"`
	ParentSessionID string                  `json:"parent_session_id"`
	BranchName      string                  `json:"branch_name,omitempty"`
	Path            string                  `json:"path"`
	MessageCount    int                     `json:"message_count"`
	Identity        session.SessionIdentity `json:"identity,omitempty"`
}

type sessionSwitchRequest struct {
	ID     string
	Format string
}

type sessionSwitchReport struct {
	Kind              string                  `json:"kind"`
	Action            string                  `json:"action"`
	Status            string                  `json:"status"`
	PreviousSessionID string                  `json:"previous_session_id,omitempty"`
	RequestedSession  string                  `json:"requested_session"`
	SessionID         string                  `json:"session_id"`
	Path              string                  `json:"path"`
	MessageCount      int                     `json:"message_count"`
	Identity          session.SessionIdentity `json:"identity,omitempty"`
	ContinueCommands  []string                `json:"continue_commands"`
}

func parseSessionSwitchArgs(command string, args []string, defaultFormat string) (sessionSwitchRequest, error) {
	format, rest, err := parseTemplateOutputArgs(command, args)
	if err != nil {
		return sessionSwitchRequest{}, err
	}
	if defaultFormat != "" && !argsHaveOutputFormat(args) {
		format = defaultFormat
	}
	if len(rest) != 1 {
		return sessionSwitchRequest{}, fmt.Errorf("usage: %s ID [--json|--output-format text|json]", command)
	}
	id := strings.TrimSpace(rest[0])
	if id == "" {
		return sessionSwitchRequest{}, fmt.Errorf("usage: %s ID [--json|--output-format text|json]", command)
	}
	return sessionSwitchRequest{ID: id, Format: format}, nil
}

func (a *App) switchSessionWithReport(previousID string, requestedID string) (sessionSwitchReport, error) {
	sess, err := a.Sessions.OpenExisting(requestedID)
	if err != nil {
		return sessionSwitchReport{}, err
	}
	exe := strings.TrimSpace(a.Executable)
	if exe == "" {
		exe = "codog"
	}
	return sessionSwitchReport{
		Kind:              "session_switch",
		Action:            "switch",
		Status:            "ok",
		PreviousSessionID: strings.TrimSpace(previousID),
		RequestedSession:  strings.TrimSpace(requestedID),
		SessionID:         sess.ID,
		Path:              sess.Path,
		MessageCount:      len(sess.Messages),
		Identity:          sess.Identity,
		ContinueCommands:  resumeContinueCommands(exe, sess.ID),
	}, nil
}

func renderSessionSwitchText(out io.Writer, report sessionSwitchReport) {
	fmt.Fprintln(out, "Session switched")
	if report.PreviousSessionID != "" {
		fmt.Fprintf(out, "  Previous         %s\n", report.PreviousSessionID)
	}
	fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	fmt.Fprintf(out, "  Requested        %s\n", report.RequestedSession)
	fmt.Fprintf(out, "  Messages         %d\n", report.MessageCount)
	fmt.Fprintf(out, "  File             %s\n", report.Path)
	if len(report.ContinueCommands) > 0 {
		fmt.Fprintln(out, "  Continue")
		for _, command := range report.ContinueCommands {
			fmt.Fprintf(out, "    %s\n", command)
		}
	}
}

func parseSessionForkArgs(command string, args []string, defaultSourceID string, defaultFormat string) (sessionForkRequest, error) {
	req := sessionForkRequest{SourceID: strings.TrimSpace(defaultSourceID), Format: defaultFormat}
	if req.Format == "" {
		req.Format = "text"
	}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "":
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("%s output format is required", command)
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{
				Command: command,
				Option:  arg,
				Usage:   command + " ID [branch-name] [--json|--output-format text|json]",
			}
		default:
			positionals = append(positionals, arg)
		}
	}
	if defaultSourceID == "" {
		if len(positionals) == 0 {
			return req, fmt.Errorf("usage: %s ID [branch-name] [--json|--output-format text|json]", command)
		}
		req.SourceID = positionals[0]
		positionals = positionals[1:]
	}
	req.BranchName = strings.TrimSpace(strings.Join(positionals, " "))
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown %s output format %q", command, req.Format)
	}
	return req, nil
}

func (a *App) forkSessionWithReport(sourceID string, branchName string) (sessionForkReport, *session.Session, error) {
	forked, err := a.Sessions.Fork(sourceID, branchName)
	if err != nil {
		return sessionForkReport{}, nil, err
	}
	report := sessionForkReport{
		Kind:            "session_fork",
		Action:          "fork",
		Status:          "ok",
		SessionID:       forked.ID,
		ParentSessionID: strings.TrimSpace(sourceID),
		BranchName:      strings.TrimSpace(branchName),
		Path:            forked.Path,
		MessageCount:    len(forked.Messages),
		Identity:        forked.Identity,
	}
	return report, forked, nil
}

func renderSessionForkText(out io.Writer, report sessionForkReport) {
	fmt.Fprintln(out, "Session forked")
	fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	fmt.Fprintf(out, "  Parent           %s\n", report.ParentSessionID)
	if report.BranchName != "" {
		fmt.Fprintf(out, "  Branch           %s\n", report.BranchName)
	}
	fmt.Fprintf(out, "  Messages         %d\n", report.MessageCount)
	fmt.Fprintf(out, "  File             %s\n", report.Path)
}

type sessionRenameRequest struct {
	OldID  string
	NewID  string
	Format string
}

type sessionRenameReport struct {
	Kind         string `json:"kind"`
	Action       string `json:"action"`
	Status       string `json:"status"`
	OldSessionID string `json:"old_session_id"`
	NewSessionID string `json:"new_session_id"`
	OldPath      string `json:"old_path"`
	NewPath      string `json:"new_path"`
	MessageCount int    `json:"message_count"`
}

func parseSessionRenameArgs(command string, args []string, defaultOldID string, defaultFormat string) (sessionRenameRequest, error) {
	req := sessionRenameRequest{OldID: strings.TrimSpace(defaultOldID), Format: defaultFormat}
	if req.Format == "" {
		req.Format = "text"
	}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "":
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("%s output format is required", command)
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{
				Command: command,
				Option:  arg,
				Usage:   command + " OLD_ID NEW_ID [--json|--output-format text|json]",
			}
		default:
			positionals = append(positionals, arg)
		}
	}
	if defaultOldID == "" {
		if len(positionals) < 2 {
			return req, fmt.Errorf("usage: %s OLD_ID NEW_ID [--json|--output-format text|json]", command)
		}
		req.OldID = positionals[0]
		req.NewID = positionals[1]
		if len(positionals) > 2 {
			return req, unexpectedExtraArgsError{
				Command: command,
				Args:    append([]string(nil), positionals[2:]...),
				Usage:   command + " OLD_ID NEW_ID [--json|--output-format text|json]",
			}
		}
	} else {
		if len(positionals) != 1 {
			return req, fmt.Errorf("usage: %s NEW_ID [--json|--output-format text|json]", command)
		}
		req.NewID = positionals[0]
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown %s output format %q", command, req.Format)
	}
	return req, nil
}

func (a *App) renameSessionWithReport(oldID string, newID string) (sessionRenameReport, error) {
	result, err := a.Sessions.Rename(oldID, newID)
	if err != nil {
		return sessionRenameReport{}, err
	}
	return sessionRenameReport{
		Kind:         "session_rename",
		Action:       "rename",
		Status:       "ok",
		OldSessionID: result.OldID,
		NewSessionID: result.NewID,
		OldPath:      result.OldPath,
		NewPath:      result.NewPath,
		MessageCount: result.MessageCount,
	}, nil
}

func renderSessionRenameText(out io.Writer, report sessionRenameReport) {
	fmt.Fprintln(out, "Session renamed")
	fmt.Fprintf(out, "  Old session      %s\n", report.OldSessionID)
	fmt.Fprintf(out, "  New session      %s\n", report.NewSessionID)
	fmt.Fprintf(out, "  Messages         %d\n", report.MessageCount)
	fmt.Fprintf(out, "  File             %s\n", report.NewPath)
}

type sessionPruneRequest struct {
	Keep      int
	EmptyOnly bool
	Confirm   bool
	Format    string
	ExcludeID string
}

func parseSessionPruneArgs(command string, args []string, defaultFormat string) (sessionPruneRequest, error) {
	req := sessionPruneRequest{Format: defaultFormat}
	if req.Format == "" {
		req.Format = "text"
	}
	usage := command + " [--empty|--keep N] [--confirm] [--session ID|--resume ID] [--json|--output-format text|json]"
	emptySet := false
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "":
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("%s output format is required", command)
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session" || arg == "--resume":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: command, Flag: arg, Usage: usage}
			}
			req.ExcludeID = strings.TrimSpace(args[index])
		case strings.HasPrefix(arg, "--session="):
			req.ExcludeID = strings.TrimSpace(strings.TrimPrefix(arg, "--session="))
		case strings.HasPrefix(arg, "--resume="):
			req.ExcludeID = strings.TrimSpace(strings.TrimPrefix(arg, "--resume="))
		case arg == "--confirm" || arg == "--force":
			req.Confirm = true
		case arg == "--empty" || arg == "--empty-only":
			req.EmptyOnly = true
			emptySet = true
		case arg == "--all":
			req.EmptyOnly = false
			emptySet = true
		case arg == "--keep":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: command, Flag: arg, Usage: usage}
			}
			value, err := parsePositiveIntOption(args[index], "--keep", usage)
			if err != nil {
				return req, err
			}
			req.Keep = value
		case strings.HasPrefix(arg, "--keep="):
			value, err := parsePositiveIntOption(strings.TrimPrefix(arg, "--keep="), "--keep", usage)
			if err != nil {
				return req, err
			}
			req.Keep = value
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: command, Option: arg, Usage: usage}
		default:
			return req, unexpectedExtraArgsError{Command: command, Args: []string{arg}, Usage: usage}
		}
	}
	if req.Keep == 0 && !emptySet {
		req.EmptyOnly = true
	}
	if req.Keep == 0 && !req.EmptyOnly {
		return req, fmt.Errorf("usage: %s", usage)
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown %s output format %q", command, req.Format)
	}
	return req, nil
}

func (a *App) pruneSessionsWithReport(req sessionPruneRequest, excludeID string) (session.PruneReport, error) {
	if strings.TrimSpace(req.ExcludeID) != "" {
		excludeID = req.ExcludeID
	}
	return a.Sessions.Prune(session.PruneOptions{
		ExcludeID: excludeID,
		Keep:      req.Keep,
		EmptyOnly: req.EmptyOnly,
		Confirm:   req.Confirm,
	})
}

func renderSessionPruneText(out io.Writer, report session.PruneReport) {
	title := "Session prune dry-run"
	if !report.DryRun {
		title = "Session prune"
	}
	fmt.Fprintln(out, title)
	fmt.Fprintf(out, "  Sessions scanned %d\n", report.Scanned)
	fmt.Fprintf(out, "  Candidates       %d\n", report.CandidateCount)
	if report.Keep > 0 {
		fmt.Fprintf(out, "  Keep newest      %d\n", report.Keep)
	}
	if report.EmptyOnly {
		fmt.Fprintln(out, "  Empty only       yes")
	}
	if report.DryRun {
		fmt.Fprintln(out, "  Deleted          0")
		fmt.Fprintln(out, "  Confirm          rerun with --confirm")
	} else {
		fmt.Fprintf(out, "  Deleted          %d\n", report.DeletedCount)
	}
	items := report.Candidates
	if !report.DryRun {
		items = report.Deleted
	}
	for _, item := range items {
		fmt.Fprintf(out, "  - %s\t%d messages\t%s\t%s\n", item.ID, item.MessageCount, item.Reason, item.Path)
	}
}

type sessionDeleteReport struct {
	Kind      string `json:"kind"`
	Action    string `json:"action"`
	Status    string `json:"status"`
	Deleted   bool   `json:"deleted"`
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
}

func (a *App) deleteSessionWithReport(id string) (sessionDeleteReport, error) {
	target, err := a.Sessions.OpenExisting(id)
	if err != nil {
		return sessionDeleteReport{}, err
	}
	if err := a.Sessions.Delete(target.ID); err != nil {
		return sessionDeleteReport{}, err
	}
	return sessionDeleteReport{
		Kind:      "session_delete",
		Action:    "delete",
		Status:    "ok",
		Deleted:   true,
		SessionID: target.ID,
		Path:      target.Path,
	}, nil
}

func renderSessionDeleteText(out io.Writer, report sessionDeleteReport) {
	fmt.Fprintln(out, "Session deleted")
	fmt.Fprintf(out, "  Deleted session  %s\n", report.SessionID)
	fmt.Fprintf(out, "  File             %s\n", report.Path)
}

type sessionExistsReport struct {
	Kind          string `json:"kind"`
	Action        string `json:"action"`
	Status        string `json:"status"`
	SessionID     string `json:"session_id"`
	Session       string `json:"session"`
	Requested     string `json:"requested"`
	Exists        bool   `json:"exists"`
	Active        bool   `json:"active"`
	Path          string `json:"path,omitempty"`
	CandidatePath string `json:"candidate_path,omitempty"`
}

func (a *App) SessionExists(args []string, activeID string) error {
	const usage = "codog sessions exists ID [--json|--output-format text|json]"
	format, remaining, err := parseTemplateOutputArgs("sessions exists", args)
	if err != nil {
		return err
	}
	if len(remaining) == 0 {
		return requiredArgumentError{Command: "sessions exists", Argument: "ID", Usage: usage}
	}
	if len(remaining) > 1 {
		return unexpectedExtraArgsError{Command: "sessions exists", Args: append([]string(nil), remaining[1:]...), Usage: usage}
	}
	requested := strings.TrimSpace(remaining[0])
	if requested == "" {
		return requiredArgumentError{Command: "sessions exists", Argument: "ID", Usage: usage}
	}
	report, err := a.buildSessionExistsReport(requested, activeID)
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderSessionExistsText(a.Out, report)
	return nil
}

func (a *App) buildSessionExistsReport(requested string, activeID string) (sessionExistsReport, error) {
	report := sessionExistsReport{
		Kind:          "session_exists",
		Action:        "exists",
		Status:        "ok",
		SessionID:     requested,
		Session:       requested,
		Requested:     requested,
		CandidatePath: filepath.Join(a.Sessions.Dir, requested+".jsonl"),
	}
	exists, err := a.Sessions.Exists(requested)
	if err != nil {
		return sessionExistsReport{}, err
	}
	report.Exists = exists
	if exists {
		sess, err := a.Sessions.OpenExisting(requested)
		if err != nil {
			return sessionExistsReport{}, err
		}
		report.SessionID = sess.ID
		report.Path = sess.Path
	}
	report.Active = strings.TrimSpace(activeID) != "" && report.SessionID == strings.TrimSpace(activeID)
	return report, nil
}

func renderSessionExistsText(out io.Writer, report sessionExistsReport) {
	exists := "no"
	if report.Exists {
		exists = "yes"
	}
	fmt.Fprintln(out, "Session exists")
	fmt.Fprintf(out, "  Session          %s\n", report.Session)
	fmt.Fprintf(out, "  Exists           %s\n", exists)
	if report.Active {
		fmt.Fprintln(out, "  Active           yes")
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Path             %s\n", report.Path)
	}
}

type resumeCommandReport struct {
	Kind             string   `json:"kind"`
	Action           string   `json:"action"`
	Status           string   `json:"status"`
	RequestedSession string   `json:"requested_session"`
	SessionID        string   `json:"session_id"`
	MessageCount     int      `json:"message_count"`
	Path             string   `json:"path"`
	ContinueCommands []string `json:"continue_commands"`
}

func (a *App) ResumeCommand(args []string) error {
	format, remaining, err := parseTemplateOutputArgs("resume", args)
	if err != nil {
		return err
	}
	if len(remaining) > 1 {
		return errors.New("usage: codog resume [ID|latest] [--json|--output-format text|json]")
	}
	requested := "latest"
	if len(remaining) == 1 {
		requested = strings.TrimSpace(remaining[0])
	}
	if requested == "" {
		return errors.New("usage: codog resume [ID|latest] [--json|--output-format text|json]")
	}
	sess, err := a.Sessions.OpenExisting(requested)
	if err != nil {
		return renderSessionRestoreError(a.Out, "show", requested, err, format)
	}
	exe := strings.TrimSpace(a.Executable)
	if exe == "" {
		exe = "codog"
	}
	report := resumeCommandReport{
		Kind:             "resume",
		Action:           "show",
		Status:           "ok",
		RequestedSession: requested,
		SessionID:        sess.ID,
		MessageCount:     len(sess.Messages),
		Path:             sess.Path,
		ContinueCommands: resumeContinueCommands(exe, sess.ID),
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderResumeCommand(a.Out, report)
	return nil
}

func resumeContinueCommands(executable string, sessionID string) []string {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		executable = "codog"
	}
	return []string{
		strings.Join([]string{shellQuote(executable), "--resume", shellQuote(sessionID), "repl"}, " "),
		strings.Join([]string{shellQuote(executable), "--resume", shellQuote(sessionID), "prompt", shellQuote("...")}, " "),
	}
}

func renderResumeCommand(out io.Writer, report resumeCommandReport) {
	fmt.Fprintln(out, "Resume Session")
	fmt.Fprintf(out, "  Session ID        %s\n", report.SessionID)
	fmt.Fprintf(out, "  Requested         %s\n", report.RequestedSession)
	fmt.Fprintf(out, "  Messages          %d\n", report.MessageCount)
	fmt.Fprintf(out, "  Path              %s\n", report.Path)
	if len(report.ContinueCommands) > 0 {
		fmt.Fprintln(out, "  Continue")
		for _, command := range report.ContinueCommands {
			fmt.Fprintf(out, "    %s\n", command)
		}
	}
}

type clearCommandReport struct {
	Kind             string   `json:"kind"`
	Action           string   `json:"action"`
	Status           string   `json:"status"`
	SessionID        string   `json:"session_id"`
	MessageCount     int      `json:"message_count"`
	Path             string   `json:"path"`
	ContinueCommands []string `json:"continue_commands"`
}

type clearResumedRequest struct {
	Format    string
	SessionID string
	Confirm   bool
}

type clearResumedReport struct {
	Kind              string `json:"kind"`
	Action            string `json:"action"`
	Status            string `json:"status"`
	SessionID         string `json:"session_id"`
	Path              string `json:"path"`
	Backup            string `json:"backup"`
	OriginalMessages  int    `json:"original_messages"`
	RemainingMessages int    `json:"remaining_messages"`
	RemovedMessages   int    `json:"removed_messages"`
}

type conversationRequest struct {
	Action       string
	SessionID    string
	Format       string
	ExportFormat string
	Output       string
	Confirm      bool
}

type conversationReport struct {
	Kind             string                  `json:"kind"`
	Action           string                  `json:"action"`
	Status           string                  `json:"status"`
	RequestedSession string                  `json:"requested_session"`
	SessionID        string                  `json:"session_id"`
	MessageCount     int                     `json:"message_count"`
	Path             string                  `json:"path"`
	PinnedMessages   []int                   `json:"pinned_messages,omitempty"`
	Lifecycle        sessionLifecycleReport  `json:"lifecycle"`
	Identity         session.SessionIdentity `json:"identity,omitempty"`
	Messages         []anthropic.Message     `json:"messages,omitempty"`
	ContinueCommands []string                `json:"continue_commands,omitempty"`
}

func (a *App) Conversation(args []string, overrides config.FlagOverrides) error {
	req, err := parseConversationArgs(args, overrides)
	if err != nil {
		return err
	}
	switch req.Action {
	case "clear":
		clearArgs := []string{}
		if req.Confirm {
			clearArgs = append(clearArgs, "--confirm")
		}
		if req.Format == "json" {
			clearArgs = append(clearArgs, "--json")
		}
		return a.ClearCommand(clearArgs)
	case "export":
		return a.writeExport(exportRequest{SessionID: req.SessionID, Output: req.Output, Format: req.ExportFormat})
	case "status", "show":
		report, err := a.buildConversationReport(req)
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		renderConversationReport(a.Out, report)
		return nil
	default:
		return fmt.Errorf("unknown conversation action %q", req.Action)
	}
}

func parseConversationArgs(args []string, overrides config.FlagOverrides) (conversationRequest, error) {
	req := conversationRequest{Action: "status", SessionID: "latest", Format: "text", ExportFormat: session.ExportMarkdown}
	if strings.TrimSpace(overrides.Resume) != "" {
		req.SessionID = overrides.Resume
	}
	if strings.TrimSpace(overrides.SessionID) != "" {
		req.SessionID = overrides.SessionID
	}
	positionals := []string{}
	actionSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--confirm":
			req.Confirm = true
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("conversation output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session" || arg == "--resume":
			index++
			if index >= len(args) {
				return req, errors.New("conversation session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case arg == "--format":
			index++
			if index >= len(args) {
				return req, errors.New("conversation export format is required")
			}
			req.ExportFormat = args[index]
		case strings.HasPrefix(arg, "--format="):
			req.ExportFormat = strings.TrimPrefix(arg, "--format=")
		case arg == "--output":
			index++
			if index >= len(args) {
				return req, errors.New("conversation export output path is required")
			}
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{
				Command: "conversation",
				Option:  arg,
				Usage:   "codog conversation [status|show|export|clear] [session-id] [--session ID] [--json|--output-format text|json]",
			}
		default:
			if !actionSet {
				switch strings.ToLower(strings.TrimSpace(arg)) {
				case "status", "show", "export", "clear":
					req.Action = strings.ToLower(strings.TrimSpace(arg))
					actionSet = true
					continue
				}
			}
			positionals = append(positionals, arg)
		}
	}
	if req.Confirm && !actionSet {
		req.Action = "clear"
	}
	if err := validateTextOrJSON(req.Format, "conversation"); err != nil {
		return req, err
	}
	if _, err := session.NormalizeExportFormat(req.ExportFormat); err != nil {
		return req, err
	}
	switch req.Action {
	case "status", "show":
		if len(positionals) > 1 {
			return req, unexpectedExtraArgsError{
				Command: "conversation",
				Args:    positionals[1:],
				Usage:   "codog conversation [status|show] [session-id] [--json|--output-format text|json]",
			}
		}
		if len(positionals) == 1 {
			req.SessionID = positionals[0]
		}
	case "export":
		if len(positionals) > 1 {
			return req, unexpectedExtraArgsError{
				Command: "conversation",
				Args:    positionals[1:],
				Usage:   "codog conversation export [PATH] [--session ID] [--format markdown|json|jsonl|html]",
			}
		}
		if len(positionals) == 1 {
			req.Output = positionals[0]
		}
	case "clear":
		if len(positionals) != 0 {
			return req, unexpectedExtraArgsError{
				Command: "conversation",
				Args:    positionals,
				Usage:   "codog conversation clear [--confirm] [--json|--output-format text|json]",
			}
		}
	default:
		return req, fmt.Errorf("unknown conversation action %q", req.Action)
	}
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = "latest"
	}
	return req, nil
}

func (a *App) buildConversationReport(req conversationRequest) (conversationReport, error) {
	openID := req.SessionID
	if session.IsSessionReferenceAlias(openID) {
		latest, err := a.Sessions.LatestAnyID()
		if err != nil {
			return conversationReport{}, err
		}
		openID = latest
	}
	sess, err := a.Sessions.OpenExisting(openID)
	if err != nil {
		return conversationReport{}, err
	}
	exe := strings.TrimSpace(a.Executable)
	if exe == "" {
		exe = "codog"
	}
	report := conversationReport{
		Kind:             "conversation",
		Action:           req.Action,
		Status:           "ok",
		RequestedSession: req.SessionID,
		SessionID:        sess.ID,
		MessageCount:     len(sess.Messages),
		Path:             sess.Path,
		PinnedMessages:   append([]int(nil), sess.Metadata.PinnedMessages...),
		Lifecycle:        lifecycleForStoredSession(sess),
		Identity:         sess.Identity,
		ContinueCommands: []string{
			strings.Join([]string{shellQuote(exe), "--resume", shellQuote(sess.ID), "repl"}, " "),
			strings.Join([]string{shellQuote(exe), "--resume", shellQuote(sess.ID), "prompt", shellQuote("...")}, " "),
			strings.Join([]string{shellQuote(exe), "conversation", "export", "--session", shellQuote(sess.ID)}, " "),
		},
	}
	if req.Action == "show" {
		report.Messages = append([]anthropic.Message(nil), sess.Messages...)
	}
	return report, nil
}

func renderConversationReport(out io.Writer, report conversationReport) {
	fmt.Fprintln(out, "Conversation")
	fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	fmt.Fprintf(out, "  Requested        %s\n", report.RequestedSession)
	fmt.Fprintf(out, "  Messages         %d\n", report.MessageCount)
	fmt.Fprintf(out, "  Lifecycle        %s\n", report.Lifecycle.Signal)
	if len(report.PinnedMessages) > 0 {
		fmt.Fprintf(out, "  Pinned           %s\n", formatIntList(report.PinnedMessages))
	}
	fmt.Fprintf(out, "  File             %s\n", report.Path)
	if strings.TrimSpace(report.Identity.Title) != "" {
		fmt.Fprintf(out, "  Title            %s\n", report.Identity.Title)
	}
	if len(report.Messages) > 0 {
		fmt.Fprintln(out, "  Transcript")
		for index, msg := range report.Messages {
			fmt.Fprintf(out, "    %d. %s", index+1, msg.Role)
			text := firstMessageText(msg)
			if text != "" {
				fmt.Fprintf(out, ": %s", trimSingleLine(text, 96))
			}
			fmt.Fprintln(out)
		}
	}
	if len(report.ContinueCommands) > 0 {
		fmt.Fprintln(out, "  Continue")
		for _, command := range report.ContinueCommands {
			fmt.Fprintf(out, "    %s\n", command)
		}
	}
}

func firstMessageText(msg anthropic.Message) string {
	for _, block := range msg.Content {
		if strings.TrimSpace(block.Text) != "" {
			return strings.TrimSpace(block.Text)
		}
	}
	return ""
}

func trimSingleLine(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

type bookmarksRequest struct {
	Action       string
	Format       string
	Name         string
	Ref          string
	SessionID    string
	MessageIndex int
	PRRef        string
	Note         string
	All          bool
}

type bookmarksReport struct {
	Kind          string               `json:"kind"`
	Action        string               `json:"action"`
	Status        string               `json:"status"`
	Workspace     string               `json:"workspace,omitempty"`
	Path          string               `json:"path,omitempty"`
	Count         int                  `json:"count"`
	Removed       int                  `json:"removed,omitempty"`
	Bookmark      *bookmarks.Bookmark  `json:"bookmark,omitempty"`
	Bookmarks     []bookmarks.Bookmark `json:"bookmarks,omitempty"`
	ResumeCommand string               `json:"resume_command,omitempty"`
	Message       string               `json:"message,omitempty"`
}

func (a *App) Bookmarks(args []string, overrides config.FlagOverrides) error {
	req, err := parseBookmarksArgs(args, overrides)
	if err != nil {
		return err
	}
	store := bookmarks.NewStore(a.Config.ConfigHome)
	path, err := store.Path()
	if err != nil {
		return err
	}
	report := bookmarksReport{
		Kind:      "bookmarks",
		Action:    req.Action,
		Status:    "ok",
		Workspace: a.Workspace,
		Path:      path,
	}
	switch req.Action {
	case "list":
		items, err := store.List(bookmarks.ListOptions{Workspace: a.Workspace, All: req.All})
		if err != nil {
			return err
		}
		report.Bookmarks = items
		report.Count = len(items)
	case "add":
		bookmark, err := a.buildBookmark(req)
		if err != nil {
			return err
		}
		created, err := store.Add(bookmark)
		if err != nil {
			return err
		}
		report.Bookmark = &created
		report.Count = 1
		report.Message = "Bookmark added"
		report.ResumeCommand = bookmarkResumeCommand(created)
	case "show":
		bookmark, err := store.Get(req.Ref)
		if err != nil {
			return err
		}
		report.Bookmark = &bookmark
		report.Count = 1
		report.ResumeCommand = bookmarkResumeCommand(bookmark)
	case "delete":
		bookmark, err := store.Delete(req.Ref)
		if err != nil {
			return err
		}
		report.Bookmark = &bookmark
		report.Count = 1
		report.Removed = 1
		report.Message = "Bookmark deleted"
	case "clear":
		removed, err := store.Clear(bookmarks.ListOptions{Workspace: a.Workspace, All: req.All})
		if err != nil {
			return err
		}
		report.Removed = removed
		report.Message = fmt.Sprintf("Removed %d bookmark(s).", removed)
	default:
		return fmt.Errorf("unknown bookmarks action %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderBookmarksReport(a.Out, report)
	return nil
}

func parseBookmarksArgs(args []string, overrides config.FlagOverrides) (bookmarksRequest, error) {
	parser := bookmarksArgParser{req: newBookmarksRequest(overrides)}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if parser.consumeBooleanOption(arg) {
			continue
		}
		handled, err := consumeValueOption(args, &index, parser.valueOptions())
		if err != nil {
			return parser.req, err
		}
		if handled {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return parser.req, unknownOptionError{Command: "bookmarks", Option: arg, Usage: bookmarksUsage}
		}
		parser.consumePositional(arg)
	}
	if err := validateBookmarksRequest(&parser.req, parser.positionals); err != nil {
		return parser.req, err
	}
	return parser.req, nil
}

const bookmarksUsage = "codog bookmarks [list|add|show|delete|clear] [NAME|ID] [--session ID] [--message N|last] [--pr PR] [--json|--output-format text|json]"

type bookmarksArgParser struct {
	req         bookmarksRequest
	positionals []string
	actionSet   bool
}

func newBookmarksRequest(overrides config.FlagOverrides) bookmarksRequest {
	req := bookmarksRequest{Action: "list", Format: "text", SessionID: "latest", MessageIndex: -1}
	if strings.TrimSpace(overrides.Resume) != "" {
		req.SessionID = overrides.Resume
	}
	if strings.TrimSpace(overrides.SessionID) != "" {
		req.SessionID = overrides.SessionID
	}
	return req
}

func (p *bookmarksArgParser) consumeBooleanOption(arg string) bool {
	switch arg {
	case "":
		return true
	case "--json":
		p.req.Format = "json"
		return true
	case "--all":
		p.req.All = true
		return true
	default:
		return false
	}
}

func (p *bookmarksArgParser) valueOptions() map[string]valueOption {
	return map[string]valueOption{
		"--output-format": p.stringOption(&p.req.Format, false),
		"-o":              p.stringOption(&p.req.Format, false),
		"--session":       p.stringOption(&p.req.SessionID, true),
		"--resume":        p.stringOption(&p.req.SessionID, true),
		"--message":       p.messageOption("--message"),
		"--message-index": p.messageOption("--message-index"),
		"--pr":            p.stringOption(&p.req.PRRef, true),
		"--pull-request":  p.stringOption(&p.req.PRRef, true),
		"--note":          p.stringOption(&p.req.Note, true),
	}
}

func (p *bookmarksArgParser) stringOption(target *string, rejectOutputFormat bool) valueOption {
	return valueOption{
		missing:            bookmarksMissingValueError,
		rejectOutputFormat: rejectOutputFormat,
		set: func(value string) error {
			*target = value
			return nil
		},
	}
}

func (p *bookmarksArgParser) messageOption(flag string) valueOption {
	return valueOption{
		missing:            bookmarksMissingValueError,
		rejectOutputFormat: true,
		set: func(value string) error {
			messageIndex, err := parseBookmarkMessageIndex(value, flag, bookmarksUsage)
			if err != nil {
				return err
			}
			p.req.MessageIndex = messageIndex
			return nil
		},
	}
}

func bookmarksMissingValueError(flag string) error {
	return missingFlagValueError{Command: "bookmarks", Flag: flag, Usage: bookmarksUsage}
}

func (p *bookmarksArgParser) consumePositional(arg string) {
	if !p.actionSet && isBookmarksAction(arg) {
		p.req.Action = normalizeBookmarksAction(arg)
		p.actionSet = true
		return
	}
	p.positionals = append(p.positionals, arg)
}

func validateBookmarksRequest(req *bookmarksRequest, positionals []string) error {
	normalizedFormat, err := normalizeOutputFormat("bookmarks", req.Format, []string{"text", "json"})
	if err != nil {
		return err
	}
	req.Format = normalizedFormat
	switch req.Action {
	case "list", "clear":
		if len(positionals) != 0 {
			return unexpectedExtraArgsError{Command: "bookmarks " + req.Action, Args: positionals, Usage: bookmarksUsage}
		}
	case "add":
		if len(positionals) == 0 {
			return requiredArgumentError{Command: "bookmarks add", Argument: "NAME", Usage: bookmarksUsage}
		}
		req.Name = strings.Join(positionals, " ")
	case "show", "delete":
		if len(positionals) != 1 {
			if len(positionals) == 0 {
				return requiredArgumentError{Command: "bookmarks " + req.Action, Argument: "ID_OR_NAME", Usage: bookmarksUsage}
			}
			return unexpectedExtraArgsError{Command: "bookmarks " + req.Action, Args: positionals[1:], Usage: bookmarksUsage}
		}
		req.Ref = positionals[0]
	default:
		return unexpectedExtraArgsError{Command: "bookmarks", Args: []string{req.Action}, Usage: bookmarksUsage}
	}
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = "latest"
	}
	return nil
}

func isBookmarksAction(value string) bool {
	switch normalizeBookmarksAction(value) {
	case "list", "add", "show", "delete", "clear":
		return true
	default:
		return false
	}
}

func normalizeBookmarksAction(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "list", "ls":
		return "list"
	case "add", "create", "new", "mark":
		return "add"
	case "show", "get", "jump", "open":
		return "show"
	case "delete", "del", "remove", "rm":
		return "delete"
	case "clear", "reset":
		return "clear"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func parseBookmarkMessageIndex(value string, option string, usage string) (int, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "last", "latest":
		return -1, nil
	}
	index, err := strconv.Atoi(value)
	if err != nil || index <= 0 {
		return 0, invalidFlagValueError{
			Flag:    option,
			Value:   value,
			Message: "bookmarks message index must be a positive integer or last",
			Usage:   usage,
		}
	}
	return index - 1, nil
}

type pullRequestReference struct {
	Repo   string
	Number int
	URL    string
}

func parsePullRequestReference(value string) (pullRequestReference, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return pullRequestReference{}, nil
	}
	if number, ok := parsePullRequestNumber(raw); ok {
		return pullRequestReference{Number: number}, nil
	}
	if strings.HasPrefix(raw, "#") {
		if number, ok := parsePullRequestNumber(strings.TrimPrefix(raw, "#")); ok {
			return pullRequestReference{Number: number}, nil
		}
	}
	if repo, numberText, ok := strings.Cut(raw, "#"); ok {
		number, valid := parsePullRequestNumber(numberText)
		if valid && validRepoSlug(repo) {
			return pullRequestReference{Repo: strings.Trim(repo, "/"), Number: number}, nil
		}
	}
	urlText := raw
	if strings.HasPrefix(urlText, "github.com/") {
		urlText = "https://" + urlText
	}
	if parsed, err := url.Parse(urlText); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		ref, ok := pullRequestReferenceFromPath(parsed.Path)
		if ok {
			if parsed.Scheme == "" {
				ref.URL = raw
			} else {
				ref.URL = parsed.String()
			}
			return ref, nil
		}
	}
	if ref, ok := pullRequestReferenceFromPath(raw); ok {
		return ref, nil
	}
	return pullRequestReference{}, fmt.Errorf("invalid pull request reference %q", value)
}

func pullRequestReferenceFromPath(path string) (pullRequestReference, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 4 || !strings.EqualFold(parts[2], "pull") {
		return pullRequestReference{}, false
	}
	number, ok := parsePullRequestNumber(parts[3])
	if !ok {
		return pullRequestReference{}, false
	}
	repo := strings.Join(parts[:2], "/")
	if !validRepoSlug(repo) {
		return pullRequestReference{}, false
	}
	return pullRequestReference{Repo: repo, Number: number}, true
}

func parsePullRequestNumber(value string) (int, bool) {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || number <= 0 {
		return 0, false
	}
	return number, true
}

func validRepoSlug(value string) bool {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	return len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != ""
}

func (a *App) buildBookmark(req bookmarksRequest) (bookmarks.Bookmark, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	messageIndex := req.MessageIndex
	var bookmarkMessageIndex *int
	if sessionID != "" {
		sess, err := a.Sessions.OpenExisting(sessionID)
		if err != nil {
			return bookmarks.Bookmark{}, err
		}
		sessionID = sess.ID
		if messageIndex < 0 && len(sess.Messages) > 0 {
			messageIndex = len(sess.Messages) - 1
		}
		if messageIndex >= len(sess.Messages) {
			return bookmarks.Bookmark{}, fmt.Errorf("message index %d out of range for %d messages", messageIndex, len(sess.Messages))
		}
		if messageIndex >= 0 {
			bookmarkMessageIndex = &messageIndex
		}
	}
	pr, err := parsePullRequestReference(req.PRRef)
	if err != nil {
		return bookmarks.Bookmark{}, err
	}
	return bookmarks.Bookmark{
		Name:         req.Name,
		Workspace:    a.Workspace,
		SessionID:    sessionID,
		MessageIndex: bookmarkMessageIndex,
		PRRepo:       pr.Repo,
		PRNumber:     pr.Number,
		PRURL:        pr.URL,
		Note:         req.Note,
	}, nil
}

func bookmarkResumeCommand(bookmark bookmarks.Bookmark) string {
	if strings.TrimSpace(bookmark.SessionID) == "" {
		return ""
	}
	return strings.Join([]string{"codog", "--resume", shellQuote(bookmark.SessionID), "repl"}, " ")
}

func renderBookmarksReport(out io.Writer, report bookmarksReport) {
	fmt.Fprintln(out, "Bookmarks")
	switch report.Action {
	case "list":
		fmt.Fprintf(out, "  Count            %d\n", report.Count)
		for _, bookmark := range report.Bookmarks {
			renderBookmarkLine(out, bookmark)
		}
	case "add", "show", "delete":
		if report.Bookmark != nil {
			renderBookmarkLine(out, *report.Bookmark)
		}
		if report.ResumeCommand != "" {
			fmt.Fprintf(out, "  Resume           %s\n", report.ResumeCommand)
		}
		if report.Message != "" {
			fmt.Fprintf(out, "  Message          %s\n", report.Message)
		}
	case "clear":
		fmt.Fprintf(out, "  Removed          %d\n", report.Removed)
		if report.Message != "" {
			fmt.Fprintf(out, "  Message          %s\n", report.Message)
		}
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  File             %s\n", report.Path)
	}
}

func renderBookmarkLine(out io.Writer, bookmark bookmarks.Bookmark) {
	fmt.Fprintf(out, "  %s  %s", bookmark.ID, bookmark.Name)
	if bookmark.SessionID != "" {
		fmt.Fprintf(out, "  session=%s", bookmark.SessionID)
	}
	if bookmark.MessageIndex != nil {
		fmt.Fprintf(out, "  message=%d", *bookmark.MessageIndex+1)
	}
	if bookmark.PRNumber > 0 {
		fmt.Fprintf(out, "  pr=%s", formatBookmarkPR(bookmark))
	}
	if bookmark.Note != "" {
		fmt.Fprintf(out, "  note=%s", bookmark.Note)
	}
	fmt.Fprintln(out)
}

func formatBookmarkPR(bookmark bookmarks.Bookmark) string {
	if bookmark.PRNumber <= 0 {
		return ""
	}
	if strings.TrimSpace(bookmark.PRRepo) != "" {
		return fmt.Sprintf("%s#%d", strings.TrimSpace(bookmark.PRRepo), bookmark.PRNumber)
	}
	return fmt.Sprintf("#%d", bookmark.PRNumber)
}

func (a *App) ClearCommand(args []string) error {
	format, err := parseClearCommandFormat(args)
	if err != nil {
		return err
	}
	sess, err := a.Sessions.CreateWithIdentity("", session.SessionIdentity{
		Workspace: a.Workspace,
		Worktree:  a.Workspace,
		Purpose:   "clear",
	})
	if err != nil {
		return err
	}
	exe := strings.TrimSpace(a.Executable)
	if exe == "" {
		exe = "codog"
	}
	report := clearCommandReport{
		Kind:         "clear",
		Action:       "create_session",
		Status:       "ok",
		SessionID:    sess.ID,
		MessageCount: len(sess.Messages),
		Path:         sess.Path,
		ContinueCommands: []string{
			strings.Join([]string{shellQuote(exe), "--session", shellQuote(sess.ID), "repl"}, " "),
			strings.Join([]string{shellQuote(exe), "--session", shellQuote(sess.ID), "prompt", shellQuote("...")}, " "),
		},
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderClearCommand(a.Out, report)
	return nil
}

func (a *App) ClearResumedSession(args []string, overrides config.FlagOverrides) error {
	req, err := parseClearResumedArgs(args, overrides)
	if err != nil {
		return err
	}
	if !req.Confirm {
		return renderClearConfirmationRequired(a.Out, req.Format)
	}
	sess, err := a.Sessions.Open(req.SessionID)
	if err != nil {
		return err
	}
	backup, err := writeSessionClearBackup(sess.Path, time.Now().UTC())
	if err != nil {
		return err
	}
	result, err := a.Sessions.ReplaceMessages(sess, nil)
	if err != nil {
		return err
	}
	report := clearResumedReport{
		Kind:              "clear",
		Action:            "clear_session",
		Status:            "ok",
		SessionID:         result.SessionID,
		Path:              result.Path,
		Backup:            backup,
		OriginalMessages:  result.OriginalMessages,
		RemainingMessages: result.RemainingMessages,
		RemovedMessages:   result.RemovedMessages,
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderClearResumed(a.Out, report)
	return nil
}

func parseClearResumedArgs(args []string, overrides config.FlagOverrides) (clearResumedRequest, error) {
	req := clearResumedRequest{Format: "text", SessionID: "latest"}
	if strings.TrimSpace(overrides.Resume) != "" {
		req.SessionID = overrides.Resume
	}
	if strings.TrimSpace(overrides.SessionID) != "" {
		req.SessionID = overrides.SessionID
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--confirm":
			req.Confirm = true
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("clear output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("clear session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) {
				return req, errors.New("clear resume id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		default:
			return req, fmt.Errorf("unknown clear argument %q", arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "clear"); err != nil {
		return req, err
	}
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = "latest"
	}
	return req, nil
}

func renderClearConfirmationRequired(out io.Writer, format string) error {
	report := slashErrorReport{
		Kind:      "confirmation_required",
		ErrorKind: "confirmation_required",
		Status:    "error",
		Command:   "/clear",
		Message:   "resumed /clear requires --confirm before modifying the selected session",
		Hint:      "Run `codog --resume SESSION /clear --confirm` to clear that saved session after writing a backup.",
	}
	err := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	}
	return &ExitError{Code: 1, Err: err}
}

func parseClearCommandFormat(args []string) (string, error) {
	const usage = "codog clear [--confirm] [--json|--output-format text|json]"
	format := "text"
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--confirm":
			continue
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return "", missingFlagValueError{Command: "clear", Flag: arg, Usage: usage}
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		default:
			if strings.HasPrefix(arg, "-") {
				return "", unknownOptionError{Command: "clear", Option: arg, Usage: usage}
			}
			return "", unexpectedExtraArgsError{Command: "clear", Args: []string{arg}, Usage: usage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("clear", format, []string{"text", "json"})
	if err != nil {
		return "", err
	}
	return normalizedFormat, nil
}

func renderClearCommand(out io.Writer, report clearCommandReport) {
	fmt.Fprintln(out, "Clear Session")
	fmt.Fprintf(out, "  Session ID        %s\n", report.SessionID)
	fmt.Fprintf(out, "  Messages          %d\n", report.MessageCount)
	fmt.Fprintf(out, "  Path              %s\n", report.Path)
	if len(report.ContinueCommands) > 0 {
		fmt.Fprintln(out, "  Continue")
		for _, command := range report.ContinueCommands {
			fmt.Fprintf(out, "    %s\n", command)
		}
	}
}

func renderClearResumed(out io.Writer, report clearResumedReport) {
	fmt.Fprintln(out, "Session Cleared")
	fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	fmt.Fprintf(out, "  Original         %d\n", report.OriginalMessages)
	fmt.Fprintf(out, "  Remaining        %d\n", report.RemainingMessages)
	fmt.Fprintf(out, "  Removed          %d\n", report.RemovedMessages)
	fmt.Fprintf(out, "  Backup           %s\n", report.Backup)
	fmt.Fprintf(out, "  Path             %s\n", report.Path)
}

func writeSessionClearBackup(path string, at time.Time) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for attempt := 0; attempt < 100; attempt++ {
		backup := sessionClearBackupPath(path, at, attempt)
		file, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil {
			return "", writeErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		return backup, nil
	}
	return "", fmt.Errorf("could not create a unique clear backup for %s", path)
}

func sessionClearBackupPath(path string, at time.Time, attempt int) string {
	suffix := ".clear-backup-" + at.UTC().Format("20060102T150405Z")
	if attempt > 0 {
		suffix += fmt.Sprintf("-%d", attempt)
	}
	return path + suffix
}

func (a *App) BackfillSessions(args []string) error {
	format, err := parseSimpleOutputFormat("backfill-sessions", args)
	if err != nil {
		return err
	}
	report, err := a.Sessions.BackfillPromptHistory()
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderBackfillSessions(a.Out, report)
	return nil
}

func (a *App) RepairSessions(args []string) error {
	format, err := parseSimpleOutputFormat("sessions repair", args)
	if err != nil {
		return err
	}
	report, err := a.Sessions.RepairSessionIdentities()
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderBackfillSessions(a.Out, report)
	return nil
}

func renderBackfillSessions(out io.Writer, report session.BackfillReport) {
	title := "Backfill Sessions"
	if report.Action == "identity_repair" {
		title = "Repair Sessions"
	}
	fmt.Fprintln(out, title)
	fmt.Fprintf(out, "  Sessions scanned %d\n", report.SessionsScanned)
	fmt.Fprintf(out, "  Sessions updated %d\n", report.SessionsUpdated)
	fmt.Fprintf(out, "  Inputs added      %d\n", report.InputsAdded)
	fmt.Fprintf(out, "  Identity updates  %d\n", report.IdentityUpdates)
	fmt.Fprintf(out, "  Skipped existing  %d\n", report.SkippedWithInputs)
	fmt.Fprintf(out, "  Skipped disabled  %d\n", report.SkippedDisabled)
	if len(report.SkippedSessionDetails) > 0 {
		fmt.Fprintln(out, "  Skipped sessions")
		for _, skipped := range report.SkippedSessionDetails {
			fmt.Fprintf(out, "    - %s %s\n", skipped.ID, skipped.Reason)
		}
	}
}

type sessionListReport struct {
	Kind           string              `json:"kind"`
	Status         string              `json:"status"`
	Action         string              `json:"action"`
	Sessions       []string            `json:"sessions"`
	SessionDetails []sessionListDetail `json:"session_details"`
	Active         string              `json:"active,omitempty"`
	Count          int                 `json:"count"`
	Total          int                 `json:"total"`
	Limit          int                 `json:"limit"`
	Offset         int                 `json:"offset"`
	HasMore        bool                `json:"has_more"`
	NextOffset     *int                `json:"next_offset,omitempty"`
	Workspace      string              `json:"workspace,omitempty"`
}

type sessionListRequest struct {
	Format    string
	Limit     int
	Offset    int
	UseLimit  bool
	UseOffset bool
}

type sessionListDetail struct {
	ID                  string                  `json:"id"`
	Path                string                  `json:"path"`
	MessageCount        int                     `json:"message_count"`
	CreatedAtMS         int64                   `json:"created_at_ms,omitempty"`
	UpdatedAtMS         int64                   `json:"updated_at_ms,omitempty"`
	ModifiedEpochMillis int64                   `json:"modified_epoch_millis,omitempty"`
	ParentSessionID     string                  `json:"parent_session_id,omitempty"`
	BranchName          string                  `json:"branch_name,omitempty"`
	PinnedMessages      []int                   `json:"pinned_messages,omitempty"`
	Lifecycle           sessionLifecycleReport  `json:"lifecycle"`
	Active              bool                    `json:"active,omitempty"`
	Identity            session.SessionIdentity `json:"identity,omitempty"`
}

type sessionLifecycleReport struct {
	Kind      string `json:"kind"`
	Signal    string `json:"signal"`
	Saved     bool   `json:"saved"`
	Abandoned bool   `json:"abandoned"`
}

func (a *App) ListSessions() error {
	return a.ListSessionsWithActive(nil, "")
}

func (a *App) ListSessionsWithActive(args []string, activeID string) error {
	req, err := parseSessionListArgs(args)
	if err != nil {
		return err
	}
	sessions, err := a.Sessions.List()
	if err != nil {
		return err
	}
	report := buildSessionListReport(sessions, activeID, a.Workspace, req)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderSessionListText(a.Out, report)
	return nil
}

func buildSessionListReport(sessions []session.Session, activeID string, workspace string, req sessionListRequest) sessionListReport {
	activeID = strings.TrimSpace(activeID)
	total := len(sessions)
	page, offset, limit, nextOffset := paginateSessions(sessions, req)
	report := sessionListReport{
		Kind:           "sessions",
		Status:         "ok",
		Action:         "list",
		Sessions:       make([]string, 0, len(page)),
		SessionDetails: make([]sessionListDetail, 0, len(page)),
		Active:         activeID,
		Count:          len(page),
		Total:          total,
		Limit:          limit,
		Offset:         offset,
		HasMore:        nextOffset != nil,
		NextOffset:     nextOffset,
		Workspace:      strings.TrimSpace(workspace),
	}
	for _, sess := range page {
		report.Sessions = append(report.Sessions, sess.ID)
		report.SessionDetails = append(report.SessionDetails, sessionListDetail{
			ID:                  sess.ID,
			Path:                sess.Path,
			MessageCount:        len(sess.Messages),
			CreatedAtMS:         timeMillis(sess.Metadata.CreatedAt),
			UpdatedAtMS:         timeMillis(sess.Metadata.UpdatedAt),
			ModifiedEpochMillis: timeMillis(sess.Metadata.ModifiedAt),
			ParentSessionID:     sess.Metadata.ParentSessionID,
			BranchName:          sess.Metadata.BranchName,
			PinnedMessages:      append([]int(nil), sess.Metadata.PinnedMessages...),
			Lifecycle:           lifecycleForStoredSession(&sess),
			Active:              activeID != "" && sess.ID == activeID,
			Identity:            sess.Identity,
		})
	}
	return report
}

func parseSessionListArgs(args []string) (sessionListRequest, error) {
	const usage = "codog sessions list [--limit N] [--offset N] [--json|--output-format text|json]"
	format, remaining, err := parseTemplateOutputArgs("sessions list", args)
	if err != nil {
		return sessionListRequest{}, err
	}
	req := sessionListRequest{Format: format}
	for index := 0; index < len(remaining); index++ {
		arg := remaining[index]
		switch {
		case arg == "--limit" || arg == "-n":
			index++
			if index >= len(remaining) {
				return req, missingFlagValueError{Command: "sessions list", Flag: arg, Usage: usage}
			}
			limit, err := parsePositiveIntOption(remaining[index], "--limit", usage)
			if err != nil {
				return req, err
			}
			req.Limit = limit
			req.UseLimit = true
		case strings.HasPrefix(arg, "--limit="):
			limit, err := parsePositiveIntOption(strings.TrimPrefix(arg, "--limit="), "--limit", usage)
			if err != nil {
				return req, err
			}
			req.Limit = limit
			req.UseLimit = true
		case arg == "--offset":
			index++
			if index >= len(remaining) {
				return req, missingFlagValueError{Command: "sessions list", Flag: arg, Usage: usage}
			}
			offset, err := parseNonNegativeIntOption(remaining[index], "--offset", usage)
			if err != nil {
				return req, err
			}
			req.Offset = offset
			req.UseOffset = true
		case strings.HasPrefix(arg, "--offset="):
			offset, err := parseNonNegativeIntOption(strings.TrimPrefix(arg, "--offset="), "--offset", usage)
			if err != nil {
				return req, err
			}
			req.Offset = offset
			req.UseOffset = true
		default:
			return req, unexpectedExtraArgsError{
				Command: "sessions list",
				Args:    []string{arg},
				Usage:   usage,
			}
		}
	}
	return req, nil
}

func paginateSessions(sessions []session.Session, req sessionListRequest) ([]session.Session, int, int, *int) {
	total := len(sessions)
	offset := 0
	if req.UseOffset {
		offset = req.Offset
		if offset > total {
			offset = total
		}
	}
	limit := total - offset
	if req.UseLimit {
		limit = req.Limit
	}
	if limit < 0 {
		limit = 0
	}
	end := offset + limit
	if end > total {
		end = total
	}
	nextOffset := (*int)(nil)
	if end < total {
		next := end
		nextOffset = &next
	}
	return sessions[offset:end], offset, limit, nextOffset
}

func renderSessionListText(out io.Writer, report sessionListReport) {
	if report.HasMore {
		fmt.Fprintf(out, "Showing %d of %d sessions from offset %d (next offset %d)\n", report.Count, report.Total, report.Offset, *report.NextOffset)
	}
	for _, sess := range report.SessionDetails {
		active := ""
		if sess.Active {
			active = "\tactive"
		}
		pinned := ""
		if len(sess.PinnedMessages) > 0 {
			pinned = "\tpinned=" + formatIntList(sess.PinnedMessages)
		}
		lineage := ""
		switch {
		case sess.ParentSessionID != "" && sess.BranchName != "":
			lineage = fmt.Sprintf("\tbranch=%s from=%s", sess.BranchName, sess.ParentSessionID)
		case sess.ParentSessionID != "":
			lineage = fmt.Sprintf("\tfrom=%s", sess.ParentSessionID)
		case sess.BranchName != "":
			lineage = fmt.Sprintf("\tbranch=%s", sess.BranchName)
		}
		fmt.Fprintf(out, "%s\t%d messages\tlifecycle=%s\t%s%s%s%s\n", sess.ID, sess.MessageCount, sess.Lifecycle.Signal, sess.Path, lineage, pinned, active)
	}
}

type sessionAuditReport struct {
	Kind                      string              `json:"kind"`
	Action                    string              `json:"action"`
	Status                    string              `json:"status"`
	Workspace                 string              `json:"workspace,omitempty"`
	SessionDir                string              `json:"session_dir,omitempty"`
	LegacySessionDir          string              `json:"legacy_session_dir,omitempty"`
	SessionCount              int                 `json:"session_count"`
	MessageCount              int                 `json:"message_count"`
	EmptyCount                int                 `json:"empty_count"`
	BranchCount               int                 `json:"branch_count"`
	PinnedMessageCount        int                 `json:"pinned_message_count"`
	PlaceholderIdentityCount  int                 `json:"placeholder_identity_count"`
	RepairableIdentityCount   int                 `json:"repairable_identity_count,omitempty"`
	ManualIdentityReviewCount int                 `json:"manual_identity_review_count,omitempty"`
	MissingIdentityCount      int                 `json:"missing_identity_count"`
	WorkspaceMismatchCount    int                 `json:"workspace_mismatch_count"`
	PinnedOutOfRangeCount     int                 `json:"pinned_out_of_range_count"`
	OversizedFileCount        int                 `json:"oversized_file_count"`
	Issues                    []sessionAuditIssue `json:"issues,omitempty"`
	NextActions               []string            `json:"next_actions,omitempty"`
}

type sessionAuditIssue struct {
	Kind         string `json:"kind"`
	Severity     string `json:"severity"`
	SessionID    string `json:"session_id"`
	Path         string `json:"path,omitempty"`
	Field        string `json:"field,omitempty"`
	MessageIndex int    `json:"message_index,omitempty"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	Message      string `json:"message"`
	NextAction   string `json:"next_action,omitempty"`
}

func (a *App) auditSessionsWithReport() (sessionAuditReport, error) {
	sessions, err := a.Sessions.List()
	if err != nil {
		return sessionAuditReport{}, err
	}
	report := sessionAuditReport{
		Kind:               "session_audit",
		Action:             "audit",
		Status:             "ok",
		Workspace:          strings.TrimSpace(a.Workspace),
		SessionDir:         a.Sessions.Dir,
		LegacySessionDir:   a.Sessions.LegacyDir,
		SessionCount:       len(sessions),
		Issues:             []sessionAuditIssue{},
		NextActions:        []string{},
		BranchCount:        0,
		PinnedMessageCount: 0,
	}
	for _, sess := range sessions {
		report.MessageCount += len(sess.Messages)
		if strings.TrimSpace(sess.Metadata.ParentSessionID) != "" || strings.TrimSpace(sess.Metadata.BranchName) != "" {
			report.BranchCount++
		}
		report.PinnedMessageCount += len(sess.Metadata.PinnedMessages)
		report.auditSession(sess)
	}
	if len(report.Issues) != 0 {
		for _, issue := range report.Issues {
			if issue.Severity == "warn" || issue.Severity == "error" {
				report.Status = "warn"
				break
			}
		}
	}
	report.NextActions = sessionAuditNextActions(report)
	return report, nil
}

func doctorSessionHygiene(report sessionAuditReport) *doctor.SessionHygiene {
	issues := make([]doctor.SessionHygieneIssue, 0, len(report.Issues))
	for _, issue := range report.Issues {
		issues = append(issues, doctor.SessionHygieneIssue{
			Kind:         issue.Kind,
			Severity:     issue.Severity,
			SessionID:    issue.SessionID,
			Path:         issue.Path,
			Field:        issue.Field,
			MessageIndex: issue.MessageIndex,
			SizeBytes:    issue.SizeBytes,
			Message:      issue.Message,
			NextAction:   issue.NextAction,
		})
	}
	return &doctor.SessionHygiene{
		Status:                    report.Status,
		Workspace:                 report.Workspace,
		SessionDir:                report.SessionDir,
		LegacySessionDir:          report.LegacySessionDir,
		SessionCount:              report.SessionCount,
		MessageCount:              report.MessageCount,
		EmptyCount:                report.EmptyCount,
		BranchCount:               report.BranchCount,
		PinnedMessageCount:        report.PinnedMessageCount,
		PlaceholderIdentityCount:  report.PlaceholderIdentityCount,
		RepairableIdentityCount:   report.RepairableIdentityCount,
		ManualIdentityReviewCount: report.ManualIdentityReviewCount,
		MissingIdentityCount:      report.MissingIdentityCount,
		WorkspaceMismatchCount:    report.WorkspaceMismatchCount,
		PinnedOutOfRangeCount:     report.PinnedOutOfRangeCount,
		OversizedFileCount:        report.OversizedFileCount,
		Issues:                    issues,
		NextActions:               append([]string(nil), report.NextActions...),
	}
}

func (report *sessionAuditReport) auditSession(sess session.Session) {
	if len(sess.Messages) == 0 {
		report.EmptyCount++
		report.Issues = append(report.Issues, sessionAuditIssue{
			Kind:       "empty_session",
			Severity:   "info",
			SessionID:  sess.ID,
			Path:       sess.Path,
			Message:    "session has no saved messages",
			NextAction: "codog sessions prune --empty --confirm",
		})
	}
	identityRepairable := sessionHasUserPromptText(sess)
	for _, placeholder := range sess.Identity.Placeholders {
		report.PlaceholderIdentityCount++
		field := strings.TrimSpace(placeholder.Field)
		message := "session identity still uses a typed placeholder"
		if strings.TrimSpace(placeholder.Reason) != "" {
			message += ": " + strings.TrimSpace(placeholder.Reason)
		}
		nextAction := "codog sessions repair"
		if identityRepairable {
			report.RepairableIdentityCount++
		} else {
			report.ManualIdentityReviewCount++
			nextAction = "codog sessions show " + shellQuote(sess.ID) + " --json"
			message += "; no saved user prompt is available for automatic repair"
		}
		report.Issues = append(report.Issues, sessionAuditIssue{
			Kind:       "identity_placeholder",
			Severity:   "warn",
			SessionID:  sess.ID,
			Path:       sess.Path,
			Field:      field,
			Message:    message,
			NextAction: nextAction,
		})
	}
	for _, field := range missingSessionIdentityFields(sess.Identity) {
		report.MissingIdentityCount++
		report.Issues = append(report.Issues, sessionAuditIssue{
			Kind:       "identity_missing_field",
			Severity:   "warn",
			SessionID:  sess.ID,
			Path:       sess.Path,
			Field:      field,
			Message:    "session identity is missing " + field,
			NextAction: "codog sessions show " + shellQuote(sess.ID) + " --json",
		})
	}
	if strings.TrimSpace(report.Workspace) != "" && strings.TrimSpace(sess.Identity.Workspace) != "" && !sameCleanPath(report.Workspace, sess.Identity.Workspace) {
		report.WorkspaceMismatchCount++
		report.Issues = append(report.Issues, sessionAuditIssue{
			Kind:       "workspace_mismatch",
			Severity:   "warn",
			SessionID:  sess.ID,
			Path:       sess.Path,
			Field:      "workspace",
			Message:    "session identity workspace differs from the current workspace",
			NextAction: "codog sessions show " + shellQuote(sess.ID) + " --json",
		})
	}
	for _, index := range sess.Metadata.PinnedMessages {
		if index >= 0 && index < len(sess.Messages) {
			continue
		}
		report.PinnedOutOfRangeCount++
		report.Issues = append(report.Issues, sessionAuditIssue{
			Kind:         "pinned_message_out_of_range",
			Severity:     "warn",
			SessionID:    sess.ID,
			Path:         sess.Path,
			MessageIndex: index + 1,
			Message:      "pinned message index is outside the saved message range",
			NextAction:   "codog sessions unpin " + shellQuote(sess.ID) + " " + strconv.Itoa(index+1),
		})
	}
	if info, err := os.Stat(sess.Path); err == nil && info.Size() >= session.MaxSessionJSONLBytes {
		report.OversizedFileCount++
		report.Issues = append(report.Issues, sessionAuditIssue{
			Kind:       "oversized_jsonl",
			Severity:   "warn",
			SessionID:  sess.ID,
			Path:       sess.Path,
			SizeBytes:  info.Size(),
			Message:    "session JSONL is large enough to slow resume and tooling",
			NextAction: "codog compact --session " + shellQuote(sess.ID) + " --json",
		})
	}
}

func missingSessionIdentityFields(identity session.SessionIdentity) []string {
	var fields []string
	if strings.TrimSpace(identity.Title) == "" {
		fields = append(fields, "title")
	}
	if strings.TrimSpace(identity.Workspace) == "" {
		fields = append(fields, "workspace")
	}
	if strings.TrimSpace(identity.Worktree) == "" {
		fields = append(fields, "worktree")
	}
	return fields
}

func sessionHasUserPromptText(sess session.Session) bool {
	for _, message := range sess.Messages {
		if message.Role != "user" {
			continue
		}
		if strings.TrimSpace(promptTextFromAnthropicContent(message.Content)) != "" {
			return true
		}
	}
	return false
}

func sameCleanPath(left string, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return left == right
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func sessionAuditNextActions(report sessionAuditReport) []string {
	actions := []string{}
	if report.SessionCount == 0 {
		actions = append(actions, "codog repl")
	}
	if report.EmptyCount > 0 {
		actions = append(actions, "codog sessions prune --empty --confirm")
	}
	for _, issue := range report.Issues {
		if strings.TrimSpace(issue.NextAction) != "" {
			actions = append(actions, issue.NextAction)
		}
	}
	return dedupeStrings(actions)
}

func renderSessionAuditText(out io.Writer, report sessionAuditReport) {
	fmt.Fprintln(out, "Session Audit")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Workspace        %s\n", report.Workspace)
	fmt.Fprintf(out, "  Sessions         %d\n", report.SessionCount)
	fmt.Fprintf(out, "  Messages         %d\n", report.MessageCount)
	fmt.Fprintf(out, "  Empty            %d\n", report.EmptyCount)
	fmt.Fprintf(out, "  Branches         %d\n", report.BranchCount)
	fmt.Fprintf(out, "  Pinned messages  %d\n", report.PinnedMessageCount)
	fmt.Fprintf(out, "  Placeholders     %d\n", report.PlaceholderIdentityCount)
	fmt.Fprintf(out, "  Repairable ids   %d\n", report.RepairableIdentityCount)
	fmt.Fprintf(out, "  Manual id review %d\n", report.ManualIdentityReviewCount)
	fmt.Fprintf(out, "  Missing identity %d\n", report.MissingIdentityCount)
	fmt.Fprintf(out, "  Workspace drift  %d\n", report.WorkspaceMismatchCount)
	fmt.Fprintf(out, "  Pin drift        %d\n", report.PinnedOutOfRangeCount)
	fmt.Fprintf(out, "  Oversized files  %d\n", report.OversizedFileCount)
	if len(report.Issues) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "  Issues")
		for _, issue := range report.Issues {
			fmt.Fprintf(out, "    - [%s] %s %s: %s\n", issue.Severity, issue.SessionID, issue.Kind, issue.Message)
		}
	}
	if len(report.NextActions) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "  Next actions")
		for _, action := range report.NextActions {
			fmt.Fprintf(out, "    - %s\n", action)
		}
	}
}

type sessionSearchRequest struct {
	Query  string
	Limit  int
	Format string
}

type sessionSearchReport struct {
	Kind            string               `json:"kind"`
	Action          string               `json:"action"`
	Status          string               `json:"status"`
	Query           string               `json:"query"`
	Count           int                  `json:"count"`
	ScannedSessions int                  `json:"scanned_sessions"`
	MatchLimit      int                  `json:"match_limit"`
	Truncated       bool                 `json:"truncated,omitempty"`
	Matches         []sessionSearchMatch `json:"matches"`
}

type sessionSearchMatch struct {
	SessionID    string `json:"session_id"`
	Path         string `json:"path"`
	MessageCount int    `json:"message_count"`
	MessageIndex int    `json:"message_index,omitempty"`
	Role         string `json:"role,omitempty"`
	Field        string `json:"field"`
	Snippet      string `json:"snippet"`
	Title        string `json:"title,omitempty"`
	UpdatedAtMS  int64  `json:"updated_at_ms,omitempty"`
}

func parseSessionSearchArgs(command string, args []string, defaultFormat string) (sessionSearchRequest, error) {
	req := sessionSearchRequest{Limit: 50, Format: defaultFormat}
	if req.Format == "" {
		req.Format = "text"
	}
	usage := command + " QUERY [--limit N] [--json|--output-format text|json]"
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "":
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: command, Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--limit":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: command, Flag: arg, Usage: usage}
			}
			limit, err := parsePositiveIntOption(args[index], "--limit", usage)
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := parsePositiveIntOption(strings.TrimPrefix(arg, "--limit="), "--limit", usage)
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: command, Option: arg, Usage: usage}
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) == 0 {
		return req, fmt.Errorf("usage: %s", usage)
	}
	req.Query = strings.TrimSpace(strings.Join(positionals, " "))
	if req.Query == "" {
		return req, fmt.Errorf("usage: %s", usage)
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown %s output format %q", command, req.Format)
	}
	return req, nil
}

func (a *App) searchSessionsWithReport(req sessionSearchRequest) (sessionSearchReport, error) {
	sessions, err := a.Sessions.List()
	if err != nil {
		return sessionSearchReport{}, err
	}
	report := sessionSearchReport{
		Kind:            "session_search",
		Action:          "search",
		Status:          "ok",
		Query:           req.Query,
		ScannedSessions: len(sessions),
		MatchLimit:      req.Limit,
		Matches:         []sessionSearchMatch{},
	}
	for _, sess := range sessions {
		for _, match := range sessionSearchMatches(sess, req.Query) {
			if len(report.Matches) >= req.Limit {
				report.Truncated = true
				report.Count = len(report.Matches)
				return report, nil
			}
			report.Matches = append(report.Matches, match)
		}
	}
	report.Count = len(report.Matches)
	return report, nil
}

func sessionSearchMatches(sess session.Session, query string) []sessionSearchMatch {
	matches := []sessionSearchMatch{}
	addField := func(field string, value string) {
		if !sessionSearchContains(value, query) {
			return
		}
		matches = append(matches, sessionSearchMatch{
			SessionID:    sess.ID,
			Path:         sess.Path,
			MessageCount: len(sess.Messages),
			Field:        field,
			Snippet:      sessionSearchSnippet(value, query, 180),
			Title:        sess.Identity.Title,
			UpdatedAtMS:  timeMillis(sess.Metadata.UpdatedAt),
		})
	}
	addField("id", sess.ID)
	addField("title", sess.Identity.Title)
	addField("workspace", sess.Identity.Workspace)
	addField("worktree", sess.Identity.Worktree)
	addField("purpose", sess.Identity.Purpose)
	for index, msg := range sess.Messages {
		text := strings.TrimSpace(renderMessagePlainText(msg))
		if !sessionSearchContains(text, query) {
			continue
		}
		matches = append(matches, sessionSearchMatch{
			SessionID:    sess.ID,
			Path:         sess.Path,
			MessageCount: len(sess.Messages),
			MessageIndex: index + 1,
			Role:         msg.Role,
			Field:        "message",
			Snippet:      sessionSearchSnippet(text, query, 180),
			Title:        sess.Identity.Title,
			UpdatedAtMS:  timeMillis(sess.Metadata.UpdatedAt),
		})
	}
	return matches
}

func sessionSearchContains(value string, query string) bool {
	value = strings.TrimSpace(value)
	query = strings.TrimSpace(query)
	if value == "" || query == "" {
		return false
	}
	return strings.Contains(strings.ToLower(value), strings.ToLower(query))
}

func sessionSearchSnippet(value string, query string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	lowerValue := strings.ToLower(value)
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	index := strings.Index(lowerValue, lowerQuery)
	if index < 0 {
		runes := []rune(value)
		return string(runes[:min(len(runes), limit)])
	}
	start := max(0, index-(limit/3))
	end := min(len(value), start+limit)
	if end-start < limit {
		start = max(0, end-limit)
	}
	for start > 0 && !utf8.RuneStart(value[start]) {
		start--
	}
	for end < len(value) && !utf8.RuneStart(value[end]) {
		end++
	}
	snippet := strings.TrimSpace(value[start:end])
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(value) {
		snippet += "..."
	}
	return snippet
}

func renderSessionSearchText(out io.Writer, report sessionSearchReport) {
	fmt.Fprintln(out, "Session Search")
	fmt.Fprintf(out, "  Query            %s\n", report.Query)
	fmt.Fprintf(out, "  Sessions scanned %d\n", report.ScannedSessions)
	fmt.Fprintf(out, "  Matches          %d\n", report.Count)
	if report.Truncated {
		fmt.Fprintf(out, "  Truncated        yes, limit=%d\n", report.MatchLimit)
	}
	for _, match := range report.Matches {
		location := match.Field
		if match.MessageIndex > 0 {
			location = fmt.Sprintf("message %d %s", match.MessageIndex, match.Role)
		}
		fmt.Fprintf(out, "  - %s\t%s\t%s\n", match.SessionID, location, match.Snippet)
	}
}

func lifecycleForStoredSession(sess *session.Session) sessionLifecycleReport {
	kind := "saved_only"
	signal := "saved only"
	if sess != nil && len(sess.Messages) == 0 {
		kind = "empty"
		signal = "empty saved session"
	}
	return sessionLifecycleReport{
		Kind:   kind,
		Signal: signal,
		Saved:  true,
	}
}

func timeMillis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixMilli()
}

func (a *App) ListSkills() error {
	return a.Skills(nil)
}

func (a *App) Skills(args []string) error {
	action := "list"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = normalizeSkillsAction(args[0])
		rest = args[1:]
	}
	switch action {
	case "list":
		return a.listSkills(rest)
	case "search":
		return a.skillsSearch(rest)
	case "audit":
		return a.skillAudit(rest)
	case "sources":
		return a.skillSources(rest)
	case "help":
		return renderCommandHelpTopic(a.Out, "skills", rest, "text")
	case "show":
		return a.skillsShow(rest)
	case "invoke":
		return a.skillsInvoke(rest)
	case "install":
		return a.skillsInstall(rest)
	case "uninstall":
		return a.skillsUninstall(rest)
	case "enable":
		return a.skillActivation("enable", rest)
	case "disable":
		return a.skillActivation("disable", rest)
	case "status":
		return a.skillActivation("status", rest)
	default:
		_, format, err := stripJSONOnlyOutputFormat("skills", rest)
		if err != nil {
			return renderCLIError(a.Out, err, requestedOutputFormat(append([]string{"skills", action}, rest...)))
		}
		return renderUnsupportedSkillsAction(a.Out, action, format)
	}
}

func (a *App) skillsSearch(args []string) error {
	format, remaining, err := parseTemplateOutputArgs("skills search", args)
	if err != nil {
		return err
	}
	query := strings.TrimSpace(strings.Join(remaining, " "))
	if query == "" {
		return renderMissingActionArgument(a.Out, "skills", "search", "query", "skills search requires a query", "Usage: codog skills search QUERY [--json|--output-format text|json].", format)
	}
	return a.listSkillsWithAction([]string{query, "--output-format", format}, "search", query)
}

func (a *App) skillsShow(args []string) error {
	format, remaining, err := parseTemplateOutputArgs("skills show", args)
	if err != nil {
		return err
	}
	if len(remaining) < 1 {
		return a.listSkills([]string{"--output-format", format})
	}
	if len(remaining) > 1 {
		return renderCLIError(a.Out, unexpectedExtraArgsError{
			Command: "skills show",
			Args:    append([]string(nil), remaining[1:]...),
			Usage:   "codog skills show NAME [--json|--output-format text|json]",
		}, format)
	}
	skill, err := a.findRuntimeSkill(remaining[0])
	if err != nil {
		return renderSkillLookupError(a.Out, "show", remaining[0], err, format)
	}
	return renderSkill(a.Out, skill, format)
}

func renderSkill(out io.Writer, skill skills.Skill, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(skill, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprint(out, skill.Body)
	if !strings.HasSuffix(skill.Body, "\n") {
		fmt.Fprintln(out)
	}
	return nil
}

func (a *App) skillsInvoke(args []string) error {
	format, remaining, err := parseTemplateOutputArgs("skills invoke", args)
	if err != nil {
		return err
	}
	if len(remaining) < 1 {
		return renderMissingActionArgument(a.Out, "skills", "invoke", "skill_name", "skills invoke requires a skill name", "Usage: codog skills invoke NAME [ARGS...] [--json|--output-format text|json]. Run `codog skills list` to see available skills.", format)
	}
	skill, err := a.findRuntimeSkill(remaining[0])
	if err != nil {
		return renderSkillLookupError(a.Out, "invoke", remaining[0], err, format)
	}
	rendered := skills.RenderInvocation(skill, strings.Join(remaining[1:], " "))
	if format == "json" {
		data, _ := json.MarshalIndent(map[string]any{
			"kind":     "skill_invocation",
			"name":     skill.Name,
			"source":   skill.Source,
			"origin":   skill.Origin,
			"path":     skill.Path,
			"rendered": rendered,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, rendered)
	return nil
}

func (a *App) skillsInstall(args []string) error {
	req, err := parseSkillInstallArgs(args)
	if err != nil {
		if errors.Is(err, errSkillInstallMissingSource) {
			return renderSkillsInstallMissingSource(a.Out, req.Format)
		}
		return err
	}
	targetRoot, targetLabel, err := a.skillTargetRoot(req.Target)
	if err != nil {
		return err
	}
	report, err := skills.Install(req.Source, targetRoot, req.Name, targetLabel)
	if err != nil {
		return renderSkillLookupError(a.Out, "install", req.Source, err, req.Format)
	}
	return renderSkillInstallReport(a.Out, report, req.Format)
}

func renderSkillInstallReport(out io.Writer, report skills.InstallReport, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Skill Installed")
	fmt.Fprintf(out, "  Name             %s\n", report.Name)
	fmt.Fprintf(out, "  Target           %s\n", report.Target)
	fmt.Fprintf(out, "  Path             %s\n", report.Path)
	return nil
}

func (a *App) skillsUninstall(args []string) error {
	req, err := parseSkillUninstallArgs(args)
	if err != nil {
		if errors.Is(err, errSkillUninstallMissingName) {
			return renderMissingActionArgument(a.Out, "skills", "uninstall", "skill_name", "skills uninstall requires a skill name", "Usage: codog skills uninstall NAME [--project|--user|--claude] [--json|--output-format text|json]. Run `codog skills list` to see installed skills.", req.Format)
		}
		return err
	}
	report, err := skills.Uninstall(req.Name, a.skillUninstallRoots(req.Target))
	if err != nil {
		return renderSkillLookupError(a.Out, "uninstall", req.Name, err, req.Format)
	}
	return renderSkillUninstallReport(a.Out, report, req.Format)
}

func renderSkillUninstallReport(out io.Writer, report skills.UninstallReport, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Skill Uninstalled")
	fmt.Fprintf(out, "  Name             %s\n", report.Name)
	fmt.Fprintf(out, "  Path             %s\n", report.Path)
	return nil
}

func normalizeSkillsAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "list", "ls":
		return "list"
	case "search", "find", "query", "lookup":
		return "search"
	case "audit", "doctor", "check", "validate":
		return "audit"
	case "source", "sources", "root", "roots":
		return "sources"
	case "show", "info", "describe", "get", "view", "cat":
		return "show"
	case "invoke", "run", "exec", "execute", "call":
		return "invoke"
	case "install", "add":
		return "install"
	case "uninstall", "remove", "delete", "rm", "del":
		return "uninstall"
	case "enable", "on":
		return "enable"
	case "disable", "off":
		return "disable"
	case "status", "enabled":
		return "status"
	case "help":
		return "help"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

func renderSkillLookupError(out io.Writer, action string, subject string, err error, format string) error {
	if errors.Is(err, skills.ErrNotFound) {
		return renderSkillNotFound(out, action, subject, format)
	}
	var sourceMissing skills.SourceNotFoundError
	if errors.As(err, &sourceMissing) {
		source := strings.TrimSpace(sourceMissing.Source)
		if source == "" {
			source = subject
		}
		return renderSkillNotFound(out, action, source, format)
	}
	return err
}

func renderSkillNotFound(out io.Writer, action string, subject string, format string) error {
	action = strings.TrimSpace(action)
	if action == "" {
		action = "show"
	}
	subject = strings.TrimSpace(subject)
	message := "skill was not found"
	if subject != "" {
		if action == "install" {
			message = fmt.Sprintf("skill source %q was not found", subject)
		} else {
			message = fmt.Sprintf("skill %q was not found", subject)
		}
	}
	return renderActionError(out, actionErrorReport{
		Kind:      "skills",
		Action:    action,
		Status:    "error",
		ErrorKind: "skill_not_found",
		Message:   message,
		Hint:      "Run `codog skills list` to see available skills, or `codog skills add <path>` / `codog skills install <path>` to install one.",
	}, format)
}

func renderSkillsInstallMissingSource(out io.Writer, format string) error {
	return renderActionError(out, actionErrorReport{
		Kind:      "skills",
		Action:    "install",
		Status:    "error",
		ErrorKind: "missing_argument",
		Argument:  "install_source",
		Message:   "skills install requires a source",
		Hint:      "Usage: codog skills install [--project|--user|--claude] [--name NAME] SOURCE [--json|--output-format text|json].",
	}, format)
}

func renderUnsupportedSkillsAction(out io.Writer, action string, format string) error {
	action = strings.TrimSpace(action)
	if action == "" {
		action = "unknown"
	}
	return renderActionError(out, actionErrorReport{
		Kind:      "skills",
		Action:    action,
		Status:    "error",
		ErrorKind: "unsupported_skills_action",
		Message:   fmt.Sprintf("unsupported skills action %q", action),
		Hint:      unknownSkillsActionHint(action),
	}, format)
}

var skillsActionCandidates = []string{
	"list", "ls", "search", "find", "query", "lookup", "audit", "doctor", "check",
	"validate", "source", "sources", "root", "roots", "show", "info", "describe",
	"get", "view", "cat", "invoke", "run", "exec", "execute", "call", "install",
	"add", "uninstall", "remove", "delete", "rm", "del", "enable", "on", "disable",
	"off", "status", "enabled", "help",
}

func unknownSkillsActionHint(action string) string {
	suggestions := toolnames.Suggestions(action, skillsActionCandidates, 4)
	switch len(suggestions) {
	case 1:
		return fmt.Sprintf("Did you mean `codog skills %s`? Use `codog skills list` to see available skills.", suggestions[0])
	case 0:
		return "Supported: `codog skills list|ls [FILTER]`, `codog skills search|find QUERY`, `codog skills audit|doctor`, `codog skills sources|roots`, `codog skills status`, `codog skills enable|on NAME`, `codog skills disable|off NAME`, `codog skills show|info|describe|view NAME`, `codog skills invoke|run|exec NAME [ARGS...]`, `codog skills add|install SOURCE`, `codog skills uninstall|remove|rm NAME`, or `codog skills help`."
	default:
		return fmt.Sprintf("Did you mean one of: %s? Use `codog skills list` to see available skills.", strings.Join(suggestions, ", "))
	}
}

type skillInstallRequest struct {
	Format string
	Target string
	Name   string
	Source string
}

type skillUninstallRequest struct {
	Format string
	Target string
	Name   string
}

type skillActivationRequest struct {
	Action string
	Format string
	Target string
	Path   string
	Names  []string
}

var (
	errSkillInstallMissingSource = errors.New("skills install source is required")
	errSkillUninstallMissingName = errors.New("skills uninstall name is required")
)

type skillActivationReport struct {
	Kind                string   `json:"kind"`
	Action              string   `json:"action"`
	Status              string   `json:"status"`
	Target              string   `json:"target,omitempty"`
	Path                string   `json:"path,omitempty"`
	EnabledSkills       []string `json:"enabled_skills"`
	Added               []string `json:"added,omitempty"`
	Removed             []string `json:"removed,omitempty"`
	Unchanged           []string `json:"unchanged,omitempty"`
	AvailableSkillCount int      `json:"available_skill_count,omitempty"`
	ResolvedSkills      []string `json:"resolved_skills,omitempty"`
	MissingSkills       []string `json:"missing_skills,omitempty"`
	Message             string   `json:"message,omitempty"`
}

type skillAuditReport struct {
	Kind                string                 `json:"kind"`
	Action              string                 `json:"action"`
	Status              string                 `json:"status"`
	SkillCount          int                    `json:"skill_count"`
	ActiveSkillCount    int                    `json:"active_skill_count"`
	ShadowedSkillCount  int                    `json:"shadowed_skill_count"`
	EnabledSkills       []string               `json:"enabled_skills"`
	ResolvedSkills      []string               `json:"resolved_skills,omitempty"`
	MissingSkills       []string               `json:"missing_skills,omitempty"`
	SourceCount         int                    `json:"source_count"`
	Sources             []skills.DiscoveryRoot `json:"sources"`
	MetadataDriftCount  int                    `json:"metadata_drift_count"`
	MetadataDrift       []skills.MetadataDrift `json:"metadata_drift,omitempty"`
	AvailableSkillCount int                    `json:"available_skill_count"`
	Message             string                 `json:"message"`
}

func (a *App) skillActivation(action string, args []string) error {
	req, err := parseSkillActivationArgs(action, args)
	if err != nil {
		return renderCLIError(a.Out, err, req.Format)
	}
	if req.Action == "status" {
		report, err := a.buildSkillActivationStatus(req)
		if err != nil {
			return err
		}
		return renderSkillActivationReport(a.Out, req.Format, report)
	}
	if len(req.Names) == 0 {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "skills",
			Action:    req.Action,
			Status:    "error",
			ErrorKind: "missing_skill_name",
			Message:   fmt.Sprintf("skills %s requires at least one skill name", req.Action),
			Hint:      fmt.Sprintf("Usage: codog skills %s NAME [NAME...] [--target user|project|local|--path PATH] [--json]", req.Action),
		}, req.Format)
	}
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	current := normalizeEnabledSkillNames(a.Config.EnabledSkills)
	next := append([]string(nil), current...)
	report := skillActivationReport{
		Kind:          "skills",
		Action:        req.Action,
		Status:        "ok",
		Target:        normalizedConfigTarget(req.Target),
		Path:          path,
		EnabledSkills: next,
	}
	switch req.Action {
	case "enable":
		for _, name := range req.Names {
			skill, err := a.findRuntimeSkill(name)
			if err != nil {
				return renderSkillLookupError(a.Out, "enable", name, err, req.Format)
			}
			if containsFold(next, skill.Name) {
				report.Unchanged = appendUniqueFold(report.Unchanged, skill.Name)
				continue
			}
			next = append(next, skill.Name)
			report.Added = appendUniqueFold(report.Added, skill.Name)
		}
		report.Message = activationSummary("enabled", report.Added, report.Unchanged)
	case "disable":
		var removed []string
		for _, name := range req.Names {
			removeName := name
			if skill, err := a.findRuntimeSkill(name); err == nil {
				removeName = skill.Name
			}
			var changed bool
			next, changed = removeEnabledSkillName(next, removeName)
			if changed {
				removed = appendUniqueFold(removed, removeName)
			} else {
				report.Unchanged = appendUniqueFold(report.Unchanged, name)
			}
		}
		report.Removed = removed
		report.Message = activationSummary("disabled", report.Removed, report.Unchanged)
	}
	report.EnabledSkills = next
	if len(next) == 0 {
		if _, err := config.UnsetFileValue(path, "enabled_skills"); err != nil {
			return err
		}
	} else if _, err := config.SetFileValue(path, "enabled_skills", next); err != nil {
		return err
	}
	a.Config.EnabledSkills = append([]string(nil), next...)
	return renderSkillActivationReport(a.Out, req.Format, report)
}

func parseSkillActivationArgs(action string, args []string) (skillActivationRequest, error) {
	req := skillActivationRequest{
		Action: canonicalSkillActivationAction(action),
		Format: "text",
		Target: "user",
	}
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("skills %s output format is required", req.Action)
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("skills %s target is required", req.Action)
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("skills %s path is required", req.Action)
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--user" || arg == "--global":
			req.Target = "user"
		case arg == "--project" || arg == "--workspace":
			req.Target = "project"
		case arg == "--local":
			req.Target = "local"
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{
				Kind:    "unknown_option",
				Command: "skills " + req.Action,
				Option:  arg,
				Usage:   skillActivationUsage(req.Action),
			}
		default:
			positionals = append(positionals, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "skills "+req.Action); err != nil {
		return req, err
	}
	req.Names = positionals
	if req.Action == "status" && len(positionals) != 0 {
		return req, unexpectedExtraArgsError{
			Command: "skills status",
			Args:    append([]string(nil), positionals...),
			Usage:   skillActivationUsage("status"),
		}
	}
	return req, nil
}

func canonicalSkillActivationAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "on":
		return "enable"
	case "off":
		return "disable"
	case "enabled":
		return "status"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

func skillActivationUsage(action string) string {
	if action == "status" {
		return "codog skills status [--json|--output-format text|json]"
	}
	return fmt.Sprintf("codog skills %s NAME [NAME...] [--target user|project|local|--path PATH] [--json|--output-format text|json]", action)
}

func (a *App) buildSkillActivationStatus(req skillActivationRequest) (skillActivationReport, error) {
	all, err := a.runtimeSkills()
	if err != nil {
		return skillActivationReport{}, err
	}
	enabled := normalizeEnabledSkillNames(a.Config.EnabledSkills)
	available := activeSkillNames(all)
	resolved, missing := resolveEnabledSkillNames(enabled, available)
	status := "ok"
	if len(missing) != 0 {
		status = "degraded"
	}
	return skillActivationReport{
		Kind:                "skills",
		Action:              "status",
		Status:              status,
		EnabledSkills:       enabled,
		AvailableSkillCount: len(available),
		ResolvedSkills:      resolved,
		MissingSkills:       missing,
		Message:             skillStatusMessage(enabled, missing),
	}, nil
}

func renderSkillActivationReport(out io.Writer, format string, report skillActivationReport) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	title := "Skill Status"
	switch report.Action {
	case "enable":
		title = "Skills Enabled"
	case "disable":
		title = "Skills Disabled"
	}
	fmt.Fprintln(out, title)
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Target != "" {
		fmt.Fprintf(out, "  Target           %s\n", report.Target)
	}
	if len(report.Added) != 0 {
		fmt.Fprintf(out, "  Added            %s\n", strings.Join(report.Added, ", "))
	}
	if len(report.Removed) != 0 {
		fmt.Fprintf(out, "  Removed          %s\n", strings.Join(report.Removed, ", "))
	}
	if len(report.Unchanged) != 0 {
		fmt.Fprintf(out, "  Unchanged        %s\n", strings.Join(report.Unchanged, ", "))
	}
	if len(report.EnabledSkills) == 0 {
		fmt.Fprintln(out, "  Enabled skills   none")
	} else {
		fmt.Fprintf(out, "  Enabled skills   %s\n", strings.Join(report.EnabledSkills, ", "))
	}
	if len(report.MissingSkills) != 0 {
		fmt.Fprintf(out, "  Missing skills   %s\n", strings.Join(report.MissingSkills, ", "))
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
	return nil
}

func normalizeEnabledSkillNames(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = appendUniqueFold(out, value)
	}
	return out
}

func appendUniqueFold(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || containsFold(values, value) {
		return values
	}
	return append(values, value)
}

func removeEnabledSkillName(values []string, name string) ([]string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return append([]string(nil), values...), false
	}
	out := make([]string, 0, len(values))
	removed := false
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), name) {
			removed = true
			continue
		}
		out = appendUniqueFold(out, value)
	}
	return out, removed
}

func activeSkillNames(all []skills.Skill) []string {
	names := make([]string, 0, len(all))
	for _, skill := range all {
		if skill.Active {
			names = appendUniqueFold(names, skill.Name)
		}
	}
	sort.Strings(names)
	return names
}

func resolveEnabledSkillNames(enabled []string, available []string) ([]string, []string) {
	var resolved []string
	var missing []string
	for _, name := range enabled {
		if containsFold(available, name) {
			resolved = appendUniqueFold(resolved, name)
		} else {
			missing = appendUniqueFold(missing, name)
		}
	}
	return resolved, missing
}

func activationSummary(verb string, changed []string, unchanged []string) string {
	if len(changed) == 0 && len(unchanged) == 0 {
		return "No skill changes were requested."
	}
	var parts []string
	if len(changed) != 0 {
		parts = append(parts, fmt.Sprintf("%s %s", verb, strings.Join(changed, ", ")))
	}
	if len(unchanged) != 0 {
		parts = append(parts, fmt.Sprintf("unchanged %s", strings.Join(unchanged, ", ")))
	}
	return strings.Join(parts, "; ") + "."
}

func skillStatusMessage(enabled []string, missing []string) string {
	if len(enabled) == 0 {
		return "No skills are enabled."
	}
	if len(missing) != 0 {
		return "Some enabled skills could not be resolved."
	}
	return "All enabled skills resolved."
}

func (a *App) skillAudit(args []string) error {
	format, err := parseSimpleOutputFormat("skills audit", args)
	if err != nil {
		return err
	}
	all, err := a.runtimeSkills()
	if err != nil {
		return err
	}
	roots := a.runtimeSkillSources()
	drifts := skills.MetadataDrifts(all)
	enabled := normalizeEnabledSkillNames(a.Config.EnabledSkills)
	available := activeSkillNames(all)
	resolved, missing := resolveEnabledSkillNames(enabled, available)
	activeCount := 0
	for _, skill := range all {
		if skill.Active {
			activeCount++
		}
	}
	report := skillAuditReport{
		Kind:                "skills",
		Action:              "audit",
		Status:              "ok",
		SkillCount:          len(all),
		ActiveSkillCount:    activeCount,
		ShadowedSkillCount:  len(all) - activeCount,
		EnabledSkills:       enabled,
		ResolvedSkills:      resolved,
		MissingSkills:       missing,
		SourceCount:         len(roots),
		Sources:             roots,
		MetadataDriftCount:  len(drifts),
		MetadataDrift:       drifts,
		AvailableSkillCount: len(available),
	}
	if len(drifts) != 0 || len(missing) != 0 {
		report.Status = "degraded"
	}
	report.Message = skillAuditMessage(report)
	return renderSkillAuditReport(a.Out, format, report)
}

func skillAuditMessage(report skillAuditReport) string {
	var issues []string
	if report.MetadataDriftCount != 0 {
		issues = append(issues, fmt.Sprintf("%d metadata drift", report.MetadataDriftCount))
	}
	if len(report.MissingSkills) != 0 {
		issues = append(issues, fmt.Sprintf("%d missing enabled skill", len(report.MissingSkills)))
	}
	if len(issues) == 0 {
		return "Skills audit passed."
	}
	return "Skills audit found " + strings.Join(issues, " and ") + "."
}

func renderSkillAuditReport(out io.Writer, format string, report skillAuditReport) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Skill Audit")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Skills           %d\n", report.SkillCount)
	fmt.Fprintf(out, "  Active           %d\n", report.ActiveSkillCount)
	fmt.Fprintf(out, "  Shadowed         %d\n", report.ShadowedSkillCount)
	fmt.Fprintf(out, "  Sources          %d\n", report.SourceCount)
	if len(report.EnabledSkills) == 0 {
		fmt.Fprintln(out, "  Enabled skills   none")
	} else {
		fmt.Fprintf(out, "  Enabled skills   %s\n", strings.Join(report.EnabledSkills, ", "))
	}
	if len(report.MissingSkills) != 0 {
		fmt.Fprintf(out, "  Missing skills   %s\n", strings.Join(report.MissingSkills, ", "))
	}
	fmt.Fprintf(out, "  Metadata drift   %d\n", report.MetadataDriftCount)
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
	return nil
}

func parseSkillInstallArgs(args []string) (skillInstallRequest, error) {
	req := skillInstallRequest{Format: "text", Target: "user"}
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("skills install output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--project":
			req.Target = "project"
		case arg == "--user":
			req.Target = "user"
		case arg == "--claude":
			req.Target = "claude"
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("skills install target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--name":
			index++
			if index >= len(args) {
				return req, errors.New("skills install name is required")
			}
			req.Name = args[index]
		case strings.HasPrefix(arg, "--name="):
			req.Name = strings.TrimPrefix(arg, "--name=")
		default:
			positionals = append(positionals, arg)
		}
	}
	if req.Format != "text" && req.Format != "json" {
		return req, fmt.Errorf("unknown skills install output format %q", req.Format)
	}
	if len(positionals) == 0 {
		return req, errSkillInstallMissingSource
	}
	if len(positionals) != 1 {
		return req, errors.New("usage: codog skills install [--project|--user|--claude] [--name NAME] SOURCE [--json]")
	}
	req.Source = positionals[0]
	return req, nil
}

func parseSkillUninstallArgs(args []string) (skillUninstallRequest, error) {
	req := skillUninstallRequest{Format: "text"}
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("skills uninstall output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--project":
			req.Target = "project"
		case arg == "--user":
			req.Target = "user"
		case arg == "--claude":
			req.Target = "claude"
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("skills uninstall target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		default:
			positionals = append(positionals, arg)
		}
	}
	if req.Format != "text" && req.Format != "json" {
		return req, fmt.Errorf("unknown skills uninstall output format %q", req.Format)
	}
	if len(positionals) == 0 {
		return req, errSkillUninstallMissingName
	}
	if len(positionals) != 1 {
		return req, errors.New("usage: codog skills uninstall NAME [--project|--user|--claude] [--json]")
	}
	req.Name = positionals[0]
	return req, nil
}

func (a *App) skillTargetRoot(target string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "", "user":
		return filepath.Join(a.Config.ConfigHome, "skills"), "user", nil
	case "project", "workspace":
		return filepath.Join(a.Workspace, ".codog", "skills"), "workspace", nil
	case "claude":
		return filepath.Join(a.Workspace, ".claude", "skills"), "claude", nil
	default:
		return "", "", fmt.Errorf("unknown skills target %q", target)
	}
}

func (a *App) skillUninstallRoots(target string) []string {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "user":
		return []string{filepath.Join(a.Config.ConfigHome, "skills")}
	case "project", "workspace":
		return []string{filepath.Join(a.Workspace, ".codog", "skills")}
	case "claude":
		return []string{filepath.Join(a.Workspace, ".claude", "skills")}
	default:
		return []string{
			filepath.Join(a.Config.ConfigHome, "skills"),
			filepath.Join(a.Workspace, ".codog", "skills"),
			filepath.Join(a.Workspace, ".claude", "skills"),
		}
	}
}

func (a *App) listSkills(args []string) error {
	return a.listSkillsWithAction(args, "list", "")
}

func (a *App) listSkillsWithAction(args []string, action string, query string) error {
	remaining, format, err := stripJSONOnlyOutputFormat("skills", args)
	if err != nil {
		return err
	}
	filter, err := parseListFilterArgs("skills list", remaining, "codog skills list [FILTER] [--json|--output-format text|json]", "unknown_option")
	if err != nil {
		return renderCLIError(a.Out, err, format)
	}
	if strings.TrimSpace(query) != "" {
		filter = strings.TrimSpace(query)
	}
	all, err := a.runtimeSkills()
	if err != nil {
		return err
	}
	if filter != "" {
		all = filterSkills(all, filter)
	}
	drifts := skills.MetadataDrifts(all)
	if format == "json" {
		status := "ok"
		if len(drifts) > 0 {
			status = "degraded"
		}
		activeCount := 0
		for _, skill := range all {
			if skill.Active {
				activeCount++
			}
		}
		data, _ := json.MarshalIndent(map[string]any{
			"kind":                 "skills",
			"action":               action,
			"status":               status,
			"query":                strings.TrimSpace(filter),
			"count":                len(all),
			"metadata_drift_count": len(drifts),
			"summary": map[string]any{
				"total":    len(all),
				"active":   activeCount,
				"shadowed": len(all) - activeCount,
			},
			"skills":         all,
			"metadata_drift": drifts,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if len(all) == 0 {
		fmt.Fprintln(a.Out, "No skills found.")
		return nil
	}
	for _, skill := range all {
		enabled := ""
		if containsFold(a.Config.EnabledSkills, skill.Name) {
			enabled = "enabled"
		}
		status := "active"
		if !skill.Active {
			status = "shadowed"
			if skill.ShadowedBy != "" {
				status += " by " + skill.ShadowedBy
			}
		}
		drift := ""
		if skill.NameDrift {
			drift = "name drift: " + skill.DisplayName
		}
		fmt.Fprintf(a.Out, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", skill.Name, skill.Source, skillOriginText(skill.Origin), status, enabled, drift, skill.Path)
	}
	return nil
}

func (a *App) skillSources(args []string) error {
	format, err := parseSimpleOutputFormat("skills sources", args)
	if err != nil {
		return err
	}
	roots := a.runtimeSkillSources()
	if format == "json" {
		data, _ := json.MarshalIndent(map[string]any{
			"kind":       "skills",
			"action":     "sources",
			"status":     "ok",
			"root_count": len(roots),
			"roots":      roots,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, "Skill Sources")
	for _, root := range roots {
		state := "missing"
		if root.Exists {
			state = "present"
		}
		fmt.Fprintf(a.Out, "  %-11s %-22s %-8s %s\n", root.Source, skillOriginText(root.Origin), state, root.Path)
	}
	return nil
}

func skillOriginText(origin *skills.Origin) string {
	if origin == nil || strings.TrimSpace(origin.ID) == "" {
		return "skills_dir"
	}
	if strings.TrimSpace(origin.DetailLabel) == "" {
		return origin.ID
	}
	return origin.ID + " (" + origin.DetailLabel + ")"
}

func filterSkills(all []skills.Skill, filter string) []skills.Skill {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return all
	}
	out := make([]skills.Skill, 0, len(all))
	for _, skill := range all {
		if strings.Contains(strings.ToLower(skill.Name), filter) ||
			strings.Contains(strings.ToLower(skill.DisplayName), filter) ||
			strings.Contains(strings.ToLower(skill.Description), filter) {
			out = append(out, skill)
		}
	}
	return out
}

func parseListFilterArgs(command string, args []string, usage string, errorKind string) (string, error) {
	if option := firstFlagShapedArg(args); option != "" {
		return "", unknownOptionError{Kind: errorKind, Command: command, Option: option, Usage: usage}
	}
	return strings.TrimSpace(strings.Join(args, " ")), nil
}

func firstFlagShapedArg(args []string) string {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

func (a *App) Commands(args []string) error {
	action := "list"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = normalizeCommandsAction(args[0])
		rest = args[1:]
	}
	switch action {
	case "list":
		return a.commandsList(rest)
	case "search":
		return a.commandsSearch(rest)
	case "audit":
		return a.commandAudit(rest)
	case "sources":
		return a.commandSources(rest)
	case "show":
		return a.commandsShow(rest)
	case "run":
		return a.commandsRun(rest)
	case "install":
		return a.commandsInstall(rest)
	case "uninstall":
		return a.commandsUninstall(rest)
	default:
		_, format, err := stripJSONOnlyOutputFormat("commands", rest)
		if err != nil {
			return renderCLIError(a.Out, err, requestedOutputFormat(append([]string{"commands", action}, rest...)))
		}
		return renderUnsupportedCommandsAction(a.Out, action, format)
	}
}

func (a *App) commandsList(args []string) error {
	format, err := parseSimpleOutputFormat("commands", args)
	if err != nil {
		return err
	}
	return a.renderCommandsList(format)
}

func (a *App) commandsSearch(args []string) error {
	format, remaining, err := parseTemplateOutputArgs("commands search", args)
	if err != nil {
		return err
	}
	query := strings.TrimSpace(strings.Join(remaining, " "))
	if query == "" {
		return renderMissingActionArgument(a.Out, "commands", "search", "query", "commands search requires a query", "Usage: codog commands search QUERY [--json|--output-format text|json].", format)
	}
	return a.renderCommandsListWithAction(format, "search", query)
}

func (a *App) commandsShow(args []string) error {
	format, remaining, err := parseTemplateOutputArgs("commands show", args)
	if err != nil {
		return err
	}
	if len(remaining) == 0 {
		return a.renderCommandsList(format)
	}
	if len(remaining) > 1 {
		return renderCLIError(a.Out, unexpectedExtraArgsError{
			Command: "commands show",
			Args:    append([]string(nil), remaining[1:]...),
			Usage:   "codog commands show [NAME] [--json|--output-format text|json]",
		}, format)
	}
	command, err := a.findRuntimeCustomCommand(remaining[0])
	if err != nil {
		return err
	}
	return renderCustomCommand(a.Out, command, format)
}

func renderCustomCommand(out io.Writer, command customcommands.Command, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(command, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprint(out, command.Body)
	if !strings.HasSuffix(command.Body, "\n") {
		fmt.Fprintln(out)
	}
	return nil
}

func (a *App) commandsRun(args []string) error {
	format, remaining, err := parseTemplateOutputArgs("commands run", args)
	if err != nil {
		return err
	}
	if len(remaining) < 1 {
		return renderMissingActionArgument(a.Out, "commands", "run", "command_name", "commands run requires a command name", "Usage: codog commands run NAME [ARGS...] [--json|--output-format text|json]. Run `codog commands list` to see available commands.", format)
	}
	command, err := a.findRuntimeCustomCommand(remaining[0])
	if err != nil {
		return err
	}
	rendered := customcommands.Render(command, strings.Join(remaining[1:], " "))
	if format == "json" {
		data, _ := json.MarshalIndent(map[string]any{"kind": "command_run", "command": rendered}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprint(a.Out, rendered.Rendered)
	if !strings.HasSuffix(rendered.Rendered, "\n") {
		fmt.Fprintln(a.Out)
	}
	return nil
}

func (a *App) commandsInstall(args []string) error {
	req, err := parseCommandInstallArgs(args)
	if err != nil {
		if errors.Is(err, errCommandInstallMissingSource) {
			return renderCommandInstallMissingSource(a.Out, req.Format)
		}
		return err
	}
	targetRoot, targetLabel, err := a.commandTargetRoot(req.Target)
	if err != nil {
		return err
	}
	report, err := customcommands.Install(req.Source, targetRoot, req.Name, targetLabel)
	if err != nil {
		return renderCommandLookupError(a.Out, "install", req.Source, err, req.Format)
	}
	return renderCommandInstallReport(a.Out, report, req.Format)
}

func renderCommandInstallReport(out io.Writer, report customcommands.InstallReport, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Command Installed")
	fmt.Fprintf(out, "  Name             %s\n", report.Name)
	fmt.Fprintf(out, "  Target           %s\n", report.Target)
	fmt.Fprintf(out, "  Path             %s\n", report.Path)
	return nil
}

func (a *App) commandsUninstall(args []string) error {
	req, err := parseCommandUninstallArgs(args)
	if err != nil {
		if errors.Is(err, errCommandUninstallMissingName) {
			return renderMissingActionArgument(a.Out, "commands", "uninstall", "command_name", "commands uninstall requires a command name", "Usage: codog commands uninstall NAME [--project|--user|--claude] [--json|--output-format text|json]. Run `codog commands list` to see installed commands.", req.Format)
		}
		return err
	}
	report, err := customcommands.Uninstall(req.Name, a.commandUninstallRoots(req.Target))
	if err != nil {
		return renderCommandLookupError(a.Out, "uninstall", req.Name, err, req.Format)
	}
	return renderCommandUninstallReport(a.Out, report, req.Format)
}

func renderCommandUninstallReport(out io.Writer, report customcommands.UninstallReport, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Command Uninstalled")
	fmt.Fprintf(out, "  Name             %s\n", report.Name)
	fmt.Fprintf(out, "  Path             %s\n", report.Path)
	return nil
}

func normalizeCommandsAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "list", "ls":
		return "list"
	case "search", "find", "query", "lookup":
		return "search"
	case "audit", "doctor", "check", "validate":
		return "audit"
	case "source", "sources", "root", "roots":
		return "sources"
	case "show", "info", "describe", "get", "view", "cat":
		return "show"
	case "run", "render", "exec", "execute", "call", "invoke":
		return "run"
	case "install", "add":
		return "install"
	case "uninstall", "remove", "delete", "rm", "del":
		return "uninstall"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

type commandInstallRequest struct {
	Format string
	Target string
	Name   string
	Source string
}

type commandUninstallRequest struct {
	Format string
	Target string
	Name   string
}

var (
	errCommandInstallMissingSource = errors.New("commands install source is required")
	errCommandUninstallMissingName = errors.New("commands uninstall name is required")
)

func parseCommandInstallArgs(args []string) (commandInstallRequest, error) {
	req := commandInstallRequest{Format: "text", Target: "user"}
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("commands install output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--project" || arg == "--workspace":
			req.Target = "project"
		case arg == "--user":
			req.Target = "user"
		case arg == "--claude":
			req.Target = "claude"
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("commands install target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--name":
			index++
			if index >= len(args) {
				return req, errors.New("commands install name is required")
			}
			req.Name = args[index]
		case strings.HasPrefix(arg, "--name="):
			req.Name = strings.TrimPrefix(arg, "--name=")
		default:
			positionals = append(positionals, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "commands install"); err != nil {
		return req, err
	}
	if len(positionals) == 0 {
		return req, errCommandInstallMissingSource
	}
	if len(positionals) != 1 {
		return req, errors.New("usage: codog commands install [--project|--user|--claude] [--name NAME] SOURCE [--json]")
	}
	req.Source = positionals[0]
	return req, nil
}
