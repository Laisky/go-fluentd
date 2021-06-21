package controller

import (
	"context"
	"encoding/hex"
	"regexp"
	"runtime"
	"sync"
	"time"

	"gofluentd/internal/acceptorfilters"
	"gofluentd/internal/monitor"
	"gofluentd/internal/postfilters"
	"gofluentd/internal/recvs"
	"gofluentd/internal/senders"
	"gofluentd/internal/tagfilters"
	"gofluentd/library"
	"gofluentd/library/log"

	"github.com/Laisky/go-kafka"
	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/cespare/xxhash"
)

// Controllor is an IoC that manage all roles
type Controllor struct {
	msgPool *sync.Pool
}

// NewControllor create new Controllor
func NewControllor() (c *Controllor) {
	log.Logger.Info("create Controllor")

	c = &Controllor{
		msgPool: &sync.Pool{
			New: func() interface{} {
				return &library.FluentMsg{
					// Message: map[string]interface{}{},
					// Id: -1,
				}
			},
		},
	}

	return c
}

func (c *Controllor) initJournal(ctx context.Context) *Journal {
	return NewJournal(ctx, &JournalCfg{
		MsgPool:                   c.msgPool,
		BufDirPath:                gutils.Settings.GetString("settings.journal.buf_dir_path"),
		BufSizeBytes:              gutils.Settings.GetInt64("settings.journal.buf_file_bytes"),
		JournalOutChanLen:         gutils.Settings.GetInt("settings.journal.journal_out_chan_len"),
		CommitIDChanLen:           gutils.Settings.GetInt("settings.journal.commit_id_chan_len"),
		ChildJournalIDInchanLen:   gutils.Settings.GetInt("settings.journal.child_id_chan_len"),
		ChildJournalDataInchanLen: gutils.Settings.GetInt("settings.journal.child_data_chan_len"),
		CommittedIDTTL:            gutils.Settings.GetDuration("settings.journal.committed_id_sec") * time.Second,
		IsCompress:                gutils.Settings.GetBool("settings.journal.is_compress"),
		GCIntervalSec:             gutils.Settings.GetDuration("settings.journal.gc_inteval_sec") * time.Second,
	})
}

