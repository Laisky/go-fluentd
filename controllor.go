package concator

import (
	"regexp"
	"runtime"
	"sync"
	"time"

	"github.com/Laisky/go-fluentd/acceptorFilters"
	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-fluentd/monitor"
	"github.com/Laisky/go-fluentd/postFilters"
	"github.com/Laisky/go-fluentd/recvs"
	"github.com/Laisky/go-fluentd/senders"
	"github.com/Laisky/go-fluentd/tagFilters"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/go-utils/kafka"
	"github.com/Laisky/zap"
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

func (c *Controllor) initRecvs(env string) []recvs.AcceptorRecvItf {
	// init tcp recvs
	receivers := []recvs.AcceptorRecvItf{}

	// init kafka tenants recvs
	sharingKMsgPool := &sync.Pool{
		New: func() interface{} {
			return &kafka.KafkaMsg{}
		},
	}

	for name := range utils.Settings.Get("settings.acceptor.recvs.tenants").(map[string]interface{}) {
		switch utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".type") {
		case "fluentd":
			if StringListContains(utils.Settings.GetStringSlice("settings.acceptor.recvs.tenants."+name+".active_env"), env) {
				receivers = append(receivers, recvs.NewFluentdRecv(&recvs.FluentdRecvCfg{
					Name:                   name,
					Addr:                   utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".addr"),
					TagKey:                 utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".tag_key"),
					IsRewriteTagFromTagKey: utils.Settings.GetBool("settings.acceptor.recvs.tenants." + name + ".is_rewrite_tag_from_tag_key"),
				}))
			}
		case "rsyslog":
			receivers = append(receivers, recvs.NewRsyslogRecv(&recvs.RsyslogCfg{
				Name:          name,
				Addr:          utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".addr"),
				Env:           env,
				TagKey:        utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".tag_key"),
				MsgKey:        utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".msg_key"),
				NewTimeFormat: utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".new_time_format"),
				TimeKey:       utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".time_key"),
				NewTimeKey:    utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".new_time_key"),
			}))
		case "http":
			receivers = append(receivers, recvs.NewHTTPRecv(&recvs.HTTPRecvCfg{ // wechat mini program
				Name:               name,
				HTTPSrv:            Server,
				Env:                env,
				MsgKey:             utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".msg_key"),
				TagKey:             utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".tag_key"),
				OrigTag:            utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".orig_tag"),
				Tag:                utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".tag"),
				Path:               utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".path"),
				SigSalt:            []byte(utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".signature_salt")),
				MaxBodySize:        utils.Settings.GetInt64("settings.acceptor.recvs.tenants." + name + ".max_body_byte"),
				TSRegexp:           regexp.MustCompile(utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".ts_regexp")),
				TimeFormat:         utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".time_format"),
				MaxAllowedDelaySec: utils.Settings.GetDuration("settings.acceptor.recvs.tenants."+name+".max_allowed_delay_sec") * time.Second,
				MaxAllowedAheadSec: utils.Settings.GetDuration("settings.acceptor.recvs.tenants."+name+".max_allowed_ahead_sec") * time.Second,
			}))
		case "kafka":
			receivers = append(receivers, recvs.NewKafkaRecv(&recvs.KafkaCfg{
				KMsgPool: sharingKMsgPool,
				Meta: utils.FallBack(
					func() interface{} {
						return utils.Settings.Get("settings.acceptor.recvs.tenants." + name + ".meta").(map[string]interface{})
					}, map[string]interface{}{}).(map[string]interface{}),
				Name:         name,
				MsgKey:       utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".msg_key"),
				Brokers:      utils.Settings.GetStringSlice("settings.acceptor.recvs.tenants." + name + ".brokers." + env),
				Topics:       []string{utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".topics." + env)},
				Group:        utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".groups." + env),
				Tag:          utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".tags." + env),
				IsJsonFormat: utils.Settings.GetBool("settings.acceptor.recvs.tenants." + name + ".is_json_format"),
				TagKey:       utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".tag_key"),
				JsonTagKey:   utils.Settings.GetString("settings.acceptor.recvs.tenants." + name + ".json_tag_key"),
				RewriteTag:   recvs.GetKafkaRewriteTag(utils.Settings.GetString("settings.acceptor.recvs.tenants."+name+".rewrite_tag"), env),
				NConsumer:    utils.Settings.GetInt("settings.acceptor.recvs.tenants." + name + ".nconsumer"),
				KafkaCommitCfg: &recvs.KafkaCommitCfg{
					IntervalNum:      utils.Settings.GetInt("settings.acceptor.recvs.tenants." + name + ".interval_num"),
					IntervalDuration: utils.Settings.GetDuration("settings.acceptor.recvs.tenants."+name+".interval_sec") * time.Second,
				},
			}))
		default:
			utils.Logger.Panic("unknown recv type",
				zap.String("recv_type", utils.Settings.GetString("settings.acceptor.recvs.tenants."+name+".type")),
				zap.String("recv_name", name))
		}
		utils.Logger.Info("active recv",
			zap.String("name", name),
			zap.String("type", utils.Settings.GetString("settings.acceptor.recvs.tenants."+name+".type")))
	}

	return receivers
}

