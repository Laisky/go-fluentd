package otlpwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

// Independent ceilings include zero-item containers, attributes, events, links,
// packed numbers and unknown-field work. They are admission policies, not an
// exact heap/RSS quota. Keep the wire/decompression/concurrency limits as well.
const (
	maxPreflightValues = 65536
	maxPreflightDepth  = 64
)

var errPreflightMalformed = errors.New("malformed OTLP structure")

type preflightField struct {
	number      protowire.Number
	name, alias string
	child       preflightKind
	repeated    bool
	packed      int // zero, varint (1), fixed32 (4), or fixed64 (8)
}
type preflightSchema struct {
	item   bool
	fields []preflightField
}
type preflightBudget struct{ values, items, maxItems int }

func (p *preflightBudget) take(n int) error {
	if n < 0 || n > maxPreflightValues-p.values {
		return ErrTooLarge
	}
	p.values += n
	return nil
}
func (p *preflightBudget) enter(k preflightKind, depth int) error {
	if depth > maxPreflightDepth {
		return ErrTooLarge
	}
	if err := p.take(1); err != nil {
		return err
	}
	if preflightSchemas[k].item {
		if p.items >= p.maxItems {
			return ErrTooLarge
		}
		p.items++
	}
	return nil
}
func preflightRoot(s Signal) preflightKind {
	switch s {
	case Logs:
		return pfExportLogsServiceRequest
	case Traces:
		return pfExportTraceServiceRequest
	default:
		return pfExportMetricsServiceRequest
	}
}
func preflight(s Signal, ct string, b []byte, items int) error {
	p := preflightBudget{maxItems: items}
	if ct == JSON {
		trim := bytes.TrimSpace(b)
		// Syntax/UTF-8 validation does not build a JSON object graph. The preflight
		// below bounds structural materialization separately from input-byte work.
		if !utf8.Valid(b) || len(trim) == 0 || trim[0] != '{' || !json.Valid(b) {
			return errPreflightMalformed
		}
		r := preflightJSON{b: trim, budget: &p}
		return r.value(preflightRoot(s), false, 0)
	}
	_, err := p.proto(b, preflightRoot(s), 0, 0)
	return err
}

// proto walks only known embedded messages. Unknown length-delimited fields are
// opaque bytes, NOT heuristically interpreted as messages. Unknown groups still
// consume depth/value budgets. No payload-sized temporary objects are created.
func (p *preflightBudget) proto(b []byte, kind preflightKind, depth int, group protowire.Number) (int, error) {
	if err := p.enter(kind, depth); err != nil {
		return 0, err
	}
	pos := 0
	for pos < len(b) {
		num, typ, n := protowire.ConsumeTag(b[pos:])
		if n < 0 {
			return 0, errPreflightMalformed
		}
		pos += n
		if typ == protowire.EndGroupType {
			if group == 0 || group != num {
				return 0, errPreflightMalformed
			}
			return pos, nil
		}
		if err := p.take(1); err != nil {
			return 0, err
		}
		var f preflightField
		for _, known := range preflightSchemas[kind].fields {
			if known.number == num {
				f = known
				break
			}
		}
		switch typ {
		case protowire.VarintType:
			_, n = protowire.ConsumeVarint(b[pos:])
		case protowire.Fixed32Type:
			n = 4
		case protowire.Fixed64Type:
			n = 8
		case protowire.BytesType:
			var v []byte
			v, n = protowire.ConsumeBytes(b[pos:])
			if n < 0 {
				return 0, errPreflightMalformed
			}
			if f.child != pfUnknown {
				if _, err := p.proto(v, f.child, depth+1, 0); err != nil {
					return 0, err
				}
			} else if f.packed > 0 {
				if f.packed == 1 {
					for len(v) > 0 {
						_, size := protowire.ConsumeVarint(v)
						if size < 0 {
							return 0, errPreflightMalformed
						}
						if err := p.take(1); err != nil {
							return 0, err
						}
						v = v[size:]
					}
				} else {
					if len(v)%f.packed != 0 {
						return 0, errPreflightMalformed
					}
					if err := p.take(len(v) / f.packed); err != nil {
						return 0, err
					}
				}
			}
		case protowire.StartGroupType:
			var err error
			n, err = p.proto(b[pos:], pfUnknown, depth+1, num)
			if err != nil {
				return 0, err
			}
		default:
			return 0, errPreflightMalformed
		}
		if n < 0 || n > len(b)-pos {
			return 0, errPreflightMalformed
		}
		pos += n
	}
	if group != 0 {
		return 0, errPreflightMalformed
	}
	return pos, nil
}

type preflightJSON struct {
	b      []byte
	pos    int
	budget *preflightBudget
}

func (r *preflightJSON) space() {
	for r.pos < len(r.b) {
		switch r.b[r.pos] {
		case ' ', '\n', '\r', '\t':
			r.pos++
		default:
			return
		}
	}
}

// stringEnd walks a syntax-validated JSON string, including escaped quotes.
func (r *preflightJSON) stringEnd() (start, end int, escaped bool) {
	r.pos++
	start = r.pos
	for r.b[r.pos] != '"' {
		if r.b[r.pos] == '\\' {
			escaped = true
			r.pos++
		}
		r.pos++
	}
	end = r.pos
	r.pos++
	return
}
func (r *preflightJSON) value(kind preflightKind, repeated bool, depth int) error {
	r.space()
	if repeated {
		// A null repeated field contains no elements. Invalid shapes are left to
		// pdata, but cannot acquire a message schema through a shape mismatch.
		if r.b[r.pos] != '[' {
			kind = pfUnknown
			repeated = false
		}
	}
	entryKind := kind
	if repeated {
		entryKind = pfUnknown
	}
	if err := r.budget.enter(entryKind, depth); err != nil {
		return err
	}
	switch r.b[r.pos] {
	case '{':
		r.pos++
		r.space()
		if r.b[r.pos] == '}' {
			r.pos++
			return nil
		}
		for {
			r.space()
			start, end, escaped := r.stringEnd()
			key := r.b[start:end]
			var decoded string
			if escaped {
				// At most input-sized work/allocation across all keys; escaped known keys
				// must not bypass the schema (e.g. "log\\u0052ecords").
				if err := json.Unmarshal(r.b[start-1:end+1], &decoded); err != nil {
					return errPreflightMalformed
				}
				key = []byte(decoded)
			}
			if err := r.budget.take(1); err != nil {
				return err
			}
			var f preflightField
			for _, known := range preflightSchemas[kind].fields {
				if string(key) == known.name || (known.alias != "" && string(key) == known.alias) {
					f = known
					break
				}
			}
			r.space()
			r.pos++ // colon; guaranteed by json.Valid
			if err := r.value(f.child, f.repeated, depth+1); err != nil {
				return err
			}
			r.space()
			c := r.b[r.pos]
			r.pos++
			if c == '}' {
				return nil
			}
		}
	case '[':
		r.pos++
		r.space()
		if r.b[r.pos] == ']' {
			r.pos++
			return nil
		}
		child := pfUnknown
		if repeated {
			child = kind
		}
		for {
			if err := r.value(child, false, depth+1); err != nil {
				return err
			}
			r.space()
			c := r.b[r.pos]
			r.pos++
			if c == ']' {
				return nil
			}
		}
	case '"':
		r.stringEnd()
	default:
		for r.pos < len(r.b) {
			switch r.b[r.pos] {
			case ',', '}', ']', ' ', '\n', '\r', '\t':
				return nil
			}
			r.pos++
		}
	}
	return nil
}
