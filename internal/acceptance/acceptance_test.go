package acceptance

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRealBinaryDirectSlashHelp(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	result := runCodog(t, bin, workspace, configHome, nil, "/help")

	require.Equal(t, 0, result.Code, result.Combined())
	require.Contains(t, result.Combined(), "Usage:")
	require.Contains(t, result.Combined(), "codog-acceptance")
	require.Contains(t, result.Combined(), "repl")
}

func TestRealBinaryHelpAfterGlobalFlagsShortCircuits(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	result := runCodog(t, bin, workspace, configHome, nil, "--model", "openai/gpt-5.5", "--help")

	require.Equal(t, 0, result.Code, result.Combined())
	require.Contains(t, result.Stdout, "Usage:")
	require.Contains(t, result.Stdout, "repl")
	require.NotContains(t, result.Combined(), "config_load_failed")
	require.NotContains(t, result.Combined(), "missing_credentials")
}

func TestRealBinaryReplSlashHelpAndExit(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	result := runCodog(t, bin, workspace, configHome, []byte("/help\n/exit\n"), "--model", "openai/gpt-5.5", "repl")

	require.Equal(t, 0, result.Code, result.Combined())
	require.Contains(t, result.Combined(), "Type /help for commands")
	require.Contains(t, result.Combined(), "/status")
	require.Contains(t, result.Combined(), "/exit")
	require.NotContains(t, result.Combined(), "missing_credentials")
}

func TestRealBinaryReplBareHelpAndQuitAreLocal(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	result := runCodog(t, bin, workspace, configHome, []byte("help\nquit\n"), "--model", "openai/gpt-5.5", "repl")

	require.Equal(t, 0, result.Code, result.Combined())
	require.Contains(t, result.Combined(), "Type /help for commands")
	require.Contains(t, result.Combined(), "/status")
	require.Contains(t, result.Combined(), "/exit")
	require.NotContains(t, result.Combined(), "missing_credentials")
}

func TestRealBinaryConcurrentReplSessionsDoNotCollide(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	const count = 8
	results := make([]commandResult, count)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index] = runCodog(t, bin, workspace, configHome, []byte("/exit\n"), "--model", "openai/gpt-5.5", "repl")
		}(i)
	}
	wg.Wait()

	for _, result := range results {
		require.Equal(t, 0, result.Code, result.Combined())
		require.NotContains(t, result.Combined(), "file exists")
		require.NotContains(t, result.Combined(), "already exists")
	}
}

func TestRealBinaryCapabilitiesExposeTerminalContract(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	result := runCodog(t, bin, workspace, configHome, nil, "capabilities", "--json")

	require.Equal(t, 0, result.Code, result.Combined())
	require.Contains(t, result.Stdout, `"terminal"`)
	require.Contains(t, result.Stdout, `"slash_command_count"`)
	require.Contains(t, result.Stdout, `"tui_submit_supported"`)
}

type commandResult struct {
	Code   int
	Stdout string
	Stderr string
}

func (r commandResult) Combined() string {
	return r.Stdout + r.Stderr
}

func runCodog(t *testing.T, bin string, workspace string, configHome string, stdin []byte, args ...string) commandResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = workspace
	cmd.Env = acceptanceEnv(configHome)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if ok := errorAs(err, &exitErr); ok {
			code = exitErr.ExitCode()
		} else {
			require.NoError(t, err)
		}
	}
	return commandResult{Code: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

func acceptanceEnv(configHome string) []string {
	env := os.Environ()
	env = append(env,
		"CODOG_CONFIG_HOME="+configHome,
		"ANTHROPIC_API_KEY=",
		"ANTHROPIC_AUTH_TOKEN=",
		"OPENAI_API_KEY=acceptance-openai-key",
		"XAI_API_KEY=",
		"DASHSCOPE_API_KEY=",
		"CODOG_DISABLE_UPDATE_CHECK=1",
	)
	return env
}

func buildCodogBinary(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "codog-acceptance")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/codog")
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	require.NoError(t, cmd.Run(), out.String())
	return bin
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	info, err := os.Stat(filepath.Join(root, "go.mod"))
	require.NoError(t, err)
	require.False(t, info.IsDir())
	return root
}

func errorAs(err error, target any) bool {
	type unwrapper interface {
		Unwrap() error
	}
	for err != nil {
		switch t := target.(type) {
		case **exec.ExitError:
			if v, ok := err.(*exec.ExitError); ok {
				*t = v
				return true
			}
		}
		if u, ok := err.(unwrapper); ok {
			err = u.Unwrap()
			continue
		}
		return false
	}
	return false
}

func TestAcceptanceHarnessUsesRealBinary(t *testing.T) {
	bin := buildCodogBinary(t)
	require.True(t, strings.Contains(filepath.Base(bin), "codog-acceptance"))
	require.Eventually(t, func() bool {
		info, err := os.Stat(bin)
		return err == nil && !info.IsDir() && info.Size() > 0
	}, time.Second, 10*time.Millisecond)
}