func (c *Controllor) initAcceptor(journal *Journal, receivers []recvs.AcceptorRecvItf) *Acceptor {
	acceptor := NewAcceptor(&AcceptorCfg{
		MsgPool:          c.msgPool,
		Journal:          journal,
		MaxRotateId:      utils.Settings.GetInt64("settings.acceptor.max_rotate_id"),
		AsyncOutChanSize: utils.Settings.GetInt("settings.acceptor.async_out_chan_size"),
		SyncOutChanSize:  utils.Settings.GetInt("settings.acceptor.sync_out_chan_size"),
	},
		receivers...,
	)

	acceptor.Run()
	return acceptor
}

func (c *Controllor) initAcceptorPipeline(env string) *acceptorFilters.AcceptorPipeline {
	afs := []acceptorFilters.AcceptorFilterItf{}
	for name := range utils.Settings.Get("settings.acceptor_filters.tenants").(map[string]interface{}) {
		switch utils.Settings.GetString("settings.acceptor_filters.tenants." + name + ".type") {
		case "spark":
			afs = append(afs, acceptorFilters.NewSparkFilter(&acceptorFilters.SparkFilterCfg{
				Tag:         "spark." + env,
				Name:        name,
				MsgKey:      utils.Settings.GetString("settings.acceptor_filters.tenants." + name + ".msg_key"),
				Identifier:  utils.Settings.GetString("settings.acceptor_filters.tenants." + name + ".identifier"),
				IgnoreRegex: regexp.MustCompile(utils.Settings.GetString("settings.acceptor_filters.tenants." + name + ".ignore_regex")),
			}))
		case "spring":
			afs = append(afs, acceptorFilters.NewSpringFilter(&acceptorFilters.SpringFilterCfg{
				Tag:    "spring." + env,
				Name:   name,
				Env:    env,
				MsgKey: utils.Settings.GetString("settings.acceptor_filters.tenants." + name + ".msg_key"),
				TagKey: utils.Settings.GetString("settings.acceptor_filters.tenants." + name + ".tag_key"),
				Rules:  acceptorFilters.ParseSpringRules(env, utils.Settings.Get("settings.acceptor_filters.tenants."+name+".rules").([]interface{})),
			}))
		default:
			utils.Logger.Panic("unknown acceptorfilter type",
				zap.String("recv_type", utils.Settings.GetString("settings.acceptor_filters.tenants."+name+".type")),
				zap.String("recv_name", name))
		}
		utils.Logger.Info("active acceptorfilter",
			zap.String("name", name),
			zap.String("type", utils.Settings.GetString("settings.acceptor_filters.recvs.tenants."+name+".type")))
	}

	// set the DefaultFilter as last filter
	afs = append(afs, acceptorFilters.NewDefaultFilter(&acceptorFilters.DefaultFilterCfg{
		Name:               "default",
		RemoveEmptyTag:     true,
		RemoveUnsupportTag: true,
		Env:                env,
		SupportedTags:      utils.Settings.GetStringSlice("consts.tags.all-tags"),
	}))

	return acceptorFilters.NewAcceptorPipeline(&acceptorFilters.AcceptorPipelineCfg{
		OutChanSize:     utils.Settings.GetInt("settings.acceptor_filters.out_buf_len"),
		MsgPool:         c.msgPool,
		ReEnterChanSize: utils.Settings.GetInt("settings.acceptor_filters.reenter_chan_len"),
		NFork:           utils.Settings.GetInt("settings.acceptor_filters.fork"),
		IsThrottle:      utils.Settings.GetBool("settings.acceptor_filters.is_throttle"),
		ThrottleMax:     utils.Settings.GetInt("settings.acceptor_filters.throttle_max"),
		ThrottleNPerSec: utils.Settings.GetInt("settings.acceptor_filters.throttle_per_sec"),
	},
		afs...,
	)
}

