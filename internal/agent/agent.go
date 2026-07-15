package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/bridge"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/control"
	"github.com/Rememorio/codog/internal/customcommands"
	"github.com/Rememorio/codog/internal/hooks"
	"github.com/Rememorio/codog/internal/memory"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/pathscope"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/projectinit"
	remoteruntime "github.com/Rememorio/codog/internal/remote"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/skills"
	"github.com/Rememorio/codog/internal/toolnames"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/trustresolver"
	"github.com/Rememorio/codog/internal/tui"
)

const version = "0.1.1"
const maxSystemGitStatusChars = 2000
const maxDynamicSkillContextPaths = 64

var resolveExecutablePath = os.Executable

type App struct {
	Config           config.Config
	Client           *anthropic.Client
	Tools            *tools.Registry
	Sessions         *session.Store
	Workspace        string
	Executable       string
	Out              io.Writer
	Err              io.Writer
	In               io.Reader
	PluginManifests  []plugins.Manifest
	AgentDefinitions []agentdefs.Definition
	InlineAgents     []agentdefs.Definition
	PluginDirs       []string
	ActiveIDE        *bridge.EditorState

	ConfigLoadError     string
	ConfigLoadErrorKind string
	mcpLoadMu           sync.Mutex
	mcpMu               sync.Mutex
	mcpToolsLoaded      bool
	mcpToolsStale       bool
	lspMu               sync.Mutex
	lspClients          *codeintel.LSPClientPool
	dynamicSkillPaths   []string
}

// Close releases persistent runtime resources owned by the application.
func (a *App) Close() error {
	if a == nil {
		return nil
	}
	var joined error
	if a.Tools != nil {
		joined = errors.Join(joined, a.Tools.Close())
	}
	a.lspMu.Lock()
	clients := a.lspClients
	a.lspClients = nil
	a.lspMu.Unlock()
	if clients != nil {
		joined = errors.Join(joined, clients.Close())
	}
	return joined
}

func (a *App) lspClientPool() *codeintel.LSPClientPool {
	if a.Tools != nil && a.Tools.LSPClientPool() != nil {
		return a.Tools.LSPClientPool()
	}
	a.lspMu.Lock()
	defer a.lspMu.Unlock()
	if a.lspClients == nil {
		a.lspClients = codeintel.NewLSPClientPool()
	}
	return a.lspClients
}

func (a *App) mcpToolsAreLoaded() bool {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	return a.mcpToolsLoaded
}

func (a *App) runtimePluginManifests() ([]plugins.Manifest, error) {
	if a.PluginManifests != nil {
		return append([]plugins.Manifest(nil), a.PluginManifests...), nil
	}
	return plugins.Load(a.Workspace)
}

func sessionPluginDirs(manifests []plugins.Manifest) []string {
	dirs := make([]string, 0, len(manifests))
	seen := map[string]struct{}{}
	for _, manifest := range manifests {
		if !manifest.Session {
			continue
		}
		dir := filepath.Clean(strings.TrimSpace(manifest.Root))
		if dir == "." || dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}
	return dirs
}

func (a *App) runtimeAgentDefinitions() ([]agentdefs.Definition, error) {
	if a.AgentDefinitions != nil {
		return append([]agentdefs.Definition(nil), a.AgentDefinitions...), nil
	}
	manifests, err := a.runtimePluginManifests()
	if err != nil {
		return nil, err
	}
	return agentdefs.LoadWithManifests(a.Workspace, manifests)
}

func (a *App) runtimeSkills() ([]skills.Skill, error) {
	manifests, err := a.runtimePluginManifests()
	if err != nil {
		return nil, err
	}
	return skills.LoadWithManifests(a.Config.ConfigHome, a.Workspace, manifests)
}

func (a *App) runtimeCustomCommands() ([]customcommands.Command, error) {
	manifests, err := a.runtimePluginManifests()
	if err != nil {
		return nil, err
	}
	return customcommands.LoadWithManifests(a.Config.ConfigHome, a.Workspace, manifests)
}

func (a *App) findRuntimeSkill(name string) (skills.Skill, error) {
	all, err := a.runtimeSkills()
	if err != nil {
		return skills.Skill{}, err
	}
	for _, skill := range all {
		if skill.Active && strings.EqualFold(strings.TrimSpace(skill.Name), strings.TrimSpace(name)) {
			return skill, nil
		}
	}
	return skills.Skill{}, fmt.Errorf("%w: %s", skills.ErrNotFound, strings.TrimSpace(name))
}

func (a *App) findRuntimeCustomCommand(name string) (customcommands.Command, error) {
	all, err := a.runtimeCustomCommands()
	if err != nil {
		return customcommands.Command{}, err
	}
	target := strings.TrimSpace(strings.TrimPrefix(name, "/"))
	target = strings.TrimSuffix(target, ".md")
	target = strings.ReplaceAll(filepath.ToSlash(target), "/", ":")
	for _, command := range all {
		if command.Active && strings.EqualFold(strings.TrimSpace(command.Name), target) {
			return command, nil
		}
	}
	return customcommands.Command{}, fmt.Errorf("%w: %s", customcommands.ErrNotFound, target)
}

func (a *App) runtimeSkillSources() []skills.DiscoveryRoot {
	manifests, _ := a.runtimePluginManifests()
	return skills.SourcesWithManifests(a.Config.ConfigHome, a.Workspace, manifests)
}

func (a *App) runtimeCustomCommandSources() []customcommands.DiscoveryRoot {
	manifests, _ := a.runtimePluginManifests()
	return customcommands.SourcesWithManifests(a.Config.ConfigHome, a.Workspace, manifests)
}

func (a *App) runtimeContextualSkills(paths []string) ([]skills.Skill, error) {
	manifests, err := a.runtimePluginManifests()
	if err != nil {
		return nil, err
	}
	return skills.ContextualForPathsWithManifests(a.Config.ConfigHome, a.Workspace, paths, manifests)
}

type cliRun struct {
	ctx           context.Context
	args          []string
	originalArgs  []string
	baseOverrides config.FlagOverrides
	overrides     config.FlagOverrides
	command       string
	rest          []string
	cfg           config.Config
	workspace     string
	format        string
	app           *App
	getwd         func() (string, error)
}

func RunCLI(ctx context.Context, args []string, baseOverrides config.FlagOverrides) error {
	run := &cliRun{
		ctx:           ctx,
		args:          append([]string(nil), args...),
		originalArgs:  append([]string(nil), args...),
		baseOverrides: baseOverrides,
		format:        requestedOutputFormat(args),
	}
	if handled, err := run.prepareInvocation(); handled {
		return err
	}
	restoreCWD, err := applyGlobalCWD(run.overrides.CWD)
	if err != nil {
		return renderCLIError(os.Stdout, err, run.format)
	}
	defer restoreCWD()

	earlyHandlers := []func() (bool, error){
		run.handleBasicCommand,
		run.handleInspectionCommand,
		run.handleLocalCommand,
		run.prepareInteractiveWorkspace,
	}
	for _, handle := range earlyHandlers {
		if handled, err := handle(); handled {
			return err
		}
	}
	if handled, err := run.loadConfig(); handled {
		return err
	}
	if err := run.buildApp(); err != nil {
		return err
	}
	defer func() { _ = run.app.Close() }()
	if handled, err := run.prepareRuntime(); handled {
		return err
	}
	if handled, err := run.normalizeSlash(); handled {
		return err
	}
	return dispatchCLICommand(run.ctx, run.app, run.command, run.rest, run.overrides, run.originalArgs, run.format, run.renderStructured)
}

func (r *cliRun) prepareInvocation() (bool, error) {
	r.args = normalizeDirectConnectInvocation(r.args)
	if handled, err := r.handleDirectGlobalFlag(); handled {
		return true, err
	}
	if handled, err := renderGlobalResumeHelp(os.Stdout, r.args); handled {
		return true, err
	}
	if handled, err := r.handleGlobalACPInvocation(); handled {
		return true, err
	}
	if len(r.args) == 1 && r.args[0] == "-v" {
		r.args = []string{"version"}
	}
	overrides, command, rest, err := parseFlags(r.args, r.baseOverrides)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, renderHelpCommand(os.Stdout, nil)
		}
		return true, renderCLIError(os.Stdout, err, r.format)
	}
	r.overrides, r.command, r.rest = overrides, command, rest
	return false, nil
}

func (r *cliRun) handleDirectGlobalFlag() (bool, error) {
	if len(r.args) == 0 {
		return false, nil
	}
	switch r.args[0] {
	case "--help", "-h":
		return true, renderHelpCommand(os.Stdout, r.args[1:])
	case "--version", "-v":
		workspace, err := os.Getwd()
		if err != nil {
			return true, err
		}
		if err := renderVersion(os.Stdout, workspace, r.args[1:]); err != nil {
			return true, renderCLIErrorWhenStructured(os.Stdout, err, r.format)
		}
		return true, nil
	case "--acp", "-acp":
		if acpHelpRequested(r.args[1:]) {
			return true, renderCommandHelpTopic(os.Stdout, "acp", commandHelpArgsWithoutHelp(r.args[1:]), r.format)
		}
		if !acpServeRequested(r.args[1:]) {
			return true, renderACPStatus(os.Stdout, r.args[1:])
		}
		r.args = append([]string{"acp"}, r.args[1:]...)
	}
	return false, nil
}

