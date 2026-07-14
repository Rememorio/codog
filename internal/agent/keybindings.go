package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/autofixpr"
	"github.com/Rememorio/codog/internal/bughunt"
	"github.com/Rememorio/codog/internal/commandrun"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/fileinventory"
	"github.com/Rememorio/codog/internal/githubcomments"
	"github.com/Rememorio/codog/internal/githubsetup"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/prompthistory"
	"github.com/Rememorio/codog/internal/prworkflow"
	localreview "github.com/Rememorio/codog/internal/review"
	"github.com/Rememorio/codog/internal/securityreview"
	"github.com/Rememorio/codog/internal/session"
	localstatus "github.com/Rememorio/codog/internal/status"
	"github.com/Rememorio/codog/internal/todos"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/versioninfo"
	"github.com/Rememorio/codog/internal/workerstate"
)

const keybindingsUsage = "codog keybindings [show|path|init|open|edit|validate|resolve CONTEXT KEY] [--path PATH] [--force] [--output-format text|json]"

func parseKeybindingsArgs(args []string) (keybindingsRequest, error) {
	req := keybindingsRequest{Action: "show", Format: "text"}
	actionSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "keybindings", Flag: arg, Usage: keybindingsUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--force":
			req.Force = true
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "keybindings", Flag: arg, Usage: keybindingsUsage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "keybindings", Option: arg, Usage: keybindingsUsage}
		default:
			if actionSet {
				req.Args = append(req.Args, arg)
				continue
			}
			switch strings.ToLower(arg) {
			case "show", "list", "report":
				req.Action = "show"
			case "path", "where":
				req.Action = "path"
			case "init", "create", "template":
				req.Action = "init"
			case "open", "edit":
				req.Action = "open"
			case "validate", "check":
				req.Action = "validate"
			case "resolve", "match":
				req.Action = "resolve"
			default:
				return req, unexpectedExtraArgsError{Command: "keybindings", Args: []string{arg}, Usage: keybindingsUsage}
			}
			actionSet = true
		}
	}
	normalizedFormat, err := normalizeOutputFormat("keybindings", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	switch req.Action {
	case "resolve":
		if len(req.Args) < 2 {
			if len(req.Args) == 0 {
				return req, requiredArgumentError{Command: "keybindings resolve", Argument: "CONTEXT", Usage: keybindingsUsage}
			}
			return req, requiredArgumentError{Command: "keybindings resolve", Argument: "KEY", Usage: keybindingsUsage}
		}
		req.Context = req.Args[0]
		req.Key = strings.Join(req.Args[1:], " ")
	case "show", "path", "init", "open", "validate":
		if len(req.Args) != 0 {
			return req, unexpectedExtraArgsError{Command: "keybindings " + req.Action, Args: req.Args, Usage: keybindingsUsage}
		}
	}
	return req, nil
}

func (a *App) openKeybindings() (keybindingsFileReport, error) {
	report, err := a.initKeybindings(false)
	if err != nil {
		return report, err
	}
	report.Action = "open"
	editor, err := openPathInEditor(report.Path)
	report.Editor = editor
	if err != nil {
		report.Status = "open_failed"
		report.EditorError = err.Error()
		return report, nil
	}
	report.Opened = true
	if report.Created {
		report.Status = "created_opened"
	} else {
		report.Status = "opened"
	}
	report.Exists = true
	return report, nil
}

func openPathInEditor(path string) (string, error) {
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		return "", errors.New("no editor configured; set VISUAL or EDITOR")
	}
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		return "", errors.New("no editor configured; set VISUAL or EDITOR")
	}
	cmd := exec.Command(fields[0], append(fields[1:], path)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return editor, err
	}
	return editor, nil
}

func (a *App) initKeybindings(force bool) (keybindingsFileReport, error) {
	path, err := a.keybindingsPath()
	if err != nil {
		return keybindingsFileReport{}, err
	}
	alreadyExists := fileExists(path)
	report := keybindingsFileReport{
		Kind:   "keybindings",
		Action: "init",
		Status: "exists",
		Path:   path,
		Exists: alreadyExists,
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return report, err
	}
	data := defaultKeybindingsTemplate()
	if force {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return report, err
		}
		report.Status = "written"
		report.Created = !alreadyExists
		report.Exists = true
		return report, nil
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return report, nil
		}
		return report, err
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(data); err != nil {
		return report, err
	}
	report.Status = "created"
	report.Created = true
	report.Exists = true
	return report, nil
}

func (a *App) keybindingsPath() (string, error) {
	if strings.TrimSpace(a.Config.ConfigHome) == "" {
		return "", errors.New("config home is unavailable")
	}
	return filepath.Join(a.Config.ConfigHome, "keybindings.json"), nil
}

func (a *App) validateKeybindings(path string) keybindingValidationReport {
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = a.keybindingsPath()
		if err != nil {
			return keybindingValidationReport{
				Kind:   "keybindings",
				Action: "validate",
				Status: "invalid",
				Errors: []string{err.Error()},
			}
		}
	} else {
		path = a.resolveOutputPath(path)
	}
	report := keybindingValidationReport{
		Kind:   "keybindings",
		Action: "validate",
		Status: "missing",
		Path:   path,
		Exists: fileExists(path),
	}
	if !report.Exists {
		report.Errors = []string{"keybindings file does not exist"}
		return report
	}
	data, err := os.ReadFile(path)
	if err != nil {
		report.Status = "invalid"
		report.Errors = []string{err.Error()}
		return report
	}
	sections, validationErrors := parseKeybindingsFile(data)
	report.Sections = sections
	report.ContextCount = len(sections)
	for _, section := range sections {
		report.BindingCount += len(section.Entries)
	}
	if len(validationErrors) != 0 {
		report.Status = "invalid"
		report.Errors = validationErrors
		return report
	}
	report.Valid = true
	report.Status = "ok"
	return report
}

