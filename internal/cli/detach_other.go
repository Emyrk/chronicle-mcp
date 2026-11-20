//go:build !unix

package cli

import (
	"errors"
	"os/exec"
)

// Non-Unix callers can supervise `serve` themselves. Do not pretend that a
// child tied to a console/session has been safely detached.
func detach(cmd *exec.Cmd) error {
	return errors.New("detached start is not supported on this platform; use serve under a process supervisor")
}

func connectionRefused(err error) bool { return false }
