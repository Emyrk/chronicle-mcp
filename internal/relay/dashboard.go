package relay

import (
	"embed"
	"mime"
	"net/http"
	"strings"
	"time"
)

//go:embed dashboard/index.html dashboard/app.js dashboard/style.css dashboard/logo.svg dashboard/inter-*.woff2 dashboard/Inter-LICENSE.txt
var dashboardAssets embed.FS

func dashboardPath(path string) bool {
	return path == "/" || path == "/dashboard" || strings.HasPrefix(path, "/dashboard/")
}

// The dashboard intentionally has no bearer authentication. Its separate trust
// boundary is the local same-origin browser document, not approved Chronicle
// origins. Never run it through the browser API's permissive bootstrap CORS.
func (d *Daemon) dashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if !d.localHost(r.Host) {
		fail(w, 403, "Invalid Host")
		return
	}
	origins, present := r.Header["Origin"]
	expectedOrigin := "http://" + r.Host
	if present && (len(origins) != 1 || origins[0] != expectedOrigin) {
		fail(w, 403, "Dashboard requires its exact local origin")
		return
	}
	sites, hasSite := r.Header["Sec-Fetch-Site"]
	if hasSite && (len(sites) != 1 || (sites[0] != "same-origin" && sites[0] != "none")) {
		fail(w, 403, "Cross-site dashboard access denied")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		fail(w, 405, "Dashboard supports GET and JSON POST only")
		return
	}
	if r.Method == http.MethodGet {
		assets := map[string]struct{ file, contentType string }{"/": {"index.html", "text/html; charset=utf-8"}, "/dashboard/app.js": {"app.js", "text/javascript; charset=utf-8"}, "/dashboard/style.css": {"style.css", "text/css; charset=utf-8"}, "/dashboard/logo.svg": {"logo.svg", "image/svg+xml"}, "/dashboard/inter-400.woff2": {"inter-400.woff2", "font/woff2"}, "/dashboard/inter-600.woff2": {"inter-600.woff2", "font/woff2"}, "/dashboard/Inter-LICENSE.txt": {"Inter-LICENSE.txt", "text/plain; charset=utf-8"}}
		if asset, ok := assets[r.URL.Path]; ok {
			body, err := dashboardAssets.ReadFile("dashboard/" + asset.file)
			if err != nil {
				fail(w, 500, "Dashboard asset unavailable")
				return
			}
			w.Header().Set("Content-Type", asset.contentType)
			w.WriteHeader(200)
			_, _ = w.Write(body)
			return
		}
		if r.URL.Path != "/dashboard/api/status" {
			fail(w, 404, "Not found")
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		d.expireLocked()
		if d.closed {
			fail(w, 503, "Daemon stopped")
			return
		}
		uptime := d.now().Sub(d.startedAt)
		if uptime < 0 {
			uptime = 0
		}
		respond(w, 200, struct {
			Inspection
			Service  string `json:"service"`
			Version  string `json:"version"`
			Protocol int    `json:"protocol"`
			Uptime   string `json:"uptime"`
			Agents   int    `json:"agents"`
			Pending  int    `json:"pending"`
		}{d.inspectionLocked(), "chronicle", Version, Protocol, uptime.Round(time.Second).String(), len(d.agents), d.pendingLocked()})
		return
	}
	// Same-origin JSON plus a non-simple header is a preflight barrier. This does
	// not authorize remote origins, even if a caller supplies a valid bearer token.
	if !present || r.Header.Get("X-Chronicle-Dashboard") != "1" {
		fail(w, 403, "Dashboard mutation requires exact Origin and X-Chronicle-Dashboard: 1")
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		fail(w, 415, "Dashboard mutations require application/json")
		return
	}
	switch r.URL.Path {
	case "/dashboard/api/revoke":
		var data struct {
			ConnectionID string  `json:"connectionId"`
			Expected     *uint64 `json:"expectedOwnershipRevision"`
		}
		if !decode(w, r, &data) {
			return
		}
		if !bounded(data.ConnectionID, 128) || data.Expected == nil || *data.Expected > maxRevision {
			fail(w, 400, "Expected connectionId and JavaScript-safe expectedOwnershipRevision")
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		d.expireLocked()
		if d.closed {
			fail(w, 503, "Daemon stopped")
			return
		}
		c := d.connections[data.ConnectionID]
		if c == nil || c.page == nil {
			fail(w, 404, "Connection is absent or expired")
			return
		}
		if *data.Expected != c.revision {
			respond(w, 409, map[string]any{"error": "stale_ownership", "message": "Ownership changed. Refresh and confirm again; do not retry automatically.", "connection": d.safeConnectionLocked(c)})
			return
		}
		d.releaseConnectionLocked(c, "ownership_revoked")
		respond(w, 200, map[string]any{"ok": true, "connection": d.safeConnectionLocked(c)})
	case "/dashboard/api/shutdown":
		var data struct {
			Force *bool `json:"force"`
		}
		if !decode(w, r, &data) {
			return
		}
		if data.Force == nil {
			fail(w, 400, "Explicit force boolean required")
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		d.expireLocked()
		if d.closed {
			fail(w, 503, "Daemon stopped")
			return
		}
		if !*data.Force && (len(d.agents) > 0 || d.pendingLocked() > 0) {
			fail(w, 409, "Active agents or pending work. Confirm forced shutdown explicitly.")
			return
		}
		respond(w, 200, map[string]any{"ok": true})
		d.stopOnce.Do(func() { close(d.shutdown) })
	default:
		fail(w, 404, "Not found")
	}
}

// No browser page session IDs, tool schemas, arguments, or credentials in dashboard responses.
func (d *Daemon) safeConnectionLocked(c *connection) ConnectionInspection {
	owner := d.metadataLocked(c).Owner
	availability := "available"
	if owner != nil {
		availability = "busy"
	}
	return ConnectionInspection{ID: c.id, Name: c.name, Page: &PageInspection{URL: c.page.URL}, Owner: owner, OwnershipRevision: c.revision, Availability: availability, Pending: c.pending != nil}
}
