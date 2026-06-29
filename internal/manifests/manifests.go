package manifests

import (
	"sort"

	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/skills"
	"github.com/Rememorio/codog/internal/slash"
	"github.com/Rememorio/codog/internal/tools"
)

type Report struct {
	Kind              string                 `json:"kind"`
	Action            string                 `json:"action"`
	Status            string                 `json:"status"`
	Source            string                 `json:"source"`
	Workspace         string                 `json:"workspace"`
	Commands          int                    `json:"commands"`
	Tools             int                    `json:"tools"`
	Agents            int                    `json:"agents"`
	Skills            int                    `json:"skills"`
	BootstrapPhases   int                    `json:"bootstrap_phases"`
	CommandManifests  []CommandManifest      `json:"command_manifests"`
	ToolManifests     []tools.ToolInfo       `json:"tool_manifests"`
	AgentManifests    []agentdefs.Definition `json:"agent_manifests"`
	SkillManifests    []SkillManifest        `json:"skill_manifests"`
	BootstrapManifest []BootstrapPhase       `json:"bootstrap_manifest"`
}

type CommandManifest struct {
	Name        string `json:"name"`
	Usage       string `json:"usage"`
	Description string `json:"description"`
	Implemented bool   `json:"implemented"`
}

type SkillManifest struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Source string `json:"source"`
}

type BootstrapPhase struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func Build(workspace string, configHome string, registry *tools.Registry) (Report, error) {
	if registry == nil {
		registry = tools.NewRegistry(workspace)
	}
	commandManifests := commandManifests()
	toolManifests := registry.Infos()
	agentManifests, err := agentdefs.Load(workspace)
	if err != nil {
		return Report{}, err
	}
	loadedSkills, err := skills.Load(configHome, workspace)
	if err != nil {
		return Report{}, err
	}
	skillManifests := make([]SkillManifest, 0, len(loadedSkills))
	for _, skill := range loadedSkills {
		skillManifests = append(skillManifests, SkillManifest{
			Name:   skill.Name,
			Path:   skill.Path,
			Source: skill.Source,
		})
	}
	bootstrap := BootstrapManifest()
	return Report{
		Kind:              "dump-manifests",
		Action:            "dump",
		Status:            "ok",
		Source:            "go-resolver",
		Workspace:         workspace,
		Commands:          len(commandManifests),
		Tools:             len(toolManifests),
		Agents:            len(agentManifests),
		Skills:            len(skillManifests),
		BootstrapPhases:   len(bootstrap),
		CommandManifests:  commandManifests,
		ToolManifests:     toolManifests,
		AgentManifests:    agentManifests,
		SkillManifests:    skillManifests,
		BootstrapManifest: bootstrap,
	}, nil
}

func BootstrapManifest() []BootstrapPhase {
	return []BootstrapPhase{
		{Name: "load_config", Description: "Merge user config, project config, environment, and CLI flags."},
		{Name: "detect_workspace", Description: "Resolve the current workspace and path-scope settings."},
		{Name: "load_project_memory", Description: "Discover project memory and workspace instruction files."},
		{Name: "initialize_tools", Description: "Register built-in tools and configured local extensions."},
		{Name: "open_session_store", Description: "Create the workspace-scoped JSONL session store."},
		{Name: "assemble_system_prompt", Description: "Build the runtime prompt from memory, skills, style, and mode state."},
		{Name: "run_surface", Description: "Start the requested prompt, REPL, TUI, bridge, or control surface."},
	}
}

func commandManifests() []CommandManifest {
	specs := slash.Specs()
	entries := make([]CommandManifest, 0, len(specs))
	for _, spec := range specs {
		entries = append(entries, CommandManifest{
			Name:        spec.Name,
			Usage:       spec.Usage,
			Description: spec.Description,
			Implemented: true,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}