func (c *Controllor) initTagPipeline(env string, waitCommitChan chan<- int64) *tagFilters.TagPipeline {
	fs := []tagFilters.TagFilterFactoryItf{}
	for name := range utils.Settings.Get("settings.tag_filters.tenants").(map[string]interface{}) {
		switch utils.Settings.GetString("settings.tag_filters.tenants." + name + ".type") {
		case "parser":
			fs = append(fs, tagFilters.NewParserFact(&tagFilters.ParserFactCfg{
				Name:            name,
				Env:             env,
				Tags:            utils.Settings.GetStringSlice("settings.tag_filters.tenants." + name + ".tags"),
				MsgKey:          utils.Settings.GetString("settings.tag_filters.tenants." + name + ".msg_key"),
				Regexp:          regexp.MustCompile(utils.Settings.GetString("settings.tag_filters.tenants." + name + ".pattern")),
				IsRemoveOrigLog: utils.Settings.GetBool("settings.tag_filters.tenants." + name + ".is_remove_orig_log"),
				MsgPool:         c.msgPool,
				ParseJsonKey:    utils.Settings.GetString("settings.tag_filters.tenants." + name + ".parse_json_key"),
				Add:             tagFilters.ParseAddCfg(env, utils.Settings.Get("settings.tag_filters.tenants."+name+".add")),
				MustInclude:     utils.Settings.GetString("settings.tag_filters.tenants." + name + ".must_include"),
				TimeKey:         utils.Settings.GetString("settings.tag_filters.tenants." + name + ".time_key"),
				TimeFormat:      utils.Settings.GetString("settings.tag_filters.tenants." + name + ".time_format"),
				NewTimeFormat:   utils.Settings.GetString("settings.tag_filters.tenants." + name + ".new_time_format"),
				ReservedTimeKey: utils.Settings.GetBool("settings.tag_filters.tenants." + name + ".reserved_time_key"),
				NewTimeKey:      utils.Settings.GetString("settings.tag_filters.tenants." + name + ".new_time_key"),
				AppendTimeZone:  utils.Settings.GetString("settings.tag_filters.tenants." + name + ".append_time_zone." + env),
			}))
		case "concator":
		default:
			utils.Logger.Panic("unknown tagfilter type",
				zap.String("recv_type", utils.Settings.GetString("settings.tag_filters.recvs.tenants."+name+".type")),
				zap.String("recv_name", name))
		}
		utils.Logger.Info("active tagfilter",
			zap.String("name", name),
			zap.String("type", utils.Settings.GetString("settings.tag_filters.recvs.tenants."+name+".type")))
	}

	// concatorFilter must in the front
	fs = append([]tagFilters.TagFilterFactoryItf{tagFilters.NewConcatorFact(&tagFilters.ConcatorFactCfg{
		MaxLen:       utils.Settings.GetInt("settings.tag_filters.tenants.concator.config.max_length"),
		ConcatorCfgs: libs.LoadConcatorTagConfigs(env, utils.Settings.Get("settings.tag_filters.tenants.concator.tenants").(map[string]interface{})),
	})}, fs...)

	return tagFilters.NewTagPipeline(&tagFilters.TagPipelineCfg{
		MsgPool:                 c.msgPool,
		CommitedChan:            waitCommitChan,
		DefaultInternalChanSize: utils.Settings.GetInt("settings.tag_filters.internal_chan_size"),
	},
		fs...,
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
		postFilters.NewESDispatcherFilter(&postFilters.ESDispatcherFilterCfg{
			Tags:     libs.LoadTagsAppendEnv(env, utils.Settings.GetStringSlice("settings.post_filters.tenants.es_dispatcher.tags")),
			TagKey:   utils.Settings.GetString("settings.post_filters.tenants.es_dispatcher.tag_key"),
			ReTagMap: postFilters.LoadReTagMap(env, utils.Settings.Get("settings.post_filters.tenants.es_dispatcher.rewrite_tag_map")),
		}),
		postFilters.NewForwardTagRewriterFilter(&postFilters.ForwardTagRewriterFilterCfg{ // wechat mini program
			Tag:    utils.Settings.GetString("settings.post_filters.tenants.forward_tag_rewriter.tag") + "." + env,
			TagKey: utils.Settings.GetString("settings.post_filters.tenants.forward_tag_rewriter.tag_key"),
		}),
	)
}

