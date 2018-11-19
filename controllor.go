package concator

import (
	"regexp"
	"runtime"
	"sync"
	"time"

	"github.com/Laisky/go-concator/acceptorFilters"
	"github.com/Laisky/go-concator/libs"
	"github.com/Laisky/go-concator/monitor"
	"github.com/Laisky/go-concator/postFilters"
	"github.com/Laisky/go-concator/recvs"
	"github.com/Laisky/go-concator/tagFilters"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/go-utils/kafka"
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
	return NewJournal(&JournalCfg{
		BufDirPath:        utils.Settings.GetString("settings.journal.buf_dir_path"),
		BufSizeBytes:      utils.Settings.GetInt64("settings.journal.buf_file_bytes"),
		JournalOutChanLen: utils.Settings.GetInt("settings.journal.journal_out_chan_len"),
		CommitIdChanLen:   utils.Settings.GetInt("settings.journal.commit_id_chan_len"),
	})
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
	acceptor := NewAcceptor(&AcceptorCfg{
		MsgPool:     c.msgPool,
		Journal:     journal,
		MaxRotateId: utils.Settings.GetInt64("settings.acceptor.max_rotate_id"),
		OutChanSize: utils.Settings.GetInt("settings.acceptor.out_chan_size"),
	},
		receivers...,
	)

	acceptor.Run()
	return acceptor
}

func (c *Controllor) initAcceptorPipeline(env string) *acceptorFilters.AcceptorPipeline {
	return acceptorFilters.NewAcceptorPipeline(&acceptorFilters.AcceptorPipelineCfg{
		OutChanSize:     utils.Settings.GetInt("settings.acceptor_filters.out_buf_len"),
		MsgPool:         c.msgPool,
		ReEnterChanSize: utils.Settings.GetInt("settings.acceptor_filters.reenter_chan_len"),
	},
		acceptorFilters.NewSparkFilter(&acceptorFilters.SparkFilterCfg{
			Tag:         "spark." + env,
			MsgKey:      utils.Settings.GetString("settings.acceptor_filters.tenants.spark.msg_key"),
			Identifier:  utils.Settings.GetString("settings.acceptor_filters.tenants.spark.identifier"),
			IgnoreRegex: regexp.MustCompile(utils.Settings.GetString("settings.acceptor_filters.tenants.spark.ignore_regex")),
		}),
		acceptorFilters.NewSpringFilter(&acceptorFilters.SpringFilterCfg{
			Tag:   "spring." + env,
			Env:   env,
			Rules: acceptorFilters.ParseSpringRules(utils.Settings.Get("settings.acceptor_filters.tenants.spring.rules").([]interface{})),
		}),
		// set the DefaultFilter as last filter
		acceptorFilters.NewDefaultFilter(acceptorFilters.NewDefaultFilterCfg()),
	)
}

func (c *Controllor) initTagPipeline(env string, waitCommitChan chan<- int64) *tagFilters.TagPipeline {
	return tagFilters.NewTagPipeline(&tagFilters.TagPipelineCfg{
		MsgPool:                 c.msgPool,
		CommitedChan:            waitCommitChan,
		DefaultInternalChanSize: utils.Settings.GetInt("settings.tag_filters.internal_chan_size"),
	},
		// set ConcatorFact as first tagfilter
		tagFilters.NewConcatorFact(&tagFilters.ConcatorFactCfg{
			MaxLen:       utils.Settings.GetInt("settings.tag_filters.tenants.concator.config.max_length"),
			ConcatorCfgs: libs.LoadConcatorTagConfigs(),
		}),
		// another tagfilters
		tagFilters.NewConnectorFact(&tagFilters.ConnectorFactCfg{
			Env:             env,
			Tags:            utils.Settings.GetStringSlice("settings.tag_filters.tenants.connector.tags"),
			MsgKey:          utils.Settings.GetString("settings.tag_filters.tenants.connector.msg_key"),
			Regexp:          regexp.MustCompile(utils.Settings.GetString("settings.tag_filters.tenants.connector.pattern")),
			IsRemoveOrigLog: utils.Settings.GetBool("settings.tag_filters.tenants.connector.is_remove_orig_log"),
			MsgPool:         c.msgPool,
		}),
		tagFilters.NewGeelyFact(&tagFilters.GeelyFactCfg{
			Tag:             utils.Settings.GetString("settings.tag_filters.tenants.geely.tag") + "." + env,
			MsgKey:          utils.Settings.GetString("settings.tag_filters.tenants.geely.msg_key"),
			Regexp:          regexp.MustCompile(utils.Settings.GetString("settings.tag_filters.tenants.geely.pattern")),
			IsRemoveOrigLog: utils.Settings.GetBool("settings.tag_filters.tenants.geely.is_remove_orig_log"),
			MsgPool:         c.msgPool,
		}),
	)
}

