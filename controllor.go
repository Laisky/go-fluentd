package concator

import (
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"time"

	"github.com/kataras/iris"

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
	_codec.RawToString = false
	_codec.StructToArray = true
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

	waitDumpChan := acceptor.MessageChan()
	waitDispatchChan := journal.DumpMsgFlow(c.msgPool, waitDumpChan)
	dispatcher := NewDispatcher(
		waitDispatchChan,
		concatorFact,
		concatorFact.MessageChan())
	dispatcher.Run()

	waitProduceChan := concatorFact.MessageChan()
	producer := NewProducer(
		utils.Settings.GetString("settings.backend_addr"),
		waitProduceChan,
		c.msgPool,
	)
	waitCommitChan := journal.GetCommitChan()

	// heartbeat
	go func() {
		for {
			for {
				utils.Logger.Info("heartbeat",
					zap.Int("goroutine", runtime.NumGoroutine()),
					zap.Int("waitDumpChan", len(waitDumpChan)),
					zap.Int("waitDispatchChan", len(waitDispatchChan)),
					zap.Int("waitProduceChan", len(waitProduceChan)),
					zap.Int("waitCommitChan", len(waitCommitChan)),
				)
				utils.Logger.Sync()
				time.Sleep(utils.Settings.GetDuration("heartbeat") * time.Second)
			}
		}
	}()
	Server.Get("/monitor/controllor", func(ctx iris.Context) {
		ctx.WriteString(fmt.Sprintf(`
goroutine: %v
waitDumpChan: %v
waitDispatchChan: %v
waitProduceChan: %v
waitCommitChan: %v`,
			runtime.NumGoroutine(),
			len(waitDumpChan),
			len(waitDispatchChan),
			len(waitProduceChan),
			len(waitCommitChan),
		))
	})

	go producer.Run(
		utils.Settings.GetInt("settings.producer_forks"),
		waitCommitChan)
}
