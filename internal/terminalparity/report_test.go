package terminalparity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildReportsReadyInteractiveSurface(t *testing.T) {
	report := Build()

	require.Equal(t, "ready", report.Status)
	require.Greater(t, report.SlashCommandCount, 100)
	require.Greater(t, report.ResumeSafeSlashCount, 10)
	require.Equal(t, len(RequiredInteractiveCommands()), report.RequiredCommandCount)
	require.Empty(t, report.MissingRequiredCommands)
	require.Empty(t, report.DuplicateSlashCommands)
	require.True(t, report.TUISubmitSupported)
	require.True(t, report.TUISlashCompletion)
	require.True(t, report.TUIFullScreenLayout)
	require.True(t, report.TUIInlineLayout)
	require.True(t, report.TUIDefaultInline)
	require.True(t, report.TUIResumePicker)
	require.True(t, report.TUIWorkspaceTrustPrompt)
	require.True(t, report.TUITranscriptViewport)
	require.True(t, report.TUILocalHelpPanel)
	require.True(t, report.TUISettingsTabs)
	require.True(t, report.TUIExtensionTabs)
	require.True(t, report.TUIRuntimeTabs)
	require.True(t, report.TUIConversationTabs)
	require.True(t, report.TUIMemorySelector)
	require.True(t, report.TUIExportDialog)
	require.True(t, report.TUIStatusBar)
	require.True(t, report.PermissionCommandsPresent)
	require.True(t, report.StatusCommandsPresent)
	require.True(t, report.SessionCommandsPresent)
}

func TestRequiredInteractiveCommandsAreStableCopy(t *testing.T) {
	commands := RequiredInteractiveCommands()
	require.Contains(t, commands, "/permissions")
	require.Contains(t, commands, "/resume")

	commands[0] = "/mutated"

	require.NotEqual(t, "/mutated", RequiredInteractiveCommands()[0])
}
