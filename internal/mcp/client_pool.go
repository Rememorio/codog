package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/Rememorio/codog/internal/config"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
)

const persistentMCPShutdownTimeout = 2 * time.Second

// ClientPool owns persistent MCP sessions for one Codog tool registry. A
// session is reused until its server configuration changes, the connection
// closes, or the pool is closed.
type ClientPool struct {
	mu      sync.Mutex
	clients map[string]*pooledClient
	closed  bool
}

type pooledClient struct {
	name   string
	server config.MCPServerConfig
	hash   string

	mu      sync.Mutex
	options syncOptions
	client  *protocol.Client
	session *protocol.ClientSession
	cancel  context.CancelFunc
	roots   map[string]Root
}

type syncOptions struct {
	mu    sync.RWMutex
	value ClientOptions
}

// NewClientPool constructs an empty persistent MCP client pool.
func NewClientPool() *ClientPool {
	return &ClientPool{clients: map[string]*pooledClient{}}
}

// Close gracefully closes every MCP session owned by the pool.
func (p *ClientPool) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	clients := make([]*pooledClient, 0, len(p.clients))
	for _, client := range p.clients {
		clients = append(clients, client)
	}
	p.clients = nil
	p.mu.Unlock()

	var joined error
	for _, client := range clients {
		joined = errors.Join(joined, client.close())
	}
	return joined
}

func (p *ClientPool) client(name string, server config.MCPServerConfig, options ClientOptions) (*pooledClient, error) {
	if p == nil {
		return nil, errors.New("MCP client pool is nil")
	}
	hash := ServerConfigHash(server)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, errors.New("MCP client pool is closed")
	}
	client := p.clients[name]
	if client == nil || client.hash != hash {
		if client != nil {
			_ = client.close()
		}
		client = &pooledClient{name: name, server: resolveMCPServerConfig(server), hash: hash}
		p.clients[name] = client
	}
	client.options.set(options)
	return client, nil
}

func (o *syncOptions) set(options ClientOptions) {
	options.Roots = append([]Root(nil), options.Roots...)
	o.mu.Lock()
	o.value = options
	o.mu.Unlock()
}

func (o *syncOptions) get() ClientOptions {
	o.mu.RLock()
	defer o.mu.RUnlock()
	options := o.value
	options.Roots = append([]Root(nil), options.Roots...)
	return options
}

func (c *pooledClient) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

func (c *pooledClient) closeLocked() error {
	var err error
	if c.session != nil {
		err = c.session.Close()
	}
	if c.cancel != nil {
		c.cancel()
	}
	c.client = nil
	c.session = nil
	c.cancel = nil
	c.roots = nil
	return err
}

func (c *pooledClient) sessionFor(ctx context.Context) (*protocol.ClientSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		c.syncRootsLocked()
		return c.session, nil
	}
	return c.connectLocked(ctx)
}

