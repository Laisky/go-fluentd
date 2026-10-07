//go:build linux

package controller

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openJournalDirectory retains the original single-component tag layout, while
// handing the path-only backend an anchored descriptor path. Neither a symlink
// at admission nor replacing the child path later can redirect backend rotation.
func openJournalDirectory(root, tag string) (*os.File, string, error) {
	if tag == "" || tag == "." || tag == ".." || filepath.IsAbs(tag) || strings.ContainsAny(tag, "/\x00") {
		return nil, "", fmt.Errorf("journal tag must be a nonempty directory component")
	}
	parent, err := os.Open(root)
	if err != nil {
		return nil, "", err
	}
	defer parent.Close()
	info, err := parent.Stat()
	if err != nil {
		return nil, "", err
	}
	if err := checkLegacyJournalInfo(info, true); err != nil {
		return nil, "", err
	}
	if err := unix.Mkdirat(int(parent.Fd()), tag, 0700); err != nil && err != unix.EEXIST {
		return nil, "", err
	}
	fd, err := unix.Openat(int(parent.Fd()), tag, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	directory := os.NewFile(uintptr(fd), filepath.Join(root, tag))
	path := fmt.Sprintf("/proc/self/fd/%d", fd)
	actual, err := os.Stat(path)
	if err == nil {
		var original os.FileInfo
		original, err = directory.Stat()
		if err == nil && !os.SameFile(original, actual) {
			err = fmt.Errorf("journal descriptor path does not identify its directory")
		}
	}
	if err != nil {
		directory.Close()
		return nil, "", fmt.Errorf("secure journal descriptor path unavailable: %w", err)
	}
	if err := checkLegacyJournalFiles(directory, path); err != nil {
		directory.Close()
		return nil, "", err
	}
	return directory, path, nil
}
