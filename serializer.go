package concator

import (
	"io"

	"github.com/ugorji/go/codec"
)

type TinyFluentRecord struct {
	Timestamp uint64
	Data      map[string]interface{}
}

type Encoder struct {
	wrap    []interface{}
	encoder *codec.Encoder
}

func NewEncoder(writer io.Writer) *Encoder {
	return &Encoder{
		wrap:    []interface{}{nil, []TinyFluentRecord{TinyFluentRecord{}}},
		encoder: codec.NewEncoder(writer, NewCodec()),
	}
}

func (e *Encoder) Encode(msg *FluentMsg) error {
	e.wrap[0] = msg.Tag
	e.wrap[1].([]TinyFluentRecord)[0].Data = msg.Message
	return e.encoder.Encode(e.wrap)
}

type Decoder struct {
	wrap    []interface{}
	decoder *codec.Decoder
}

func NewDecoder(reader io.Reader) *Decoder {
	return &Decoder{
		wrap:    []interface{}{nil, nil, nil},
		decoder: codec.NewDecoder(reader, NewCodec()),
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
