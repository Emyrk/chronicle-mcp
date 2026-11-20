package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func dashboardRequest(t *testing.T, s *httptest.Server, method, path string, body any, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	r, _ := http.NewRequest(method, s.URL+path, bytes.NewReader(b))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	response, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	data, e := io.ReadAll(response.Body)
	if e != nil {
		t.Fatal(e)
	}
	return response.StatusCode, response.Header, data
}
func mutationHeaders(s *httptest.Server) map[string]string {
	return map[string]string{"Origin": s.URL, "Sec-Fetch-Site": "same-origin", "Content-Type": "application/json", "X-Chronicle-Dashboard": "1"}
}
func dashboardJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var data map[string]any
	if e := json.Unmarshal(b, &data); e != nil {
		t.Fatal(e)
	}
	return data
}

func TestDashboardReadSecurity(t *testing.T) {
	d, s := setup(t)
	_ = d
	for _, path := range []string{"/", "/dashboard/app.js", "/dashboard/style.css", "/dashboard/logo.svg", "/dashboard/inter-400.woff2", "/dashboard/inter-600.woff2", "/dashboard/Inter-LICENSE.txt", "/dashboard/api/status"} {
		for _, headers := range []map[string]string{nil, {"Sec-Fetch-Site": "none", "Sec-Fetch-Mode": "navigate"}, {"Sec-Fetch-Site": "same-origin"}, {"Origin": s.URL, "Sec-Fetch-Site": "same-origin"}} {
			code, h, b := dashboardRequest(t, s, "GET", path, nil, headers)
			if code != 200 || len(b) == 0 {
				t.Fatalf("local dashboard read %s: %d", path, code)
			}
			for _, key := range []string{"Content-Security-Policy", "Referrer-Policy", "X-Content-Type-Options", "X-Frame-Options", "Cross-Origin-Resource-Policy", "Cross-Origin-Opener-Policy"} {
				if h.Get(key) == "" {
					t.Fatal("missing protection", key)
				}
			}
			if h.Get("Cache-Control") != "no-store" || h.Get("Access-Control-Allow-Origin") != "" || !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
				t.Fatal("dashboard inherited API CORS or unsafe cache/frame policy")
			}
		}
		for _, headers := range []map[string]string{{"Origin": "null"}, {"Origin": ""}, {"Origin": "https://evil.test"}, {"Origin": "https://chronicleclassic.com"}, {"Origin": "http://localhost:4000"}, {"Origin": s.URL + "/"}, {"Origin": "http://localhost:" + strings.Split(s.URL, ":")[2]}, {"Sec-Fetch-Site": "cross-site"}, {"Sec-Fetch-Site": "same-site"}, {"Origin": s.URL, "Sec-Fetch-Site": "cross-site"}, {"Sec-Fetch-Site": "unexpected"}} {
			code, h, _ := dashboardRequest(t, s, "GET", path, nil, headers)
			if code != 403 || h.Get("Access-Control-Allow-Origin") != "" {
				t.Fatalf("dashboard cross-origin read %s %v =>%d", path, headers, code)
			}
		}
	}
	req, _ := http.NewRequest("GET", s.URL+"/", nil)
	req.Host = "localhost.evil.test:" + strings.Split(s.URL, ":")[2]
	response, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("nonexact dashboard Host accepted")
	}
	for _, origin := range []string{s.URL, "https://chronicleclassic.com", "null"} {
		code, h, _ := dashboardRequest(t, s, "OPTIONS", "/dashboard/api/revoke", nil, map[string]string{"Origin": origin, "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "X-Chronicle-Dashboard"})
		if code == 200 || code == 204 || h.Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("dashboard preflight enabled remote mutation")
		}
	}
	// Existing management and MCP authentication remain mandatory.
	for _, path := range []string{"/health", "/shutdown", "/mcp"} {
		code, _, _ := dashboardRequest(t, s, "GET", path, nil, nil)
		if code != 401 {
			t.Fatal("dashboard weakened authenticated endpoint", path, code)
		}
	}
}

