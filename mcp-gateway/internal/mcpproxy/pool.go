package mcpproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"sync"
	"time"

	"github.com/akmatori/mcp-gateway/internal/cache"
	"github.com/akmatori/mcp-gateway/internal/mcp"
)

const (
	// DefaultIdleTimeout is how long a connection sits idle before being closed.
	DefaultIdleTimeout = 5 * time.Minute

	// DefaultCleanupInterval is how often the pool checks for idle connections.
	DefaultCleanupInterval = 1 * time.Minute

	// DefaultSchemaRefreshInterval is how often tool schemas are refreshed.
	DefaultSchemaRefreshInterval = 5 * time.Minute

	// DefaultSchemaCacheTTL is the TTL for cached tool schemas.
	DefaultSchemaCacheTTL = 5 * time.Minute

	// DefaultConnectTimeout is the timeout for establishing a new connection.
	DefaultConnectTimeout = 30 * time.Second
)

// TransportType defines how to communicate with an external MCP server.
type TransportType string

const (
	TransportSSE   TransportType = "sse"
	TransportStdio TransportType = "stdio"
)

// MCPServerConfig defines the configuration for connecting to an external MCP server.
type MCPServerConfig struct {
	Transport       TransportType     `json:"transport"`
	URL             string            `json:"url,omitempty"`              // For SSE transport
	Command         string            `json:"command,omitempty"`          // For stdio transport
	Args            []string          `json:"args,omitempty"`             // For stdio transport
	EnvVars         map[string]string `json:"env_vars,omitempty"`         // For stdio transport
	NamespacePrefix string            `json:"namespace_prefix,omitempty"` // e.g., "ext.github"
	AuthConfig      json.RawMessage   `json:"auth_config,omitempty"`      // Auth to inject
}

// MCPConnection represents an active connection to an external MCP server.
type MCPConnection struct {
	mu         sync.RWMutex
	config     MCPServerConfig
	instanceID uint
	tools      []mcp.Tool
	lastUsed   time.Time
	connected  bool
	closed     bool

	// SSE transport
	httpClient *http.Client
	sseCancel  context.CancelFunc

	// Stdio transport
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr io.ReadCloser

	// JSON-RPC state
	nextID int
	idMu   sync.Mutex

	logger *slog.Logger
}

// MCPConnectionPool manages connections to external MCP servers.
type MCPConnectionPool struct {
	mu          sync.RWMutex
	connections map[uint]*MCPConnection
	schemaCache *cache.Cache
	idleTimeout time.Duration
	stopCleanup chan struct{}
	stopped     bool
	logger      *slog.Logger

	// For testing: allow overriding connect behavior
	connectFunc func(ctx context.Context, conn *MCPConnection) error
}

// PoolOption configures the connection pool.
type PoolOption func(*MCPConnectionPool)

// WithIdleTimeout sets the idle timeout for connections.
func WithIdleTimeout(d time.Duration) PoolOption {
	return func(p *MCPConnectionPool) {
		p.idleTimeout = d
	}
}

// WithLogger sets the logger for the pool.
func WithLogger(logger *slog.Logger) PoolOption {
	return func(p *MCPConnectionPool) {
		p.logger = logger
	}
}

// WithConnectFunc overrides the connection function (for testing).
func WithConnectFunc(f func(ctx context.Context, conn *MCPConnection) error) PoolOption {
	return func(p *MCPConnectionPool) {
		p.connectFunc = f
	}
}

// NewPool creates a new MCP connection pool.
func NewPool(opts ...PoolOption) *MCPConnectionPool {
	p := &MCPConnectionPool{
		connections: make(map[uint]*MCPConnection),
		schemaCache: cache.New(DefaultSchemaCacheTTL, DefaultCleanupInterval),
		idleTimeout: DefaultIdleTimeout,
		stopCleanup: make(chan struct{}),
		logger:      slog.Default(),
	}
	for _, opt := range opts {
		opt(p)
	}
	go p.cleanupLoop()
	return p
}

