package library

import (
	"bytes"
	"io"
	"sync"

	"github.com/tinylib/msgp/msgp"
)

const BufByte = 1024 * 1024 * 4

var fluentdWrapMsgPool = &sync.Pool{
	New: func() interface{} {
		return &[]interface{}{0, nil}
	},
}

type TinyFluentRecord struct {
	Timestamp uint64
	Data      map[string]interface{}
}

type FluentEncoder struct {
	batchEntries    []*[]interface{}
	wrap, batchWrap FluentBatchMsg
	writer          *msgp.Writer
	msgBuf          *bytes.Buffer
}

func NewFluentEncoder(writer io.Writer) *FluentEncoder {
	enc := &FluentEncoder{
		// wrap: tag, [[ts, msg], [ts, msg], ...]
		wrap: FluentBatchMsg{0, []interface{}{
			[]interface{}{0, nil},
		}},
		batchWrap: FluentBatchMsg{0, []interface{}{}},
		writer:    msgp.NewWriterSize(writer, BufByte),
		msgBuf:    &bytes.Buffer{},
	}

	return enc
}

func (e *FluentEncoder) Encode(msg *FluentMsg) error {
	e.wrap[0] = msg.Tag
	e.wrap[1].([]interface{})[0].([]interface{})[1] = msg.Message
	return e.wrap.EncodeMsg(e.writer)
}

// EncodeBatch borrows distinct wrappers and returns each original pool object once.
// Message references are cleared before returning wrappers to the shared pool.
func (e *FluentEncoder) EncodeBatch(tag string, msgBatch []*FluentMsg) (err error) {
	e.batchWrap[1] = e.batchWrap[1].([]interface{})[:0]
	e.batchEntries = e.batchEntries[:0]
	for _, msg := range msgBatch {
		entry := fluentdWrapMsgPool.Get().(*[]interface{})
		(*entry)[1] = msg.Message
		e.batchEntries = append(e.batchEntries, entry)
		e.batchWrap[1] = append(e.batchWrap[1].([]interface{}), *entry)
	}
	e.batchWrap[0] = tag
	err = e.batchWrap.EncodeMsg(e.writer)
	for i, entry := range e.batchEntries {
		(*entry)[1] = nil
		fluentdWrapMsgPool.Put(entry)
		e.batchEntries[i] = nil
		e.batchWrap[1].([]interface{})[i] = nil
	}
	e.batchEntries = e.batchEntries[:0]
	e.batchWrap[1] = e.batchWrap[1].([]interface{})[:0]
	return err
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
