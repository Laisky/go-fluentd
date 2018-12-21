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

func (c *Controllor) initRecvs(env string) []recvs.AcceptorRecvItf {
	// init tcp recvs
	receivers := []recvs.AcceptorRecvItf{
		recvs.NewFluentdRecv(&recvs.FluentdRecvCfg{
			Addr:   utils.Settings.GetString("settings.acceptor.recvs.fluentd.addr"), // FluentdRec
			TagKey: utils.Settings.GetString("settings.acceptor.recvs.fluentd.tag_key"),
		}),
		recvs.NewRsyslogRecv(&recvs.RsyslogCfg{
			Addr:   utils.Settings.GetString("settings.acceptor.recvs.rsyslog.addr"),
			Env:    env,
			TagKey: utils.Settings.GetString("settings.acceptor.recvs.rsyslog.tag_key"),
		}),
	}
	// init kafka tenants recvs
	sharingKMsgPool := &sync.Pool{
		New: func() interface{} {
			return &kafka.KafkaMsg{}
		},
	}

	if tenants, ok := utils.Settings.Get("settings.acceptor.recvs.kafka_recvs.tenants").(map[string]interface{}); ok {
		for prj := range tenants {
			utils.Logger.Info("starting kafka recvs", zap.String("project", prj))
			receivers = append(receivers, recvs.NewKafkaRecv(&recvs.KafkaCfg{
				KMsgPool: sharingKMsgPool,
				Meta: utils.FallBack(
					func() interface{} {
						return utils.Settings.Get("settings.acceptor.recvs.kafka_recvs.tenants." + prj + ".meta").(map[string]interface{})
					}, map[string]interface{}{}).(map[string]interface{}),
				Name:         utils.Settings.GetString("settings.acceptor.recvs.kafka_recvs.tenants." + prj + ".name"),
				MsgKey:       utils.Settings.GetString("settings.acceptor.recvs.kafka_recvs.tenants." + prj + ".msg_key"),
				Brokers:      utils.Settings.GetStringSlice("settings.acceptor.recvs.kafka_recvs.tenants." + prj + ".brokers." + env),
				Topics:       []string{utils.Settings.GetString("settings.acceptor.recvs.kafka_recvs.tenants." + prj + ".topics." + env)},
				Group:        utils.Settings.GetString("settings.acceptor.recvs.kafka_recvs.tenants." + prj + ".groups." + env),
				Tag:          utils.Settings.GetString("settings.acceptor.recvs.kafka_recvs.tenants." + prj + ".tags." + env),
				IsJsonFormat: utils.Settings.GetBool("settings.acceptor.recvs.kafka_recvs.tenants." + prj + ".is_json_format"),
				TagKey:       utils.Settings.GetString("settings.acceptor.recvs.kafka_recvs.tenants." + prj + ".tag_key"),
				JsonTagKey:   utils.Settings.GetString("settings.acceptor.recvs.kafka_recvs.tenants." + prj + ".json_tag_key"),
				RewriteTag:   recvs.GetKafkaRewriteTag(utils.Settings.GetString("settings.acceptor.recvs.kafka_recvs.tenants."+prj+".rewrite_tag"), env),
				NConsumer:    utils.Settings.GetInt("settings.acceptor.recvs.kafka_recvs.tenants." + prj + ".nconsumer"),
				KafkaCommitCfg: &recvs.KafkaCommitCfg{
					IntervalNum:      utils.Settings.GetInt("settings.acceptor.recvs.kafka_recvs.interval_num"),
					IntervalDuration: utils.Settings.GetDuration("settings.acceptor.recvs.kafka_recvs.interval_sec") * time.Second,
				},
			}))
		}
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
			TagKey: utils.Settings.GetString("settings.acceptor_filters.tenants.spring.tag_key"),
			Rules:  acceptorFilters.ParseSpringRules(env, utils.Settings.Get("settings.acceptor_filters.tenants.spring.rules").([]interface{})),
		}),
		// set the DefaultFilter as last filter
		acceptorFilters.NewDefaultFilter(&acceptorFilters.DefaultFilterCfg{
			RemoveEmptyTag:     true,
			RemoveUnsupportTag: true,
			Env:                env,
			SupportedTags:      utils.Settings.GetStringSlice("settings.producer.tenants.fluentd.tags"),
		}),
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
			TimeKey:         utils.Settings.GetString("settings.tag_filters.tenants.connector.time_key"),
			TimeFormat:      utils.Settings.GetString("settings.tag_filters.tenants.connector.time_format"),
			NewTimeFormat:   utils.Settings.GetString("settings.tag_filters.tenants.connector.new_time_format"),
			ReservedTimeKey: utils.Settings.GetBool("settings.tag_filters.tenants.connector.reserved_time_key"),
			NewTimeKey:      utils.Settings.GetString("settings.tag_filters.tenants.connector.new_time_key"),
			AppendTimeZone:  utils.Settings.GetString("settings.tag_filters.tenants.connector.append_time_zone"),
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
			TimeKey:         utils.Settings.GetString("settings.tag_filters.tenants.spring.time_key"),
			TimeFormat:      utils.Settings.GetString("settings.tag_filters.tenants.spring.time_format"),
			NewTimeFormat:   utils.Settings.GetString("settings.tag_filters.tenants.spring.new_time_format"),
			ReservedTimeKey: utils.Settings.GetBool("settings.tag_filters.tenants.spring.reserved_time_key"),
			NewTimeKey:      utils.Settings.GetString("settings.tag_filters.tenants.spring.new_time_key"),
			AppendTimeZone:  utils.Settings.GetString("settings.tag_filters.tenants.spring.append_time_zone"),
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
			TimeKey:         utils.Settings.GetString("settings.tag_filters.tenants.geely.time_key"),
			TimeFormat:      utils.Settings.GetString("settings.tag_filters.tenants.geely.time_format"),
			NewTimeFormat:   utils.Settings.GetString("settings.tag_filters.tenants.geely.new_time_format"),
			ReservedTimeKey: utils.Settings.GetBool("settings.tag_filters.tenants.geely.reserved_time_key"),
			NewTimeKey:      utils.Settings.GetString("settings.tag_filters.tenants.geely.new_time_key"),
			AppendTimeZone:  utils.Settings.GetString("settings.tag_filters.tenants.geely.append_time_zone"),
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
			TimeKey:         utils.Settings.GetString("settings.tag_filters.tenants.ptdeployer.time_key"),
			TimeFormat:      utils.Settings.GetString("settings.tag_filters.tenants.ptdeployer.time_format"),
			NewTimeFormat:   utils.Settings.GetString("settings.tag_filters.tenants.ptdeployer.new_time_format"),
			ReservedTimeKey: utils.Settings.GetBool("settings.tag_filters.tenants.ptdeployer.reserved_time_key"),
			NewTimeKey:      utils.Settings.GetString("settings.tag_filters.tenants.ptdeployer.new_time_key"),
			AppendTimeZone:  utils.Settings.GetString("settings.tag_filters.tenants.ptdeployer.append_time_zone"),
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
			TimeKey:         utils.Settings.GetString("settings.tag_filters.tenants.cp.time_key"),
			TimeFormat:      utils.Settings.GetString("settings.tag_filters.tenants.cp.time_format"),
			NewTimeFormat:   utils.Settings.GetString("settings.tag_filters.tenants.cp.new_time_format"),
			ReservedTimeKey: utils.Settings.GetBool("settings.tag_filters.tenants.cp.reserved_time_key"),
			NewTimeKey:      utils.Settings.GetString("settings.tag_filters.tenants.cp.new_time_key"),
			AppendTimeZone:  utils.Settings.GetString("settings.tag_filters.tenants.cp.append_time_zone"),
		}),
		tagFilters.NewParserFact(&tagFilters.ParserFactCfg{ // emqtt
			Name:            "emqtt",
			Env:             env,
			Tags:            utils.Settings.GetStringSlice("settings.tag_filters.tenants.emqtt.tags"),
			MsgPool:         c.msgPool,
			Add:             tagFilters.ParseAddCfg(env, utils.Settings.Get("settings.tag_filters.tenants.emqtt.add")),
			TimeKey:         utils.Settings.GetString("settings.tag_filters.tenants.emqtt.time_key"),
			TimeFormat:      utils.Settings.GetString("settings.tag_filters.tenants.emqtt.time_format"),
			NewTimeFormat:   utils.Settings.GetString("settings.tag_filters.tenants.emqtt.new_time_format"),
			ReservedTimeKey: utils.Settings.GetBool("settings.tag_filters.tenants.emqtt.reserved_time_key"),
			NewTimeKey:      utils.Settings.GetString("settings.tag_filters.tenants.emqtt.new_time_key"),
			AppendTimeZone:  utils.Settings.GetString("settings.tag_filters.tenants.emqtt.append_time_zone." + env),
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
			TimeKey:         utils.Settings.GetString("settings.tag_filters.tenants.spark.time_key"),
			TimeFormat:      utils.Settings.GetString("settings.tag_filters.tenants.spark.time_format"),
			NewTimeFormat:   utils.Settings.GetString("settings.tag_filters.tenants.spark.new_time_format"),
			ReservedTimeKey: utils.Settings.GetBool("settings.tag_filters.tenants.spark.reserved_time_key"),
			NewTimeKey:      utils.Settings.GetString("settings.tag_filters.tenants.spark.new_time_key"),
			AppendTimeZone:  utils.Settings.GetString("settings.tag_filters.tenants.spark.append_time_zone." + env),
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
		postFilters.NewESDispatcherFilter(&postFilters.ESDispatcherFilterCfg{
			Tag:      utils.Settings.GetString("settings.post_filters.tenants.es_dispatcher.tag") + "." + env,
			TagKey:   utils.Settings.GetString("settings.post_filters.tenants.es_dispatcher.tag_key"),
			ReTagMap: postFilters.LoadReTagMap(env, utils.Settings.Get("settings.post_filters.tenants.es_dispatcher.rewrite_tag_map")),
		}),
	)
}

