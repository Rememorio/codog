package planmode

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type State struct {
	Active    bool   `json:"active"`
	Plan      string `json:"plan,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	ExitedAt  string `json:"exited_at,omitempty"`
}

type Report struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Status string `json:"status"`
	Path   string `json:"path"`
	State  State  `json:"state"`
}

func Path(workspace string) string {
	if workspace == "" {
		workspace = "."
	}
	return filepath.Join(workspace, ".codog", "plan.json")
}

func Load(workspace string) (State, error) {
	data, err := os.ReadFile(Path(workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	return normalize(state), nil
}

func Show(workspace string) (Report, error) {
	state, err := Load(workspace)
	if err != nil {
		return Report{}, err
	}
	return report(workspace, "show", state), nil
}

func Enter(workspace string, plan string) (Report, error) {
	state, err := Load(workspace)
	if err != nil {
		return Report{}, err
	}
	now := timestamp()
	if state.CreatedAt == "" {
		state.CreatedAt = now
	}
	state.Active = true
	state.ExitedAt = ""
	state.UpdatedAt = now
	if strings.TrimSpace(plan) != "" {
		state.Plan = strings.TrimSpace(plan)
	}
	if err := save(workspace, state); err != nil {
		return Report{}, err
	}
	return report(workspace, "enter", state), nil
}

func Set(workspace string, plan string) (Report, error) {
	plan = strings.TrimSpace(plan)
	if plan == "" {
		return Report{}, errors.New("plan text is required")
	}
	state, err := Load(workspace)
	if err != nil {
		return Report{}, err
	}
	now := timestamp()
	if state.CreatedAt == "" {
		state.CreatedAt = now
	}
	state.Active = true
	state.Plan = plan
	state.ExitedAt = ""
	state.UpdatedAt = now
	if err := save(workspace, state); err != nil {
		return Report{}, err
	}
	return report(workspace, "set", state), nil
}

func Exit(workspace string) (Report, error) {
	state, err := Load(workspace)
	if err != nil {
		return Report{}, err
	}
	now := timestamp()
	if state.CreatedAt == "" {
		state.CreatedAt = now
	}
	state.Active = false
	state.UpdatedAt = now
	state.ExitedAt = now
	if err := save(workspace, state); err != nil {
		return Report{}, err
	}
	return report(workspace, "exit", state), nil
}

func Clear(workspace string) (Report, error) {
	path := Path(workspace)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return Report{}, err
	}
	return report(workspace, "clear", State{}), nil
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Plan")
	fmt.Fprintf(w, "  Status           %s\n", report.Status)
	fmt.Fprintf(w, "  Path             %s\n", report.Path)
	if report.State.UpdatedAt != "" {
		fmt.Fprintf(w, "  Updated          %s\n", report.State.UpdatedAt)
	}
	if strings.TrimSpace(report.State.Plan) == "" {
		fmt.Fprintln(w, "  Text             none")
		return
	}
	fmt.Fprintln(w, "  Text")
	for _, line := range strings.Split(report.State.Plan, "\n") {
		fmt.Fprintf(w, "    %s\n", line)
	}
}

func RenderPrompt(state State) string {
	state = normalize(state)
	if !state.Active {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("<codog_plan_mode active=\"true\">\n")
	builder.WriteString("You are in plan mode. Inspect, ask clarifying questions when needed, and produce an implementation plan. Do not modify files, create commits, run mutating commands, or perform irreversible actions until the user exits plan mode.\n")
	if strings.TrimSpace(state.Plan) != "" {
		builder.WriteString("\nCurrent plan:\n")
		builder.WriteString(strings.TrimSpace(state.Plan))
		builder.WriteString("\n")
	}
	builder.WriteString("</codog_plan_mode>")
	return builder.String()
}

func save(workspace string, state State) error {
	state = normalize(state)
	path := Path(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func report(workspace string, action string, state State) Report {
	state = normalize(state)
	status := "inactive"
	if state.Active {
		status = "active"
	}
	return Report{
		Kind:   "plan",
		Action: action,
		Status: status,
		Path:   Path(workspace),
		State:  state,
	}
}

func normalize(state State) State {
	state.Plan = strings.TrimSpace(state.Plan)
	return state
}

func timestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}
