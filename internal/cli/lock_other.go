//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package cli

import "errors"

func lockState(string) (func(), error) {
	return nil, errors.New("daemon state locking is not supported on this platform")
}
