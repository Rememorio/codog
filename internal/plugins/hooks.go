package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Rememorio/codog/internal/argsub"
	"github.com/Rememorio/codog/internal/config"
)

type HookConfigFile struct {
	PluginID string            `json:"plugin_id"`
	Path     string            `json:"path"`
	Config   config.HookConfig `json:"config"`
}

func LoadHookConfigs(workspace string) ([]HookConfigFile, error) {
	manifests, err := Load(workspace)
	if err != nil {
		return nil, err
	}
	var out []HookConfigFile
	for _, manifest := range manifests {
		if !manifest.Enabled {
			continue
		}
		for _, path := range hookConfigPaths(manifest) {
			data, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			var cfg config.HookConfig
			if err := json.Unmarshal(data, &cfg); err != nil {
				return nil, fmt.Errorf("plugin %q hook config %s: %w", manifest.ID, path, err)
			}
			cfg = resolvePluginHookConfig(manifest, cfg)
			out = append(out, HookConfigFile{PluginID: manifest.ID, Path: path, Config: cfg})
		}
	}
	return out, nil
}

func resolvePluginHookConfig(manifest Manifest, cfg config.HookConfig) config.HookConfig {
	variables := pluginVariables(manifest)
	cfg.PreToolUseCommands = resolvePluginHookCommands(cfg.PreToolUseCommands, variables)
	cfg.PostToolUseCommands = resolvePluginHookCommands(cfg.PostToolUseCommands, variables)
	cfg.PostToolUseFailureCommands = resolvePluginHookCommands(cfg.PostToolUseFailureCommands, variables)
	cfg.PermissionRequestCommands = resolvePluginHookCommands(cfg.PermissionRequestCommands, variables)
	cfg.PermissionDeniedCommands = resolvePluginHookCommands(cfg.PermissionDeniedCommands, variables)
	cfg.UserPromptSubmitCommands = resolvePluginHookCommands(cfg.UserPromptSubmitCommands, variables)
	cfg.SessionStartCommands = resolvePluginHookCommands(cfg.SessionStartCommands, variables)
	cfg.SessionEndCommands = resolvePluginHookCommands(cfg.SessionEndCommands, variables)
	cfg.SetupCommands = resolvePluginHookCommands(cfg.SetupCommands, variables)
	cfg.StopCommands = resolvePluginHookCommands(cfg.StopCommands, variables)
	cfg.StopFailureCommands = resolvePluginHookCommands(cfg.StopFailureCommands, variables)
	cfg.PreCompactCommands = resolvePluginHookCommands(cfg.PreCompactCommands, variables)
	cfg.PostCompactCommands = resolvePluginHookCommands(cfg.PostCompactCommands, variables)
	cfg.NotificationCommands = resolvePluginHookCommands(cfg.NotificationCommands, variables)
	cfg.SubagentStartCommands = resolvePluginHookCommands(cfg.SubagentStartCommands, variables)
	cfg.SubagentStopCommands = resolvePluginHookCommands(cfg.SubagentStopCommands, variables)
	cfg.WorktreeCreateCommands = resolvePluginHookCommands(cfg.WorktreeCreateCommands, variables)
	cfg.WorktreeRemoveCommands = resolvePluginHookCommands(cfg.WorktreeRemoveCommands, variables)
	cfg.CwdChangedCommands = resolvePluginHookCommands(cfg.CwdChangedCommands, variables)
	cfg.TaskCreatedCommands = resolvePluginHookCommands(cfg.TaskCreatedCommands, variables)
	cfg.TaskCompletedCommands = resolvePluginHookCommands(cfg.TaskCompletedCommands, variables)
	cfg.InstructionsLoadedCommands = resolvePluginHookCommands(cfg.InstructionsLoadedCommands, variables)
	cfg.FileChangedCommands = resolvePluginHookCommands(cfg.FileChangedCommands, variables)
	syncPluginHookDisplays(&cfg)
	return cfg
}

