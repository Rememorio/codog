package bashvalidation

import (
	"encoding/json"
	"strings"
)

type Severity string

const (
	SeverityAllow   Severity = "allow"
	SeverityConfirm Severity = "confirm"
	SeverityBlock   Severity = "block"
)

type Intent string

const (
	IntentReadOnly          Intent = "read-only"
	IntentWrite             Intent = "write"
	IntentDestructive       Intent = "destructive"
	IntentNetwork           Intent = "network"
	IntentProcessManagement Intent = "process-management"
	IntentPackageManagement Intent = "package-management"
	IntentSystemAdmin       Intent = "system-admin"
	IntentUnknown           Intent = "unknown"
)

type Result struct {
	Severity Severity `json:"severity"`
	Intent   Intent   `json:"intent"`
	Reason   string   `json:"reason,omitempty"`
}

func CommandFromInput(input []byte) string {
	var payload struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(input, &payload)
	return strings.TrimSpace(payload.Command)
}

func Validate(command string, mode string, workspace string) Result {
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
	if reason := sedReason(command, mode); reason != "" {
		return Result{Severity: SeverityBlock, Intent: IntentWrite, Reason: reason}
	}
	if mode == "read-only" {
		if intent == IntentReadOnly && !hasWriteRedirection(command) {
			return Result{Severity: SeverityAllow, Intent: intent}
		}
		return Result{Severity: SeverityBlock, Intent: intent, Reason: "bash command is not read-only"}
	}
	if reason := pathReason(command, intent, workspace); reason != "" {
		return Result{Severity: SeverityConfirm, Intent: intent, Reason: reason}
	}
	return Result{Severity: SeverityAllow, Intent: intent}
}

func Classify(command string) Intent {
	first := firstCommand(command)
	switch {
	case readOnlyCommands[first]:
		if hasWriteRedirection(command) {
			return IntentWrite
		}
		return IntentReadOnly
	case writeCommands[first]:
		return IntentWrite
	case destructiveCommands[first]:
		return IntentDestructive
	case networkCommands[first]:
		return IntentNetwork
	case processCommands[first]:
		return IntentProcessManagement
	case packageCommands[first]:
		return IntentPackageManagement
	case systemCommands[first]:
		return IntentSystemAdmin
	default:
		if hasWriteRedirection(command) {
			return IntentWrite
		}
		return IntentUnknown
	}
}

func firstCommand(command string) string {
	for _, sep := range []string{"&&", "||", ";", "|"} {
		if before, _, ok := strings.Cut(command, sep); ok {
			command = before
		}
	}
	fields := strings.Fields(command)
	for len(fields) > 0 {
		first := strings.Trim(fields[0], "'\"")
		switch {
		case first == "sudo" || first == "command" || first == "builtin" || first == "exec":
			fields = fields[1:]
		case first == "env":
			fields = fields[1:]
		case strings.Contains(first, "=") && !strings.HasPrefix(first, "-"):
			fields = fields[1:]
		default:
			return first
		}
	}
	return ""
}

func destructiveReason(command string) string {
	normalized := strings.Join(strings.Fields(command), " ")
	patterns := map[string]string{
		"rm -rf /":      "recursive forced deletion at root",
		"rm -rf ~":      "recursive forced deletion of home directory",
		"rm -rf *":      "recursive forced deletion of all files in current directory",
		"rm -rf .":      "recursive forced deletion of current directory",
		"chmod -R 777":  "recursively setting world-writable permissions",
		"chmod -R 000":  "recursively removing all permissions",
		":(){ :|:& };:": "fork bomb",
		"mkfs":          "filesystem creation can destroy data",
		"dd if=":        "direct disk write can overwrite devices",
		"> /dev/sd":     "writing to a raw disk device",
		">/dev/sd":      "writing to a raw disk device",
		"curl ":         "",
		"wget ":         "",
		" | sh":         "piping network content to a shell",
		" | bash":       "piping network content to a shell",
		"|sh":           "piping network content to a shell",
		"|bash":         "piping network content to a shell",
	}
	for pattern, reason := range patterns {
		if reason != "" && strings.Contains(normalized, pattern) {
			return reason
		}
	}
	first := firstCommand(command)
	if first == "shred" || first == "wipefs" {
		return "inherently destructive command"
	}
	if strings.Contains(normalized, "rm ") && hasRecursiveForceFlags(normalized) {
		return "recursive forced deletion detected"
	}
	if (strings.Contains(normalized, "curl ") || strings.Contains(normalized, "wget ")) &&
		(strings.Contains(normalized, "| sh") || strings.Contains(normalized, "| bash") || strings.Contains(normalized, "|sh") || strings.Contains(normalized, "|bash")) {
		return "piping network content to a shell"
	}
	return ""
}