func (c *Controllor) initRecvs(env string) []recvs.AcceptorRecvItf {
	// init tcp recvs
	receivers := []recvs.AcceptorRecvItf{}

	// init kafka plugins recvs
	sharingKMsgPool := &sync.Pool{
		New: func() interface{} {
			return &kafka.KafkaMsg{}
		},
	}

	switch gutils.Settings.Get("settings.acceptor.recvs.plugins").(type) {
	case map[string]interface{}:
		for name := range gutils.Settings.Get("settings.acceptor.recvs.plugins").(map[string]interface{}) {
			if !StringListContains(gutils.Settings.GetStringSlice("settings.acceptor.recvs.plugins."+name+".active_env"), env) {
				log.Logger.Info("recv not support current env", zap.String("name", name), zap.String("env", env))
				continue
			}

			t := gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".type")
			switch t {
			case "fluentd":
				receivers = append(receivers, recvs.NewFluentdRecv(&recvs.FluentdRecvCfg{
					Name:                   name,
					Addr:                   gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".addr"),
					TagKey:                 gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".tag_key"),
					LBKey:                  gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".lb_key"),
					IsRewriteTagFromTagKey: gutils.Settings.GetBool("settings.acceptor.recvs.plugins." + name + ".is_rewrite_tag_from_tag_key"),
					OriginRewriteTagKey:    gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".origin_rewrite_tag_key"),
					ConcatMaxLen:           gutils.Settings.GetInt("settings.acceptor.recvs.plugins." + name + ".concat_max_len"),
					NFork:                  gutils.Settings.GetInt("settings.acceptor.recvs.plugins." + name + ".nfork"),
					ConcatorWait:           gutils.Settings.GetDuration("settings.acceptor.recvs.plugins."+name+".concat_with_sec") * time.Second,
					ConcatorBufSize:        gutils.Settings.GetInt("settings.acceptor.recvs.plugins." + name + ".internal_buf_size"),
					ConcatCfg:              library.LoadTagsMapAppendEnv(env, gutils.Settings.GetStringMap("settings.acceptor.recvs.plugins."+name+".concat")),
				}))
			case "rsyslog":
				receivers = append(receivers, recvs.NewRsyslogRecv(&recvs.RsyslogCfg{
					Name:          name,
					RewriteTags:   gutils.Settings.GetStringMapString("settings.acceptor.recvs.plugins." + name + ".rewrite_tags"),
					Addr:          gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".addr"),
					Tag:           library.LoadTagReplaceEnv(env, gutils.Settings.GetString("settings.acceptor.recvs.plugins."+name+".tag")),
					TagKey:        gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".tag_key"),
					MsgKey:        gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".msg_key"),
					TimeShift:     gutils.Settings.GetDuration("settings.acceptor.recvs.plugins."+name+".time_shift_sec") * time.Second,
					NewTimeFormat: gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".new_time_format"),
					TimeKey:       gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".time_key"),
					NewTimeKey:    gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".new_time_key"),
				}))
			case "http":
				receivers = append(receivers, recvs.NewHTTPRecv(&recvs.HTTPRecvCfg{ // wechat mini program
					Name:               name,
					HTTPSrv:            server,
					Env:                env,
					MsgKey:             gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".msg_key"),
					TagKey:             gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".tag_key"),
					OrigTag:            gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".orig_tag"),
					Tag:                gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".tag"),
					Path:               gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".path"),
					SigKey:             gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".signature_key"),
					SigSalt:            []byte(gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".signature_salt")),
					MaxBodySize:        gutils.Settings.GetInt64("settings.acceptor.recvs.plugins." + name + ".max_body_byte"),
					TSRegexp:           regexp.MustCompile(gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".ts_regexp")),
					TimeKey:            gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".time_key"),
					TimeFormat:         gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".time_format"),
					MaxAllowedDelaySec: gutils.Settings.GetDuration("settings.acceptor.recvs.plugins."+name+".max_allowed_delay_sec") * time.Second,
					MaxAllowedAheadSec: gutils.Settings.GetDuration("settings.acceptor.recvs.plugins."+name+".max_allowed_ahead_sec") * time.Second,
				}))
			case "kafka":
				kafkaCfg := &recvs.KafkaCfg{
					KMsgPool:          sharingKMsgPool,
					Name:              name,
					MsgKey:            gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".msg_key"),
					Brokers:           gutils.Settings.GetStringSlice("settings.acceptor.recvs.plugins." + name + ".brokers." + env),
					Topics:            []string{gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".topics." + env)},
					Group:             gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".groups." + env),
					Tag:               gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".tags." + env),
					IsJSONFormat:      gutils.Settings.GetBool("settings.acceptor.recvs.plugins." + name + ".is_json_format"),
					TagKey:            gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".tag_key"),
					JSONTagKey:        gutils.Settings.GetString("settings.acceptor.recvs.plugins." + name + ".json_tag_key"),
					RewriteTag:        recvs.GetKafkaRewriteTag(gutils.Settings.GetString("settings.acceptor.recvs.plugins."+name+".rewrite_tag"), env),
					NConsumer:         gutils.Settings.GetInt("settings.acceptor.recvs.plugins." + name + ".nconsumer"),
					ReconnectInterval: gutils.Settings.GetDuration("settings.acceptor.recvs.plugins."+name+".reconnect_sec") * time.Second,
				}
				kafkaCfg.IntervalNum = gutils.Settings.GetInt("settings.acceptor.recvs.plugins." + name + ".interval_num")
				kafkaCfg.IntervalDuration = gutils.Settings.GetDuration("settings.acceptor.recvs.plugins."+name+".interval_sec") * time.Second
				receivers = append(receivers, recvs.NewKafkaRecv(kafkaCfg))
			default:
				log.Logger.Panic("unknown recv type",
					zap.String("type", t),
					zap.String("name", name))
			}

			log.Logger.Info("active recv",
				zap.String("name", name),
				zap.String("type", t))
		}
	case nil:
	default:
		log.Logger.Panic("recv plugins configuration error")
	}

	return receivers
}

