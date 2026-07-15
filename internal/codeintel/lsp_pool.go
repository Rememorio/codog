package codeintel

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

const lspShutdownTimeout = time.Second

// LSPClientPool owns initialized language-server sessions for a long-lived
// Codog runtime. Queries for the same language, command, and workspace reuse
// one stdio process and are serialized because LSP document versions are
// session scoped.
type LSPClientPool struct {
	mu       sync.Mutex
	sessions map[string]*pooledLSPSession
	closed   bool
}

type pooledLSPSession struct {
	mu        sync.Mutex
	language  string
	command   string
	workspace string
	process   *lspProcess
}

type lspProcess struct {
	client *lspClient
	stdin  io.WriteCloser
	cancel context.CancelFunc
	wait   func() error
}

type lspQueryOutcome struct {
	result LSPQueryResult
	err    error
}

// NewLSPClientPool constructs an empty language-server session pool.
func NewLSPClientPool() *LSPClientPool {
	return &LSPClientPool{sessions: map[string]*pooledLSPSession{}}
}

// Close terminates every language-server process owned by the pool.
func (p *LSPClientPool) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	sessions := make([]*pooledLSPSession, 0, len(p.sessions))
	for _, session := range p.sessions {
		sessions = append(sessions, session)
	}
	p.sessions = nil
	p.mu.Unlock()

	var joined error
	for _, session := range sessions {
		session.mu.Lock()
		joined = errors.Join(joined, session.closeProcess(true))
		session.mu.Unlock()
	}
	return joined
}

// Invalidate closes and forgets the session for language. The next query will
// reload its recorded command and initialize a new process.
func (p *LSPClientPool) Invalidate(language string) error {
	if p == nil {
		return nil
	}
	language, err := normalizeLanguage(language)
	if err != nil {
		return err
	}
	p.mu.Lock()
	session := p.sessions[language]
	delete(p.sessions, language)
	p.mu.Unlock()
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.closeProcess(true)
}

// Query executes a request through a persistent session using configuration
// recorded in store.
func (p *LSPClientPool) Query(ctx context.Context, store LSPStore, language string, request LSPQueryRequest) (LSPQueryResult, error) {
	if p == nil {
		return LSPQueryResult{}, errors.New("LSP client pool is nil")
	}
	language, err := normalizeLanguage(language)
	if err != nil {
		return LSPQueryResult{}, err
	}
	server, err := store.loadForQuery(language)
	if err != nil {
		return LSPQueryResult{}, err
	}
	workspace, err := lspWorkspace(server.Workspace, store.Workspace)
	if err != nil {
		return LSPQueryResult{}, err
	}
	session, err := p.session(language, server.Command, workspace)
	if err != nil {
		return LSPQueryResult{}, err
	}
	return session.query(ctx, request)
}

func (p *LSPClientPool) session(language, command, workspace string) (*pooledLSPSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, errors.New("LSP client pool is closed")
	}
	session := p.sessions[language]
	if session == nil || session.command != command || session.workspace != workspace {
		if session != nil {
			session.mu.Lock()
			_ = session.closeProcess(true)
			session.mu.Unlock()
		}
		session = &pooledLSPSession{language: language, command: command, workspace: workspace}
		p.sessions[language] = session
	}
	return session, nil
}

func (s *pooledLSPSession) query(ctx context.Context, request LSPQueryRequest) (LSPQueryResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	action, err := NormalizeLSPAction(request.Action)
	if err != nil {
		return LSPQueryResult{}, err
	}
	queryCtx, cancel := lspQueryContext(ctx, action)
	defer cancel()
	if s.process == nil {
		process, err := startLSPProcess(queryCtx, s.workspace, s.command)
		if err != nil {
			return LSPQueryResult{}, err
		}
		s.process = process
	}

	done := make(chan lspQueryOutcome, 1)
	go func() {
		result, queryErr := runPooledLSPQuery(queryCtx, s.process.client, s.workspace, s.language, request)
		done <- lspQueryOutcome{result: result, err: queryErr}
	}()
	select {
	case outcome := <-done:
		if outcome.err != nil {
			_ = s.closeProcess(false)
		}
		return outcome.result, outcome.err
	case <-queryCtx.Done():
		_ = s.closeProcess(false)
		outcome := <-done
		if action == "diagnostics" && outcome.err == nil {
			return outcome.result, nil
		}
		return LSPQueryResult{}, queryCtx.Err()
	}
}

