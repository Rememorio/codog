package slash

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderHelpIncludesCoreCommands(t *testing.T) {
	var out bytes.Buffer
	RenderHelp(&out)
	require.Contains(t, out.String(), "/status")
	require.Contains(t, out.String(), "/init")
	require.Contains(t, out.String(), "/state")
	require.Contains(t, out.String(), "/terminal-setup")
	require.Contains(t, out.String(), "/terminalSetup")
	require.Contains(t, out.String(), "/remote-env")
	require.Contains(t, out.String(), "/memory")
	require.Contains(t, out.String(), "/project")
	require.Contains(t, out.String(), "/env")
	require.Contains(t, out.String(), "/init-verifiers")
	require.Contains(t, out.String(), "/context")
	require.Contains(t, out.String(), "/ctx_viz")
	require.Contains(t, out.String(), "/config")
	require.Contains(t, out.String(), "/model")
	require.Contains(t, out.String(), "/advisor")
	require.Contains(t, out.String(), "/max-tokens")
	require.Contains(t, out.String(), "/max-turns")
	require.Contains(t, out.String(), "/btw")
	require.Contains(t, out.String(), "/permissions")
	require.Contains(t, out.String(), "/allowed-tools")
	require.Contains(t, out.String(), "/clear")
	require.Contains(t, out.String(), "/doctor")
	require.Contains(t, out.String(), "/sandbox")
	require.Contains(t, out.String(), "/sandbox-toggle")
	require.Contains(t, out.String(), "/heapdump")
	require.Contains(t, out.String(), "/compact")
	require.Contains(t, out.String(), "/undo")
	require.Contains(t, out.String(), "/usage")
	require.Contains(t, out.String(), "/stats")
	require.Contains(t, out.String(), "/think-back")
	require.Contains(t, out.String(), "/thinkback")
	require.Contains(t, out.String(), "/thinkback-play")
	require.Contains(t, out.String(), "/rate-limit-options")
	require.Contains(t, out.String(), "/reset-limits")
	require.Contains(t, out.String(), "/plan")
	require.Contains(t, out.String(), "/ultraplan")
	require.Contains(t, out.String(), "/exit-plan")
	require.Contains(t, out.String(), "/diff")
	require.Contains(t, out.String(), "/commit")
	require.Contains(t, out.String(), "/branch")
	require.Contains(t, out.String(), "/tag")
	require.Contains(t, out.String(), "/git")
	require.Contains(t, out.String(), "/log")
	require.Contains(t, out.String(), "/changelog")
	require.Contains(t, out.String(), "/release-notes")
	require.Contains(t, out.String(), "/stash")
	require.Contains(t, out.String(), "/blame")
	require.Contains(t, out.String(), "/run")
	require.Contains(t, out.String(), "/node")
	require.Contains(t, out.String(), "/python")
	require.Contains(t, out.String(), "/test")
	require.Contains(t, out.String(), "/build")
	require.Contains(t, out.String(), "/lint")
	require.Contains(t, out.String(), "/symbols")
	require.Contains(t, out.String(), "/diagnostics")
	require.Contains(t, out.String(), "/map")
	require.Contains(t, out.String(), "/references")
	require.Contains(t, out.String(), "/definition")
	require.Contains(t, out.String(), "/hover")
	require.Contains(t, out.String(), "/completion")
	require.Contains(t, out.String(), "/format")
	require.Contains(t, out.String(), "/export")
	require.Contains(t, out.String(), "/history")
	require.Contains(t, out.String(), "/summary")
	require.Contains(t, out.String(), "/todos")
	require.Contains(t, out.String(), "/prompt-history")
	require.Contains(t, out.String(), "/resume")
	require.Contains(t, out.String(), "/rewind")
	require.Contains(t, out.String(), "/files")
	require.Contains(t, out.String(), "/search")
	require.Contains(t, out.String(), "/security-review")
	require.Contains(t, out.String(), "/review")
	require.Contains(t, out.String(), "/ultrareview")
	require.Contains(t, out.String(), "/commit-push-pr")
	require.Contains(t, out.String(), "/pr-comments")
	require.Contains(t, out.String(), "/pr_comments")
	require.Contains(t, out.String(), "/passes")
	require.Contains(t, out.String(), "/focus")
	require.Contains(t, out.String(), "/unfocus")
	require.Contains(t, out.String(), "/add-dir")
	require.Contains(t, out.String(), "/output-style")
	require.Contains(t, out.String(), "/session")
	require.Contains(t, out.String(), "/backfill-sessions")
	require.Contains(t, out.String(), "/commands")
	require.Contains(t, out.String(), "/hooks")
	require.Contains(t, out.String(), "/mcp")
	require.Contains(t, out.String(), "/acp")
	require.Contains(t, out.String(), "/bridge-kick")
	require.Contains(t, out.String(), "/system-prompt")
	require.Contains(t, out.String(), "/tool-details")
	require.Contains(t, out.String(), "/tokens")
	require.Contains(t, out.String(), "/version")
	require.Contains(t, out.String(), "/templates")
	require.Contains(t, out.String(), "/copy")
	require.Contains(t, out.String(), "/agents")
	require.Contains(t, out.String(), "/team")
	require.Contains(t, out.String(), "/tasks")
	require.Contains(t, out.String(), "/bashes")
	require.Contains(t, out.String(), "/background")
	require.Contains(t, out.String(), "/cron")
	require.Contains(t, out.String(), "/plugin")
	require.Contains(t, out.String(), "/plugins")
	require.Contains(t, out.String(), "/marketplace")
	require.Contains(t, out.String(), "/providers")
	require.Contains(t, out.String(), "/login")
	require.Contains(t, out.String(), "/oauth-refresh")
	require.Contains(t, out.String(), "/logout")
	require.Contains(t, out.String(), "/voice")
	require.Contains(t, out.String(), "/remote-setup")
	require.Contains(t, out.String(), "/web-setup")
	require.Contains(t, out.String(), "/remote-control")
	require.Contains(t, out.String(), "/bridge")
	require.Contains(t, out.String(), "/desktop")
	require.Contains(t, out.String(), "/mobile")
	require.Contains(t, out.String(), "/reload-plugins")
	require.Contains(t, out.String(), "/upgrade")
	require.Contains(t, out.String(), "/install")
	require.Contains(t, out.String(), "/debug-tool-call")
}

