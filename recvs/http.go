package recvs

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"regexp"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/kataras/iris"
)

const (
	SIGNATURE_KEY = "sig"
	TIMESTAMP_KEY = "@timestamp"
)

type HTTPRecvCfg struct {
	HTTPSrv                                *iris.Application
	MaxBodySize                            int64
	Path, Env                              string
	Name, OrigTag, Tag, TagKey, MsgKey     string
	TimeKey, NewTimeKey, NewTimeFormat     string
	TimeFormat                             string
	SigSalt                                []byte
	TSRegexp                               *regexp.Regexp
	MaxAllowedDelaySec, MaxAllowedAheadSec time.Duration
}

type HTTPRecv struct {
	*BaseRecv
	*HTTPRecvCfg
}

func NewHTTPRecv(cfg *HTTPRecvCfg) *HTTPRecv {
	utils.Logger.Info("create HTTPRecv",
		zap.String("tag", cfg.Tag),
		zap.String("path", cfg.Path))

	if cfg.Path == "" {
		utils.Logger.Panic("path should not be emqty")
	}

	r := &HTTPRecv{
		BaseRecv:    &BaseRecv{},
		HTTPRecvCfg: cfg,
	}
	r.HTTPSrv.Post(r.Path, r.HTTPLogHandler)
	r.HTTPSrv.Get(r.Path, func(ctx iris.Context) {
		ctx.WriteString("HTTPrecv")
	})
	return r
}

func (r *HTTPRecv) GetName() string {
	return r.Name
}

func (r *HTTPRecv) Run() {
	utils.Logger.Info("run HTTPRecv")
}

func (r *HTTPRecv) validate(ctx iris.Context, msg *libs.FluentMsg) bool {
	switch msg.Message[TIMESTAMP_KEY].(type) {
	case nil:
		utils.Logger.Warn("message should contains `@timestamp`")
		r.BadRequest(ctx, "message should contains `@timestamp`")
		return false
	case string:
		msg.Message[TIMESTAMP_KEY] = []byte(msg.Message[TIMESTAMP_KEY].(string))
	case []byte:
	default:
		utils.Logger.Warn("unknown type of `@timestamp`", zap.String(TIMESTAMP_KEY, fmt.Sprint(msg.Message[TIMESTAMP_KEY])))
		r.BadRequest(ctx, "unknown type of `@timestamp`")
		return false
	}

	if !r.TSRegexp.Match(msg.Message[TIMESTAMP_KEY].([]byte)) {
		utils.Logger.Warn("unknown format of `@timestamp`", zap.ByteString(TIMESTAMP_KEY, msg.Message[TIMESTAMP_KEY].([]byte)))
		r.BadRequest(ctx, "unknown format of `@timestamp`")
		return false
	}

	// signature
	switch msg.Message[SIGNATURE_KEY].(type) {
	case nil:
		utils.Logger.Warn("`sig` not exists")
		r.BadRequest(ctx, "`sig` not exists")
		return false
	case []byte:
	case string:
		msg.Message[SIGNATURE_KEY] = []byte(msg.Message[SIGNATURE_KEY].(string))
	default:
		utils.Logger.Warn("`unknown type of `sig`", zap.String(SIGNATURE_KEY, fmt.Sprint(msg.Message[SIGNATURE_KEY])))
		r.BadRequest(ctx, "`unknown type of `sig`")
		return false
	}
	hash := md5.Sum(append(msg.Message[TIMESTAMP_KEY].([]byte), r.SigSalt...))
	sig := hex.EncodeToString(hash[:])
	if sig != string(msg.Message[SIGNATURE_KEY].([]byte)) {
		utils.Logger.Warn("signature of `@timestamp` incorrect",
			zap.String("expect", sig),
			zap.ByteString("got", msg.Message[SIGNATURE_KEY].([]byte)))
		r.BadRequest(ctx, "signature error")
		return false
	}

	// check whether @timestamp is expires
	now := utils.Clock.GetUTCNow()
	if ts, err := time.Parse(r.TimeFormat, string(msg.Message[TIMESTAMP_KEY].([]byte))); err != nil {
		utils.Logger.Error("parse ts got error",
			zap.Error(err),
			zap.ByteString(TIMESTAMP_KEY, msg.Message[TIMESTAMP_KEY].([]byte)))
		r.BadRequest(ctx, "signature error")
		return false
	} else if now.Sub(ts) > r.MaxAllowedDelaySec {
		utils.Logger.Warn("@timestamp expires", zap.Time("ts", ts))
		r.BadRequest(ctx, "expires")
		return false
	} else if ts.Sub(now) > r.MaxAllowedAheadSec {
		utils.Logger.Warn("@timestamp ahead of now", zap.Time("ts", ts))
		r.BadRequest(ctx, "come from future?")
		return false
	}

	return true
}

func (r *HTTPRecv) BadRequest(ctx iris.Context, msg string) {
	ctx.StatusCode(iris.StatusBadRequest)
	ctx.WriteString(msg)
}

func (r *HTTPRecv) HTTPLogHandler(ctx iris.Context) {
	env := ctx.Params().Get("env")
	switch env {
	case "sit":
	case "perf":
	case "uat":
	case "prod":
	default:
		utils.Logger.Warn("unknown env", zap.String("env", env))
		r.BadRequest(ctx, fmt.Sprintf("only accept sit/perf/uat/prod, but got `%v`", env))
		return
	}
	// utils.Logger.Debug("got new http log", zap.String("env", env))

	if ctx.GetContentLength() > r.MaxBodySize {
		utils.Logger.Warn("content size too big", zap.Int64("size", ctx.GetContentLength()))
		r.BadRequest(ctx, fmt.Sprintf("content size must less than %d bytes", r.MaxBodySize))
		return
	}

	msg := r.msgPool.Get().(*libs.FluentMsg)
	if log, err := ioutil.ReadAll(ctx.Request().Body); err != nil {
		utils.Logger.Warn("try to read log got error", zap.Error(err))
		r.msgPool.Put(msg)
		r.BadRequest(ctx, "can not load request body")
		return
	} else {
		msg.Tag = r.Tag + "." + r.Env // forward-xxx.sit
		msg.Message = map[string]interface{}{}
		if err = json.Unmarshal(log, &msg.Message); err != nil {
			utils.Logger.Warn("try to unmarsh json got error")
			r.msgPool.Put(msg)
			r.BadRequest(ctx, "try to unmarsh json body got error")
			return
		}
	}

	if !r.validate(ctx, msg) {
		r.msgPool.Put(msg)
		return
	}

	libs.FlattenMap(msg.Message, "__")
	msg.Message[r.TagKey] = r.OrigTag + "." + env
	msg.Id = r.counter.Count()
	utils.Logger.Debug("receive new msg", zap.String("tag", msg.Tag), zap.Int64("id", msg.Id))
	ctx.Writef(`'{"msgid": %d}'`, msg.Id)
	r.asyncOutChan <- msg
}
