package streamformat

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// Freeze a2a5b686's direct tokenizer for same-input differential benchmarks.
// The reader-based oracle and explicit domain tests remain independent checks.
type stringTokenReference struct{ bufferTokens }

func (d *stringTokenReference) Token() (json.Token, error) {
	t, err := d.decoder.ReadToken()
	if err != nil {
		return nil, err
	}
	switch k := t.Kind(); k {
	case 'n':
		return nil, nil
	case 'f':
		return false, nil
	case 't':
		return true, nil
	case '"':
		return t.String(), nil
	case '0':
		return json.Number(t.String()), nil
	case '{', '}', '[', ']':
		return json.Delim(k), nil
	default:
		return nil, fmt.Errorf("invalid token %q", k)
	}
}
func tokenStringsReference(body []byte) (any, error) {
	if !utf8.Valid(body) {
		return nil, fmt.Errorf("UTF-8")
	}
	if err := validEscapes(body); err != nil {
		return nil, err
	}
	d := &stringTokenReference{bufferTokens{jsontext.NewDecoder(bytes.NewBuffer(body), jsontext.AllowDuplicateNames(true))}}
	v, err := value(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing value")
	}
	return v, nil
}

func TestJSONStringPrivateUnicodeAndEscapes(t *testing.T) {
	for _, s := range []string{"", "ascii", "世界 café", "é𝄞😀\u2028\u2029", "\x00\n\r\t\b\f", "slash / quote \" backslash \\", strings.Repeat("a", 16384) + "世界", strings.Repeat("世", 8192), strings.Repeat("\\", 8192)} {
		want := map[string]any{s: []any{s, "after"}, "tail": s}
		body, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		saved := bytes.Clone(body)
		got, err := JSON(body)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(body, saved) {
			t.Fatal("mutated input")
		}
		for i := range body {
			body[i] = 'x'
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("changed or aliased private key/value")
		}
	}
}
func TestJSONStringInvalidWireStillRejected(t *testing.T) {
	for _, body := range []string{`"\ud800"`, `"\udfff"`, `"\ud800x"`, `"\u12"`, `"\x41"`, `"\v"`, `"\'"`, `"a`, "\"a\n\"", "\"\xff\"", `{"世界":1,"\u4e16\u754c":2}`, `{"a":"x" "b":"y"}`, `["x" "y"]`, `"x" "y"`} {
		if _, err := JSON([]byte(body)); err == nil {
			t.Fatalf("accepted invalid wire %q", body)
		}
	}
}
func TestJSONStringRawAndEscapedEquivalence(t *testing.T) {
	for _, body := range []string{`{"\u4e16\u754c":"\ud83d\ude00","text":"a\/b\\c\t\n\r\b\f\""}`, `{"世界":"😀","": ["", null, true, false, 18446744073709551615]}`, `"\u0000\u2028\u2029"`, `"\\ud800"`} {
		want, err := readerJSONReference([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		got, err := JSON([]byte(body))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%q: %v / %v", body, got, err)
		}
	}
}
func TestJSONStringConcurrentOwnership(t *testing.T) {
	var wg sync.WaitGroup
	for worker := 0; worker < 24; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				text := fmt.Sprintf("世界-%d-%d-", worker, n) + strings.Repeat("a", 4096)
				body, _ := json.Marshal(map[string]any{text: text})
				got, err := JSON(body)
				if err != nil {
					t.Error(err)
					return
				}
				for i := range body {
					body[i] = 'x'
				}
				if !reflect.DeepEqual(got, map[string]any{text: text}) {
					t.Error("concurrent string storage alias")
					return
				}
			}
		}(worker)
	}
	wg.Wait()
}
func FuzzJSONStringTokenParity(f *testing.F) {
	for _, s := range []string{`{"世界":"café😀"}`, `{"a":1,"\u0061":2}`, `"\ud800"`, `"\ud83d\ude00"`, `"\/"`, `"\\ud800"`, `"` + strings.Repeat("世", 4096) + `"`, `{} []`, "\"\xff\""} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<18 {
			return
		}
		copy := bytes.Clone(body)
		want, we := tokenStringsReference(copy)
		got, ge := JSON(body)
		if (we == nil) != (ge == nil) || (we == nil && !reflect.DeepEqual(want, got)) {
			t.Fatalf("token/string parity: %v / %v", we, ge)
		}
		if !bytes.Equal(body, copy) {
			t.Fatal("mutated input")
		}
	})
}
func BenchmarkJSONStringMaterialization(b *testing.B) {
	for _, tc := range []struct{ name, text string }{
		{"ascii-16k", strings.Repeat("a", 16384)}, {"unicode-16k", strings.Repeat("a", 16384) + "世界 café"},
		{"unicode-dense", strings.Repeat("世", 5461)}, {"escaped-16k", strings.Repeat("a\\\n", 4096)}, {"small", "世界 café"},
	} {
		body, _ := json.Marshal(map[string]any{"text": tc.text, "key": int64(42)})
		for _, impl := range []struct {
			name   string
			decode func([]byte) (any, error)
		}{{"baseline", tokenStringsReference}, {"candidate", JSON}} {
			b.Run(tc.name+"/"+impl.name, func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				for b.Loop() {
					if _, err := impl.decode(body); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