func StringListContains(ls []string, v string) bool {
	for _, vi := range ls {
		if vi == v {
			return true
		}
	}

	return false
}

func (c *Controllor) initSenders(env string) []senders.SenderItf {
	ss := []senders.SenderItf{}

	for name := range utils.Settings.Get("settings.producer.tenants").(map[string]interface{}) {
		switch utils.Settings.GetString("settings.producer.tenants." + name + ".type") {
		case "fluentd":
			if StringListContains(utils.Settings.GetStringSlice("settings.producer.tenants."+name+".active_env"), env) {
				ss = append(ss, senders.NewFluentSender(&senders.FluentSenderCfg{
					Name:                 name,
					Addr:                 utils.Settings.GetString("settings.producer.tenants." + name + ".addr"),
					BatchSize:            utils.Settings.GetInt("settings.producer.tenants." + name + ".msg_batch_size"),
					MaxWait:              utils.Settings.GetDuration("settings.producer.tenants."+name+".max_wait_sec") * time.Second,
					RetryChanSize:        utils.Settings.GetInt("settings.producer.tenants." + name + ".retry_chan_len"),
					InChanSize:           utils.Settings.GetInt("settings.producer.sender_inchan_size"),
					NFork:                utils.Settings.GetInt("settings.producer.tenants." + name + ".forks"),
					Tags:                 utils.Settings.GetStringSlice("settings.producer.tenants." + name + ".tags"), // do not append env
					IsDiscardWhenBlocked: utils.Settings.GetBool("settings.producer.tenants." + name + ".is_discard_when_blocked"),
				}))
			}
		case "kafka":
			if StringListContains(utils.Settings.GetStringSlice("settings.producer.tenants."+name+".active_env"), env) {
				ss = append(ss, senders.NewKafkaSender(&senders.KafkaSenderCfg{
					Name:                 name,
					Brokers:              utils.Settings.GetStringSlice("settings.producer.tenants." + name + ".brokers." + env),
					Topic:                utils.Settings.GetString("settings.producer.tenants." + name + ".topic"),
					TagKey:               utils.Settings.GetString("settings.producer.tenants." + name + ".tag_key"),
					BatchSize:            utils.Settings.GetInt("settings.producer.tenants." + name + ".msg_batch_size"),
					MaxWait:              utils.Settings.GetDuration("settings.producer.tenants."+name+".max_wait_sec") * time.Second,
					RetryChanSize:        utils.Settings.GetInt("settings.producer.tenants." + name + ".retry_chan_len"),
					InChanSize:           utils.Settings.GetInt("settings.producer.sender_inchan_size"),
					NFork:                utils.Settings.GetInt("settings.producer.tenants." + name + ".forks"),
					Tags:                 libs.LoadTagsAppendEnv(env, utils.Settings.GetStringSlice("settings.producer.tenants."+name+".tags")),
					IsDiscardWhenBlocked: utils.Settings.GetBool("settings.producer.tenants." + name + ".is_discard_when_blocked"),
				}))
			}
		case "es":
			if StringListContains(utils.Settings.GetStringSlice("settings.producer.tenants."+name+".active_env"), env) {
				ss = append(ss, senders.NewElasticSearchSender(&senders.ElasticSearchSenderCfg{
					Name:                 name,
					BatchSize:            utils.Settings.GetInt("settings.producer.tenants." + name + ".msg_batch_size"),
					Addr:                 utils.Settings.GetString("settings.producer.tenants." + name + ".addr"),
					MaxWait:              utils.Settings.GetDuration("settings.producer.tenants."+name+".max_wait_sec") * time.Second,
					RetryChanSize:        utils.Settings.GetInt("settings.producer.tenants." + name + ".retry_chan_len"),
					InChanSize:           utils.Settings.GetInt("settings.producer.sender_inchan_size"),
					NFork:                utils.Settings.GetInt("settings.producer.tenants." + name + ".forks"),
					TagKey:               utils.Settings.GetString("settings.producer.tenants." + name + ".tag_key"),
					Tags:                 libs.LoadTagsAppendEnv(env, utils.Settings.GetStringSlice("settings.producer.tenants."+name+".tags")),
					TagIndexMap:          senders.LoadESTagIndexMap(env, utils.Settings.Get("settings.producer.tenants."+name+".indices")),
					IsDiscardWhenBlocked: utils.Settings.GetBool("settings.producer.tenants." + name + ".is_discard_when_blocked"),
				}))
			}
		default:
			utils.Logger.Panic("unknown sender type",
				zap.String("sender_type", utils.Settings.GetString("settings.producer.tenants."+name+".type")),
				zap.String("sender_name", name))
		}
		utils.Logger.Info("active sender",
			zap.String("type", utils.Settings.GetString("settings.producer.tenants."+name+".type")),
			zap.String("name", name),
			zap.String("env", env))
	}

	return ss
}

