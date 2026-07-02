// Package powershellvalidation classifies PowerShell commands for tool
// permission decisions.
package powershellvalidation

import (
	"encoding/json"
	"strings"

	"github.com/Rememorio/codog/internal/pathscope"
)

// Severity is the permission outcome for a PowerShell command.
type Severity string

const (
	// SeverityAllow allows the command without an extra prompt.
	SeverityAllow Severity = "allow"
	// SeverityConfirm requires explicit confirmation before execution.
	SeverityConfirm Severity = "confirm"
	// SeverityBlock blocks the command.
	SeverityBlock Severity = "block"
)

// Intent describes the host impact of a PowerShell command.
type Intent string

const (
	// IntentReadOnly describes inspection-only commands.
	IntentReadOnly Intent = "read-only"
	// IntentWrite describes commands that write workspace state.
	IntentWrite Intent = "write"
	// IntentDestructive describes commands that can delete or overwrite data.
	IntentDestructive Intent = "destructive"
	// IntentNetwork describes commands that access the network.
	IntentNetwork Intent = "network"
	// IntentProcessManagement describes process-control commands.
	IntentProcessManagement Intent = "process-management"
	// IntentSystemAdmin describes system-level administration commands.
	IntentSystemAdmin Intent = "system-admin"
	// IntentUnknown describes unrecognized commands.
	IntentUnknown Intent = "unknown"
)

// Result reports a PowerShell validation decision.
type Result struct {
	Severity Severity `json:"severity"`
	Intent   Intent   `json:"intent"`
	Reason   string   `json:"reason,omitempty"`
}

// CommandFromInput extracts the command field from a PowerShell tool payload.
func CommandFromInput(input []byte) string {
	var payload struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(input, &payload)
	return strings.TrimSpace(payload.Command)
}

// Validate classifies a PowerShell command for the active permission mode.
func Validate(command string, mode string, workspace string, additionalDirs []string) Result {
	command = strings.TrimSpace(command)
	if command == "" {
		return Result{Severity: SeverityBlock, Intent: IntentUnknown, Reason: "command is required"}
	}
	intent := Classify(command)
	if reason := destructiveReason(command); reason != "" {
		intent = IntentDestructive
		if mode == "read-only" {
			return Result{Severity: SeverityBlock, Intent: intent, Reason: reason}
		}
		return Result{Severity: SeverityConfirm, Intent: intent, Reason: reason}
	}
	if mode == "read-only" {
		if decision := pathscope.ValidatePayloadScope(workspace, additionalDirs, command, workspace); !decision.Allowed {
			return Result{Severity: SeverityBlock, Intent: intent, Reason: decision.Reason}
		}
		if intent == IntentReadOnly && !hasPowerShellRedirection(command) {
			return Result{Severity: SeverityAllow, Intent: intent}
		}
		return Result{Severity: SeverityBlock, Intent: intent, Reason: "PowerShell command is not read-only"}
	}
	if intent == IntentDestructive || intent == IntentSystemAdmin {
		return Result{Severity: SeverityConfirm, Intent: intent, Reason: "destructive or system-level command detected"}
	}
	return Result{Severity: SeverityAllow, Intent: intent}
}

// Classify returns the highest-risk intent found in a PowerShell command.
func Classify(command string) Intent {
	intent := IntentReadOnly
	seen := false
	for _, segment := range commandSegments(command) {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		seen = true
		intent = higherRiskIntent(intent, classifySingle(segment))
		if intent == IntentDestructive || intent == IntentSystemAdmin {
			return intent
		}
	}
	if !seen {
		return IntentUnknown
	}
	if hasPowerShellRedirection(command) {
		return higherRiskIntent(intent, IntentWrite)
	}
	return intent
}

func classifySingle(command string) Intent {
	first := normalizeCommandName(firstCommand(command))
	if first == "" {
		return IntentUnknown
	}
	if readOnlyCommands[first] {
		if hasPowerShellRedirection(command) {
			return IntentWrite
		}
		return IntentReadOnly
	}
	if writeCommands[first] {
		return IntentWrite
	}
	if destructiveCommands[first] {
		return IntentDestructive
	}
	if networkCommands[first] {
		return IntentNetwork
	}
	if processCommands[first] {
		return IntentProcessManagement
	}
	if systemCommands[first] {
		return IntentSystemAdmin
	}
	return IntentUnknown
}

