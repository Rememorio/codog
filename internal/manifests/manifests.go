package manifests

import (
	"sort"

	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/skills"
	"github.com/Rememorio/codog/internal/slash"
	"github.com/Rememorio/codog/internal/tools"
)

type Report struct {
	Kind             string                 `json:"kind"`
	Action           string                 `json:"action"`
	Status           string                 `json:"status"`
	Source           string                 `json:"source"`
	Workspace        string                 `json:"workspace"`
	Commands         int                    `json:"commands"`
	Tools            int                    `json:"tools"`
	Agents           int                    `json:"agents"`
	Skills           int                    `json:"skills"`
	CommandManifests []CommandManifest      `json:"command_manifests"`
	ToolManifests    []tools.ToolInfo       `json:"tool_manifests"`
	AgentManifests   []agentdefs.Definition `json:"agent_manifests"`
	SkillManifests   []SkillManifest        `json:"skill_manifests"`
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
	return Report{
		Kind:             "dump-manifests",
		Action:           "dump",
		Status:           "ok",
		Source:           "go-resolver",
		Workspace:        workspace,
		Commands:         len(commandManifests),
		Tools:            len(toolManifests),
		Agents:           len(agentManifests),
		Skills:           len(skillManifests),
		CommandManifests: commandManifests,
		ToolManifests:    toolManifests,
		AgentManifests:   agentManifests,
		SkillManifests:   skillManifests,
	}, nil
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
