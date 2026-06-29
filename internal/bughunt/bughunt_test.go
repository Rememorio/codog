package bughunt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScanFindsLikelyGoBugs(t *testing.T) {
	workspace := t.TempDir()
	source := `package demo

import "os"

func risky(value any) {
	_, _ = os.Open("missing")
	_ = value.(string)
	for _, item := range []string{"a"} {
		defer os.Remove(item)
		go func() { println(item) }()
	}
	panic("boom")
	os.Exit(1)
}
`
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte(source), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main_test.go"), []byte("package demo\n\nfunc TestPanic(){ panic(\"test\") }\n"), 0o644))

	report, err := Scan(workspace, Options{Limit: 20})
	require.NoError(t, err)
	require.Equal(t, "bughunter", report.Kind)
	require.Equal(t, "findings", report.Status)
	require.GreaterOrEqual(t, report.Total, 5)
	requireContainsRule(t, report, "ignored-return-value")
	requireContainsRule(t, report, "unchecked-type-assertion")
	requireContainsRule(t, report, "defer-in-loop")
	requireContainsRule(t, report, "goroutine-loop-capture")
	requireContainsRule(t, report, "panic-in-runtime-path")
	requireContainsRule(t, report, "process-exit")
	for _, finding := range report.Findings {
		require.NotEqual(t, "main_test.go", finding.Path)
	}
}

func TestScanRespectsScopeAndLimit(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "a.go"), []byte("package pkg\nfunc A(){ panic(\"a\") }\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "b.go"), []byte("package demo\nfunc B(){ panic(\"b\") }\n"), 0o644))

	report, err := Scan(workspace, Options{Scope: "pkg", Limit: 1})
	require.NoError(t, err)
	require.Equal(t, "pkg", report.Scope)
	require.Len(t, report.Findings, 1)
	require.Equal(t, "pkg/a.go", report.Findings[0].Path)
}

func TestScanRejectsEscapedScope(t *testing.T) {
	_, err := Scan(t.TempDir(), Options{Scope: "../outside"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workspace")
}

func requireContainsRule(t *testing.T, report Report, rule string) {
	t.Helper()
	for _, finding := range report.Findings {
		if finding.Rule == rule {
			return
		}
	}
	require.Failf(t, "missing rule", "expected rule %s in %+v", rule, report.Findings)
}
