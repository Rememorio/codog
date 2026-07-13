package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

func TestNormalizeThemeNameSupportsCanonicalNamesAndLegacyAliases(t *testing.T) {
	tests := map[string]string{
		"":                 "auto",
		"default":          "auto",
		"system":           "auto",
		"DARK":             "dark",
		"dark-colorblind":  "dark-daltonized",
		"light-colorblind": "light-daltonized",
		"ansi":             "dark-ansi",
		"plain":            "no-color",
		"light-ansi":       "light-ansi",
		"light-daltonized": "light-daltonized",
		"dark-daltonized":  "dark-daltonized",
	}
	for input, expected := range tests {
		actual, ok := NormalizeThemeName(input)
		require.True(t, ok, input)
		require.Equal(t, expected, actual, input)
	}
	_, ok := NormalizeThemeName("unknown")
	require.False(t, ok)
}

func TestThemePickerPreviewsAcceptsAndCancels(t *testing.T) {
	m := newThemePickerModel("dark", true)
	require.Equal(t, "dark", m.selectedTheme())
	require.Contains(t, m.View(), "Let's get started.")
	require.Contains(t, m.View(), "Choose the text style")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(themePickerModel)
	require.Equal(t, "light", m.selectedTheme())

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(themePickerModel)
	require.True(t, m.accepted)
	require.NotNil(t, cmd)
	require.Empty(t, m.View())

	m = newThemePickerModel("dark", false)
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(themePickerModel)
	require.False(t, m.accepted)
	require.NotNil(t, cmd)
}

func TestThemePickerFitsNarrowTerminal(t *testing.T) {
	m := newThemePickerModel("dark", true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 12, Height: 8})
	m = updated.(themePickerModel)
	for _, line := range strings.Split(m.View(), "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), 12, line)
	}
}

func TestThemePalettesRenderEverySupportedMode(t *testing.T) {
	roles := []string{"assistant", "tool", "permission", "question", "user", "success", "error", "system"}
	for _, name := range ThemeNames() {
		styles := stylesForTheme(name)
		require.NotEmpty(t, styles.header().Render("header"), name)
		require.NotEmpty(t, styles.inlineHeader().Render("header"), name)
		require.NotEmpty(t, styles.status().Render("status"), name)
		require.NotEmpty(t, styles.inlineStatus().Render("status"), name)
		require.NotEmpty(t, styles.panelTitle().Render("title"), name)
		require.NotEmpty(t, styles.completion().Render("completion"), name)
		require.NotEmpty(t, styles.completionTitle().Render("title"), name)
		require.NotEmpty(t, styles.selectedCompletion().Render("selected"), name)
		for _, role := range roles {
			require.NotEmpty(t, styles.role(role).Render(role), name+"/"+role)
		}
		ta := newPromptTextarea("")
		styles.applyTextarea(&ta)
		require.True(t, ta.Focused())
	}

	stylesForTheme("unknown").applyTextarea(nil)
}

func TestThemePickerNavigationBoundariesAndFallbacks(t *testing.T) {
	m := newThemePickerModel("unknown", false)
	require.Nil(t, m.Init())
	require.Equal(t, "auto", m.selectedTheme())

	updated, _ := m.Update(tea.WindowSizeMsg{})
	m = updated.(themePickerModel)
	require.Equal(t, 80, m.width)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(themePickerModel)
	require.Equal(t, "no-color", m.selectedTheme())
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = updated.(themePickerModel)
	require.Equal(t, "auto", m.selectedTheme())
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(themePickerModel)
	require.Equal(t, "no-color", m.selectedTheme())

	m.selected = -1
	require.Equal(t, "auto", m.selectedTheme())
	require.Equal(t, 0, indexOfTheme(ThemeNames(), "missing"))
	require.Equal(t, "custom", themeLabel("custom"))

	m = newThemePickerModel("dark", false)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(themePickerModel)
	require.False(t, m.accepted)
	require.NotNil(t, cmd)
}

func TestRuntimeThemePickerPreviewsRestoresAndPersists(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.theme = "dark"
	m.selectTheme = func(_ context.Context, selected string) (RuntimeControlResult, error) {
		return RuntimeControlResult{
			Title:  "Theme",
			Status: "theme " + selected,
			Lines:  []string{"Theme: " + selected},
		}, nil
	}
	m.applyTheme()

	updated, cmd := m.startInput("/theme")
	require.Nil(t, cmd)
	m = updated.(model)
	require.True(t, m.themePicker)
	require.Equal(t, "dark", m.theme)
	require.Contains(t, m.View(), "theme")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	require.Equal(t, "light", m.theme)
	require.Contains(t, m.View(), "Light mode")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.False(t, m.themePicker)
	require.Equal(t, "dark", m.theme)

	updated, _ = m.startInput("/theme")
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	msg := cmd()
	updated, _ = m.Update(msg)
	m = updated.(model)
	require.Equal(t, "light", m.theme)
	require.Equal(t, "theme light", m.status)
	require.Contains(t, m.View(), "Theme")
}

func TestRuntimeThemePickerRestoresThemeWhenPersistenceFails(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.theme = "dark"
	m.selectTheme = func(context.Context, string) (RuntimeControlResult, error) {
		return RuntimeControlResult{}, errors.New("write failed")
	}
	m.applyTheme()
	m.openThemePicker()
	m.moveThemePicker(1)

	updated, cmd := m.acceptThemePicker()
	m = updated.(model)
	require.NotNil(t, cmd)
	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.Equal(t, "dark", m.theme)
	require.Equal(t, "theme error", m.status)
	require.Contains(t, m.View(), "write failed")
}

func TestNoColorThemeDoesNotAssignTerminalColors(t *testing.T) {
	styles := stylesForTheme("no-color")
	require.True(t, styles.palette.noColor)
	require.Empty(t, styles.panelTitle().GetForeground())
	require.Empty(t, styles.selectedCompletion().GetBackground())

	view := renderThemePicker("no-color", len(ThemeNames())-1, 80, styles)
	require.NotContains(t, view, "\x1b[")
	require.Contains(t, strings.ToLower(view), "no color")

	trust := newWorkspaceTrustModelWithTheme("/workspace/project", "no-color")
	require.NotContains(t, trust.View(), "\x1b[")
	sessions := newSessionPickerModelWithTheme([]SessionChoice{{ID: "session", Title: "Session"}}, "no-color")
	require.NotContains(t, sessions.View(), "\x1b[")
}