func (c *Controllor) initAcceptor(ctx context.Context, journal *Journal, receivers []recvs.AcceptorRecvItf) *Acceptor {
	acceptor := NewAcceptor(&AcceptorCfg{
		MsgPool:          c.msgPool,
		Journal:          journal,
		MaxRotateID:      gutils.Settings.GetInt64("settings.acceptor.max_rotate_id"),
		AsyncOutChanSize: gutils.Settings.GetInt("settings.acceptor.async_out_chan_size"),
		SyncOutChanSize:  gutils.Settings.GetInt("settings.acceptor.sync_out_chan_size"),
	},
		receivers...,
	)

	acceptor.Run(ctx)
	return acceptor
}

func (c *Controllor) initAcceptorPipeline(ctx context.Context, env string) (*acceptorfilters.AcceptorPipeline, error) {
	afs := []acceptorfilters.AcceptorFilterItf{}
	switch gutils.Settings.Get("settings.acceptor_filters.plugins").(type) {
	case map[string]interface{}:
		for name := range gutils.Settings.Get("settings.acceptor_filters.plugins").(map[string]interface{}) {
			if name == "default" {
				continue
			}

			t := gutils.Settings.GetString("settings.acceptor_filters.plugins." + name + ".type")
			switch t {
			case "spark":
				afs = append(afs, acceptorfilters.NewSparkFilter(&acceptorfilters.SparkFilterCfg{
					Tag:         "spark." + env,
					Name:        name,
					MsgKey:      gutils.Settings.GetString("settings.acceptor_filters.plugins." + name + ".msg_key"),
					Identifier:  gutils.Settings.GetString("settings.acceptor_filters.plugins." + name + ".identifier"),
					IgnoreRegex: regexp.MustCompile(gutils.Settings.GetString("settings.acceptor_filters.plugins." + name + ".ignore_regex")),
				}))
			case "spring":
				afs = append(afs, acceptorfilters.NewSpringFilter(&acceptorfilters.SpringFilterCfg{
					Tag:    "spring." + env,
					Name:   name,
					Env:    env,
					MsgKey: gutils.Settings.GetString("settings.acceptor_filters.plugins." + name + ".msg_key"),
					TagKey: gutils.Settings.GetString("settings.acceptor_filters.plugins." + name + ".tag_key"),
					Rules:  acceptorfilters.ParseSpringRules(env, gutils.Settings.Get("settings.acceptor_filters.plugins."+name+".rules").([]interface{})),
				}))
			default:
				log.Logger.Panic("unknown acceptorfilter type",
					zap.String("type", t),
					zap.String("name", name))
			}
			log.Logger.Info("active acceptorfilter",
				zap.String("name", name),
				zap.String("type", t))
		}
	case nil:
	default:
		log.Logger.Panic("acceptorfilter configuration error")
	}

	// set the DefaultFilter as last filter
	afs = append(afs, acceptorfilters.NewDefaultFilter(&acceptorfilters.DefaultFilterCfg{
		Name:               "default",
		RemoveEmptyTag:     gutils.Settings.GetBool("settings.acceptor_filters.plugins.default.remove_empty_tag"),
		RemoveUnsupportTag: gutils.Settings.GetBool("settings.acceptor_filters.plugins.default.remove_unknown_tag"),
		AddCfg:             library.ParseAddCfg(env, gutils.Settings.Get("settings.acceptor_filters.plugins.default.add")),
		AcceptTags:         library.LoadTagsReplaceEnv(env, gutils.Settings.GetStringSlice("settings.acceptor_filters.plugins.default.accept_tags")),
	}))

	return acceptorfilters.NewAcceptorPipeline(ctx, &acceptorfilters.AcceptorPipelineCfg{
		OutChanSize:     gutils.Settings.GetInt("settings.acceptor_filters.out_buf_len"),
		MsgPool:         c.msgPool,
		ReEnterChanSize: gutils.Settings.GetInt("settings.acceptor_filters.reenter_chan_len"),
		NFork:           gutils.Settings.GetInt("settings.acceptor_filters.fork"),
		IsThrottle:      gutils.Settings.GetBool("settings.acceptor_filters.is_throttle"),
		ThrottleMax:     gutils.Settings.GetInt("settings.acceptor_filters.throttle_max"),
		ThrottleNPerSec: gutils.Settings.GetInt("settings.acceptor_filters.throttle_per_sec"),
	},
		afs...,
	)
}

