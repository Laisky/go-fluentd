//go:build linux || darwin

package controller

import "golang.org/x/sys/unix"

func otlpFilesystemCapacity(path string) (uint64, uint64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	if st.Bsize <= 0 || uint64(st.Bavail) > ^uint64(0)/uint64(st.Bsize) {
		return 0, 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), uint64(st.Ffree), true
}
