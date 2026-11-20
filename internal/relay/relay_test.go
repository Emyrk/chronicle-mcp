package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type authTransport struct{ token string }

func (a authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)
	return http.DefaultTransport.RoundTrip(r)
}
func setup(t *testing.T) (*Daemon, *httptest.Server) {
	t.Helper()
	d, e := New(Config{Origin: "http://localhost:4000", AgentLease: 5 * time.Minute, IdleTimeout: 6 * time.Hour})
	if e != nil {
		t.Fatal(e)
	}
	s := httptest.NewUnstartedServer(d.Handler())
	d.Config.Port = s.Listener.Addr().(*net.TCPAddr).Port
	s.Start()
	t.Cleanup(func() {
		for sess := range d.server.Sessions() {
			_ = sess.Close()
		}
		s.Close()
	})
	return d, s
}
func request(t *testing.T, s *httptest.Server, token, method, path, origin string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	r, _ := http.NewRequest(method, s.URL+path, bytes.NewReader(b))
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Chronicle-Client", "tab")
	res, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	var v map[string]any
	_ = json.NewDecoder(res.Body).Decode(&v)
	return res.StatusCode, v
}
func heartbeat() map[string]any {
	return map[string]any{"clientId": "tab", "page": map[string]any{"url": "http://localhost:4000/edit#private", "generation": "g1", "sessionId": "p1"}, "tools": []any{map[string]any{"name": "edit", "description": "Browser-owned editing", "inputSchema": map[string]any{"type": "object", "additionalProperties": true, "properties": map[string]any{"value": map[string]any{"oneOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "number"}}}}}, "capability": map[string]any{"id": "cap", "version": 1, "stateRevision": 0}, "effect": "view_mutation", "confirmation": "unless_yolo"}}}
}
func client(t *testing.T, d *Daemon, s *httptest.Server, notify chan struct{}) *mcp.ClientSession {
	t.Helper()
	options := &mcp.ClientOptions{}
	if notify != nil {
		options.ToolListChangedHandler = func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case notify <- struct{}{}:
			default:
			}
		}
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, options)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, e := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: s.URL + "/mcp", HTTPClient: &http.Client{Transport: authTransport{d.AgentToken}}, MaxRetries: -1}, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}
func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-timer.C:
			t.Fatal("condition did not become true")
		case <-tick.C:
		}
	}
}
func pending(t *testing.T, d *Daemon) *command {
	t.Helper()
	var c *command
	eventually(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); c = d.pending; return c != nil })
	return c
}
func awaitResult(t *testing.T, ch <-chan *mcp.CallToolResult) *mcp.CallToolResult {
	t.Helper()
	select {
	case r := <-ch:
		if r == nil {
			t.Fatal("MCP call returned transport error")
		}
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("call did not finish")
		return nil
	}
}
func callAsync(c *mcp.ClientSession, ctx context.Context) <-chan *mcp.CallToolResult {
	ch := make(chan *mcp.CallToolResult, 1)
	go func() {
		r, _ := c.CallTool(ctx, &mcp.CallToolParams{Name: "edit", Arguments: map[string]any{"value": "hi"}})
		ch <- r
	}()
	return ch
}
func resultText(r *mcp.CallToolResult) string { return r.Content[0].(*mcp.TextContent).Text }

