package coveragecheck

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseProfileNormalizesModulePathsAndEndLines(t *testing.T) {
	blocks, err := ParseProfile(strings.NewReader(`mode: atomic
github.com/Rememorio/codog/internal/example/example.go:10.2,13.1 2 1
github.com/Rememorio/codog/internal/example/example.go:20.2,20.12 1 0
`), "github.com/Rememorio/codog")
	require.NoError(t, err)
	require.Equal(t, []Block{
		{File: "internal/example/example.go", StartLine: 10, EndLine: 12, Count: 1},
		{File: "internal/example/example.go", StartLine: 20, EndLine: 20, Count: 0},
	}, blocks)
}

func TestParseProfileRejectsMalformedInput(t *testing.T) {
	_, err := ParseProfile(strings.NewReader("mode: count\ninvalid\n"), "")
	require.EqualError(t, err, "coverage profile line 2 is malformed")
}

func TestParseDiffExtractsAddedProductionLines(t *testing.T) {
	changed, err := ParseDiff(strings.NewReader(`diff --git a/internal/example/example.go b/internal/example/example.go
--- a/internal/example/example.go
+++ b/internal/example/example.go
@@ -8,0 +9,2 @@
+first
+second
@@ -20 +22,0 @@
-removed
`))
	require.NoError(t, err)
	require.Equal(t, map[int]struct{}{9: {}, 10: {}}, changed["internal/example/example.go"])
}

func TestParseDiffSupportsQuotedPaths(t *testing.T) {
	changed, err := ParseDiff(strings.NewReader("+++ \"b/internal/example file.go\"\n@@ -0,0 +1 @@\n+package example\n"))
	require.NoError(t, err)
	require.Contains(t, changed, "internal/example file.go")
}

func TestParseDiffRejectsHunkWithoutFile(t *testing.T) {
	_, err := ParseDiff(strings.NewReader("@@ -0,0 +1 @@\n+line\n"))
	require.EqualError(t, err, "diff line 1 has a hunk without a destination file")
}

func TestEvaluateCountsOnlyCoverableChangedLines(t *testing.T) {
	blocks := []Block{
		{File: "internal/example/example.go", StartLine: 10, EndLine: 12, Count: 1},
		{File: "internal/example/example.go", StartLine: 20, EndLine: 20, Count: 0},
	}
	changed := ChangedLines{
		"internal/example/example.go": {9: {}, 10: {}, 11: {}, 20: {}},
	}

	report := Evaluate(blocks, changed)
	require.Equal(t, 2, report.Covered)
	require.Equal(t, 3, report.Coverable)
	require.InDelta(t, 66.67, report.Percent, 0.01)
	require.Equal(t, []Location{{File: "internal/example/example.go", Line: 20}}, report.Uncovered)
	require.False(t, report.MeetsThreshold(85))
	require.True(t, report.MeetsThreshold(60))
}

func TestEvaluatePassesWhenNoChangedLinesAreCoverable(t *testing.T) {
	report := Evaluate(nil, ChangedLines{"README.md": {1: {}}})
	require.Equal(t, 100.0, report.Percent)
	require.True(t, report.MeetsThreshold(100))
}
