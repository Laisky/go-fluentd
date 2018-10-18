package concator

import (
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/Laisky/go-concator/libs"
	"github.com/Laisky/go-concator/recvs"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/go-utils/kafka"
	"github.com/kataras/iris"
	"go.uber.org/zap"
)

// Controllor is an IoC that manage all roles
type Controllor struct {
	msgPool *sync.Pool
}

// NewControllor create new Controllor
func NewControllor() *Controllor {
	utils.Logger.Info("create Controllor")

	return &Controllor{
		msgPool: &sync.Pool{
			New: func() interface{} {
				return &libs.FluentMsg{
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
	env := utils.Settings.GetString("env")

	// init journal
	journal := NewJournal(
		utils.Settings.GetString("settings.buf_dir_path"),
		utils.Settings.GetInt64("settings.buf_file_bytes"),
	)

	// init tcp recvs
	receivers := []libs.AcceptorRecvItf{
		recvs.NewTcpRecv(utils.Settings.GetString("settings.listen_addr")), // tcprecv
	}
	// init kafka tenants recvs
	sharingKMsgPool := &sync.Pool{
		New: func() interface{} {
			return &kafka.KafkaMsg{}
		},
	}
	for prj := range utils.Settings.Get("settings.kafka_recvs.tenants").(map[string]interface{}) {
		utils.Logger.Info("starting kafka recvs", zap.String("project", prj))
		receivers = append(receivers, recvs.NewKafkaRecv(&recvs.KafkaCfg{
			KMsgPool:  sharingKMsgPool,
			Meta:      utils.Settings.Get("settings.kafka_recvs.tenants." + prj + ".meta").(map[string]interface{}),
			MsgKey:    utils.Settings.GetString("settings.kafka_recvs.tenants." + prj + ".msg_key"),
			Brokers:   utils.Settings.GetStringSlice("settings.kafka_recvs.tenants." + prj + ".brokers." + env),
			Topics:    []string{utils.Settings.GetString("settings.kafka_recvs.tenants." + prj + ".topics." + env)},
			Group:     utils.Settings.GetString("settings.kafka_recvs.tenants." + prj + ".groups." + env),
			Tag:       utils.Settings.GetString("settings.kafka_recvs.tenants." + prj + ".tags." + env),
			NConsumer: utils.Settings.GetInt("settings.kafka_recvs.tenants." + prj + ".nconsumer"),
			KafkaCommitCfg: &recvs.KafkaCommitCfg{
				IntervalNum:      utils.Settings.GetInt("settings.kafka_recvs.interval_num." + env),
				IntervalDuration: utils.Settings.GetDuration("settings.kafka_recvs.interval_sec." + env),
			},
		}))
	}

	// init acceptors
	acceptor := NewAcceptor(
		c.msgPool,
		journal,
		receivers...,
	)
	if err = acceptor.Run(); err != nil {
		utils.Logger.Fatal("try to run acceptor", zap.Error(err))
	}

	// init concator
	concatorFact := NewConcatorFactory()

	// init dispatcher
	waitDumpChan := acceptor.MessageChan()
	waitDispatchChan := journal.DumpMsgFlow(c.msgPool, waitDumpChan)
	dispatcher := NewDispatcher(
		waitDispatchChan,
		concatorFact,
		concatorFact.MessageChan())
	dispatcher.Run()

	// init producer
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
	}()

	// monitor server
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