func TestDashboardMutationSecurity(t *testing.T) {
	d, s := setup(t)
	tab := connect(t, s, "tab", "edit")
	agent := client(t, d, s, nil)
	selectTab(t, agent, tab.id, true)
	body := map[string]any{"connectionId": tab.id, "expectedOwnershipRevision": 1}
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"no-origin", map[string]string{"Content-Type": "application/json", "X-Chronicle-Dashboard": "1"}, 403},
		{"no-header", map[string]string{"Origin": s.URL, "Content-Type": "application/json"}, 403},
		{"wrong-header", map[string]string{"Origin": s.URL, "Content-Type": "application/json", "X-Chronicle-Dashboard": "0"}, 403},
		{"form", map[string]string{"Origin": s.URL, "Content-Type": "application/x-www-form-urlencoded", "X-Chronicle-Dashboard": "1"}, 415},
		{"text", map[string]string{"Origin": s.URL, "Content-Type": "text/plain", "X-Chronicle-Dashboard": "1"}, 415},
		{"null", map[string]string{"Origin": "null", "Content-Type": "application/json", "X-Chronicle-Dashboard": "1"}, 403},
		{"chronicle", map[string]string{"Origin": "https://chronicleclassic.com", "Content-Type": "application/json", "X-Chronicle-Dashboard": "1"}, 403},
		{"fetch-cross-site", map[string]string{"Origin": s.URL, "Sec-Fetch-Site": "cross-site", "Content-Type": "application/json", "X-Chronicle-Dashboard": "1"}, 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/dashboard/api/revoke", "/dashboard/api/shutdown"} {
				code, h, _ := dashboardRequest(t, s, "POST", path, body, tc.headers)
				if code != tc.want || h.Get("Access-Control-Allow-Origin") != "" {
					t.Fatalf("%s returned%d want%d", path, code, tc.want)
				}
			}
		})
	}
	for _, method := range []string{"GET", "PUT", "DELETE", "PATCH"} {
		code, _, _ := dashboardRequest(t, s, method, "/dashboard/api/revoke", body, mutationHeaders(s))
		if code != 404 && code != 405 {
			t.Fatal("non-POST mutation accepted", method, code)
		}
	}
	d.mu.Lock()
	owner := d.connections[tab.id].owner
	d.mu.Unlock()
	if owner != agent.ID() {
		t.Fatal("rejected mutation changed ownership")
	}
	code, _, _ := dashboardRequest(t, s, "POST", "/dashboard/api/revoke", strings.Repeat("x", bodyLimit), mutationHeaders(s))
	if code != 413 {
		t.Fatal("dashboard body limit missing")
	}
	for _, invalid := range []any{map[string]any{}, map[string]any{"connectionId": tab.id, "expectedOwnershipRevision": -1}, map[string]any{"connectionId": tab.id, "expectedOwnershipRevision": 1.5}, map[string]any{"connectionId": tab.id, "expectedOwnershipRevision": float64(maxRevision) + 1}} {
		code, _, _ := dashboardRequest(t, s, "POST", "/dashboard/api/revoke", invalid, mutationHeaders(s))
		if code != 400 {
			t.Fatal("invalid revoke accepted", invalid, code)
		}
	}
}

func TestDashboardStatusPrivacyAndIdle(t *testing.T) {
	d, s := setup(t)
	var elapsed atomic.Int64
	base := time.Now()
	d.now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
	d.startedAt = base
	d.lastActivity = base
	tab := connect(t, s, "tab", "edit")
	a := client(t, d, s, nil)
	selectTab(t, a, tab.id, true)
	callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch := callAsync(a, callCtx, "edit")
	cmd := pending(t, d, tab.id)
	elapsed.Store(int64(3 * time.Second))
	code, h, b := dashboardRequest(t, s, "GET", "/dashboard/api/status", nil, nil)
	if code != 200 || h.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal(code)
	}
	status := dashboardJSON(t, b)
	if status["uptime"] != "3s" || status["version"] != Version || status["pending"] != float64(1) {
		t.Fatal(status)
	}
	for _, secret := range []string{d.Token, d.AgentToken, tab.token, a.ID(), `"sessionId"`, `"generation"`, `"arguments"`, `"capability"`, `"tools"`, cmd.ID} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("dashboard leaked private state")
		}
	}
	d.mu.Lock()
	if !d.lastActivity.Equal(base) {
		t.Fatal("dashboard renewed idle activity")
	}
	d.mu.Unlock()
	tab.result(t, s, cmd, cmd.OwnershipRevision, "success")
	awaitResult(t, ch)
	if e := a.Close(); e != nil {
		t.Fatal(e)
	}
	elapsed.Store(int64(7 * time.Hour))
	dashboardRequest(t, s, "GET", "/dashboard/api/status", nil, nil)
	d.mu.Lock()
	idle := d.idleLocked()
	d.mu.Unlock()
	if !idle {
		t.Fatal("dashboard kept empty daemon alive")
	}
}

