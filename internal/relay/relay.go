// Package relay implements the browser-owned Chronicle protocol and shared MCP daemon.
package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Version = "0.2.1"
const Protocol = 3
const bodyLimit = 128 << 10

// Bound registry memory and worst-case escaped inspection JSON together.
const MaxConnections = 64
const MaxControlResponseBytes = 8 << 20
const browserLease = 10 * time.Second
const maxRevision = 9007199254740991

type Config struct {
	Port                    int
	Origin                  string
	IdleTimeout, AgentLease time.Duration
}
type Page struct {
	URL        string `json:"url"`
	Generation string `json:"generation"`
	SessionID  string `json:"sessionId"`
}
type Tool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	Capability   map[string]any `json:"capability"`
	Effect       string         `json:"effect"`
	Confirmation string         `json:"confirmation"`
}
type ClientIdentity struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}
type PageInspection struct {
	URL string `json:"url"`
}
type ConnectionInspection struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Page              *PageInspection `json:"page"`
	Owner             *ClientIdentity `json:"owner"`
	OwnershipRevision uint64          `json:"ownershipRevision"`
	Availability      string          `json:"availability"`
	Pending           bool            `json:"pending"`
}
type IdleInspection struct {
	Disabled    bool       `json:"disabled"`
	Deadline    *time.Time `json:"deadline"`
	Remaining   string     `json:"remaining"`
	PendingCall bool       `json:"pendingCall"`
}
type Inspection struct {
	Connections       []ConnectionInspection `json:"connections"`
	LastAgentActivity time.Time              `json:"lastAgentActivity"`
	Idle              IdleInspection         `json:"idle"`
}
type connection struct {
	id, token, origin, name string
	page                    *Page
	tools                   []Tool
	seen                    time.Time
	owner                   string
	revision                uint64
	pending                 *command
}
type connectionMetadata struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Page              *Page           `json:"page"`
	Owner             *ClientIdentity `json:"owner"`
	OwnershipRevision uint64          `json:"ownershipRevision"`
}
type observation struct {
	connection string
	revision   uint64
	page       Page
	tools      []Tool
}
type agent struct {
	session  *mcp.ServerSession
	server   *mcp.Server
	identity ClientIdentity
	seen     time.Time
	selected string
	observed observation
}
type command struct {
	ID                string          `json:"id"`
	Type              string          `json:"type"`
	Generation        string          `json:"generation"`
	ToolName          string          `json:"toolName"`
	Arguments         json.RawMessage `json:"arguments"`
	Capability        map[string]any  `json:"capability"`
	OwnershipRevision uint64          `json:"ownershipRevision"`
	sessionID         string
	done              chan outcome
}
type outcome struct {
	result json.RawMessage
	err    string
}
type Daemon struct {
	Config Config
	// Token is retained only for private state-file ownership/cleanup. It grants no browser access.
	Token, AgentToken string
	mu                sync.Mutex
	connections       map[string]*connection
	agents            map[string]*agent
	now               func() time.Time
	lastActivity      time.Time
	startedAt         time.Time
	mcpHTTP           *mcp.StreamableHTTPHandler
	shutdown          chan struct{}
	stopOnce          sync.Once
	closed            bool
	callTimeout       time.Duration
}