// GetOrConnect returns an existing connection or establishes a new one.
func (p *MCPConnectionPool) GetOrConnect(ctx context.Context, instanceID uint, config MCPServerConfig) (*MCPConnection, error) {
	p.mu.RLock()
	conn, exists := p.connections[instanceID]
	p.mu.RUnlock()

	if exists {
		conn.mu.RLock()
		connected := conn.connected && !conn.closed
		conn.mu.RUnlock()

		if connected {
			conn.mu.Lock()
			conn.lastUsed = time.Now()
			conn.mu.Unlock()
			return conn, nil
		}
		// Connection exists but is not healthy — remove and reconnect
		p.mu.Lock()
		delete(p.connections, instanceID)
		p.mu.Unlock()
		conn.close()
	}

	// Create new connection
	conn = &MCPConnection{
		config:     config,
		instanceID: instanceID,
		lastUsed:   time.Now(),
		httpClient: &http.Client{Timeout: DefaultConnectTimeout},
		logger:     p.logger.With("instance_id", instanceID, "transport", config.Transport),
	}

	connectFn := p.connectFunc
	if connectFn == nil {
		connectFn = defaultConnect
	}

	if err := connectFn(ctx, conn); err != nil {
		return nil, fmt.Errorf("connect to MCP server (instance %d): %w", instanceID, err)
	}

	conn.mu.Lock()
	conn.connected = true
	conn.mu.Unlock()

	// Fetch tool schemas
	tools, err := p.fetchToolSchemas(ctx, conn)
	if err != nil {
		conn.close()
		return nil, fmt.Errorf("fetch tool schemas (instance %d): %w", instanceID, err)
	}

	conn.mu.Lock()
	conn.tools = tools
	conn.mu.Unlock()

	// Cache the tools
	cacheKey := fmt.Sprintf("tools:%d", instanceID)
	p.schemaCache.Set(cacheKey, tools)

	p.mu.Lock()
	p.connections[instanceID] = conn
	p.mu.Unlock()

	p.logger.Info("connected to MCP server",
		"instance_id", instanceID,
		"transport", config.Transport,
		"tools_count", len(tools),
	)

	return conn, nil
}

// GetCachedTools returns cached tool schemas for an instance without connecting.
func (p *MCPConnectionPool) GetCachedTools(instanceID uint) ([]mcp.Tool, bool) {
	cacheKey := fmt.Sprintf("tools:%d", instanceID)
	if val, ok := p.schemaCache.Get(cacheKey); ok {
		if tools, ok := val.([]mcp.Tool); ok {
			return tools, true
		}
	}
	return nil, false
}

// CallTool forwards a tool call to the external MCP server.
func (p *MCPConnectionPool) CallTool(ctx context.Context, instanceID uint, toolName string, args map[string]interface{}) (*mcp.CallToolResult, error) {
	p.mu.RLock()
	conn, exists := p.connections[instanceID]
	p.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("no connection for instance %d", instanceID)
	}

	conn.mu.Lock()
	conn.lastUsed = time.Now()
	conn.mu.Unlock()

	return conn.callTool(ctx, toolName, args)
}

// Close closes a specific connection.
func (p *MCPConnectionPool) Close(instanceID uint) {
	p.mu.Lock()
	conn, exists := p.connections[instanceID]
	if exists {
		delete(p.connections, instanceID)
	}
	p.mu.Unlock()

	if exists {
		conn.close()
		p.logger.Info("closed MCP connection", "instance_id", instanceID)
	}
}

// CloseAll closes all connections and stops the cleanup goroutine.
func (p *MCPConnectionPool) CloseAll() {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	conns := make(map[uint]*MCPConnection, len(p.connections))
	for k, v := range p.connections {
		conns[k] = v
	}
	p.connections = make(map[uint]*MCPConnection)
	p.mu.Unlock()

	close(p.stopCleanup)
	p.schemaCache.Stop()

	for _, conn := range conns {
		conn.close()
	}

	p.logger.Info("closed all MCP connections", "count", len(conns))
}

// ConnectionCount returns the number of active connections.
func (p *MCPConnectionPool) ConnectionCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.connections)
}