func TestDashboardRevokeAndShutdownLifecycle(t *testing.T) {
	d, s := setup(t)
	aTab := connect(t, s, "a", "edit")
	bTab := connect(t, s, "b", "edit")
	a := client(t, d, s, nil)
	b := client(t, d, s, nil)
	selectTab(t, a, aTab.id, true)
	selectTab(t, b, bTab.id, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ac := callAsync(a, ctx, "edit")
	bc := callAsync(b, ctx, "edit")
	old := pending(t, d, aTab.id)
	other := pending(t, d, bTab.id)
	code, _, raw := dashboardRequest(t, s, "POST", "/dashboard/api/revoke", map[string]any{"connectionId": aTab.id, "expectedOwnershipRevision": old.OwnershipRevision}, mutationHeaders(s))
	if code != 200 {
		t.Fatal(code, string(raw))
	}
	response := dashboardJSON(t, raw)
	if response["connection"].(map[string]any)["owner"] != nil {
		t.Fatal("revoke retained owner")
	}
	if !awaitResult(t, ac).IsError {
		t.Fatal("revocation did not cancel pending approval")
	}
	checkTools(t, a, "")
	if aTab.result(t, s, old, old.OwnershipRevision, "success") {
		t.Fatal("revoked result accepted")
	}
	selectTab(t, a, aTab.id, true)
	code, _, raw = dashboardRequest(t, s, "POST", "/dashboard/api/revoke", map[string]any{"connectionId": aTab.id, "expectedOwnershipRevision": old.OwnershipRevision}, mutationHeaders(s))
	if code != 409 || dashboardJSON(t, raw)["connection"].(map[string]any)["ownershipRevision"] != float64(3) {
		t.Fatal("stale revoke evicted new owner")
	}
	if !bTab.result(t, s, other, other.OwnershipRevision, "success") {
		t.Fatal("revoke affected other tab")
	}
	awaitResult(t, bc)
	code, _, _ = dashboardRequest(t, s, "POST", "/dashboard/api/shutdown", map[string]any{}, mutationHeaders(s))
	if code != 400 {
		t.Fatal("missing explicit force accepted")
	}
	code, _, _ = dashboardRequest(t, s, "POST", "/dashboard/api/shutdown", map[string]any{"force": false}, mutationHeaders(s))
	if code != 409 {
		t.Fatal("active unforced shutdown accepted")
	}
	select {
	case <-d.shutdown:
		t.Fatal("failed shutdown stopped daemon")
	default:
	}
	code, _, _ = dashboardRequest(t, s, "POST", "/dashboard/api/shutdown", map[string]any{"force": true}, mutationHeaders(s))
	if code != 200 {
		t.Fatal("explicit forced shutdown rejected")
	}
	select {
	case <-d.shutdown:
	default:
		t.Fatal("shutdown not signaled")
	}
	d.mu.Lock()
	if d.connections[aTab.id].page == nil || d.connections[bTab.id].page == nil {
		t.Fatal("control discarded browser metadata")
	}
	d.mu.Unlock()
}

func TestDashboardSlowWriterDoesNotBlockRegistry(t *testing.T) {
	d, s := setup(t)
	tab := connect(t, s, "tab", "edit")
	writer := &blockedWriter{header: make(http.Header), started: make(chan struct{}), release: make(chan struct{})}
	req, _ := http.NewRequest("GET", s.URL+"/dashboard/api/status", nil)
	done := make(chan struct{})
	go func() { d.Handler().ServeHTTP(writer, req); close(done) }()
	t.Cleanup(func() {
		close(writer.release)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("writer cleanup timed out")
		}
	})
	select {
	case <-writer.started:
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot writer did not start")
	}
	if writer.deadline.IsZero() {
		t.Fatal("dashboard writer deadline missing")
	}
	finished := make(chan error, 1)
	go func() {
		r, _ := http.NewRequest("GET", s.URL+"/v1/state", nil)
		r.Header.Set("Origin", tab.origin)
		r.Header.Set("Authorization", "Bearer "+tab.token)
		r.Header.Set("X-Chronicle-Client", tab.id)
		response, e := http.DefaultClient.Do(r)
		if e != nil {
			finished <- e
			return
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			finished <- fmt.Errorf("poll returned %d", response.StatusCode)
			return
		}
		finished <- nil
	}()
	select {
	case e := <-finished:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dashboard response held registry mutex")
	}
}

func TestDashboardAssetsUseSafeDOMAndBoundedPolling(t *testing.T) {
	js, e := dashboardAssets.ReadFile("dashboard/app.js")
	if e != nil {
		t.Fatal(e)
	}
	source := string(js)
	for _, unsafe := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "eval("} {
		if strings.Contains(source, unsafe) {
			t.Fatal("unsafe DOM sink", unsafe)
		}
	}
	for _, expected := range []string{"textContent", "noopener noreferrer", "['http:', 'https:']", "failures >= 4", "expectedOwnershipRevision", "force: active", "X-Chronicle-Dashboard"} {
		if !strings.Contains(source, expected) {
			t.Fatal("missing UI safeguard", expected)
		}
	}
	html, e := dashboardAssets.ReadFile("dashboard/index.html")
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(html, []byte("onclick=")) || bytes.Contains(html, []byte("<script>")) {
		t.Fatal("inline script violates CSP")
	}
}
