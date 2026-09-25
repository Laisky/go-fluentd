package streamformat

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func scalarEscapesReference(b []byte) error {
	quoted := false
	for i := 0; i < len(b); i++ {
		if b[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			break
		}
		if b[i] != 'u' || i+4 >= len(b) {
			continue
		}
		u, e := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if e != nil {
			continue
		}
		i += 4
		if u >= 0xdc00 && u <= 0xdfff {
			return fmt.Errorf("unpaired Unicode surrogate")
		}
		if u >= 0xd800 && u <= 0xdbff {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return fmt.Errorf("unpaired Unicode surrogate")
			}
			low, e := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return fmt.Errorf("unpaired Unicode surrogate")
			}
			i += 6
		}
	}
	return nil
}

func FuzzEscapeScanParity(f *testing.F) {
	for _, s := range []string{`"hello"`, `"\ud800"`, `"\ud83d\ude00"`, `"\\ud800"`, `"\"\ud800"`, strings.Repeat("x", 16384) + `"\udfff"`, strings.Repeat("x", 16384)} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<18 {
			return
		}
		want, got := scalarEscapesReference(b), validEscapes(b)
		if (want == nil) != (got == nil) {
			t.Fatalf("escape validation changed: %v / %v", want, got)
		}
	})
}
func TestEscapeFastPathKeepsSurrogateRules(t *testing.T) {
	for _, s := range []string{`"\ud800"`, `"\udfff"`, `"\ud800x"`, `"\ud800\u0041"`} {
		if validEscapes([]byte(s)) == nil {
			t.Fatal("invalid surrogate accepted")
		}
	}
	for _, s := range []string{`"\ud83d\ude00"`, `"\\ud800"`, `"世界"`, strings.Repeat("a", 65536)} {
		if err := validEscapes([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
}
