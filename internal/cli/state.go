package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type state struct {
	Port        int           `json:"port"`
	Token       string        `json:"token"`
	AgentToken  string        `json:"agentToken"`
	Version     string        `json:"version"`
	PID         int           `json:"pid,omitempty"`
	Origin      string        `json:"origin"`
	IdleTimeout time.Duration `json:"idleTimeout"`
	AgentLease  time.Duration `json:"agentLease"`
}

// Check every existing component, not only the final file: credentials must
// never be read or written through a symlink supplied as a state directory.
func noSymlinks(path string) error {
	p, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for {
		info, err := os.Lstat(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink in state path: %s", p)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil
		}
		p = parent
	}
}
func privateDir(dir string, create bool) error {
	if err := noSymlinks(dir); err != nil {
		return err
	}
	if create {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("state path is not a directory")
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("state directory %q must have mode 0700", dir)
	}
	return nil
}
func readState(dir string) (state, error) {
	var s state
	if err := privateDir(dir, false); err != nil {
		return s, err
	}
	p := filepath.Join(dir, "dev.json")
	if err := noSymlinks(p); err != nil {
		return s, err
	}
	f, err := os.Open(p)
	if err != nil {
		return s, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return s, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return s, errors.New("state file must be a regular file with mode 0600")
	}
	if err = json.NewDecoder(f).Decode(&s); err != nil {
		return s, errors.New("invalid state JSON")
	}
	if s.Port < 1 || s.Port > 65535 || s.Token == "" || s.AgentToken == "" {
		return s, errors.New("invalid state credentials or port")
	}
	return s, nil
}
func writeState(dir string, s state) error {
	if err := privateDir(dir, true); err != nil {
		return err
	}
	p := filepath.Join(dir, "dev.json")
	if err := noSymlinks(p); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".dev-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if err = json.NewEncoder(f).Encode(s); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}
func removeOwnState(dir, token string) {
	s, err := readState(dir)
	if err == nil && s.Token == token {
		_ = os.Remove(filepath.Join(dir, "dev.json"))
	}
}
