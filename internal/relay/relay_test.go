package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
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
	t.Cleanup(func() { d.closeSessions(); s.Close() })
	return d, s
}
func request(t *testing.T, s *httptest.Server, token, id, method, path, origin string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	r, _ := http.NewRequest(method, s.URL+path, bytes.NewReader(b))
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if id != "" {
		r.Header.Set("X-Chronicle-Client", id)
	}
	r.Header.Set("Content-Type", "application/json")
	res, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	var v map[string]any
	_ = json.NewDecoder(res.Body).Decode(&v)
	return res.StatusCode, v
}
func heartbeat(id, name, tool string) map[string]any {
	return map[string]any{"clientId": id, "name": name, "page": map[string]any{"url": "http://localhost:4000/edit#private", "generation": "g1", "sessionId": "page-" + id}, "tools": []any{map[string]any{"name": tool, "description": "Browser-owned tool", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"oneOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "number"}}}}, "additionalProperties": true}, "capability": map[string]any{"id": "cap-" + id, "version": 1, "stateRevision": 0}, "effect": "view_mutation", "confirmation": "unless_yolo"}}}
}

type tab struct {
	id, token, origin string
	hb                map[string]any
}

func connect(t *testing.T, s *httptest.Server, id, tool string) *tab {
	t.Helper()
	code, result := request(t, s, "", "", "POST", "/v1/connect", "http://localhost:4000", map[string]any{"protocol": 3, "clientId": id})
	if code != 200 {
		t.Fatalf("bootstrap %d %v", code, result)
	}
	token := result["token"].(string)
	if len(token) != 64 || strings.Trim(token, "0123456789abcdef") != "" {
		t.Fatal("invalid bootstrap credential shape")
	}
	b := &tab{id, token, "http://localhost:4000", heartbeat(id, "same-name", tool)}
	b.beat(t, s)
	return b
}
func (b *tab) req(t *testing.T, s *httptest.Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	return request(t, s, b.token, b.id, method, path, b.origin, body)
}
func (b *tab) beat(t *testing.T, s *httptest.Server) map[string]any {
	t.Helper()
	code, v := b.req(t, s, "POST", "/v1/browser", b.hb)
	if code != 200 {
		t.Fatalf("heartbeat %d %v", code, v)
	}
	return v
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
	c := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, options)
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
func pending(t *testing.T, d *Daemon, id string) *command {
	t.Helper()
	var c *command
	eventually(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		if b := d.connections[id]; b != nil {
			c = b.pending
		}
		return c != nil
	})
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
func callAsync(c *mcp.ClientSession, ctx context.Context, name string) <-chan *mcp.CallToolResult {
	ch := make(chan *mcp.CallToolResult, 1)
	go func() {
		r, _ := c.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"value": "hello"}})
		ch <- r
	}()
	return ch
}
func resultText(r *mcp.CallToolResult) string { return r.Content[0].(*mcp.TextContent).Text }
func call(t *testing.T, c *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	if args == nil {
		args = map[string]any{}
	}
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, e := c.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func value(t *testing.T, r *mcp.CallToolResult) map[string]any {
	t.Helper()
	var v map[string]any
	if e := json.Unmarshal([]byte(resultText(r)), &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func selectTab(t *testing.T, c *mcp.ClientSession, id string, observe bool) {
	t.Helper()
	r := call(t, c, "select_connection", map[string]any{"connection_id": id})
	if r.IsError {
		t.Fatal(resultText(r))
	}
	if observe {
		r = call(t, c, "whereami", nil)
		if r.IsError {
			t.Fatal(resultText(r))
		}
	}
}
func checkTools(t *testing.T, c *mcp.ClientSession, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, e := c.ListTools(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	n := 4
	if want != "" {
		n++
	}
	if len(list.Tools) != n {
		t.Fatalf("tool count %d want%d", len(list.Tools), n)
	}
	if want != "" {
		if list.Tools[4].Name != want {
			t.Fatalf("wrong selected catalog: %s", list.Tools[4].Name)
		}
		b, _ := json.Marshal(list.Tools[4].InputSchema)
		if !bytes.Contains(b, []byte("oneOf")) {
			t.Fatal("schema modified")
		}
	}
}
func (b *tab) result(t *testing.T, s *httptest.Server, c *command, revision uint64, status string) bool {
	t.Helper()
	b.hb["result"] = map[string]any{"id": c.ID, "generation": c.Generation, "ownershipRevision": revision, "result": map[string]any{"status": status, "result": map[string]any{"message": "browser-defined"}, "extra": 42}}
	response := b.beat(t, s)
	delete(b.hb, "result")
	return response["resultAccepted"] == true
}

func TestTwoBrowsersTwoAgentsRouting(t *testing.T) {
	d, s := setup(t)
	aTab := connect(t, s, "tab-a", "edit_a")
	bTab := connect(t, s, "tab-b", "edit_b")
	a := client(t, d, s, nil)
	b := client(t, d, s, nil)
	listed := value(t, call(t, a, "list_connections", nil))["connections"].([]any)
	if len(listed) != 2 {
		t.Fatal(listed)
	}
	for _, item := range listed {
		m := item.(map[string]any)
		if m["name"] != "same-name" || m["availability"] != "available" || m["owner"] != nil {
			t.Fatal(m)
		}
	}
	checkTools(t, a, "")
	checkTools(t, b, "")
	for _, c := range []*mcp.ClientSession{a, b} {
		where := value(t, call(t, c, "whereami", nil))
		if where["selectedConnection"] != nil || where["connected"] != false || where["page"] != nil {
			t.Fatal("implicit selection", where)
		}
	}
	selectTab(t, a, aTab.id, false)
	unobserved := call(t, a, "edit_a", map[string]any{})
	if !unobserved.IsError || !strings.Contains(resultText(unobserved), "stale_state") {
		t.Fatal("selection authorized unobserved execution")
	}
	selectTab(t, b, bTab.id, true)
	checkTools(t, a, "edit_a")
	checkTools(t, b, "edit_b")
	idempotent := value(t, call(t, a, "select_connection", map[string]any{"connection_id": aTab.id}))["connection"].(map[string]any)
	if idempotent["ownershipRevision"] != float64(1) {
		t.Fatal("idempotent select incremented revision")
	}
	failed := call(t, a, "select_connection", map[string]any{"connection_id": bTab.id})
	if !failed.IsError || value(t, failed)["error"] != "already_selected" {
		t.Fatal("switch did not require release")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	aCall := callAsync(a, ctx, "edit_a")
	bCall := callAsync(b, ctx, "edit_b")
	ac := pending(t, d, aTab.id)
	bc := pending(t, d, bTab.id)
	concurrent := call(t, a, "edit_a", map[string]any{})
	if !concurrent.IsError || !strings.Contains(resultText(concurrent), "already pending") {
		t.Fatal("concurrent call accepted on one connection")
	}
	for _, pair := range []struct {
		tab *tab
		cmd *command
	}{{aTab, ac}, {bTab, bc}} {
		code, state := pair.tab.req(t, s, "GET", "/v1/state", nil)
		commands := state["commands"].([]any)
		if code != 200 || len(commands) != 1 || commands[0].(map[string]any)["id"] != pair.cmd.ID || state["connection"].(map[string]any)["owner"] == nil {
			t.Fatalf("cross-tab routing: %d %v", code, state)
		}
	}
	if aTab.result(t, s, bc, bc.OwnershipRevision, "success") {
		t.Fatal("cross-tab result accepted")
	}
	if !aTab.result(t, s, ac, ac.OwnershipRevision, "rejected") {
		t.Fatal("approval rejection not accepted")
	}
	r := awaitResult(t, aCall)
	if !r.IsError || !strings.Contains(resultText(r), "browser-defined") {
		t.Fatal("approval rejection lost")
	}
	if !bTab.result(t, s, bc, bc.OwnershipRevision, "success") {
		t.Fatal("success rejected")
	}
	r = awaitResult(t, bCall)
	if r.IsError || !strings.Contains(resultText(r), `"extra":42`) {
		t.Fatal("continuation modified")
	}
	// A release only cancels that tab and removes only its selected catalog.
	aCall = callAsync(a, ctx, "edit_a")
	bCall = callAsync(b, ctx, "edit_b")
	pending(t, d, aTab.id)
	bc = pending(t, d, bTab.id)
	if r = call(t, a, "release_connection", map[string]any{"connection_id": aTab.id}); r.IsError {
		t.Fatal(resultText(r))
	}
	if !awaitResult(t, aCall).IsError {
		t.Fatal("released pending call succeeded")
	}
	checkTools(t, a, "")
	checkTools(t, b, "edit_b")
	if !bTab.result(t, s, bc, bc.OwnershipRevision, "success") {
		t.Fatal("other tab affected by release")
	}
	awaitResult(t, bCall)
	selectTab(t, a, aTab.id, true)
	aCall = callAsync(a, ctx, "edit_a")
	pending(t, d, aTab.id)
	bCall = callAsync(b, ctx, "edit_b")
	bc = pending(t, d, bTab.id)
	deleteReq, _ := http.NewRequest("DELETE", s.URL+"/mcp", nil)
	deleteReq.Header.Set("Authorization", "Bearer "+d.AgentToken)
	deleteReq.Header.Set("Mcp-Session-Id", a.ID())
	deleteResponse, e := http.DefaultClient.Do(deleteReq)
	if e != nil {
		t.Fatal(e)
	}
	deleteResponse.Body.Close()
	if deleteResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status %d", deleteResponse.StatusCode)
	}
	select {
	case <-aCall:
	case <-ctx.Done():
		t.Fatal("disconnect did not cancel")
	}
	eventually(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.connections[aTab.id].owner == "" && d.connections[aTab.id].pending == nil
	})
	if !bTab.result(t, s, bc, bc.OwnershipRevision, "success") {
		t.Fatal("agent disconnect affected other tab")
	}
	awaitResult(t, bCall)
}

func TestConcurrentClaimsRevokeAndRevision(t *testing.T) {
	d, s := setup(t)
	tab := connect(t, s, "shared", "edit")
	a := client(t, d, s, nil)
	b := client(t, d, s, nil)
	start := make(chan struct{})
	results := make(chan struct {
		session *mcp.ClientSession
		r       *mcp.CallToolResult
	}, 2)
	var wg sync.WaitGroup
	for _, c := range []*mcp.ClientSession{a, b} {
		wg.Add(1)
		go func(c *mcp.ClientSession) {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r, _ := c.CallTool(ctx, &mcp.CallToolParams{Name: "select_connection", Arguments: map[string]any{"connection_id": tab.id}})
			results <- struct {
				session *mcp.ClientSession
				r       *mcp.CallToolResult
			}{c, r}
		}(c)
	}
	close(start)
	wg.Wait()
	close(results)
	var winner, loser *mcp.ClientSession
	for result := range results {
		if result.r == nil {
			t.Fatal("transport error")
		}
		if result.r.IsError {
			loser = result.session
		} else {
			winner = result.session
		}
	}
	if winner == nil || loser == nil {
		t.Fatal("claim was not exclusive")
	}
	call(t, winner, "whereami", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ch := callAsync(winner, ctx, "edit")
	cmd := pending(t, d, tab.id)
	code, response := tab.req(t, s, "POST", "/v1/browser/revoke", map[string]any{"expectedOwnershipRevision": cmd.OwnershipRevision})
	if code != 200 || response["connection"].(map[string]any)["owner"] != nil {
		t.Fatal(code, response)
	}
	if !awaitResult(t, ch).IsError {
		t.Fatal("revoked operation succeeded")
	}
	checkTools(t, winner, "")
	selectTab(t, loser, tab.id, true)
	code, response = tab.req(t, s, "POST", "/v1/browser/revoke", map[string]any{"expectedOwnershipRevision": cmd.OwnershipRevision})
	if code != 409 || response["connection"].(map[string]any)["ownershipRevision"] != float64(3) {
		t.Fatal("stale revoke affected new owner", code, response)
	}
	if tab.result(t, s, cmd, cmd.OwnershipRevision, "success") {
		t.Fatal("revoked late result accepted")
	}
	ch = callAsync(loser, ctx, "edit")
	newCmd := pending(t, d, tab.id)
	if tab.result(t, s, newCmd, cmd.OwnershipRevision, "success") {
		t.Fatal("old revision accepted for current command")
	}
	if !tab.result(t, s, newCmd, newCmd.OwnershipRevision, "success") {
		t.Fatal("current result rejected")
	}
	awaitResult(t, ch)
	if r := call(t, winner, "release_connection", map[string]any{"connection_id": tab.id}); !r.IsError {
		t.Fatal("delayed release evicted new owner")
	}
	call(t, loser, "release_connection", map[string]any{"connection_id": tab.id})
	selectTab(t, loser, tab.id, false)
	if r := call(t, loser, "edit", map[string]any{}); !r.IsError || !strings.Contains(resultText(r), "stale_state") {
		t.Fatal("reacquisition retained observation")
	}
}

func TestNavigationCapabilitiesCancellationAndNotifications(t *testing.T) {
	d, s := setup(t)
	tab := connect(t, s, "tab", "edit")
	notify := make(chan struct{}, 10)
	a := client(t, d, s, notify)
	selectTab(t, a, tab.id, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	select {
	case <-notify:
	case <-ctx.Done():
		t.Fatal("missing selected catalog notification")
	}
	ch := callAsync(a, ctx, "edit")
	cmd := pending(t, d, tab.id)
	tab.hb["page"].(map[string]any)["sessionId"] = "new-page"
	tab.beat(t, s)
	select {
	case <-notify:
	case <-ctx.Done():
		t.Fatal("missing navigation catalog notification")
	}
	r := awaitResult(t, ch)
	if !r.IsError || !strings.Contains(resultText(r), "stale_state") {
		t.Fatal("navigation not invalidated")
	}
	if tab.result(t, s, cmd, cmd.OwnershipRevision, "success") {
		t.Fatal("old page result accepted")
	}
	r = call(t, a, "edit", map[string]any{})
	if !r.IsError {
		t.Fatal("notification refreshed observation")
	}
	checkTools(t, a, "edit")
	tab.hb["tools"].([]any)[0].(map[string]any)["capability"].(map[string]any)["stateRevision"] = 1
	tab.beat(t, s)
	r = call(t, a, "edit", map[string]any{})
	if !r.IsError || !strings.Contains(resultText(r), "stale_state") {
		t.Fatal("capability revision not pinned")
	}
	call(t, a, "whereami", nil)
	// Renaming is metadata only and must not release or invalidate the observation.
	tab.hb["name"] = "  renamed 🙂  "
	metadata := tab.beat(t, s)["connection"].(map[string]any)
	if metadata["name"] != "renamed 🙂" || metadata["ownershipRevision"] != float64(1) {
		t.Fatal(metadata)
	}
	callCtx, stop := context.WithCancel(ctx)
	ch = callAsync(a, callCtx, "edit")
	pending(t, d, tab.id)
	stop()
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatal("cancellation stuck")
	}
	eventually(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.connections[tab.id].pending == nil })
	code, _ := tab.req(t, s, "POST", "/v1/browser/disconnect", map[string]any{})
	if code != 200 {
		t.Fatal(code)
	}
	checkTools(t, a, "")
	code, _ = tab.req(t, s, "GET", "/v1/state", nil)
	if code != 401 {
		t.Fatal("disconnected token valid", code)
	}
}

func TestScopedAuthenticationAndValidation(t *testing.T) {
	d, s := setup(t)
	a := connect(t, s, "a", "edit_a")
	b := connect(t, s, "b", "edit_b")
	code, v := request(t, s, "", "", "POST", "/v1/connect", a.origin, map[string]any{"protocol": 2})
	if code != 400 || v["error"] != "upgrade_required" || !strings.Contains(v["message"].(string), "protocol 3") {
		t.Fatal(code, v)
	}
	code, _ = request(t, s, "", "", "POST", "/v1/connect", a.origin, map[string]any{"protocol": 3, "clientId": a.id})
	if code != 409 {
		t.Fatal("duplicate client ID accepted")
	}
	for _, path := range []string{"/v1/state", "/v1/status", "/v1/browser/revoke", "/v1/browser/disconnect"} {
		method := "GET"
		if strings.Contains(path, "browser/") {
			method = "POST"
		}
		code, _ = request(t, s, a.token, b.id, method, path, a.origin, map[string]any{"expectedOwnershipRevision": 0})
		if code != 401 {
			t.Fatalf("cross-tab auth %s %d", path, code)
		}
	}
	code, _ = request(t, s, a.token, a.id, "POST", "/v1/browser", a.origin, b.hb)
	if code != 403 {
		t.Fatal("body identity mismatch accepted")
	}
	for _, origin := range []string{"", "https://chronicleclassic.com", "https://other.chronicleclassic.com", "https://evil.test"} {
		code, _ = request(t, s, a.token, a.id, "GET", "/v1/state", origin, nil)
		if code != 403 {
			t.Fatalf("origin %q accepted %d", origin, code)
		}
	}
	for _, path := range []string{"/health", "/mcp", "/shutdown"} {
		code, _ = request(t, s, a.token, a.id, "GET", path, "", nil)
		if code != 401 {
			t.Fatal("browser controlled agent endpoint", path, code)
		}
	}
	code, _ = request(t, s, d.AgentToken, "", "GET", "/health", a.origin, nil)
	if code != 403 {
		t.Fatal("browser origin controlled health")
	}
	code, _ = request(t, s, d.Token, a.id, "GET", "/v1/state", a.origin, nil)
	if code != 401 {
		t.Fatal("state cleanup token granted browser access")
	}
	req, _ := http.NewRequest("GET", s.URL+"/health", nil)
	req.Host = "evil.test"
	req.Header.Set("Authorization", "Bearer "+d.AgentToken)
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("host bypass")
	}
	for _, mutate := range []func(map[string]any){func(h map[string]any) { h["name"] = "  " }, func(h map[string]any) { h["name"] = strings.Repeat("🙂", 81) }, func(h map[string]any) { h["page"].(map[string]any)["url"] = "https://chronicleclassic.com/edit" }, func(h map[string]any) { delete(h["tools"].([]any)[0].(map[string]any), "description") }, func(h map[string]any) {
		h["tools"].([]any)[0].(map[string]any)["capability"].(map[string]any)["version"] = 1.5
	}, func(h map[string]any) { h["result"] = map[string]any{"id": "id", "generation": "g1", "result": nil} }} {
		h := heartbeat(a.id, "name", "edit")
		mutate(h)
		code, _ = a.req(t, s, "POST", "/v1/browser", h)
		if code != 400 {
			t.Fatal("invalid heartbeat accepted", code, h)
		}
	}
	for _, name := range []string{"whereami", "list_connections", "select_connection", "release_connection", "status"} {
		h := heartbeat(a.id, "name", name)
		code, _ = a.req(t, s, "POST", "/v1/browser", h)
		if code != 400 {
			t.Fatal("reserved name accepted", name)
		}
	}
	code, _ = a.req(t, s, "POST", "/v1/browser", strings.Repeat("x", bodyLimit))
	if code != 413 {
		t.Fatal("unbounded body")
	}
	h := heartbeat(a.id, "name", "edit")
	h["result"] = map[string]any{"id": "id", "generation": "g1", "ownershipRevision": 0, "result": strings.Repeat("x", 64<<10)}
	code, _ = a.req(t, s, "POST", "/v1/browser", h)
	if code != 413 {
		t.Fatal("unbounded result")
	}
	for _, revision := range []any{nil, -1, 1.5, float64(maxRevision) + 1} {
		code, _ = a.req(t, s, "POST", "/v1/browser/revoke", map[string]any{"expectedOwnershipRevision": revision})
		if code != 400 {
			t.Fatal("invalid ownership revision", revision, code)
		}
	}
}

func TestExpiryAndIdleClocks(t *testing.T) {
	d, s := setup(t)
	var elapsed atomic.Int64
	base := time.Now()
	d.now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
	d.lastActivity = base
	request(t, s, "", "", "POST", "/v1/connect", "http://localhost:4000", map[string]any{"protocol": 3, "clientId": "unused"})
	a := connect(t, s, "a", "edit")
	b := connect(t, s, "b", "edit")
	agent := client(t, d, s, nil)
	selectTab(t, agent, a.id, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch := callAsync(agent, ctx, "edit")
	pending(t, d, a.id)
	elapsed.Store(int64(9 * time.Second))
	b.beat(t, s)
	elapsed.Store(int64(10 * time.Second))
	code, _ := b.req(t, s, "GET", "/v1/state", nil)
	if code != 200 {
		t.Fatal("other browser lease expired")
	}
	if !awaitResult(t, ch).IsError {
		t.Fatal("expired call succeeded")
	}
	d.mu.Lock()
	_, unused := d.connections["unused"]
	_, old := d.connections[a.id]
	remaining := len(d.connections)
	last := d.lastActivity
	d.mu.Unlock()
	if unused || old || remaining != 1 {
		t.Fatal("expiry did not remove allocation and token")
	}
	checkTools(t, agent, "")
	_ = agent.Close()
	elapsed.Store(int64(7 * time.Hour))
	b = connect(t, s, "new-b", "edit")
	b.beat(t, s)
	request(t, s, d.AgentToken, "", "GET", "/health", "", nil)
	d.mu.Lock()
	if !d.idleLocked() || d.lastActivity.Before(last) {
		t.Fatal("browser housekeeping reset idle")
	}
	d.Config.IdleTimeout = 0
	if d.idleLocked() {
		t.Fatal("disabled idle expired")
	}
	d.mu.Unlock()
}

func TestAgentGETLeaseAndSafeStatus(t *testing.T) {
	d, s := setup(t)
	d.Config.AgentLease = 5 * time.Second
	var elapsed atomic.Int64
	base := time.Now()
	d.now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
	tab := connect(t, s, "tab", "edit")
	a := client(t, d, s, nil)
	selectTab(t, a, tab.id, true)
	code, status := request(t, s, d.AgentToken, "", "GET", "/health", "", nil)
	if code != 200 || status["protocol"] != float64(3) || status["version"] != Version {
		t.Fatal(status)
	}
	connection := status["connections"].([]any)[0].(map[string]any)
	owner := connection["owner"].(map[string]any)
	if owner["id"] == a.ID() || owner["name"] != "test-agent" {
		t.Fatal("unsafe owner identity", owner)
	}
	for _, field := range []string{"id", "name", "version"} {
		text := owner[field].(string)
		if n := utf8.RuneCountInString(text); n < 1 || n > 128 {
			t.Fatal("unbounded owner", owner)
		}
	}
	encoded, _ := json.Marshal(status)
	for _, secret := range []string{d.Token, d.AgentToken, tab.token, a.ID(), `"sessionId"`, `"generation"`} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("status secret leak")
		}
	}
	elapsed.Store(int64(4 * time.Second))
	req, _ := http.NewRequest("GET", s.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+d.AgentToken)
	req.Header.Set("Mcp-Session-Id", a.ID())
	req.Header.Set("Accept", "application/json")
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	elapsed.Store(int64(6 * time.Second))
	tab.beat(t, s)
	d.mu.Lock()
	d.expireLocked()
	owned := d.connections[tab.id].owner == a.ID()
	d.mu.Unlock()
	if !owned {
		t.Fatal("GET did not renew agent lease")
	}
	elapsed.Store(int64(9 * time.Second))
	tab.beat(t, s)
	d.mu.Lock()
	d.expireLocked()
	owned = d.connections[tab.id].owner != ""
	count := len(d.agents)
	d.mu.Unlock()
	if owned || count != 0 {
		t.Fatal("ghost owner after agent expiry")
	}
	res, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatal("expired session resurrected")
	}
}