func secret() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func New(c Config) (*Daemon, error) {
	if c.Origin == "" {
		c.Origin = "https://chronicleclassic.com"
	}
	if !exactOrigin(c.Origin) {
		return nil, errors.New("origin must be an exact HTTP(S) origin without wildcard or trailing slash")
	}
	if c.IdleTimeout < 0 || c.AgentLease <= 0 || c.AgentLease > 24*time.Hour {
		return nil, errors.New("idle timeout must be nonnegative; agent lease must be >0 and <=24h")
	}
	d := &Daemon{Config: c, Token: secret(), AgentToken: secret(), connections: map[string]*connection{}, agents: map[string]*agent{}, now: time.Now, lastActivity: time.Now(), startedAt: time.Now(), shutdown: make(chan struct{}), callTimeout: 60 * time.Second}
	// Each MCP session gets its own server for targeted catalog notifications. The
	// relay alone owns inactivity leases, including GET keepalives and pending calls.
	d.mcpHTTP = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return d.newMCPServer() }, &mcp.StreamableHTTPOptions{})
	return d, nil
}
func relayTools() []*mcp.Tool {
	empty := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	selection := map[string]any{"type": "object", "properties": map[string]any{"connection_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}, "required": []string{"connection_id"}, "additionalProperties": false}
	return []*mcp.Tool{
		{Name: "list_connections", Description: "List live browser connections without selecting one. Ask the user to choose if unspecified or ambiguous.", InputSchema: empty},
		{Name: "select_connection", Description: "Explicitly select an available connection by ID. Release an existing selection before switching. Then call whereami and discover page guidance.", InputSchema: selection},
		{Name: "release_connection", Description: "Release your selected connection by ID and cancel its pending work. Does not save or discard browser edits.", InputSchema: selection},
		{Name: "whereami", Description: "Observe the selected browser page and capabilities without acquiring any connection.", InputSchema: empty},
	}
}
func reserved(name string) bool {
	return name == "status" || name == "whereami" || name == "list_connections" || name == "select_connection" || name == "release_connection"
}
func (d *Daemon) newMCPServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "chronicle", Version: Version}, &mcp.ServerOptions{Instructions: "If no browser tabs are connected, ask the user to open https://choose.chronicleclassic.com/modules, open a module editor, and click Connect to local MCP. List connections and ask for an explicit user choice when unspecified. Select by connection ID, then call whereami, discover tools, and fetch gen_page_context and applicable list_skill/read_skill guidance. Neither discovery nor whereami acquires a tab. Refresh guidance after navigation. Browser approval is authoritative; never claim unsaved edits are persisted.", Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}}, SupportedProtocolVersions: []string{"2025-11-25", "2025-06-18", "2025-03-26"}})
	for _, t := range relayTools() {
		s.AddTool(t, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, nil })
	}
	s.AddReceivingMiddleware(d.middleware(s))
	return s
}
func (d *Daemon) notifyLocked(id string) {
	if a := d.agents[id]; a != nil {
		a.server.AddTool(relayTools()[3], func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, nil })
	}
}
func finishLocked(c *connection, o outcome) {
	if c.pending != nil {
		c.pending.done <- o
		c.pending = nil
	}
}
func (d *Daemon) releaseConnectionLocked(c *connection, reason string) {
	if c.owner == "" {
		return
	}
	id := c.owner
	if a := d.agents[id]; a != nil {
		a.selected = ""
		a.observed = observation{}
	}
	c.owner = ""
	if c.revision < maxRevision {
		c.revision++
	}
	finishLocked(c, outcome{err: reason})
	d.notifyLocked(id)
}
func (d *Daemon) removeConnectionLocked(id, reason string) {
	if c := d.connections[id]; c != nil {
		d.releaseConnectionLocked(c, reason)
		finishLocked(c, outcome{err: reason})
		delete(d.connections, id)
	}
}
func (d *Daemon) releaseAgentLocked(id string) {
	if a := d.agents[id]; a != nil {
		if c := d.connections[a.selected]; c != nil && c.owner == id {
			d.releaseConnectionLocked(c, "agent_disconnected")
		}
		delete(d.agents, id)
	}
}
func (d *Daemon) agentPendingLocked(a *agent) bool {
	c := d.connections[a.selected]
	return c != nil && c.pending != nil
}
func (d *Daemon) expireLocked() {
	now := d.now()
	for id, c := range d.connections {
		if now.Sub(c.seen) >= browserLease {
			d.removeConnectionLocked(id, "browser_disconnected")
		}
	}
	for id, a := range d.agents {
		live := false
		for s := range a.server.Sessions() {
			if s == a.session {
				live = true
				break
			}
		}
		if !live || (now.Sub(a.seen) >= d.Config.AgentLease && !d.agentPendingLocked(a)) {
			d.releaseAgentLocked(id)
			if live {
				go a.session.Close()
			}
		}
	}
}
func (d *Daemon) activityLocked(a *agent) { a.seen = d.now(); d.lastActivity = a.seen }
func (d *Daemon) observeLocked(a *agent) observation {
	var o observation
	if c := d.connections[a.selected]; c != nil && c.owner == a.session.ID() && c.page != nil {
		o = observation{c.id, c.revision, *c.page, c.tools}
	}
	a.observed = o
	return o
}
func (d *Daemon) metadataLocked(c *connection) connectionMetadata {
	var owner *ClientIdentity
	if a := d.agents[c.owner]; a != nil {
		copy := a.identity
		owner = &copy
	}
	return connectionMetadata{c.id, c.name, c.page, owner, c.revision}
}
func (d *Daemon) listLocked(agentID string) []ConnectionInspection {
	list := []ConnectionInspection{}
	for _, c := range d.connections {
		if c.page == nil {
			continue
		}
		meta := d.metadataLocked(c)
		availability := "available"
		if c.owner != "" {
			availability = "busy"
			if c.owner == agentID {
				availability = "owned_by_you"
			}
		}
		list = append(list, ConnectionInspection{ID: c.id, Name: c.name, Page: &PageInspection{URL: c.page.URL}, Owner: meta.Owner, OwnershipRevision: c.revision, Availability: availability, Pending: c.pending != nil})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}
func textResult(v any, isError bool) *mcp.CallToolResult {
	b, _ := json.Marshal(v)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}, IsError: isError}
}
func failure(s string) *mcp.CallToolResult {
	if s == "stale_page" {
		return textResult(map[string]any{"status": "stale_state", "result": map[string]any{"message": "Page changed"}}, true)
	}
	return textResult(map[string]any{"status": "client_error", "result": map[string]any{"message": s}}, true)
}
func controlFailure(code, message string) *mcp.CallToolResult {
	return textResult(map[string]any{"status": "client_error", "error": code, "result": map[string]any{"message": message}}, true)
}
func identityText(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		s = fallback
	}
	r := []rune(s)
	if len(r) > 128 {
		r = r[:128]
	}
	return string(r)
}
func (d *Daemon) middleware(server *mcp.Server) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			id := req.GetSession().ID()
			d.mu.Lock()
			d.expireLocked()
			if d.closed {
				d.mu.Unlock()
				return nil, errors.New("relay stopped")
			}
			a := d.agents[id]
			if a == nil {
				if method != "initialize" {
					d.mu.Unlock()
					return nil, errors.New("agent session expired; reconnect")
				}
				a = &agent{server: server, session: req.GetSession().(*mcp.ServerSession), identity: ClientIdentity{ID: secret(), Name: "Unknown agent", Version: "unknown"}}
				d.agents[id] = a
			}
			d.activityLocked(a)
			if init := a.session.InitializeParams(); init != nil && init.ClientInfo != nil {
				a.identity.Name = identityText(init.ClientInfo.Name, "Unknown agent")
				a.identity.Version = identityText(init.ClientInfo.Version, "unknown")
			}
			switch method {
			case "tools/list":
				o := d.observeLocked(a)
				tools := relayTools()
				for _, t := range o.tools {
					tools = append(tools, &mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
				}
				d.mu.Unlock()
				return &mcp.ListToolsResult{Tools: tools}, nil
			case "tools/call":
				p, ok := req.GetParams().(*mcp.CallToolParamsRaw)
				if !ok {
					d.mu.Unlock()
					return nil, errors.New("invalid call parameters")
				}
				if reserved(p.Name) && p.Name != "status" {
					result := d.controlLocked(a, p)
					d.mu.Unlock()
					return result, nil
				}
				o := a.observed
				d.mu.Unlock()
				return d.call(ctx, id, p.Name, p.Arguments, o), nil
			default:
				d.mu.Unlock()
				return next(ctx, method, req)
			}
		}
	}
}
func (d *Daemon) controlLocked(a *agent, p *mcp.CallToolParamsRaw) *mcp.CallToolResult {
	raw := p.Arguments
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(raw, &args) != nil || args == nil {
		return controlFailure("invalid_arguments", "Expected arguments object")
	}
	if p.Name == "whereami" || p.Name == "list_connections" {
		if len(args) != 0 {
			return controlFailure("invalid_arguments", "Expected empty arguments")
		}
	}
	switch p.Name {
	case "list_connections":
		return textResult(map[string]any{"connections": d.listLocked(a.session.ID())}, false)
	case "whereami":
		o := d.observeLocked(a)
		if o.connection == "" {
			return textResult(map[string]any{"selectedConnection": nil, "page": nil, "connected": false, "instructions": "Call list_connections, ask the user to choose, then select_connection by ID. No connection is selected automatically."}, false)
		}
		c := d.connections[o.connection]
		return textResult(map[string]any{"selectedConnection": d.metadataLocked(c), "page": c.page, "connected": true}, false)
	}
	var id string
	if len(args) != 1 || json.Unmarshal(args["connection_id"], &id) != nil || !bounded(id, 128) {
		return controlFailure("invalid_arguments", "Expected connection_id")
	}
	if p.Name == "release_connection" {
		if a.selected != id {
			return controlFailure("not_selected", "The requested connection is not your current selection")
		}
		c := d.connections[id]
		if c == nil || c.owner != a.session.ID() {
			return controlFailure("not_selected", "Connection is no longer selected")
		}
		d.releaseConnectionLocked(c, "connection_released")
		return textResult(map[string]any{"ok": true, "connection": d.metadataLocked(c)}, false)
	}
	if a.selected != "" && a.selected != id {
		return controlFailure("already_selected", "Release the current selection before selecting another connection")
	}
	c := d.connections[id]
	if c == nil || c.page == nil {
		return controlFailure("connection_not_found", "Connection is absent or expired")
	}
	if c.owner != "" && c.owner != a.session.ID() {
		return controlFailure("connection_busy", "Another agent owns this connection")
	}
	if c.owner == a.session.ID() {
		return textResult(map[string]any{"ok": true, "connection": d.metadataLocked(c)}, false)
	}
	if c.revision >= maxRevision-1 {
		return controlFailure("revision_exhausted", "Reconnect this browser with a new connection ID")
	}
	c.owner = a.session.ID()
	c.revision++
	a.selected = id
	a.observed = observation{}
	d.notifyLocked(a.session.ID())
	return textResult(map[string]any{"ok": true, "connection": d.metadataLocked(c)}, false)
}
func (d *Daemon) call(ctx context.Context, owner, name string, args json.RawMessage, o observation) *mcp.CallToolResult {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var object map[string]any
	if len(args) > bodyLimit || json.Unmarshal(args, &object) != nil || object == nil {
		return failure("Expected arguments object")
	}
	d.mu.Lock()
	d.expireLocked()
	reject := func(s string) *mcp.CallToolResult { d.mu.Unlock(); return failure(s) }
	if d.closed {
		return reject("relay stopped")
	}
	a := d.agents[owner]
	if a == nil {
		return reject("agent_disconnected")
	}
	c := d.connections[a.selected]
	if c == nil || c.owner != owner || c.page == nil {
		return reject("No selected connection; call list_connections and select_connection explicitly")
	}
	if c.pending != nil {
		return reject("A tool call is already pending on this connection")
	}
	var tool *Tool
	for i := range c.tools {
		if c.tools[i].Name == name {
			tool = &c.tools[i]
			break
		}
	}
	if tool == nil {
		return reject("Unknown browser tool")
	}
	var observed *Tool
	for i := range o.tools {
		if o.tools[i].Name == name {
			observed = &o.tools[i]
			break
		}
	}
	if observed == nil || o.connection != c.id || o.revision != c.revision || o.page.Generation != c.page.Generation || o.page.SessionID != c.page.SessionID || !reflect.DeepEqual(observed.Capability, tool.Capability) {
		return reject("stale_page")
	}
	cmd := &command{ID: secret(), Type: "tool", Generation: c.page.Generation, ToolName: name, Arguments: args, Capability: tool.Capability, OwnershipRevision: c.revision, sessionID: c.page.SessionID, done: make(chan outcome, 1)}
	c.pending = cmd
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		if c.pending == cmd {
			c.pending = nil
		}
		d.mu.Unlock()
	}()
	timer := time.NewTimer(d.callTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return failure("Tool call canceled")
	case <-timer.C:
		return failure("Tool call timed out waiting for browser result")
	case o := <-cmd.done:
		if o.err != "" {
			return failure(o.err)
		}
		var continuation struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(o.result, &continuation)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(o.result)}}, IsError: continuation.Status != "success"}
	}
}