// IsConnected checks if an instance has an active connection.
func (p *MCPConnectionPool) IsConnected(instanceID uint) bool {
	p.mu.RLock()
	conn, exists := p.connections[instanceID]
	p.mu.RUnlock()

	if !exists {
		return false
	}

	conn.mu.RLock()
	defer conn.mu.RUnlock()
	return conn.connected && !conn.closed
}

// GetTools returns the discovered tools for a connected instance.
func (p *MCPConnectionPool) GetTools(instanceID uint) ([]mcp.Tool, bool) {
	p.mu.RLock()
	conn, exists := p.connections[instanceID]
	p.mu.RUnlock()

	if !exists {
		return nil, false
	}

	conn.mu.RLock()
	defer conn.mu.RUnlock()
	if conn.tools == nil {
		return nil, false
	}
	toolsCopy := make([]mcp.Tool, len(conn.tools))
	copy(toolsCopy, conn.tools)
	return toolsCopy, true
}

// HealthCheck checks the health of a specific connection.
func (p *MCPConnectionPool) HealthCheck(ctx context.Context, instanceID uint) error {
	p.mu.RLock()
	conn, exists := p.connections[instanceID]
	p.mu.RUnlock()

	if !exists {
		return fmt.Errorf("no connection for instance %d", instanceID)
	}

	return conn.ping(ctx)
}

// RefreshTools re-fetches tool schemas from a connected server.
func (p *MCPConnectionPool) RefreshTools(ctx context.Context, instanceID uint) ([]mcp.Tool, error) {
	p.mu.RLock()
	conn, exists := p.connections[instanceID]
	p.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("no connection for instance %d", instanceID)
	}

	tools, err := p.fetchToolSchemas(ctx, conn)
	if err != nil {
		return nil, err
	}

	conn.mu.Lock()
	conn.tools = tools
	conn.mu.Unlock()

	cacheKey := fmt.Sprintf("tools:%d", instanceID)
	p.schemaCache.Set(cacheKey, tools)

	return tools, nil
}

// cleanupLoop runs periodically to close idle connections.
func (p *MCPConnectionPool) cleanupLoop() {
	ticker := time.NewTicker(DefaultCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.cleanupIdle()
		case <-p.stopCleanup:
			return
		}
	}
}

// cleanupIdle closes connections that have been idle beyond the timeout.
func (p *MCPConnectionPool) cleanupIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for id, conn := range p.connections {
		conn.mu.RLock()
		idle := now.Sub(conn.lastUsed) > p.idleTimeout
		conn.mu.RUnlock()

		if idle {
			delete(p.connections, id)
			go conn.close()
			p.logger.Info("closed idle MCP connection", "instance_id", id)
		}
	}
}

// fetchToolSchemas sends tools/list to the external MCP server.
func (p *MCPConnectionPool) fetchToolSchemas(ctx context.Context, conn *MCPConnection) ([]mcp.Tool, error) {
	resp, err := conn.sendRequest(ctx, "tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("tools/list error: %s (code %d)", resp.Error.Message, resp.Error.Code)
	}

	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("marshal tools/list result: %w", err)
	}

	var listResult mcp.ListToolsResult
	if err := json.Unmarshal(resultBytes, &listResult); err != nil {
		return nil, fmt.Errorf("unmarshal tools/list result: %w", err)
	}

	return listResult.Tools, nil
}

// --- MCPConnection methods ---

func (c *MCPConnection) nextRequestID() int {
	c.idMu.Lock()
	defer c.idMu.Unlock()
	c.nextID++
	return c.nextID
}

func (c *MCPConnection) sendRequest(ctx context.Context, method string, params interface{}) (*mcp.Response, error) {
	c.mu.RLock()
	transport := c.config.Transport
	closed := c.closed
	c.mu.RUnlock()

	if closed {
		return nil, fmt.Errorf("connection is closed")
	}

	switch transport {
	case TransportSSE:
		return c.sendSSERequest(ctx, method, params)
	case TransportStdio:
		return c.sendStdioRequest(ctx, method, params)
	default:
		return nil, fmt.Errorf("unsupported transport: %s", transport)
	}
}