func TestMCPBrowserLifecycle(t *testing.T) {
	d, s := setup(t)
	notifications := make(chan struct{}, 10)
	a := client(t, d, s, notifications)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	list, e := a.ListTools(ctx, nil)
	if e != nil || len(list.Tools) != 1 {
		t.Fatalf("disconnected list: %v %v", list, e)
	}
	hb := heartbeat()
	if code, _ := request(t, s, d.Token, "POST", "/v1/browser", "http://localhost:4000", hb); code != 200 {
		t.Fatalf("heartbeat %d", code)
	}
	select {
	case <-notifications:
	case <-ctx.Done():
		t.Fatal("missing list changed")
	}
	list, e = a.ListTools(ctx, nil)
	if e != nil || len(list.Tools) != 2 {
		t.Fatalf("connected list: %v %v", list, e)
	}
	schema, _ := json.Marshal(list.Tools[1].InputSchema)
	if !bytes.Contains(schema, []byte("oneOf")) {
		t.Fatal("schema lost")
	}
	if _, e = a.CallTool(ctx, &mcp.CallToolParams{Name: "whereami"}); e != nil {
		t.Fatal(e)
	}
	b := client(t, d, s, nil)
	if _, e = b.ListTools(ctx, nil); e != nil {
		t.Fatal(e)
	}
	busy, e := b.CallTool(ctx, &mcp.CallToolParams{Name: "edit", Arguments: map[string]any{}})
	if e != nil || !busy.IsError || !strings.Contains(resultText(busy), "Another agent") {
		t.Fatalf("ownership not enforced: %v %v", busy, e)
	}
	ch := callAsync(a, ctx)
	c := pending(t, d)
	code, state := request(t, s, d.Token, "GET", "/v1/state", "http://localhost:4000", nil)
	if code != 200 || len(state["commands"].([]any)) != 1 {
		t.Fatalf("state %d %v", code, state)
	}
	hb["result"] = map[string]any{"id": c.ID, "generation": "g1", "result": map[string]any{"status": "rejected", "result": map[string]any{"message": "approval denied"}}}
	code, ack := request(t, s, d.Token, "POST", "/v1/browser", "http://localhost:4000", hb)
	if code != 200 || ack["resultAccepted"] != true {
		t.Fatalf("result %d %v", code, ack)
	}
	if r := awaitResult(t, ch); !r.IsError || !strings.Contains(resultText(r), "approval denied") {
		t.Fatalf("rejection lost: %v", r)
	}
	delete(hb, "result")
	ch = callAsync(a, ctx)
	c = pending(t, d)
	hb["page"].(map[string]any)["generation"] = "g2"
	request(t, s, d.Token, "POST", "/v1/browser", "http://localhost:4000", hb)
	if r := awaitResult(t, ch); !r.IsError || !strings.Contains(resultText(r), "stale_state") {
		t.Fatal("navigation did not invalidate")
	}
	hb["result"] = map[string]any{"id": c.ID, "generation": "g1", "result": map[string]any{"status": "success"}}
	_, ack = request(t, s, d.Token, "POST", "/v1/browser", "http://localhost:4000", hb)
	if ack["resultAccepted"] != false {
		t.Fatal("late result accepted")
	}
	stale, e := a.CallTool(ctx, &mcp.CallToolParams{Name: "edit", Arguments: map[string]any{}})
	if e != nil || !stale.IsError || !strings.Contains(resultText(stale), "stale_state") {
		t.Fatal("notification refreshed observation")
	}
	if _, e = a.CallTool(ctx, &mcp.CallToolParams{Name: "whereami"}); e != nil {
		t.Fatal(e)
	}
	callCtx, stop := context.WithCancel(ctx)
	ch = callAsync(a, callCtx)
	pending(t, d)
	stop()
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatal("cancel stuck")
	}
	eventually(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.pending == nil })
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	eventually(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.owner == "" })
	if _, e = b.ListTools(ctx, nil); e != nil {
		t.Fatal(e)
	}
	d.mu.Lock()
	owner := d.owner
	d.mu.Unlock()
	if owner != "" {
		t.Fatal("tools/list claimed ownership")
	}
	if _, e = b.CallTool(ctx, &mcp.CallToolParams{Name: "whereami"}); e != nil {
		t.Fatal(e)
	}
	d.mu.Lock()
	owner = d.owner
	d.mu.Unlock()
	if owner != b.ID() {
		t.Fatalf("ownership not transferred: %s", owner)
	}
}

