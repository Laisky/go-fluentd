// Built only in an isolated module pinned to the genuine historical v1 writer.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	journal "github.com/Laisky/go-journal"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

func payload(id int64) map[string]interface{} {
	p := map[string]interface{}{"event": fmt.Sprintf("event-%d", id), "text": "hello\x00世界 / café", "nested": map[string]interface{}{"ok": true, "value": int64(73)}}
	if id == 5 {
		p["large"] = strings.Repeat("x", (4<<20)+17)
	}
	if id == 7 {
		p["large"] = strings.Repeat("z", (128<<10)+7)
	}
	p["binary"] = []byte{0, byte(id), 255}
	return p
}
func seed(dir string, compressed bool) error {
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		return fmt.Errorf("writer requires fresh directory: %s", dir)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	j, err := journal.NewJournal(journal.WithBufDirPath(dir), journal.WithIsCompress(compressed),
		journal.WithBufSizeByte(1<<20), journal.WithIsAggresiveGC(false), journal.WithFlushInterval(time.Hour),
		journal.WithRotateDuration(time.Hour), journal.WithRotateCheckInterval(time.Hour), journal.WithCommitIDTTL(time.Hour))
	if err != nil {
		return err
	}
	if err := j.Start(context.Background()); err != nil {
		j.Close()
		return err
	}
	defer j.Close()
	for id := int64(1); id <= 12; id++ {
		if err := j.WriteData(&journal.Data{ID: id, Data: map[string]interface{}{"tag": "source", "message": payload(id)}}); err != nil {
			return err
		}
		if id%3 == 0 {
			if err := j.WriteId(id); err != nil {
				return err
			}
		}
		if id%4 == 0 {
			if err := j.Sync(); err != nil {
				return err
			}
			if err := j.Rotate(context.Background()); err != nil {
				return err
			}
		}
	}
	return j.Sync()
}
func run() error {
	if len(os.Args) != 4 {
		return fmt.Errorf("usage: v1-writer fixture-dir expected-version provenance-file")
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Errorf("historical writer build provenance unavailable")
	}
	expected, found := os.Args[2], false
	for _, module := range info.Deps {
		if module.Path == "github.com/Laisky/go-journal/v2" {
			return fmt.Errorf("historical writer linked v2 instead of frozen v1")
		}
		if module.Path == "github.com/Laisky/go-journal" {
			if module.Replace != nil || module.Version != expected {
				return fmt.Errorf("historical writer journal provenance mismatch")
			}
			found = true
		}
	}
	if !found {
		return fmt.Errorf("historical writer did not link frozen v1")
	}
	data, err := json.MarshalIndent(map[string]interface{}{"module": "github.com/Laisky/go-journal", "version": expected, "go": info.GoVersion}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(os.Args[3], append(data, '\n'), 0600); err != nil {
		return err
	}
	for _, compressed := range []bool{false, true} {
		if err := seed(filepath.Join(os.Args[1], fmt.Sprintf("gzip-%v", compressed)), compressed); err != nil {
			return err
		}
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
