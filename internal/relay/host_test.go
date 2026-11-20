package relay

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoopbackHostAliases(t *testing.T) {
	d, _ := setup(t)
	for _, name := range []string{"localhost", "127.0.0.1"} {
		host := fmt.Sprintf("%s:%d", name, d.Config.Port)
		for _, path := range []string{"/", "/dashboard/app.js", "/dashboard/api/status", "/healthz"} {
			r := httptest.NewRequest("GET", "http://"+host+path, nil)
			w := httptest.NewRecorder()
			d.Handler().ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("%s %s: %d", host, path, w.Code)
			}
		}
		// Host aliases must not bypass the management or browser API credentials.
		for _, path := range []string{"/health", "/v1/state"} {
			r := httptest.NewRequest("GET", "http://"+host+path, nil)
			w := httptest.NewRecorder()
			d.Handler().ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatalf("unauthenticated %s: %d", path, w.Code)
			}
		}
		for _, originHost := range []string{"localhost", "127.0.0.1", "evil.test"} {
			origin := fmt.Sprintf("http://%s:%d", originHost, d.Config.Port)
			r := httptest.NewRequest("POST", "http://"+host+"/dashboard/api/revoke", strings.NewReader(`{"connectionId":"missing","expectedOwnershipRevision":0}`))
			r.Header.Set("Origin", origin)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Chronicle-Dashboard", "1")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			w := httptest.NewRecorder()
			d.Handler().ServeHTTP(w, r)
			want := 403
			if originHost == name {
				want = 404
			}
			if w.Code != want {
				t.Fatalf("host %s origin %s: got %d want %d: %s", host, origin, w.Code, want, w.Body.String())
			}
		}
	}
	for _, host := range []string{"localhost", "localhost:0", "localhost.evil.test", "evil.test", "127.0.0.2", "[::1]", "localhost."} {
		if !strings.Contains(host, ":") {
			host = fmt.Sprintf("%s:%d", host, d.Config.Port)
		}
		if host == fmt.Sprintf("localhost:%d", d.Config.Port) {
			host = "localhost"
		}
		for _, path := range []string{"/", "/healthz"} {
			r := httptest.NewRequest("GET", "http://127.0.0.1/", nil)
			r.URL.Path = path
			r.Host = host
			w := httptest.NewRecorder()
			d.Handler().ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("unexpected Host %s accepted at %s: %d", host, path, w.Code)
			}
		}
	}
}