func TestSecurityAndValidation(t *testing.T) {
	d, s := setup(t)
	cases := []struct {
		name, token, method, path, origin string
		body                              any
		want                              int
	}{
		{"bootstrap", "", "POST", "/v1/connect", "http://localhost:4000", map[string]any{"protocol": 2}, 200},
		{"bootstrap-no-origin", "", "POST", "/v1/connect", "", map[string]any{"protocol": 2}, 403},
		{"evil-origin", d.Token, "GET", "/v1/status", "https://evil.test", nil, 403},
		{"missing-auth", "", "GET", "/v1/status", "", nil, 401},
		{"browser-cannot-control", d.Token, "GET", "/health", "", nil, 401},
		{"browser-cannot-mcp", d.Token, "POST", "/mcp", "", nil, 401},
		{"agent-origin", d.AgentToken, "GET", "/health", "http://localhost:4000", nil, 403},
		{"agent-health", d.AgentToken, "GET", "/health", "", nil, 200},
		{"state-before-heartbeat", d.Token, "GET", "/v1/state", "http://localhost:4000", nil, 409},
		{"oversize", d.Token, "POST", "/v1/browser", "", strings.Repeat("x", bodyLimit), 413},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			code, v := request(t, s, tt.token, tt.method, tt.path, tt.origin, tt.body)
			if code != tt.want {
				t.Fatalf("got %d %v want%d", code, v, tt.want)
			}
			if tt.path == "/health" && code == 200 {
				b, _ := json.Marshal(v)
				if bytes.Contains(b, []byte(d.AgentToken)) || bytes.Contains(b, []byte(d.Token)) {
					t.Fatal("health leaked credentials")
				}
			}
		})
	}
	req, _ := http.NewRequest("GET", s.URL+"/health", nil)
	req.Host = "evil.test"
	req.Header.Set("Authorization", "Bearer "+d.AgentToken)
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("Host accepted")
	}
	for _, mutate := range []func(map[string]any){func(h map[string]any) { h["page"].(map[string]any)["url"] = "https://evil.test/" }, func(h map[string]any) { h["tools"].([]any)[0].(map[string]any)["name"] = "whereami" }, func(h map[string]any) { delete(h["tools"].([]any)[0].(map[string]any), "description") }, func(h map[string]any) {
		h["tools"].([]any)[0].(map[string]any)["capability"].(map[string]any)["version"] = 1.5
	}} {
		h := heartbeat()
		mutate(h)
		if code, _ := request(t, s, d.Token, "POST", "/v1/browser", "http://localhost:4000", h); code != 400 {
			t.Fatalf("invalid heartbeat accepted %d", code)
		}
	}
	h := heartbeat()
	request(t, s, d.Token, "POST", "/v1/browser", "http://localhost:4000", h)
	h["clientId"] = "other"
	if code, _ := request(t, s, d.Token, "POST", "/v1/browser", "http://localhost:4000", h); code != 409 {
		t.Fatal("competing browser accepted")
	}
}
func TestActivityAndLeaseClock(t *testing.T) {
	d, s := setup(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	d.lastActivity = now
	request(t, s, d.Token, "POST", "/v1/browser", "http://localhost:4000", heartbeat())
	now = now.Add(time.Minute)
	request(t, s, d.Token, "GET", "/v1/status", "", nil)
	if !d.lastActivity.Equal(now.Add(-time.Minute)) {
		t.Fatal("browser/status renewed idle")
	}
	d.mu.Lock()
	d.activityLocked("agent")
	d.observeLocked("agent", true)
	d.mu.Unlock()
	last := d.lastActivity
	now = now.Add(4 * time.Minute)
	d.mu.Lock()
	d.activityLocked("agent")
	d.mu.Unlock()
	if !d.lastActivity.After(last) {
		t.Fatal("agent keepalive did not renew")
	}
	now = now.Add(5 * time.Minute)
	d.mu.Lock()
	d.expireLocked()
	if len(d.agents) != 0 || d.owner != "" {
		t.Fatal("lease not released")
	}
	last = d.lastActivity
	d.mu.Unlock()
	now = now.Add(time.Hour)
	request(t, s, d.Token, "POST", "/v1/browser", "http://localhost:4000", heartbeat())
	request(t, s, d.AgentToken, "GET", "/health", "", nil)
	if !last.Equal(d.lastActivity) {
		t.Fatal("zero-agent housekeeping renewed idle")
	}
}
func TestShutdownRequiresForce(t *testing.T) {
	d, s := setup(t)
	a := client(t, d, s, nil)
	_ = a
	code, _ := request(t, s, d.AgentToken, "POST", "/shutdown", "", map[string]any{})
	if code != 409 {
		t.Fatalf("active shutdown %d", code)
	}
	code, _ = request(t, s, d.AgentToken, "POST", "/shutdown", "", map[string]any{"force": true})
	if code != 200 {
		t.Fatal(code)
	}
	select {
	case <-d.shutdown:
	default:
		t.Fatal("shutdown not signaled")
	}
}

func TestIdlePolicy(t *testing.T) {
	d, s := setup(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	d.lastActivity = now
	now = now.Add(6*time.Hour - time.Second)
	if d.idleLocked() {
		t.Fatal("expired before six hours")
	}
	// Browser-only activity, including re-pairing, does not keep the daemon alive.
	request(t, s, d.Token, "POST", "/v1/browser", "http://localhost:4000", heartbeat())
	now = now.Add(time.Second)
	if !d.idleLocked() {
		t.Fatal("did not expire at six hours")
	}
	d.Config.IdleTimeout = 0
	if d.idleLocked() {
		t.Fatal("disabled timeout expired")
	}
	d.Config.IdleTimeout = 6 * time.Hour
	d.mu.Lock()
	d.activityLocked("session")
	d.mu.Unlock()
	if d.idleLocked() {
		t.Fatal("agent activity failed to extend idle deadline")
	}
}

func TestServeShutdownCancelsWork(t *testing.T) {
	d, e := New(Config{AgentLease: 5 * time.Minute, IdleTimeout: 6 * time.Hour})
	if e != nil {
		t.Fatal(e)
	}
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx, l) }()
	// A response proves the server has finished initializing Config.Port.
	endpoint := "http://" + l.Addr().String()
	req, _ := http.NewRequest("GET", endpoint+"/health", nil)
	req.Header.Set("Authorization", "Bearer "+d.AgentToken)
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	c := mcp.NewClient(&mcp.Implementation{Name: "shutdown-test", Version: "1"}, nil)
	session, e := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: endpoint + "/mcp", HTTPClient: &http.Client{Transport: authTransport{d.AgentToken}}, MaxRetries: -1}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	fakeServer := &httptest.Server{URL: endpoint}
	request(t, fakeServer, d.Token, "POST", "/v1/browser", "http://localhost:4000", heartbeat())
	if _, e = session.ListTools(context.Background(), nil); e != nil {
		t.Fatal(e)
	}
	callCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if _, e = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "whereami"}); e != nil {
		t.Fatal(e)
	}
	ch := callAsync(session, callCtx)
	pending(t, d)
	cancel()
	select {
	case <-ch:
	case <-callCtx.Done():
		t.Fatal("shutdown did not cancel call")
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-callCtx.Done():
		t.Fatal("Serve did not stop")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending != nil {
		t.Fatal("pending command retained")
	}
	if d.browser == nil {
		t.Fatal("shutdown unnecessarily discarded browser metadata")
	}
}

