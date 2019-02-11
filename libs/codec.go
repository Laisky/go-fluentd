package libs

import (
	"reflect"

	"github.com/ugorji/go/codec"
)

// NewOutputCodec return an new Msgpack codec handler
//
// Notice: do not share same codec among goroutines
func NewOutputCodec() *codec.MsgpackHandle {
	_codec := &codec.MsgpackHandle{}
	_codec.MapType = reflect.TypeOf(map[string]interface{}(nil))
	_codec.RawToString = false
	// _codec.DecodeOptions.MapValueReset = true
	_codec.StructToArray = true
	return _codec
}

func NewInputCodec() *codec.MsgpackHandle {
	_codec := &codec.MsgpackHandle{}
	_codec.MapType = reflect.TypeOf(map[string]interface{}(nil))
	_codec.RawToString = false
	// _codec.DecodeOptions.MapValueReset = true
	// _codec.StructToArray = true
	return _codec
}
