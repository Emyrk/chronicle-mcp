package relay

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestHealthz(t *testing.T) {
	d, server := setup(t)
	before := d.lastActivity
	for _, tc := range []struct {
		name, method, origin, host string
		status                     int
	}{
		{"local", "GET", "", "", 200},
		{"frontend", "GET", "https://octo.chronicleclassic.com", "", 200},
		{"development", "GET", "http://localhost:4000", "", 200},
		{"vite", "GET", "http://localhost:5175", "", 200},
		{"preflight", "OPTIONS", "https://octo.chronicleclassic.com", "", 204},
		{"unapproved", "GET", "https://evil.example", "", 403},
		{"null", "GET", "null", "", 403},
		{"bad host", "GET", "", "evil.example", 403},
		{"localhost", "GET", "", fmt.Sprintf("localhost:%d", d.Config.Port), 200},
		{"mutation", "POST", "", "", 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, fmt.Sprintf("http://127.0.0.1:%d/healthz", d.Config.Port), nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.host != "" {
				r.Host = tc.host
			}
			w := httptest.NewRecorder()
			d.Handler().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if tc.status == 200 {
				var got map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				want := map[string]any{"service": "chronicle", "version": Version, "protocol": float64(Protocol)}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("unexpected public data: %v", got)
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("probe must not be cached")
				}
			}
			if tc.origin != "" && tc.status < 300 && w.Header().Get("Access-Control-Allow-Origin") != tc.origin {
				t.Fatal("missing exact CORS origin")
			}
			if tc.status == 403 && w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("rejected origin allowed")
			}
		})
	}
	if !d.lastActivity.Equal(before) || len(d.connections) != 0 || len(d.agents) != 0 {
		t.Fatal("probe changed lifecycle state")
	}
	code, _ := request(t, server, "", "", "GET", "/health", "", nil)
	if code != 401 {
		t.Fatalf("private health status = %d", code)
	}
}
