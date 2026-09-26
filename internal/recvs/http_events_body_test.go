package recvs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fragmentBody struct {
	data []byte
	end  error
}

func (r *fragmentBody) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		if r.end != nil {
			return 0, r.end
		}
		return 0, io.EOF
	}
	n := min(len(p), min(7, len(r.data)))
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}
func TestEventBodyReadKeepsLimitsAndLengthHintSemantics(t *testing.T) {
	for _, size := range []int{0, 1, 511, 512, 513, 16384, 65536, 65537} {
		data := bytes.Repeat([]byte("x"), size)
		for _, hint := range []int64{-1, 0, 1, int64(size), int64(size + 1), 1 << 20} {
			for _, limit := range []int64{int64(size), int64(max(0, size-1)), int64(size + 1)} {
				t.Run(fmt.Sprintf("%d/hint%d/limit%d", size, hint, limit), func(t *testing.T) {
					reader := http.MaxBytesReader(httptest.NewRecorder(), io.NopCloser(&fragmentBody{data: data}), limit)
					got, err := readEventBody(reader, hint)
					if int64(size) > limit {
						var large *http.MaxBytesError
						if !errors.As(err, &large) {
							t.Fatalf("limit bypass: %v", err)
						}
						return
					}
					if err != nil || !bytes.Equal(got, data) {
						t.Fatalf("changed or truncated input: %v", err)
					}
				})
			}
		}
	}
	cause := errors.New("injected read failure")
	got, err := readEventBody(&fragmentBody{data: []byte("complete prefix"), end: cause}, 32)
	if !errors.Is(err, cause) || string(got) != "complete prefix" {
		t.Fatal("read failure hidden")
	}
}
func BenchmarkEventBodyRead16K(b *testing.B) {
	data := bytes.Repeat([]byte("x"), 16384)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		if _, err := readEventBody(bytes.NewReader(data), int64(len(data))); err != nil {
			b.Fatal(err)
		}
	}
}
