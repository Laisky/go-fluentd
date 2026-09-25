// Package streamformat implements bounded HTTP event formats without changing
// journal or Fluent wire formats. Records use ordinary MessagePack-safe values.
package streamformat

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// JSON decodes exactly one UTF-8 JSON value. Integer tokens remain int64/uint64,
// never float64; out-of-range integers are rejected instead of being rounded.
// Fractional/exponent tokens use finite IEEE-754 float64. Duplicate object keys,
// invalid Unicode and nesting beyond 64 levels are rejected.
func JSON(body []byte) (interface{}, error) {
	if !utf8.Valid(body) {
		return nil, fmt.Errorf("invalid UTF-8")
	}
	if err := validEscapes(body); err != nil {
		return nil, err
	}
	// Use the standard-library tokenizer directly over the immutable body.
	// encoding/json's compatibility wrapper deliberately hides bytes.Buffer,
	// causing another geometrically grown input buffer on large records.
	// Duplicate keys are still rejected by value; token strings own storage.
	d := &bufferTokens{jsontext.NewDecoder(bytes.NewBuffer(body), jsontext.AllowDuplicateNames(true))}
	v, err := value(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fmt.Errorf("expected one JSON value")
	}
	return v, nil
}

// Both the production tokenizer and the reader-based test reference use the
// same domain conversion, including integer precision, depth and duplicate keys.
type jsonTokens interface {
	Token() (json.Token, error)
	More() bool
}

type bufferTokens struct{ decoder *jsontext.Decoder }

func (d *bufferTokens) More() bool {
	k := d.decoder.PeekKind()
	return k != 0 && k != '}' && k != ']'
}
func (d *bufferTokens) Token() (json.Token, error) {
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
		return nil, fmt.Errorf("invalid JSON token %q", k)
	}
}

func value(d jsonTokens, depth int) (interface{}, error) {
	if depth > 64 {
		return nil, fmt.Errorf("JSON nesting exceeds 64")
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t := t.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := map[string]interface{}{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return nil, err
				}
				key, ok := k.(string)
				if !ok {
					return nil, fmt.Errorf("invalid object key")
				}
				if _, ok = m[key]; ok {
					return nil, fmt.Errorf("duplicate JSON key %q", key)
				}
				v, err := value(d, depth+1)
				if err != nil {
					return nil, err
				}
				m[key] = v
			}
			if end, err := d.Token(); err != nil || end != json.Delim('}') {
				return nil, fmt.Errorf("unterminated object")
			}
			return m, nil
		case '[':
			a := []interface{}{}
			for d.More() {
				v, err := value(d, depth+1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			if end, err := d.Token(); err != nil || end != json.Delim(']') {
				return nil, fmt.Errorf("unterminated array")
			}
			return a, nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter")
		}
	case json.Number:
		s := string(t)
		if !strings.ContainsAny(s, ".eE") {
			if v, e := strconv.ParseInt(s, 10, 64); e == nil {
				return v, nil
			}
			if v, e := strconv.ParseUint(s, 10, 64); e == nil {
				return v, nil
			}
			return nil, fmt.Errorf("integer outside 64-bit domain")
		}
		v, e := strconv.ParseFloat(s, 64)
		if e != nil || math.IsInf(v, 0) || math.IsNaN(v) {
			return nil, fmt.Errorf("non-finite JSON number")
		}
		// Reject underflow rather than silently turn a nonzero payload into zero.
		if v == 0 {
			mantissa := strings.FieldsFunc(s, func(r rune) bool { return r == 'e' || r == 'E' })[0]
			if strings.Trim(mantissa, "-+.0") != "" {
				return nil, fmt.Errorf("JSON number underflow")
			}
		}
		return v, nil
	default:
		return t, nil
	}
}

// encoding/json replaces lone UTF-16 surrogates with U+FFFD. For a forwarding
// pipeline that would silently alter caller content, so validate escape pairs.
func validEscapes(b []byte) error {
	// UTF-16 surrogate escapes require a backslash. Most log/event strings
	// contain none; the optimized byte search avoids a branch for every byte.
	// JSON syntax and UTF-8 validation still run, including on this fast path.
	if bytes.IndexByte(b, '\\') < 0 {
		return nil
	}

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