func resolvePluginHookCommands(commands []config.HookCommand, variables map[string]string) []config.HookCommand {
	if len(commands) == 0 {
		return nil
	}
	out := make([]config.HookCommand, len(commands))
	for i, command := range commands {
		command.Matcher = argsub.SubstituteVariables(command.Matcher, variables)
		command.Command = argsub.SubstituteVariables(command.Command, variables)
		command.URL = argsub.SubstituteVariables(command.URL, variables)
		command.Prompt = argsub.SubstituteVariables(command.Prompt, variables)
		command.Model = argsub.SubstituteVariables(command.Model, variables)
		command.If = argsub.SubstituteVariables(command.If, variables)
		command.Shell = argsub.SubstituteVariables(command.Shell, variables)
		command.StatusMessage = argsub.SubstituteVariables(command.StatusMessage, variables)
		command.AllowedEnvVars = argsub.SubstituteVariablesInList(command.AllowedEnvVars, variables)
		command.Headers = resolvePluginHookHeaders(command.Headers, variables)
		out[i] = command
	}
	return out
}

func resolvePluginHookHeaders(headers map[string]string, variables map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for key, value := range headers {
		out[argsub.SubstituteVariables(key, variables)] = argsub.SubstituteVariables(value, variables)
	}
	return out
}

func syncPluginHookDisplays(cfg *config.HookConfig) {
	cfg.PreToolUse = hookDisplays(cfg.PreToolUseCommands)
	cfg.PostToolUse = hookDisplays(cfg.PostToolUseCommands)
	cfg.PostToolUseFailure = hookDisplays(cfg.PostToolUseFailureCommands)
	cfg.PermissionRequest = hookDisplays(cfg.PermissionRequestCommands)
	cfg.PermissionDenied = hookDisplays(cfg.PermissionDeniedCommands)
	cfg.UserPromptSubmit = hookDisplays(cfg.UserPromptSubmitCommands)
	cfg.SessionStart = hookDisplays(cfg.SessionStartCommands)
	cfg.SessionEnd = hookDisplays(cfg.SessionEndCommands)
	cfg.Setup = hookDisplays(cfg.SetupCommands)
	cfg.Stop = hookDisplays(cfg.StopCommands)
	cfg.StopFailure = hookDisplays(cfg.StopFailureCommands)
	cfg.PreCompact = hookDisplays(cfg.PreCompactCommands)
	cfg.PostCompact = hookDisplays(cfg.PostCompactCommands)
	cfg.Notification = hookDisplays(cfg.NotificationCommands)
	cfg.SubagentStart = hookDisplays(cfg.SubagentStartCommands)
	cfg.SubagentStop = hookDisplays(cfg.SubagentStopCommands)
	cfg.WorktreeCreate = hookDisplays(cfg.WorktreeCreateCommands)
	cfg.WorktreeRemove = hookDisplays(cfg.WorktreeRemoveCommands)
	cfg.CwdChanged = hookDisplays(cfg.CwdChangedCommands)
	cfg.TaskCreated = hookDisplays(cfg.TaskCreatedCommands)
	cfg.TaskCompleted = hookDisplays(cfg.TaskCompletedCommands)
	cfg.InstructionsLoaded = hookDisplays(cfg.InstructionsLoadedCommands)
	cfg.FileChanged = hookDisplays(cfg.FileChangedCommands)
}

func hookDisplays(commands []config.HookCommand) []string {
	out := make([]string, 0, len(commands))
	for _, command := range commands {
		if display := config.HookCommandDisplay(command); display != "" {
			out = append(out, display)
		}
	}
	return out
}

func hookConfigPaths(manifest Manifest) []string {
	paths := []string{filepath.Join(manifest.Root, "hooks", "hooks.json")}
	seen := map[string]bool{filepath.Clean(paths[0]): true}
	for _, spec := range manifest.Hooks {
		path, err := ResolveContentPath(manifest.Root, spec)
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.IsDir() {
			path = filepath.Join(path, "hooks.json")
		}
		if !filepath.IsAbs(path) {
			path = filepath.Clean(path)
		}
		key := filepath.Clean(path)
		if seen[key] {
			continue
		}
		seen[key] = true
		paths = append(paths, path)
	}
	return paths
}
