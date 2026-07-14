package harness

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/doctor"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/stretchr/testify/require"
)

func TestHarnessReportValidatorsRejectInvalidData(t *testing.T) {
	t.Parallel()

	require.Error(t, verifyInitialBudgetReport(budgetHarnessReport{}))
	require.Error(t, verifySetBudgetReport(budgetHarnessReport{}))
	require.Error(t, verifyResetBudgetReport(budgetHarnessReport{}))
	require.Error(t, verifyHarnessChecks("report", "output", true, false))
	require.NoError(t, verifyHarnessChecks("report", "output", true, true))

	moduleDir := filepath.Join(t.TempDir(), "module")
	var packet taskPacketHarnessReport
	require.Error(t, verifyTaskPacketCreated(packet, "output", moduleDir))
	packet.TaskID = "task-1"
	packet.Status = "running"
	packet.Description = "Implement typed task packet parity"
	require.Error(t, verifyTaskPacketCreated(packet, "output", moduleDir))
	packet.Task.ID = packet.TaskID
	packet.Task.Kind = "task_packet"
	packet.Task.TaskPacket = []byte(`{}`)
	require.Error(t, verifyTaskPacketCreated(packet, "output", moduleDir))
	packet.ResolvedScope.Scope = "module"
	packet.ResolvedScope.Path = "internal/taskpacket"
	packet.ResolvedScope.AbsolutePath = moduleDir
	require.Error(t, verifyTaskPacketCreated(packet, "output", moduleDir))
}

func TestHarnessDecodeAndFileValidationErrors(t *testing.T) {
	t.Parallel()

	var decoded map[string]any
	_, err := decodeHarnessOutput(&decoded, func() (string, error) {
		return "", errors.New("execute failed")
	})
	require.ErrorContains(t, err, "execute failed")
	_, err = decodeHarnessOutput(&decoded, func() (string, error) {
		return "{", nil
	})
	require.Error(t, err)

	path := filepath.Join(t.TempDir(), "fixture.txt")
	require.NoError(t, os.WriteFile(path, []byte("alpha"), 0o600))
	require.Error(t, verifyHarnessFileContainsAll(path, "alpha", "beta"))
	require.Error(t, verifyHarnessFileContainsAll(path+".missing", "alpha"))
}

func TestVerifyDiagnosticsDoctorReportErrors(t *testing.T) {
	t.Parallel()

	_, _, _, err := verifyDiagnosticsDoctorReport(doctor.Report{})
	require.Error(t, err)

	report := validDiagnosticsDoctorReport()
	report.CheckNames = nil
	_, _, _, err = verifyDiagnosticsDoctorReport(report)
	require.Error(t, err)

	report = validDiagnosticsDoctorReport()
	report.Summary.Total = 0
	_, _, _, err = verifyDiagnosticsDoctorReport(report)
	require.Error(t, err)

	report = validDiagnosticsDoctorReport()
	report.Checks[0].Data = nil
	_, _, _, err = verifyDiagnosticsDoctorReport(report)
	require.Error(t, err)

	identity, freshness, baseCommit, err := verifyDiagnosticsDoctorReport(validDiagnosticsDoctorReport())
	require.NoError(t, err)
	require.Equal(t, "head", identity.HeadSHA)
	require.Equal(t, "fresh", freshness.Status)
	require.Equal(t, "matches", baseCommit.Status)
}

func validDiagnosticsDoctorReport() doctor.Report {
	return doctor.Report{
		Kind:       "doctor",
		Action:     "doctor",
		CheckNames: []string{"auth", "config home", "workspace", "permissions", "tools", "hooks", "sandbox"},
		Summary:    doctor.Summary{Total: 1},
		Checks: []doctor.Check{{
			Name: "Git",
			Data: map[string]any{
				"identity":    gitops.Identity{HeadSHA: "head"},
				"freshness":   gitops.BranchFreshness{Status: "fresh"},
				"base_commit": gitops.BaseCommitCheck{Status: "matches"},
			},
		}},
	}
}