func (c *Controllor) initTagPipeline(ctx context.Context, env string, waitCommitChan chan<- *library.FluentMsg) *tagfilters.TagPipeline {
	fs := []tagfilters.TagFilterFactoryItf{}
	isEnableConcator := false

	switch gutils.Settings.Get("settings.tag_filters.plugins").(type) {
	case map[string]interface{}:
		for name := range gutils.Settings.Get("settings.tag_filters.plugins").(map[string]interface{}) {
			t := gutils.Settings.GetString("settings.tag_filters.plugins." + name + ".type")
			switch t {
			case "parser":
				fs = append(fs, tagfilters.NewParserFact(&tagfilters.ParserFactCfg{
					Name:            name,
					NFork:           gutils.Settings.GetInt("settings.tag_filters.plugins." + name + ".nfork"),
					LBKey:           gutils.Settings.GetString("settings.tag_filters.plugins." + name + ".lb_key"),
					Tags:            library.LoadTagsReplaceEnv(env, gutils.Settings.GetStringSlice("settings.tag_filters.plugins."+name+".tags")),
					MsgKey:          gutils.Settings.GetString("settings.tag_filters.plugins." + name + ".msg_key"),
					Regexp:          regexp.MustCompile(gutils.Settings.GetString("settings.tag_filters.plugins." + name + ".pattern")),
					IsRemoveOrigLog: gutils.Settings.GetBool("settings.tag_filters.plugins." + name + ".is_remove_orig_log"),
					MsgPool:         c.msgPool,
					ParseJSONKey:    gutils.Settings.GetString("settings.tag_filters.plugins." + name + ".parse_json_key"),
					AddCfg:          library.ParseAddCfg(env, gutils.Settings.Get("settings.tag_filters.plugins."+name+".add")),
					MustInclude:     gutils.Settings.GetString("settings.tag_filters.plugins." + name + ".must_include"),
					TimeKey:         gutils.Settings.GetString("settings.tag_filters.plugins." + name + ".time_key"),
					TimeFormat:      gutils.Settings.GetString("settings.tag_filters.plugins." + name + ".time_format"),
					NewTimeFormat:   gutils.Settings.GetString("settings.tag_filters.plugins." + name + ".new_time_format"),
					ReservedTimeKey: gutils.Settings.GetBool("settings.tag_filters.plugins." + name + ".reserved_time_key"),
					NewTimeKey:      gutils.Settings.GetString("settings.tag_filters.plugins." + name + ".new_time_key"),
					AppendTimeZone:  gutils.Settings.GetString("settings.tag_filters.plugins." + name + ".append_time_zone." + env),
				}))
			case "concator":
				isEnableConcator = true
			default:
				log.Logger.Panic("unknown tagfilter type",
					zap.String("type", t),
					zap.String("name", name))
			}
			log.Logger.Info("active tagfilter",
				zap.String("name", name),
				zap.String("type", t))
		}
	case nil:
	default:
		log.Logger.Panic("tagfilter configuration error")
	}

	// PAAS-397: put concat in fluentd-recvs
	// concatorFilter must in the front
	if isEnableConcator {
		fs = append([]tagfilters.TagFilterFactoryItf{tagfilters.NewConcatorFact(&tagfilters.ConcatorFactCfg{
			NFork:   gutils.Settings.GetInt("settings.tag_filters.plugins.concator.config.nfork"),
			LBKey:   gutils.Settings.GetString("settings.tag_filters.plugins.concator.config.lb_key"),
			MaxLen:  gutils.Settings.GetInt("settings.tag_filters.plugins.concator.config.max_length"),
			Plugins: tagfilters.LoadConcatorTagConfigs(env, gutils.Settings.Get("settings.tag_filters.plugins.concator.plugins").(map[string]interface{})),
		})}, fs...)
	}

	return tagfilters.NewTagPipeline(ctx, &tagfilters.TagPipelineCfg{
		MsgPool:          c.msgPool,
		WaitCommitChan:   waitCommitChan,
		InternalChanSize: gutils.Settings.GetInt("settings.tag_filters.internal_chan_size"),
	},
		fs...,
	)
}

