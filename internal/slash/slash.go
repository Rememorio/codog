package slash

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

type Spec struct {
	Name        string
	Usage       string
	Description string
}

func Specs() []Spec {
	specs := []Spec{
		{Name: "/help", Usage: "/help", Description: "Show slash command help."},
		{Name: "/status", Usage: "/status", Description: "Show session, model, and permission status."},
		{Name: "/cost", Usage: "/cost", Description: "Estimate token usage and cost for the current session."},
		{Name: "/compact", Usage: "/compact", Description: "Compact in-memory request context for long sessions."},
		{Name: "/skills", Usage: "/skills", Description: "List discovered Markdown skills."},
		{Name: "/mcp", Usage: "/mcp", Description: "Inspect configured stdio MCP servers."},
		{Name: "/exit", Usage: "/exit", Description: "Exit the REPL."},
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

func Lookup(name string) (Spec, bool) {
	for _, spec := range Specs() {
		if strings.EqualFold(spec.Name, name) {
			return spec, true
		}
	}
	return Spec{}, false
}

func RenderHelp(w io.Writer) {
	for _, spec := range Specs() {
		fmt.Fprintf(w, "%-10s %s\n", spec.Usage, spec.Description)
	}
}