func TestSignedUpdaterHarnessServerRoutes(t *testing.T) {
	t.Parallel()

	_, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	state := &signedUpdaterHarnessServer{
		artifactPayload: []byte("artifact"),
		privateKey:      privateKey,
		baseURL:         "https://example.test",
	}
	state.artifactSHA = sha256.Sum256(state.artifactPayload)

	manifest := httptest.NewRecorder()
	state.ServeHTTP(manifest, httptest.NewRequest(http.MethodGet, "/manifest.json", nil))
	require.Equal(t, http.StatusOK, manifest.Code)
	require.Contains(t, manifest.Body.String(), `"signature"`)

	artifact := httptest.NewRecorder()
	state.ServeHTTP(artifact, httptest.NewRequest(http.MethodGet, "/codog-test", nil))
	require.Equal(t, "artifact", artifact.Body.String())

	missing := httptest.NewRecorder()
	state.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/missing", nil))
	require.Equal(t, http.StatusNotFound, missing.Code)

	state.serveManifest(&failingHarnessResponseWriter{header: http.Header{}})
}

func TestOAuthRefreshHarnessServerRoutes(t *testing.T) {
	t.Parallel()

	state := &oauthRefreshHarnessServer{}
	discovery := httptest.NewRecorder()
	state.ServeHTTP(discovery, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil))
	require.Equal(t, http.StatusOK, discovery.Code)
	require.Contains(t, discovery.Body.String(), "token_endpoint")

	malformed := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader("refresh_token=%zz"))
	malformed.Header.Set("content-type", "application/x-www-form-urlencoded")
	malformedResponse := httptest.NewRecorder()
	state.ServeHTTP(malformedResponse, malformed)
	require.Equal(t, http.StatusBadRequest, malformedResponse.Code)

	invalid := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(url.Values{"grant_type": {"refresh_token"}}.Encode()))
	invalid.Header.Set("content-type", "application/x-www-form-urlencoded")
	invalidResponse := httptest.NewRecorder()
	state.ServeHTTP(invalidResponse, invalid)
	require.Equal(t, http.StatusBadRequest, invalidResponse.Code)

	validForm := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"old-refresh-token-secret"},
		"client_id":     {"client-harness"},
	}
	valid := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(validForm.Encode()))
	valid.Header.Set("content-type", "application/x-www-form-urlencoded")
	validResponse := httptest.NewRecorder()
	state.ServeHTTP(validResponse, valid)
	require.Equal(t, http.StatusOK, validResponse.Code)
	require.True(t, state.refreshSeen)

	missing := httptest.NewRecorder()
	state.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/missing", nil))
	require.Equal(t, http.StatusNotFound, missing.Code)
}

func TestMCPAuthHarnessServerRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	handler := mcpAuthHarnessServer{}
	methodResponse := httptest.NewRecorder()
	handler.ServeHTTP(methodResponse, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	require.Equal(t, http.StatusMethodNotAllowed, methodResponse.Code)

	decodeResponse := httptest.NewRecorder()
	handler.ServeHTTP(decodeResponse, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{")))
	require.Equal(t, http.StatusBadRequest, decodeResponse.Code)

	unsupportedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unsupportedResponse, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"unknown"}`)))
	require.Equal(t, http.StatusOK, unsupportedResponse.Code)
	require.Contains(t, unsupportedResponse.Body.String(), "unsupported method")

	for _, method := range []string{"initialize", "notifications/initialized", "tools/list", "resources/list"} {
		response := httptest.NewRecorder()
		body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `"}`
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body)))
		require.Contains(t, []int{http.StatusOK, http.StatusAccepted}, response.Code)
	}
}

func TestRemoteBridgeHarnessClientValidation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer secret-token", r.Header.Get("authorization"))
		if r.Method == http.MethodPost {
			require.Equal(t, "application/json", r.Header.Get("content-type"))
		}
		_, _ = w.Write([]byte("needle"))
	}))
	defer server.Close()

	client := remoteBridgeHarnessClient{baseURL: server.URL}
	require.NoError(t, client.runCases([]remoteBridgeHarnessCase{{
		method:   http.MethodPost,
		path:     "/ok",
		payload:  `{}`,
		status:   http.StatusOK,
		contains: []string{"needle"},
	}}))
	require.Error(t, client.runCase(remoteBridgeHarnessCase{
		method: http.MethodGet,
		path:   "/status",
		status: http.StatusCreated,
	}))
	require.Error(t, client.runCase(remoteBridgeHarnessCase{
		method:   http.MethodGet,
		path:     "/content",
		status:   http.StatusOK,
		contains: []string{"missing"},
	}))
	require.Error(t, client.runCases([]remoteBridgeHarnessCase{{
		method: http.MethodGet,
		path:   "/status",
		status: http.StatusCreated,
	}}))

	invalidClient := remoteBridgeHarnessClient{baseURL: "://invalid"}
	_, _, err := invalidClient.request(http.MethodGet, "/", "")
	require.Error(t, err)
	unreachableClient := remoteBridgeHarnessClient{baseURL: "http://127.0.0.1:1"}
	_, _, err = unreachableClient.request(http.MethodGet, "/", "")
	require.Error(t, err)
}

