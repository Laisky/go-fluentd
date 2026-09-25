package otlphttp

import (
	"bytes"
	"compress/gzip"
	"io"
	"sync"
	"testing"
)

func TestGzipWorkspaceReuseKeepsBodiesIndependent(t *testing.T) {
	e := &Exporter{}
	var wg sync.WaitGroup
	for n := 0; n < 32; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			var oldBody, oldInput []byte
			for i := 0; i < 20; i++ {
				input := bytes.Repeat([]byte{byte(n), byte(i), 'x'}, (i+1)*500)
				got, err := e.compress(input)
				if err != nil {
					t.Error(err)
					return
				}
				reader, err := gzip.NewReader(bytes.NewReader(got))
				if err != nil {
					t.Error(err)
					return
				}
				decoded, err := io.ReadAll(reader)
				reader.Close()
				if err != nil || !bytes.Equal(decoded, input) {
					t.Error("gzip body changed", err)
					return
				}
				if oldBody != nil {
					r, err := gzip.NewReader(bytes.NewReader(oldBody))
					if err != nil {
						t.Error(err)
						return
					}
					b, err := io.ReadAll(r)
					r.Close()
					if err != nil || !bytes.Equal(b, oldInput) {
						t.Error("reused workspace changed previously returned bytes")
						return
					}
				}
				oldBody, oldInput = got, input
			}
		}(n)
	}
	wg.Wait()
}
