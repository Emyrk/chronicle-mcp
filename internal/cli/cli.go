// Package cli implements the Chronicle daemon's local lifecycle commands.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Emyrk/chronicle-mcp/internal/relay"
)

type options struct {
	port        int
	dir, origin string
	idle, lease time.Duration
	force       bool
}
type health struct {
	relay.Inspection
	Service     string `json:"service"`
	Version     string `json:"version"`
	Protocol    int    `json:"protocol"`
	Agents      int    `json:"agents"`
	Pending     int    `json:"pending"`
	Origin      string `json:"origin"`
	IdleTimeout string `json:"idleTimeout"`
	AgentLease  string `json:"agentLease"`
}

// Run never writes credentials except for the explicit auth headers command.
func Run(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: chronicle serve|start|status|stop|auth headers [flags]")
	}
	command := args[0]
	args = args[1:]
	if command == "auth" {
		if len(args) == 0 || args[0] != "headers" {
			return errors.New("usage: chronicle auth headers [--state-dir directory]")
		}
		args = args[1:]
	}
	switch command {
	case "serve", "start", "status", "stop", "auth":
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	o := options{}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.IntVar(&o.port, "port", 17643, "loopback listener port (0 selects an ephemeral port)")
	fs.StringVar(&o.dir, "state-dir", filepath.Join(base, "chronicle"), "private state directory")
	fs.StringVar(&o.origin, "origin", "https://chronicleclassic.com", "allowed browser origin")
	fs.DurationVar(&o.idle, "idle-timeout", 6*time.Hour, "idle shutdown timeout (0 disables)")
	fs.DurationVar(&o.lease, "agent-lease", 5*time.Minute, "agent lease duration")
	if command == "stop" {
		fs.BoolVar(&o.force, "force", false, "stop even with active agents or pending calls")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if o.port < 0 || o.port > 65535 || o.idle < 0 || o.lease <= 0 || o.lease > 24*time.Hour {
		return errors.New("invalid port or duration")
	}
	switch command {
	case "serve":
		return serve(ctx, o)
	case "start":
		return start(ctx, o, out)
	case "auth":
		s, err := readState(o.dir)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]string{"Authorization": "Bearer " + s.AgentToken})
	case "status", "stop":
		s, err := readState(o.dir)
		if err != nil {
			return fmt.Errorf("read daemon state: %w", err)
		}
		h, err := checkHealth(ctx, s)
		if err != nil {
			return err
		}
		if command == "status" {
			return json.NewEncoder(out).Encode(struct {
				health
				Port int `json:"port"`
			}{h, s.Port})
		}
		body, _ := json.Marshal(map[string]bool{"force": o.force})
		resp, err := request(ctx, s, http.MethodPost, "/shutdown", body)
		if err != nil {
			return fmt.Errorf("shutdown request failed: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusConflict {
			return errors.New("daemon has active agents or pending calls; use stop --force to override")
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("shutdown rejected (HTTP %d)", resp.StatusCode)
		}
		_, err = fmt.Fprintln(out, "Shutdown requested")
		return err
	}
	return nil
}

func request(ctx context.Context, s state, method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:"+strconv.Itoa(s.Port)+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.AgentToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Disable proxies and redirects: local credentials must not leave this origin.
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := http.Client{Timeout: time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client.Do(req)
}
func checkHealth(ctx context.Context, s state) (health, error) {
	var h health
	resp, err := request(ctx, s, http.MethodGet, "/health", nil)
	if err != nil {
		return h, fmt.Errorf("daemon unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return h, fmt.Errorf("daemon authentication/health rejected (HTTP %d); refusing reuse", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&h); err != nil {
		return h, errors.New("invalid daemon health response")
	}
	if h.Service != "chronicle" || h.Version != relay.Version || h.Protocol != 2 || s.Version != relay.Version {
		return h, errors.New("incompatible daemon identity, version, or protocol; refusing reuse")
	}
	if h.Origin != s.Origin || h.IdleTimeout != s.IdleTimeout.String() || h.AgentLease != s.AgentLease.String() {
		return h, errors.New("daemon configuration does not match private state; refusing reuse")
	}
	return h, nil
}
func sameConfig(s state, o options) bool {
	return (o.port == 0 || s.Port == o.port) && s.Origin == o.origin && s.IdleTimeout == o.idle && s.AgentLease == o.lease
}

func serve(ctx context.Context, o options) error {
	if err := privateDir(o.dir, true); err != nil {
		return err
	}
	unlock, err := lockState(o.dir)
	if err != nil {
		return err
	}
	defer unlock()
	// A state directory belongs to a single live daemon, even when ports differ.
	if s, err := readState(o.dir); err == nil {
		if _, err := checkHealth(ctx, s); err == nil {
			return errors.New("a daemon already owns this state directory")
		} else if !connectionRefused(err) {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(o.port)))
	if err != nil {
		return fmt.Errorf("listen: %w (refusing to terminate any existing listener)", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	d, err := relay.New(relay.Config{Port: port, Origin: o.origin, IdleTimeout: o.idle, AgentLease: o.lease})
	if err != nil {
		return err
	}
	s := state{Port: port, Token: d.Token, AgentToken: d.AgentToken, Version: relay.Version, PID: os.Getpid(), Origin: o.origin, IdleTimeout: o.idle, AgentLease: o.lease}
	if err := writeState(o.dir, s); err != nil {
		return err
	}
	defer removeOwnState(o.dir, s.Token)
	return d.Serve(ctx, listener)
}

func start(ctx context.Context, o options, out io.Writer) error {
	if err := privateDir(o.dir, true); err != nil {
		return err
	}
	if s, err := readState(o.dir); err == nil {
		if _, err := checkHealth(ctx, s); err == nil {
			if !sameConfig(s, o) {
				return errors.New("running daemon configuration differs; stop it explicitly before restarting")
			}
			_, err = fmt.Fprintf(out, "Chronicle already running on port %d\n", s.Port)
			return err
		} else {
			// Only a refused connection is evidence of stale state. Timeouts, invalid
			// identities, and auth failures must not trigger a competing daemon.
			if !connectionRefused(err) {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, "serve", "--port", strconv.Itoa(o.port), "--state-dir", o.dir, "--origin", o.origin, "--idle-timeout", o.idle.String(), "--agent-lease", o.lease.String())
	if err = detach(cmd); err != nil {
		return err
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	cmd.Stdin = null
	cmd.Stdout = null
	cmd.Stderr = null
	if err = cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var childExit error
	childExited := false
	for {
		if s, err := readState(o.dir); err == nil {
			if _, err = checkHealth(ctx, s); err == nil {
				if !sameConfig(s, o) {
					return errors.New("running daemon configuration differs; stop it explicitly before restarting")
				}
				message := "Chronicle already running on port %d\n"
				if s.PID == cmd.Process.Pid {
					message = "Chronicle started on port %d\n"
				}
				_, err = fmt.Fprintf(out, message, s.Port)
				return err
			} else {
				var networkError net.Error
				if !errors.As(err, &networkError) {
					return err
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case childExit = <-done:
			// A concurrent start can win the state lock before publishing its
			// credentials. Keep the bounded readiness wait even if our child
			// lost that race, and authenticate whichever daemon becomes ready.
			childExited = true
			done = nil
		case <-deadline.C:
			if childExited {
				return fmt.Errorf("daemon exited before readiness (%v); run chronicle serve with the same flags for diagnostics", childExit)
			}
			return errors.New("daemon did not become ready within 10s; inspect status before retrying (no process was killed)")
		case <-ticker.C:
		}
	}
}