func TestSetupHelpersSurfaceFilesystemErrors(t *testing.T) {
	workspaceFile := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.WriteFile(workspaceFile, []byte("not a directory"), 0o600))
	_, _, err := setupDiagnosticsWorkspace(workspaceFile)
	require.Error(t, err)
	_, err = setupSkillActivationFixture(workspaceFile)
	require.Error(t, err)
	require.Error(t, setupOnboardingWorkspace(workspaceFile))
	_, err = setupMemoryLifecycle(workspaceFile)
	require.Error(t, err)

	zshWorkspace := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(zshWorkspace, ".zshrc"), 0o700))
	_, _, err = setupDiagnosticsWorkspace(zshWorkspace)
	require.Error(t, err)

	gitWorkspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(gitWorkspace, ".git"), []byte("invalid"), 0o600))
	_, _, err = setupDiagnosticsWorkspace(gitWorkspace)
	require.Error(t, err)

	readmeWorkspace := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(readmeWorkspace, "README.md"), 0o700))
	_, _, err = setupDiagnosticsWorkspace(readmeWorkspace)
	require.Error(t, err)

	lockedWorkspace := t.TempDir()
	require.NoError(t, runHarnessGit(lockedWorkspace, "init", "-q", "-b", "main"))
	require.NoError(t, os.WriteFile(filepath.Join(lockedWorkspace, ".git", "index.lock"), nil, 0o600))
	_, _, err = setupDiagnosticsWorkspace(lockedWorkspace)
	require.Error(t, err)

	skillWorkspace := t.TempDir()
	skillPath := filepath.Join(skillWorkspace, ".codog", "skills", "review", "SKILL.md")
	require.NoError(t, os.MkdirAll(skillPath, 0o700))
	_, err = setupSkillActivationFixture(skillWorkspace)
	require.Error(t, err)

	emptyMemoryWorkspace := t.TempDir()
	_, err = resetMemoryLifecycle(emptyMemoryWorkspace)
	require.Error(t, err)
	invalidMemoryWorkspace := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(invalidMemoryWorkspace, "AGENTS.md"), 0o700))
	_, err = resetMemoryLifecycle(invalidMemoryWorkspace)
	require.Error(t, err)
	emptyMemoryFileWorkspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(emptyMemoryFileWorkspace, "AGENTS.md"), nil, 0o600))
	_, err = resetMemoryLifecycle(emptyMemoryFileWorkspace)
	require.Error(t, err)
	multipleMemoryWorkspace := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(multipleMemoryWorkspace, ".codog"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(multipleMemoryWorkspace, ".codog", "instructions.md"), []byte("extra"), 0o600))
	_, err = setupMemoryLifecycle(multipleMemoryWorkspace)
	require.Error(t, err)

	shimDir := t.TempDir()
	gitShim := filepath.Join(shimDir, "git")
	require.NoError(t, os.WriteFile(gitShim, []byte("#!/bin/sh\n[ \"$1\" = init ] && exit 0\nexit 1\n"), 0o700))
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, _, err = setupDiagnosticsWorkspace(t.TempDir())
	require.Error(t, err)
}

func TestRunSkillActivationCommandPropagatesExecutionError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := runSkillActivationCommand(ctx, t.TempDir(), "--version")
	require.Error(t, err)
}

type failingHarnessResponseWriter struct {
	header http.Header
}

func (w *failingHarnessResponseWriter) Header() http.Header {
	return w.header
}

func (*failingHarnessResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func (*failingHarnessResponseWriter) WriteHeader(int) {}
