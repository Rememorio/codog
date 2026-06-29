package future

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReportJSONIsMachineReadable(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, RenderReportJSON(&out, NewReport("test")))

	var report Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "codog", report.Project)
	require.NotEmpty(t, report.Surfaces)
	_, ok := Find("remote")
	require.True(t, ok)
}