func commandSegments(command string) []string {
	segments := []string{command}
	for _, sep := range []string{"&&", "||", ";", "|"} {
		next := make([]string, 0, len(segments))
		for _, segment := range segments {
			next = append(next, strings.Split(segment, sep)...)
		}
		segments = next
	}
	return segments
}

func firstCommand(command string) string {
	fields := strings.Fields(command)
	for len(fields) > 0 {
		first := strings.Trim(fields[0], "'\"`")
		if strings.Contains(first, "=") && !strings.HasPrefix(first, "-") {
			fields = fields[1:]
			continue
		}
		if strings.EqualFold(first, "powershell") || strings.EqualFold(first, "pwsh") || strings.EqualFold(first, "command") {
			fields = fields[1:]
			continue
		}
		if strings.HasPrefix(first, "-") {
			fields = fields[1:]
			continue
		}
		return first
	}
	return ""
}

func normalizeCommandName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.TrimSuffix(name, ".exe")
}

func destructiveReason(command string) string {
	normalized := " " + strings.ToLower(strings.Join(strings.Fields(command), " ")) + " "
	switch {
	case strings.Contains(normalized, " remove-item ") && (strings.Contains(normalized, " -recurse ") || strings.Contains(normalized, " -r ")) && (strings.Contains(normalized, " -force ") || strings.Contains(normalized, " -f ")):
		return "recursive forced deletion detected"
	case strings.Contains(normalized, " rm ") && (strings.Contains(normalized, " -recurse ") || strings.Contains(normalized, " -r ")) && (strings.Contains(normalized, " -force ") || strings.Contains(normalized, " -f ")):
		return "recursive forced deletion detected"
	case strings.Contains(normalized, " invoke-expression ") || strings.Contains(normalized, " iex "):
		if strings.Contains(normalized, "invoke-webrequest") || strings.Contains(normalized, "iwr ") || strings.Contains(normalized, "irm ") || strings.Contains(normalized, "invoke-restmethod") {
			return "piping network content to PowerShell execution"
		}
	case strings.Contains(normalized, " format-volume "):
		return "volume formatting can destroy data"
	case strings.Contains(normalized, " clear-disk "):
		return "disk clearing can destroy data"
	}
	return ""
}

func hasPowerShellRedirection(command string) bool {
	for _, token := range []string{">", ">>", "*>", "2>", "3>", "4>", "5>", "6>", "out-file", "set-content", "add-content"} {
		if strings.Contains(strings.ToLower(command), token) {
			return true
		}
	}
	return false
}

func higherRiskIntent(left Intent, right Intent) Intent {
	if intentRank(right) > intentRank(left) {
		return right
	}
	return left
}

func intentRank(intent Intent) int {
	switch intent {
	case IntentReadOnly:
		return 0
	case IntentUnknown:
		return 1
	case IntentNetwork:
		return 2
	case IntentWrite:
		return 3
	case IntentProcessManagement:
		return 4
	case IntentDestructive:
		return 5
	case IntentSystemAdmin:
		return 6
	default:
		return 1
	}
}

var readOnlyCommands = map[string]bool{
	"cat": true, "cd": true, "dir": true, "echo": true, "gc": true, "gci": true,
	"get-childitem": true, "get-command": true, "get-content": true, "get-date": true,
	"get-help": true, "get-item": true, "get-location": true, "get-member": true,
	"get-process": true, "get-service": true, "ls": true, "measure-object": true,
	"pwd": true, "select-object": true, "select-string": true, "sort-object": true,
	"type": true, "where-object": true,
}

var writeCommands = map[string]bool{
	"add-content": true, "clear-content": true, "copy": true, "copy-item": true,
	"mkdir": true, "move": true, "move-item": true, "new-item": true,
	"ni": true, "out-file": true, "ren": true, "rename-item": true,
	"set-content": true, "set-item": true,
}

var destructiveCommands = map[string]bool{
	"clear-disk": true, "del": true, "erase": true, "format-volume": true,
	"rd": true, "remove-item": true, "rm": true, "rmdir": true,
}

var networkCommands = map[string]bool{
	"curl": true, "iwr": true, "irm": true, "invoke-restmethod": true,
	"invoke-webrequest": true, "wget": true,
}

var processCommands = map[string]bool{"kill": true, "stop-process": true}

var systemCommands = map[string]bool{
	"disable-computerrestore": true, "enable-psremoting": true, "new-service": true,
	"restart-computer": true, "set-executionpolicy": true, "start-service": true,
	"stop-service": true,
}
