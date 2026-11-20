package relay

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

func exactOrigin(s string) bool {
	u, e := url.Parse(s)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && s == u.Scheme+"://"+u.Host && !regexp.MustCompile(`\*`).MatchString(s)
}

// Trust only HTTPS Chronicle DNS names, not substring matches or arbitrary ports.
var chronicleHost = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*chronicleclassic\.com$`)

func (d *Daemon) allowed(s string) bool {
	if s == "http://localhost:4000" || s == "http://localhost:5175" || s == d.Config.Origin {
		return true
	}
	if !exactOrigin(s) {
		return false
	}
	u, _ := url.Parse(s)
	return u.Scheme == "https" && (u.Port() == "" || u.Port() == "443") && chronicleHost.MatchString(strings.ToLower(u.Hostname()))
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
		if !bounded(t.Name, 128) || !namePattern.MatchString(t.Name) || reserved(t.Name) || names[t.Name] || len(t.Description) > 8192 || t.InputSchema["type"] != "object" || (t.Effect != "read" && t.Effect != "view_mutation") || (t.Confirmation != "never" && t.Confirmation != "unless_yolo") {
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