func sedReason(command string, mode string) string {
	if firstCommand(command) != "sed" {
		return ""
	}
	if mode == "read-only" && (strings.Contains(command, " -i") || strings.Contains(command, " --in-place")) {
		return "sed in-place editing is not allowed in read-only mode"
	}
	return ""
}

func pathReason(command string, intent Intent, workspace string) string {
	if intent == IntentReadOnly {
		return ""
	}
	if strings.Contains(command, "../") {
		if workspace == "" || !strings.Contains(command, workspace) {
			return "command contains directory traversal pattern"
		}
	}
	if strings.Contains(command, "~/") || strings.Contains(command, "$HOME") {
		return "command references the home directory"
	}
	for _, path := range []string{"/etc/", "/usr/", "/var/", "/boot/", "/sys/", "/proc/", "/dev/", "/sbin/", "/lib/", "/opt/"} {
		if strings.Contains(command, path) {
			return "command appears to target system paths"
		}
	}
	return ""
}

func hasWriteRedirection(command string) bool {
	for _, token := range []string{">", ">>", ">&", "2>", "1>"} {
		if strings.Contains(command, token) {
			return true
		}
	}
	return false
}

func hasRecursiveForceFlags(command string) bool {
	fields := strings.Fields(command)
	for _, field := range fields {
		if strings.HasPrefix(field, "-") && strings.Contains(field, "r") && strings.Contains(field, "f") {
			return true
		}
	}
	return false
}

var readOnlyCommands = map[string]bool{
	"awk": true, "bc": true, "cal": true, "cat": true, "date": true, "df": true,
	"du": true, "echo": true, "env": true, "expr": true, "false": true, "fgrep": true,
	"file": true, "find": true, "free": true, "grep": true, "groups": true, "head": true,
	"hostname": true, "id": true, "less": true, "ls": true, "man": true, "more": true,
	"printenv": true, "printf": true, "pwd": true, "rg": true, "sed": true, "sort": true,
	"stat": true, "tail": true, "test": true, "true": true, "uname": true, "uniq": true,
	"uptime": true, "wc": true, "what": true, "whatis": true, "whereis": true, "which": true,
}

var writeCommands = map[string]bool{
	"chgrp": true, "chmod": true, "chown": true, "cp": true, "dd": true, "install": true,
	"ln": true, "mkdir": true, "mkfifo": true, "mknod": true, "mv": true, "rm": true,
	"rmdir": true, "tee": true, "touch": true, "truncate": true,
}

var destructiveCommands = map[string]bool{"shred": true, "wipefs": true, "mkfs": true}

var networkCommands = map[string]bool{
	"curl": true, "ftp": true, "rsync": true, "scp": true, "sftp": true, "ssh": true, "wget": true,
}

var processCommands = map[string]bool{"kill": true, "killall": true, "pkill": true}

var packageCommands = map[string]bool{
	"apt": true, "apt-get": true, "brew": true, "bun": true, "cargo": true, "dnf": true,
	"gem": true, "go": true, "npm": true, "pacman": true, "pip": true, "pip3": true,
	"pnpm": true, "rustup": true, "yarn": true, "yum": true,
}

var systemCommands = map[string]bool{
	"at": true, "chroot": true, "crontab": true, "docker": true, "groupadd": true,
	"groupdel": true, "halt": true, "mount": true, "poweroff": true, "reboot": true,
	"service": true, "shutdown": true, "sudo": true, "systemctl": true, "umount": true,
	"useradd": true, "userdel": true, "usermod": true,
}