func (c *Controllor) initDispatcher(waitDispatchChan chan *libs.FluentMsg, tagPipeline *tagFilters.TagPipeline) *Dispatcher {
	dispatcher := NewDispatcher(&DispatcherCfg{
		InChan:      waitDispatchChan,
		TagPipeline: tagPipeline,
		OutChanSize: utils.Settings.GetInt("settings.dispatcher.out_chan_size"),
	})
	dispatcher.Run()

	return dispatcher
}

func (c *Controllor) initPostPipeline(env string, waitCommitChan chan<- int64) *postFilters.PostPipeline {
	return postFilters.NewPostPipeline(&postFilters.PostPipelineCfg{
		MsgPool:         c.msgPool,
		CommittedChan:   waitCommitChan,
		ReEnterChanSize: utils.Settings.GetInt("settings.post_filters.reenter_chan_len"),
		OutChanSize:     utils.Settings.GetInt("settings.post_filters.out_chan_size"),
	},
		// set the DefaultFilter as first filter
		postFilters.NewDefaultFilter(&postFilters.DefaultFilterCfg{
			MsgKey: utils.Settings.GetString("settings.post_filters.tenants.default.msg_key"),
			MaxLen: utils.Settings.GetInt("settings.post_filters.tenants.default.max_len"),
		}),
		// custom filters...
	)
}

func (c *Controllor) initProducer(waitProduceChan chan *libs.FluentMsg) *Producer {
	return NewProducer(&ProducerCfg{
		Addr:            utils.Settings.GetString("settings.producer.backend_addr"),
		BatchSize:       utils.Settings.GetInt("settings.producer.msg_batch_size"),
		MaxWait:         utils.Settings.GetDuration("settings.producer.max_wait_sec") * time.Second,
		InChan:          waitProduceChan,
		MsgPool:         c.msgPool,
		EachTagChanSize: utils.Settings.GetInt("settings.producer.each_tag_chan_len"),
		RetryChanSize:   utils.Settings.GetInt("settings.producer.retry_chan_len"),
	})
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

	waitCommitChan := journal.GetCommitChan()
	waitAccepPipelineChan := acceptor.MessageChan()
	waitDumpChan, skipDumpChan := acceptorPipeline.Wrap(waitAccepPipelineChan)

	// after `journal.DumpMsgFlow`, every discarded msg should commit to waitCommitChan
	waitDispatchChan := journal.DumpMsgFlow(c.msgPool, waitDumpChan, skipDumpChan)

	tagPipeline := c.initTagPipeline(env, waitCommitChan)
	dispatcher := c.initDispatcher(waitDispatchChan, tagPipeline)
	waitPostPipelineChan := dispatcher.GetOutChan()
	postPipeline := c.initPostPipeline(env, waitCommitChan)
	waitProduceChan := postPipeline.Wrap(waitPostPipelineChan)
	producer := c.initProducer(waitProduceChan)

	// heartbeat
	go c.runHeartBeat()

	// monitor
	monitor.AddMetric("controllor", func() map[string]interface{} {
		return map[string]interface{}{
			"goroutine":                runtime.NumGoroutine(),
			"waitAccepPipelineChanLen": len(waitAccepPipelineChan),
			"waitAccepPipelineChanCap": cap(waitAccepPipelineChan),
			"waitDumpChanLen":          len(waitDumpChan),
			"waitDumpChanCap":          cap(waitDumpChan),
			"skipDumpChanLen":          len(skipDumpChan),
			"skipDumpChanCap":          cap(skipDumpChan),
			"waitDispatchChanLen":      len(waitDispatchChan),
			"waitDispatchChanCap":      cap(waitDispatchChan),
			"waitPostPipelineChanLen":  len(waitPostPipelineChan),
			"waitPostPipelineChanCap":  cap(waitPostPipelineChan),
			"waitProduceChanLen":       len(waitProduceChan),
			"waitProduceChanCap":       cap(waitProduceChan),
			"waitCommitChanLen":        len(waitCommitChan),
			"waitCommitChanCap":        cap(waitCommitChan),
		}
	})
	monitor.BindHTTP(Server)

	go producer.Run(
		utils.Settings.GetInt("settings.producer_forks"),
		waitCommitChan)

	RunServer(utils.Settings.GetString("addr"))
}