func TestPendingPinsAgentAndIdleButNotBrowserLease(t *testing.T) {
	d, s := setup(t)
	d.Config.AgentLease = time.Second
	d.Config.IdleTimeout = time.Second
	var elapsed atomic.Int64
	base := time.Now()
	d.now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
	tab := connect(t, s, "tab", "edit")
	a := client(t, d, s, nil)
	selectTab(t, a, tab.id, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch := callAsync(a, ctx, "edit")
	cmd := pending(t, d, tab.id)
	elapsed.Store(int64(2 * time.Second))
	tab.beat(t, s)
	d.mu.Lock()
	pinned := d.connections[tab.id].owner == a.ID() && !d.idleLocked()
	d.mu.Unlock()
	if !pinned {
		t.Fatal("pending approval lost lease")
	}
	if !tab.result(t, s, cmd, cmd.OwnershipRevision, "success") {
		t.Fatal("pending result rejected")
	}
	awaitResult(t, ch)
	d.mu.Lock()
	d.expireLocked()
	idle := d.idleLocked()
	owner := d.connections[tab.id].owner
	d.mu.Unlock()
	if !idle || owner != "" {
		t.Fatal("completed work retained stale lease")
	}
}

func TestChronicleOrigins(t *testing.T) {
	d, _ := setup(t)
	for _, origin := range []string{"https://chronicleclassic.com", "https://octo.chronicleclassic.com", "https://a.b.chronicleclassic.com", "https://octo.chronicleclassic.com:443", "http://localhost:4000"} {
		if !d.allowed(origin) {
			t.Fatal("approved origin rejected", origin)
		}
	}
	for _, origin := range []string{"https://evilchronicleclassic.com", "https://chronicleclassic.com.evil.test", "http://octo.chronicleclassic.com", "https://octo.chronicleclassic.com:123", "https://chronicleclassic.com/", "https://*.chronicleclassic.com", "https://user@chronicleclassic.com"} {
		if d.allowed(origin) {
			t.Fatal("unsafe origin approved", origin)
		}
	}
}

func TestShutdownCancelsAllConnections(t *testing.T) {
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
	s := &httptest.Server{URL: "http://" + l.Addr().String()}
	request(t, s, d.AgentToken, "", "GET", "/health", "", nil)
	aTab := connect(t, s, "a", "edit")
	bTab := connect(t, s, "b", "edit")
	a := client(t, d, s, nil)
	b := client(t, d, s, nil)
	selectTab(t, a, aTab.id, true)
	selectTab(t, b, bTab.id, true)
	callCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	ac := callAsync(a, callCtx, "edit")
	bc := callAsync(b, callCtx, "edit")
	pending(t, d, aTab.id)
	pending(t, d, bTab.id)
	code, _ := request(t, s, d.AgentToken, "", "POST", "/shutdown", "", map[string]any{})
	if code != 409 {
		t.Fatal("unforced active shutdown")
	}
	code, _ = request(t, s, d.AgentToken, "", "POST", "/shutdown", "", map[string]any{"force": true})
	if code != 200 {
		t.Fatal(code)
	}
	for _, ch := range []<-chan *mcp.CallToolResult{ac, bc} {
		select {
		case <-ch:
		case <-callCtx.Done():
			t.Fatal("shutdown did not cancel work")
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-callCtx.Done():
		t.Fatal("shutdown stuck")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pendingLocked() != 0 {
		t.Fatal("pending work retained")
	}
	if len(d.connections) != 2 {
		t.Fatal("shutdown unnecessarily discarded browser metadata")
	}
}

func TestPreflightBootstrapAndResultBounds(t *testing.T) {
	d, s := setup(t)
	for _, origin := range []string{"", "https://evil.test"} {
		code, _ := request(t, s, "", "", "POST", "/v1/connect", origin, map[string]any{"protocol": 3, "clientId": "id"})
		if code != 403 {
			t.Fatal("unauthorized bootstrap", code)
		}
	}
	req, _ := http.NewRequest("OPTIONS", s.URL+"/v1/browser", nil)
	req.Header.Set("Origin", "https://octo.chronicleclassic.com")
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 204 || res.Header.Get("Access-Control-Allow-Origin") != "https://octo.chronicleclassic.com" || res.Header.Get("Access-Control-Allow-Private-Network") != "true" || !strings.Contains(res.Header.Get("Access-Control-Allow-Headers"), "X-Chronicle-Client") {
		t.Fatal("invalid PNA preflight")
	}
	_ = d
}

func TestCallDeadline(t *testing.T) {
	d, s := setup(t)
	d.callTimeout = 0
	tab := connect(t, s, "tab", "edit")
	a := client(t, d, s, nil)
	selectTab(t, a, tab.id, true)
	r := call(t, a, "edit", map[string]any{})
	if !r.IsError || !strings.Contains(resultText(r), "timed out") {
		t.Fatal("deadline not enforced")
	}
	code, v := tab.req(t, s, "GET", "/v1/state", nil)
	if code != 200 || len(v["commands"].([]any)) != 0 || v["connection"].(map[string]any)["owner"] == nil {
		t.Fatal("timeout unpaired or retained command")
	}
}

func TestBodyIdentityAndScopedStatus(t *testing.T) {
	d, s := setup(t)
	a := connect(t, s, "a", "edit_a")
	b := connect(t, s, "b", "edit_b")
	agent := client(t, d, s, nil)
	selectTab(t, agent, b.id, true)
	for _, path := range []string{"/v1/browser/revoke", "/v1/browser/disconnect"} {
		code, _ := a.req(t, s, "POST", path, map[string]any{"clientId": b.id, "expectedOwnershipRevision": 0})
		if code != 403 {
			t.Fatal("body client identity ignored", path, code)
		}
	}
	code, v := a.req(t, s, "GET", "/v1/status", nil)
	if code != 200 || v["connection"].(map[string]any)["id"] != a.id {
		t.Fatal("status not scoped")
	}
	encoded, _ := json.Marshal(v)
	if bytes.Contains(encoded, []byte("edit_b")) || bytes.Contains(encoded, []byte(b.token)) || bytes.Contains(encoded, []byte(agent.ID())) {
		t.Fatal("cross-tab status leak")
	}
	// Unowned revocation is idempotent at the current revision.
	code, v = a.req(t, s, "POST", "/v1/browser/revoke", map[string]any{"expectedOwnershipRevision": 0})
	if code != 200 || v["connection"].(map[string]any)["ownershipRevision"] != float64(0) {
		t.Fatal("unowned revoke changed revision")
	}
}

func TestSingleTabNeverImplicitlySelected(t *testing.T) {
	d, s := setup(t)
	connect(t, s, "only-tab", "edit")
	a := client(t, d, s, nil)
	checkTools(t, a, "")
	list := value(t, call(t, a, "list_connections", nil))["connections"].([]any)
	if len(list) != 1 {
		t.Fatal(list)
	}
	where := value(t, call(t, a, "whereami", nil))
	if where["selectedConnection"] != nil || where["page"] != nil || where["connected"] != false {
		t.Fatal("single tab automatically selected")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.connections["only-tab"].owner != "" {
		t.Fatal("inspection acquired browser")
	}
}

type blockedWriter struct {
	header   http.Header
	started  chan struct{}
	release  chan struct{}
	deadline time.Time
	once     sync.Once
}

func (w *blockedWriter) Header() http.Header                       { return w.header }
func (w *blockedWriter) WriteHeader(int)                           {}
func (w *blockedWriter) SetWriteDeadline(deadline time.Time) error { w.deadline = deadline; return nil }
func (w *blockedWriter) Write(b []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(b), nil
}

func TestSlowReaderDoesNotHoldRegistryLock(t *testing.T) {
	d, s := setup(t)
	var elapsed atomic.Int64
	base := time.Now()
	d.now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
	a := connect(t, s, "a", "edit")
	b := connect(t, s, "b", "edit")
	agent := client(t, d, s, nil)
	selectTab(t, agent, a.id, true)
	writer := &blockedWriter{header: make(http.Header), started: make(chan struct{}), release: make(chan struct{})}
	req, _ := http.NewRequest("GET", s.URL+"/health", nil)
	req.Header.Set("Authorization", "Bearer "+d.AgentToken)
	done := make(chan struct{})
	go func() { d.Handler().ServeHTTP(writer, req); close(done) }()
	t.Cleanup(func() {
		close(writer.release)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("blocked writer did not finish")
		}
	})
	select {
	case <-writer.started:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not start")
	}
	if writer.deadline.IsZero() {
		t.Fatal("non-MCP response lacks write deadline")
	}
	// Both independent tab work and ownership changes must run while one network
	// writer remains blocked. The fake clock also proves housekeeping can acquire mu.
	finished := make(chan error, 1)
	go func() {
		for _, route := range []struct {
			tab  *tab
			path string
			body any
		}{{b, "/v1/browser", b.hb}, {a, "/v1/browser/revoke", map[string]any{"expectedOwnershipRevision": 1}}} {
			encoded, _ := json.Marshal(route.body)
			r, _ := http.NewRequest("POST", s.URL+route.path, bytes.NewReader(encoded))
			r.Header.Set("Authorization", "Bearer "+route.tab.token)
			r.Header.Set("Origin", route.tab.origin)
			r.Header.Set("X-Chronicle-Client", route.tab.id)
			response, err := http.DefaultClient.Do(r)
			if err != nil {
				finished <- err
				return
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 200 {
				finished <- fmt.Errorf("route returned %d", response.StatusCode)
				return
			}
		}
		elapsed.Store(int64(11 * time.Second))
		d.mu.Lock()
		d.expireLocked()
		count := len(d.connections)
		d.mu.Unlock()
		if count != 0 {
			finished <- fmt.Errorf("expiry retained %d connections", count)
			return
		}
		finished <- nil
	}()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("slow reader blocked registry operations")
	}
}

func TestBootstrapCapacityIsBoundedAndExpires(t *testing.T) {
	d, s := setup(t)
	var elapsed atomic.Int64
	base := time.Now()
	d.now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
	for i := 0; i < MaxConnections; i++ {
		code, _ := request(t, s, "", "", "POST", "/v1/connect", "http://localhost:4000", map[string]any{"protocol": Protocol, "clientId": fmt.Sprintf("id-%d", i)})
		if code != 200 {
			t.Fatalf("allocation %d returned %d", i, code)
		}
	}
	code, _ := request(t, s, "", "", "POST", "/v1/connect", "http://localhost:4000", map[string]any{"protocol": Protocol, "clientId": "overflow"})
	if code != 503 {
		t.Fatalf("unbounded registry: %d", code)
	}
	elapsed.Store(int64(browserLease))
	code, _ = request(t, s, "", "", "POST", "/v1/connect", "http://localhost:4000", map[string]any{"protocol": Protocol, "clientId": "after-expiry"})
	if code != 200 {
		t.Fatal("expired allocation retained capacity")
	}
}