func (c *Controllor) initProducer(env string, waitProduceChan chan *libs.FluentMsg, commitChan chan<- int64, senders []senders.SenderItf) *Producer {
	return NewProducer(
		&ProducerCfg{
			InChan:          waitProduceChan,
			MsgPool:         c.msgPool,
			CommitChan:      commitChan,
			DiscardChanSize: utils.Settings.GetInt("settings.producer.discard_chan_size"),
		},
		// senders...
		senders...,
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
	waitAccepPipelineSyncChan := acceptor.GetSyncOutChan()
	waitAccepPipelineAsyncChan := acceptor.GetAsyncOutChan()
	waitDumpChan, skipDumpChan := acceptorPipeline.Wrap(waitAccepPipelineAsyncChan, waitAccepPipelineSyncChan)

	// after `journal.DumpMsgFlow`, every discarded msg should commit to waitCommitChan
	waitDispatchChan := journal.DumpMsgFlow(c.msgPool, waitDumpChan, skipDumpChan)

	tagPipeline := c.initTagPipeline(env, waitCommitChan)
	dispatcher := c.initDispatcher(waitDispatchChan, tagPipeline)
	waitPostPipelineChan := dispatcher.GetOutChan()
	postPipeline := c.initPostPipeline(env, waitCommitChan)
	waitProduceChan := postPipeline.Wrap(waitPostPipelineChan)
	producerSenders := c.initSenders(env)
	producer := c.initProducer(env, waitProduceChan, waitCommitChan, producerSenders)

	// heartbeat
	go c.runHeartBeat()

	// monitor
	monitor.AddMetric("controllor", func() map[string]interface{} {
		return map[string]interface{}{
			"goroutine":                     runtime.NumGoroutine(),
			"waitAccepPipelineSyncChanLen":  len(waitAccepPipelineSyncChan),
			"waitAccepPipelineSyncChanCap":  cap(waitAccepPipelineSyncChan),
			"waitAccepPipelineAsyncChanLen": len(waitAccepPipelineAsyncChan),
			"waitAccepPipelineAsyncChanCap": cap(waitAccepPipelineAsyncChan),
			"waitDumpChanLen":               len(waitDumpChan),
			"waitDumpChanCap":               cap(waitDumpChan),
			"skipDumpChanLen":               len(skipDumpChan),
			"skipDumpChanCap":               cap(skipDumpChan),
			"waitDispatchChanLen":           len(waitDispatchChan),
			"waitDispatchChanCap":           cap(waitDispatchChan),
			"waitPostPipelineChanLen":       len(waitPostPipelineChan),
			"waitPostPipelineChanCap":       cap(waitPostPipelineChan),
			"waitProduceChanLen":            len(waitProduceChan),
			"waitProduceChanCap":            cap(waitProduceChan),
			"waitCommitChanLen":             len(waitCommitChan),
			"waitCommitChanCap":             cap(waitCommitChan),
		}
	})
	monitor.BindHTTP(Server)

	go producer.Run()
	RunServer(utils.Settings.GetString("addr"))
}
