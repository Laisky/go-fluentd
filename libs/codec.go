package libs

import (
	"reflect"

	"github.com/ugorji/go/codec"
)

// NewCodec return an new Msgpack codec handler
//
// Notice: do not share same codec among goroutines
func NewCodec() *codec.MsgpackHandle {
	_codec := &codec.MsgpackHandle{}
	_codec.MapType = reflect.TypeOf(map[string]interface{}(nil))
	_codec.DecodeOptions.MapValueReset = true
	_codec.RawToString = false
	_codec.StructToArray = true
	return _codec
}