func parseKeybindingsFile(data []byte) ([]keybindingSection, []string) {
	type bindingBlock struct {
		Context  string            `json:"context"`
		Bindings map[string]string `json:"bindings"`
		Disabled bool              `json:"disabled,omitempty"`
	}
	var raw struct {
		Bindings []bindingBlock `json:"bindings"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, []string{err.Error()}
	}
	var validationErrors []string
	if len(raw.Bindings) == 0 {
		validationErrors = append(validationErrors, "bindings must contain at least one context")
	}
	seen := map[string]bool{}
	sections := make([]keybindingSection, 0, len(raw.Bindings))
	for index, block := range raw.Bindings {
		contextName := strings.TrimSpace(block.Context)
		if contextName == "" {
			validationErrors = append(validationErrors, fmt.Sprintf("bindings[%d].context is required", index))
		}
		if len(block.Bindings) == 0 {
			validationErrors = append(validationErrors, fmt.Sprintf("bindings[%d].bindings must contain at least one key", index))
		}
		entries := make([]keybindingEntry, 0, len(block.Bindings))
		keys := make([]string, 0, len(block.Bindings))
		for key := range block.Bindings {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			action := block.Bindings[key]
			trimmedKey := strings.TrimSpace(key)
			trimmedAction := strings.TrimSpace(action)
			if trimmedKey == "" {
				validationErrors = append(validationErrors, fmt.Sprintf("bindings[%d] contains an empty key", index))
				continue
			}
			if trimmedAction == "" {
				validationErrors = append(validationErrors, fmt.Sprintf("bindings[%d].bindings[%q] action is required", index, trimmedKey))
				continue
			}
			normalizedContext := normalizeKeybindingContext(contextName)
			normalizedKey := normalizeShortcut(trimmedKey)
			if normalizedKey == "" {
				validationErrors = append(validationErrors, fmt.Sprintf("bindings[%d].bindings[%q] could not be parsed", index, trimmedKey))
				continue
			}
			duplicateKey := normalizedContext + "\x00" + normalizedKey
			if seen[duplicateKey] {
				validationErrors = append(validationErrors, fmt.Sprintf("duplicate binding %q in context %q", trimmedKey, contextName))
				continue
			}
			seen[duplicateKey] = true
			entries = append(entries, keybindingEntry{Key: trimmedKey, NormalizedKey: normalizedKey, Action: trimmedAction})
		}
		sections = append(sections, keybindingSection{Name: contextName, Entries: entries, Disabled: block.Disabled})
	}
	return sections, validationErrors
}

type effectiveKeybinding struct {
	Context  string
	Entry    keybindingEntry
	Source   string
	Section  string
	Disabled bool
}

func (a *App) resolveKeybinding(contextName string, key string, path string) keybindingResolveReport {
	normalizedContext := normalizeKeybindingContext(contextName)
	normalizedKey := normalizeShortcut(key)
	report := keybindingResolveReport{
		Kind:          "keybindings",
		Action:        "resolve",
		Status:        "missing",
		Context:       normalizedContext,
		Key:           strings.TrimSpace(key),
		NormalizedKey: normalizedKey,
	}
	if normalizedContext == "" || normalizedKey == "" {
		report.Status = "invalid"
		report.Errors = []string{"context and key are required"}
		return report
	}
	bindings, validationErrors := a.effectiveKeybindings(path)
	if len(validationErrors) != 0 {
		report.Status = "invalid"
		report.Errors = validationErrors
		return report
	}
	binding, ok := bindings[normalizedContext+"\x00"+normalizedKey]
	if !ok {
		return report
	}
	report.Status = "ok"
	report.Found = true
	report.Source = binding.Source
	report.BindingAction = binding.Entry.Action
	report.Section = binding.Section
	report.Disabled = binding.Disabled
	return report
}

func (a *App) effectiveKeybindings(path string) (map[string]effectiveKeybinding, []string) {
	out := map[string]effectiveKeybinding{}
	defaultSections, validationErrors := parseKeybindingsFile(defaultKeybindingsTemplate())
	if len(validationErrors) != 0 {
		return nil, validationErrors
	}
	addEffectiveKeybindings(out, defaultSections, "default", effectiveEditorMode(a.Config.EditorMode))
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = a.keybindingsPath()
		if err != nil {
			return out, nil
		}
	} else {
		path = a.resolveOutputPath(path)
	}
	if path == "" || !fileExists(path) {
		return out, nil
	}
	report := a.validateKeybindings(path)
	if !report.Valid {
		return nil, report.Errors
	}
	addEffectiveKeybindings(out, report.Sections, "user", effectiveEditorMode(a.Config.EditorMode))
	return out, nil
}

func addEffectiveKeybindings(out map[string]effectiveKeybinding, sections []keybindingSection, source string, editorMode string) {
	for _, section := range sections {
		contextName := normalizeKeybindingContext(section.Name)
		if contextName == "" {
			continue
		}
		disabled := section.Disabled || (contextName == "repl-vim" && !editorModeIsVim(editorMode))
		for _, entry := range section.Entries {
			normalizedKey := entry.NormalizedKey
			if normalizedKey == "" {
				normalizedKey = normalizeShortcut(entry.Key)
			}
			if normalizedKey == "" {
				continue
			}
			entry.NormalizedKey = normalizedKey
			out[contextName+"\x00"+normalizedKey] = effectiveKeybinding{
				Context:  contextName,
				Entry:    entry,
				Source:   source,
				Section:  section.Name,
				Disabled: disabled,
			}
		}
	}
}

func normalizeKeybindingContext(contextName string) string {
	contextName = strings.ToLower(strings.TrimSpace(contextName))
	contextName = strings.ReplaceAll(contextName, "_", "-")
	contextName = strings.Join(strings.Fields(contextName), "-")
	return contextName
}

func normalizeShortcut(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if strings.HasPrefix(key, "/") {
		return strings.ToLower(strings.Join(strings.Fields(key), " "))
	}
	fields := strings.Fields(key)
	if len(fields) > 1 {
		normalized := make([]string, 0, len(fields))
		for _, field := range fields {
			part := normalizeShortcut(field)
			if part == "" {
				return ""
			}
			normalized = append(normalized, part)
		}
		return strings.Join(normalized, " ")
	}
	lower := strings.ToLower(key)
	lower = strings.ReplaceAll(lower, " ", "")
	keySymbol := ""
	if strings.HasSuffix(lower, "+-") {
		keySymbol = "-"
		lower = strings.TrimSuffix(lower, "+-")
	} else if strings.HasSuffix(lower, "--") {
		keySymbol = "-"
		lower = strings.TrimSuffix(lower, "--")
	}
	parts := strings.FieldsFunc(lower, func(r rune) bool {
		return r == '+' || r == '-'
	})
	if len(parts) == 0 {
		return keySymbol
	}
	if len(parts) == 1 && keySymbol == "" {
		return normalizeShortcutToken(parts[0])
	}
	modSeen := map[string]bool{}
	keyPart := keySymbol
	for _, part := range parts {
		token := normalizeShortcutToken(part)
		if token == "" {
			continue
		}
		if isShortcutModifier(token) {
			modSeen[token] = true
			continue
		}
		keyPart = token
	}
	if keyPart == "" {
		return ""
	}
	var normalized []string
	for _, modifier := range []string{"ctrl", "alt", "shift", "meta"} {
		if modSeen[modifier] {
			normalized = append(normalized, modifier)
		}
	}
	normalized = append(normalized, keyPart)
	return strings.Join(normalized, "+")
}

func normalizeShortcutToken(token string) string {
	token = strings.TrimSpace(token)
	switch token {
	case "control", "ctl":
		return "ctrl"
	case "cmd", "command", "super":
		return "meta"
	case "option":
		return "alt"
	case "escape":
		return "esc"
	case "return":
		return "enter"
	case "spacebar":
		return "space"
	default:
		return token
	}
}

func isShortcutModifier(token string) bool {
	switch token {
	case "ctrl", "alt", "shift", "meta":
		return true
	default:
		return false
	}
}

func defaultKeybindingsTemplate() []byte {
	type bindingBlock struct {
		Context  string            `json:"context"`
		Bindings map[string]string `json:"bindings"`
	}
	template := struct {
		Bindings []bindingBlock `json:"bindings"`
	}{
		Bindings: []bindingBlock{
			{
				Context: "repl",
				Bindings: map[string]string{
					"enter":  "submit prompt",
					"tab":    "complete slash command, skill, model, or session",
					"ctrl+r": "reverse search prompt history",
					"ctrl+c": "exit current REPL read",
				},
			},
			{
				Context: "repl-vim",
				Bindings: map[string]string{
					"esc": "enter normal mode",
					"i":   "enter insert mode",
					"h":   "move left",
					"j":   "previous history item",
					"k":   "next history item",
					"l":   "move right",
				},
			},
			{
				Context: "tui",
				Bindings: map[string]string{
					"enter":            "submit prompt",
					"shift+enter":      "insert newline",
					"alt+enter":        "insert newline fallback",
					"ctrl+j":           "insert newline",
					"ctrl+s":           "stash or restore composer",
					"ctrl+g":           "edit composer in $EDITOR",
					"ctrl+x ctrl+e":    "edit composer in $EDITOR",
					"ctrl+x ctrl+k":    "stop running background tasks and agents",
					"ctrl+x ctrl+c":    "compact current session",
					"ctrl+x ctrl+u":    "undo last file change",
					"ctrl+x ctrl+s":    "export current conversation",
					"ctrl+x ctrl+y":    "copy current conversation",
					"ctrl+x backspace": "remove last attachment",
					"ctrl+_":           "undo composer edit",
					"ctrl+shift+-":     "undo composer edit",
					"ctrl+v":           "paste clipboard text or image",
					"ctrl+shift+p":     "quick open files",
					"ctrl+p":           "quick open fallback",
					"ctrl+shift+f":     "search workspace",
					"ctrl+f":           "search workspace fallback",
					"alt+m":            "cycle permission mode fallback",
					"meta+m":           "cycle permission mode fallback",
					"alt+p":            "open model picker",
					"alt+o":            "toggle fast mode",
					"alt+t":            "cycle thinking effort",
					"shift+up":         "open message actions",
					"ctrl+o":           "toggle expanded transcript",
					"ctrl+l":           "clear screen",
					"ctrl+u":           "delete before cursor",
					"ctrl+k":           "delete after cursor",
					"home":             "move to line start",
					"ctrl+a":           "move to line start",
					"end":              "move to line end",
					"ctrl+d":           "exit when composer is empty",
					"ctrl+b":           "run composer prompt in background",
					"ctrl+t":           "toggle tasks",
					"ctrl+shift+t":     "show background task board",
					"tab":              "complete slash command",
					"up":               "edit queued prompts, choose completion, or recall history",
					"esc":              "quit",
					"ctrl+c":           "quit",
				},
			},
			{
				Context: "tui-modal",
				Bindings: map[string]string{
					"j":          "move modal selection down",
					"k":          "move modal selection up",
					"ctrl+n":     "move modal selection down",
					"ctrl+p":     "move modal selection up",
					"home":       "jump modal selection to top",
					"end":        "jump modal selection to bottom",
					"ctrl+up":    "jump modal selection to top",
					"ctrl+down":  "jump modal selection to bottom",
					"meta+up":    "jump modal selection to top",
					"meta+down":  "jump modal selection to bottom",
					"alt+up":     "jump modal selection to top",
					"alt+down":   "jump modal selection to bottom",
					"shift+k":    "jump modal selection to top",
					"shift+j":    "jump modal selection to bottom",
					"left":       "move message target backward",
					"right":      "move message target forward",
					"shift+up":   "move to previous user message",
					"shift+down": "move to next user message",
				},
			},
			{
				Context: "tui-attachments",
				Bindings: map[string]string{
					"right":     "select next attachment",
					"left":      "select previous attachment",
					"backspace": "remove selected attachment",
					"delete":    "remove selected attachment",
					"down":      "close attachment selector",
					"esc":       "close attachment selector",
				},
			},
			{
				Context: "tui-diff",
				Bindings: map[string]string{
					"esc":   "close diff dialog",
					"left":  "previous diff source or back from detail",
					"right": "next diff source",
					"up":    "select previous changed file",
					"down":  "select next changed file",
					"enter": "view selected file diff",
				},
			},
			{
				Context: "slash",
				Bindings: map[string]string{
					"/help":                 "show command help",
					"/keybindings":          "show keybinding report",
					"/keybindings init":     "create keybindings.json template",
					"/keybindings validate": "validate keybindings.json",
					"/vim":                  "toggle vim keybinding preference",
				},
			},
		},
	}
	data, _ := json.MarshalIndent(template, "", "  ")
	return append(data, '\n')
}

func (a *App) keybindingReport() keybindingReport {
	editorMode := effectiveEditorMode(a.Config.EditorMode)
	path, _ := a.keybindingsPath()
	var userBindings *keybindingValidationReport
	if path != "" && fileExists(path) {
		report := a.validateKeybindings(path)
		userBindings = &report
	}
	return keybindingReport{
		Kind:              "keybindings",
		Action:            "show",
		Status:            "ok",
		EditorMode:        editorMode,
		VimMode:           editorModeIsVim(editorMode),
		KeybindingsPath:   path,
		KeybindingsExists: path != "" && fileExists(path),
		UserBindings:      userBindings,
		Sections: []keybindingSection{
			{
				Name: "REPL",
				Entries: []keybindingEntry{
					{Key: "Enter", Action: "submit prompt"},
					{Key: "Tab", Action: "complete slash command, skill, model, or session"},
					{Key: "Ctrl-R", Action: "reverse search prompt history", Description: "available when prompt history is enabled"},
					{Key: "Ctrl-C", Action: "exit current REPL read"},
					{Key: "/exit", Action: "quit the REPL"},
				},
			},
			{
				Name:     "REPL vim",
				Disabled: !editorModeIsVim(editorMode),
				Entries: []keybindingEntry{
					{Key: "Esc", Action: "enter normal mode", Mode: "insert"},
					{Key: "i", Action: "enter insert mode", Mode: "normal"},
					{Key: "h/j/k/l", Action: "move cursor/history", Mode: "normal"},
					{Key: "0/$", Action: "jump to line start/end", Mode: "normal"},
				},
			},
			{
				Name: "TUI",
				Entries: []keybindingEntry{
					{Key: "Enter", Action: "submit prompt"},
					{Key: "Shift-Enter", Action: "insert newline"},
					{Key: "Alt-Enter", Action: "insert newline fallback"},
					{Key: "Ctrl-J", Action: "insert newline"},
					{Key: "Ctrl-S", Action: "stash or restore composer"},
					{Key: "Ctrl-G", Action: "edit composer in $EDITOR"},
					{Key: "Ctrl-X Ctrl-E", Action: "edit composer in $EDITOR"},
					{Key: "Ctrl-X Ctrl-K", Action: "stop running background tasks and agents"},
					{Key: "Ctrl-X Ctrl-C", Action: "compact current session"},
					{Key: "Ctrl-X Ctrl-U", Action: "undo last file change"},
					{Key: "Ctrl-X Ctrl-S", Action: "export current conversation"},
					{Key: "Ctrl-X Ctrl-Y", Action: "copy current conversation"},
					{Key: "Ctrl-X Backspace", Action: "remove last attachment"},
					{Key: "Ctrl-_", Action: "undo composer edit"},
					{Key: "Ctrl-Shift--", Action: "undo composer edit"},
					{Key: "Ctrl-V", Action: "paste clipboard text or image"},
					{Key: "Ctrl-Shift-P", Action: "quick open files"},
					{Key: "Ctrl-P", Action: "quick open fallback"},
					{Key: "Ctrl-Shift-F", Action: "search workspace"},
					{Key: "Ctrl-F", Action: "search workspace fallback"},
					{Key: "Alt-M", Action: "cycle permission mode fallback"},
					{Key: "Meta-M", Action: "cycle permission mode fallback"},
					{Key: "Alt-P", Action: "open model picker"},
					{Key: "Alt-O", Action: "toggle fast mode"},
					{Key: "Alt-T", Action: "cycle thinking effort"},
					{Key: "Shift-Up", Action: "open message actions"},
					{Key: "Ctrl-O", Action: "toggle expanded transcript"},
					{Key: "Ctrl-L", Action: "clear screen"},
					{Key: "Ctrl-U", Action: "delete before cursor"},
					{Key: "Ctrl-K", Action: "delete after cursor"},
					{Key: "Home / Ctrl-A", Action: "move to line start"},
					{Key: "End", Action: "move to line end"},
					{Key: "Ctrl-D", Action: "exit when composer is empty"},
					{Key: "Ctrl-B", Action: "run composer prompt in background"},
					{Key: "Ctrl-T", Action: "toggle tasks"},
					{Key: "Ctrl-Shift-T", Action: "show background task board"},
					{Key: "Tab", Action: "complete slash command"},
					{Key: "Up", Action: "edit queued prompts, choose completion, or recall history"},
					{Key: "Esc", Action: "quit"},
					{Key: "Ctrl-C", Action: "quit"},
				},
			},
			{
				Name: "TUI modal",
				Entries: []keybindingEntry{
					{Key: "J / Down / Ctrl-N", Action: "move selection down", Description: "model picker, message actions, quick open, and search dialogs"},
					{Key: "K / Up / Ctrl-P", Action: "move selection up", Description: "model picker, message actions, quick open, and search dialogs"},
					{Key: "Home / Ctrl-Up / Meta-Up / Alt-Up / Shift-K", Action: "jump selection to top"},
					{Key: "End / Ctrl-Down / Meta-Down / Alt-Down / Shift-J", Action: "jump selection to bottom"},
					{Key: "Left / Right", Action: "move message action target"},
					{Key: "Shift-Up / Shift-Down", Action: "move between user messages in message actions"},
				},
			},
			{
				Name: "TUI attachments",
				Entries: []keybindingEntry{
					{Key: "Right / Left", Action: "select next or previous pending attachment"},
					{Key: "Backspace / Delete", Action: "remove selected pending attachment"},
					{Key: "Down / Esc", Action: "close attachment selector"},
				},
			},
			{
				Name: "TUI diff",
				Entries: []keybindingEntry{
					{Key: "Up / Down", Action: "select previous or next changed file"},
					{Key: "Left / Right", Action: "move between diff sources or return from detail"},
					{Key: "Enter", Action: "view selected file diff"},
					{Key: "Esc", Action: "close diff dialog"},
				},
			},
			{
				Name: "Slash",
				Entries: []keybindingEntry{
					{Key: "/help", Action: "show command help"},
					{Key: "/keybindings", Action: "show this report"},
					{Key: "/keybindings init", Action: "create keybindings.json template"},
					{Key: "/keybindings validate", Action: "validate keybindings.json"},
					{Key: "/vim", Action: "toggle vim keybinding preference"},
					{Key: "/privacy-settings", Action: "change local privacy preferences"},
				},
			},
		},
	}
}

func renderKeybindingsValidation(out io.Writer, report keybindingValidationReport) {
	fmt.Fprintln(out, "Keybindings Validation")
	fmt.Fprintf(out, "  Path             %s\n", emptyAsNone(report.Path))
	fmt.Fprintf(out, "  Exists           %t\n", report.Exists)
	fmt.Fprintf(out, "  Valid            %t\n", report.Valid)
	fmt.Fprintf(out, "  Contexts         %d\n", report.ContextCount)
	fmt.Fprintf(out, "  Bindings         %d\n", report.BindingCount)
	for _, validationError := range report.Errors {
		fmt.Fprintf(out, "  Error            %s\n", validationError)
	}
}

func renderKeybindingResolve(out io.Writer, report keybindingResolveReport) {
	fmt.Fprintln(out, "Keybinding Resolve")
	fmt.Fprintf(out, "  Context          %s\n", report.Context)
	fmt.Fprintf(out, "  Key              %s\n", report.Key)
	fmt.Fprintf(out, "  Normalized key   %s\n", report.NormalizedKey)
	fmt.Fprintf(out, "  Found            %t\n", report.Found)
	if report.Source != "" {
		fmt.Fprintf(out, "  Source           %s\n", report.Source)
	}
	if report.Section != "" {
		fmt.Fprintf(out, "  Section          %s\n", report.Section)
	}
	if report.BindingAction != "" {
		fmt.Fprintf(out, "  Action           %s\n", report.BindingAction)
	}
	if report.Disabled {
		fmt.Fprintln(out, "  Disabled         true")
	}
	for _, validationError := range report.Errors {
		fmt.Fprintf(out, "  Error            %s\n", validationError)
	}
}

func renderKeybindingsFileReport(out io.Writer, report keybindingsFileReport) {
	switch report.Status {
	case "created":
		fmt.Fprintf(out, "Created keybindings template: %s\n", report.Path)
	case "created_opened":
		fmt.Fprintf(out, "Created keybindings template and opened in editor: %s\n", report.Path)
	case "opened":
		fmt.Fprintf(out, "Opened keybindings in editor: %s\n", report.Path)
	case "open_failed":
		prefix := "Keybindings file is ready"
		if report.Created {
			prefix = "Created keybindings template"
		}
		fmt.Fprintf(out, "%s: %s\n", prefix, report.Path)
		fmt.Fprintf(out, "Could not open editor: %s\n", report.EditorError)
	case "written":
		fmt.Fprintf(out, "Wrote keybindings template: %s\n", report.Path)
	default:
		fmt.Fprintf(out, "Keybindings file already exists: %s\n", report.Path)
	}
}

func renderKeybindings(out io.Writer, report keybindingReport) {
	fmt.Fprintln(out, "Keybindings")
	fmt.Fprintf(out, "  Editor mode      %s\n", report.EditorMode)
	fmt.Fprintf(out, "  Vim mode         %t\n", report.VimMode)
	if report.KeybindingsPath != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.KeybindingsPath)
		fmt.Fprintf(out, "  Config exists    %t\n", report.KeybindingsExists)
	}
	if report.UserBindings != nil {
		fmt.Fprintf(out, "  User valid       %t\n", report.UserBindings.Valid)
		fmt.Fprintf(out, "  User contexts    %d\n", report.UserBindings.ContextCount)
		fmt.Fprintf(out, "  User bindings    %d\n", report.UserBindings.BindingCount)
		for _, validationError := range report.UserBindings.Errors {
			fmt.Fprintf(out, "  User error       %s\n", validationError)
		}
	}
	for _, section := range report.Sections {
		fmt.Fprintln(out)
		name := section.Name
		if section.Disabled {
			name += " (disabled)"
		}
		fmt.Fprintf(out, "%s\n", name)
		for _, entry := range section.Entries {
			action := entry.Action
			if entry.Mode != "" {
				action += " [" + entry.Mode + "]"
			}
			if entry.Description != "" {
				action += " - " + entry.Description
			}
			fmt.Fprintf(out, "  %-14s %s\n", entry.Key, action)
		}
	}
}

type todosRequest struct {
	Action   string
	ID       string
	Content  string
	Priority string
	Format   string
}

func (a *App) Todos(args []string) error {
	req, err := parseTodosArgs(args)
	if err != nil {
		return err
	}
	var report todos.Report
	switch req.Action {
	case "list":
		report, err = todos.List(a.Workspace)
	case "add":
		report, err = todos.Add(a.Workspace, req.Content, req.Priority)
	case "start":
		report, err = todos.UpdateStatus(a.Workspace, req.ID, "in_progress")
	case "done":
		report, err = todos.UpdateStatus(a.Workspace, req.ID, "completed")
	case "pending":
		report, err = todos.UpdateStatus(a.Workspace, req.ID, "pending")
	case "clear":
		report, err = todos.Clear(a.Workspace)
	default:
		err = fmt.Errorf("unknown todos command %q", req.Action)
	}
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	todos.RenderText(a.Out, report)
	return nil
}

func parseTodosArgs(args []string) (todosRequest, error) {
	req := todosRequest{Action: "list", Format: "text", Priority: "medium"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return todosRequest{}, errors.New("todos output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--priority":
			index++
			if index >= len(args) {
				return todosRequest{}, errors.New("todo priority is required")
			}
			req.Priority = args[index]
		case strings.HasPrefix(arg, "--priority="):
			req.Priority = strings.TrimPrefix(arg, "--priority=")
		default:
			rest = append(rest, arg)
		}
	}
	switch req.Format {
	case "text", "json":
	default:
		return todosRequest{}, fmt.Errorf("unknown todos output format %q", req.Format)
	}
	if len(rest) == 0 {
		return req, nil
	}
	req.Action = normalizeTodosAction(rest[0])
	if req.Action == "list" {
		return req, nil
	}
	switch req.Action {
	case "add":
		if len(rest) < 2 {
			return todosRequest{}, errors.New("todo content is required")
		}
		req.Content = strings.Join(rest[1:], " ")
	case "start", "done", "pending":
		if len(rest) < 2 {
			return todosRequest{}, fmt.Errorf("todo id is required for %s", req.Action)
		}
		req.ID = rest[1]
	case "clear":
	default:
		return todosRequest{}, fmt.Errorf("unknown todos command %q", req.Action)
	}
	return req, nil
}

func normalizeTodosAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "list", "ls", "status", "show":
		return "list"
	case "add", "new", "create":
		return "add"
	case "start", "begin", "in-progress", "in_progress", "doing":
		return "start"
	case "done", "complete", "completed", "finish", "finished", "resolve", "resolved":
		return "done"
	case "pending", "todo", "open", "reopen":
		return "pending"
	case "clear", "reset", "remove-all", "delete-all":
		return "clear"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

type commandRequest struct {
	Format    string
	TimeoutMS int
	Command   []string
}

func (a *App) RunCommand(ctx context.Context, args []string) error {
	req, err := parseCommandRequest("run", args, nil)
	if err != nil {
		return err
	}
	return a.runCommandRequest(ctx, "run", req)
}

func (a *App) LanguageCommand(ctx context.Context, language string, args []string) error {
	req, err := parseCommandRequest(language, args, nil)
	if err != nil {
		return err
	}
	command, err := languageCommand(a.Workspace, language, req.Command)
	if err != nil {
		return err
	}
	req.Command = command
	return a.runCommandRequest(ctx, language, req)
}

func (a *App) ProjectCommand(ctx context.Context, kind string, args []string) error {
	req, err := parseCommandRequest(kind, args, defaultProjectCommand(kind))
	if err != nil {
		return err
	}
	return a.runCommandRequest(ctx, kind, req)
}

func (a *App) runCommandRequest(ctx context.Context, kind string, req commandRequest) error {
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	result, err := commandrun.Run(ctx, commandrun.Options{
		Workspace: a.Workspace,
		Command:   req.Command,
		Timeout:   timeout,
		Kind:      kind,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	commandrun.RenderText(a.Out, result)
	return nil
}

func parseCommandRequest(command string, args []string, defaultCommand []string) (commandRequest, error) {
	usage := "codog " + command + " COMMAND [ARG...] [--timeout-ms N] [--json|--output-format text|json]"
	if len(defaultCommand) > 0 {
		usage = "codog " + command + " [ARG...] [--timeout-ms N] [--json|--output-format text|json]"
	}
	req := commandRequest{Format: "text", TimeoutMS: 10 * 60 * 1000}
	commandArgs := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--":
			commandArgs = append(commandArgs, args[index+1:]...)
			index = len(args)
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: command, Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--timeout-ms":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: command, Flag: arg, Usage: usage}
			}
			timeout, err := parseCommandTimeout(args[index], "--timeout-ms", usage)
			if err != nil {
				return req, err
			}
			req.TimeoutMS = timeout
		case strings.HasPrefix(arg, "--timeout-ms="):
			timeout, err := parseCommandTimeout(strings.TrimPrefix(arg, "--timeout-ms="), "--timeout-ms", usage)
			if err != nil {
				return req, err
			}
			req.TimeoutMS = timeout
		default:
			commandArgs = append(commandArgs, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat(command, req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(defaultCommand) == 0 {
		req.Command = commandArgs
	} else {
		req.Command = append([]string(nil), defaultCommand...)
		req.Command = append(req.Command, commandArgs...)
	}
	if len(req.Command) == 0 {
		return req, requiredArgumentError{Command: command, Argument: "COMMAND", Usage: usage}
	}
	return req, nil
}

func languageCommand(workspace, language string, args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("usage: codog %s CODE|FILE [ARG...]", language)
	}
	var executable string
	var inlineFlag string
	switch strings.ToLower(language) {
	case "node", "javascript", "js":
		executable = "node"
		inlineFlag = "-e"
	case "python", "python3", "py":
		executable = pythonExecutable()
		inlineFlag = "-c"
	default:
		return nil, fmt.Errorf("unknown language command %q", language)
	}
	if path, ok := existingLanguageScript(workspace, args[0]); ok {
		return append([]string{executable, path}, args[1:]...), nil
	}
	return []string{executable, inlineFlag, strings.Join(args, " ")}, nil
}

func existingLanguageScript(workspace, value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	path := value
	if !filepath.IsAbs(path) && strings.TrimSpace(workspace) != "" {
		path = filepath.Join(workspace, path)
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	return path, true
}

func pythonExecutable() string {
	if _, err := exec.LookPath("python3"); err == nil {
		return "python3"
	}
	return "python"
}

func parseCommandTimeout(value string, option string, usage string) (int, error) {
	timeout, err := strconv.Atoi(value)
	if err != nil || timeout <= 0 {
		return 0, invalidFlagValueError{Flag: option, Value: value, Message: "command timeout must be positive", Usage: usage}
	}
	return timeout, nil
}

func defaultProjectCommand(kind string) []string {
	switch kind {
	case "test":
		return []string{"go", "test", "./..."}
	case "build":
		return []string{"go", "build", "./..."}
	case "lint":
		return []string{"go", "vet", "./..."}
	default:
		return nil
	}
}

type searchRequest struct {
	Query      string
	Path       string
	Glob       string
	IgnoreCase bool
	Limit      int
	Format     string
}

type searchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type searchReport struct {
	Kind       string        `json:"kind"`
	Query      string        `json:"query"`
	Path       string        `json:"path,omitempty"`
	Glob       string        `json:"glob,omitempty"`
	IgnoreCase bool          `json:"ignore_case,omitempty"`
	Limit      int           `json:"limit"`
	Total      int           `json:"total"`
	Truncated  bool          `json:"truncated"`
	Matches    []searchMatch `json:"matches"`
}

type filesRequest struct {
	Format        string
	Path          string
	Glob          string
	Limit         int
	IncludeHidden bool
}

func (a *App) Files(args []string) error {
	req, err := parseFilesArgs(args)
	if err != nil {
		return err
	}
	report, err := fileinventory.Build(a.Workspace, fileinventory.Options{
		Path:             req.Path,
		Glob:             req.Glob,
		Limit:            req.Limit,
		IncludeHidden:    req.IncludeHidden,
		RespectGitignore: a.Config.EffectiveRespectGitignore(),
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fileinventory.RenderText(a.Out, report)
	return nil
}

func (a *App) Search(ctx context.Context, args []string) error {
	req, err := parseSearchArgs(args)
	if err != nil {
		return err
	}
	report, err := a.searchReport(ctx, req)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderSearchReport(a.Out, report)
	return nil
}

func (a *App) searchReport(ctx context.Context, req searchRequest) (searchReport, error) {
	payload, _ := json.Marshal(map[string]any{
		"pattern":     req.Query,
		"path":        req.Path,
		"glob":        req.Glob,
		"output_mode": "content",
		"ignore_case": req.IgnoreCase,
		"limit":       req.Limit,
	})
	raw, err := tools.GrepTool{Workspace: a.Workspace, RespectGitignore: a.Config.EffectiveRespectGitignore()}.Execute(ctx, payload)
	if err != nil {
		return searchReport{}, err
	}
	var result struct {
		Matches   []searchMatch `json:"matches"`
		Truncated bool          `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return searchReport{}, err
	}
	return searchReport{
		Kind:       "search",
		Query:      req.Query,
		Path:       req.Path,
		Glob:       req.Glob,
		IgnoreCase: req.IgnoreCase,
		Limit:      req.Limit,
		Total:      len(result.Matches),
		Truncated:  result.Truncated,
		Matches:    result.Matches,
	}, nil
}