func (c *pooledClient) connectLocked(ctx context.Context) (*protocol.ClientSession, error) {
	client := protocol.NewClient(&protocol.Implementation{Name: "codog", Version: "0.1.1"}, c.protocolOptions())
	for _, root := range c.options.get().Roots {
		client.AddRoots(&protocol.Root{URI: root.URI, Name: root.Name})
	}
	transport, err := c.transport()
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	type connectResult struct {
		session *protocol.ClientSession
		err     error
	}
	connected := make(chan connectResult, 1)
	go func() {
		session, connectErr := client.Connect(lifetime, transport, nil)
		connected <- connectResult{session: session, err: connectErr}
	}()
	select {
	case result := <-connected:
		if result.err != nil {
			cancel()
			return nil, result.err
		}
		c.client = client
		c.session = result.session
		c.cancel = cancel
		c.roots = rootsByURI(c.options.get().Roots)
		return result.session, nil
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
}

func (c *pooledClient) protocolOptions() *protocol.ClientOptions {
	notify := func(method string, params any) {
		options := c.options.get()
		if options.OnNotification == nil {
			return
		}
		data, _ := json.Marshal(params)
		options.OnNotification(Notification{Method: method, Params: data})
	}
	return &protocol.ClientOptions{
		Capabilities: &protocol.ClientCapabilities{
			RootsV2: &protocol.RootCapabilities{ListChanged: false},
			Elicitation: &protocol.ElicitationCapabilities{
				Form: &protocol.FormElicitationCapabilities{},
				URL:  &protocol.URLElicitationCapabilities{},
			},
		},
		ElicitationHandler: func(ctx context.Context, request *protocol.ElicitRequest) (*protocol.ElicitResult, error) {
			params := request.Params
			requestedSchema := map[string]any(nil)
			if params.RequestedSchema != nil {
				data, err := json.Marshal(params.RequestedSchema)
				if err != nil {
					return nil, err
				}
				if err := json.Unmarshal(data, &requestedSchema); err != nil {
					return nil, err
				}
			}
			options := c.options.get()
			if options.Elicit == nil {
				return &protocol.ElicitResult{Action: "decline"}, nil
			}
			result, err := options.Elicit(ctx, ElicitationRequest{
				Mode: params.Mode, Message: params.Message, RequestedSchema: requestedSchema,
				URL: params.URL, ElicitationID: params.ElicitationID,
			})
			if err != nil {
				return nil, err
			}
			return &protocol.ElicitResult{Action: result.Action, Content: result.Content}, nil
		},
		ToolListChangedHandler: func(_ context.Context, request *protocol.ToolListChangedRequest) {
			notify("notifications/tools/list_changed", request.Params)
		},
		PromptListChangedHandler: func(_ context.Context, request *protocol.PromptListChangedRequest) {
			notify("notifications/prompts/list_changed", request.Params)
		},
		ResourceListChangedHandler: func(_ context.Context, request *protocol.ResourceListChangedRequest) {
			notify("notifications/resources/list_changed", request.Params)
		},
		ResourceUpdatedHandler: func(_ context.Context, request *protocol.ResourceUpdatedNotificationRequest) {
			notify("notifications/resources/updated", request.Params)
		},
		LoggingMessageHandler: func(_ context.Context, request *protocol.LoggingMessageRequest) {
			notify("notifications/message", request.Params)
		},
		ProgressNotificationHandler: func(_ context.Context, request *protocol.ProgressNotificationClientRequest) {
			notify("notifications/progress", request.Params)
		},
		ElicitationCompleteHandler: func(_ context.Context, request *protocol.ElicitationCompleteNotificationRequest) {
			notify("notifications/elicitation/complete", request.Params)
		},
		KeepAlive: 30 * time.Second,
	}
}

func (c *pooledClient) transport() (protocol.Transport, error) {
	if isHTTPServer(c.server) {
		endpoint, err := validateHTTPServerURL(c.server.URL)
		if err != nil {
			return nil, err
		}
		return &protocol.StreamableClientTransport{
			Endpoint: endpoint,
			HTTPClient: &http.Client{Transport: mcpHeaderTransport{
				base: http.DefaultTransport, server: c.server,
			}},
		}, nil
	}
	if c.server.Command == "" {
		return nil, errors.New("missing command")
	}
	command := exec.Command(c.server.Command, c.server.Args...)
	command.Env = append(os.Environ(), c.server.Env...)
	command.Stderr = io.Discard
	return &protocol.CommandTransport{Command: command, TerminateDuration: persistentMCPShutdownTimeout}, nil
}

type mcpHeaderTransport struct {
	base   http.RoundTripper
	server config.MCPServerConfig
}

// RoundTrip resolves dynamic MCP headers immediately before each HTTP request.
func (t mcpHeaderTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	headers, err := resolveHTTPHeaders(request.Context(), t.server)
	if err != nil {
		return nil, err
	}
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	for key, value := range headers {
		cloned.Header.Set(key, value)
	}
	return t.base.RoundTrip(cloned)
}

func (c *pooledClient) syncRootsLocked() {
	if c.client == nil {
		return
	}
	next := rootsByURI(c.options.get().Roots)
	for uri := range c.roots {
		if _, ok := next[uri]; !ok {
			c.client.RemoveRoots(uri)
		}
	}
	for uri, root := range next {
		if current, ok := c.roots[uri]; ok && current == root {
			continue
		}
		c.client.AddRoots(&protocol.Root{URI: root.URI, Name: root.Name})
	}
	c.roots = next
}

func rootsByURI(roots []Root) map[string]Root {
	result := make(map[string]Root, len(roots))
	for _, root := range roots {
		result[root.URI] = root
	}
	return result
}

func poolRequest[T any](ctx context.Context, pool *ClientPool, name string, server config.MCPServerConfig, options ClientOptions, call func(context.Context, *protocol.ClientSession) (T, error)) (T, error) {
	var zero T
	client, err := pool.client(name, server, options)
	if err != nil {
		return zero, err
	}
	requestCtx, cancel := mcpCallContext(ctx, server)
	defer cancel()
	for attempt := 0; attempt < 2; attempt++ {
		session, err := client.sessionFor(requestCtx)
		if err != nil {
			return zero, err
		}
		result, err := call(requestCtx, session)
		if err == nil {
			return result, nil
		}
		if !errors.Is(err, protocol.ErrConnectionClosed) && !errors.Is(err, protocol.ErrSessionMissing) {
			return zero, err
		}
		_ = client.close()
	}
	return zero, errors.New("MCP connection closed after reconnect")
}

func mcpCallContext(ctx context.Context, server config.MCPServerConfig) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, stdioRequestTimeout(server))
}
