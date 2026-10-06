//go:build !linux

package controller

import (
	"fmt"
	"os"
)

// The current path-only backend cannot provide a directory-relative containment
// guarantee here. Fail closed rather than silently returning an unsafe path.
func openJournalDirectory(root, tag string) (*os.File, string, error) {
	return nil, "", fmt.Errorf("secure legacy journals require Linux with /proc/self/fd")
}