func TestDiscoveryDoesNotClaimAndCallsNeedExplicitClaim(t *testing.T) {
	d, s := setup(t)
	a := client(t, d, s, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request(t, s, d.Token, "POST", "/v1/browser", "http://localhost:4000", heartbeat())
	if _, e := a.ListTools(ctx, nil); e != nil {
		t.Fatal(e)
	}
	d.mu.Lock()
	owner := d.owner
	d.mu.Unlock()
	if owner != "" {
		t.Fatal("automatic discovery claimed browser")
	}
	r, e := a.CallTool(ctx, &mcp.CallToolParams{Name: "edit", Arguments: map[string]any{}})
	if e != nil || !r.IsError || !strings.Contains(resultText(r), "whereami") {
		t.Fatalf("unclaimed call: %v %v", r, e)
	}
	if _, e = a.CallTool(ctx, &mcp.CallToolParams{Name: "whereami"}); e != nil {
		t.Fatal(e)
	}
	d.mu.Lock()
	owner = d.owner
	d.mu.Unlock()
	if owner != a.ID() {
		t.Fatal("explicit observation did not claim")
	}
}

func TestPendingCallPinsLeaseAndIdle(t *testing.T) {
	d, e := New(Config{IdleTimeout: time.Second, AgentLease: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	d.lastActivity = now
	d.activityLocked("owner")
	d.owner = "owner"
	d.pending = &command{owner: "owner", done: make(chan outcome, 1)}
	now = now.Add(2 * time.Second)
	d.expireLocked()
	if d.owner != "owner" || len(d.agents) != 1 {
		t.Fatal("active approval call lost agent lease")
	}
	if d.idleLocked() {
		t.Fatal("active call triggered idle exit")
	}
	d.finishLocked(outcome{err: "complete"})
	d.expireLocked()
	if d.owner != "" || len(d.agents) != 0 || !d.idleLocked() {
		t.Fatal("completed call retained stale lease")
	}
}

func TestCapabilityRevisionAndSuccessContinuation(t *testing.T) {
	d, s := setup(t)
	a := client(t, d, s, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hb := heartbeat()
	request(t, s, d.Token, "POST", "/v1/browser", "http://localhost:4000", hb)
	if _, e := a.CallTool(ctx, &mcp.CallToolParams{Name: "whereami"}); e != nil {
		t.Fatal(e)
	}
	hb["tools"].([]any)[0].(map[string]any)["capability"].(map[string]any)["stateRevision"] = 1
	request(t, s, d.Token, "POST", "/v1/browser", "http://localhost:4000", hb)
	r, e := a.CallTool(ctx, &mcp.CallToolParams{Name: "edit", Arguments: map[string]any{}})
	if e != nil || !r.IsError || !strings.Contains(resultText(r), "stale_state") {
		t.Fatalf("changed capability accepted: %v %v", r, e)
	}
	if _, e = a.ListTools(ctx, nil); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		ch := callAsync(a, ctx)
		c := pending(t, d)
		continuation := map[string]any{"status": "success", "result": map[string]any{"revision": 42, "saved": false}, "extra": "browser-defined"}
		hb["result"] = map[string]any{"id": c.ID, "generation": "g1", "result": continuation}
		code, ack := request(t, s, d.Token, "POST", "/v1/browser", "http://localhost:4000", hb)
		if code != 200 || ack["resultAccepted"] != true {
			t.Fatal(code, ack)
		}
		got := awaitResult(t, ch)
		want, _ := json.Marshal(continuation)
		if got.IsError || resultText(got) != string(want) {
			t.Fatalf("continuation modified: %s", resultText(got))
		}
	}
}
