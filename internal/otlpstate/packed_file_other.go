//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package otlpstate

import "os"

func acceptedFileSafe(st, owner os.FileInfo) bool { return false }
