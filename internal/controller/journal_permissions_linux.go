//go:build linux

package controller

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// ErrLegacyJournalPermissions refuses unsafe historical storage without chmod,
// deleting records or implying successful durable acceptance.
var ErrLegacyJournalPermissions = errors.New("private journal permissions required")

func checkLegacyJournalInfo(info os.FileInfo, directory bool) error {
	want := os.FileMode(0600)
	if directory {
		want = 0700
	}
	if info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) || info.Mode().Perm() != want {
		return fmt.Errorf("%w: %q must have mode %04o and the expected directory/regular-file type (got %s); stop writers and migrate storage explicitly", ErrLegacyJournalPermissions, info.Name(), want, info.Mode())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || (!directory && stat.Nlink != 1) {
		return fmt.Errorf("%w: %q must belong to the effective service UID; files must have one link", ErrLegacyJournalPermissions, info.Name())
	}
	return nil
}

func prepareLegacyJournalRoot(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	return checkLegacyJournalInfo(info, true)
}

// The directory descriptor, not its mutable original pathname, is the trust
// anchor. Validate the entire flat historical tree before the path-only backend
// can create its writability probe, lock or new segments. Never follow symlinks.
// Owner-only directory permissions exclude other UIDs from replacing entries;
// privileged processes and a competing process with the same UID are not an
// isolation boundary. Configured root paths and their ancestors must be trusted.
func checkLegacyJournalFiles(directory *os.File, anchoredPath string) error {
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	if err = checkLegacyJournalInfo(info, true); err != nil {
		return err
	}
	for {
		entries, err := directory.ReadDir(128)
		if err != nil && err != io.EOF {
			return err
		}
		for _, entry := range entries {
			info, err := os.Lstat(filepath.Join(anchoredPath, entry.Name()))
			if err != nil {
				return err
			}
			if err = checkLegacyJournalInfo(info, false); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}
