package concator

import (
	"bufio"
	"bytes"
	"io"

	"github.com/Laisky/go-concator/libs"
	"github.com/Laisky/go-utils"
	"github.com/ugorji/go/codec"
	"go.uber.org/zap"
)

type TinyFluentRecord struct {
	Timestamp uint64
	Data      map[string]interface{}
}

type Encoder struct {
	wrap, batchWrap               []interface{}
	encoder, internalBatchEncoder *codec.Encoder
	msgBuf                        *bytes.Buffer
	msgWriter                     *bufio.Writer
	tmpMsg                        *libs.FluentMsg
}

func NewEncoder(writer io.Writer) *Encoder {
	enc := &Encoder{
		wrap:      []interface{}{0, []TinyFluentRecord{TinyFluentRecord{}}},
		encoder:   codec.NewEncoder(writer, libs.NewCodec()),
		msgBuf:    &bytes.Buffer{},
		batchWrap: []interface{}{nil, nil, nil},
	}
	enc.msgWriter = bufio.NewWriter(enc.msgBuf)
	enc.internalBatchEncoder = codec.NewEncoder(enc.msgWriter, libs.NewCodec())
	return enc
}

func (e *Encoder) Encode(msg *libs.FluentMsg) error {
	e.wrap[0] = msg.Tag
	e.wrap[1].([]TinyFluentRecord)[0].Data = msg.Message
	return e.encoder.Encode(e.wrap)
}

func (e *Encoder) EncodeBatch(tag string, msgBatch []*libs.FluentMsg) (err error) {
	for _, e.tmpMsg = range msgBatch {
		e.wrap[1] = e.tmpMsg.Message
		if err = e.internalBatchEncoder.Encode(e.wrap); err != nil {
			utils.Logger.Error("try to encode msg got error", zap.String("tag", tag))
		}
	}

	e.batchWrap[0] = tag
	e.msgWriter.Flush()
	e.batchWrap[1] = e.msgBuf.Bytes()
	e.msgBuf.Reset()
	return e.encoder.Encode(e.batchWrap)
}

type Decoder struct {
	wrap    []interface{}
	decoder *codec.Decoder
}

func NewDecoder(reader io.Reader) *Decoder {
	return &Decoder{
		wrap:    []interface{}{nil, nil, nil},
		decoder: codec.NewDecoder(reader, libs.NewCodec()),
	}
}

func (d *Decoder) Decode(msg *libs.FluentMsg) (err error) {
	d.wrap[2] = make(map[string]interface{}) // create new map, avoid influenced by old data
	if err = d.decoder.Decode(&d.wrap); err != nil {
		return err
	}

	msg.Tag = string(d.wrap[0].([]byte))
	msg.Message = d.wrap[2].(map[string]interface{})
	return nil
}
