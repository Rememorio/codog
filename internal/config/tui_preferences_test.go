package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigLoadsTerminalExperiencePreferences(t *testing.T) {
	var cfg Config
	require.NoError(t, json.Unmarshal([]byte(`{
		"tui_raw_output_mode": true,
		"tui_pet": "codog"
	}`), &cfg))
	require.NotNil(t, cfg.TUIRawOutputMode)
	require.True(t, *cfg.TUIRawOutputMode)
	require.Equal(t, "codog", cfg.TUIPet)
}
