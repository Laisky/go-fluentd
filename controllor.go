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
	"github.com/Laisky/go-concator/senders"
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
		MsgPool:           c.msgPool,
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
		NFork:           utils.Settings.GetInt("settings.acceptor_filters.fork"),
	},
		acceptorFilters.NewSparkFilter(&acceptorFilters.SparkFilterCfg{
			Tag:         "spark." + env,
			MsgKey:      utils.Settings.GetString("settings.acceptor_filters.tenants.spark.msg_key"),
			Identifier:  utils.Settings.GetString("settings.acceptor_filters.tenants.spark.identifier"),
			IgnoreRegex: regexp.MustCompile(utils.Settings.GetString("settings.acceptor_filters.tenants.spark.ignore_regex")),
		}),
		acceptorFilters.NewSpringFilter(&acceptorFilters.SpringFilterCfg{
			Tag:    "spring." + env,
			Env:    env,
			MsgKey: utils.Settings.GetString("settings.acceptor_filters.tenants.spring.msg_key"),
			Rules:  acceptorFilters.ParseSpringRules(env, utils.Settings.Get("settings.acceptor_filters.tenants.spring.rules").([]interface{})),
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
		tagFilters.NewParserFact(&tagFilters.ParserFactCfg{ // connector
			Name:            "connector",
			Env:             env,
			Tags:            utils.Settings.GetStringSlice("settings.tag_filters.tenants.connector.tags"),
			MsgKey:          utils.Settings.GetString("settings.tag_filters.tenants.connector.msg_key"),
			Regexp:          regexp.MustCompile(utils.Settings.GetString("settings.tag_filters.tenants.connector.pattern")),
			IsRemoveOrigLog: utils.Settings.GetBool("settings.tag_filters.tenants.connector.is_remove_orig_log"),
			MsgPool:         c.msgPool,
			ParseJsonKey:    utils.Settings.GetString("settings.tag_filters.tenants.connector.parse_json_key"),
			Add:             tagFilters.ParseAddCfg(env, utils.Settings.Get("settings.tag_filters.tenants.connector.add")),
			MustInclude:     utils.Settings.GetString("settings.tag_filters.tenants.connector.must_include"),
		}),
		tagFilters.NewParserFact(&tagFilters.ParserFactCfg{ // spring
			Name:            "spring",
			Env:             env,
			Tags:            utils.Settings.GetStringSlice("settings.tag_filters.tenants.spring.tags"),
			MsgKey:          utils.Settings.GetString("settings.tag_filters.tenants.spring.msg_key"),
			Regexp:          regexp.MustCompile(utils.Settings.GetString("settings.tag_filters.tenants.spring.pattern")),
			IsRemoveOrigLog: utils.Settings.GetBool("settings.tag_filters.tenants.spring.is_remove_orig_log"),
			MsgPool:         c.msgPool,
			Add:             tagFilters.ParseAddCfg(env, utils.Settings.Get("settings.tag_filters.tenants.spring.add")),
			MustInclude:     utils.Settings.GetString("settings.tag_filters.tenants.spring.must_include"),
		}),
		tagFilters.NewParserFact(&tagFilters.ParserFactCfg{ // geely
			Name:            "geely",
			Env:             env,
			Tags:            utils.Settings.GetStringSlice("settings.tag_filters.tenants.geely.tags"),
			MsgKey:          utils.Settings.GetString("settings.tag_filters.tenants.geely.msg_key"),
			Regexp:          regexp.MustCompile(utils.Settings.GetString("settings.tag_filters.tenants.geely.pattern")),
			IsRemoveOrigLog: utils.Settings.GetBool("settings.tag_filters.tenants.geely.is_remove_orig_log"),
			MsgPool:         c.msgPool,
			Add:             tagFilters.ParseAddCfg(env, utils.Settings.Get("settings.tag_filters.tenants.geely.add")),
			MustInclude:     utils.Settings.GetString("settings.tag_filters.tenants.geely.must_include"),
		}),
		tagFilters.NewParserFact(&tagFilters.ParserFactCfg{ // ptdeloyer
			Name:            "ptdeloyer",
			Env:             env,
			Tags:            utils.Settings.GetStringSlice("settings.tag_filters.tenants.ptdeployer.tags"),
			MsgKey:          utils.Settings.GetString("settings.tag_filters.tenants.ptdeployer.msg_key"),
			Regexp:          regexp.MustCompile(utils.Settings.GetString("settings.tag_filters.tenants.ptdeployer.pattern")),
			IsRemoveOrigLog: utils.Settings.GetBool("settings.tag_filters.tenants.ptdeployer.is_remove_orig_log"),
			MsgPool:         c.msgPool,
			Add:             tagFilters.ParseAddCfg(env, utils.Settings.Get("settings.tag_filters.tenants.ptdeployer.add")),
			MustInclude:     utils.Settings.GetString("settings.tag_filters.tenants.ptdeployer.must_include"),
		}),
		tagFilters.NewParserFact(&tagFilters.ParserFactCfg{ // cp
			Name:            "cp",
			Env:             env,
			Tags:            utils.Settings.GetStringSlice("settings.tag_filters.tenants.cp.tags"),
			MsgKey:          utils.Settings.GetString("settings.tag_filters.tenants.cp.msg_key"),
			Regexp:          regexp.MustCompile(utils.Settings.GetString("settings.tag_filters.tenants.cp.pattern")),
			IsRemoveOrigLog: utils.Settings.GetBool("settings.tag_filters.tenants.cp.is_remove_orig_log"),
			MsgPool:         c.msgPool,
			Add:             tagFilters.ParseAddCfg(env, utils.Settings.Get("settings.tag_filters.tenants.cp.add")),
			MustInclude:     utils.Settings.GetString("settings.tag_filters.tenants.cp.must_include"),
		}),
		tagFilters.NewParserFact(&tagFilters.ParserFactCfg{ // spark
			Name:            "spark",
			Env:             env,
			Tags:            utils.Settings.GetStringSlice("settings.tag_filters.tenants.spark.tags"),
			MsgKey:          utils.Settings.GetString("settings.tag_filters.tenants.spark.msg_key"),
			Regexp:          regexp.MustCompile(utils.Settings.GetString("settings.tag_filters.tenants.spark.pattern")),
			IsRemoveOrigLog: utils.Settings.GetBool("settings.tag_filters.tenants.spark.is_remove_orig_log"),
			MsgPool:         c.msgPool,
			Add:             tagFilters.ParseAddCfg(env, utils.Settings.Get("settings.tag_filters.tenants.spark.add")),
			MustInclude:     utils.Settings.GetString("settings.tag_filters.tenants.spark.must_include"),
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
		NFork:           utils.Settings.GetInt("settings.post_filters.fork"),
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

func (c *Controllor) initProducer(env string, waitProduceChan chan *libs.FluentMsg, commitChan chan<- int64) *Producer {
	return NewProducer(
		&ProducerCfg{
			InChan:          waitProduceChan,
			MsgPool:         c.msgPool,
			CommitChan:      commitChan,
			DiscardChanSize: utils.Settings.GetInt("settings.producer.discard_chan_size"),
		},
		// senders...
		// senders.NewFluentSender(&senders.FluentSenderCfg{ // fluentd backend
		// 	Addr:          utils.Settings.GetString("settings.producer.tenants.fluentd.addr"),
		// 	BatchSize:     utils.Settings.GetInt("settings.producer.tenants.fluentd.msg_batch_size"),
		// 	MaxWait:       utils.Settings.GetDuration("settings.producer.tenants.fluentd.max_wait_sec") * time.Second,
		// 	RetryChanSize: utils.Settings.GetInt("settings.producer.tenants.fluentd.retry_chan_len"),
		// 	InChanSize:    utils.Settings.GetInt("settings.producer.sender_inchan_size"),
		// 	NFork:         utils.Settings.GetInt("settings.producer.tenants.fluentd.forks"),
		// 	Tags:          senders.TagsAppendEnv(env, utils.Settings.GetStringSlice("settings.producer.tenants.fluentd.tags")),
		// }),
		senders.NewKafkaSender(&senders.KafkaSenderCfg{ // kafka backend
			Name:          "KafkaSender",
			Brokers:       utils.Settings.GetStringSlice("settings.producer.tenants.kafka.brokers"),
			Topic:         utils.Settings.GetString("settings.producer.tenants.kafka.topic." + env),
			BatchSize:     utils.Settings.GetInt("settings.producer.tenants.kafka.msg_batch_size"),
			MaxWait:       utils.Settings.GetDuration("settings.producer.tenants.kafka.max_wait_sec") * time.Second,
			RetryChanSize: utils.Settings.GetInt("settings.producer.tenants.kafka.retry_chan_len"),
			InChanSize:    utils.Settings.GetInt("settings.producer.sender_inchan_size"),
			NFork:         utils.Settings.GetInt("settings.producer.tenants.kafka.forks"),
			Tags:          senders.TagsAppendEnv(env, utils.Settings.GetStringSlice("settings.producer.tenants.kafka.tags")),
		}),
		senders.NewKafkaSender(&senders.KafkaSenderCfg{ // cp kafka backend
			Name:          "KafkaCpSender",
			Brokers:       utils.Settings.GetStringSlice("settings.producer.tenants.kafka_cp.brokers." + env),
			Topic:         utils.Settings.GetString("settings.producer.tenants.kafka_cp.topic"),
			BatchSize:     utils.Settings.GetInt("settings.producer.tenants.kafka_cp.msg_batch_size"),
			MaxWait:       utils.Settings.GetDuration("settings.producer.tenants.kafka_cp.max_wait_sec") * time.Second,
			RetryChanSize: utils.Settings.GetInt("settings.producer.tenants.kafka_cp.retry_chan_len"),
			InChanSize:    utils.Settings.GetInt("settings.producer.sender_inchan_size"),
			NFork:         utils.Settings.GetInt("settings.producer.tenants.kafka_cp.forks"),
			Tags:          senders.TagsAppendEnv(env, utils.Settings.GetStringSlice("settings.producer.tenants.kafka_cp.tags")),
		}),
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
	producer := c.initProducer(env, waitProduceChan, waitCommitChan)

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

	go producer.Run()

	RunServer(utils.Settings.GetString("addr"))
}