func renderSearchReport(out io.Writer, report searchReport) {
	fmt.Fprintln(out, "Search")
	fmt.Fprintf(out, "  Query            %s\n", report.Query)
	fmt.Fprintf(out, "  Matches          %d\n", report.Total)
	if report.Truncated {
		fmt.Fprintf(out, "  Truncated        true\n")
	}
	if report.Total == 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "No matches.")
		return
	}
	fmt.Fprintln(out)
	for _, match := range report.Matches {
		fmt.Fprintf(out, "%s:%d:%s\n", match.Path, match.Line, match.Text)
	}
}

type securityReviewRequest struct {
	Format string
	Limit  int
}

type bughunterRequest struct {
	Format string
	Scope  string
	Limit  int
}

func (a *App) Bughunter(args []string) error {
	req, err := parseBughunterArgs(args)
	if err != nil {
		return err
	}
	report, err := bughunt.Scan(a.Workspace, bughunt.Options{Scope: req.Scope, Limit: req.Limit})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	bughunt.RenderText(a.Out, report)
	return nil
}

func parseBughunterArgs(args []string) (bughunterRequest, error) {
	const usage = "codog bughunter [SCOPE] [--limit N] [--json|--output-format text|json]"
	req := bughunterRequest{Format: "text", Limit: 200}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return bughunterRequest{}, missingFlagValueError{Command: "bughunter", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--limit":
			index++
			if missingFlagValueAt(args, index) {
				return bughunterRequest{}, missingFlagValueError{Command: "bughunter", Flag: arg, Usage: usage}
			}
			limit, err := parsePositiveIntOption(args[index], "--limit", usage)
			if err != nil {
				return bughunterRequest{}, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := parsePositiveIntOption(strings.TrimPrefix(arg, "--limit="), "--limit", usage)
			if err != nil {
				return bughunterRequest{}, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "-"):
			return bughunterRequest{}, unknownOptionError{Command: "bughunter", Option: arg, Usage: usage}
		default:
			if req.Scope != "" {
				return bughunterRequest{}, unexpectedExtraArgsError{Command: "bughunter", Args: []string{arg}, Usage: usage}
			}
			req.Scope = arg
		}
	}
	normalizedFormat, err := normalizeOutputFormat("bughunter", req.Format, []string{"text", "json"})
	if err != nil {
		return bughunterRequest{}, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func (a *App) SecurityReview(args []string) error {
	req, err := parseSecurityReviewArgs(args)
	if err != nil {
		return err
	}
	report, err := securityreview.Review(a.Workspace, req.Limit)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	securityreview.RenderText(a.Out, report)
	return nil
}

func parseSecurityReviewArgs(args []string) (securityReviewRequest, error) {
	const usage = "codog security-review [--limit N] [--json|--output-format text|json]"
	req := securityReviewRequest{Format: "text", Limit: 200}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return securityReviewRequest{}, missingFlagValueError{Command: "security-review", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--limit":
			index++
			if missingFlagValueAt(args, index) {
				return securityReviewRequest{}, missingFlagValueError{Command: "security-review", Flag: arg, Usage: usage}
			}
			limit, err := parsePositiveIntOption(args[index], "--limit", usage)
			if err != nil {
				return securityReviewRequest{}, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := parsePositiveIntOption(strings.TrimPrefix(arg, "--limit="), "--limit", usage)
			if err != nil {
				return securityReviewRequest{}, err
			}
			req.Limit = limit
		default:
			if strings.HasPrefix(arg, "-") {
				return securityReviewRequest{}, unknownOptionError{Command: "security-review", Option: arg, Usage: usage}
			}
			return securityReviewRequest{}, unexpectedExtraArgsError{Command: "security-review", Args: []string{arg}, Usage: usage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("security-review", req.Format, []string{"text", "json"})
	if err != nil {
		return securityReviewRequest{}, err
	}
	req.Format = normalizedFormat
	return req, nil
}

type reviewRequest struct {
	Format string
	Base   string
	Staged bool
	Limit  int
}

type reviewRemoteRequest struct {
	Format string
	Base   string
	Staged bool
	Limit  int
	PR     string
	Repo   string
}

type reviewRemoteReport struct {
	Kind        string                `json:"kind"`
	Action      string                `json:"action"`
	Status      string                `json:"status"`
	Repository  string                `json:"repository,omitempty"`
	PullRequest int                   `json:"pull_request,omitempty"`
	URL         string                `json:"url,omitempty"`
	Local       localreview.Report    `json:"local_review"`
	Remote      githubcomments.Report `json:"remote_comments"`
	Signals     []string              `json:"signals,omitempty"`
}

func (a *App) Review(args []string) error {
	req, err := parseReviewArgs(args)
	if err != nil {
		return err
	}
	report, err := localreview.Run(a.Workspace, localreview.Options{
		Base:   req.Base,
		Staged: req.Staged,
		Limit:  req.Limit,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	localreview.RenderText(a.Out, report)
	return nil
}

func (a *App) ReviewRemote(ctx context.Context, args []string) error {
	req, err := parseReviewRemoteArgs(args)
	if err != nil {
		return err
	}
	localReport, err := localreview.Run(a.Workspace, localreview.Options{
		Base:   req.Base,
		Staged: req.Staged,
		Limit:  req.Limit,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	remoteReport, err := githubcomments.Fetch(ctx, githubcomments.Options{
		PR:   req.PR,
		Repo: req.Repo,
	})
	if err != nil {
		return err
	}
	report := buildReviewRemoteReport(localReport, remoteReport)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderReviewRemoteReport(a.Out, report)
	return nil
}

func parseReviewArgs(args []string) (reviewRequest, error) {
	req := reviewRequest{Format: "text", Limit: 200}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--staged":
			req.Staged = true
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return reviewRequest{}, errors.New("review output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--base":
			index++
			if index >= len(args) {
				return reviewRequest{}, errors.New("review base ref is required")
			}
			req.Base = args[index]
		case strings.HasPrefix(arg, "--base="):
			req.Base = strings.TrimPrefix(arg, "--base=")
		case arg == "--limit":
			index++
			if index >= len(args) {
				return reviewRequest{}, errors.New("review limit is required")
			}
			limit, err := strconv.Atoi(args[index])
			if err != nil {
				return reviewRequest{}, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := strconv.Atoi(strings.TrimPrefix(arg, "--limit="))
			if err != nil {
				return reviewRequest{}, err
			}
			req.Limit = limit
		default:
			return reviewRequest{}, fmt.Errorf("unknown review argument %q", arg)
		}
	}
	switch req.Format {
	case "text", "json":
		return req, nil
	default:
		return reviewRequest{}, fmt.Errorf("unknown review output format %q", req.Format)
	}
}

func parseReviewRemoteArgs(args []string) (reviewRemoteRequest, error) {
	req := reviewRemoteRequest{Format: "text", Limit: 200}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--staged":
			req.Staged = true
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("reviewRemote output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--base":
			index++
			if index >= len(args) {
				return req, errors.New("reviewRemote base ref is required")
			}
			req.Base = args[index]
		case strings.HasPrefix(arg, "--base="):
			req.Base = strings.TrimPrefix(arg, "--base=")
		case arg == "--limit":
			index++
			if index >= len(args) {
				return req, errors.New("reviewRemote limit is required")
			}
			limit, err := strconv.Atoi(args[index])
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := strconv.Atoi(strings.TrimPrefix(arg, "--limit="))
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case arg == "--repo":
			index++
			if index >= len(args) {
				return req, errors.New("reviewRemote repository is required")
			}
			req.Repo = args[index]
		case strings.HasPrefix(arg, "--repo="):
			req.Repo = strings.TrimPrefix(arg, "--repo=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, fmt.Errorf("unknown reviewRemote argument %q", arg)
			}
			rest = append(rest, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "reviewRemote"); err != nil {
		return req, err
	}
	if len(rest) > 1 {
		return req, fmt.Errorf("unexpected reviewRemote argument %q", rest[1])
	}
	if len(rest) == 1 {
		req.PR = rest[0]
	}
	return req, nil
}

func buildReviewRemoteReport(localReport localreview.Report, remoteReport githubcomments.Report) reviewRemoteReport {
	report := reviewRemoteReport{
		Kind:        "review_remote",
		Action:      "scan",
		Status:      "ok",
		Repository:  remoteReport.Repository,
		PullRequest: remoteReport.Number,
		URL:         remoteReport.URL,
		Local:       localReport,
		Remote:      remoteReport,
	}
	if localReport.Status == "clean" && remoteReport.Total == 0 {
		report.Status = "clean"
	}
	if remoteReport.Total != 0 {
		report.Status = "comments"
	}
	if len(localReport.SecurityFindings) != 0 || localReport.Status == "findings" {
		report.Status = "findings"
		report.Signals = append(report.Signals, "local security findings")
	}
	if len(remoteReport.ReviewComments) != 0 {
		report.Signals = append(report.Signals, "remote review comments")
	}
	if len(remoteReport.IssueComments) != 0 {
		report.Signals = append(report.Signals, "remote issue comments")
	}
	if len(localReport.Files) != 0 {
		report.Signals = append(report.Signals, "local changed files")
	}
	return report
}

func renderReviewRemoteReport(out io.Writer, report reviewRemoteReport) {
	fmt.Fprintln(out, "Remote Review")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	if report.Repository != "" {
		fmt.Fprintf(out, "  Repository       %s\n", report.Repository)
	}
	if report.PullRequest != 0 {
		fmt.Fprintf(out, "  Pull request     #%d\n", report.PullRequest)
	}
	if report.URL != "" {
		fmt.Fprintf(out, "  URL              %s\n", report.URL)
	}
	fmt.Fprintf(out, "  Local files      %d\n", report.Local.Summary.Files)
	fmt.Fprintf(out, "  Local findings   %d\n", len(report.Local.SecurityFindings))
	fmt.Fprintf(out, "  Remote comments  %d\n", report.Remote.Total)
	if len(report.Signals) != 0 {
		fmt.Fprintln(out, "Signals")
		for _, signal := range report.Signals {
			fmt.Fprintf(out, "  - %s\n", signal)
		}
	}
	fmt.Fprintln(out)
	localreview.RenderText(out, report.Local)
	fmt.Fprintln(out)
	githubcomments.RenderText(out, report.Remote)
}

type feedbackRequest struct {
	Format    string
	Output    string
	Message   string
	SessionID string
}

type feedbackReport struct {
	Kind            string `json:"kind"`
	Action          string `json:"action"`
	Status          string `json:"status"`
	File            string `json:"file"`
	Bytes           int    `json:"bytes"`
	Message         string `json:"message,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	SessionMessages int    `json:"session_messages,omitempty"`
	Model           string `json:"model"`
	PermissionMode  string `json:"permission_mode"`
	GitBranch       string `json:"git_branch,omitempty"`
	GitClean        bool   `json:"git_clean"`
}

type feedbackBundle struct {
	CreatedAt time.Time            `json:"created_at"`
	Message   string               `json:"message"`
	Version   versioninfo.Report   `json:"version"`
	Status    localstatus.Snapshot `json:"status"`
}

func (a *App) Feedback(args []string, overrides config.FlagOverrides) error {
	req, err := parseFeedbackArgs(args, overrides)
	if err != nil {
		return err
	}
	report, err := a.writeFeedback(req)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		encoded, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(encoded))
		return nil
	}
	renderFeedbackReport(a.Out, report)
	return nil
}

func (a *App) writeFeedback(req feedbackRequest) (feedbackReport, error) {
	active, err := a.feedbackSession(req.SessionID)
	if err != nil {
		return feedbackReport{}, err
	}
	snapshot := a.statusSnapshot(active)
	bundle := feedbackBundle{
		CreatedAt: time.Now().UTC(),
		Message:   strings.TrimSpace(req.Message),
		Version:   versioninfo.Build(version, a.Workspace),
		Status:    snapshot,
	}
	path := a.feedbackOutputPath(req.Output, bundle.CreatedAt)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return feedbackReport{}, err
	}
	if err := session.ValidateExportOutputPath(path); err != nil {
		return feedbackReport{}, err
	}
	data := []byte(renderFeedbackMarkdown(bundle))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return feedbackReport{}, err
	}
	report := feedbackReport{
		Kind:            "feedback",
		Action:          "write",
		Status:          "ok",
		File:            path,
		Bytes:           len(data),
		Message:         bundle.Message,
		SessionID:       snapshot.Session.ID,
		SessionMessages: snapshot.Session.MessageCount,
		Model:           snapshot.Config.Model,
		PermissionMode:  snapshot.Config.PermissionMode,
		GitBranch:       snapshot.Git.Branch,
		GitClean:        snapshot.Git.Clean,
	}
	return report, nil
}

func parseFeedbackArgs(args []string, overrides config.FlagOverrides) (feedbackRequest, error) {
	req := feedbackRequest{Format: "text"}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
	}
	if overrides.SessionID != "" {
		req.SessionID = overrides.SessionID
	}
	var message []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("feedback output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--output":
			index++
			if index >= len(args) {
				return req, errors.New("feedback output path is required")
			}
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("feedback session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) {
				return req, errors.New("feedback resume session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case arg == "--message":
			index++
			if index >= len(args) {
				return req, errors.New("feedback message is required")
			}
			message = append(message, args[index])
		case strings.HasPrefix(arg, "--message="):
			message = append(message, strings.TrimPrefix(arg, "--message="))
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown feedback flag %q", arg)
		default:
			message = append(message, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "feedback"); err != nil {
		return req, err
	}
	req.Message = strings.TrimSpace(strings.Join(message, " "))
	return req, nil
}

func (a *App) feedbackSession(sessionID string) (*session.Session, error) {
	if strings.TrimSpace(sessionID) == "" || a.Sessions == nil {
		return nil, nil
	}
	active, err := a.Sessions.Open(sessionID)
	if errors.Is(err, session.ErrNoSessions) {
		return nil, nil
	}
	return active, err
}

func (a *App) feedbackOutputPath(output string, createdAt time.Time) string {
	filename := fmt.Sprintf("feedback-%s-%d.md", createdAt.Format("20060102T150405Z"), createdAt.UnixNano())
	if strings.TrimSpace(output) == "" {
		return filepath.Join(a.Workspace, ".codog", "feedback", filename)
	}
	path := a.resolveOutputPath(output)
	if strings.EqualFold(filepath.Ext(path), ".md") {
		return path
	}
	return filepath.Join(path, filename)
}

func renderFeedbackReport(out io.Writer, report feedbackReport) {
	fmt.Fprintln(out, "Feedback")
	fmt.Fprintf(out, "  File             %s\n", report.File)
	fmt.Fprintf(out, "  Bytes            %d\n", report.Bytes)
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", prompthistory.Preview(report.Message, 80))
	}
	if report.SessionID != "" {
		fmt.Fprintf(out, "  Session          %s (%d messages)\n", report.SessionID, report.SessionMessages)
	}
	fmt.Fprintf(out, "  Model            %s\n", report.Model)
	fmt.Fprintf(out, "  Permission       %s\n", report.PermissionMode)
	if report.GitBranch != "" {
		fmt.Fprintf(out, "  Git              branch=%s clean=%t\n", report.GitBranch, report.GitClean)
	}
}

func renderFeedbackMarkdown(bundle feedbackBundle) string {
	var builder strings.Builder
	builder.WriteString("# Codog Feedback\n\n")
	builder.WriteString(fmt.Sprintf("- Created: %s\n", bundle.CreatedAt.Format(time.RFC3339)))
	builder.WriteString(fmt.Sprintf("- Version: %s\n", bundle.Version.Version))
	builder.WriteString(fmt.Sprintf("- Target: %s\n", bundle.Version.BuildTarget))
	builder.WriteString(fmt.Sprintf("- Workspace: %s\n", bundle.Status.Workspace.Path))
	builder.WriteString(fmt.Sprintf("- Model: %s\n", bundle.Status.Config.Model))
	builder.WriteString(fmt.Sprintf("- Permission mode: %s\n", bundle.Status.Config.PermissionMode))
	if bundle.Status.Session.Active {
		builder.WriteString(fmt.Sprintf("- Session: %s (%d messages)\n", bundle.Status.Session.ID, bundle.Status.Session.MessageCount))
	}
	if bundle.Status.Git.Available {
		builder.WriteString(fmt.Sprintf("- Git: branch=%s clean=%t staged=%d unstaged=%d untracked=%d conflicts=%d\n",
			bundle.Status.Git.Branch,
			bundle.Status.Git.Clean,
			bundle.Status.Git.Staged,
			bundle.Status.Git.Unstaged,
			bundle.Status.Git.Untracked,
			bundle.Status.Git.Conflicts,
		))
	}
	builder.WriteString("\n## Message\n\n")
	if strings.TrimSpace(bundle.Message) == "" {
		builder.WriteString("No description provided.\n")
	} else {
		builder.WriteString(bundle.Message)
		builder.WriteString("\n")
	}
	builder.WriteString("\n## Diagnostics\n\n```json\n")
	diagnostics, _ := json.MarshalIndent(map[string]any{
		"version": bundle.Version,
		"status":  bundle.Status,
	}, "", "  ")
	builder.Write(diagnostics)
	builder.WriteString("\n```\n")
	return builder.String()
}

type draftRequest struct {
	Format    string
	Output    string
	Context   string
	SessionID string
}

type draftReport struct {
	Kind            string               `json:"kind"`
	Action          string               `json:"action"`
	Status          string               `json:"status"`
	File            string               `json:"file"`
	Bytes           int                  `json:"bytes"`
	Title           string               `json:"title"`
	Context         string               `json:"context,omitempty"`
	Branch          string               `json:"branch,omitempty"`
	GitClean        bool                 `json:"git_clean"`
	SessionID       string               `json:"session_id,omitempty"`
	SessionMessages int                  `json:"session_messages,omitempty"`
	GitState        *draftGitStateReport `json:"git_state,omitempty"`
}

type draftGitStateReport struct {
	RemoteBase     string `json:"remote_base,omitempty"`
	RemoteBaseSHA  string `json:"remote_base_sha,omitempty"`
	HeadSHA        string `json:"head_sha,omitempty"`
	BranchName     string `json:"branch_name,omitempty"`
	PatchBytes     int    `json:"patch_bytes"`
	UntrackedFiles int    `json:"untracked_files"`
	FormatPatch    bool   `json:"format_patch"`
}

type draftBundle struct {
	Kind       string
	CreatedAt  time.Time
	Context    string
	Title      string
	Status     localstatus.Snapshot
	GitStatus  string
	DiffStat   string
	StagedStat string
	RecentLog  string
	Remote     string
	GitState   *gitops.PreservedGitState
}

func (a *App) PullRequestDraft(args []string, overrides config.FlagOverrides) error {
	return a.writeDraft("pr", args, overrides)
}

type commitPushPRRequest struct {
	Format  string
	Message string
	Title   string
	Body    string
	Branch  string
	Base    string
	Remote  string
	All     bool
	Draft   bool
	NoPR    bool
	DryRun  bool
}

func (a *App) CommitPushPR(ctx context.Context, args []string) error {
	req, err := parseCommitPushPRArgs(args)
	if err != nil {
		return err
	}
	report, err := prworkflow.Run(ctx, prworkflow.Options{
		Workspace: a.Workspace,
		Message:   req.Message,
		Title:     req.Title,
		Body:      req.Body,
		Branch:    req.Branch,
		Base:      req.Base,
		Remote:    req.Remote,
		All:       req.All,
		Draft:     req.Draft,
		NoPR:      req.NoPR,
		DryRun:    req.DryRun,
	})
	if err != nil {
		return err
	}
	if report.Ship != nil {
		_ = workerstate.Save(a.Workspace, workerstate.New(workerstate.Options{
			Version:        version,
			Mode:           "ship",
			Status:         report.Status,
			Workspace:      a.Workspace,
			PermissionMode: a.Config.PermissionMode,
			Ship:           report.Ship,
		}))
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderCommitPushPRReport(a.Out, report)
	return nil
}

func parseCommitPushPRArgs(args []string) (commitPushPRRequest, error) {
	req := commitPushPRRequest{Format: "text", Remote: "origin", All: true}
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("commit-push-pr output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--message" || arg == "-m":
			index++
			if index >= len(args) {
				return req, errors.New("commit-push-pr message is required")
			}
			req.Message = args[index]
		case strings.HasPrefix(arg, "--message="):
			req.Message = strings.TrimPrefix(arg, "--message=")
		case arg == "--title":
			index++
			if index >= len(args) {
				return req, errors.New("commit-push-pr title is required")
			}
			req.Title = args[index]
		case strings.HasPrefix(arg, "--title="):
			req.Title = strings.TrimPrefix(arg, "--title=")
		case arg == "--body":
			index++
			if index >= len(args) {
				return req, errors.New("commit-push-pr body is required")
			}
			req.Body = args[index]
		case strings.HasPrefix(arg, "--body="):
			req.Body = strings.TrimPrefix(arg, "--body=")
		case arg == "--branch" || arg == "-b":
			index++
			if index >= len(args) {
				return req, errors.New("commit-push-pr branch is required")
			}
			req.Branch = args[index]
		case strings.HasPrefix(arg, "--branch="):
			req.Branch = strings.TrimPrefix(arg, "--branch=")
		case arg == "--base":
			index++
			if index >= len(args) {
				return req, errors.New("commit-push-pr base branch is required")
			}
			req.Base = args[index]
		case strings.HasPrefix(arg, "--base="):
			req.Base = strings.TrimPrefix(arg, "--base=")
		case arg == "--remote":
			index++
			if index >= len(args) {
				return req, errors.New("commit-push-pr remote is required")
			}
			req.Remote = args[index]
		case strings.HasPrefix(arg, "--remote="):
			req.Remote = strings.TrimPrefix(arg, "--remote=")
		case arg == "--draft":
			req.Draft = true
		case arg == "--no-pr":
			req.NoPR = true
		case arg == "--dry-run":
			req.DryRun = true
		case arg == "--all":
			req.All = true
		case arg == "--staged":
			req.All = false
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown commit-push-pr flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "commit-push-pr"); err != nil {
		return req, err
	}
	if strings.TrimSpace(req.Message) == "" {
		req.Message = strings.Join(positionals, " ")
	}
	if strings.TrimSpace(req.Message) == "" && strings.TrimSpace(req.Title) != "" {
		req.Message = req.Title
	}
	if strings.TrimSpace(req.Message) == "" {
		return req, errors.New("commit-push-pr requires a commit message")
	}
	return req, nil
}

func renderCommitPushPRReport(out io.Writer, report prworkflow.Report) {
	fmt.Fprintln(out, "Commit Push PR")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Dry run          %t\n", report.DryRun)
	fmt.Fprintf(out, "  Branch           %s\n", report.Branch)
	if report.Base != "" {
		fmt.Fprintf(out, "  Base             %s\n", report.Base)
	}
	fmt.Fprintf(out, "  Remote           %s\n", report.Remote)
	fmt.Fprintf(out, "  Title            %s\n", report.Title)
	if report.Commit != "" {
		fmt.Fprintf(out, "  Commit           %s\n", report.Commit)
	}
	if report.PRURL != "" {
		fmt.Fprintf(out, "  PR URL           %s\n", report.PRURL)
	}
	if report.Ship != nil {
		fmt.Fprintf(out, "  Ship             %s\n", report.Ship.Summary)
		fmt.Fprintf(out, "  Ship method      %s\n", report.Ship.Provenance.MergeMethod)
		fmt.Fprintf(out, "  Ship range       %s\n", emptyAsNone(report.Ship.Provenance.CommitRange))
		fmt.Fprintf(out, "  Ship actor       %s\n", emptyAsNone(report.Ship.Provenance.Actor))
	}
	for _, step := range report.Steps {
		fmt.Fprintf(out, "  Step             %-12s %s", step.Name, step.Status)
		if len(step.Command) > 0 {
			fmt.Fprintf(out, "  %s", strings.Join(step.Command, " "))
		}
		fmt.Fprintln(out)
		if step.Output != "" {
			fmt.Fprintf(out, "    %s\n", prompthistory.Preview(step.Output, 180))
		}
	}
}

type prCommentsRequest struct {
	PR     string
	Repo   string
	Format string
}

type installGitHubAppRequest struct {
	Format     string
	SecretName string
	Workflows  []string
	Force      bool
	DryRun     bool
}

type installSlackAppRequest struct {
	Action string
	Format string
	Target string
	Path   string
	Open   bool
}

type installSlackAppReport struct {
	Kind         string `json:"kind"`
	Action       string `json:"action"`
	Status       string `json:"status"`
	URL          string `json:"url"`
	Opened       bool   `json:"opened"`
	Opener       string `json:"opener,omitempty"`
	InstallCount int    `json:"install_count"`
	Path         string `json:"path,omitempty"`
	Message      string `json:"message,omitempty"`
}

type stickersRequest struct {
	Action string
	Format string
	Target string
	Path   string
	Open   bool
}

type stickersReport struct {
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	URL        string `json:"url"`
	Opened     bool   `json:"opened"`
	Opener     string `json:"opener,omitempty"`
	OrderCount int    `json:"order_count"`
	Path       string `json:"path,omitempty"`
	Message    string `json:"message,omitempty"`
}

type extraUsageRequest struct {
	Action string
	Format string
	Target string
	Path   string
	Open   bool
	Mode   string
}

type extraUsageReport struct {
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Mode       string `json:"mode"`
	URL        string `json:"url"`
	Opened     bool   `json:"opened"`
	Opener     string `json:"opener,omitempty"`
	VisitCount int    `json:"visit_count"`
	Path       string `json:"path,omitempty"`
	Message    string `json:"message,omitempty"`
}

type passesRequest struct {
	Action      string
	Format      string
	Target      string
	Path        string
	Open        bool
	Docs        bool
	ReferralURL string
	BaseURL     string
	OrgUUID     string
	Token       string
	Campaign    string
	Redemptions bool
	SaveURL     bool
	SaveCache   bool
	TimeoutMS   int
}

type passesReport struct {
	Kind                    string            `json:"kind"`
	Action                  string            `json:"action"`
	Status                  string            `json:"status"`
	URL                     string            `json:"url"`
	URLSource               string            `json:"url_source"`
	DocsURL                 string            `json:"docs_url"`
	ReferralURL             string            `json:"referral_url,omitempty"`
	ReferralConfigured      bool              `json:"referral_configured"`
	Opened                  bool              `json:"opened"`
	Opener                  string            `json:"opener,omitempty"`
	VisitCount              int               `json:"visit_count"`
	Path                    string            `json:"path,omitempty"`
	Message                 string            `json:"message,omitempty"`
	RequestSent             bool              `json:"request_sent,omitempty"`
	OrganizationUUID        string            `json:"organization_uuid,omitempty"`
	Campaign                string            `json:"campaign,omitempty"`
	Eligible                *bool             `json:"eligible,omitempty"`
	RemainingPasses         *int              `json:"remaining_passes,omitempty"`
	Limit                   *int              `json:"limit,omitempty"`
	Redeemed                *int              `json:"redeemed,omitempty"`
	AvailablePasses         *int              `json:"available_passes,omitempty"`
	ReferrerReward          *config.MoneyInfo `json:"referrer_reward,omitempty"`
	ReferrerRewardFormatted string            `json:"referrer_reward_formatted,omitempty"`
	SavedReferralURL        bool              `json:"saved_referral_url,omitempty"`
	CacheHit                bool              `json:"cache_hit,omitempty"`
	CachedAt                string            `json:"cached_at,omitempty"`
	SavedEligibilityCache   bool              `json:"saved_eligibility_cache,omitempty"`
	HasVisitedPasses        bool              `json:"has_visited_passes"`
	UpsellSeenCount         int               `json:"upsell_seen_count"`
	LastSeenRemaining       *int              `json:"last_seen_remaining,omitempty"`
	UpsellVisible           bool              `json:"upsell_visible"`
	UpsellReset             bool              `json:"upsell_reset,omitempty"`
	MarkedVisited           bool              `json:"marked_visited,omitempty"`
	MarkedUpsellSeen        bool              `json:"marked_upsell_seen,omitempty"`
}

const slackAppURL = "https://slack.com/marketplace/A08SF47R6P4-claude"
const stickerOrderURL = "https://www.stickermule.com/claudecode"
const extraUsagePersonalURL = "https://claude.ai/settings/usage"
const extraUsageAdminURL = "https://claude.ai/admin-settings/usage"
const guestPassDocsURL = "https://support.claude.com/en/articles/12875061-claude-code-guest-passes"
const installSlackAppUsage = "codog install-slack-app [status|list] [--open|--no-open] [--target user|project|local] [--path PATH] [--output-format text|json]"
const stickersUsage = "codog stickers [status|list] [--open|--no-open] [--target user|project|local] [--path PATH] [--output-format text|json]"
const passesUsage = "codog passes [status|list|show|open|fetch|visit|upsell-seen|set-url URL|clear-url] [--docs] [--referral-url URL] [--org UUID] [--token TOKEN] [--base-url URL] [--campaign NAME] [--redemptions] [--no-save-cache] [--open|--no-open] [--target user|project|local] [--path PATH] [--output-format text|json]"

var openExternalURL = openSystemURL

type autofixPRRequest struct {
	Format string
	PR     string
	Repo   string
	Output string
	Limit  int
	Write  bool
}

func (a *App) AutofixPR(ctx context.Context, args []string) error {
	req, err := parseAutofixPRArgs(args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	comments, err := githubcomments.Fetch(ctx, githubcomments.Options{
		PR:   req.PR,
		Repo: req.Repo,
	})
	if err != nil {
		return err
	}
	report := autofixpr.Build(comments, req.Limit)
	if req.Write || strings.TrimSpace(req.Output) != "" {
		path := a.autofixPROutputPath(req.Output, report, time.Now().UTC())
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := session.ValidateExportOutputPath(path); err != nil {
			return err
		}
		data := []byte(autofixpr.RenderMarkdown(report))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		report.File = path
		report.Bytes = len(data)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	autofixpr.RenderText(a.Out, report)
	return nil
}

func (a *App) PRComments(ctx context.Context, args []string) error {
	req, err := parsePRCommentsArgs(args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	report, err := githubcomments.Fetch(ctx, githubcomments.Options{
		PR:   req.PR,
		Repo: req.Repo,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	githubcomments.RenderText(a.Out, report)
	return nil
}

func parsePRCommentsArgs(args []string) (prCommentsRequest, error) {
	req := prCommentsRequest{Format: "text"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("pr-comments output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--repo":
			index++
			if index >= len(args) {
				return req, errors.New("pr-comments repository is required")
			}
			req.Repo = args[index]
		case strings.HasPrefix(arg, "--repo="):
			req.Repo = strings.TrimPrefix(arg, "--repo=")
		default:
			rest = append(rest, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "pr-comments"); err != nil {
		return req, err
	}
	if len(rest) > 1 {
		return req, fmt.Errorf("unexpected pr-comments argument %q", rest[1])
	}
	if len(rest) == 1 {
		req.PR = rest[0]
	}
	return req, nil
}

func parseAutofixPRArgs(args []string) (autofixPRRequest, error) {
	req := autofixPRRequest{Format: "text", Limit: 100}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("autofix-pr output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--repo":
			index++
			if index >= len(args) {
				return req, errors.New("autofix-pr repository is required")
			}
			req.Repo = args[index]
		case strings.HasPrefix(arg, "--repo="):
			req.Repo = strings.TrimPrefix(arg, "--repo=")
		case arg == "--output":
			index++
			if index >= len(args) {
				return req, errors.New("autofix-pr output path is required")
			}
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		case arg == "--write":
			req.Write = true
		case arg == "--limit":
			index++
			if index >= len(args) {
				return req, errors.New("autofix-pr limit is required")
			}
			limit, err := strconv.Atoi(args[index])
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := strconv.Atoi(strings.TrimPrefix(arg, "--limit="))
			if err != nil {
				return req, err
			}
			req.Limit = limit
		default:
			if strings.HasPrefix(arg, "-") {
				return req, fmt.Errorf("unknown autofix-pr argument %q", arg)
			}
			rest = append(rest, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "autofix-pr"); err != nil {
		return req, err
	}
	if req.Limit < 0 {
		return req, errors.New("autofix-pr limit must be non-negative")
	}
	if len(rest) > 1 {
		return req, fmt.Errorf("unexpected autofix-pr argument %q", rest[1])
	}
	if len(rest) == 1 {
		req.PR = rest[0]
	}
	return req, nil
}

func (a *App) autofixPROutputPath(output string, report autofixpr.Report, createdAt time.Time) string {
	repo := strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(strings.TrimSpace(report.Repository))
	if repo == "" {
		repo = "pr"
	}
	filename := fmt.Sprintf("autofix-pr-%s-%d-%s-%d.md", repo, report.PullRequest, createdAt.Format("20060102T150405Z"), createdAt.UnixNano())
	if strings.TrimSpace(output) == "" {
		return filepath.Join(a.Workspace, ".codog", "autofix", filename)
	}
	path := a.resolveOutputPath(output)
	if strings.EqualFold(filepath.Ext(path), ".md") {
		return path
	}
	return filepath.Join(path, filename)
}

func (a *App) InstallGitHubApp(args []string) error {
	req, err := parseInstallGitHubAppArgs(args)
	if err != nil {
		return err
	}
	report, err := githubsetup.Setup(githubsetup.Options{
		Workspace:  a.Workspace,
		SecretName: req.SecretName,
		Workflows:  req.Workflows,
		Force:      req.Force,
		DryRun:     req.DryRun,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderInstallGitHubAppReport(a.Out, report)
	return nil
}

func parseInstallGitHubAppArgs(args []string) (installGitHubAppRequest, error) {
	req := installGitHubAppRequest{Format: "text"}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("install-github-app output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--secret-name" || arg == "--secret":
			index++
			if index >= len(args) {
				return req, errors.New("GitHub secret name is required")
			}
			req.SecretName = args[index]
		case strings.HasPrefix(arg, "--secret-name="):
			req.SecretName = strings.TrimPrefix(arg, "--secret-name=")
		case strings.HasPrefix(arg, "--secret="):
			req.SecretName = strings.TrimPrefix(arg, "--secret=")
		case arg == "--workflow" || arg == "--workflows":
			index++
			if index >= len(args) {
				return req, errors.New("GitHub workflow name is required")
			}
			req.Workflows = append(req.Workflows, args[index])
		case strings.HasPrefix(arg, "--workflow="):
			req.Workflows = append(req.Workflows, strings.TrimPrefix(arg, "--workflow="))
		case strings.HasPrefix(arg, "--workflows="):
			req.Workflows = append(req.Workflows, strings.TrimPrefix(arg, "--workflows="))
		case arg == "--force":
			req.Force = true
		case arg == "--dry-run":
			req.DryRun = true
		default:
			return req, fmt.Errorf("unknown install-github-app option %q", arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "install-github-app"); err != nil {
		return req, err
	}
	return req, nil
}

func renderInstallGitHubAppReport(out io.Writer, report githubsetup.Report) {
	fmt.Fprintln(out, "GitHub App Setup")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Workspace        %s\n", report.Workspace)
	if report.Repo != "" {
		fmt.Fprintf(out, "  Repository       %s\n", report.Repo)
	}
	fmt.Fprintf(out, "  Secret           %s\n", report.SecretName)
	fmt.Fprintf(out, "  Dry run          %t\n", report.DryRun)
	fmt.Fprintf(out, "  Docs             %s\n", report.DocsURL)
	for _, workflow := range report.Workflows {
		state := "ready"
		switch {
		case workflow.Created:
			state = "created"
		case workflow.Overwritten:
			state = "overwritten"
		case workflow.Exists:
			state = "exists"
		}
		fmt.Fprintf(out, "  Workflow         %s %s %s\n", workflow.Name, state, workflow.Path)
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(out, "  Warning          %s\n", warning)
	}
	for _, instruction := range report.Instructions {
		fmt.Fprintf(out, "  Next             %s\n", instruction)
	}
}

func (a *App) InstallSlackApp(args []string) error {
	req, err := parseInstallSlackAppArgs(args)
	if err != nil {
		return err
	}
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	if req.Action == "status" {
		report := installSlackAppReport{
			Kind:         "install_slack_app",
			Action:       "status",
			Status:       "ok",
			URL:          slackAppURL,
			Opened:       false,
			InstallCount: a.Config.Future.SlackAppInstallCount,
			Path:         path,
			Message:      "Slack app installation status loaded.",
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		renderInstallSlackAppReport(a.Out, report)
		return nil
	}
	count := a.Config.Future.SlackAppInstallCount + 1
	if err := setCompatibilityValue(path, "compatibility.slack_app_install_count", legacySlackAppInstallCountKey, count); err != nil {
		return err
	}
	a.Config.Future.SlackAppInstallCount = count
	report := installSlackAppReport{
		Kind:         "install_slack_app",
		Action:       "open",
		Status:       "ok",
		URL:          slackAppURL,
		InstallCount: count,
		Path:         path,
		Message:      "Visit the Slack Marketplace URL to install the Claude app.",
	}
	if req.Open {
		opener, err := openExternalURL(slackAppURL)
		if err != nil {
			report.Status = "open_failed"
			report.Message = "Could not open a browser automatically. Visit the URL manually."
		} else {
			report.Opened = true
			report.Opener = opener
			report.Message = "Opening Slack app installation page in browser."
		}
	} else {
		report.Action = "show"
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderInstallSlackAppReport(a.Out, report)
	return nil
}

func parseInstallSlackAppArgs(args []string) (installSlackAppRequest, error) {
	req := installSlackAppRequest{Action: "open", Format: "text", Target: "user", Open: true}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "install-slack-app", Flag: arg, Usage: installSlackAppUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "install-slack-app", Flag: arg, Usage: installSlackAppUsage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "install-slack-app", Flag: arg, Usage: installSlackAppUsage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--open":
			req.Open = true
		case arg == "--no-open":
			req.Open = false
		case arg == "status" || arg == "list" || arg == "ls":
			req.Action = "status"
			req.Open = false
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "install-slack-app", Option: arg, Usage: installSlackAppUsage}
			}
			return req, unexpectedExtraArgsError{Command: "install-slack-app", Args: []string{arg}, Usage: installSlackAppUsage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("install-slack-app", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func renderInstallSlackAppReport(out io.Writer, report installSlackAppReport) {
	fmt.Fprintln(out, "Slack App Setup")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  URL              %s\n", report.URL)
	fmt.Fprintf(out, "  Opened           %t\n", report.Opened)
	if report.Opener != "" {
		fmt.Fprintf(out, "  Opener           %s\n", report.Opener)
	}
	fmt.Fprintf(out, "  Install count    %d\n", report.InstallCount)
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func (a *App) Stickers(args []string) error {
	req, err := parseStickersArgs(args)
	if err != nil {
		return err
	}
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	if req.Action == "status" {
		report := stickersReport{
			Kind:       "stickers",
			Action:     "status",
			Status:     "ok",
			URL:        stickerOrderURL,
			Opened:     false,
			OrderCount: a.Config.Future.StickerOrderCount,
			Path:       path,
			Message:    "Sticker order status loaded.",
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		renderStickersReport(a.Out, report)
		return nil
	}
	count := a.Config.Future.StickerOrderCount + 1
	if err := setCompatibilityValue(path, "compatibility.sticker_order_count", legacyStickerOrderCountKey, count); err != nil {
		return err
	}
	a.Config.Future.StickerOrderCount = count
	report := stickersReport{
		Kind:       "stickers",
		Action:     "open",
		Status:     "ok",
		URL:        stickerOrderURL,
		OrderCount: count,
		Path:       path,
		Message:    "Visit the sticker page to order Claude Code stickers.",
	}
	if req.Open {
		opener, err := openExternalURL(stickerOrderURL)
		if err != nil {
			report.Status = "open_failed"
			report.Message = "Could not open a browser automatically. Visit the URL manually."
		} else {
			report.Opened = true
			report.Opener = opener
			report.Message = "Opening sticker order page in browser."
		}
	} else {
		report.Action = "show"
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderStickersReport(a.Out, report)
	return nil
}

func parseStickersArgs(args []string) (stickersRequest, error) {
	req := stickersRequest{Action: "open", Format: "text", Target: "user", Open: true}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "stickers", Flag: arg, Usage: stickersUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "stickers", Flag: arg, Usage: stickersUsage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "stickers", Flag: arg, Usage: stickersUsage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--open":
			req.Open = true
		case arg == "--no-open":
			req.Open = false
		case arg == "status" || arg == "list" || arg == "ls":
			req.Action = "status"
			req.Open = false
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "stickers", Option: arg, Usage: stickersUsage}
			}
			return req, unexpectedExtraArgsError{Command: "stickers", Args: []string{arg}, Usage: stickersUsage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("stickers", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func renderStickersReport(out io.Writer, report stickersReport) {
	fmt.Fprintln(out, "Sticker Order")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  URL              %s\n", report.URL)
	fmt.Fprintf(out, "  Opened           %t\n", report.Opened)
	if report.Opener != "" {
		fmt.Fprintf(out, "  Opener           %s\n", report.Opener)
	}
	fmt.Fprintf(out, "  Order count      %d\n", report.OrderCount)
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func (a *App) ExtraUsage(args []string) error {
	req, err := parseExtraUsageArgs(args)
	if err != nil {
		return err
	}
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	if req.Action == "status" {
		report := extraUsageReport{
			Kind:       "extra_usage",
			Action:     "status",
			Status:     "ok",
			Mode:       req.Mode,
			URL:        extraUsageURL(req.Mode),
			Opened:     false,
			VisitCount: a.Config.Future.ExtraUsageVisitCount,
			Path:       path,
			Message:    "Extra usage status loaded.",
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		renderExtraUsageReport(a.Out, report)
		return nil
	}
	count := a.Config.Future.ExtraUsageVisitCount + 1
	if err := setCompatibilityValue(path, "compatibility.extra_usage_visit_count", legacyExtraUsageVisitCountKey, count); err != nil {
		return err
	}
	a.Config.Future.ExtraUsageVisitCount = count

	url := extraUsageURL(req.Mode)
	report := extraUsageReport{
		Kind:       "extra_usage",
		Action:     "open",
		Status:     "ok",
		Mode:       req.Mode,
		URL:        url,
		VisitCount: count,
		Path:       path,
		Message:    extraUsageMessage(req.Mode),
	}
	if req.Open {
		opener, err := openExternalURL(url)
		if err != nil {
			report.Status = "open_failed"
			report.Message = "Could not open a browser automatically. Visit the URL manually."
		} else {
			report.Opened = true
			report.Opener = opener
			report.Message = "Opening Claude usage settings in browser."
		}
	} else {
		report.Action = "show"
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderExtraUsageReport(a.Out, report)
	return nil
}

func parseExtraUsageArgs(args []string) (extraUsageRequest, error) {
	req := extraUsageRequest{Action: "open", Format: "text", Target: "user", Open: true, Mode: "personal"}
	const usage = "codog extra-usage [status|list|personal|admin] [--open|--no-open] [--target user|project|local] [--path PATH] [--output-format text|json]"
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "extra-usage", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "extra-usage", Flag: arg, Usage: usage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "extra-usage", Flag: arg, Usage: usage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--open":
			req.Open = true
		case arg == "--no-open":
			req.Open = false
		case arg == "--admin":
			req.Mode = "admin"
		case arg == "--personal":
			req.Mode = "personal"
		case arg == "status" || arg == "list" || arg == "ls":
			req.Action = "status"
			req.Open = false
		case arg == "admin" || arg == "team" || arg == "enterprise" || arg == "org" || arg == "organization":
			req.Mode = "admin"
		case arg == "personal" || arg == "user" || arg == "individual":
			req.Mode = "personal"
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "extra-usage", Option: arg, Usage: usage}
			}
			return req, unexpectedExtraArgsError{Command: "extra-usage", Args: []string{arg}, Usage: usage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("extra-usage", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func appendExtraUsageNoOpen(args []string) []string {
	out := append([]string{}, args...)
	return append(out, "--no-open")
}

func extraUsageURL(mode string) string {
	if mode == "admin" {
		return extraUsageAdminURL
	}
	return extraUsagePersonalURL
}

func extraUsageMessage(mode string) string {
	if mode == "admin" {
		return "Visit Claude admin usage settings to manage organization extra usage."
	}
	return "Visit Claude usage settings to manage extra usage."
}

func renderExtraUsageReport(out io.Writer, report extraUsageReport) {
	fmt.Fprintln(out, "Extra Usage")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Mode             %s\n", report.Mode)
	fmt.Fprintf(out, "  URL              %s\n", report.URL)
	fmt.Fprintf(out, "  Opened           %t\n", report.Opened)
	if report.Opener != "" {
		fmt.Fprintf(out, "  Opener           %s\n", report.Opener)
	}
	fmt.Fprintf(out, "  Visit count      %d\n", report.VisitCount)
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func (a *App) Passes(args []string) error {
	req, err := parsePassesArgs(args)
	if err != nil {
		return err
	}
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	report := passesReport{
		Kind:               "passes",
		Action:             req.Action,
		Status:             "ok",
		DocsURL:            guestPassDocsURL,
		ReferralURL:        firstNonEmpty(req.ReferralURL, a.Config.Future.GuestPassReferralURL),
		ReferralConfigured: strings.TrimSpace(firstNonEmpty(req.ReferralURL, a.Config.Future.GuestPassReferralURL)) != "",
		VisitCount:         a.Config.Future.GuestPassVisitCount,
		Path:               path,
		HasVisitedPasses:   configBoolValue(a.Config.Future.HasVisitedPasses),
		UpsellSeenCount:    a.Config.Future.PassesUpsellSeenCount,
		LastSeenRemaining:  cloneIntPtr(a.Config.Future.PassesLastSeenRemaining),
	}
	report.URL, report.URLSource = passesURLWithSource(report.ReferralURL, req.Docs)
	if cached, ok := a.cachedGuestPassEligibility(req); ok {
		applyGuestPassCacheToReport(&report, cached)
		if report.ReferralURL == "" {
			report.ReferralConfigured = false
		}
		report.URL, report.URLSource = passesURLWithSource(report.ReferralURL, req.Docs)
	}
	if report.RemainingPasses != nil && a.resetPassesUpsellIfRefreshed(path, *report.RemainingPasses) {
		report.UpsellReset = true
		report.HasVisitedPasses = false
		report.UpsellSeenCount = 0
		report.LastSeenRemaining = cloneIntPtr(report.RemainingPasses)
	}
	report.UpsellVisible = guestPassUpsellVisible(report)
	if err := a.executePassesAction(req, path, &report); err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderPassesReport(a.Out, report)
	return nil
}

func (a *App) executePassesAction(req passesRequest, path string, report *passesReport) error {
	switch req.Action {
	case "status":
		report.Message = "Guest pass status loaded."
	case "fetch":
		return a.fetchPasses(req, path, report)
	case "visit":
		return a.visitPasses(path, report)
	case "upsell-seen":
		return a.recordPassesUpsell(path, report)
	case "set-url":
		return a.setPassesURL(req.ReferralURL, path, report)
	case "clear-url":
		return a.clearPassesURL(path, report)
	case "show", "open":
		return a.showPasses(req, path, report)
	default:
		return fmt.Errorf("unknown passes command %q", req.Action)
	}
	return nil
}

func (a *App) fetchPasses(req passesRequest, path string, report *passesReport) error {
	fetchCtx, cancel := context.WithTimeout(context.Background(), passesFetchTimeout(req.TimeoutMS))
	defer cancel()
	fetched, err := a.fetchGuestPasses(fetchCtx, req)
	if err != nil {
		return err
	}
	applyFetchedPasses(report, fetched, req.Docs)
	if err := a.persistFetchedPasses(req, path, fetched, report); err != nil {
		return err
	}
	if fetched.Eligible {
		report.Message = "Guest pass eligibility fetched."
	} else {
		report.Message = "Guest passes are not currently available for this organization."
	}
	report.UpsellVisible = guestPassUpsellVisible(*report)
	return nil
}

func applyFetchedPasses(report *passesReport, fetched guestPassesFetchResult, docs bool) {
	report.RequestSent = true
	report.OrganizationUUID = fetched.OrganizationUUID
	report.Campaign = fetched.Campaign
	report.Eligible = &fetched.Eligible
	report.ReferralURL = firstNonEmpty(fetched.ReferralURL, report.ReferralURL)
	report.ReferralConfigured = strings.TrimSpace(report.ReferralURL) != ""
	report.URL, report.URLSource = passesURLWithSource(report.ReferralURL, docs)
	if fetched.RemainingPasses != nil {
		report.RemainingPasses = fetched.RemainingPasses
	}
	if fetched.Limit != nil {
		report.Limit = fetched.Limit
	}
	if fetched.Redeemed != nil {
		report.Redeemed = fetched.Redeemed
	}
	if fetched.AvailablePasses != nil {
		report.AvailablePasses = fetched.AvailablePasses
	}
	applyGuestPassRewardToReport(report, fetched.ReferrerReward)
}

func (a *App) persistFetchedPasses(req passesRequest, path string, fetched guestPassesFetchResult, report *passesReport) error {
	if req.SaveURL && strings.TrimSpace(fetched.ReferralURL) != "" {
		if err := setCompatibilityValue(path, "compatibility.guest_pass_referral_url", legacyGuestPassReferralURLKey, fetched.ReferralURL); err != nil {
			return err
		}
		a.Config.Future.GuestPassReferralURL = fetched.ReferralURL
		report.SavedReferralURL = true
	}
	if req.SaveCache {
		if err := a.saveGuestPassEligibilityCache(path, fetched, time.Now().UTC()); err != nil {
			return err
		}
		report.SavedEligibilityCache = true
	}
	return nil
}

func (a *App) visitPasses(path string, report *passesReport) error {
	remaining := intPtrValue(report.RemainingPasses, intPtrValue(a.Config.Future.PassesLastSeenRemaining, 0))
	if err := a.markPassesVisited(path, remaining); err != nil {
		return err
	}
	report.HasVisitedPasses, report.MarkedVisited = true, true
	report.LastSeenRemaining = &remaining
	report.UpsellVisible = false
	report.Message = "Guest passes visit state saved."
	return nil
}

func (a *App) recordPassesUpsell(path string, report *passesReport) error {
	count, err := a.markPassesUpsellSeen(path)
	if err != nil {
		return err
	}
	report.UpsellSeenCount, report.MarkedUpsellSeen = count, true
	report.UpsellVisible = guestPassUpsellVisible(*report)
	report.Message = "Guest passes upsell impression recorded."
	return nil
}

func (a *App) setPassesURL(url, path string, report *passesReport) error {
	if err := validateHTTPURL(url, "guest pass referral URL"); err != nil {
		return err
	}
	if err := setCompatibilityValue(path, "compatibility.guest_pass_referral_url", legacyGuestPassReferralURLKey, url); err != nil {
		return err
	}
	a.Config.Future.GuestPassReferralURL = url
	report.ReferralURL, report.URL, report.URLSource = url, url, "referral"
	report.ReferralConfigured = true
	report.Message = "Guest pass referral URL saved."
	return nil
}

func (a *App) clearPassesURL(path string, report *passesReport) error {
	if err := unsetCompatibilityValue(path, "compatibility.guest_pass_referral_url", legacyGuestPassReferralURLKey); err != nil {
		return err
	}
	a.Config.Future.GuestPassReferralURL = ""
	report.ReferralURL, report.URL, report.URLSource = "", guestPassDocsURL, "docs"
	report.ReferralConfigured = false
	report.Message = "Guest pass referral URL cleared."
	return nil
}

func (a *App) showPasses(req passesRequest, path string, report *passesReport) error {
	count := a.Config.Future.GuestPassVisitCount + 1
	if err := setCompatibilityValue(path, "compatibility.guest_pass_visit_count", legacyGuestPassVisitCountKey, count); err != nil {
		return err
	}
	a.Config.Future.GuestPassVisitCount = count
	report.VisitCount = count
	report.URL, report.URLSource = passesURLWithSource(report.ReferralURL, req.Docs)
	setPassesShowMessage(req, report)
	if req.Action == "show" || !req.Open {
		report.Action = "show"
		return nil
	}
	opener, err := openExternalURL(report.URL)
	if err != nil {
		report.Status = "open_failed"
		report.Message = "Could not open a browser automatically. Visit the URL manually."
		return nil
	}
	report.Opened, report.Opener = true, opener
	report.Message = "Opening guest pass page in browser."
	return nil
}

func setPassesShowMessage(req passesRequest, report *passesReport) {
	if report.ReferralURL == "" || req.Docs {
		report.Message = "No guest pass referral URL is configured. Showing Claude Code guest pass documentation."
	} else {
		report.Message = "Showing configured guest pass referral URL."
	}
}

func parsePassesArgs(args []string) (passesRequest, error) {
	req := passesRequest{Action: "open", Format: "text", Target: "user", Open: true, BaseURL: "https://api.anthropic.com", Campaign: "claude_code_guest_pass", SaveURL: true, SaveCache: true, TimeoutMS: 5000}
	rest, err := parsePassesOptions(args, &req)
	if err != nil {
		return req, err
	}
	normalizedFormat, err := normalizeOutputFormat("passes", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return applyPassesAction(req, rest)
}

func parsePassesOptions(args []string, req *passesRequest) ([]string, error) {
	options := passesValueOptions(req)
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--json":
			req.Format = "json"
			continue
		case "--open":
			req.Open = true
			continue
		case "--no-open":
			req.Open = false
			continue
		case "--docs":
			req.Docs = true
			continue
		case "--redemptions":
			req.Redemptions = true
			continue
		case "--save-url":
			req.SaveURL = true
			continue
		case "--no-save-url":
			req.SaveURL = false
			continue
		case "--save-cache":
			req.SaveCache = true
			continue
		case "--no-save-cache":
			req.SaveCache = false
			continue
		}
		handled, err := consumeValueOption(args, &index, options)
		if err != nil {
			return nil, err
		}
		if !handled {
			rest = append(rest, arg)
		}
	}
	return rest, nil
}

func passesValueOptions(req *passesRequest) map[string]valueOption {
	options := map[string]valueOption{}
	addPassesStringOption(options, "--output-format", &req.Format, false)
	options["-o"] = options["--output-format"]
	addPassesStringOption(options, "--target", &req.Target, false)
	addPassesStringOption(options, "--path", &req.Path, false)
	addPassesStringOption(options, "--referral-url", &req.ReferralURL, false)
	addPassesStringOption(options, "--org", &req.OrgUUID, true)
	options["--organization"] = options["--org"]
	options["--organization-uuid"] = options["--org"]
	addPassesStringOption(options, "--token", &req.Token, true)
	options["--auth-token"] = options["--token"]
	addPassesStringOption(options, "--base-url", &req.BaseURL, true)
	addPassesStringOption(options, "--campaign", &req.Campaign, true)
	options["--timeout-ms"] = valueOption{
		missing:            passesMissingValue,
		rejectOutputFormat: true,
		set:                func(value string) error { return setPassesTimeout(req, value) },
	}
	return options
}

func addPassesStringOption(options map[string]valueOption, name string, target *string, trim bool) {
	options[name] = valueOption{
		missing:            passesMissingValue,
		rejectOutputFormat: name != "--output-format",
		set: func(value string) error {
			if trim {
				value = strings.TrimSpace(value)
			}
			*target = value
			return nil
		},
	}
}

func passesMissingValue(flag string) error {
	return missingFlagValueError{Command: "passes", Flag: flag, Usage: passesUsage}
}

func setPassesTimeout(req *passesRequest, raw string) error {
	timeoutMS, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("passes timeout-ms must be an integer: %w", err)
	}
	req.TimeoutMS = timeoutMS
	return nil
}

func applyPassesAction(req passesRequest, rest []string) (passesRequest, error) {
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "status", "list", "ls":
		req.Action = "status"
		req.Open = false
	case "show":
		req.Action = "show"
		req.Open = false
	case "open":
		req.Action = "open"
	case "fetch", "refresh", "sync":
		req.Action = "fetch"
		req.Open = false
	case "visit", "visited", "mark-visited":
		req.Action = "visit"
		req.Open = false
	case "upsell-seen", "seen-upsell", "mark-upsell-seen":
		req.Action = "upsell-seen"
		req.Open = false
	case "set-url", "set", "url":
		req.Action = "set-url"
		if req.ReferralURL == "" && len(rest) > 1 {
			req.ReferralURL = rest[1]
		}
		if req.ReferralURL == "" {
			return req, requiredArgumentError{Command: "passes set-url", Argument: "URL", Usage: passesUsage}
		}
	case "clear-url", "clear", "unset":
		req.Action = "clear-url"
	default:
		if strings.HasPrefix(rest[0], "-") {
			return req, unknownOptionError{Command: "passes", Option: rest[0], Usage: passesUsage}
		}
		return req, unexpectedExtraArgsError{Command: "passes", Args: []string{rest[0]}, Usage: passesUsage}
	}
	if (req.Action == "show" || req.Action == "open" || req.Action == "fetch" || req.Action == "visit" || req.Action == "upsell-seen" || req.Action == "clear-url") && len(rest) > 1 {
		return req, unexpectedExtraArgsError{Command: "passes " + req.Action, Args: rest[1:], Usage: passesUsage}
	}
	if req.Action == "set-url" && len(rest) > 2 {
		return req, unexpectedExtraArgsError{Command: "passes set-url", Args: rest[2:], Usage: passesUsage}
	}
	return req, nil
}

type guestPassesFetchResult struct {
	OrganizationUUID string
	Campaign         string
	Eligible         bool
	ReferralURL      string
	RemainingPasses  *int
	Limit            *int
	Redeemed         *int
	AvailablePasses  *int
	ReferrerReward   *config.MoneyInfo
}

func (a *App) cachedGuestPassEligibility(req passesRequest) (config.GuestPassEligibilityCacheEntry, bool) {
	cache := a.Config.Future.GuestPassEligibilityCache
	if len(cache) == 0 {
		return config.GuestPassEligibilityCacheEntry{}, false
	}
	orgUUID := strings.TrimSpace(firstNonEmpty(req.OrgUUID, a.Config.ForceLoginOrgUUID))
	if orgUUID != "" {
		entry, ok := cache[orgUUID]
		return entry, ok
	}
	if len(cache) == 1 {
		for _, entry := range cache {
			return entry, true
		}
	}
	return config.GuestPassEligibilityCacheEntry{}, false
}

func applyGuestPassCacheToReport(report *passesReport, entry config.GuestPassEligibilityCacheEntry) {
	report.CacheHit = true
	if !entry.Timestamp.IsZero() {
		report.CachedAt = entry.Timestamp.UTC().Format(time.RFC3339)
	}
	report.Eligible = &entry.Eligible
	if entry.Campaign != "" {
		report.Campaign = entry.Campaign
	}
	if entry.ReferralURL != "" {
		report.ReferralURL = entry.ReferralURL
		report.ReferralConfigured = true
	}
	report.RemainingPasses = cloneIntPtr(entry.RemainingPasses)
	report.Limit = cloneIntPtr(entry.Limit)
	report.Redeemed = cloneIntPtr(entry.Redeemed)
	report.AvailablePasses = cloneIntPtr(entry.AvailablePasses)
	applyGuestPassRewardToReport(report, entry.ReferrerReward)
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneMoneyInfoPtr(value *config.MoneyInfo) *config.MoneyInfo {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func applyGuestPassRewardToReport(report *passesReport, reward *config.MoneyInfo) {
	report.ReferrerReward = cloneMoneyInfoPtr(reward)
	report.ReferrerRewardFormatted = formatGuestPassReward(reward)
}

func formatGuestPassReward(reward *config.MoneyInfo) string {
	if reward == nil {
		return ""
	}
	currency := strings.ToUpper(strings.TrimSpace(reward.Currency))
	symbols := map[string]string{
		"USD": "$",
		"EUR": "\u20ac",
		"GBP": "\u00a3",
		"BRL": "R$",
		"CAD": "CA$",
		"AUD": "A$",
		"NZD": "NZ$",
		"SGD": "S$",
	}
	symbol := symbols[currency]
	if symbol == "" {
		if currency == "" {
			symbol = ""
		} else {
			symbol = currency + " "
		}
	}
	major := float64(reward.AmountMinorUnits) / 100
	if reward.AmountMinorUnits%100 == 0 {
		return fmt.Sprintf("%s%d", symbol, reward.AmountMinorUnits/100)
	}
	return fmt.Sprintf("%s%.2f", symbol, major)
}

func configBoolValue(value *bool) bool {
	return value != nil && *value
}

func intPtrValue(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func guestPassUpsellVisible(report passesReport) bool {
	if !report.CacheHit || report.Eligible == nil || !*report.Eligible {
		return false
	}
	if report.RemainingPasses != nil && *report.RemainingPasses <= 0 {
		return false
	}
	if report.UpsellSeenCount >= 3 || report.HasVisitedPasses {
		return false
	}
	return true
}

func (a *App) resetPassesUpsellIfRefreshed(path string, remaining int) bool {
	if remaining <= 0 {
		return false
	}
	lastSeen := intPtrValue(a.Config.Future.PassesLastSeenRemaining, 0)
	if remaining <= lastSeen {
		return false
	}
	visited := false
	if err := setCompatibilityValue(path, "compatibility.has_visited_passes", legacyHasVisitedPassesKey, visited); err != nil {
		return false
	}
	if err := setCompatibilityValue(path, "compatibility.passes_upsell_seen_count", legacyPassesUpsellSeenCountKey, 0); err != nil {
		return false
	}
	if err := setCompatibilityValue(path, "compatibility.passes_last_seen_remaining", legacyPassesLastSeenRemainingKey, remaining); err != nil {
		return false
	}
	a.Config.Future.HasVisitedPasses = &visited
	a.Config.Future.PassesUpsellSeenCount = 0
	a.Config.Future.PassesLastSeenRemaining = cloneIntPtr(&remaining)
	return true
}

func (a *App) markPassesVisited(path string, remaining int) error {
	visited := true
	if err := setCompatibilityValue(path, "compatibility.has_visited_passes", legacyHasVisitedPassesKey, visited); err != nil {
		return err
	}
	if err := setCompatibilityValue(path, "compatibility.passes_last_seen_remaining", legacyPassesLastSeenRemainingKey, remaining); err != nil {
		return err
	}
	a.Config.Future.HasVisitedPasses = &visited
	a.Config.Future.PassesLastSeenRemaining = cloneIntPtr(&remaining)
	return nil
}

func (a *App) markPassesUpsellSeen(path string) (int, error) {
	count := a.Config.Future.PassesUpsellSeenCount + 1
	if err := setCompatibilityValue(path, "compatibility.passes_upsell_seen_count", legacyPassesUpsellSeenCountKey, count); err != nil {
		return 0, err
	}
	a.Config.Future.PassesUpsellSeenCount = count
	return count, nil
}

func (a *App) saveGuestPassEligibilityCache(path string, fetched guestPassesFetchResult, now time.Time) error {
	if strings.TrimSpace(fetched.OrganizationUUID) == "" {
		return nil
	}
	cache := map[string]config.GuestPassEligibilityCacheEntry{}
	for org, entry := range a.Config.Future.GuestPassEligibilityCache {
		cache[org] = entry
	}
	cache[fetched.OrganizationUUID] = config.GuestPassEligibilityCacheEntry{
		Eligible:        fetched.Eligible,
		Timestamp:       now.UTC(),
		Campaign:        fetched.Campaign,
		ReferralURL:     fetched.ReferralURL,
		RemainingPasses: cloneIntPtr(fetched.RemainingPasses),
		Limit:           cloneIntPtr(fetched.Limit),
		Redeemed:        cloneIntPtr(fetched.Redeemed),
		AvailablePasses: cloneIntPtr(fetched.AvailablePasses),
		ReferrerReward:  cloneMoneyInfoPtr(fetched.ReferrerReward),
	}
	if err := setCompatibilityValue(path, "compatibility.guest_pass_eligibility_cache", "future.guest_pass_eligibility_cache", cache); err != nil {
		return err
	}
	a.Config.Future.GuestPassEligibilityCache = cache
	return nil
}

type guestPassesEligibilityResponse struct {
	Eligible            bool              `json:"eligible"`
	RemainingPasses     *int              `json:"remaining_passes"`
	ReferrerReward      *config.MoneyInfo `json:"referrer_reward"`
	ReferralCodeDetails *struct {
		ReferralLink string `json:"referral_link"`
		Campaign     string `json:"campaign"`
	} `json:"referral_code_details"`
}

type guestPassesRedemptionsResponse struct {
	Redemptions []json.RawMessage `json:"redemptions"`
	Limit       int               `json:"limit"`
}

func (a *App) fetchGuestPasses(ctx context.Context, req passesRequest) (guestPassesFetchResult, error) {
	orgUUID := strings.TrimSpace(firstNonEmpty(req.OrgUUID, a.Config.ForceLoginOrgUUID))
	if orgUUID == "" {
		return guestPassesFetchResult{}, requiredArgumentError{Command: "passes fetch", Argument: "--org UUID", Usage: passesUsage}
	}
	token := strings.TrimSpace(firstNonEmpty(req.Token, a.Config.AuthToken))
	if token == "" && strings.TrimSpace(a.Config.ConfigHome) != "" {
		if stored, err := oauth.LoadToken(a.Config.ConfigHome); err == nil {
			token = strings.TrimSpace(stored.AccessToken)
		}
	}
	if token == "" {
		return guestPassesFetchResult{}, requiredArgumentError{Command: "passes fetch", Argument: "--token TOKEN", Usage: passesUsage}
	}
	baseURL, err := normalizedPassesBaseURL(req.BaseURL)
	if err != nil {
		return guestPassesFetchResult{}, err
	}
	campaign := strings.TrimSpace(req.Campaign)
	if campaign == "" {
		campaign = "claude_code_guest_pass"
	}
	eligibilityURL := baseURL + "/api/oauth/organizations/" + url.PathEscape(orgUUID) + "/referral/eligibility"
	var eligibility guestPassesEligibilityResponse
	if err := fetchGuestPassesJSON(ctx, eligibilityURL, token, orgUUID, campaign, &eligibility); err != nil {
		return guestPassesFetchResult{}, err
	}
	if eligibility.ReferralCodeDetails != nil && strings.TrimSpace(eligibility.ReferralCodeDetails.Campaign) != "" {
		campaign = strings.TrimSpace(eligibility.ReferralCodeDetails.Campaign)
	}
	result := guestPassesFetchResult{
		OrganizationUUID: orgUUID,
		Campaign:         campaign,
		Eligible:         eligibility.Eligible,
		RemainingPasses:  eligibility.RemainingPasses,
		ReferrerReward:   cloneMoneyInfoPtr(eligibility.ReferrerReward),
	}
	if eligibility.ReferralCodeDetails != nil {
		result.ReferralURL = strings.TrimSpace(eligibility.ReferralCodeDetails.ReferralLink)
	}
	if req.Redemptions {
		redemptionsURL := baseURL + "/api/oauth/organizations/" + url.PathEscape(orgUUID) + "/referral/redemptions"
		var redemptions guestPassesRedemptionsResponse
		if err := fetchGuestPassesJSON(ctx, redemptionsURL, token, orgUUID, campaign, &redemptions); err != nil {
			return guestPassesFetchResult{}, err
		}
		limit := redemptions.Limit
		if limit == 0 {
			limit = 3
		}
		redeemed := len(redemptions.Redemptions)
		available := limit - redeemed
		if available < 0 {
			available = 0
		}
		result.Limit = &limit
		result.Redeemed = &redeemed
		result.AvailablePasses = &available
	}
	return result, nil
}

func normalizedPassesBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "https://api.anthropic.com"
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("passes base URL must be a valid URL")
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return "", errors.New("passes base URL must use http or https")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func passesFetchTimeout(timeoutMS int) time.Duration {
	if timeoutMS <= 0 {
		timeoutMS = 5000
	}
	return time.Duration(timeoutMS) * time.Millisecond
}

func fetchGuestPassesJSON(ctx context.Context, endpoint string, token string, orgUUID string, campaign string, target any) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	query := parsed.Query()
	query.Set("campaign", campaign)
	parsed.RawQuery = query.Encode()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("x-organization-uuid", orgUUID)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("passes API request failed: %s", resp.Status)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return err
	}
	return nil
}

func passesURLWithSource(referralURL string, docs bool) (string, string) {
	if docs || strings.TrimSpace(referralURL) == "" {
		return guestPassDocsURL, "docs"
	}
	return referralURL, "referral"
}

func validateHTTPURL(raw string, label string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must be a valid URL", label)
	}
	switch parsed.Scheme {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("%s must use http or https", label)
	}
}

func renderPassesReport(out io.Writer, report passesReport) {
	fmt.Fprintln(out, "Guest Passes")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	fmt.Fprintf(out, "  URL              %s\n", report.URL)
	if report.URLSource != "" {
		fmt.Fprintf(out, "  URL source       %s\n", report.URLSource)
	}
	fmt.Fprintf(out, "  Docs             %s\n", report.DocsURL)
	fmt.Fprintf(out, "  Referral set     %t\n", report.ReferralConfigured)
	if report.ReferralURL != "" {
		fmt.Fprintf(out, "  Referral URL     %s\n", report.ReferralURL)
	}
	if report.OrganizationUUID != "" {
		fmt.Fprintf(out, "  Organization     %s\n", report.OrganizationUUID)
	}
	if report.Campaign != "" {
		fmt.Fprintf(out, "  Campaign         %s\n", report.Campaign)
	}
	if report.Eligible != nil {
		fmt.Fprintf(out, "  Eligible         %t\n", *report.Eligible)
	}
	if report.RemainingPasses != nil {
		fmt.Fprintf(out, "  Remaining        %d\n", *report.RemainingPasses)
	}
	if report.Limit != nil {
		fmt.Fprintf(out, "  Limit            %d\n", *report.Limit)
	}
	if report.Redeemed != nil {
		fmt.Fprintf(out, "  Redeemed         %d\n", *report.Redeemed)
	}
	if report.AvailablePasses != nil {
		fmt.Fprintf(out, "  Available        %d\n", *report.AvailablePasses)
	}
	if report.ReferrerRewardFormatted != "" {
		fmt.Fprintf(out, "  Referrer reward  %s\n", report.ReferrerRewardFormatted)
	}
	if report.SavedReferralURL {
		fmt.Fprintln(out, "  Saved referral   true")
	}
	if report.CacheHit {
		fmt.Fprintln(out, "  Cache hit        true")
	}
	if report.CachedAt != "" {
		fmt.Fprintf(out, "  Cached at        %s\n", report.CachedAt)
	}
	if report.SavedEligibilityCache {
		fmt.Fprintln(out, "  Saved cache      true")
	}
	fmt.Fprintf(out, "  Visited          %t\n", report.HasVisitedPasses)
	fmt.Fprintf(out, "  Upsell seen      %d\n", report.UpsellSeenCount)
	if report.LastSeenRemaining != nil {
		fmt.Fprintf(out, "  Last seen left   %d\n", *report.LastSeenRemaining)
	}
	fmt.Fprintf(out, "  Upsell visible   %t\n", report.UpsellVisible)
	if report.UpsellReset {
		fmt.Fprintln(out, "  Upsell reset     true")
	}
	if report.MarkedVisited {
		fmt.Fprintln(out, "  Marked visited   true")
	}
	if report.MarkedUpsellSeen {
		fmt.Fprintln(out, "  Marked upsell    true")
	}
	fmt.Fprintf(out, "  Opened           %t\n", report.Opened)
	if report.Opener != "" {
		fmt.Fprintf(out, "  Opener           %s\n", report.Opener)
	}
	if report.VisitCount != 0 {
		fmt.Fprintf(out, "  Visit count      %d\n", report.VisitCount)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func openSystemURL(url string) (string, error) {
	var command []string
	switch runtime.GOOS {
	case "darwin":
		command = []string{"open", url}
	case "windows":
		command = []string{"rundll32", "url.dll,FileProtocolHandler", url}
	default:
		command = []string{"xdg-open", url}
	}
	if _, err := exec.LookPath(command[0]); err != nil {
		return "", err
	}
	cmd := exec.Command(command[0], command[1:]...)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	return command[0], nil
}

func (a *App) IssueDraft(args []string, overrides config.FlagOverrides) error {
	return a.writeDraft("issue", args, overrides)
}

func (a *App) writeDraft(kind string, args []string, overrides config.FlagOverrides) error {
	req, err := parseDraftArgs(kind, args, overrides)
	if err != nil {
		return err
	}
	active, err := a.feedbackSession(req.SessionID)
	if err != nil {
		return err
	}
	createdAt := time.Now().UTC()
	bundle := draftBundle{
		Kind:       kind,
		CreatedAt:  createdAt,
		Context:    strings.TrimSpace(req.Context),
		Status:     a.statusSnapshot(active),
		GitStatus:  boundedGitOutput(a.Workspace, 12000, "status", "--short", "--branch"),
		DiffStat:   boundedGitOutput(a.Workspace, 12000, "diff", "--stat"),
		StagedStat: boundedGitOutput(a.Workspace, 12000, "diff", "--cached", "--stat"),
		RecentLog:  boundedGitOutput(a.Workspace, 12000, "log", "--oneline", "--decorate", "--max-count=12"),
		Remote:     boundedGitOutput(a.Workspace, 2000, "remote", "get-url", "origin"),
	}
	if state, err := gitops.PreserveStateForIssue(a.Workspace); err == nil {
		bundle.GitState = state
	}
	bundle.Title = draftTitle(kind, bundle)
	path := a.draftOutputPath(kind, req.Output, createdAt)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := session.ValidateExportOutputPath(path); err != nil {
		return err
	}
	data := []byte(renderDraftMarkdown(bundle))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	report := draftReport{
		Kind:            kind,
		Action:          "draft",
		Status:          "ok",
		File:            path,
		Bytes:           len(data),
		Title:           bundle.Title,
		Context:         bundle.Context,
		Branch:          bundle.Status.Git.Branch,
		GitClean:        bundle.Status.Git.Clean,
		SessionID:       bundle.Status.Session.ID,
		SessionMessages: bundle.Status.Session.MessageCount,
		GitState:        draftGitStateSummary(bundle.GitState),
	}
	if req.Format == "json" {
		encoded, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(encoded))
		return nil
	}
	renderDraftReport(a.Out, report)
	return nil
}

func parseDraftArgs(kind string, args []string, overrides config.FlagOverrides) (draftRequest, error) {
	req := draftRequest{Format: "text"}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
	}
	if overrides.SessionID != "" {
		req.SessionID = overrides.SessionID
	}
	var contextParts []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("%s output format is required", kind)
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--output":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("%s output path is required", kind)
			}
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("%s session id is required", kind)
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("%s resume session id is required", kind)
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case arg == "--context" || arg == "--message":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("%s context is required", kind)
			}
			contextParts = append(contextParts, args[index])
		case strings.HasPrefix(arg, "--context="):
			contextParts = append(contextParts, strings.TrimPrefix(arg, "--context="))
		case strings.HasPrefix(arg, "--message="):
			contextParts = append(contextParts, strings.TrimPrefix(arg, "--message="))
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown %s flag %q", kind, arg)
		default:
			contextParts = append(contextParts, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, kind); err != nil {
		return req, err
	}
	req.Context = strings.TrimSpace(strings.Join(contextParts, " "))
	return req, nil
}

func (a *App) draftOutputPath(kind, output string, createdAt time.Time) string {
	filename := fmt.Sprintf("%s-%s-%d.md", kind, createdAt.Format("20060102T150405Z"), createdAt.UnixNano())
	if strings.TrimSpace(output) == "" {
		return filepath.Join(a.Workspace, ".codog", "drafts", filename)
	}
	path := a.resolveOutputPath(output)
	if strings.EqualFold(filepath.Ext(path), ".md") {
		return path
	}
	return filepath.Join(path, filename)
}

func draftTitle(kind string, bundle draftBundle) string {
	context := prompthistory.Preview(bundle.Context, 72)
	if context != "" {
		if kind == "issue" {
			return "Issue: " + context
		}
		return "PR: " + context
	}
	if kind == "issue" {
		return "Issue: " + emptyAs(bundle.Status.Workspace.Name, "workspace follow-up")
	}
	branch := emptyAs(bundle.Status.Git.Branch, "workspace changes")
	return "PR: " + branch
}

func renderDraftReport(out io.Writer, report draftReport) {
	label := "Pull Request Draft"
	if report.Kind == "issue" {
		label = "Issue Draft"
	}
	fmt.Fprintln(out, label)
	fmt.Fprintf(out, "  File             %s\n", report.File)
	fmt.Fprintf(out, "  Title            %s\n", report.Title)
	fmt.Fprintf(out, "  Bytes            %d\n", report.Bytes)
	if report.Branch != "" {
		fmt.Fprintf(out, "  Branch           %s\n", report.Branch)
	}
	if report.SessionID != "" {
		fmt.Fprintf(out, "  Session          %s (%d messages)\n", report.SessionID, report.SessionMessages)
	}
	if report.GitState != nil {
		fmt.Fprintf(out, "  Git state        branch=%s remote_base=%s patch_bytes=%d untracked=%d\n",
			emptyAs(report.GitState.BranchName, "detached"),
			emptyAs(report.GitState.RemoteBase, "none"),
			report.GitState.PatchBytes,
			report.GitState.UntrackedFiles,
		)
	}
}

func renderDraftMarkdown(bundle draftBundle) string {
	label := "Pull Request Draft"
	if bundle.Kind == "issue" {
		label = "Issue Draft"
	}
	var builder strings.Builder
	builder.WriteString("# " + label + "\n\n")
	builder.WriteString("## Title\n\n")
	builder.WriteString(bundle.Title + "\n\n")
	builder.WriteString("## Context\n\n")
	if bundle.Context == "" {
		builder.WriteString("No additional context provided.\n\n")
	} else {
		builder.WriteString(bundle.Context + "\n\n")
	}
	builder.WriteString("## Workspace\n\n")
	builder.WriteString(fmt.Sprintf("- Created: %s\n", bundle.CreatedAt.Format(time.RFC3339)))
	builder.WriteString(fmt.Sprintf("- Workspace: %s\n", bundle.Status.Workspace.Path))
	builder.WriteString(fmt.Sprintf("- Branch: %s\n", emptyAs(bundle.Status.Git.Branch, "unknown")))
	builder.WriteString(fmt.Sprintf("- Git clean: %t\n", bundle.Status.Git.Clean))
	if bundle.Remote != "" {
		builder.WriteString(fmt.Sprintf("- Origin: %s\n", bundle.Remote))
	}
	if bundle.Status.Session.Active {
		builder.WriteString(fmt.Sprintf("- Session: %s (%d messages)\n", bundle.Status.Session.ID, bundle.Status.Session.MessageCount))
	}
	builder.WriteString("\n## Current Git Status\n\n")
	writeDraftCodeBlock(&builder, bundle.GitStatus)
	builder.WriteString("\n## Unstaged Diff Stat\n\n")
	writeDraftCodeBlock(&builder, emptyAs(bundle.DiffStat, "No unstaged changes."))
	builder.WriteString("\n## Staged Diff Stat\n\n")
	writeDraftCodeBlock(&builder, emptyAs(bundle.StagedStat, "No staged changes."))
	builder.WriteString("\n## Recent Commits\n\n")
	writeDraftCodeBlock(&builder, bundle.RecentLog)
	if bundle.GitState != nil {
		builder.WriteString("\n## Preserved Git State\n\n")
		builder.WriteString(fmt.Sprintf("- Remote base: %s\n", emptyAs(bundle.GitState.RemoteBase, "none")))
		builder.WriteString(fmt.Sprintf("- Remote base SHA: %s\n", emptyAs(bundle.GitState.RemoteBaseSHA, "none")))
		builder.WriteString(fmt.Sprintf("- HEAD SHA: %s\n", emptyAs(bundle.GitState.HeadSHA, "unknown")))
		builder.WriteString(fmt.Sprintf("- Branch: %s\n", emptyAs(bundle.GitState.BranchName, "detached")))
		builder.WriteString(fmt.Sprintf("- Patch bytes: %d\n", len(bundle.GitState.Patch)))
		builder.WriteString(fmt.Sprintf("- Untracked files: %d\n", len(bundle.GitState.UntrackedFiles)))
		builder.WriteString(fmt.Sprintf("- Format patch: %t\n", strings.TrimSpace(bundle.GitState.FormatPatch) != ""))
		builder.WriteString("\n### Patch\n\n")
		writeDraftCodeBlock(&builder, boundedString(bundle.GitState.Patch, 20000))
		if strings.TrimSpace(bundle.GitState.FormatPatch) != "" {
			builder.WriteString("\n### Format Patch\n\n")
			writeDraftCodeBlock(&builder, boundedString(bundle.GitState.FormatPatch, 20000))
		}
		if len(bundle.GitState.UntrackedFiles) != 0 {
			builder.WriteString("\n### Untracked Files\n\n")
			for _, file := range bundle.GitState.UntrackedFiles {
				builder.WriteString(fmt.Sprintf("#### %s\n\n", file.Path))
				writeDraftCodeBlock(&builder, boundedString(file.Content, 12000))
				builder.WriteString("\n")
			}
		}
	}
	if bundle.Kind == "pr" {
		builder.WriteString("\n## Checklist\n\n")
		builder.WriteString("- [ ] Tests pass\n")
		builder.WriteString("- [ ] Documentation updated if needed\n")
		builder.WriteString("- [ ] Review risk noted\n")
	} else {
		builder.WriteString("\n## Expected Follow-Up\n\n")
		builder.WriteString("- [ ] Reproduce or confirm the issue\n")
		builder.WriteString("- [ ] Identify affected versions or environments\n")
		builder.WriteString("- [ ] Attach logs, screenshots, or session details if useful\n")
	}
	return builder.String()
}
