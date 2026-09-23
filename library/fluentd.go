package library

import (
	"io"

	"github.com/tinylib/msgp/msgp"
)

const BufByte = 1024 * 1024 * 4

type TinyFluentRecord struct {
	Timestamp uint64
	Data      map[string]interface{}
}

type FluentEncoder struct {
	wrap   FluentBatchMsg
	writer *msgp.Writer
}

func NewFluentEncoder(writer io.Writer) *FluentEncoder {
	enc := &FluentEncoder{
		// wrap: tag, [[ts, msg], [ts, msg], ...]
		wrap: FluentBatchMsg{0, []interface{}{
			[]interface{}{0, nil},
		}},
		writer: msgp.NewWriterSize(writer, BufByte),
	}

	return enc
}

func (e *FluentEncoder) Encode(msg *FluentMsg) error {
	e.wrap[0] = msg.Tag
	e.wrap[1].([]interface{})[0].([]interface{})[1] = msg.Message
	return e.wrap.EncodeMsg(e.writer)
}

// EncodeBatch writes the Forward envelope directly. Only payload values use
// dynamic encoding; the fixed [tag, [[0, record], ...]] framing needs no pooled
// interface wrappers or per-record temporary slices.
func (e *FluentEncoder) EncodeBatch(tag string, msgBatch []*FluentMsg) error {
	if err := e.writer.WriteArrayHeader(2); err != nil {
		return err
	}
	if err := e.writer.WriteString(tag); err != nil {
		return err
	}
	if err := e.writer.WriteArrayHeader(uint32(len(msgBatch))); err != nil {
		return err
	}
	for _, msg := range msgBatch {
		if err := e.writer.WriteArrayHeader(2); err != nil {
			return err
		}
		if err := e.writer.WriteInt64(0); err != nil {
			return err
		}
		if err := e.writer.WriteMapStrIntf(msg.Message); err != nil {
			return err
		}
	}
	return nil
}

func (e *FluentEncoder) Flush() error {
	return e.writer.Flush()
}

// type Decoder struct {
// 	wrap    []interface{}
// 	decoder *codec.Decoder
// }

// func NewDecoder(reader io.Reader) *Decoder {
// 	return &Decoder{
// 		wrap:    []interface{}{nil, nil, nil},
// 		decoder: codec.NewDecoder(reader, NewOutputCodec()),
// 	}
// }

// func (d *Decoder) Decode(msg *FluentMsg) (err error) {
// 	d.wrap[2] = make(map[string]interface{}) // create new map, avoid influenced by old data
// 	if err = d.decoder.Decode(&d.wrap); err != nil {
// 		return err
// 	}

// 	msg.Tag = string(d.wrap[0].([]byte))
// 	msg.Message = d.wrap[2].(map[string]interface{})
// 	return nil
// }