func (c *MCPConnection) sendSSERequest(ctx context.Context, method string, params interface{}) (*mcp.Response, error) {
	var paramsRaw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		paramsRaw = b
	}

	reqBody := mcp.Request{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  method,
		Params:  paramsRaw,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// For SSE transport, POST to the server's /mcp endpoint
	url := c.config.URL
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	var mcpResp mcp.Response
	if err := json.NewDecoder(resp.Body).Decode(&mcpResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &mcpResp, nil
}

func (c *MCPConnection) sendStdioRequest(ctx context.Context, method string, params interface{}) (*mcp.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stdin == nil || c.stdout == nil {
		return nil, fmt.Errorf("stdio not initialized")
	}

	var paramsRaw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		paramsRaw = b
	}

	reqBody := mcp.Request{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  method,
		Params:  paramsRaw,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Write request followed by newline
	bodyBytes = append(bodyBytes, '\n')
	if _, err := c.stdin.Write(bodyBytes); err != nil {
		return nil, fmt.Errorf("write to stdin: %w", err)
	}

	// Read response line
	type result struct {
		resp *mcp.Response
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
			ch <- result{nil, fmt.Errorf("read from stdout: %w", err)}
			return
		}
		var mcpResp mcp.Response
		if err := json.Unmarshal(line, &mcpResp); err != nil {
			ch <- result{nil, fmt.Errorf("decode response: %w", err)}
			return
		}
		ch <- result{&mcpResp, nil}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.resp, r.err
	}
}

func (c *MCPConnection) callTool(ctx context.Context, toolName string, args map[string]interface{}) (*mcp.CallToolResult, error) {
	params := mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	}

	resp, err := c.sendRequest(ctx, "tools/call", params)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("tool call error: %s (code %d)", resp.Error.Message, resp.Error.Code)
	}

	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("marshal tool result: %w", err)
	}

	var callResult mcp.CallToolResult
	if err := json.Unmarshal(resultBytes, &callResult); err != nil {
		return nil, fmt.Errorf("unmarshal tool result: %w", err)
	}

	return &callResult, nil
}

func (c *MCPConnection) ping(ctx context.Context) error {
	resp, err := c.sendRequest(ctx, "ping", nil)
	if err != nil {
		return fmt.Errorf("ping failed: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("ping error: %s", resp.Error.Message)
	}
	return nil
}

func (c *MCPConnection) close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}
	c.closed = true
	c.connected = false

	if c.sseCancel != nil {
		c.sseCancel()
	}

	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.stderr != nil {
		c.stderr.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		c.cmd.Process.Kill()
		c.cmd.Wait()
	}
}

// defaultConnect establishes a connection based on transport type.
func defaultConnect(ctx context.Context, conn *MCPConnection) error {
	switch conn.config.Transport {
	case TransportSSE:
		return connectSSE(ctx, conn)
	case TransportStdio:
		return connectStdio(ctx, conn)
	default:
		return fmt.Errorf("unsupported transport type: %s", conn.config.Transport)
	}
}

// connectSSE validates the SSE endpoint is reachable.
func connectSSE(ctx context.Context, conn *MCPConnection) error {
	if conn.config.URL == "" {
		return fmt.Errorf("URL is required for SSE transport")
	}

	// Verify the server is reachable with a simple request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, conn.config.URL, nil)
	if err != nil {
		return fmt.Errorf("create health check request: %w", err)
	}

	resp, err := conn.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("server unreachable: %w", err)
	}
	resp.Body.Close()

	return nil
}

// connectStdio spawns a subprocess and sets up stdin/stdout communication.
func connectStdio(ctx context.Context, conn *MCPConnection) error {
	if conn.config.Command == "" {
		return fmt.Errorf("command is required for stdio transport")
	}

	cmd := exec.CommandContext(ctx, conn.config.Command, conn.config.Args...)

	// Set environment variables
	if len(conn.config.EnvVars) > 0 {
		for k, v := range conn.config.EnvVars {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return fmt.Errorf("create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		return fmt.Errorf("create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start command: %w", err)
	}

	conn.cmd = cmd
	conn.stdin = stdin
	conn.stdout = bufio.NewReader(stdout)
	conn.stderr = stderr

	return nil
}