func TestCandidatesFiltersSlashCommands(t *testing.T) {
	require.Contains(t, Candidates("/ac"), "/acp")

	candidates := Candidates("/co")
	require.Contains(t, candidates, "/compact")
	require.Contains(t, candidates, "/completion")
	require.Contains(t, candidates, "/commands")
	require.Contains(t, candidates, "/config auth")
	require.Contains(t, Candidates("/cr"), "/cron create ")
	require.Contains(t, Candidates("/cr"), "/cron due")
	require.Contains(t, Candidates("/cr"), "/cron list")
	require.Contains(t, Candidates("/cr"), "/cron run-due")
	require.Contains(t, Candidates("/te"), "/team create ")
	require.Contains(t, Candidates("/te"), "/team list")
	require.Contains(t, Candidates("/te"), "/team status ")
	require.Contains(t, Candidates("/te"), "/team logs ")
	require.Contains(t, Candidates("/te"), "/team watch ")
	require.NotContains(t, candidates, "/status")
	require.Contains(t, Candidates("/un"), "/undo")
	require.Empty(t, Candidates("co"))
}

func TestHookCandidatesIncludeCurrentEvents(t *testing.T) {
	candidates := Candidates("/hooks run ")
	for _, candidate := range []string{
		"/hooks run user-prompt-submit",
		"/hooks run session-start",
		"/hooks run stop",
		"/hooks run pre-compact",
		"/hooks run cwd-changed",
		"/hooks run file-changed",
		"/hooks run instructions-loaded",
	} {
		require.Contains(t, candidates, candidate)
	}
	healthCandidates := Candidates("/hooks health ")
	require.Contains(t, healthCandidates, "/hooks health pre")
	require.Contains(t, healthCandidates, "/hooks health notification")
}

func TestCandidatesWithOptionsIncludeModelAndSessions(t *testing.T) {
	candidates := CandidatesWithOptions("/resume", CandidateOptions{
		Model:            "claude-test",
		ActiveSessionID:  "active-session",
		RecentSessionIDs: []string{"recent-one", "recent-two"},
	})
	require.Contains(t, candidates, "/resume active-session")
	require.Contains(t, candidates, "/resume recent-one")
	require.Contains(t, candidates, "/resume recent-two")

	modelCandidates := CandidatesWithOptions("/model", CandidateOptions{Model: "claude-test"})
	require.Contains(t, modelCandidates, "/model claude-test")
	require.Contains(t, modelCandidates, "/model ")

	modelValueCandidates := CandidatesWithOptions("/model ", CandidateOptions{Model: "claude-test"})
	require.Contains(t, modelValueCandidates, "/model claude-test")
	require.NotContains(t, modelValueCandidates, "/model")
}

func TestCandidatesWithOptionsIncludeExtraCandidates(t *testing.T) {
	candidates := CandidatesWithOptions("/team/", CandidateOptions{Extra: []string{"/team/review ", "/team/audit "}})
	require.Equal(t, []string{"/team/audit ", "/team/review "}, candidates)
}