func (d *Daemon) Handler() http.Handler { return http.HandlerFunc(d.serveHTTP) }

// Non-MCP responses are snapshots: never perform socket writes with mu held.
// MCP owns its streaming response and must not inherit this write deadline.
func (d *Daemon) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/mcp" {
		d.handle(w, r)
		return
	}
	response := &bufferedResponse{header: make(http.Header), status: http.StatusOK}
	d.handle(response, r)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second))
	if response.overflow {
		fail(w, http.StatusInternalServerError, "Response exceeds control response bound")
		return
	}
	for key, values := range response.header {
		w.Header()[key] = values
	}
	w.WriteHeader(response.status)
	_, _ = w.Write(response.body.Bytes())
}

type bufferedResponse struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func (w *bufferedResponse) Header() http.Header    { return w.header }
func (w *bufferedResponse) WriteHeader(status int) { w.status = status }
func (w *bufferedResponse) Write(b []byte) (int, error) {
	if w.overflow || w.body.Len()+len(b) > MaxControlResponseBytes {
		w.overflow = true
		return 0, errors.New("response exceeds bound")
	}
	return w.body.Write(b)
}
func (d *Daemon) handle(w http.ResponseWriter, r *http.Request) {
	if dashboardPath(r.URL.Path) {
		d.dashboard(w, r)
		return
	}
	if !d.localHost(r.Host) {
		fail(w, 403, "Invalid Host")
		return
	}
	origin := r.Header.Get("Origin")
	_, hasOrigin := r.Header["Origin"]
	if hasOrigin && !d.allowed(origin) {
		fail(w, 403, "Origin not allowed")
		return
	}
	if hasOrigin {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	if r.Method == "OPTIONS" {
		if !hasOrigin {
			fail(w, 403, "Preflight requires approved Origin")
			return
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Chronicle-Client")
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
		w.WriteHeader(204)
		return
	}
	// Discovery is credential-free but retains the exact Host and approved-Origin
	// guards above. It neither allocates a connection nor renews agent activity.
	if r.URL.Path == "/healthz" {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET, OPTIONS")
			fail(w, http.StatusMethodNotAllowed, "Health probe requires GET")
			return
		}
		d.mu.Lock()
		closed := d.closed
		d.mu.Unlock()
		if closed {
			fail(w, http.StatusServiceUnavailable, "Relay stopped")
			return
		}
		respond(w, http.StatusOK, map[string]any{"service": "chronicle", "version": Version, "protocol": Protocol})
		return
	}
	control := r.URL.Path == "/mcp" || r.URL.Path == "/health" || r.URL.Path == "/shutdown"
	bootstrap := r.Method == "POST" && r.URL.Path == "/v1/connect"
	if control {
		if hasOrigin {
			fail(w, 403, "Agent endpoints do not accept browser origins")
			return
		}
		if !authenticated(r, d.AgentToken) {
			fail(w, 401, "Agent bearer token required")
			return
		}
	}
	if r.URL.Path == "/mcp" {
		id := r.Header.Get("Mcp-Session-Id")
		d.mu.Lock()
		d.expireLocked()
		if d.closed {
			d.mu.Unlock()
			fail(w, 503, "Relay stopped")
			return
		}
		if id != "" {
			a := d.agents[id]
			if a == nil {
				d.mu.Unlock()
				fail(w, 404, "Agent session expired; reconnect")
				return
			}
			d.activityLocked(a)
			// SDK DELETE waits for in-flight handlers. Cancel browser work
			// before entering it so shutdown cannot wait for its own call.
			if r.Method == http.MethodDelete {
				d.releaseAgentLocked(id)
			}
		}
		d.mu.Unlock()
		r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
		d.mcpHTTP.ServeHTTP(w, r)
		if r.Method == http.MethodDelete {
			d.mu.Lock()
			d.releaseAgentLocked(id)
			d.mu.Unlock()
		}
		return
	}
	// Buffer outside the mutex, with transport deadline and size cap.
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, bodyLimit))
		if err != nil {
			var large *http.MaxBytesError
			if errors.As(err, &large) {
				fail(w, 413, "Request body must not exceed 128 KiB")
			} else {
				fail(w, 400, "Unable to read body")
			}
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expireLocked()
	if d.closed {
		fail(w, 503, "Relay stopped")
		return
	}
	if bootstrap {
		if !hasOrigin {
			fail(w, 403, "Connect requires approved Origin")
			return
		}
		var data struct {
			Protocol int    `json:"protocol"`
			ClientID string `json:"clientId"`
		}
		if !decode(w, r, &data) {
			return
		}
		if data.Protocol != Protocol {
			respond(w, 400, map[string]any{"error": "upgrade_required", "message": "Chronicle requires browser protocol 3. Upgrade browser and daemon together; protocol 2 is unsupported.", "protocol": Protocol})
			return
		}
		if !bounded(data.ClientID, 128) {
			fail(w, 400, "Expected nonempty clientId at most 128 bytes")
			return
		}
		if d.connections[data.ClientID] != nil {
			fail(w, 409, "Connection ID already allocated; reconnect with a fresh ID")
			return
		}
		if len(d.connections) >= MaxConnections {
			fail(w, http.StatusServiceUnavailable, "Connection capacity reached (64); disconnect a tab or wait for unused bootstrap leases to expire")
			return
		}
		c := &connection{id: data.ClientID, token: secret(), origin: origin, seen: d.now()}
		d.connections[c.id] = c
		respond(w, 200, map[string]any{"protocol": Protocol, "clientId": c.id, "token": c.token})
		return
	}
	if control {
		switch r.Method + " " + r.URL.Path {
		case "GET /health":
			respond(w, 200, struct {
				Inspection
				Service     string `json:"service"`
				Version     string `json:"version"`
				Protocol    int    `json:"protocol"`
				Agents      int    `json:"agents"`
				Pending     int    `json:"pending"`
				Origin      string `json:"origin"`
				IdleTimeout string `json:"idleTimeout"`
				AgentLease  string `json:"agentLease"`
			}{d.inspectionLocked(), "chronicle", Version, Protocol, len(d.agents), d.pendingLocked(), d.Config.Origin, d.Config.IdleTimeout.String(), d.Config.AgentLease.String()})
		case "POST /shutdown":
			var v struct {
				Force bool `json:"force"`
			}
			if !decode(w, r, &v) {
				return
			}
			if !v.Force && (len(d.agents) > 0 || d.pendingLocked() > 0) {
				fail(w, 409, "Active agent sessions; use stop --force to disconnect them")
				return
			}
			respond(w, 200, map[string]any{"ok": true})
			d.stopOnce.Do(func() { close(d.shutdown) })
		default:
			fail(w, 404, "Not found")
		}
		return
	}
	id := r.Header.Get("X-Chronicle-Client")
	c := d.connections[id]
	if c == nil || !authenticated(r, c.token) {
		fail(w, 401, "Connection bearer token and X-Chronicle-Client required; reconnect explicitly if expired")
		return
	}
	if !hasOrigin || origin != c.origin {
		fail(w, 403, "Origin must match this connection's bootstrap origin")
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "POST /v1/browser":
		d.heartbeatLocked(w, r, c)
	case "GET /v1/state":
		if c.page == nil {
			fail(w, 409, "Heartbeat required before polling")
			return
		}
		commands := []*command{}
		if c.pending != nil {
			commands = append(commands, c.pending)
		}
		respond(w, 200, map[string]any{"protocol": Protocol, "connection": d.metadataLocked(c), "commands": commands})
	case "GET /v1/status":
		respond(w, 200, map[string]any{"protocol": Protocol, "connection": d.metadataLocked(c), "connected": c.page != nil, "pending": boolInt(c.pending != nil)})
	case "POST /v1/browser/revoke":
		var data struct {
			Expected *uint64 `json:"expectedOwnershipRevision"`
			ClientID *string `json:"clientId"`
		}
		if !decode(w, r, &data) {
			return
		}
		if data.ClientID != nil && *data.ClientID != c.id {
			fail(w, 403, "clientId must match credential and header")
			return
		}
		if data.Expected == nil || *data.Expected > maxRevision {
			fail(w, 400, "Expected JavaScript-safe expectedOwnershipRevision")
			return
		}
		if *data.Expected != c.revision {
			respond(w, 409, map[string]any{"error": "stale_ownership", "connection": d.metadataLocked(c)})
			return
		}
		d.releaseConnectionLocked(c, "ownership_revoked")
		respond(w, 200, map[string]any{"ok": true, "connection": d.metadataLocked(c)})
	case "POST /v1/browser/disconnect":
		var data map[string]any
		if !decode(w, r, &data) {
			return
		}
		if data == nil {
			fail(w, 400, "Expected object")
			return
		}
		if id, present := data["clientId"]; present && id != c.id {
			fail(w, 403, "clientId must match credential and header")
			return
		}
		d.removeConnectionLocked(c.id, "browser_disconnected")
		respond(w, 200, map[string]any{"ok": true})
	default:
		fail(w, 404, "Not found")
	}
}
func (d *Daemon) heartbeatLocked(w http.ResponseWriter, r *http.Request, c *connection) {
	var data struct {
		ClientID string `json:"clientId"`
		Name     string `json:"name"`
		Page     Page   `json:"page"`
		Tools    []Tool `json:"tools"`
		Result   *struct {
			ID                string          `json:"id"`
			Generation        string          `json:"generation"`
			OwnershipRevision *uint64         `json:"ownershipRevision"`
			Result            json.RawMessage `json:"result"`
		} `json:"result"`
	}
	if !decode(w, r, &data) {
		return
	}
	if data.ClientID != c.id {
		fail(w, 403, "clientId must match credential and header")
		return
	}
	name := strings.TrimSpace(data.Name)
	if name == "" || utf8.RuneCountInString(name) > 80 || !bounded(data.Page.URL, 8192) || !bounded(data.Page.Generation, 256) || !bounded(data.Page.SessionID, 256) || !validTools(data.Tools) {
		fail(w, 400, "Invalid browser heartbeat, name, or tool descriptor")
		return
	}
	u, e := url.Parse(data.Page.URL)
	if e != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Scheme+"://"+u.Host != c.origin {
		fail(w, 400, "Page URL must match bootstrap origin")
		return
	}
	u.Fragment = ""
	data.Page.URL = u.String()
	if data.Result != nil {
		if !bounded(data.Result.ID, 128) || !bounded(data.Result.Generation, 256) || data.Result.OwnershipRevision == nil || *data.Result.OwnershipRevision > maxRevision || len(data.Result.Result) == 0 {
			fail(w, 400, "Invalid browser result or ownershipRevision")
			return
		}
		if len(data.Result.Result) > 64<<10 {
			fail(w, 413, "Browser result exceeds 64 KiB")
			return
		}
	}
	navigated := c.page != nil && (c.page.Generation != data.Page.Generation || c.page.SessionID != data.Page.SessionID)
	changed := c.page == nil || navigated || !reflect.DeepEqual(c.tools, data.Tools)
	if navigated {
		finishLocked(c, outcome{err: "stale_page"})
	}
	c.name = name
	c.page = &data.Page
	c.tools = data.Tools
	c.seen = d.now()
	if changed {
		d.notifyLocked(c.owner)
	}
	response := map[string]any{"ok": true, "connection": d.metadataLocked(c)}
	if result := data.Result; result != nil {
		p := c.pending
		accepted := p != nil && p.ID == result.ID && p.Generation == result.Generation && p.Generation == c.page.Generation && p.sessionID == c.page.SessionID && p.OwnershipRevision == *result.OwnershipRevision && p.OwnershipRevision == c.revision
		response["resultAccepted"] = accepted
		if accepted {
			finishLocked(c, outcome{result: result.Result})
		}
	}
	respond(w, 200, response)
}
func (d *Daemon) pendingLocked() int {
	n := 0
	for _, c := range d.connections {
		if c.pending != nil {
			n++
		}
	}
	return n
}
func (d *Daemon) idleLocked() bool {
	return d.pendingLocked() == 0 && d.Config.IdleTimeout > 0 && d.now().Sub(d.lastActivity) >= d.Config.IdleTimeout
}
func (d *Daemon) inspectionLocked() Inspection {
	i := Inspection{Connections: d.listLocked(""), LastAgentActivity: d.lastActivity, Idle: IdleInspection{Disabled: d.Config.IdleTimeout == 0, PendingCall: d.pendingLocked() > 0}}
	if d.Config.IdleTimeout > 0 {
		deadline := d.lastActivity.Add(d.Config.IdleTimeout)
		i.Idle.Deadline = &deadline
		remaining := deadline.Sub(d.now())
		if remaining < 0 {
			remaining = 0
		}
		i.Idle.Remaining = remaining.String()
	}
	return i
}
func (d *Daemon) closeSessions() {
	d.mu.Lock()
	sessions := []*mcp.ServerSession{}
	for _, a := range d.agents {
		sessions = append(sessions, a.session)
	}
	d.mu.Unlock()
	for _, s := range sessions {
		_ = s.Close()
	}
}

// Serve owns the loopback listener. Shutdown never executes save/discard browser operations.
func (d *Daemon) Serve(ctx context.Context, l net.Listener) error {
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.Equal(net.ParseIP("127.0.0.1")) {
		return errors.New("listener must bind IPv4 loopback")
	}
	d.Config.Port = addr.Port
	srv := &http.Server{Handler: d.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var result error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-d.shutdown:
			break loop
		case result = <-done:
			break loop
		case <-tick.C:
			d.mu.Lock()
			d.expireLocked()
			idle := d.idleLocked()
			d.mu.Unlock()
			if idle {
				break loop
			}
		}
	}
	d.mu.Lock()
	d.closed = true
	for _, c := range d.connections {
		d.releaseConnectionLocked(c, "relay stopped")
		finishLocked(c, outcome{err: "relay stopped"})
	}
	d.mu.Unlock()
	d.closeSessions()
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	_ = srv.Close()
	if errors.Is(result, http.ErrServerClosed) {
		return nil
	}
	return result
}
