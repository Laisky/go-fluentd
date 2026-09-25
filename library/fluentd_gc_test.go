package library

import (
	"bytes"
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/tinylib/msgp/msgp"
)

// This fixture is literal MessagePack, not encoded with the implementation under
// test: ["tag-3", [[0, {"sourceTag": "tag-3"}]]]. No sender or routing is involved.
func TestRegressionFluentDecodeStringLifetime(t *testing.T) {
	wire := []byte{0x92, 0xa5, 't', 'a', 'g', '-', '3', 0x91, 0x92, 0, 0x81,
		0xa9, 's', 'o', 'u', 'r', 'c', 'e', 'T', 'a', 'g', 0xa5, 't', 'a', 'g', '-', '3'}
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-stop:
				return
			default:
				runtime.GC()
			}
		}
	}()
	defer func() { close(stop); <-stopped }()
	const workers, rounds = 8, 4096
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				var packet FluentBatchMsg
				if err := packet.DecodeMsg(msgp.NewReader(bytes.NewReader(wire))); err != nil {
					errs <- err
					return
				}
				if len(packet) != 2 || packet[0] != "tag-3" {
					errs <- fmt.Errorf("decoded tag corrupted: %#v", packet)
					return
				}
				records, ok := packet[1].([]interface{})
				if !ok || len(records) != 1 {
					errs <- fmt.Errorf("invalid records: %#v", packet[1])
					return
				}
				entry, ok := records[0].([]interface{})
				if !ok || len(entry) != 2 {
					errs <- fmt.Errorf("invalid entry: %#v", records[0])
					return
				}
				record, ok := entry[1].(map[string]interface{})
				if !ok || record["sourceTag"] != "tag-3" {
					errs <- fmt.Errorf("decoded string lifetime corrupted: %#v", entry[1])
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
