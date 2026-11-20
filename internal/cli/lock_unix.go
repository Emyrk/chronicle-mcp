//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Keep the lock inode in place after release; unlinking allows two owners to
// lock different inodes with the same pathname.
func lockState(dir string) (func(), error) {
	path := filepath.Join(dir, ".daemon.lock")
	if err := noSymlinks(path); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("state directory is owned by another daemon: %w", err)
	}
	return func() { _ = syscall.Flock(fd, syscall.LOCK_UN); _ = f.Close() }, nil
}
