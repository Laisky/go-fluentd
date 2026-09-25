//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package otlpstate

import (
	"fmt"
	"os"
	"syscall"
)

func acquireLock(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("OTLP disposition directory already owned: %w", err)
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("invalid OTLP lock file: %v", err)
	}
	return f, nil
}
