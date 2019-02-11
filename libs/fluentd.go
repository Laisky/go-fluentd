package libs

import (
	"bytes"
	"io"

	"github.com/ugorji/go/codec"
)

type TinyFluentRecord struct {
	Timestamp uint64
	Data      map[string]interface{}
}

type FluentEncoder struct {
	wrap, batchWrap []interface{}
	encoder         *codec.Encoder
	msgBuf          *bytes.Buffer
	tmpMsg          *FluentMsg
}

func NewFluentEncoder(writer io.Writer) *FluentEncoder {
	enc := &FluentEncoder{
		wrap:      []interface{}{0, nil},
		encoder:   codec.NewEncoder(writer, NewOutputCodec()),
		msgBuf:    &bytes.Buffer{},
		batchWrap: []interface{}{nil, nil, nil},
	}
	return enc
}

func (e *FluentEncoder) Encode(msg *FluentMsg) error {
	e.wrap[0] = msg.Tag
	e.wrap[1] = []*TinyFluentRecord{&TinyFluentRecord{Data: msg.Message}}
	return e.encoder.Encode(e.wrap)
}

func (e *FluentEncoder) EncodeBatch(tag string, msgBatch []*FluentMsg) (err error) {
	msgs := []*TinyFluentRecord{}
	for _, e.tmpMsg = range msgBatch {
		msgs = append(msgs, &TinyFluentRecord{Data: e.tmpMsg.Message})
	}
	e.wrap[0] = tag
	e.wrap[1] = msgs
	return e.encoder.Encode(e.wrap)

}

type Decoder struct {
	wrap    []interface{}
	decoder *codec.Decoder
}

func NewDecoder(reader io.Reader) *Decoder {
	return &Decoder{
		wrap:    []interface{}{nil, nil, nil},
		decoder: codec.NewDecoder(reader, NewOutputCodec()),
	}
}

func (d *Decoder) Decode(msg *FluentMsg) (err error) {
	d.wrap[2] = make(map[string]interface{}) // create new map, avoid influenced by old data
	if err = d.decoder.Decode(&d.wrap); err != nil {
		return err
	}

	msg.Tag = string(d.wrap[0].([]byte))
	msg.Message = d.wrap[2].(map[string]interface{})
	return nil
}