func (r *cliRun) handleGlobalACPInvocation() (bool, error) {
	acpArgs, ok, err := parseACPGlobalInvocation(r.args)
	if !ok && err == nil {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if acpHelpRequested(acpArgs) {
		return true, renderCommandHelpTopic(os.Stdout, "acp", commandHelpArgsWithoutHelp(acpArgs), r.format)
	}
	if !acpServeRequested(acpArgs) {
		return true, renderACPStatus(os.Stdout, acpArgs)
	}
	r.args = append([]string{"acp"}, acpArgs...)
	return false, nil
}

func (r *cliRun) handleBasicCommand() (bool, error) {
	switch {
	case r.command == "" && hasExplicitEmptyPositional(r.originalArgs):
		return true, renderEmptyPrompt(os.Stdout, r.format)
	case r.command == "help" || r.command == "--help" || r.command == "-h":
		return true, renderHelpCommand(os.Stdout, r.rest)
	case r.command == "version" || r.command == "--version" || r.command == "-v":
		workspace, err := os.Getwd()
		if err != nil {
			return true, err
		}
		if err := renderVersion(os.Stdout, workspace, r.rest); err != nil {
			return true, renderCLIErrorWhenStructured(os.Stdout, err, r.format)
		}
		return true, nil
	case r.command == "acp":
		if handled, err := renderCommandHelpRequest(os.Stdout, r.command, r.rest, r.format); handled {
			return true, err
		}
		if !acpServeRequested(r.rest) {
			return true, renderACPStatus(os.Stdout, r.rest)
		}
	}
	return false, nil
}

func (r *cliRun) handleInspectionCommand() (bool, error) {
	switch r.command {
	case "config", "settings":
		return true, r.runConfigInspection()
	case "providers":
		return true, r.runProvidersInspection()
	default:
		return false, nil
	}
}

func (r *cliRun) runConfigInspection() error {
	if r.command == "settings" && positionalHelpSubcommand(r.rest) {
		return renderCommandHelpTopic(os.Stdout, "settings", argsWithoutHelpSubcommand(r.rest), r.format)
	}
	if handled, err := renderCommandHelpRequest(os.Stdout, r.command, r.rest, r.format); handled {
		return err
	}
	if handled, err := renderConfigValidateWithoutLoadedConfig(os.Stdout, r.rest, r.overrides); handled {
		return err
	}
	cfg, paths, err := config.LoadForInspection(r.overrides)
	if err != nil {
		if config.IsDiagnosticLoadError(err) {
			return renderConfigWithConfigLoadError(os.Stdout, r.command, r.rest, r.overrides, r.originalArgs, err)
		}
		return renderCLIError(os.Stdout, err, r.format)
	}
	return renderConfigInspection(os.Stdout, redactedConfig(cfg), paths, r.rest)
}

func (r *cliRun) runProvidersInspection() error {
	cfg, paths, err := config.LoadForInspection(r.overrides)
	if err != nil {
		if config.IsDiagnosticLoadError(err) {
			return renderProvidersWithConfigLoadError(os.Stdout, r.command, r.rest, r.overrides, r.originalArgs, err)
		}
		return renderCLIError(os.Stdout, err, r.format)
	}
	applyStoredOAuthToken(&cfg, time.Now().UTC())
	if err := renderProvidersCommand(os.Stdout, cfg, paths, r.rest); err != nil {
		return renderCLIErrorWhenStructured(os.Stdout, err, r.format)
	}
	return nil
}

func (r *cliRun) handleLocalCommand() (bool, error) {
	if handled, err := r.runCommandGuards(); handled {
		return true, err
	}
	switch r.command {
	case "mock-server":
		return true, r.runMockServer()
	case "self-test", "mock-parity", "parity":
		return true, r.runParity()
	case "completion":
		if shellCompletionRequested(r.rest) || shellCompletionOutputFlagPresent(r.rest) {
			return true, renderShellCompletionCommand(os.Stdout, r.rest)
		}
	case "init":
		return true, r.runProjectInit()
	case "state":
		return true, r.runWorkerState()
	case "memory":
		return true, r.runMemory()
	case "enterprise":
		if len(r.rest) > 0 && r.rest[0] == "verify" {
			err := enterpriseVerify(os.Stdout, stripEnterpriseOutputFormatFlags(r.rest))
			return true, renderCLIErrorWhenStructured(os.Stdout, err, r.format)
		}
	}
	return false, nil
}

func (r *cliRun) runCommandGuards() (bool, error) {
	helpCommand := r.command
	if strings.HasPrefix(r.command, "/") && strings.TrimSpace(r.overrides.Resume) == "" {
		if mapped := slashCommandName(r.command); mapped != "" {
			helpCommand = mapped
		}
	}
	if handled, err := renderCommandHelpRequest(os.Stdout, helpCommand, r.rest, r.format); handled {
		return true, err
	}
	if handled, err := renderGlobalResumeArgumentGuard(os.Stdout, r.command, r.rest, r.overrides, r.format); handled {
		return true, err
	}
	if handled, err := renderLocalRouteGuard(os.Stdout, r.command, r.rest, r.format); handled {
		return true, err
	}
	return false, nil
}

func (r *cliRun) runMockServer() error {
	addr := ":8089"
	if len(r.rest) > 0 {
		addr = r.rest[0]
	}
	fmt.Fprintf(os.Stderr, "mock Anthropic-compatible server listening on %s\n", addr)
	return http.ListenAndServe(addr, mockanthropic.Server{Text: "mock response from codog"}.Handler())
}

func (r *cliRun) runParity() error {
	defaultFormat := "text"
	if r.command == "self-test" {
		defaultFormat = "json"
	}
	if err := runMockParityCommand(r.ctx, os.Stdout, r.rest, r.format, defaultFormat); err != nil {
		return renderCLIErrorWhenStructured(os.Stdout, err, r.format)
	}
	return nil
}

func (r *cliRun) runProjectInit() error {
	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	err = initProject(os.Stdout, workspace, r.rest, func(report projectinit.Report) error {
		cfg, _, err := config.LoadForInspection(r.overrides)
		if err != nil {
			return nil
		}
		return runSetupHookPayload(r.ctx, hooks.Runner{
			Config:                 cfg.Hooks,
			Workspace:              workspace,
			ConfigHome:             cfg.ConfigHome,
			Disabled:               cfg.EffectiveDisableAllHooks(),
			AllowedHTTPHookURLs:    cfg.AllowedHTTPHookURLs,
			HTTPHookAllowedEnvVars: cfg.HTTPHookAllowedEnvVars,
		}, workspace, "init", report.Status)
	})
	if err != nil {
		return renderCLIError(os.Stdout, err, r.format)
	}
	return nil
}

func (r *cliRun) runWorkerState() error {
	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := renderWorkerState(os.Stdout, workspace, r.rest); err != nil {
		var exitErr *ExitError
		if errors.As(err, &exitErr) {
			return err
		}
		return renderCLIErrorWhenStructured(os.Stdout, err, r.format)
	}
	return nil
}

func (r *cliRun) runMemory() error {
	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	rulesImport := memory.RulesImportOptions{}
	if cfg, err := config.Load(r.baseOverrides); err == nil {
		rulesImport = memoryRulesImportOptionsFromConfig(cfg)
	}
	return renderMemoryCommand(os.Stdout, workspace, rulesImport, r.rest)
}

func (r *cliRun) prepareInteractiveWorkspace() (bool, error) {
	if !interactiveWorkspaceCommand(r.command) {
		return false, nil
	}
	workspace, err := os.Getwd()
	if err != nil {
		return true, err
	}
	if err := renderBroadCWDGuard(os.Stdout, r.command, r.rest, workspace, r.overrides.AllowBroadCWD, r.format); err != nil {
		return true, err
	}
	proceed, err := confirmInteractiveWorkspaceTrust(r.ctx, workspace, r.overrides)
	if err != nil {
		return true, renderCLIErrorWhenStructured(os.Stdout, err, r.format)
	}
	if !proceed {
		return true, nil
	}
	proceed, err = configureInteractiveTheme(r.ctx, r.overrides)
	if err != nil {
		return true, renderCLIErrorWhenStructured(os.Stdout, err, r.format)
	}
	return !proceed, nil
}

func (r *cliRun) loadConfig() (bool, error) {
	cfg, err := config.Load(r.overrides)
	if err != nil {
		return true, r.renderConfigLoadError(err)
	}
	applyStoredOAuthToken(&cfg, time.Now().UTC())
	getwd := r.getwd
	if getwd == nil {
		getwd = os.Getwd
	}
	workspace, err := getwd()
	if err != nil {
		return true, err
	}
	if err := renderBroadCWDGuard(os.Stdout, r.command, r.rest, workspace, r.overrides.AllowBroadCWD, r.format); err != nil {
		return true, err
	}
	r.cfg, r.workspace = cfg, workspace
	return false, nil
}

func (r *cliRun) renderConfigLoadError(err error) error {
	if !config.IsDiagnosticLoadError(err) {
		return renderCLIError(os.Stdout, err, r.format)
	}
	switch {
	case isConfigCommand(r.command):
		return renderConfigWithConfigLoadError(os.Stdout, r.command, r.rest, r.overrides, r.originalArgs, err)
	case isMCPCommand(r.command):
		return renderMCPWithConfigLoadError(os.Stdout, r.command, r.rest, r.originalArgs, err)
	case isPluginsCommand(r.command):
		return renderPluginsWithConfigLoadError(os.Stdout, r.command, r.rest, r.originalArgs, err)
	case isProvidersCommand(r.command):
		return renderProvidersWithConfigLoadError(os.Stdout, r.command, r.rest, r.overrides, r.originalArgs, err)
	case isStatusCommand(r.command):
		return renderStatusWithConfigLoadError(os.Stdout, r.command, r.rest, r.overrides, r.originalArgs, err)
	case isBootstrapPlanCommand(r.command):
		return renderBootstrapPlanWithConfigLoadError(os.Stdout, r.rest, r.overrides, r.originalArgs, err)
	case isDeferredInitCommand(r.command):
		return renderDeferredInitWithConfigLoadError(os.Stdout, r.command, r.rest, r.overrides, r.originalArgs, err)
	case isPrefetchCommand(r.command):
		return renderPrefetchWithConfigLoadError(os.Stdout, r.rest, r.overrides, r.originalArgs, err)
	case isCapabilitiesCommand(r.command):
		return renderCapabilitiesWithConfigLoadError(os.Stdout, r.rest, r.overrides, r.originalArgs)
	case isDoctorCommand(r.command):
		return renderDoctorWithConfigLoadError(os.Stdout, r.command, r.rest, r.overrides, r.originalArgs, err)
	default:
		return renderCLIError(os.Stdout, err, r.format)
	}
}

func (r *cliRun) buildApp() error {
	pluginManifests, err := plugins.LoadWithDirs(r.workspace, r.overrides.PluginDirs)
	if err != nil {
		return renderCLIErrorWhenStructured(os.Stdout, err, r.format)
	}
	pluginDirs := sessionPluginDirs(pluginManifests)
	fileAgentDefinitions, err := agentdefs.LoadWithManifests(r.workspace, pluginManifests)
	if err != nil {
		return renderCLIErrorWhenStructured(os.Stdout, err, r.format)
	}
	inlineAgentDefinitions, err := agentdefs.ParseInline(r.overrides.Agents)
	if err != nil {
		return renderCLIErrorWhenStructured(os.Stdout, err, r.format)
	}
	agentDefinitions := agentdefs.Merge(fileAgentDefinitions, inlineAgentDefinitions)
	if err := applyPluginHookConfigsFromManifests(&r.cfg, pluginManifests); err != nil {
		return err
	}
	if err := applyPluginMCPServersFromManifests(&r.cfg, pluginManifests); err != nil {
		return err
	}
	additionalDirs, err := pathscope.EffectiveDirs(r.workspace, r.cfg.AdditionalDirs)
	if err != nil {
		return err
	}
	sessionStore, err := session.NewWorkspaceStoreWithCleanup(r.cfg.ConfigHome, r.workspace, r.cfg.EffectiveCleanupPeriodDays())
	if err != nil {
		return err
	}
	executable, _ := resolveExecutablePath()
	registryOptions := toolRegistryOptionsFromConfig(r.cfg, additionalDirs, os.Stdin, os.Stderr, executable, agentDefinitions)
	registryOptions.PluginDirs = append([]string(nil), pluginDirs...)
	r.app = &App{
		Config:           r.cfg,
		Client:           anthropicClientFromConfig(r.cfg),
		Tools:            tools.NewRegistryWithOptions(r.workspace, registryOptions),
		Sessions:         sessionStore,
		Workspace:        r.workspace,
		Executable:       executable,
		Out:              os.Stdout,
		Err:              os.Stderr,
		In:               os.Stdin,
		PluginManifests:  pluginManifests,
		AgentDefinitions: agentDefinitions,
		InlineAgents:     inlineAgentDefinitions,
		PluginDirs:       append([]string(nil), pluginDirs...),
	}
	return nil
}

func (r *cliRun) prepareRuntime() (bool, error) {
	if handled, err := r.selectInteractiveResume(); handled {
		return true, err
	}
	if r.overrides.IDE {
		if err := r.app.connectActiveIDE(); err != nil {
			return true, renderCLIErrorWhenStructured(os.Stdout, err, r.format)
		}
	}
	if err := renderDebugStartup(os.Stderr, r.cfg, r.command, r.rest, r.workspace, r.overrides, r.format); err != nil {
		return true, renderCLIErrorWhenStructured(os.Stdout, err, r.format)
	}
	if err := r.app.RegisterPluginTools(); err != nil {
		return true, err
	}
	if err := r.app.validateGlobalToolRules(r.overrides, r.format); err != nil {
		return true, err
	}
	return false, nil
}

func (r *cliRun) selectInteractiveResume() (bool, error) {
	if r.overrides.Resume != interactiveResumeValue {
		return false, nil
	}
	if _, ok := terminalInput(r.app.In); !ok {
		return true, renderInteractiveOnlyWithHint(r.app.Out, "resume", "--resume without a session id requires an interactive terminal", "Pass `--resume latest` or `--resume SESSION_ID` in non-interactive mode.", r.format)
	}
	sessions, err := r.app.Sessions.List()
	if err != nil {
		return true, renderCLIErrorWhenStructured(r.app.Out, err, r.format)
	}
	if len(sessions) == 0 {
		return true, renderCLIError(r.app.Out, invalidFlagValueError{
			Flag:    "--resume",
			Message: "no saved sessions are available to resume",
			Usage:   "codog [--resume latest|--resume SESSION_ID]",
		}, r.format)
	}
	selected, err := tui.SelectSessionWithTheme(r.ctx, resumeSessionChoices(sessions), r.cfg.Theme)
	if err != nil {
		return true, renderCLIErrorWhenStructured(r.app.Out, err, r.format)
	}
	if selected == "" {
		return true, nil
	}
	r.overrides.Resume = selected
	return false, nil
}

func (r *cliRun) normalizeSlash() (bool, error) {
	if !strings.HasPrefix(r.command, "/") {
		return false, nil
	}
	if strings.TrimSpace(r.overrides.Resume) != "" {
		return true, r.app.RunResumedSlash(r.ctx, r.command, r.rest, r.overrides, r.format)
	}
	if slashCommandName(r.command) == "" && !directSlashInteractiveOnly(r.command) {
		if handled, err := r.app.runDirectCustomSlash(r.ctx, r.command, r.rest, r.overrides, r.format); handled {
			return true, r.renderStructured(err)
		}
	}
	command, rest, err := normalizeDirectSlashInvocation(os.Stdout, r.command, r.rest, r.format, r.app.customSlashCompletionCandidates())
	if err != nil {
		return true, err
	}
	r.command, r.rest = command, rest
	if handled, err := renderCommandHelpRequest(os.Stdout, r.command, r.rest, r.format); handled {
		return true, err
	}
	return false, nil
}

func (r *cliRun) renderStructured(err error) error {
	if err == nil {
		return nil
	}
	return renderCLIErrorWhenStructured(r.app.Out, err, r.format)
}

var errCLICommandNotHandled = errors.New("CLI command not handled")

type cliCommandHandler func(context.Context, *App, string, []string, config.FlagOverrides, []string, string, func(error) error) error

func dispatchCLICommand(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	handlers := []cliCommandHandler{
		runCLIInteractionCommands,
		runCLISessionsCommands,
		runCLISessionWorkspaceCommands,
		runCLIConfigurationCommands,
		runCLIModelConfigurationCommands,
		runCLIPreferenceCommands,
		runCLIAccessibilityCommands,
		runCLIExperienceCommands,
		runCLIExtensionsCommands,
		runCLIUsageCommands,
		runCLIInsightCommands,
		runCLILimitCommands,
		runCLIParityCommands,
		runCLIPlansCommands,
		runCLIGitCommands,
		runCLIReviewsCommands,
		runCLIExecutionCommands,
		runCLIPluginsAuthCommands,
		runCLILifecycleCommands,
		runCLIDiagnosticsCommands,
		runCLIWorkspaceCommands,
		runCLICodeIntelCommands,
		runCLIRemoteCommands,
		runCLIMaintenanceCommands,
	}
	for _, handle := range handlers {
		if err := handle(ctx, app, command, rest, overrides, originalArgs, format, wrapStructured); !errors.Is(err, errCLICommandNotHandled) {
			return err
		}
	}
	if command != "" {
		if len(rest) == 0 {
			if slashName := bareApprovalSlashName(command); slashName != "" {
				return renderInteractiveOnlySlash(os.Stdout, slashName, requestedOutputFormat(originalArgs))
			}
		}
		return renderCommandNotFound(os.Stdout, command, rest, requestedOutputFormat(originalArgs))
	}
	return app.REPL(ctx, overrides)
}

func runCLIInteractionCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "help":
		return renderHelpCommand(app.Out, rest)
	case "version":
		if err := renderVersion(app.Out, app.Workspace, rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "":
		input, nonTerminalStdin, err := readPromptInputState(app.In)
		if err != nil {
			return err
		}
		if strings.TrimSpace(input) != "" {
			return app.promptWithOutput(ctx, strings.TrimSpace(input), overrides, requestedOutputFormat(originalArgs), false)
		}
		if nonTerminalStdin {
			return renderInteractiveOnlyWithHint(app.Out, "repl", "codog requires an interactive terminal", "Pipe a prompt with `echo 'task' | codog` or run `codog repl` in an interactive terminal.", requestedOutputFormat(originalArgs))
		}
		return app.TUI(ctx, overrides)
	case "repl":
		return app.REPL(ctx, overrides)
	case "tui":
		return app.TUI(ctx, overrides)
	case "prompt":
		return runCLIPromptCommand(ctx, app, rest, overrides, originalArgs)
	default:
		return errCLICommandNotHandled
	}
}

func runCLIPromptCommand(ctx context.Context, app *App, args []string, overrides config.FlagOverrides, originalArgs []string) error {
	req, err := parsePromptArgs(args)
	if err != nil {
		return renderCLIError(app.Out, err, requestedOutputFormat(originalArgs))
	}
	input, replayMessages, err := readCLIPromptInput(app, req)
	if err != nil {
		return err
	}
	if strings.TrimSpace(input) == "" {
		if req.Compact {
			return renderCompactPromptMissingArgument(app.Out, req.Format)
		}
		return renderMissingPrompt(app.Out, req.Format)
	}
	verboseOutput := req.Verbose || overrides.Verbose
	includePartialMessages := req.IncludePartialMessages || overrides.IncludePartialMessages || (verboseOutput && req.Format == "stream-json")
	if (req.IncludePartialMessages || overrides.IncludePartialMessages) && req.Format != "stream-json" {
		return renderCLIError(app.Out, invalidFlagValueError{
			Flag:    "--include-partial-messages",
			Value:   req.Format,
			Message: "--include-partial-messages requires --output-format=stream-json",
			Usage:   "codog -p --output-format stream-json --include-partial-messages \"<prompt>\"",
		}, req.Format)
	}
	promptOverrides := overrides
	if req.MaxBudgetUSD != nil {
		promptOverrides.MaxBudgetUSD = req.MaxBudgetUSD
	}
	options := turnOptions{
		Attachments:            req.Attachments,
		ReplayUserMessages:     replayMessages,
		JSONSchema:             req.JSONSchema,
		IncludePartialMessages: includePartialMessages,
		Verbose:                verboseOutput,
	}
	return app.promptWithOutputOptions(ctx, input, promptOverrides, req.Format, req.Compact, options)
}

func readCLIPromptInput(app *App, req promptCLIRequest) (string, []promptStreamJSONReplayMessage, error) {
	if req.InputFormat == "stream-json" {
		streamInput, err := readPromptStreamJSONInputState(app.In)
		if err != nil {
			return "", nil, renderCLIError(app.Out, err, req.Format)
		}
		replayMessages := []promptStreamJSONReplayMessage(nil)
		if req.ReplayUserMessages {
			replayMessages = streamInput.ReplayMessages
		}
		return mergePromptWithStdin(req.Prompt, streamInput.Prompt), replayMessages, nil
	}
	if !req.PromptProvided {
		data, err := readPromptInput(app.In)
		return strings.TrimSpace(string(data)), nil, err
	}
	if req.UseStdin {
		data, err := readPromptInput(app.In)
		return mergePromptWithStdin(req.Prompt, string(data)), nil, err
	}
	return req.Prompt, nil, nil
}

func runCLISessionsCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "acp":
		return wrapStructured(app.ACP(ctx, rest))
	case "btw":
		return wrapStructured(app.BTW(ctx, rest, overrides, nil))
	case "config":
		return wrapStructured(app.ConfigCommand(rest))
	case "session", "sessions":
		if err := app.SessionsCommand(rest); err != nil {
			return renderSessionsCommandError(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "resume", "continue":
		return wrapStructured(app.ResumeCommand(rest))
	case "clear":
		return wrapStructured(app.ClearCommand(rest))
	case "conversation":
		return wrapStructured(app.Conversation(rest, overrides))
	case "backfill-sessions":
		return wrapStructured(app.BackfillSessions(rest))
	case "generateSessionName", "generatesessionname", "generate-session-name":
		return wrapStructured(app.GenerateSessionName(rest, overrides))
	case "rename":
		return wrapStructured(app.Rename(rest, overrides))
	default:
		return errCLICommandNotHandled
	}
}

func runCLISessionWorkspaceCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "history", "prompt-history":
		return wrapStructured(app.History(rest, overrides))
	case "summary":
		return wrapStructured(app.Summary(rest, overrides))
	case "rewind", "checkpoint":
		return wrapStructured(app.Rewind(rest, overrides))
	case "todos":
		return wrapStructured(app.Todos(rest))
	case "focus":
		return wrapStructured(app.Focus(rest))
	case "unfocus":
		return wrapStructured(app.Unfocus(rest))
	case "add-dir":
		return wrapStructured(app.AddDir(rest))
	case "validation":
		return wrapStructured(app.Validation(rest))
	case "workspace", "cwd":
		return wrapStructured(app.WorkspaceCommand(rest))
	case "scope", "safer-scope":
		return wrapStructured(app.Scope(rest))
	default:
		return errCLICommandNotHandled
	}
}

func runCLIConfigurationCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "output-style":
		if err := app.OutputStyle(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "reset":
		if err := app.Reset(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "model":
		if err := app.Model(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "models":
		if err := app.Models(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "api":
		if err := app.APIContext(ctx, rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "server":
		if err := app.Server(ctx, rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	default:
		return errCLICommandNotHandled
	}
}

func runCLIModelConfigurationCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "open":
		if err := app.Open(ctx, rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "ssh":
		if err := app.SSH(ctx, rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "api-key":
		if err := app.APIKey(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "auth":
		if err := app.Auth(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "setup-token":
		if err := app.SetupToken(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "advisor":
		if err := app.Advisor(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	default:
		return errCLICommandNotHandled
	}
}

func runCLIPreferenceCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "budget":
		if err := app.Budget(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "max-tokens":
		if err := app.MaxTokens(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "temperature":
		if err := app.Temperature(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "max-turns":
		if err := app.MaxTurns(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "permissions":
		if err := app.Permissions(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "allowed-tools":
		return wrapStructured(app.AllowedTools(rest))
	default:
		return errCLICommandNotHandled
	}
}

func runCLIAccessibilityCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "language":
		if err := app.Language(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "theme":
		if err := app.Theme(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "color":
		if err := app.Theme(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "vim":
		if err := app.Vim(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "effort":
		if err := app.Effort(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "reasoning":
		if err := app.Reasoning(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "fast":
		if err := app.Fast(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	default:
		return errCLICommandNotHandled
	}
}

func runCLIExperienceCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "voice":
		if err := app.Voice(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "listen":
		if err := app.Voice(append([]string{"listen"}, rest...)); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "speak":
		return wrapStructured(app.Speak(ctx, rest, overrides))
	case "chrome":
		if err := app.Chrome(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "privacy-settings":
		if err := app.PrivacySettings(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "profile":
		if err := app.Profile(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "telemetry":
		if err := app.Telemetry(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "keybindings":
		if err := app.Keybindings(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "notifications":
		if err := app.Notifications(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	default:
		return errCLICommandNotHandled
	}
}

func runCLIExtensionsCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "skill", "skills":
		if err := app.Skills(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "commands":
		if err := app.Commands(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "slash":
		if err := app.Slash(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "templates":
		if err := app.Templates(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "hooks":
		if err := app.Hooks(ctx, rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "mcp":
		return wrapStructured(app.MCP(ctx, rest))
	case "capabilities":
		if err := app.Capabilities(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	default:
		return errCLICommandNotHandled
	}
}

func runCLIUsageCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "cost", "tokens":
		if err := app.UsageOverview(command, rest, overrides); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "cache", "caches":
		if err := app.Cache(rest, overrides); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "break-cache":
		if err := app.BreakCache(rest, overrides); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "usage":
		if err := app.Usage(rest, overrides); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "stats":
		if err := app.UsageOverview(command, rest, overrides); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	default:
		return errCLICommandNotHandled
	}
}

func runCLIInsightCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "bookmarks":
		if err := app.Bookmarks(rest, overrides); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "metrics":
		if err := app.Metrics(rest, overrides); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "perf-issue":
		if err := app.PerfIssue(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "insights":
		if err := app.Insights(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "think-back", "thinkback", "thinkback-play":
		if err := app.ThinkBack(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	default:
		return errCLICommandNotHandled
	}
}

func runCLILimitCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "compact":
		if err := app.Compact(rest, overrides); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "undo":
		if err := app.Undo(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "extra-usage":
		extraUsageArgs := injectGlobalOutputFormat("extra-usage", rest, format)
		if err := app.ExtraUsage(extraUsageArgs); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "extra-usage-core":
		extraUsageArgs := injectGlobalOutputFormat("extra-usage", rest, format)
		if err := app.ExtraUsage(extraUsageArgs); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "extra-usage-noninteractive":
		extraUsageArgs := injectGlobalOutputFormat("extra-usage", rest, format)
		if err := app.ExtraUsage(appendExtraUsageNoOpen(extraUsageArgs)); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	default:
		return errCLICommandNotHandled
	}
}

func runCLIParityCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "rate-limit":
		if err := app.RateLimit(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "rate-limit-options":
		if err := app.RateLimitOptions(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "reset-limits":
		if err := app.ResetLimits(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "ant-trace":
		if err := app.AntTrace(ctx, rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "mock-limits":
		if err := app.MockLimits(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	default:
		return errCLICommandNotHandled
	}
}

func runCLIPlansCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "mock-parity", "parity", "self-test":
		if err := app.MockParity(ctx, rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "plan":
		return wrapStructured(app.Plan(rest))
	case "ultraplan":
		return wrapStructured(app.Plan(rest))
	case "exit-plan":
		return wrapStructured(app.Plan(append([]string{"exit"}, rest...)))
	case "export":
		if err := app.ExportWithOverrides(rest, overrides); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "share":
		return wrapStructured(app.Share(rest, overrides))
	case "copy":
		return wrapStructured(app.Copy(ctx, rest, overrides))
	case "paste":
		return wrapStructured(app.Paste(ctx, rest, overrides))
	case "pin":
		return wrapStructured(app.Pin(rest, overrides))
	case "unpin":
		return wrapStructured(app.Unpin(rest, overrides))
	default:
		return errCLICommandNotHandled
	}
}

func runCLIGitCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "git":
		if err := app.Git(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "diff", "log", "blame", "commit":
		if err := app.Git(append([]string{command}, rest...)); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "branch":
		return wrapStructured(app.Branch(rest))
	case "branch-lock", "branchlock":
		return wrapStructured(app.BranchLock(rest))
	case "stale-base", "base-check":
		return wrapStructured(app.StaleBase(rest))
	case "green-contract", "green":
		return wrapStructured(app.GreenContract(rest))
	case "g004-conformance", "g004":
		return wrapStructured(app.G004Conformance(rest))
	case "report-schema":
		return wrapStructured(app.ReportSchema(rest))
	case "trust":
		return wrapStructured(app.Trust(rest))
	case "tag":
		return wrapStructured(app.Tag(rest))
	case "stash":
		return wrapStructured(app.Stash(rest))
	case "changelog":
		return wrapStructured(app.Changelog(rest))
	case "release-notes":
		return wrapStructured(app.ReleaseNotes(rest))
	default:
		return errCLICommandNotHandled
	}
}

func runCLIReviewsCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "review", "ultrareview":
		return wrapStructured(app.Review(rest))
	case "reviewRemote", "review-remote":
		return wrapStructured(app.ReviewRemote(ctx, rest))
	case "feedback", "bug":
		return wrapStructured(app.Feedback(rest, overrides))
	case "pr":
		return wrapStructured(app.PullRequestDraft(rest, overrides))
	case "commit-push-pr":
		return wrapStructured(app.CommitPushPR(ctx, rest))
	case "autofix-pr":
		return wrapStructured(app.AutofixPR(ctx, rest))
	case "pr-comments", "pr_comments":
		return wrapStructured(app.PRComments(ctx, rest))
	case "install-github-app", "setupGitHubActions":
		return wrapStructured(app.InstallGitHubApp(rest))
	case "install-slack-app":
		slackAppArgs := injectGlobalOutputFormat("install-slack-app", rest, format)
		return wrapStructured(app.InstallSlackApp(slackAppArgs))
	case "stickers":
		stickerArgs := injectGlobalOutputFormat("stickers", rest, format)
		return wrapStructured(app.Stickers(stickerArgs))
	case "passes":
		passesArgs := injectGlobalOutputFormat("passes", rest, format)
		return wrapStructured(app.Passes(passesArgs))
	case "issue":
		return wrapStructured(app.IssueDraft(rest, overrides))
	default:
		return errCLICommandNotHandled
	}
}

func runCLIExecutionCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "run":
		return wrapStructured(app.RunCommand(ctx, rest))
	case "node", "python":
		return wrapStructured(app.LanguageCommand(ctx, command, rest))
	case "test":
		return wrapStructured(app.ProjectCommand(ctx, "test", rest))
	case "build":
		return wrapStructured(app.ProjectCommand(ctx, "build", rest))
	case "lint":
		return wrapStructured(app.ProjectCommand(ctx, "lint", rest))
	case "background":
		if err := app.BackgroundWithFormat(rest, overrides, format); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "tasks", "bashes":
		if err := app.BackgroundWithFormat(rest, overrides, format); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "cron":
		if err := app.CronWithFormat(rest, format); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "team":
		if err := app.TeamWithFormat(rest, format); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "agents":
		return wrapStructured(app.AgentsWithOverrides(rest, overrides))
	case "subagent":
		return wrapStructured(app.Subagent(rest, overrides))
	default:
		return errCLICommandNotHandled
	}
}

func runCLIPluginsAuthCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "reload-plugins":
		return wrapStructured(app.ReloadPluginsWithFormat(rest, format))
	case "plugin", "plugins", "marketplace":
		return wrapStructured(app.Marketplace(rest))
	case "login":
		return wrapStructured(app.Login(rest))
	case "logout":
		return wrapStructured(app.Logout(rest))
	case "oauth":
		if err := app.OAuth(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "oauth-refresh":
		return wrapStructured(app.OAuthRefresh(rest))
	case "providers":
		if err := app.Providers(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	default:
		return errCLICommandNotHandled
	}
}

func runCLILifecycleCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "exit", "quit":
		return wrapStructured(app.ExitCompatibility(rest))
	case "good-claude":
		return wrapStructured(app.GoodClaude(rest))
	case "brief":
		return wrapStructured(app.BriefWithFormat(rest, format))
	default:
		return errCLICommandNotHandled
	}
}

func runCLIDiagnosticsCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "bootstrap-plan":
		return wrapStructured(app.BootstrapPlan(rest))
	case "prefetch":
		return wrapStructured(app.Prefetch(rest))
	case "deferred-init", "startup-report":
		return wrapStructured(app.DeferredInit(command, rest))
	case "status":
		return wrapStructured(app.Status(rest, overrides))
	case "statusline":
		return wrapStructured(app.Statusline(rest, overrides))
	case "setup":
		return wrapStructured(app.Setup(ctx, rest))
	case "terminal-setup", "terminalSetup":
		return wrapStructured(app.TerminalSetup(rest))
	case "context", "context-noninteractive":
		return wrapStructured(app.Context(rest, overrides))
	case "ctx_viz":
		return wrapStructured(app.ContextViz(rest, overrides))
	case "files":
		return wrapStructured(app.Files(rest))
	case "search":
		return wrapStructured(app.Search(ctx, rest))
	case "security-review":
		return wrapStructured(app.SecurityReview(rest))
	case "bughunter":
		return wrapStructured(app.Bughunter(rest))
	default:
		return errCLICommandNotHandled
	}
}

func runCLIWorkspaceCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "init":
		return wrapStructured(app.Init(rest))
	case "init-verifiers":
		return wrapStructured(app.InitVerifiers(rest))
	case "state":
		return wrapStructured(app.State(rest))
	case "memory":
		return wrapStructured(app.Memory(rest))
	case "project":
		if err := app.Project(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "onboarding":
		return wrapStructured(app.Onboarding(rest))
	case "env":
		envArgs := injectGlobalOutputFormat("env", rest, format)
		if err := app.Env(envArgs); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "doctor":
		return wrapStructured(app.Doctor(rest))
	case "sandbox":
		return wrapStructured(app.Sandbox())
	case "sandbox-toggle":
		sandboxToggleArgs := injectGlobalOutputFormat("sandbox-toggle", rest, format)
		if err := app.SandboxToggle(sandboxToggleArgs); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "heapdump":
		return wrapStructured(app.HeapDump(rest))
	default:
		return errCLICommandNotHandled
	}
}

func runCLICodeIntelCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "symbols":
		return wrapStructured(app.Symbols(rest))
	case "diagnostics":
		return wrapStructured(app.Diagnostics(ctx, rest))
	case "map":
		return wrapStructured(app.Map(rest))
	case "references":
		return wrapStructured(app.References(rest))
	case "definition":
		return wrapStructured(app.Definition(rest))
	case "hover":
		return wrapStructured(app.Hover(rest))
	case "teleport":
		return wrapStructured(app.Teleport(rest))
	case "completion":
		return wrapStructured(app.Completion(rest))
	case "format":
		return wrapStructured(app.Format(rest))
	case "code-intel":
		return wrapStructured(app.CodeIntel(rest))
	case "notebook-read":
		return wrapStructured(app.CodeIntel(append([]string{"notebook-read"}, rest...)))
	case "notebook-edit":
		return wrapStructured(app.CodeIntel(append([]string{"notebook-edit"}, rest...)))
	default:
		return errCLICommandNotHandled
	}
}

func runCLIRemoteCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "remote":
		return wrapStructured(app.Remote(rest))
	case "remote-env":
		return wrapStructured(app.RemoteEnv(rest))
	case "remote-setup", "web-setup":
		return wrapStructured(app.RemoteSetup(rest, overrides))
	case "bridge", "remote-control", "rc":
		return wrapStructured(app.Bridge(rest))
	case "bridge-kick":
		return wrapStructured(app.BridgeKick(rest))
	case "desktop", "app":
		return wrapStructured(app.Desktop(rest, overrides))
	case "mobile":
		return wrapStructured(app.Mobile(rest, overrides))
	case "ios", "android":
		return wrapStructured(app.Mobile(append([]string{command}, rest...), overrides))
	case "ide":
		return wrapStructured(app.IDE(rest))
	default:
		return errCLICommandNotHandled
	}
}

func runCLIMaintenanceCommands(ctx context.Context, app *App, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, format string, wrapStructured func(error) error) error {
	switch command {
	case "debug-tool-call":
		return wrapStructured(app.DebugToolCall(ctx, rest, overrides))
	case "updater":
		if err := app.Updater(ctx, rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "rollback":
		if err := app.Updater(ctx, append([]string{"rollback"}, rest...)); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "upgrade":
		if err := app.Upgrade(ctx, rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "install":
		if err := app.Install(ctx, rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "enterprise":
		if err := app.Enterprise(rest); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "dump-manifests":
		return wrapStructured(app.DumpManifests(rest))
	case "system-prompt":
		systemPromptArgs := injectGlobalOutputFormat("system-prompt", rest, format)
		if err := app.SystemPromptCommand(systemPromptArgs); err != nil {
			return renderCLIErrorWhenStructured(app.Out, err, requestedOutputFormat(originalArgs))
		}
		return nil
	case "tool-details":
		return wrapStructured(app.ToolDetailsWithFormat(rest, format))
	default:
		return errCLICommandNotHandled
	}
}

func interactiveWorkspaceCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "", "tui", "repl":
		return true
	default:
		return false
	}
}

func confirmInteractiveWorkspaceTrust(ctx context.Context, workspace string, overrides config.FlagOverrides) (bool, error) {
	if _, ok := terminalInput(os.Stdin); !ok {
		return true, nil
	}
	trustOverrides := overrides
	trustOverrides.SettingSources = []string{"user"}
	trustOverrides.SettingSourcesSet = true
	cfg, _, err := config.LoadForInspection(trustOverrides)
	if err != nil {
		return false, err
	}
	if workspaceMatchesTrustedRoots(workspace, cfg.TrustedRoots) {
		return true, nil
	}
	trusted, err := tui.ConfirmWorkspaceTrustWithTheme(ctx, workspace, cfg.Theme)
	if err != nil || !trusted {
		return false, err
	}
	workspace = canonicalTrustRoot(workspace)
	roots := appendUniqueTrustRoot(cfg.TrustedRoots, workspace)
	_, err = config.SetFileValue(filepath.Join(cfg.ConfigHome, "config.json"), "trustedRoots", roots)
	if err != nil {
		return false, fmt.Errorf("persist workspace trust: %w", err)
	}
	return true, nil
}

func configureInteractiveTheme(ctx context.Context, overrides config.FlagOverrides) (bool, error) {
	if _, ok := terminalInput(os.Stdin); !ok {
		return true, nil
	}
	userOverrides := overrides
	userOverrides.SettingSources = []string{"user"}
	userOverrides.SettingSourcesSet = true
	cfg, _, err := config.LoadForInspection(userOverrides)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(cfg.Theme) != "" {
		if _, ok := tui.NormalizeThemeName(cfg.Theme); ok {
			return true, nil
		}
	}
	selected, accepted, err := tui.SelectTheme(ctx, "auto", true)
	if err != nil || !accepted {
		return false, err
	}
	_, err = config.SetFileValue(filepath.Join(cfg.ConfigHome, "config.json"), "theme", selected)
	if err != nil {
		return false, fmt.Errorf("persist terminal theme: %w", err)
	}
	return true, nil
}

func workspaceMatchesTrustedRoots(workspace string, roots []string) bool {
	workspace = canonicalTrustRoot(workspace)
	entries := make([]trustresolver.AllowlistEntry, 0, len(roots))
	for _, root := range roots {
		if root = strings.TrimSpace(root); root != "" {
			if filepath.IsAbs(root) && !strings.ContainsAny(root, "*?") {
				root = canonicalTrustRoot(root)
			}
			entries = append(entries, trustresolver.AllowlistEntry{Pattern: root})
		}
	}
	worktree := ""
	if strings.TrimSpace(workspace) != "" {
		worktree = filepath.Join(workspace, ".git")
	}
	return trustresolver.New(trustresolver.Config{Allowlisted: entries}).Trusts(workspace, worktree)
}

func canonicalTrustRoot(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if resolved, err := filepath.EvalSymlinks(workspace); err == nil {
		workspace = resolved
	}
	if absolute, err := filepath.Abs(workspace); err == nil {
		workspace = absolute
	}
	return filepath.Clean(workspace)
}

func appendUniqueTrustRoot(roots []string, workspace string) []string {
	workspace = canonicalTrustRoot(workspace)
	out := make([]string, 0, len(roots)+1)
	found := false
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		out = append(out, root)
		if canonicalTrustRoot(root) == workspace {
			found = true
		}
	}
	if !found {
		out = append(out, workspace)
	}
	return out
}

type ExitError struct {
	Code   int
	Err    error
	Silent bool
}

func (e *ExitError) Error() string {
	if e == nil || e.Err == nil {
		return "exit"
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func isStatusCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "status", "/status":
		return true
	default:
		return false
	}
}

func isBootstrapPlanCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "bootstrap-plan":
		return true
	default:
		return false
	}
}

func isDeferredInitCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "deferred-init", "startup-report":
		return true
	default:
		return false
	}
}

func isPrefetchCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "prefetch", "/prefetch":
		return true
	default:
		return false
	}
}

func isCapabilitiesCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "capabilities", "/capabilities":
		return true
	default:
		return false
	}
}

func isConfigCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "config", "settings", "/config", "/settings":
		return true
	default:
		return false
	}
}

func isDoctorCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "doctor", "/doctor":
		return true
	default:
		return false
	}
}

func isMCPCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "mcp", "/mcp":
		return true
	default:
		return false
	}
}

func isPluginsCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "plugin", "plugins", "marketplace", "/plugin", "/plugins", "/marketplace":
		return true
	default:
		return false
	}
}

func isProvidersCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "providers", "/providers":
		return true
	default:
		return false
	}
}

func renderStatusWithConfigLoadError(out io.Writer, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, loadErr error) error {
	cfg, err := config.DiagnosticDefault(overrides)
	if err != nil {
		return renderCLIError(out, err, requestedOutputFormat(originalArgs))
	}
	applyStoredOAuthToken(&cfg, time.Now().UTC())
	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	statusArgs := append([]string(nil), rest...)
	if strings.EqualFold(strings.TrimSpace(command), "/status") {
		statusArgs = injectGlobalOutputFormat("status", statusArgs, requestedOutputFormat(originalArgs))
	}
	additionalDirs, err := pathscope.EffectiveDirs(workspace, cfg.AdditionalDirs)
	if err != nil {
		return err
	}
	app := &App{
		Config:              cfg,
		Client:              anthropicClientFromConfig(cfg),
		Tools:               tools.NewRegistryWithOptions(workspace, toolRegistryOptionsFromConfig(cfg, additionalDirs, os.Stdin, os.Stderr, "")),
		Sessions:            session.NewWorkspaceStore(cfg.ConfigHome, workspace),
		Workspace:           workspace,
		Out:                 out,
		Err:                 os.Stderr,
		In:                  os.Stdin,
		ConfigLoadError:     strings.TrimSpace(loadErr.Error()),
		ConfigLoadErrorKind: buildCLIErrorReport(loadErr).ErrorKind,
	}
	return app.Status(statusArgs, overrides)
}

func renderBootstrapPlanWithConfigLoadError(out io.Writer, rest []string, overrides config.FlagOverrides, originalArgs []string, loadErr error) error {
	cfg, err := config.DiagnosticDefault(overrides)
	if err != nil {
		return renderCLIError(out, err, requestedOutputFormat(originalArgs))
	}
	applyStoredOAuthToken(&cfg, time.Now().UTC())
	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	additionalDirs, err := pathscope.EffectiveDirs(workspace, cfg.AdditionalDirs)
	if err != nil {
		return err
	}
	app := &App{
		Config:              cfg,
		Client:              anthropicClientFromConfig(cfg),
		Tools:               tools.NewRegistryWithOptions(workspace, toolRegistryOptionsFromConfig(cfg, additionalDirs, os.Stdin, os.Stderr, "")),
		Sessions:            session.NewWorkspaceStore(cfg.ConfigHome, workspace),
		Workspace:           workspace,
		Out:                 out,
		Err:                 os.Stderr,
		In:                  os.Stdin,
		ConfigLoadError:     strings.TrimSpace(loadErr.Error()),
		ConfigLoadErrorKind: buildCLIErrorReport(loadErr).ErrorKind,
	}
	return app.BootstrapPlan(rest)
}

func renderDeferredInitWithConfigLoadError(out io.Writer, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, loadErr error) error {
	cfg, err := config.DiagnosticDefault(overrides)
	if err != nil {
		return renderCLIError(out, err, requestedOutputFormat(originalArgs))
	}
	applyStoredOAuthToken(&cfg, time.Now().UTC())
	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	additionalDirs, err := pathscope.EffectiveDirs(workspace, cfg.AdditionalDirs)
	if err != nil {
		return err
	}
	app := &App{
		Config:              cfg,
		Client:              anthropicClientFromConfig(cfg),
		Tools:               tools.NewRegistryWithOptions(workspace, toolRegistryOptionsFromConfig(cfg, additionalDirs, os.Stdin, os.Stderr, "")),
		Sessions:            session.NewWorkspaceStore(cfg.ConfigHome, workspace),
		Workspace:           workspace,
		Out:                 out,
		Err:                 os.Stderr,
		In:                  os.Stdin,
		ConfigLoadError:     strings.TrimSpace(loadErr.Error()),
		ConfigLoadErrorKind: buildCLIErrorReport(loadErr).ErrorKind,
	}
	return app.DeferredInit(command, rest)
}

func renderPrefetchWithConfigLoadError(out io.Writer, rest []string, overrides config.FlagOverrides, originalArgs []string, loadErr error) error {
	cfg, err := config.DiagnosticDefault(overrides)
	if err != nil {
		return renderCLIError(out, err, requestedOutputFormat(originalArgs))
	}
	applyStoredOAuthToken(&cfg, time.Now().UTC())
	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	prefetchArgs := append([]string(nil), rest...)
	if !argsHaveOutputFormat(prefetchArgs) {
		prefetchArgs = injectGlobalOutputFormat("prefetch", prefetchArgs, requestedOutputFormat(originalArgs))
	}
	additionalDirs, err := pathscope.EffectiveDirs(workspace, cfg.AdditionalDirs)
	if err != nil {
		return err
	}
	app := &App{
		Config:              cfg,
		Client:              anthropicClientFromConfig(cfg),
		Tools:               tools.NewRegistryWithOptions(workspace, toolRegistryOptionsFromConfig(cfg, additionalDirs, os.Stdin, os.Stderr, "")),
		Sessions:            session.NewWorkspaceStore(cfg.ConfigHome, workspace),
		Workspace:           workspace,
		Out:                 out,
		Err:                 os.Stderr,
		In:                  os.Stdin,
		ConfigLoadError:     strings.TrimSpace(loadErr.Error()),
		ConfigLoadErrorKind: buildCLIErrorReport(loadErr).ErrorKind,
	}
	return app.Prefetch(prefetchArgs)
}

func renderCapabilitiesWithConfigLoadError(out io.Writer, rest []string, overrides config.FlagOverrides, originalArgs []string) error {
	cfg, err := config.DiagnosticDefault(overrides)
	if err != nil {
		return renderCLIError(out, err, requestedOutputFormat(originalArgs))
	}
	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	capabilityArgs := append([]string(nil), rest...)
	if !argsHaveOutputFormat(capabilityArgs) {
		capabilityArgs = injectGlobalOutputFormat("capabilities", capabilityArgs, requestedOutputFormat(originalArgs))
	}
	additionalDirs, err := pathscope.EffectiveDirs(workspace, cfg.AdditionalDirs)
	if err != nil {
		return err
	}
	app := &App{
		Config:    cfg,
		Tools:     tools.NewRegistryWithOptions(workspace, toolRegistryOptionsFromConfig(cfg, additionalDirs, os.Stdin, os.Stderr, "")),
		Sessions:  session.NewWorkspaceStore(cfg.ConfigHome, workspace),
		Workspace: workspace,
		Out:       out,
		Err:       os.Stderr,
		In:        os.Stdin,
	}
	if err := app.Capabilities(capabilityArgs); err != nil {
		return renderCLIErrorWhenStructured(out, err, requestedOutputFormat(originalArgs))
	}
	return nil
}

func renderDoctorWithConfigLoadError(out io.Writer, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, loadErr error) error {
	cfg, err := config.DiagnosticDefault(overrides)
	if err != nil {
		return renderCLIError(out, err, requestedOutputFormat(originalArgs))
	}
	applyStoredOAuthToken(&cfg, time.Now().UTC())
	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	doctorArgs := append([]string(nil), rest...)
	if strings.EqualFold(strings.TrimSpace(command), "/doctor") {
		doctorArgs = injectGlobalOutputFormat("doctor", doctorArgs, requestedOutputFormat(originalArgs))
	}
	additionalDirs, err := pathscope.EffectiveDirs(workspace, cfg.AdditionalDirs)
	if err != nil {
		return err
	}
	app := &App{
		Config:              cfg,
		Client:              anthropicClientFromConfig(cfg),
		Tools:               tools.NewRegistryWithOptions(workspace, toolRegistryOptionsFromConfig(cfg, additionalDirs, os.Stdin, os.Stderr, "")),
		Sessions:            session.NewWorkspaceStore(cfg.ConfigHome, workspace),
		Workspace:           workspace,
		Out:                 out,
		Err:                 os.Stderr,
		In:                  os.Stdin,
		ConfigLoadError:     strings.TrimSpace(loadErr.Error()),
		ConfigLoadErrorKind: buildCLIErrorReport(loadErr).ErrorKind,
	}
	return app.Doctor(doctorArgs)
}

func renderConfigWithConfigLoadError(out io.Writer, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, loadErr error) error {
	configArgs := append([]string(nil), rest...)
	if !argsHaveOutputFormat(configArgs) {
		configArgs = injectGlobalOutputFormat("config", configArgs, requestedOutputFormat(originalArgs))
	}
	req, err := parseConfigInspectionArgs(configArgs)
	if err != nil {
		return renderCLIError(out, err, requestedOutputFormat(originalArgs))
	}
	cfg, err := config.DiagnosticDefault(overrides)
	if err != nil {
		return renderCLIError(out, err, req.Format)
	}
	_, paths, _ := config.LoadForInspection(overrides)
	if len(paths) == 0 {
		paths = configInspectionFallbackPaths(cfg.ConfigHome, overrides.ConfigPath)
	}
	report := buildConfigLoadReport(redactedConfig(cfg), paths, command, req.Args, loadErr)
	if err := renderConfigLoadReport(out, req.Format, report); err != nil {
		return err
	}
	exitErr := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	if req.Format == "json" {
		return &ExitError{Code: 1, Err: exitErr, Silent: true}
	}
	return &ExitError{Code: 1, Err: exitErr}
}

func renderMCPWithConfigLoadError(out io.Writer, command string, rest []string, originalArgs []string, loadErr error) error {
	mcpArgs := append([]string(nil), rest...)
	if !argsHaveOutputFormat(mcpArgs) {
		mcpArgs = injectGlobalOutputFormat("mcp", mcpArgs, requestedOutputFormat(originalArgs))
	}
	cleanArgs, format, err := stripJSONOnlyOutputFormat("mcp", mcpArgs)
	if err != nil {
		return renderCLIError(out, err, requestedOutputFormat(originalArgs))
	}
	if hasMCPHelpArg(cleanArgs) {
		renderMCPUsageReport(out, format, buildMCPUsageReport(mcpHelpUnexpected(cleanArgs)))
		return nil
	}
	requestedArgs := append([]string(nil), cleanArgs...)
	if len(cleanArgs) > 0 && !strings.HasPrefix(cleanArgs[0], "-") {
		cleanArgs[0] = normalizeMCPAction(cleanArgs[0])
	}
	if len(cleanArgs) == 0 || cleanArgs[0] == "list" {
		if len(cleanArgs) > 1 {
			return renderMCPUnsupportedAction(out, format, strings.Join(requestedArgs, " "), "list accepts no filter argument; use `codog mcp list`")
		}
		renderMCPListReport(out, format, buildMCPListReport(nil, buildMCPValidation(nil), strings.TrimSpace(loadErr.Error()), buildCLIErrorReport(loadErr).ErrorKind))
		return nil
	}
	if isMCPShowAction(cleanArgs[0]) {
		if len(cleanArgs) < 2 {
			return renderActionError(out, actionErrorReport{
				Kind:      "mcp",
				Action:    "show",
				Status:    "error",
				ErrorKind: "missing_argument",
				Message:   "mcp show requires a server name",
				Hint:      "Usage: codog mcp show <server>.",
			}, format)
		}
		if len(cleanArgs) > 2 {
			return renderCLIError(out, unexpectedExtraArgsError{
				Command: "mcp show",
				Args:    append([]string(nil), cleanArgs[2:]...),
				Usage:   "codog mcp show SERVER [--json|--output-format text|json]",
			}, format)
		}
		renderMCPShowReport(out, format, buildMCPShowReport(mcpShowReportOptions{
			Workspace:           currentWorkingDirectory(),
			ServerName:          cleanArgs[1],
			ConfigLoadError:     strings.TrimSpace(loadErr.Error()),
			ConfigLoadErrorKind: buildCLIErrorReport(loadErr).ErrorKind,
		}))
		return nil
	}
	return renderCLIError(out, loadErr, format)
}

func renderProvidersWithConfigLoadError(out io.Writer, command string, rest []string, overrides config.FlagOverrides, originalArgs []string, loadErr error) error {
	providerArgs := append([]string(nil), rest...)
	if !argsHaveOutputFormat(providerArgs) {
		providerArgs = injectGlobalOutputFormat("providers", providerArgs, requestedOutputFormat(originalArgs))
	}
	req, err := parseProviderCommandArgs(providerArgs)
	if err != nil {
		return renderCLIError(out, err, requestedOutputFormat(originalArgs))
	}
	if req.Action == "set" {
		return renderCLIError(out, loadErr, req.Format)
	}
	cfg, err := config.DiagnosticDefault(overrides)
	if err != nil {
		return renderCLIError(out, err, req.Format)
	}
	applyStoredOAuthToken(&cfg, time.Now().UTC())
	report, err := buildProvidersReport(cfg, req.Action)
	if err != nil {
		return err
	}
	report = withProviderConfigLoadError(report, loadErr)
	if req.Action == "show" {
		payload, err := providerShowPayload(report, req.Name)
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(payload, "", "  ")
			fmt.Fprintln(out, string(data))
			return nil
		}
		renderProviderShowText(out, payload)
		return nil
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	renderProvidersText(out, report)
	return nil
}

func renderPluginsWithConfigLoadError(out io.Writer, command string, rest []string, originalArgs []string, loadErr error) error {
	pluginArgs := append([]string(nil), rest...)
	if !argsHaveOutputFormat(pluginArgs) {
		pluginArgs = injectGlobalOutputFormat("plugins", pluginArgs, requestedOutputFormat(originalArgs))
	}
	cleanArgs, format, err := stripJSONOnlyOutputFormat("plugins", pluginArgs)
	if err != nil {
		return renderCLIError(out, err, requestedOutputFormat(originalArgs))
	}
	if len(cleanArgs) == 0 || cleanArgs[0] == "list" {
		if len(cleanArgs) > 1 {
			if option := firstFlagShapedArg(cleanArgs[1:]); option != "" {
				return renderCLIError(out, unknownOptionError{
					Kind:    "cli_parse",
					Command: "plugins list",
					Option:  option,
					Usage:   "codog plugins list [--json|--output-format text|json]",
				}, format)
			}
			return renderCLIError(out, unexpectedExtraArgsError{
				Command: "plugins list",
				Args:    append([]string(nil), cleanArgs[1:]...),
				Usage:   "codog plugins list [--json|--output-format text|json]",
			}, format)
		}
		workspace, err := os.Getwd()
		if err != nil {
			return err
		}
		manifests, err := plugins.Load(workspace)
		if err != nil {
			return err
		}
		report := buildPluginsListReport(manifests, strings.TrimSpace(loadErr.Error()), buildCLIErrorReport(loadErr).ErrorKind)
		renderPluginsListReport(out, format, report)
		return nil
	}
	return renderCLIError(out, loadErr, format)
}

func configInspectionFallbackPaths(configHome string, explicit string) []string {
	if strings.TrimSpace(explicit) != "" {
		return []string{explicit}
	}
	return []string{
		filepath.Join(configHome, "config.json"),
		filepath.Join(".claude", "settings.json"),
		filepath.Join(".claude", "settings.local.json"),
		filepath.Join(".omc", "settings.json"),
		filepath.Join(".omc", "settings.local.json"),
		filepath.Join(".omc", "config.json"),
		filepath.Join(".claw", "settings.json"),
		filepath.Join(".claw", "settings.local.json"),
		filepath.Join(".claw", "config.json"),
		".codog.json",
		".codog.local.json",
	}
}

func applyStoredOAuthToken(cfg *config.Config, now time.Time) {
	if cfg.AuthToken != "" {
		return
	}
	token, err := oauth.LoadToken(cfg.ConfigHome)
	if err != nil {
		return
	}
	if token.Expired(now) {
		if token.RefreshToken == "" {
			return
		}
		refreshed, err := oauth.RefreshStoredToken(context.Background(), cfg.ConfigHome, cfg.OAuthProfile)
		if err != nil || refreshed.Expired(now) {
			return
		}
		token = refreshed
	}
	cfg.AuthToken = token.AccessToken
}