func runPooledLSPQuery(ctx context.Context, client *lspClient, workspace, language string, request LSPQueryRequest) (LSPQueryResult, error) {
	run, err := newLSPQueryRun(ctx, workspace, "persistent", language, request)
	if err != nil {
		return LSPQueryResult{}, err
	}
	defer run.cancel()
	run.client = client
	client.applyWorkspaceEdits = request.Apply
	client.notifications = nil
	client.notificationEvents = nil
	client.workspaceEdits = nil
	client.workspaceTextEdits = 0
	client.workspaceApplied = false
	if err := run.openDocument(); err != nil {
		return LSPQueryResult{}, err
	}
	if run.info.RequiresDocument {
		defer func() {
			_ = client.notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": run.uri}})
		}()
	}
	return run.execute()
}

func startLSPProcess(ctx context.Context, workspace, command string) (*lspProcess, error) {
	if command == "" {
		return nil, errors.New("lsp command is required")
	}
	lifetime, cancel := context.WithCancel(context.Background())
	cmd := lspShellCommand(lifetime, command)
	cmd.Dir = workspace
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	process := &lspProcess{
		stdin:  stdin,
		cancel: cancel,
		wait: func() error {
			err := cmd.Wait()
			if err != nil && stderr.Len() > 0 {
				return fmt.Errorf("%w: %s", err, stderr.String())
			}
			return err
		},
	}
	process.client = &lspClient{stdin: stdin, stdout: bufio.NewReader(stdout), workspace: workspace}
	done := make(chan error, 1)
	go func() { done <- initializeLSPClient(process.client, workspace) }()
	select {
	case err := <-done:
		if err != nil {
			_ = process.close(false)
			return nil, err
		}
		return process, nil
	case <-ctx.Done():
		_ = process.close(false)
		<-done
		return nil, ctx.Err()
	}
}

func initializeLSPClient(client *lspClient, workspace string) error {
	_, err := client.request("initialize", map[string]any{
		"processId": nil,
		"rootUri":   fileURI(workspace),
		"capabilities": map[string]any{
			"textDocument": map[string]any{},
			"workspace":    map[string]any{},
		},
		"clientInfo": map[string]any{"name": "codog"},
	})
	if err != nil {
		return err
	}
	return client.notify("initialized", map[string]any{})
}

func (s *pooledLSPSession) closeProcess(graceful bool) error {
	if s.process == nil {
		return nil
	}
	err := s.process.close(graceful)
	s.process = nil
	return err
}

func (p *lspProcess) close(graceful bool) error {
	if p == nil {
		return nil
	}
	gracefulCompleted := false
	if graceful && p.client != nil {
		done := make(chan struct{})
		go func() {
			_, _ = p.client.request("shutdown", nil)
			_ = p.client.notify("exit", nil)
			close(done)
		}()
		select {
		case <-done:
			gracefulCompleted = true
		case <-time.After(lspShutdownTimeout):
		}
	}
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if !gracefulCompleted && p.cancel != nil {
		p.cancel()
	}
	var err error
	if p.wait != nil {
		err = p.wait()
	}
	if p.cancel != nil {
		p.cancel()
	}
	if gracefulCompleted && errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func lspWorkspace(primary, fallback string) (string, error) {
	if primary != "" {
		return primary, nil
	}
	if fallback != "" {
		return fallback, nil
	}
	return os.Getwd()
}
