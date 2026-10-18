//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package otlpstate

import (
	"os"
	"syscall"
)

func acceptedFileSafe(st, owner os.FileInfo) bool {
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		return false
	}
	a, ok := st.Sys().(*syscall.Stat_t)
	b, ok2 := owner.Sys().(*syscall.Stat_t)
	return ok && ok2 && a.Nlink == 1 && a.Uid == b.Uid
}