func applyPluginHookConfigsFromManifests(cfg *config.Config, manifests []plugins.Manifest) error {
	if cfg.EffectiveAllowManagedHooksOnly() {
		cfg.Hooks = config.HookConfig{}
	}
	files, err := plugins.LoadHookConfigsFromManifests(manifests)
	if err != nil {
		return err
	}
	for _, file := range files {
		config.MergeHookConfig(&cfg.Hooks, file.Config)
	}
	return nil
}

func applyPluginMCPServers(cfg *config.Config, workspace string) error {
	manifests, err := plugins.Load(workspace)
	if err != nil {
		return err
	}
	return applyPluginMCPServersFromManifests(cfg, manifests)
}

func applyPluginMCPServersFromManifests(cfg *config.Config, manifests []plugins.Manifest) error {
	servers := plugins.LoadMCPServersFromManifests(manifests)
	if len(servers) == 0 {
		return nil
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]config.MCPServerConfig{}
	}
	for name, server := range servers {
		if _, exists := cfg.MCPServers[name]; exists {
			continue
		}
		cfg.MCPServers[name] = server
	}
	return nil
}

func anthropicRateLimitOptions(cfg config.RateLimitConfig) anthropic.RateLimitOptions {
	cfg = config.NormalizeRateLimitConfig(cfg)
	return anthropic.RateLimitOptions{
		MaxRetries:     cfg.MaxRetries,
		InitialBackoff: time.Duration(cfg.InitialBackoffMS) * time.Millisecond,
		MaxBackoff:     time.Duration(cfg.MaxBackoffMS) * time.Millisecond,
	}
}

func anthropicRateLimitOptionsFromConfig(cfg config.Config) anthropic.RateLimitOptions {
	options := anthropicRateLimitOptions(cfg.RateLimit)
	if cfg.APITimeout.MaxRetries > 0 {
		options.MaxRetries = cfg.APITimeout.MaxRetries
	}
	return options
}

func anthropicClientOptionsFromConfig(cfg config.Config) anthropic.ClientOptions {
	options := anthropic.ClientOptions{
		RateLimit:             anthropicRateLimitOptionsFromConfig(cfg),
		ForceOpenAICompatible: cfg.RuntimeProvider == modelrouting.ProviderOpenAI,
		Fallbacks: anthropic.ProviderFallbackOptions{
			Primary: cfg.ProviderFallbacks.Primary,
			Models:  append([]string(nil), cfg.ProviderFallbacks.Fallbacks...),
		},
	}
	if cfg.APITimeout.RequestTimeoutSeconds > 0 {
		options.RequestTimeout = time.Duration(cfg.APITimeout.RequestTimeoutSeconds) * time.Second
	}
	if cfg.APITimeout.ConnectTimeoutSeconds > 0 {
		options.ConnectTimeout = time.Duration(cfg.APITimeout.ConnectTimeoutSeconds) * time.Second
	}
	return options
}

func anthropicClientFromConfig(cfg config.Config) *anthropic.Client {
	return anthropic.NewWithOptions(cfg.BaseURL, cfg.APIKey, cfg.AuthToken, anthropicClientOptionsFromConfig(cfg))
}

func toolRegistryOptionsFromConfig(cfg config.Config, additionalDirs []string, questionIn io.Reader, questionOut io.Writer, executable string, agentDefinitions ...[]agentdefs.Definition) tools.RegistryOptions {
	var ragTimeout time.Duration
	if cfg.RAGTimeoutSeconds > 0 {
		ragTimeout = time.Duration(cfg.RAGTimeoutSeconds) * time.Second
	}
	options := tools.RegistryOptions{
		SandboxStrategy:  cfg.Future.SandboxStrategy,
		Sandbox:          cfg.Future.Sandbox,
		AdditionalDirs:   additionalDirs,
		ConfigHome:       cfg.ConfigHome,
		ConfigEnv:        cfg.Env,
		Executable:       executable,
		DefaultShell:     cfg.DefaultShell,
		TrustedRoots:     cfg.TrustedRoots,
		RespectGitignore: cfg.EffectiveRespectGitignore(),
		OAuthProfile:     cfg.OAuthProfile,
		MCPServers:       cfg.MCPServers,
		RAGBaseURL:       cfg.RAGBaseURL,
		RAGTimeout:       ragTimeout,
		RAGTopKMax:       cfg.RAGTopKMax,
		QuestionIn:       questionIn,
		QuestionOut:      questionOut,
	}
	if len(agentDefinitions) > 0 {
		options.AgentDefinitions = append([]agentdefs.Definition(nil), agentDefinitions[0]...)
	}
	return options
}

func (a *App) Remote(args []string) error {
	meaningful := routeMeaningfulArgs(args)
	if len(meaningful) == 0 || !strings.EqualFold(strings.TrimSpace(meaningful[0]), "serve") {
		return a.RemoteSetup(args, config.FlagOverrides{})
	}
	addr := "127.0.0.1:8791"
	serveArgs, err := resumedRemoteServeArgs(args)
	if err != nil {
		return renderCLIError(a.Out, err, requestedOutputFormat(args))
	}
	if len(serveArgs) > 1 {
		addr = serveArgs[1]
	}
	return a.serveRemoteControl(context.Background(), addr)
}

func (a *App) remoteControlServerWithMaxSessions(addr string, maxSessions int) control.Server {
	executable, _ := os.Executable()
	runtimeReport := remoteruntime.InspectEnv(remoteruntime.Env(), remoteProxyPortFromAddr(addr))
	remoteEnv := []string(nil)
	if len(runtimeReport.SubprocessEnv) > 0 {
		remoteEnv = remoteruntime.MergeEnv(os.Environ(), runtimeReport.SubprocessEnv)
	}
	return control.Server{
		Sessions:    a.Sessions,
		ConfigHome:  a.Config.ConfigHome,
		Workspace:   a.Workspace,
		AuthToken:   a.Config.Future.RemoteAuthToken,
		MaxSessions: maxSessions,
		Hooks:       a.Config.Hooks,
		MCPServers:  a.Config.MCPServers,
		LSPClients:  a.lspClientPool(),
		LeaseTTL:    time.Duration(a.Config.Future.RemoteLeaseSeconds) * time.Second,
		Executable:  executable,
		EditorToken: a.Config.Future.EditorBridgeToken,
		RemoteEnv:   remoteEnv,
	}
}

func (a *App) serveRemoteControl(ctx context.Context, addr string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	normalizedAddr, remoteURL, err := normalizeRemoteHandoffAddr(addr)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", normalizedAddr)
	if err != nil {
		return err
	}
	actualAddr := listener.Addr().String()
	_, actualURL, urlErr := normalizeRemoteHandoffAddr(actualAddr)
	if urlErr == nil {
		remoteURL = actualURL
	}
	fmt.Fprintf(a.Err, "codog remote control listening on %s\n", remoteURL)
	return a.serveControlListener(ctx, listener, actualAddr)
}

type apiRequest struct {
	Action  string
	Format  string
	Addr    string
	AddrSet bool
}

type apiReport struct {
	Kind                string              `json:"kind"`
	Action              string              `json:"action"`
	Status              string              `json:"status"`
	Workspace           string              `json:"workspace,omitempty"`
	Enabled             bool                `json:"enabled"`
	Ready               bool                `json:"ready"`
	AuthTokenConfigured bool                `json:"auth_token_configured"`
	AuthRequired        bool                `json:"auth_required"`
	LeaseSeconds        int                 `json:"lease_seconds"`
	RemoteCommand       string              `json:"remote_command"`
	RemoteAddr          string              `json:"remote_addr"`
	RemoteURL           string              `json:"remote_url"`
	HealthURL           string              `json:"health_url"`
	StateURL            string              `json:"state_url"`
	RoutesURL           string              `json:"routes_url"`
	CapabilitiesURL     string              `json:"capabilities_url"`
	Listening           bool                `json:"listening"`
	RouteCount          int                 `json:"route_count"`
	PublicRouteCount    int                 `json:"public_route_count"`
	StreamingRouteCount int                 `json:"streaming_route_count"`
	Routes              []control.RouteSpec `json:"routes"`
	Messages            []string            `json:"messages,omitempty"`
}

func (a *App) API(args []string) error {
	return a.APIContext(context.Background(), args)
}

func (a *App) APIContext(ctx context.Context, args []string) error {
	req, err := parseAPIArgs(args)
	if err != nil {
		return err
	}
	addr, remoteURL, err := normalizeRemoteHandoffAddr(req.Addr)
	if err != nil {
		return err
	}
	if req.Action == "serve" {
		return a.serveAPI(ctx, req, addr)
	}
	report := a.buildAPIReport(req, addr, remoteURL)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderAPIReport(a.Out, report)
	return nil
}

func (a *App) serveAPI(ctx context.Context, req apiRequest, addr string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	actualAddr := listener.Addr().String()
	_, remoteURL, err := normalizeRemoteHandoffAddr(actualAddr)
	if err != nil {
		_ = listener.Close()
		return err
	}
	report := a.buildAPIReport(req, actualAddr, remoteURL)
	report.Status = "serving"
	report.Enabled = true
	report.Ready = true
	report.Listening = true
	report.RemoteCommand = "codog api serve " + actualAddr
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
	} else {
		renderAPIReport(a.Out, report)
	}
	fmt.Fprintf(a.Err, "codog api listening on %s\n", remoteURL)
	return a.serveControlListener(ctx, listener, actualAddr)
}

type serverRequest struct {
	Host          string
	Port          string
	AuthToken     string
	Unix          string
	Workspace     string
	IdleTimeoutMS int
	MaxSessions   int
	Format        string
}

type serverReport struct {
	Kind                string   `json:"kind"`
	Action              string   `json:"action"`
	Status              string   `json:"status"`
	Workspace           string   `json:"workspace,omitempty"`
	Network             string   `json:"network"`
	Addr                string   `json:"addr"`
	HTTPURL             string   `json:"http_url"`
	HealthURL           string   `json:"health_url,omitempty"`
	RoutesURL           string   `json:"routes_url,omitempty"`
	CapabilitiesURL     string   `json:"capabilities_url,omitempty"`
	AuthTokenConfigured bool     `json:"auth_token_configured"`
	AuthToken           string   `json:"auth_token,omitempty"`
	IdleTimeoutMS       int      `json:"idle_timeout_ms"`
	MaxSessions         int      `json:"max_sessions"`
	MaxSessionsEnforced bool     `json:"max_sessions_enforced"`
	RouteCount          int      `json:"route_count"`
	Routes              []string `json:"routes,omitempty"`
	Messages            []string `json:"messages,omitempty"`
}

const serverUsage = "codog server [--host HOST] [--port PORT] [--auth-token TOKEN] [--unix PATH] [--workspace DIR] [--idle-timeout MS] [--max-sessions N] [--output-format text|json]"

func (a *App) Server(ctx context.Context, args []string) error {
	req, err := parseServerArgs(args)
	if err != nil {
		return err
	}
	if req.Workspace != "" {
		if err := a.useServerWorkspace(req.Workspace); err != nil {
			return err
		}
	}
	if strings.TrimSpace(req.AuthToken) == "" {
		token, err := generateServerAuthToken()
		if err != nil {
			return err
		}
		req.AuthToken = token
	}
	a.Config.Future.RemoteEnabled = true
	a.Config.Future.RemoteAuthToken = req.AuthToken
	if req.IdleTimeoutMS > 0 {
		a.Config.Future.RemoteLeaseSeconds = int(math.Ceil(float64(req.IdleTimeoutMS) / 1000))
	}
	if strings.TrimSpace(req.Unix) != "" {
		return a.serveUnixServer(ctx, req)
	}
	return a.serveTCPServer(ctx, req)
}

func parseServerArgs(args []string) (serverRequest, error) {
	parser := serverArgParser{req: serverRequest{
		Host:          "0.0.0.0",
		Port:          "0",
		IdleTimeoutMS: 600000,
		MaxSessions:   32,
		Format:        "text",
	}}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--json" {
			parser.req.Format = "json"
			continue
		}
		handled, err := consumeValueOption(args, &index, parser.valueOptions())
		if err != nil {
			return parser.req, err
		}
		if !handled {
			return parser.req, serverArgumentError(arg)
		}
	}
	if err := validateServerRequest(&parser.req); err != nil {
		return parser.req, err
	}
	return parser.req, nil
}

type serverArgParser struct {
	req serverRequest
}

func (p *serverArgParser) valueOptions() map[string]valueOption {
	return map[string]valueOption{
		"--output-format": p.stringOption(&p.req.Format, false, false),
		"-o":              p.stringOption(&p.req.Format, false, false),
		"--host":          p.stringOption(&p.req.Host, true, false),
		"--port":          p.stringOption(&p.req.Port, true, false),
		"--auth-token":    p.stringOption(&p.req.AuthToken, true, true),
		"--unix":          p.stringOption(&p.req.Unix, true, false),
		"--workspace":     p.stringOption(&p.req.Workspace, true, false),
		"--idle-timeout":  p.numberOption("--idle-timeout", &p.req.IdleTimeoutMS),
		"--max-sessions":  p.numberOption("--max-sessions", &p.req.MaxSessions),
	}
}

func (p *serverArgParser) stringOption(target *string, rejectOutputFormat bool, trim bool) valueOption {
	return valueOption{
		missing:            serverMissingValueError,
		rejectOutputFormat: rejectOutputFormat,
		set: func(value string) error {
			if trim {
				value = strings.TrimSpace(value)
			}
			*target = value
			return nil
		},
	}
}

func (p *serverArgParser) numberOption(flag string, target *int) valueOption {
	return valueOption{
		missing:            serverMissingValueError,
		rejectOutputFormat: true,
		set: func(value string) error {
			parsed, err := parseNonNegativeServerInt(flag, value)
			if err != nil {
				return err
			}
			*target = parsed
			return nil
		},
	}
}

func serverMissingValueError(flag string) error {
	return missingFlagValueError{Command: "server", Flag: flag, Usage: serverUsage}
}

func serverArgumentError(arg string) error {
	if strings.HasPrefix(arg, "-") {
		return unknownOptionError{Command: "server", Option: arg, Usage: serverUsage}
	}
	return unexpectedExtraArgsError{Command: "server", Args: []string{arg}, Usage: serverUsage}
}

func validateServerRequest(req *serverRequest) error {
	if strings.TrimSpace(req.Host) == "" {
		return invalidFlagValueError{Flag: "--host", Value: req.Host, Message: "host must not be empty"}
	}
	if strings.TrimSpace(req.Port) == "" {
		return invalidFlagValueError{Flag: "--port", Value: req.Port, Message: "port must not be empty"}
	}
	if _, err := strconv.Atoi(req.Port); err != nil {
		return invalidFlagValueError{Flag: "--port", Value: req.Port, Message: "port must be an integer"}
	}
	if strings.TrimSpace(req.Unix) != "" && (req.Host != "0.0.0.0" || req.Port != "0") {
		return invalidFlagValueError{Flag: "--unix", Value: req.Unix, Message: "--unix cannot be combined with --host or --port"}
	}
	normalizedFormat, err := normalizeOutputFormat("server", req.Format, []string{"text", "json"})
	if err != nil {
		return err
	}
	req.Format = normalizedFormat
	return nil
}

func parseNonNegativeServerInt(flag string, value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return 0, invalidFlagValueError{Flag: flag, Value: value, Message: strings.TrimPrefix(flag, "--") + " must be a non-negative integer"}
	}
	return parsed, nil
}

func (a *App) useServerWorkspace(workspace string) error {
	resolved, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace %q is not a directory", workspace)
	}
	a.Workspace = resolved
	a.Sessions = session.NewWorkspaceStore(a.Config.ConfigHome, resolved)
	return nil
}

func (a *App) serveTCPServer(ctx context.Context, req serverRequest) error {
	addr := net.JoinHostPort(req.Host, req.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	actualAddr := listener.Addr().String()
	_, remoteURL, err := normalizeRemoteHandoffAddr(actualAddr)
	if err != nil {
		_ = listener.Close()
		return err
	}
	report := a.buildServerReport(req, "tcp", actualAddr, remoteURL)
	if err := renderServerReport(a.Out, report, req.Format); err != nil {
		_ = listener.Close()
		return err
	}
	if a.Err != nil {
		fmt.Fprintf(a.Err, "codog server listening on %s\n", remoteURL)
	}
	return a.serveControlListenerWithOptions(ctx, listener, actualAddr, controlListenerOptions{
		MaxSessions: req.MaxSessions,
		IdleTimeout: time.Duration(req.IdleTimeoutMS) * time.Millisecond,
	})
}

func (a *App) serveUnixServer(ctx context.Context, req serverRequest) error {
	socketPath := a.resolveOutputPath(req.Unix)
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return err
	}
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(socketPath) }()
	remoteURL := "unix:" + socketPath
	report := a.buildServerReport(req, "unix", socketPath, remoteURL)
	if err := renderServerReport(a.Out, report, req.Format); err != nil {
		_ = listener.Close()
		return err
	}
	if a.Err != nil {
		fmt.Fprintf(a.Err, "codog server listening on %s\n", remoteURL)
	}
	return a.serveControlListenerWithOptions(ctx, listener, socketPath, controlListenerOptions{
		MaxSessions: req.MaxSessions,
		IdleTimeout: time.Duration(req.IdleTimeoutMS) * time.Millisecond,
	})
}