func (c *Controllor) initSenders(env string) []senders.SenderItf {
	ss := []senders.SenderItf{
		// senders.NewFluentSender(&senders.FluentSenderCfg{ // fluentd backend
		// 	Name:          "FluentdSender",
		// 	Addr:          utils.Settings.GetString("settings.producer.tenants.fluentd.addr"),
		// 	BatchSize:     utils.Settings.GetInt("settings.producer.tenants.fluentd.msg_batch_size"),
		// 	MaxWait:       utils.Settings.GetDuration("settings.producer.tenants.fluentd.max_wait_sec") * time.Second,
		// 	RetryChanSize: utils.Settings.GetInt("settings.producer.tenants.fluentd.retry_chan_len"),
		// 	InChanSize:    utils.Settings.GetInt("settings.producer.sender_inchan_size"),
		// 	NFork:         utils.Settings.GetInt("settings.producer.tenants.fluentd.forks"),
		// 	Tags:          senders.TagsAppendEnv(env, utils.Settings.GetStringSlice("settings.producer.tenants.fluentd.tags")),
		//  IsDiscardWhenBlocked: utils.Settings.GetBool("settings.producer.tenants.fluentd.is_discard_when_blocked"),
		// }),
		senders.NewKafkaSender(&senders.KafkaSenderCfg{ // kafka buf
			Name:                 "KafkaBufSender",
			Brokers:              utils.Settings.GetStringSlice("settings.producer.tenants.kafkabuf.brokers"),
			Topic:                utils.Settings.GetString("settings.producer.tenants.kafkabuf.topic." + env),
			TagKey:               utils.Settings.GetString("settings.producer.tenants.kafkabuf.tag_key"),
			BatchSize:            utils.Settings.GetInt("settings.producer.tenants.kafkabuf.msg_batch_size"),
			MaxWait:              utils.Settings.GetDuration("settings.producer.tenants.kafkabuf.max_wait_sec") * time.Second,
			RetryChanSize:        utils.Settings.GetInt("settings.producer.tenants.kafkabuf.retry_chan_len"),
			InChanSize:           utils.Settings.GetInt("settings.producer.sender_inchan_size"),
			NFork:                utils.Settings.GetInt("settings.producer.tenants.kafkabuf.forks"),
			Tags:                 senders.TagsAppendEnv(env, utils.Settings.GetStringSlice("settings.producer.tenants.kafkabuf.tags")),
			IsDiscardWhenBlocked: utils.Settings.GetBool("settings.producer.tenants.kafkabuf.is_discard_when_blocked"),
		}),
		senders.NewKafkaSender(&senders.KafkaSenderCfg{ // cp kafka backend
			Name:                 "KafkaCpSender",
			Brokers:              utils.Settings.GetStringSlice("settings.producer.tenants.kafka_cp.brokers." + env),
			Topic:                utils.Settings.GetString("settings.producer.tenants.kafka_cp.topic"),
			TagKey:               utils.Settings.GetString("settings.producer.tenants.kafka_cp.tag_key"),
			BatchSize:            utils.Settings.GetInt("settings.producer.tenants.kafka_cp.msg_batch_size"),
			MaxWait:              utils.Settings.GetDuration("settings.producer.tenants.kafka_cp.max_wait_sec") * time.Second,
			RetryChanSize:        utils.Settings.GetInt("settings.producer.tenants.kafka_cp.retry_chan_len"),
			InChanSize:           utils.Settings.GetInt("settings.producer.sender_inchan_size"),
			NFork:                utils.Settings.GetInt("settings.producer.tenants.kafka_cp.forks"),
			Tags:                 senders.TagsAppendEnv(env, utils.Settings.GetStringSlice("settings.producer.tenants.kafka_cp.tags")),
			IsDiscardWhenBlocked: utils.Settings.GetBool("settings.producer.tenants.kafka_cp.is_discard_when_blocked"),
		}),
		senders.NewElasticSearchSender(&senders.ElasticSearchSenderCfg{ // general elasticsearch backend
			Name:                 "GeneralESSender",
			BatchSize:            utils.Settings.GetInt("settings.producer.tenants.es_general.msg_batch_size"),
			Addr:                 utils.Settings.GetString("settings.producer.tenants.es_general.addr"),
			MaxWait:              utils.Settings.GetDuration("settings.producer.tenants.es_general.max_wait_sec") * time.Second,
			RetryChanSize:        utils.Settings.GetInt("settings.producer.tenants.es_general.retry_chan_len"),
			InChanSize:           utils.Settings.GetInt("settings.producer.sender_inchan_size"),
			NFork:                utils.Settings.GetInt("settings.producer.tenants.es_general.forks"),
			TagKey:               utils.Settings.GetString("settings.producer.tenants.es_general.tag_key"),
			Tags:                 utils.Settings.GetStringSlice("settings.producer.tenants.es_general.tags"),
			TagIndexMap:          senders.LoadESTagIndexMap(env, utils.Settings.Get("settings.producer.tenants.es_general.indices")),
			IsDiscardWhenBlocked: utils.Settings.GetBool("settings.producer.tenants.es_general.is_discard_when_blocked"),
		}),
		senders.NewElasticSearchSender(&senders.ElasticSearchSenderCfg{ // geely elasticsearch backend
			Name:                 "GeelyESSender",
			BatchSize:            utils.Settings.GetInt("settings.producer.tenants.es_geely.msg_batch_size"),
			Addr:                 utils.Settings.GetString("settings.producer.tenants.es_geely.addr"),
			MaxWait:              utils.Settings.GetDuration("settings.producer.tenants.es_geely.max_wait_sec") * time.Second,
			RetryChanSize:        utils.Settings.GetInt("settings.producer.tenants.es_geely.retry_chan_len"),
			InChanSize:           utils.Settings.GetInt("settings.producer.sender_inchan_size"),
			NFork:                utils.Settings.GetInt("settings.producer.tenants.es_geely.forks"),
			TagKey:               utils.Settings.GetString("settings.producer.tenants.es_geely.tag_key"),
			Tags:                 utils.Settings.GetStringSlice("settings.producer.tenants.es_geely.tags"),
			TagIndexMap:          senders.LoadESTagIndexMap(env, utils.Settings.Get("settings.producer.tenants.es_geely.indices")),
			IsDiscardWhenBlocked: utils.Settings.GetBool("settings.producer.tenants.es_geely.is_discard_when_blocked"),
		}),
	}

	if env == "prod" {
		ss = append(ss,
			senders.NewFluentSender(&senders.FluentSenderCfg{ // fluentd backend geely
				Name:                 "FluentdSender",
				Addr:                 utils.Settings.GetString("settings.producer.tenants.fluentd_backup_geely.addr"),
				BatchSize:            utils.Settings.GetInt("settings.producer.tenants.fluentd_backup_geely.msg_batch_size"),
				MaxWait:              utils.Settings.GetDuration("settings.producer.tenants.fluentd_backup_geely.max_wait_sec") * time.Second,
				RetryChanSize:        utils.Settings.GetInt("settings.producer.tenants.fluentd_backup_geely.retry_chan_len"),
				InChanSize:           utils.Settings.GetInt("settings.producer.sender_inchan_size"),
				NFork:                utils.Settings.GetInt("settings.producer.tenants.fluentd_backup_geely.forks"),
				Tags:                 senders.TagsAppendEnv(env, utils.Settings.GetStringSlice("settings.producer.tenants.fluentd_backup_geely.tags")),
				IsDiscardWhenBlocked: utils.Settings.GetBool("settings.producer.tenants.fluentd_backup_geely.is_discard_when_blocked"),
			}),
			senders.NewFluentSender(&senders.FluentSenderCfg{ // fluentd backend emqtt
				Name:                 "FluentdSender",
				Addr:                 utils.Settings.GetString("settings.producer.tenants.fluentd_backup_emqtt.addr"),
				BatchSize:            utils.Settings.GetInt("settings.producer.tenants.fluentd_backup_emqtt.msg_batch_size"),
				MaxWait:              utils.Settings.GetDuration("settings.producer.tenants.fluentd_backup_emqtt.max_wait_sec") * time.Second,
				RetryChanSize:        utils.Settings.GetInt("settings.producer.tenants.fluentd_backup_emqtt.retry_chan_len"),
				InChanSize:           utils.Settings.GetInt("settings.producer.sender_inchan_size"),
				NFork:                utils.Settings.GetInt("settings.producer.tenants.fluentd_backup_emqtt.forks"),
				Tags:                 senders.TagsAppendEnv(env, utils.Settings.GetStringSlice("settings.producer.tenants.fluentd_backup_emqtt.tags")),
				IsDiscardWhenBlocked: utils.Settings.GetBool("settings.producer.tenants.fluentd_backup_emqtt.is_discard_when_blocked"),
			}),
		)
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
