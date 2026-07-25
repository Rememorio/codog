package tui

func (m *model) setRawOutput(enabled bool) {
	m.rawOutput = enabled
	if enabled {
		m.transcriptMode = false
		m.status = "raw output"
	} else {
		m.status = "ready"
	}
	m.reflowTerminalExperience()
	m.viewport.GotoBottom()
}

func rawOutputNotice(enabled bool) string {
	if enabled {
		return "Raw output on: transcript text is shown without terminal styling or Markdown rendering."
	}
	return "Raw output off: rich transcript rendering restored."
}