func (a *App) buildServerReport(req serverRequest, network, addr, httpURL string) serverReport {
	routes := control.RouteSpecs()
	routePaths := make([]string, 0, len(routes))
	for _, route := range routes {
		routePaths = append(routePaths, route.Path)
	}
	report := serverReport{
		Kind:                "server",
		Action:              "serve",
		Status:              "serving",
		Workspace:           a.Workspace,
		Network:             network,
		Addr:                addr,
		HTTPURL:             httpURL,
		AuthTokenConfigured: strings.TrimSpace(req.AuthToken) != "",
		AuthToken:           req.AuthToken,
		IdleTimeoutMS:       req.IdleTimeoutMS,
		MaxSessions:         req.MaxSessions,
		MaxSessionsEnforced: req.MaxSessions > 0,
		RouteCount:          len(routes),
		Routes:              routePaths,
	}
	if strings.HasPrefix(httpURL, "http://") || strings.HasPrefix(httpURL, "https://") {
		baseURL := strings.TrimRight(httpURL, "/")
		report.HealthURL = baseURL + "/health"
		report.RoutesURL = baseURL + "/routes"
		report.CapabilitiesURL = baseURL + "/capabilities"
	}
	return report
}

func renderServerReport(out io.Writer, report serverReport, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Server")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Workspace        %s\n", report.Workspace)
	fmt.Fprintf(out, "  Network          %s\n", report.Network)
	fmt.Fprintf(out, "  Address          %s\n", report.Addr)
	fmt.Fprintf(out, "  URL              %s\n", report.HTTPURL)
	if report.HealthURL != "" {
		fmt.Fprintf(out, "  Health URL       %s\n", report.HealthURL)
	}
	if report.RoutesURL != "" {
		fmt.Fprintf(out, "  Routes URL       %s\n", report.RoutesURL)
	}
	if report.CapabilitiesURL != "" {
		fmt.Fprintf(out, "  Capabilities URL %s\n", report.CapabilitiesURL)
	}
	fmt.Fprintf(out, "  Auth token       %s\n", report.AuthToken)
	fmt.Fprintf(out, "  Idle timeout     %d ms\n", report.IdleTimeoutMS)
	fmt.Fprintf(out, "  Max sessions     %d\n", report.MaxSessions)
	fmt.Fprintf(out, "  Routes           %d\n", report.RouteCount)
	for _, message := range report.Messages {
		fmt.Fprintf(out, "  Note             %s\n", message)
	}
	return nil
}

func generateServerAuthToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "sk-ant-cc-" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

type openRequest struct {
	RawURL string
	Print  bool
	Prompt string
	Format string
}

type openTarget struct {
	ServerURL string
	AuthToken string
	UnixPath  string
}

type openReport struct {
	Kind                string         `json:"kind"`
	Action              string         `json:"action"`
	Status              string         `json:"status"`
	ServerURL           string         `json:"server_url"`
	SessionID           string         `json:"session_id,omitempty"`
	WorkDir             string         `json:"work_dir,omitempty"`
	AuthTokenConfigured bool           `json:"auth_token_configured"`
	Print               bool           `json:"print"`
	PromptSubmitted     bool           `json:"prompt_submitted"`
	PromptTask          map[string]any `json:"prompt_task,omitempty"`
	Message             string         `json:"message,omitempty"`
}

type sshRequest struct {
	Host                       string
	Directory                  string
	Print                      bool
	Prompt                     string
	ExtraArgs                  []string
	PermissionMode             string
	PlanModeRequired           bool
	DangerouslySkipPermissions bool
	Local                      bool
	Execute                    bool
	Format                     string
}

type sshReport struct {
	Kind                       string   `json:"kind"`
	Action                     string   `json:"action"`
	Status                     string   `json:"status"`
	Host                       string   `json:"host"`
	Directory                  string   `json:"directory,omitempty"`
	Local                      bool     `json:"local"`
	Print                      bool     `json:"print,omitempty"`
	PromptConfigured           bool     `json:"prompt_configured,omitempty"`
	ExtraArgs                  []string `json:"extra_args,omitempty"`
	PermissionMode             string   `json:"permission_mode,omitempty"`
	PlanModeRequired           bool     `json:"plan_mode_required,omitempty"`
	DangerouslySkipPermissions bool     `json:"dangerously_skip_permissions,omitempty"`
	Command                    []string `json:"command"`
	RemoteShell                string   `json:"remote_shell,omitempty"`
	RemoteEnvKeys              []string `json:"remote_env_keys,omitempty"`
	RemoteAuthForwarded        bool     `json:"remote_auth_forwarded"`
	RemoteExecutable           string   `json:"remote_executable,omitempty"`
	DeployCommand              []string `json:"deploy_command,omitempty"`
	Executed                   bool     `json:"executed"`
	ExitCode                   *int     `json:"exit_code,omitempty"`
	DurationMS                 int64    `json:"duration_ms,omitempty"`
	Stdout                     string   `json:"stdout,omitempty"`
	Stderr                     string   `json:"stderr,omitempty"`
	Error                      string   `json:"error,omitempty"`
	Message                    string   `json:"message,omitempty"`
}

func normalizeDirectConnectInvocation(args []string) []string {
	directURLIndex := -1
	for index, arg := range args {
		if isDirectConnectURLArg(arg) {
			directURLIndex = index
			break
		}
	}
	if directURLIndex == -1 || hasCommandBeforeDirectConnectURL(args, directURLIndex) {
		return args
	}
	directURL := args[directURLIndex]
	prefix := []string{}
	openArgs := []string{directURL}
	for index := 0; index < len(args); index++ {
		if index == directURLIndex {
			continue
		}
		arg := args[index]
		if arg == "--dangerously-skip-permissions" {
			continue
		}
		if index < directURLIndex && strings.HasPrefix(arg, "-") && !isOpenPrintFlag(arg) {
			prefix = append(prefix, arg)
			if globalFlagTakesValue(arg) && !strings.Contains(arg, "=") && index+1 < directURLIndex {
				index++
				prefix = append(prefix, args[index])
			}
			continue
		}
		openArgs = append(openArgs, arg)
	}
	normalized := append([]string{}, prefix...)
	normalized = append(normalized, "open")
	normalized = append(normalized, openArgs...)
	return normalized
}

func isDirectConnectURLArg(arg string) bool {
	return strings.HasPrefix(arg, "cc://") || strings.HasPrefix(arg, "cc+unix://")
}

func isOpenPrintFlag(arg string) bool {
	return arg == "-p" || arg == "--print" || strings.HasPrefix(arg, "-p=") || strings.HasPrefix(arg, "--print=")
}