func (c *Controllor) initDispatcher(ctx context.Context, waitDispatchChan chan *library.FluentMsg, tagPipeline *tagfilters.TagPipeline) *Dispatcher {
	dispatcher := NewDispatcher(&DispatcherCfg{
		InChan:      waitDispatchChan,
		TagPipeline: tagPipeline,
		NFork:       gutils.Settings.GetInt("settings.dispatcher.nfork"),
		OutChanSize: gutils.Settings.GetInt("settings.dispatcher.out_chan_size"),
	})
	dispatcher.Run(ctx)

	return dispatcher
}

func (c *Controllor) initPostPipeline(env string, waitCommitChan chan<- *library.FluentMsg) *postfilters.PostPipeline {
	fs := []postfilters.PostFilterItf{
		// set the DefaultFilter as first filter
		postfilters.NewDefaultFilter(&postfilters.DefaultFilterCfg{
			MsgKey: gutils.Settings.GetString("settings.post_filters.plugins.default.msg_key"),
			MaxLen: gutils.Settings.GetInt("settings.post_filters.plugins.default.max_len"),
		}),
	}

	switch gutils.Settings.Get("settings.post_filters.plugins").(type) {
	case map[string]interface{}:
		for name := range gutils.Settings.Get("settings.post_filters.plugins").(map[string]interface{}) {
			if name == "default" {
				continue
			}

			t := gutils.Settings.GetString("settings.post_filters.plugins." + name + ".type")
			switch t {
			case "es-dispatcher":
				fs = append(fs, postfilters.NewESDispatcherFilter(&postfilters.ESDispatcherFilterCfg{
					Tags:     library.LoadTagsAppendEnv(env, gutils.Settings.GetStringSlice("settings.post_filters.plugins."+name+".tags")),
					TagKey:   gutils.Settings.GetString("settings.post_filters.plugins." + name + ".tag_key"),
					ReTagMap: postfilters.LoadReTagMap(env, gutils.Settings.Get("settings.post_filters.plugins."+name+".rewrite_tag_map")),
				}))
			case "tag-rewriter":
				fs = append(fs, postfilters.NewForwardTagRewriterFilter(&postfilters.ForwardTagRewriterFilterCfg{ // wechat mini program
					Tag:    gutils.Settings.GetString("settings.post_filters.plugins."+name+".tag") + "." + env,
					TagKey: gutils.Settings.GetString("settings.post_filters.plugins." + name + ".tag_key"),
				}))
			case "fields":
				fs = append(fs, postfilters.NewFieldsFilter(&postfilters.FieldsFilterCfg{
					Tags:              library.LoadTagsAppendEnv(env, gutils.Settings.GetStringSlice("settings.post_filters.plugins."+name+".tags")),
					IncludeFields:     gutils.Settings.GetStringSlice("settings.post_filters.plugins." + name + ".include_fields"),
					ExcludeFields:     gutils.Settings.GetStringSlice("settings.post_filters.plugins." + name + ".exclude_fields"),
					NewFieldTemplates: gutils.Settings.GetStringMapString("settings.post_filters.plugins." + name + ".new_fields"),
				}))
			case "custom-bigdata":
				fs = append(fs, postfilters.NewCustomBigDataFilter(&postfilters.CustomBigDataFilterCfg{
					Tags: library.LoadTagsAppendEnv(env, gutils.Settings.GetStringSlice("settings.post_filters.plugins."+name+".tags")),
				}))
			default:
				log.Logger.Panic("unknown post_filter type",
					zap.String("post_filter_type", t),
					zap.String("post_filter_name", name))
			}

			log.Logger.Info("active post_filter",
				zap.String("type", t),
				zap.String("name", name),
				zap.String("env", env))
		}
	case nil:
	default:
		log.Logger.Panic("post_filter configuration error")
	}

	return postfilters.NewPostPipeline(&postfilters.PostPipelineCfg{
		MsgPool:         c.msgPool,
		WaitCommitChan:  waitCommitChan,
		NFork:           gutils.Settings.GetInt("settings.post_filters.fork"),
		ReEnterChanSize: gutils.Settings.GetInt("settings.post_filters.reenter_chan_len"),
		OutChanSize:     gutils.Settings.GetInt("settings.post_filters.out_chan_size"),
	}, fs...)
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
	switch gutils.Settings.Get("settings.producer.plugins").(type) {
	case map[string]interface{}:
		for name := range gutils.Settings.Get("settings.producer.plugins").(map[string]interface{}) {
			if !StringListContains(gutils.Settings.GetStringSlice("settings.producer.plugins."+name+".active_env"), env) {
				log.Logger.Info("sender not support current env", zap.String("name", name), zap.String("env", env))
				continue
			}

			t := gutils.Settings.GetString("settings.producer.plugins." + name + ".type")
			switch t {
			case "fluentd":
				ss = append(ss, senders.NewFluentSender(&senders.FluentSenderCfg{
					Name:                 name,
					Addr:                 gutils.Settings.GetString("settings.producer.plugins." + name + ".addr"),
					BatchSize:            gutils.Settings.GetInt("settings.producer.plugins." + name + ".msg_batch_size"),
					MaxWait:              gutils.Settings.GetDuration("settings.producer.plugins."+name+".max_wait_sec") * time.Second,
					InChanSize:           gutils.Settings.GetInt("settings.producer.sender_inchan_size"),
					NFork:                gutils.Settings.GetInt("settings.producer.plugins." + name + ".forks"),
					Tags:                 gutils.Settings.GetStringSlice("settings.producer.plugins." + name + ".tags"), // do not append env
					IsDiscardWhenBlocked: gutils.Settings.GetBool("settings.producer.plugins." + name + ".is_discard_when_blocked"),
				}))
			case "kafka":
				ss = append(ss, senders.NewKafkaSender(&senders.KafkaSenderCfg{
					Name:                 name,
					Brokers:              gutils.Settings.GetStringSlice("settings.producer.plugins." + name + ".brokers." + env),
					Topic:                gutils.Settings.GetString("settings.producer.plugins." + name + ".topic." + env),
					TagKey:               gutils.Settings.GetString("settings.producer.plugins." + name + ".tag_key"),
					BatchSize:            gutils.Settings.GetInt("settings.producer.plugins." + name + ".msg_batch_size"),
					MaxWait:              gutils.Settings.GetDuration("settings.producer.plugins."+name+".max_wait_sec") * time.Second,
					InChanSize:           gutils.Settings.GetInt("settings.producer.sender_inchan_size"),
					NFork:                gutils.Settings.GetInt("settings.producer.plugins." + name + ".forks"),
					Tags:                 library.LoadTagsAppendEnv(env, gutils.Settings.GetStringSlice("settings.producer.plugins."+name+".tags")),
					IsDiscardWhenBlocked: gutils.Settings.GetBool("settings.producer.plugins." + name + ".is_discard_when_blocked"),
				}))
			case "es":
				ss = append(ss, senders.NewElasticSearchSender(&senders.ElasticSearchSenderCfg{
					Name:                 name,
					BatchSize:            gutils.Settings.GetInt("settings.producer.plugins." + name + ".msg_batch_size"),
					Addr:                 gutils.Settings.GetString("settings.producer.plugins." + name + ".addr"),
					MaxWait:              gutils.Settings.GetDuration("settings.producer.plugins."+name+".max_wait_sec") * time.Second,
					InChanSize:           gutils.Settings.GetInt("settings.producer.sender_inchan_size"),
					NFork:                gutils.Settings.GetInt("settings.producer.plugins." + name + ".forks"),
					TagKey:               gutils.Settings.GetString("settings.producer.plugins." + name + ".tag_key"),
					Tags:                 library.LoadTagsReplaceEnv(env, gutils.Settings.GetStringSlice("settings.producer.plugins."+name+".tags")),
					TagIndexMap:          senders.LoadESTagIndexMap(env, gutils.Settings.Get("settings.producer.plugins."+name+".indices")),
					IsDiscardWhenBlocked: gutils.Settings.GetBool("settings.producer.plugins." + name + ".is_discard_when_blocked"),
				}))
			case "stdout":
				ss = append(ss, senders.NewStdoutSender(&senders.StdoutSenderCfg{
					Name:                 name,
					Tags:                 library.LoadTagsReplaceEnv(env, gutils.Settings.GetStringSlice("settings.producer.plugins."+name+".tags")),
					LogLevel:             gutils.Settings.GetString("settings.producer.plugins." + name + ".log_level"),
					InChanSize:           gutils.Settings.GetInt("settings.producer.sender_inchan_size"),
					NFork:                gutils.Settings.GetInt("settings.producer.plugins." + name + ".forks"),
					IsCommit:             gutils.Settings.GetBool("settings.producer.plugins." + name + ".is_commit"),
					IsDiscardWhenBlocked: gutils.Settings.GetBool("settings.producer.plugins." + name + ".is_discard_when_blocked"),
				}))
			default:
				log.Logger.Panic("unknown sender type",
					zap.String("type", t),
					zap.String("name", name))
			}
			log.Logger.Info("active sender",
				zap.String("type", t),
				zap.String("name", name),
				zap.String("env", env))
		}
	case nil:
	default:
		log.Logger.Panic("sender configuration error")
	}

	return ss
}

