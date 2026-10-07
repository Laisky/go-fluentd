//go:build !linux

package controller

import "fmt"

func prepareLegacyJournalRoot(string) error {
	return fmt.Errorf("private legacy journals require Linux owner/mode and descriptor-path enforcement")
}
