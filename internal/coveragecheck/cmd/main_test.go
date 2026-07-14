package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunReportsChangedLineCoverage(t *testing.T) {
	profile, diff := writeInputs(t, `mode: atomic
github.com/Rememorio/codog/internal/example/example.go:10.2,11.12 1 1
`, `+++ b/internal/example/example.go
@@ -9,0 +10 @@
+covered
`)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"--profile", profile, "--diff", diff, "--module", "github.com/Rememorio/codog", "--threshold", "85"}, &stdout, &stderr)

	require.Zero(t, code)
	require.Contains(t, stdout.String(), "100.00% (1/1 coverable lines)")
	require.Empty(t, stderr.String())
}

func TestRunFailsBelowThreshold(t *testing.T) {
	profile, diff := writeInputs(t, `mode: atomic
github.com/Rememorio/codog/internal/example/example.go:20.2,20.12 1 0
`, `+++ b/internal/example/example.go
@@ -19,0 +20 @@
+uncovered
`)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"--profile=" + profile, "--diff=" + diff, "--module", "github.com/Rememorio/codog"}, &stdout, &stderr)

	require.Equal(t, 1, code)
	require.Contains(t, stdout.String(), "uncovered: internal/example/example.go:20")
	require.Contains(t, stderr.String(), "is below 85.00%")
}

func TestRunRejectsInvalidArgumentsAndInputs(t *testing.T) {
	tests := []struct {
		name string
		args func(t *testing.T) []string
	}{
		{name: "missing paths", args: func(*testing.T) []string { return nil }},
		{name: "invalid flag", args: func(*testing.T) []string { return []string{"--missing"} }},
		{name: "missing profile", args: func(t *testing.T) []string {
			return []string{"--profile", filepath.Join(t.TempDir(), "missing"), "--diff", filepath.Join(t.TempDir(), "diff")}
		}},
		{name: "invalid profile", args: func(t *testing.T) []string {
			profile, diff := writeInputs(t, "invalid\n", "+++ b/example.go\n@@ -0,0 +1 @@\n+line\n")
			return []string{"--profile", profile, "--diff", diff}
		}},
		{name: "missing diff", args: func(t *testing.T) []string {
			profile := writeFile(t, "profile.out", "mode: atomic\n")
			return []string{"--profile", profile, "--diff", filepath.Join(t.TempDir(), "missing")}
		}},
		{name: "invalid diff", args: func(t *testing.T) []string {
			profile, diff := writeInputs(t, "mode: atomic\n", "@@ malformed\n")
			return []string{"--profile", profile, "--diff", diff}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			require.Equal(t, 2, run(test.args(t), &stdout, &stderr))
			require.NotEmpty(t, stderr.String())
		})
	}
}

func writeInputs(t *testing.T, profile string, diff string) (string, string) {
	t.Helper()
	return writeFile(t, "coverage.out", profile), writeFile(t, "changes.diff", diff)
}

func writeFile(t *testing.T, name string, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}