func (c *Controllor) initProducer(env string, waitProduceChan chan *library.FluentMsg, commitChan chan<- *library.FluentMsg, senders []senders.SenderItf) *Producer {
	hasher := xxhash.New()
	p, err := NewProducer(
		&ProducerCfg{
			DistributeKey:   hex.EncodeToString(hasher.Sum([]byte((gutils.Settings.GetString("host") + "-" + gutils.Settings.GetString("env"))))),
			InChan:          waitProduceChan,
			MsgPool:         c.msgPool,
			CommitChan:      commitChan,
			NFork:           gutils.Settings.GetInt("settings.producer.forks"),
			DiscardChanSize: gutils.Settings.GetInt("settings.producer.discard_chan_size"),
		},
		// senders...
		senders...,
	)
	if err != nil {
		log.Logger.Panic("new producer", zap.Error(err))
	}

	return p
}

func (c *Controllor) runHeartBeat(ctx context.Context) {
	defer log.Logger.Info("heartbeat exit")
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		log.Logger.Info("heartbeat",
			zap.Int("goroutine", runtime.NumGoroutine()),
		)
		time.Sleep(gutils.Settings.GetDuration("heartbeat") * time.Second)
	}
}

// Run starting all pipeline
func (c *Controllor) Run(ctx context.Context) {
	log.Logger.Info("running...")
	env := gutils.Settings.GetString("env")

	journal := c.initJournal(ctx)

	receivers := c.initRecvs(env)
	acceptor := c.initAcceptor(ctx, journal, receivers)
	acceptorPipeline, err := c.initAcceptorPipeline(ctx, env)
	if err != nil {
		log.Logger.Panic("initAcceptorPipeline", zap.Error(err))
	}

	waitCommitChan := journal.GetCommitChan()
	waitAccepPipelineSyncChan := acceptor.GetSyncOutChan()
	waitAccepPipelineAsyncChan := acceptor.GetAsyncOutChan()
	waitDumpChan, skipDumpChan := acceptorPipeline.Wrap(ctx, waitAccepPipelineAsyncChan, waitAccepPipelineSyncChan)

	// after `journal.DumpMsgFlow`, every discarded msg should commit to waitCommitChan
	waitDispatchChan := journal.DumpMsgFlow(ctx, c.msgPool, waitDumpChan, skipDumpChan)

	tagPipeline := c.initTagPipeline(ctx, env, waitCommitChan)
	dispatcher := c.initDispatcher(ctx, waitDispatchChan, tagPipeline)
	waitPostPipelineChan := dispatcher.GetOutChan()
	postPipeline := c.initPostPipeline(env, waitCommitChan)
	waitProduceChan := postPipeline.Wrap(ctx, waitPostPipelineChan)
	producerSenders := c.initSenders(env)
	producer := c.initProducer(env, waitProduceChan, waitCommitChan, producerSenders)

	// heartbeat
	go c.runHeartBeat(ctx)

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
	monitor.BindHTTP(server)

	go producer.Run(ctx)
	RunServer(ctx, gutils.Settings.GetString("addr"))
}
