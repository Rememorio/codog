package harness

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunUsesMockProvider(t *testing.T) {
	report, err := Run(context.Background())
	require.NoError(t, err)
	require.True(t, report.OK)
	require.Contains(t, report.Output, "codog harness ok")
	require.Equal(t, 1, report.Iterations)
	require.GreaterOrEqual(t, report.MessageCount, 2)
}
