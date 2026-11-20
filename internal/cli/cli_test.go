package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Emyrk/chronicle-mcp/internal/relay"
)

// The start command executes this test executable as a daemon child.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		if err := Run(context.Background(), os.Args[1:], io.Discard, os.Stderr); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func fixture() state {
	return state{Port: 17643, Token: "browser-secret", AgentToken: "agent-secret", Version: relay.Version, Origin: "https://chronicleclassic.com", IdleTimeout: 6 * time.Hour, AgentLease: 5 * time.Minute}
}
func TestPrivateState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s := fixture()
	if err := writeState(dir, s); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{dir: 0700, filepath.Join(dir, "dev.json"): 0600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("mode = %o", info.Mode().Perm())
		}
	}
	got, err := readState(dir)
	if err != nil || got != s {
		t.Fatalf("state roundtrip failed: %v", err)
	}
	removeOwnState(dir, "another token")
	if _, err := readState(dir); err != nil {
		t.Fatal("removed another daemon state")
	}
	removeOwnState(dir, s.Token)
	if _, err := os.Stat(filepath.Join(dir, "dev.json")); !os.IsNotExist(err) {
		t.Fatal("own state not removed")
	}
}
func TestStateSymlinksAndPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permission and symlink semantics")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "real")
	if err := writeState(dir, fixture()); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readState(link); err == nil {
		t.Fatal("read accepted directory symlink")
	}
	if err := writeState(link, fixture()); err == nil {
		t.Fatal("write accepted directory symlink")
	}
	path := filepath.Join(dir, "dev.json")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readState(dir); err == nil {
		t.Fatal("read accepted public state")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "target"), path); err != nil {
		t.Fatal(err)
	}
	if err := writeState(dir, fixture()); err == nil {
		t.Fatal("write accepted file symlink")
	}
}
func TestAuthHeadersReadOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"auth", "headers", "--state-dir", dir}, &out, io.Discard); err == nil {
		t.Fatal("missing state accepted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("auth created state directory")
	}
	if err := writeState(dir, fixture()); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"auth", "headers", "--state-dir", dir}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if out.String() != "{\"Authorization\":\"Bearer agent-secret\"}\n" {
		t.Fatal("incorrect auth headers output")
	}
}
func TestHealthIdentityAndShutdown(t *testing.T) {
	for _, tc := range []struct {
		name             string
		status           int
		service, version string
		protocol         int
		wantOK           bool
	}{
		{"valid", 200, "chronicle", relay.Version, 2, true}, {"unauthorized", 401, "chronicle", relay.Version, 2, false}, {"unrelated", 200, "other", relay.Version, 2, false}, {"version", 200, "chronicle", "old", 2, false}, {"protocol", 200, "chronicle", relay.Version, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture()
			shutdowns := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+s.AgentToken {
					t.Error("missing agent authorization")
				}
				if r.URL.Path == "/shutdown" {
					shutdowns++
					var body struct {
						Force bool `json:"force"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					if !body.Force {
						w.WriteHeader(409)
					}
					return
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(health{Service: tc.service, Version: tc.version, Protocol: tc.protocol, Origin: s.Origin, IdleTimeout: s.IdleTimeout.String(), AgentLease: s.AgentLease.String(), Inspection: relay.Inspection{
					Browser:   relay.BrowserInspection{Connected: true, Page: &relay.PageInspection{URL: "https://chronicleclassic.com/workshop"}},
					Ownership: relay.OwnershipInspection{Busy: true, Client: &relay.ClientIdentity{Name: "test-agent", Version: "1"}},
					Idle:      relay.IdleInspection{Disabled: true},
				}})
			}))
			defer server.Close()
			s.Port = server.Listener.Addr().(*net.TCPAddr).Port
			dir := filepath.Join(t.TempDir(), "state")
			if err := writeState(dir, s); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := Run(context.Background(), []string{"status", "--state-dir", dir}, &out, io.Discard)
			if (err == nil) != tc.wantOK {
				t.Fatalf("status error: %v", err)
			}
			if tc.wantOK {
				var inspection relay.Inspection
				if err := json.Unmarshal(out.Bytes(), &inspection); err != nil {
					t.Fatal(err)
				}
				if !inspection.Browser.Connected || inspection.Browser.Page == nil || inspection.Browser.Page.URL != "https://chronicleclassic.com/workshop" {
					t.Fatalf("status lost browser metadata: %+v", inspection.Browser)
				}
				if !inspection.Ownership.Busy || inspection.Ownership.Client == nil || inspection.Ownership.Client.Name != "test-agent" || inspection.Ownership.Client.Version != "1" {
					t.Fatalf("status lost ownership metadata: %+v", inspection.Ownership)
				}
				if !inspection.Idle.Disabled {
					t.Fatal("status lost idle metadata")
				}
			}
			if strings.Contains(out.String(), "secret") {
				t.Fatal("status leaked credentials")
			}
			err = Run(context.Background(), []string{"stop", "--state-dir", dir}, io.Discard, io.Discard)
			if err == nil {
				t.Fatal("busy or invalid daemon stopped")
			}
			err = Run(context.Background(), []string{"stop", "--force", "--state-dir", dir}, io.Discard, io.Discard)
			if (err == nil) != tc.wantOK {
				t.Fatalf("forced stop error: %v", err)
			}
			if !tc.wantOK && shutdowns != 0 {
				t.Fatal("shutdown sent to invalid daemon")
			}
		})
	}
}
func TestDetachedLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("detached lifecycle platform")
	}
	dir := filepath.Join(t.TempDir(), "state")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args := []string{"start", "--port", "0", "--state-dir", dir, "--idle-timeout", "0"}
	if err := Run(ctx, args, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	// Register shutdown immediately so every assertion path cleans up the child.
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = Run(cleanupCtx, []string{"stop", "--force", "--state-dir", dir}, io.Discard, io.Discard)
	})
	first, err := readState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, args, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	second, err := readState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.Token != second.Token {
		t.Fatal("start did not reuse daemon")
	}
	bad := append(append([]string{}, args...), "--origin", "https://example.com")
	if err := Run(ctx, bad, io.Discard, io.Discard); err == nil {
		t.Fatal("reused incompatible config")
	}
	if err := Run(ctx, []string{"stop", "--state-dir", dir}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "dev.json")); os.IsNotExist(err) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("state cleanup timed out")
		case <-ticker.C:
		}
	}
}

func TestServeRefusesOccupiedPort(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	dir := filepath.Join(t.TempDir(), "state")
	o := options{port: listener.Addr().(*net.TCPAddr).Port, dir: dir, origin: fixture().Origin, idle: 0, lease: 5 * time.Minute}
	if err := serve(context.Background(), o); err == nil {
		t.Fatal("occupied port accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "dev.json")); !os.IsNotExist(err) {
		t.Fatal("published credentials without owning listener")
	}
}
func TestStateExclusiveLock(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("supported lock platforms")
	}
	dir := t.TempDir()
	unlock, err := lockState(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	other, err := lockState(dir)
	if err == nil {
		other()
		t.Fatal("second daemon acquired state ownership")
	}
}
func TestHealthRedirectDoesNotForwardCredentials(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed redirect with private credentials") }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	s := fixture()
	s.Port = server.Listener.Addr().(*net.TCPAddr).Port
	if _, err := checkHealth(context.Background(), s); err == nil {
		t.Fatal("redirect accepted")
	}
}

func TestConcurrentStartReusesWinner(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("detached lifecycle platform")
	}
	dir := filepath.Join(t.TempDir(), "state")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// All children compete for one state lock, with OS-assigned ports. No
	// reserve-and-close port race or fixed startup sleeps are needed.
	const starters = 6
	ready := make(chan struct{})
	results := make(chan error, starters)
	for i := 0; i < starters; i++ {
		go func() {
			<-ready
			results <- Run(ctx, []string{"start", "--port", "0", "--state-dir", dir, "--idle-timeout", "0"}, io.Discard, io.Discard)
		}()
	}
	close(ready)
	for i := 0; i < starters; i++ {
		if err := <-results; err != nil {
			t.Errorf("concurrent start: %v", err)
		}
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cleanupCancel()
	if err := Run(cleanupCtx, []string{"stop", "--force", "--state-dir", dir}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "dev.json")); os.IsNotExist(err) {
			break
		}
		select {
		case <-cleanupCtx.Done():
			t.Fatal("concurrent winner cleanup timed out")
		case <-ticker.C:
		}
	}
}
