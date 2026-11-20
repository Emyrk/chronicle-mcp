// Package relay implements the browser-owned Chronicle protocol and shared MCP daemon.
package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Version = "0.1.0"
const bodyLimit = 128 << 10

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
type browser struct {
	ClientID string `json:"clientId"`
	Page     Page   `json:"page"`
	Tools    []Tool `json:"tools"`
	seen     time.Time
}
type observation struct {
	page  Page
	tools []Tool
}
type agent struct {
	seen     time.Time
	observed observation
}
type command struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Generation       string          `json:"generation"`
	ToolName         string          `json:"toolName"`
	Arguments        json.RawMessage `json:"arguments"`
	Capability       map[string]any  `json:"capability"`
	sessionID, owner string
	done             chan outcome
}
type outcome struct {
	result json.RawMessage
	err    string
}

type Daemon struct {
	Config            Config
	Token, AgentToken string
	mu                sync.Mutex
	browser           *browser
	agents            map[string]*agent
	owner             string
	pending           *command
	lastActivity      time.Time
	now               func() time.Time
	server            *mcp.Server
	mcpHTTP           *mcp.StreamableHTTPHandler
	shutdown          chan struct{}
	stopOnce          sync.Once
	closed            bool
}

func secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
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
	d := &Daemon{Config: c, Token: secret(), AgentToken: secret(), agents: map[string]*agent{}, now: time.Now, lastActivity: time.Now(), shutdown: make(chan struct{})}
	d.server = mcp.NewServer(&mcp.Implementation{Name: "chronicle", Version: Version}, &mcp.ServerOptions{Instructions: "Call whereami first. Discover the current tools, then gen_page_context, list_skill and read_skill when advertised. Refresh guidance after navigation. Browser edits are unsaved until browser-approved save; never bypass approval.", Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}}, SupportedProtocolVersions: []string{"2025-11-25", "2025-06-18", "2025-03-26"}})
	// Catalog updates trigger SDK notifications, while middleware preserves browser schemas verbatim.
	d.server.AddTool(&mcp.Tool{Name: "whereami", Description: "Observe and claim the connected Chronicle browser for this agent session", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, nil })
	d.server.AddReceivingMiddleware(d.middleware)
	d.mcpHTTP = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return d.server }, &mcp.StreamableHTTPOptions{SessionTimeout: c.AgentLease})
	return d, nil
}
func exactOrigin(s string) bool {
	u, e := url.Parse(s)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && s == u.Scheme+"://"+u.Host && !regexp.MustCompile(`\*`).MatchString(s)
}
func (d *Daemon) allowed(s string) bool {
	return s == "https://chronicleclassic.com" || s == "http://localhost:4000" || s == d.Config.Origin
}
func (d *Daemon) finishLocked(o outcome) {
	if d.pending != nil {
		d.pending.done <- o
		d.pending = nil
	}
}
func (d *Daemon) changedLocked() { // AddTool replaces only our local sentinel; never compiles browser schemas.
	d.server.AddTool(&mcp.Tool{Name: "whereami", Description: "Observe and claim the connected Chronicle browser for this agent session", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, nil })
}
func (d *Daemon) expireLocked() {
	now := d.now()
	for id, a := range d.agents {
		if now.Sub(a.seen) >= d.Config.AgentLease && (d.pending == nil || d.pending.owner != id) {
			d.releaseLocked(id)
		}
	}
	if d.browser != nil && now.Sub(d.browser.seen) >= 10*time.Second {
		d.browser = nil
		d.owner = ""
		d.finishLocked(outcome{err: "browser_disconnected"})
		d.changedLocked()
	}
}
func (d *Daemon) releaseLocked(id string) {
	delete(d.agents, id)
	if d.owner == id {
		d.owner = ""
		d.finishLocked(outcome{err: "agent_disconnected"})
		d.changedLocked()
	}
}
func (d *Daemon) activityLocked(id string) {
	a := d.agents[id]
	if a == nil {
		a = &agent{}
		d.agents[id] = a
	}
	a.seen = d.now()
	d.lastActivity = a.seen
}
func (d *Daemon) observeLocked(id string, claim bool) observation {
	var o observation
	if d.browser != nil {
		o = observation{d.browser.Page, d.browser.Tools}
		if claim && d.owner == "" {
			d.owner = id
		}
	}
	d.agents[id].observed = o
	return o
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
func (d *Daemon) middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		id := req.GetSession().ID()
		d.mu.Lock()
		d.expireLocked()
		if d.closed {
			d.mu.Unlock()
			return nil, errors.New("relay stopped")
		}
		d.activityLocked(id)
		switch method {
		case "tools/list":
			o := d.observeLocked(id, false)
			tools := []*mcp.Tool{{Name: "whereami", Description: "Observe and claim the connected Chronicle browser for this agent session", InputSchema: map[string]any{"type": "object"}}}
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
			if p.Name == "whereami" {
				o := d.observeLocked(id, true)
				var page any
				if o.page.Generation != "" {
					page = o.page
				}
				v := map[string]any{"page": page, "connected": d.browser != nil, "ownership": map[string]any{"owned": d.owner == id, "busy": d.owner != "" && d.owner != id}}
				d.mu.Unlock()
				return textResult(v, false), nil
			}
			o := d.agents[id].observed
			d.mu.Unlock()
			return d.call(ctx, id, p.Name, p.Arguments, o), nil
		default:
			d.mu.Unlock()
			return next(ctx, method, req)
		}
	}
}
func (d *Daemon) call(ctx context.Context, owner, name string, args json.RawMessage, o observation) *mcp.CallToolResult {
	var object map[string]any
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if len(args) > bodyLimit || json.Unmarshal(args, &object) != nil || object == nil {
		return failure("Expected arguments object")
	}
	d.mu.Lock()
	d.expireLocked()
	reject := func(s string) *mcp.CallToolResult { d.mu.Unlock(); return failure(s) }
	if d.closed {
		return reject("relay stopped")
	}
	if d.browser == nil {
		return reject("No browser connected")
	}
	if d.owner == "" {
		return reject("Call whereami to claim the connected browser before calling browser tools")
	}
	if d.owner != owner {
		return reject("Another agent session owns the browser; retry after it disconnects or its lease expires")
	}
	if d.pending != nil {
		return reject("A tool call is already pending")
	}
	var tool *Tool
	for i := range d.browser.Tools {
		if d.browser.Tools[i].Name == name {
			tool = &d.browser.Tools[i]
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
	if observed == nil || o.page.Generation != d.browser.Page.Generation || o.page.SessionID != d.browser.Page.SessionID || !reflect.DeepEqual(observed.Capability, tool.Capability) {
		return reject("stale_page")
	}
	c := &command{ID: secret(), Type: "tool", Generation: d.browser.Page.Generation, ToolName: name, Arguments: args, Capability: tool.Capability, sessionID: d.browser.Page.SessionID, owner: owner, done: make(chan outcome, 1)}
	d.pending = c
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		if d.pending == c {
			d.pending = nil
		}
		d.mu.Unlock()
	}()
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return failure("Tool call canceled")
	case <-timer.C:
		return failure("Tool call timed out waiting for browser result")
	case o := <-c.done:
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

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, s string) {
	respond(w, status, map[string]any{"error": s})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			fail(w, 413, "Request body must not exceed 128 KiB")
		} else {
			fail(w, 400, "Invalid JSON")
		}
		return false
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		fail(w, 400, "Expected one JSON value")
		return false
	}
	return true
}
func authenticated(r *http.Request, token string) bool {
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) == 1
}
func (d *Daemon) Handler() http.Handler { return http.HandlerFunc(d.handle) }
func (d *Daemon) handle(w http.ResponseWriter, r *http.Request) {
	if r.Host != fmt.Sprintf("127.0.0.1:%d", d.Config.Port) {
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
	if r.Method == "POST" && r.URL.Path == "/v1/connect" {
		if !hasOrigin {
			fail(w, 403, "Connect requires approved Origin")
			return
		}
		var v struct {
			Protocol int `json:"protocol"`
		}
		if !decode(w, r, &v) {
			return
		}
		if v.Protocol != 2 {
			fail(w, 400, "Expected protocol 2")
			return
		}
		respond(w, 200, map[string]any{"protocol": 2, "token": d.Token})
		return
	}
	control := r.URL.Path == "/mcp" || r.URL.Path == "/health" || r.URL.Path == "/shutdown"
	token := d.Token
	if control {
		token = d.AgentToken
		if hasOrigin {
			fail(w, 403, "Agent endpoints do not accept browser origins")
			return
		}
	}
	if !authenticated(r, token) {
		fail(w, 401, "Bearer token required")
		return
	}
	if r.URL.Path == "/mcp" {
		id := r.Header.Get("Mcp-Session-Id")
		d.mu.Lock()
		d.expireLocked()
		if _, ok := d.agents[id]; ok {
			// An expired SDK session ID is not agent activity, even if its
			// relay lease has not yet been swept. Invalid retries cannot keep
			// ownership or the daemon alive indefinitely.
			live := false
			for session := range d.server.Sessions() {
				if session.ID() == id {
					live = true
					break
				}
			}
			if live {
				d.activityLocked(id)
			} else {
				d.releaseLocked(id)
			}
		}
		d.mu.Unlock()
		r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
		d.mcpHTTP.ServeHTTP(w, r)
		if r.Method == http.MethodDelete {
			d.mu.Lock()
			d.releaseLocked(id)
			d.mu.Unlock()
		}
		return
	}
	// Read bodies before taking the relay lock. A slow browser must not stall
	// other agents, cancellation, or lease housekeeping.
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
	switch r.Method + " " + r.URL.Path {
	case "GET /health":
		respond(w, 200, map[string]any{"service": "chronicle", "version": Version, "protocol": 2, "agents": len(d.agents), "pending": boolInt(d.pending != nil), "origin": d.Config.Origin, "idleTimeout": d.Config.IdleTimeout.String(), "agentLease": d.Config.AgentLease.String()})
	case "POST /shutdown":
		var v struct {
			Force bool `json:"force"`
		}
		if !decode(w, r, &v) {
			return
		}
		if !v.Force && (len(d.agents) > 0 || d.pending != nil) {
			fail(w, 409, "Active agent sessions; use stop --force to disconnect them")
			return
		}
		respond(w, 200, map[string]any{"ok": true})
		d.stopOnce.Do(func() { close(d.shutdown) })
	case "POST /v1/browser":
		d.heartbeat(w, r)
	case "GET /v1/status":
		var page any
		tools := []Tool{}
		if d.browser != nil {
			page = d.browser.Page
			tools = d.browser.Tools
		}
		respond(w, 200, map[string]any{"protocol": 2, "page": page, "tools": tools, "connected": d.browser != nil, "pending": boolInt(d.pending != nil)})
	case "GET /v1/state":
		if hasOrigin && (d.browser == nil || r.Header.Get("X-Chronicle-Client") != d.browser.ClientID) {
			fail(w, 409, "Heartbeat required from lease owner before state")
			return
		}
		commands := []*command{}
		if d.pending != nil {
			commands = append(commands, d.pending)
		}
		respond(w, 200, map[string]any{"protocol": 2, "commands": commands})
	default:
		fail(w, 404, "Not found")
	}
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

func bounded(s string, n int) bool { return len(s) > 0 && len(s) <= n }
func integer(v any, min float64) bool {
	f, ok := v.(float64)
	return ok && f >= min && f <= 9007199254740991 && f == float64(int64(f))
}
func validTools(tools []Tool) bool {
	if tools == nil || len(tools) > 64 {
		return false
	}
	names := map[string]bool{}
	for _, t := range tools {
		if !bounded(t.Name, 128) || !namePattern.MatchString(t.Name) || t.Name == "whereami" || t.Name == "status" || names[t.Name] || len(t.Description) > 8192 || t.InputSchema["type"] != "object" || (t.Effect != "read" && t.Effect != "view_mutation") || (t.Confirmation != "never" && t.Confirmation != "unless_yolo") {
			return false
		}
		names[t.Name] = true
		id, ok := t.Capability["id"].(string)
		if !ok || !bounded(id, 256) || !integer(t.Capability["version"], 1) {
			return false
		}
		for _, k := range []string{"scopeId", "scopeType"} {
			if v, ok := t.Capability[k]; ok {
				s, ok := v.(string)
				if !ok || !bounded(s, 1024) {
					return false
				}
			}
		}
		if v, ok := t.Capability["stateRevision"]; ok && !integer(v, 0) {
			return false
		}
	}
	return true
}
func (d *Daemon) heartbeat(w http.ResponseWriter, r *http.Request) {
	var data struct {
		browser
		Result *struct {
			ID         string          `json:"id"`
			Generation string          `json:"generation"`
			Result     json.RawMessage `json:"result"`
		} `json:"result"`
	}
	if !decode(w, r, &data) {
		return
	}
	if !bounded(data.ClientID, 128) || !bounded(data.Page.URL, 8192) || !bounded(data.Page.Generation, 256) || !bounded(data.Page.SessionID, 256) || !validTools(data.Tools) {
		fail(w, 400, "Invalid browser heartbeat or tool descriptor")
		return
	}
	u, err := url.Parse(data.Page.URL)
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || !d.allowed(u.Scheme+"://"+u.Host) || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != u.Scheme+"://"+u.Host) {
		fail(w, 400, "Page URL origin not allowed")
		return
	}
	u.Fragment = ""
	data.Page.URL = u.String()
	if data.Result != nil {
		if !bounded(data.Result.ID, 128) || !bounded(data.Result.Generation, 256) || len(data.Result.Result) == 0 {
			fail(w, 400, "Invalid browser result")
			return
		}
		if len(data.Result.Result) > 64<<10 {
			fail(w, 413, "Browser result exceeds 64 KiB")
			return
		}
	}
	if d.browser != nil && d.browser.ClientID != data.ClientID {
		fail(w, 409, "Another browser client owns the active lease")
		return
	}
	navigated := d.browser != nil && (d.browser.Page.Generation != data.Page.Generation || d.browser.Page.SessionID != data.Page.SessionID)
	changed := d.browser == nil || navigated || !reflect.DeepEqual(d.browser.Tools, data.Tools)
	if navigated {
		d.finishLocked(outcome{err: "stale_page"})
	}
	data.seen = d.now()
	d.browser = &data.browser
	if changed {
		d.changedLocked()
	}
	result := map[string]any{"ok": true}
	if data.Result != nil {
		accepted := d.pending != nil && d.pending.ID == data.Result.ID && d.pending.Generation == data.Result.Generation && d.pending.Generation == data.Page.Generation && d.pending.sessionID == data.Page.SessionID
		result["resultAccepted"] = accepted
		if accepted {
			d.finishLocked(outcome{result: data.Result.Result})
		}
	}
	respond(w, 200, result)
}

// Serve owns the supplied loopback listener. Cancellation never invokes a browser save or discards its working copy.
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
	d.finishLocked(outcome{err: "relay stopped"})
	d.mu.Unlock()
	for s := range d.server.Sessions() {
		_ = s.Close()
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	_ = srv.Close()
	if errors.Is(result, http.ErrServerClosed) {
		return nil
	}
	return result
}

// UnmarshalJSON distinguishes an omitted description from a valid empty one.
func (t *Tool) UnmarshalJSON(b []byte) error {
	type plain Tool
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	if desc, ok := fields["description"]; !ok || bytes.Equal(desc, []byte("null")) {
		return errors.New("tool description is required")
	}
	*t = Tool(v)
	return nil
}

func (d *Daemon) idleLocked() bool {
	return d.pending == nil && d.Config.IdleTimeout > 0 && d.now().Sub(d.lastActivity) >= d.Config.IdleTimeout
}