func hasCommandBeforeDirectConnectURL(args []string, directURLIndex int) bool {
	for index := 0; index < directURLIndex; index++ {
		arg := args[index]
		if arg == "--dangerously-skip-permissions" {
			continue
		}
		if isOpenPrintFlag(arg) {
			if (arg == "-p" || arg == "--print") && index+1 < directURLIndex && !strings.HasPrefix(args[index+1], "-") {
				index++
			}
			continue
		}
		if globalFlagTakesValue(arg) {
			if !strings.Contains(arg, "=") && index+1 < directURLIndex {
				index++
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return true
	}
	return false
}

const openUsage = "codog open <cc-url|http-url> [-p|--print [PROMPT]] [--output-format text|json|stream-json]"
const sshUsage = "codog ssh <host> [dir] [-p|--print [PROMPT]] [--continue|-c] [--resume ID|latest] [--model MODEL] [--permission-mode MODE] [--plan-mode-required] [--dangerously-skip-permissions] [--local] [--execute] [--json|--output-format text|json]"

func (a *App) Open(ctx context.Context, args []string) error {
	req, err := parseOpenArgs(args)
	if err != nil {
		return err
	}
	target, err := parseOpenTarget(req.RawURL)
	if err != nil {
		return err
	}
	client := openHTTPClient(target)
	sessionID, workDir, err := createOpenSession(ctx, client, target, currentWorkingDirectory())
	if err != nil {
		return err
	}
	report := openReport{
		Kind:                "open",
		Action:              "connect",
		Status:              "connected",
		ServerURL:           target.ServerURL,
		SessionID:           sessionID,
		WorkDir:             workDir,
		AuthTokenConfigured: strings.TrimSpace(target.AuthToken) != "",
		Print:               req.Print,
		Message:             "Connected to Codog control server.",
	}
	if strings.TrimSpace(req.Prompt) != "" {
		task, err := submitOpenPrompt(ctx, client, target, sessionID, req.Prompt)
		if err != nil {
			return err
		}
		report.PromptSubmitted = true
		report.PromptTask = task
		report.Message = "Connected to Codog control server and submitted prompt."
	}
	return renderOpenReport(a.Out, report, req.Format)
}

func (a *App) SSH(ctx context.Context, args []string) error {
	req, err := parseSSHArgs(args)
	if err != nil {
		return err
	}
	report := a.buildSSHReport(req)
	if req.Format == "json" {
		if req.Execute {
			var runErr error
			report, runErr = a.runSSHCommandReport(ctx, req, report)
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return runErr
		}
		report.Message = "SSH execution plan generated. Pass --execute with --json to start the remote session."
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	return a.runSSHCommand(ctx, req)
}

func parseSSHArgs(args []string) (sshRequest, error) {
	parser := sshArgParser{req: sshRequest{Format: "text"}}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if parser.consumeBoolean(arg) || parser.consumePrint(args, &index) {
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
			return parser.req, unknownOptionError{Command: "ssh", Option: arg, Usage: sshUsage}
		}
		parser.positionals = append(parser.positionals, arg)
	}
	if err := parser.finish(); err != nil {
		return parser.req, err
	}
	return parser.req, nil
}

type sshArgParser struct {
	req         sshRequest
	positionals []string
}

func (p *sshArgParser) consumeBoolean(arg string) bool {
	switch arg {
	case "--json":
		p.req.Format = "json"
	case "--plan-mode-required":
		p.req.PlanModeRequired = true
	case "-c", "--continue":
		p.req.ExtraArgs = append(p.req.ExtraArgs, "--continue")
	case "--dangerously-skip-permissions", "--skip-permissions":
		p.req.DangerouslySkipPermissions = true
	case "--local":
		p.req.Local = true
	case "--execute", "--run":
		p.req.Execute = true
	default:
		return false
	}
	return true
}

func (p *sshArgParser) consumePrint(args []string, index *int) bool {
	arg := args[*index]
	switch {
	case arg == "-p" || arg == "--print":
		p.req.Print = true
		if *index+1 < len(args) && !strings.HasPrefix(args[*index+1], "-") {
			(*index)++
			p.req.Prompt = args[*index]
		}
		return true
	case strings.HasPrefix(arg, "-p="):
		p.req.Print, p.req.Prompt = true, strings.TrimPrefix(arg, "-p=")
		return true
	case strings.HasPrefix(arg, "--print="):
		p.req.Print, p.req.Prompt = true, strings.TrimPrefix(arg, "--print=")
		return true
	default:
		return false
	}
}

func (p *sshArgParser) valueOptions() map[string]valueOption {
	return map[string]valueOption{
		"--output-format":   p.stringOption(&p.req.Format, false),
		"-o":                p.stringOption(&p.req.Format, false),
		"--permission-mode": p.stringOption(&p.req.PermissionMode, true),
		"--resume":          p.forwardedOption("--resume"),
		"--model":           p.forwardedOption("--model"),
	}
}

func (p *sshArgParser) stringOption(target *string, trim bool) valueOption {
	return valueOption{missing: sshMissingValue, rejectEmptySeparate: true, rejectOutputFormat: true, set: func(value string) error {
		if trim {
			value = strings.TrimSpace(value)
		}
		*target = value
		return nil
	}}
}

func (p *sshArgParser) forwardedOption(name string) valueOption {
	return valueOption{missing: sshMissingValue, rejectEmptySeparate: true, rejectOutputFormat: true, set: func(value string) error {
		value = strings.TrimSpace(value)
		if value == "" {
			return sshMissingValue(name)
		}
		p.req.ExtraArgs = append(p.req.ExtraArgs, name, value)
		return nil
	}}
}

func sshMissingValue(flag string) error {
	return missingFlagValueError{Command: "ssh", Flag: flag, Usage: sshUsage}
}

func (p *sshArgParser) finish() error {
	if len(p.positionals) == 0 || strings.TrimSpace(p.positionals[0]) == "" {
		return requiredArgumentError{Command: "ssh", Argument: "host", Usage: sshUsage}
	}
	if len(p.positionals) > 2 {
		return unexpectedExtraArgsError{Command: "ssh", Args: p.positionals[2:], Usage: sshUsage}
	}
	p.req.Host = strings.TrimSpace(p.positionals[0])
	if len(p.positionals) == 2 {
		p.req.Directory = strings.TrimSpace(p.positionals[1])
	}
	if p.req.PermissionMode != "" && !validPermissionMode(p.req.PermissionMode) {
		return fmt.Errorf("invalid --permission-mode %q; expected read-only, workspace-write, danger-full-access, prompt, or allow", p.req.PermissionMode)
	}
	normalizedFormat, err := normalizeOutputFormat("ssh", p.req.Format, []string{"text", "json"})
	if err != nil {
		return err
	}
	p.req.Format = normalizedFormat
	return nil
}

func (a *App) buildSSHReport(req sshRequest) sshReport {
	command, remoteShell := a.sshCommand(req, true)
	remoteEnv := a.sshRemoteEnv(req, true)
	deployCommand, remoteExecutable := a.sshDeployCommand(req)
	return sshReport{
		Kind:                       "ssh",
		Action:                     "connect",
		Status:                     "planned",
		Host:                       req.Host,
		Directory:                  req.Directory,
		Local:                      req.Local,
		Print:                      req.Print,
		PromptConfigured:           strings.TrimSpace(req.Prompt) != "",
		ExtraArgs:                  append([]string(nil), req.ExtraArgs...),
		PermissionMode:             req.PermissionMode,
		PlanModeRequired:           req.PlanModeRequired,
		DangerouslySkipPermissions: req.DangerouslySkipPermissions,
		Command:                    command,
		RemoteShell:                remoteShell,
		RemoteEnvKeys:              sshRemoteEnvKeys(remoteEnv),
		RemoteAuthForwarded:        sshRemoteAuthForwarded(remoteEnv),
		RemoteExecutable:           remoteExecutable,
		DeployCommand:              deployCommand,
		Executed:                   false,
	}
}

func (a *App) runSSHCommandReport(ctx context.Context, req sshRequest, report sshReport) (sshReport, error) {
	report.Executed = true
	start := time.Now()
	command, _ := a.sshCommand(req, false)
	if len(command) == 0 {
		err := errors.New("ssh command is empty")
		report.Status = "failed"
		report.Error = err.Error()
		report.Message = "SSH command failed."
		report.DurationMS = time.Since(start).Milliseconds()
		return report, err
	}
	if !req.Local {
		if err := a.deploySSHBinary(ctx, req); err != nil {
			report.Status = "failed"
			report.Error = err.Error()
			report.Message = "SSH deploy failed."
			report.DurationMS = time.Since(start).Milliseconds()
			return report, err
		}
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	if req.Local && strings.TrimSpace(req.Directory) != "" {
		cmd.Dir = req.Directory
	}
	cmd.Stdin = a.In
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	report.DurationMS = time.Since(start).Milliseconds()
	report.Stdout = stdout.String()
	report.Stderr = stderr.String()
	exitCode := sshCommandExitCode(err)
	report.ExitCode = &exitCode
	switch {
	case err == nil:
		report.Status = "completed"
		report.Message = "SSH command completed."
		return report, nil
	case ctx.Err() != nil:
		report.Status = "canceled"
		report.Error = ctx.Err().Error()
		report.Message = "SSH command canceled."
		return report, err
	default:
		report.Status = "failed"
		report.Error = err.Error()
		report.Message = "SSH command failed."
		return report, err
	}
}

func (a *App) runSSHCommand(ctx context.Context, req sshRequest) error {
	command, _ := a.sshCommand(req, false)
	if len(command) == 0 {
		return errors.New("ssh command is empty")
	}
	if !req.Local {
		if err := a.deploySSHBinary(ctx, req); err != nil {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	if req.Local && strings.TrimSpace(req.Directory) != "" {
		cmd.Dir = req.Directory
	}
	cmd.Stdin = a.In
	cmd.Stdout = a.Out
	cmd.Stderr = a.Err
	return cmd.Run()
}

func sshCommandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func (a *App) sshCommand(req sshRequest, redact bool) ([]string, string) {
	remoteArgs := a.sshRemoteCodogArgs(req)
	if req.Local {
		return remoteArgs, ""
	}
	remoteShell := sshRemoteShell(req.Directory, a.sshRemoteEnv(req, redact), remoteArgs)
	return []string{"ssh", req.Host, remoteShell}, remoteShell
}

func (a *App) sshRemoteCodogArgs(req sshRequest) []string {
	executable := sshRemoteExecutable(req.Host)
	if req.Local {
		executable = strings.TrimSpace(a.Executable)
	}
	if req.Local && executable == "" {
		if path, err := resolveExecutablePath(); err == nil {
			executable = strings.TrimSpace(path)
		}
	}
	if executable == "" {
		executable = "codog"
	}
	out := []string{executable}
	out = append(out, req.ExtraArgs...)
	if req.PermissionMode != "" {
		out = append(out, "--permission-mode", req.PermissionMode)
	}
	if req.PlanModeRequired {
		out = append(out, "--plan-mode-required")
	}
	if req.DangerouslySkipPermissions {
		out = append(out, "--dangerously-skip-permissions")
	}
	if req.Print {
		out = append(out, "prompt")
		if strings.TrimSpace(req.Prompt) != "" {
			out = append(out, req.Prompt)
		}
		return out
	}
	out = append(out, "repl")
	return out
}

func (a *App) sshDeployCommand(req sshRequest) ([]string, string) {
	if req.Local {
		return nil, ""
	}
	remoteExecutable := sshRemoteExecutable(req.Host)
	return []string{"ssh", req.Host, sshDeployShell(remoteExecutable)}, remoteExecutable
}

func (a *App) deploySSHBinary(ctx context.Context, req sshRequest) error {
	deployCommand, _ := a.sshDeployCommand(req)
	if len(deployCommand) == 0 {
		return nil
	}
	localExecutable := strings.TrimSpace(a.Executable)
	if localExecutable == "" {
		if path, err := resolveExecutablePath(); err == nil {
			localExecutable = strings.TrimSpace(path)
		}
	}
	if localExecutable == "" {
		return errors.New("cannot deploy ssh binary: current executable path is unavailable")
	}
	binary, err := os.Open(localExecutable)
	if err != nil {
		return fmt.Errorf("cannot open ssh deploy binary: %w", err)
	}
	defer func() { _ = binary.Close() }()
	cmd := exec.CommandContext(ctx, deployCommand[0], deployCommand[1:]...)
	cmd.Stdin = binary
	cmd.Stdout = a.Out
	cmd.Stderr = a.Err
	return cmd.Run()
}

func sshRemoteExecutable(host string) string {
	slug := regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(strings.TrimSpace(host), "-")
	slug = strings.Trim(slug, "-.")
	if slug == "" {
		slug = "remote"
	}
	return ".cache/codog/remote/" + slug + "/codog"
}

func sshDeployShell(remoteExecutable string) string {
	dir := remotePathDir(remoteExecutable)
	return "mkdir -p " + shellQuote(dir) + " && cat > " + shellQuote(remoteExecutable) + " && chmod 700 " + shellQuote(remoteExecutable)
}

func remotePathDir(path string) string {
	path = strings.TrimSpace(path)
	index := strings.LastIndex(path, "/")
	if index <= 0 {
		return "."
	}
	return path[:index]
}

func (a *App) sshRemoteEnv(req sshRequest, redact bool) map[string]string {
	if req.Local {
		return map[string]string{}
	}
	env := map[string]string{}
	add := func(key, value string, secret bool) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if redact && secret {
			value = "[redacted]"
		}
		env[key] = value
	}
	add("CODOG_MODEL", a.Config.Model, false)
	add("ANTHROPIC_MODEL", a.Config.Model, false)
	add("CLAUDE_MODEL", a.Config.Model, false)
	add("CODOG_BASE_URL", a.Config.BaseURL, false)
	add("ANTHROPIC_BASE_URL", a.Config.BaseURL, false)
	add("CODOG_API_KEY", a.Config.APIKey, true)
	add("ANTHROPIC_API_KEY", a.Config.APIKey, true)
	add("CODOG_AUTH_TOKEN", a.Config.AuthToken, true)
	add("ANTHROPIC_AUTH_TOKEN", a.Config.AuthToken, true)
	add("CLAUDE_CODE_OAUTH_TOKEN", a.Config.AuthToken, true)
	for _, key := range []string{
		"CLAUDE_CODE_REMOTE",
		"CLAUDE_CODE_REMOTE_SESSION_ID",
		"CCR_UPSTREAM_PROXY_ENABLED",
		"CCR_SESSION_TOKEN_PATH",
		"CCR_CA_BUNDLE_PATH",
		"CCR_SYSTEM_CA_BUNDLE",
	} {
		add(key, os.Getenv(key), strings.Contains(key, "TOKEN"))
	}
	return env
}

func sshRemoteEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sshRemoteAuthForwarded(env map[string]string) bool {
	for _, key := range []string{"CODOG_API_KEY", "ANTHROPIC_API_KEY", "CODOG_AUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if strings.TrimSpace(env[key]) != "" {
			return true
		}
	}
	return false
}

func shellQuoteArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for index, arg := range args {
		if index == 0 && arg == "codog" {
			out = append(out, arg)
			continue
		}
		if strings.ContainsAny(arg, " \t\n'\"") {
			out = append(out, shellQuote(arg))
			continue
		}
		out = append(out, arg)
	}
	return out
}

func sshRemoteShell(directory string, env map[string]string, args []string) string {
	commandParts := []string{}
	if len(env) > 0 {
		envArgs := []string{"env"}
		for _, key := range sshRemoteEnvKeys(env) {
			envArgs = append(envArgs, key+"="+shellQuote(env[key]))
		}
		commandParts = append(commandParts, envArgs...)
	}
	commandParts = append(commandParts, shellQuoteArgs(args)...)
	command := strings.Join(commandParts, " ")
	if strings.TrimSpace(directory) == "" {
		return command
	}
	return "cd " + shellQuote(directory) + " && " + command
}

func parseOpenArgs(args []string) (openRequest, error) {
	req := openRequest{Format: "text"}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "open", Flag: arg, Usage: openUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "-p" || arg == "--print":
			req.Print = true
			if index+1 < len(args) && !strings.HasPrefix(args[index+1], "-") {
				index++
				req.Prompt = args[index]
			}
		case strings.HasPrefix(arg, "-p="):
			req.Print = true
			req.Prompt = strings.TrimPrefix(arg, "-p=")
		case strings.HasPrefix(arg, "--print="):
			req.Print = true
			req.Prompt = strings.TrimPrefix(arg, "--print=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "open", Option: arg, Usage: openUsage}
			}
			if req.RawURL != "" {
				return req, unexpectedExtraArgsError{Command: "open", Args: []string{arg}, Usage: openUsage}
			}
			req.RawURL = arg
		}
	}
	if strings.TrimSpace(req.RawURL) == "" {
		return req, requiredArgumentError{Command: "open", Argument: "cc-url", Usage: openUsage}
	}
	normalizedFormat, err := normalizeOutputFormat("open", req.Format, []string{"text", "json", "stream-json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func parseOpenTarget(raw string) (openTarget, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return openTarget{}, fmt.Errorf("invalid open URL %q: %w", raw, err)
	}
	switch parsed.Scheme {
	case "http", "https":
		token := firstOpenQueryValue(parsed.Query(), "authToken", "auth_token", "token")
		parsed.RawQuery = removeOpenTokenQuery(parsed.Query()).Encode()
		return openTarget{ServerURL: strings.TrimRight(parsed.String(), "/"), AuthToken: token}, nil
	case "cc":
		query := parsed.Query()
		token := firstOpenQueryValue(query, "authToken", "auth_token", "token")
		if serverURL := firstOpenQueryValue(query, "url", "serverUrl", "server_url", "server"); serverURL != "" {
			target, err := parseOpenTarget(serverURL)
			if err != nil {
				return openTarget{}, err
			}
			if token != "" {
				target.AuthToken = token
			}
			return target, nil
		}
		if parsed.Host == "" {
			return openTarget{}, fmt.Errorf("invalid cc URL %q: host is required", raw)
		}
		path := strings.TrimRight(parsed.EscapedPath(), "/")
		serverURL := (&url.URL{Scheme: "http", Host: parsed.Host, Path: path}).String()
		return openTarget{ServerURL: strings.TrimRight(serverURL, "/"), AuthToken: token}, nil
	case "cc+unix":
		token := firstOpenQueryValue(parsed.Query(), "authToken", "auth_token", "token")
		socketPath := strings.TrimSpace(parsed.Host + parsed.Path)
		if unescaped, err := url.PathUnescape(socketPath); err == nil {
			socketPath = unescaped
		}
		if socketPath == "" {
			return openTarget{}, fmt.Errorf("invalid cc+unix URL %q: socket path is required", raw)
		}
		return openTarget{ServerURL: "unix:" + socketPath, AuthToken: token, UnixPath: socketPath}, nil
	default:
		return openTarget{}, fmt.Errorf("unsupported open URL scheme %q", parsed.Scheme)
	}
}

func firstOpenQueryValue(query url.Values, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(query.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func removeOpenTokenQuery(query url.Values) url.Values {
	out := url.Values{}
	for key, values := range query {
		switch strings.ToLower(key) {
		case "authtoken", "auth_token", "token":
			continue
		default:
			out[key] = append([]string(nil), values...)
		}
	}
	return out
}

func openHTTPClient(target openTarget) *http.Client {
	if strings.TrimSpace(target.UnixPath) == "" {
		return &http.Client{Timeout: 10 * time.Second}
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", target.UnixPath)
		},
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

func createOpenSession(ctx context.Context, client *http.Client, target openTarget, cwd string) (string, string, error) {
	var response map[string]any
	if err := openJSONRequest(ctx, client, target, http.MethodPost, "/sessions", map[string]any{"cwd": cwd}, &response); err != nil {
		return "", "", err
	}
	sessionID := firstOpenString(response, "session_id", "id", "ID")
	if sessionID == "" {
		if nested, ok := response["session"].(map[string]any); ok {
			sessionID = firstOpenString(nested, "session_id", "id", "ID")
		}
	}
	if sessionID == "" {
		return "", "", errors.New("open: server response did not include a session id")
	}
	workDir := firstOpenString(response, "work_dir", "workDir", "workspace")
	return sessionID, workDir, nil
}

func submitOpenPrompt(ctx context.Context, client *http.Client, target openTarget, sessionID string, prompt string) (map[string]any, error) {
	var response map[string]any
	path := "/sessions/" + url.PathEscape(sessionID) + "/prompt"
	if err := openJSONRequest(ctx, client, target, http.MethodPost, path, map[string]any{"prompt": prompt}, &response); err != nil {
		return nil, err
	}
	return response, nil
}

func openJSONRequest(ctx context.Context, client *http.Client, target openTarget, method string, path string, payload any, out any) error {
	baseURL := strings.TrimRight(target.ServerURL, "/")
	if strings.HasPrefix(baseURL, "unix:") {
		baseURL = "http://unix"
	}
	body := bytes.Buffer{}
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	if strings.TrimSpace(target.AuthToken) != "" {
		req.Header.Set("authorization", "Bearer "+strings.TrimSpace(target.AuthToken))
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(data))
		if message == "" {
			message = resp.Status
		}
		return fmt.Errorf("open: %s %s failed: %s", method, path, message)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("open: decode response: %w", err)
	}
	return nil
}

func firstOpenString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			if text := strings.TrimSpace(fmt.Sprint(value)); text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func renderOpenReport(out io.Writer, report openReport, format string) error {
	if format == "json" || format == "stream-json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Open")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Server URL       %s\n", report.ServerURL)
	fmt.Fprintf(out, "  Session ID       %s\n", report.SessionID)
	if report.WorkDir != "" {
		fmt.Fprintf(out, "  Work dir         %s\n", report.WorkDir)
	}
	fmt.Fprintf(out, "  Auth token       %t\n", report.AuthTokenConfigured)
	fmt.Fprintf(out, "  Print            %t\n", report.Print)
	fmt.Fprintf(out, "  Prompt submitted %t\n", report.PromptSubmitted)
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
	return nil
}

func (a *App) serveControlListener(ctx context.Context, listener net.Listener, addr string) error {
	return a.serveControlListenerWithOptions(ctx, listener, addr, controlListenerOptions{})
}

type controlListenerOptions struct {
	MaxSessions int
	IdleTimeout time.Duration
}

func (a *App) serveControlListenerWithOptions(ctx context.Context, listener net.Listener, addr string, opts controlListenerOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	handler := a.remoteControlServerWithMaxSessions(addr, opts.MaxSessions).Handler()
	var idleActivity chan struct{}
	if opts.IdleTimeout > 0 {
		idleActivity = make(chan struct{}, 1)
		handler = idleTrackingHandler(handler, idleActivity)
	}
	server := &http.Server{Handler: handler}
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()
	if opts.IdleTimeout > 0 {
		stopIdle := startControlIdleShutdown(ctx, server, opts.IdleTimeout, idleActivity)
		defer stopIdle()
	}
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}
		err := <-errCh
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func idleTrackingHandler(next http.Handler, activity chan<- struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case activity <- struct{}{}:
		default:
		}
		next.ServeHTTP(w, r)
	})
}

func startControlIdleShutdown(ctx context.Context, server *http.Server, timeout time.Duration, activity <-chan struct{}) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case <-activity:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(timeout)
			case <-timer.C:
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = server.Shutdown(shutdownCtx)
				cancel()
				return
			case <-ctx.Done():
				return
			case <-stop:
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

const apiUsage = "codog api [routes|list|show|status|serve|listen|start] [ADDR|--addr ADDR] [--output-format text|json]"

