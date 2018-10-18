package concator

import (
	"fmt"
	"regexp"
	"runtime"
	"sync"
	"time"

	"github.com/Laisky/go-concator/acceptorFilters"
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

func (c *Controllor) initJournal() *Journal {
	return NewJournal(
		utils.Settings.GetString("settings.buf_dir_path"),
		utils.Settings.GetInt64("settings.buf_file_bytes"),
	)
}

func (c *Controllor) initRecvs(env string) []libs.AcceptorRecvItf {
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

	return receivers
}

func (c *Controllor) initAcceptor(journal *Journal, receivers []libs.AcceptorRecvItf) *Acceptor {
	acceptor := NewAcceptor(
		c.msgPool,
		journal,
		receivers...,
	)
	if err := acceptor.Run(); err != nil {
		utils.Logger.Fatal("try to run acceptor", zap.Error(err))
	}

	return acceptor
}

func (c *Controllor) initAcceptorPipeline(env string) *acceptorFilters.AcceptorPipeline {
	return acceptorFilters.NewAcceptorPipeline(
		c.msgPool,
		acceptorFilters.NewSparkFilter(&acceptorFilters.SparkFilterCfg{
			Tag:         utils.Settings.GetString("settings.acceptor_filters.spark.tags." + env),
			MsgKey:      utils.Settings.GetString("settings.acceptor_filters.spark.msg_key"),
			IgnoreRegex: regexp.MustCompile(utils.Settings.GetString("settings.acceptor_filters.spark.ignore_regex")),
		}),
	)
}

func (c *Controllor) initDispatcher(waitDispatchChan chan *libs.FluentMsg, concatorFact ConcatorFactoryItf) *Dispatcher {
	dispatcher := NewDispatcher(
		waitDispatchChan,
		concatorFact)
	dispatcher.Run()

	return dispatcher
}

func (c *Controllor) initProducer(waitProduceChan chan *libs.FluentMsg) *Producer {
	return NewProducer(
		utils.Settings.GetString("settings.backend_addr"),
		waitProduceChan,
		c.msgPool,
	)
}

func (c *Controllor) runHeartBeat() {
	for {
		utils.Logger.Info("heartbeat",
			zap.Int("goroutine", runtime.NumGoroutine()),
		)
		utils.Logger.Sync()
		time.Sleep(utils.Settings.GetDuration("heartbeat") * time.Second)
	}
}

// Run starting all pipeline
func (c *Controllor) Run() {
	utils.Logger.Info("running...")
	env := utils.Settings.GetString("env")

	journal := c.initJournal()
	receivers := c.initRecvs(env)
	acceptor := c.initAcceptor(journal, receivers)
	acceptorPipeline := c.initAcceptorPipeline(env)

	waitAccepPipelineChan := acceptor.MessageChan()
	waitDumpChan := acceptorPipeline.Wrap(waitAccepPipelineChan)
	waitDispatchChan := journal.DumpMsgFlow(c.msgPool, waitDumpChan)

	concatorFact := NewConcatorFactory()
	dispatcher := c.initDispatcher(waitDispatchChan, concatorFact)
	waitProduceChan := dispatcher.GetOutChan()
	producer := c.initProducer(waitProduceChan)
	waitCommitChan := journal.GetCommitChan()

	// heartbeat
	go c.runHeartBeat()

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

	producer.Run(
		utils.Settings.GetInt("settings.producer_forks"),
		waitCommitChan)
}
