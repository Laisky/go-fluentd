//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package otlpstate

import (
	"errors"
	"os"
)

func acquireLock(string) (*os.File, error) {
	return nil, errors.New("OTLP disposition store requires supported Unix file locking")
}
