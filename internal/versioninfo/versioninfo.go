package versioninfo

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
)

var (
	GitSHA    = "unknown"
	GitBranch = "unknown"
	GitDirty  = "unknown"
	BuildDate = "unknown"
)

type Report struct {
	Kind            string `json:"kind"`
	Action          string `json:"action"`
	Status          string `json:"status"`
	HumanReadable   string `json:"human_readable"`
	Version         string `json:"version"`
	GitSHA          string `json:"git_sha,omitempty"`
	GitSHAShort     string `json:"git_sha_short,omitempty"`
	GitBranch       string `json:"git_branch,omitempty"`
	GitDirty        string `json:"git_dirty,omitempty"`
	BuildDate       string `json:"build_date,omitempty"`
	BuildTarget     string `json:"build_target"`
	GoVersion       string `json:"go_version"`
	ModulePath      string `json:"module_path,omitempty"`
	ExecutablePath  string `json:"executable_path,omitempty"`
	WorkspaceGitSHA string `json:"workspace_git_sha,omitempty"`
	WorkspaceMatch  *bool  `json:"workspace_match,omitempty"`
	Hint            string `json:"hint,omitempty"`
}

type Metadata struct {
	GitSHA    string
	GitBranch string
	GitDirty  string
	BuildDate string
}

func Build(version string, workspace string) Report {
	return BuildWithMetadata(version, workspace, Metadata{
		GitSHA:    GitSHA,
		GitBranch: GitBranch,
		GitDirty:  GitDirty,
		BuildDate: BuildDate,
	})
}

func BuildWithMetadata(version string, workspace string, metadata Metadata) Report {
	report := Report{
		Kind:        "version",
		Action:      "show",
		Status:      "ok",
		Version:     version,
		GitSHA:      known(metadata.GitSHA),
		GitBranch:   known(metadata.GitBranch),
		GitDirty:    known(metadata.GitDirty),
		BuildDate:   known(metadata.BuildDate),
		BuildTarget: runtime.GOOS + "/" + runtime.GOARCH,
		GoVersion:   runtime.Version(),
		ModulePath:  modulePath(),
	}
	report.GitSHAShort = shortSHA(report.GitSHA)
	if path, err := os.Executable(); err == nil {
		report.ExecutablePath = path
	}
	if sha, ok := workspaceSHA(workspace); ok {
		report.WorkspaceGitSHA = sha
		if report.GitSHA != "" {
			match := report.GitSHA == sha
			report.WorkspaceMatch = &match
		}
	}
	if report.GitSHA == "" {
		report.Hint = "Build metadata did not include a git SHA; set internal/versioninfo.GitSHA with -ldflags for provenance-sensitive builds."
	} else if report.WorkspaceMatch != nil && !*report.WorkspaceMatch {
		report.Hint = "The running binary was built from a different commit than the current workspace HEAD."
	}
	report.HumanReadable = RenderString(report)
	return report
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprint(w, RenderString(report))
}

func RenderString(report Report) string {
	gitSHA := emptyAsUnknown(report.GitSHAShort)
	if gitSHA == "unknown" {
		gitSHA = emptyAsUnknown(report.GitSHA)
	}
	var builder strings.Builder
	builder.WriteString("Codog\n")
	builder.WriteString(fmt.Sprintf("  Version          %s\n", report.Version))
	builder.WriteString(fmt.Sprintf("  Git SHA          %s\n", gitSHA))
	builder.WriteString(fmt.Sprintf("  Branch           %s\n", emptyAsUnknown(report.GitBranch)))
	builder.WriteString(fmt.Sprintf("  Dirty            %s\n", emptyAsUnknown(report.GitDirty)))
	builder.WriteString(fmt.Sprintf("  Target           %s\n", report.BuildTarget))
	builder.WriteString(fmt.Sprintf("  Go version       %s\n", report.GoVersion))
	builder.WriteString(fmt.Sprintf("  Build date       %s", emptyAsUnknown(report.BuildDate)))
	if report.WorkspaceGitSHA != "" {
		builder.WriteString("\n")
		builder.WriteString(fmt.Sprintf("  Workspace SHA    %s", shortSHA(report.WorkspaceGitSHA)))
	}
	if report.Hint != "" {
		builder.WriteString("\n")
		builder.WriteString(fmt.Sprintf("  Hint             %s", report.Hint))
	}
	return builder.String()
}

func known(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "unknown" {
		return ""
	}
	return value
}

func emptyAsUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func shortSHA(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func modulePath() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return info.Main.Path
}

func workspaceSHA(workspace string) (string, bool) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return "", false
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", false
	}
	cmd := exec.Command("git", "-C", workspace, "rev-parse", "HEAD")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", false
	}
	sha := strings.TrimSpace(stdout.String())
	return sha, sha != ""
}