func parseAPIArgs(args []string) (apiRequest, error) {
	req := apiRequest{Action: "routes", Format: "text", Addr: "127.0.0.1:8791"}
	actionSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "api", Flag: arg, Usage: apiUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--addr":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "api", Flag: arg, Usage: apiUsage}
			}
			req.Addr = args[index]
			req.AddrSet = true
		case strings.HasPrefix(arg, "--addr="):
			req.Addr = strings.TrimPrefix(arg, "--addr=")
			req.AddrSet = true
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "api", Option: arg, Usage: apiUsage}
		default:
			if actionSet && req.Action == "serve" && !req.AddrSet {
				req.Addr = arg
				req.AddrSet = true
				continue
			}
			if actionSet {
				return req, unexpectedExtraArgsError{Command: "api " + req.Action, Args: []string{arg}, Usage: apiUsage}
			}
			switch strings.ToLower(arg) {
			case "routes", "list", "show":
				req.Action = "routes"
			case "status":
				req.Action = "status"
			case "serve", "listen", "start":
				req.Action = "serve"
			default:
				return req, unexpectedExtraArgsError{Command: "api", Args: []string{arg}, Usage: apiUsage}
			}
			actionSet = true
		}
	}
	normalizedFormat, err := normalizeOutputFormat("api", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func (a *App) buildAPIReport(req apiRequest, addr, remoteURL string) apiReport {
	routes := control.RouteSpecs()
	publicCount := 0
	streamingCount := 0
	for _, route := range routes {
		if route.Public {
			publicCount++
		}
		if route.Streaming {
			streamingCount++
		}
	}
	authConfigured := strings.TrimSpace(a.Config.Future.RemoteAuthToken) != ""
	status := "disabled"
	switch {
	case a.Config.Future.RemoteEnabled && authConfigured:
		status = "ready"
	case a.Config.Future.RemoteEnabled:
		status = "enabled_without_auth"
	}
	remoteCommand := "codog remote serve " + addr
	if req.Action == "serve" {
		remoteCommand = "codog api serve " + addr
	}
	baseURL := strings.TrimRight(remoteURL, "/")
	report := apiReport{
		Kind:                "api",
		Action:              req.Action,
		Status:              status,
		Workspace:           a.Workspace,
		Enabled:             a.Config.Future.RemoteEnabled,
		Ready:               a.Config.Future.RemoteEnabled,
		AuthTokenConfigured: authConfigured,
		AuthRequired:        authConfigured,
		LeaseSeconds:        a.Config.Future.RemoteLeaseSeconds,
		RemoteCommand:       remoteCommand,
		RemoteAddr:          addr,
		RemoteURL:           remoteURL,
		HealthURL:           baseURL + "/health",
		StateURL:            baseURL + "/state",
		RoutesURL:           baseURL + "/routes",
		CapabilitiesURL:     baseURL + "/capabilities",
		RouteCount:          len(routes),
		PublicRouteCount:    publicCount,
		StreamingRouteCount: streamingCount,
		Routes:              routes,
	}
	if !report.Enabled {
		report.Messages = append(report.Messages, "Enable remote control with `codog remote-setup enable` before exposing the API to a client.")
	}
	if report.Enabled && !authConfigured {
		report.Messages = append(report.Messages, "No auth token is configured; keep the listener on localhost or set one with `codog remote-setup enable --auth-token TOKEN`.")
	}
	return report
}

func renderAPIReport(out io.Writer, report apiReport) {
	fmt.Fprintln(out, "Remote API")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	fmt.Fprintf(out, "  Listening        %t\n", report.Listening)
	fmt.Fprintf(out, "  Auth required    %t\n", report.AuthRequired)
	fmt.Fprintf(out, "  Lease seconds    %d\n", report.LeaseSeconds)
	fmt.Fprintf(out, "  Remote command   %s\n", report.RemoteCommand)
	fmt.Fprintf(out, "  Remote URL       %s\n", report.RemoteURL)
	fmt.Fprintf(out, "  Health URL       %s\n", report.HealthURL)
	fmt.Fprintf(out, "  State URL        %s\n", report.StateURL)
	fmt.Fprintf(out, "  Routes URL       %s\n", report.RoutesURL)
	fmt.Fprintf(out, "  Capabilities URL %s\n", report.CapabilitiesURL)
	fmt.Fprintf(out, "  Routes           %d\n", report.RouteCount)
	if len(report.Routes) != 0 {
		fmt.Fprintln(out, "  Endpoints")
		for _, route := range report.Routes {
			auth := "auth"
			if route.Public {
				auth = "public"
			}
			streaming := ""
			if route.Streaming {
				streaming = " stream"
			}
			fmt.Fprintf(out, "    %-28s %-9s %s%s\n", route.Path, strings.Join(route.Methods, ","), auth, streaming)
		}
	}
	for _, message := range report.Messages {
		fmt.Fprintf(out, "  Note             %s\n", message)
	}
}

type remoteEnvRequest struct {
	Action       string
	Format       string
	Target       string
	Path         string
	SetEnabled   bool
	Enabled      bool
	AuthToken    string
	ClearToken   bool
	SetLease     bool
	LeaseSeconds int
}

type remoteEnvReport struct {
	Kind                string `json:"kind"`
	Action              string `json:"action"`
	Status              string `json:"status"`
	Enabled             bool   `json:"enabled"`
	AuthTokenConfigured bool   `json:"auth_token_configured"`
	LeaseSeconds        int    `json:"lease_seconds"`
	Path                string `json:"path,omitempty"`
}

type remoteSetupRequest struct {
	Action       string
	Format       string
	Addr         string
	Target       string
	Path         string
	AuthToken    string
	ClearToken   bool
	SetLease     bool
	LeaseSeconds int
	SessionID    string
}

type remoteSetupReport struct {
	Kind                string                      `json:"kind"`
	Action              string                      `json:"action"`
	Status              string                      `json:"status"`
	Workspace           string                      `json:"workspace,omitempty"`
	SessionID           string                      `json:"session_id,omitempty"`
	Enabled             bool                        `json:"enabled"`
	Ready               bool                        `json:"ready"`
	AuthTokenConfigured bool                        `json:"auth_token_configured"`
	LeaseSeconds        int                         `json:"lease_seconds"`
	RemoteCommand       string                      `json:"remote_command"`
	RemoteAddr          string                      `json:"remote_addr"`
	RemoteURL           string                      `json:"remote_url"`
	HealthURL           string                      `json:"health_url"`
	StateURL            string                      `json:"state_url"`
	Path                string                      `json:"path,omitempty"`
	Messages            []string                    `json:"messages,omitempty"`
	Runtime             remoteruntime.RuntimeReport `json:"runtime"`
}

func (a *App) RemoteEnv(args []string) error {
	req, err := parseRemoteEnvArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "show":
	case "set":
		if err := a.setRemoteEnv(&req); err != nil {
			return err
		}
	case "clear":
		if err := a.clearRemoteEnv(&req); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown remote-env command %q", req.Action)
	}
	return a.renderRemoteEnv(req)
}

func (a *App) setRemoteEnv(req *remoteEnvRequest) error {
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	if !req.SetEnabled && req.AuthToken == "" && !req.ClearToken && !req.SetLease {
		return errors.New("remote-env set requires --enabled, --auth-token, --clear-auth-token, or --lease-seconds")
	}
	if req.SetEnabled {
		if err := a.writeRemoteEnabled(path, req.Enabled); err != nil {
			return err
		}
	}
	if req.AuthToken != "" {
		if err := a.writeRemoteAuthToken(path, req.AuthToken); err != nil {
			return err
		}
	}
	if req.ClearToken {
		if err := a.clearRemoteAuthToken(path); err != nil {
			return err
		}
	}
	if req.SetLease {
		if err := a.writeRemoteLease(path, req.LeaseSeconds); err != nil {
			return err
		}
	}
	req.Path = path
	return nil
}

func (a *App) clearRemoteEnv(req *remoteEnvRequest) error {
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	if err := unsetConfigKeys(path, remoteResetKeys); err != nil {
		return err
	}
	a.Config.Future.RemoteEnabled = false
	a.Config.Future.RemoteAuthToken = ""
	a.Config.Future.RemoteLeaseSeconds = 0
	req.Path = path
	return nil
}

func (a *App) renderRemoteEnv(req remoteEnvRequest) error {
	report := remoteEnvReport{
		Kind:                "remote_env",
		Action:              req.Action,
		Status:              "ok",
		Enabled:             a.Config.Future.RemoteEnabled,
		AuthTokenConfigured: strings.TrimSpace(a.Config.Future.RemoteAuthToken) != "",
		LeaseSeconds:        a.Config.Future.RemoteLeaseSeconds,
		Path:                req.Path,
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderRemoteEnvReport(a.Out, report)
	return nil
}

func (a *App) RemoteSetup(args []string, overrides config.FlagOverrides) error {
	req, err := parseRemoteSetupArgs(args, overrides)
	if err != nil {
		return err
	}
	sessionID, err := resolveHandoffSessionID(a.Sessions, req.SessionID)
	if err != nil {
		return err
	}
	addr, remoteURL, err := normalizeRemoteHandoffAddr(req.Addr)
	if err != nil {
		return err
	}
	switch req.Action {
	case "status":
	case "enable":
		if err := a.enableRemoteSetup(&req); err != nil {
			return err
		}
	case "disable":
		if err := a.disableRemoteSetup(&req); err != nil {
			return err
		}
	case "clear":
		if err := a.clearRemoteSetup(&req); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown remote-setup command %q", req.Action)
	}
	report := a.buildRemoteSetupReport(req, sessionID, addr, remoteURL)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderRemoteSetupReport(a.Out, report)
	return nil
}

func (a *App) enableRemoteSetup(req *remoteSetupRequest) error {
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	if err := a.writeRemoteEnabled(path, true); err != nil {
		return err
	}
	if req.AuthToken != "" {
		if err := a.writeRemoteAuthToken(path, req.AuthToken); err != nil {
			return err
		}
	}
	if req.ClearToken {
		if err := a.clearRemoteAuthToken(path); err != nil {
			return err
		}
	}
	if req.SetLease {
		if err := a.writeRemoteLease(path, req.LeaseSeconds); err != nil {
			return err
		}
	}
	req.Path = path
	return nil
}

func (a *App) disableRemoteSetup(req *remoteSetupRequest) error {
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	if err := a.writeRemoteEnabled(path, false); err != nil {
		return err
	}
	if req.ClearToken {
		if err := a.clearRemoteAuthToken(path); err != nil {
			return err
		}
	}
	req.Path = path
	return nil
}

func (a *App) clearRemoteSetup(req *remoteSetupRequest) error {
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	if err := unsetConfigKeys(path, remoteResetKeys); err != nil {
		return err
	}
	a.Config.Future.RemoteEnabled = false
	a.Config.Future.RemoteAuthToken = ""
	a.Config.Future.RemoteLeaseSeconds = 0
	req.Path = path
	return nil
}

func (a *App) writeRemoteEnabled(path string, enabled bool) error {
	if _, err := config.SetFileValue(path, "remote.enabled", enabled); err != nil {
		return err
	}
	a.Config.Future.RemoteEnabled = enabled
	return nil
}

func (a *App) writeRemoteAuthToken(path string, token string) error {
	if _, err := config.SetFileValue(path, "remote.auth_token", token); err != nil {
		return err
	}
	a.Config.Future.RemoteAuthToken = token
	return nil
}

func (a *App) clearRemoteAuthToken(path string) error {
	if _, err := config.UnsetFileValue(path, "remote.auth_token"); err != nil {
		return err
	}
	if _, err := config.UnsetFileValue(path, legacyRemoteAuthTokenKey); err != nil {
		return err
	}
	a.Config.Future.RemoteAuthToken = ""
	return nil
}

func (a *App) writeRemoteLease(path string, seconds int) error {
	if _, err := config.SetFileValue(path, "remote.lease_seconds", seconds); err != nil {
		return err
	}
	a.Config.Future.RemoteLeaseSeconds = seconds
	return nil
}

func parseRemoteEnvArgs(args []string) (remoteEnvRequest, error) {
	parser := remoteEnvArgParser{req: remoteEnvRequest{Action: "show", Format: "text", Target: "user"}}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if parser.consumeBoolean(arg) {
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
			return parser.req, unknownOptionError{Command: "remote-env", Option: arg, Usage: remoteEnvUsage}
		}
		parser.rest = append(parser.rest, arg)
	}
	if err := parser.finish(); err != nil {
		return parser.req, err
	}
	return parser.req, nil
}

const remoteEnvUsage = "codog remote-env [show|set|clear] [--target user|project|local] [--path PATH] [--enabled on|off] [--auth-token TOKEN] [--clear-auth-token] [--lease-seconds N] [--json|--output-format text|json]"

type remoteEnvArgParser struct {
	req  remoteEnvRequest
	rest []string
}

func (p *remoteEnvArgParser) consumeBoolean(arg string) bool {
	switch arg {
	case "--json":
		p.req.Format = "json"
		return true
	case "--clear-auth-token":
		p.req.ClearToken = true
		return true
	default:
		return false
	}
}

func (p *remoteEnvArgParser) valueOptions() map[string]valueOption {
	return map[string]valueOption{
		"--output-format": p.stringOption(&p.req.Format),
		"-o":              p.stringOption(&p.req.Format),
		"--target":        p.stringOption(&p.req.Target),
		"--path":          p.stringOption(&p.req.Path),
		"--auth-token":    p.stringOption(&p.req.AuthToken),
		"--enabled":       {missing: remoteEnvMissingValue, rejectEmptySeparate: true, rejectOutputFormat: true, set: p.setEnabled},
		"--lease-seconds": {missing: remoteEnvMissingValue, rejectEmptySeparate: true, rejectOutputFormat: true, set: p.setLease},
	}
}

func (p *remoteEnvArgParser) stringOption(target *string) valueOption {
	return valueOption{missing: remoteEnvMissingValue, rejectEmptySeparate: true, rejectOutputFormat: true, set: func(value string) error {
		*target = value
		return nil
	}}
}

func remoteEnvMissingValue(flag string) error {
	return missingFlagValueError{Command: "remote-env", Flag: flag, Usage: remoteEnvUsage}
}

func (p *remoteEnvArgParser) setEnabled(value string) error {
	enabled, err := parseOnOffBool(value)
	if err != nil {
		return err
	}
	p.req.SetEnabled = true
	p.req.Enabled = enabled
	return nil
}

func (p *remoteEnvArgParser) setLease(value string) error {
	seconds, err := parseNonNegativeIntOption(value, "--lease-seconds", remoteEnvUsage)
	if err != nil {
		return err
	}
	p.req.SetLease = true
	p.req.LeaseSeconds = seconds
	return nil
}

func (p *remoteEnvArgParser) finish() error {
	format, err := normalizeOutputFormat("remote-env", p.req.Format, []string{"text", "json"})
	if err != nil {
		return err
	}
	p.req.Format = format
	if len(p.rest) > 1 {
		return unexpectedExtraArgsError{Command: "remote-env", Args: p.rest[1:], Usage: remoteEnvUsage}
	}
	if len(p.rest) == 1 {
		switch strings.ToLower(p.rest[0]) {
		case "show", "status":
			p.req.Action = "show"
		case "set":
			p.req.Action = "set"
		case "clear", "reset", "unset":
			p.req.Action = "clear"
		default:
			return unknownActionError{
				Command:     "remote-env",
				Action:      p.rest[0],
				Expected:    append([]string(nil), remoteEnvActionCandidates...),
				Suggestions: toolnames.Suggestions(p.rest[0], remoteEnvActionCandidates, 4),
				Usage:       remoteEnvUsage,
			}
		}
	}
	return nil
}

var remoteEnvActionCandidates = []string{"show", "status", "set", "clear", "reset", "unset"}

func parseRemoteSetupArgs(args []string, overrides config.FlagOverrides) (remoteSetupRequest, error) {
	parser := remoteSetupArgParser{req: remoteSetupRequest{
		Action:    "status",
		Format:    "text",
		Addr:      "127.0.0.1:8791",
		Target:    "user",
		SessionID: firstNonEmpty(overrides.Resume, overrides.SessionID),
	}}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if parser.consumeBoolean(arg) {
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
			return parser.req, unknownOptionError{Command: "remote-setup", Option: arg, Usage: remoteSetupUsage}
		}
		if err := parser.consumeAction(arg); err != nil {
			return parser.req, err
		}
	}
	if err := parser.finish(); err != nil {
		return parser.req, err
	}
	return parser.req, nil
}

const remoteSetupUsage = "codog remote-setup [status|enable|disable|clear] [--addr ADDR] [--target user|project|local] [--path PATH] [--auth-token TOKEN] [--clear-auth-token] [--lease-seconds N] [--session ID|--resume ID] [--json|--output-format text|json]"

type remoteSetupArgParser struct {
	req       remoteSetupRequest
	actionSet bool
}

func (p *remoteSetupArgParser) consumeBoolean(arg string) bool {
	switch arg {
	case "--json":
		p.req.Format = "json"
	case "--clear-auth-token":
		p.req.ClearToken = true
	case "--enable":
		p.req.Action, p.actionSet = "enable", true
	case "--disable":
		p.req.Action, p.actionSet = "disable", true
	default:
		return false
	}
	return true
}

func (p *remoteSetupArgParser) valueOptions() map[string]valueOption {
	return map[string]valueOption{
		"--output-format": p.stringOption(&p.req.Format),
		"-o":              p.stringOption(&p.req.Format),
		"--addr":          p.stringOption(&p.req.Addr),
		"--target":        p.stringOption(&p.req.Target),
		"--path":          p.stringOption(&p.req.Path),
		"--auth-token":    p.stringOption(&p.req.AuthToken),
		"--session":       p.stringOption(&p.req.SessionID),
		"--resume":        p.stringOption(&p.req.SessionID),
		"--lease-seconds": {missing: remoteSetupMissingValue, rejectEmptySeparate: true, rejectOutputFormat: true, set: p.setLease},
	}
}

func (p *remoteSetupArgParser) stringOption(target *string) valueOption {
	return valueOption{missing: remoteSetupMissingValue, rejectEmptySeparate: true, rejectOutputFormat: true, set: func(value string) error {
		*target = value
		return nil
	}}
}

func remoteSetupMissingValue(flag string) error {
	return missingFlagValueError{Command: "remote-setup", Flag: flag, Usage: remoteSetupUsage}
}

func (p *remoteSetupArgParser) setLease(value string) error {
	seconds, err := parseNonNegativeIntOption(value, "--lease-seconds", remoteSetupUsage)
	if err != nil {
		return err
	}
	p.req.SetLease, p.req.LeaseSeconds = true, seconds
	return nil
}

func (p *remoteSetupArgParser) consumeAction(arg string) error {
	if p.actionSet {
		return unexpectedExtraArgsError{Command: "remote-setup", Args: []string{arg}, Usage: remoteSetupUsage}
	}
	switch strings.ToLower(arg) {
	case "status", "show", "check":
		p.req.Action = "status"
	case "enable", "on", "setup":
		p.req.Action = "enable"
	case "disable", "off":
		p.req.Action = "disable"
	case "clear", "reset", "unset":
		p.req.Action = "clear"
	default:
		return unknownActionError{Command: "remote-setup", Action: arg, Expected: append([]string(nil), remoteSetupActionCandidates...), Suggestions: toolnames.Suggestions(arg, remoteSetupActionCandidates, 4), Usage: remoteSetupUsage}
	}
	p.actionSet = true
	return nil
}

func (p *remoteSetupArgParser) finish() error {
	format, err := normalizeOutputFormat("remote-setup", p.req.Format, []string{"text", "json"})
	if err != nil {
		return err
	}
	p.req.Format = format
	if p.req.AuthToken != "" && p.req.ClearToken {
		return errors.New("remote-setup cannot set and clear auth token in the same command")
	}
	if p.req.Action == "status" && (p.req.AuthToken != "" || p.req.ClearToken || p.req.SetLease) {
		p.req.Action = "enable"
	}
	if p.req.Action == "disable" && (p.req.AuthToken != "" || p.req.SetLease) {
		return errors.New("remote-setup disable only accepts --clear-auth-token as a write option")
	}
	return nil
}
