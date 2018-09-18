package concator

import (
	"reflect"
	"sync"

	"go.uber.org/zap"

	utils "github.com/Laisky/go-utils"
	"github.com/ugorji/go/codec"
)

// Controllor is an IoC that manage all roles
type Controllor struct {
	msgPool *sync.Pool
}

// NewCodec return an new Msgpack codec handler
//
// Notice: do not share same codec among goroutines
func NewCodec() *codec.MsgpackHandle {
	_codec := &codec.MsgpackHandle{}
	_codec.MapType = reflect.TypeOf(map[string]interface{}(nil))
	_codec.DecodeOptions.MapValueReset = true
	// _codec.RawToString = false
	return _codec
}

// NewControllor create new Controllor
func NewControllor() *Controllor {
	utils.Logger.Info("create Controllor")

	return &Controllor{
		msgPool: &sync.Pool{
			New: func() interface{} {
				return &FluentMsg{
					// Message: map[string]interface{}{},
					Id: -1,
				}
			},
		},
	}
}

// Run starting all pipeline
func (c *Controllor) Run() {
	utils.Logger.Info("running...")
	var err error

	journal := NewJournal(
		utils.Settings.GetString("settings.buf_dir_path"),
		utils.Settings.GetInt64("settings.buf_file_bytes"),
	)

	acceptor := NewAcceptor(
		utils.Settings.GetString("settings.listen_addr"),
		c.msgPool,
		journal,
	)
	if err = acceptor.Run(); err != nil {
		utils.Logger.Fatal("try to run acceptor", zap.Error(err))
	}

	concatorFact := NewConcatorFactory()

	dispatcher := NewDispatcher(
		journal.DumpMsgFlow(c.msgPool, acceptor.MessageChan()),
		concatorFact,
		concatorFact.MessageChan())
	dispatcher.Run()

	producer := NewProducer(
		utils.Settings.GetString("settings.backend_addr"),
		concatorFact.MessageChan(),
		c.msgPool,
	)
	producer.Run(
		utils.Settings.GetInt("settings.producer_forks"),
		journal.GetCommitChan())
}
