package recvs

import (
	"context"
	"time"

	"github.com/Laisky/go-syslog/format"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-syslog"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

func NewRsyslogSrv(addr string) (*syslog.Server, syslog.LogPartsChannel) {
	inchan := make(syslog.LogPartsChannel, 1000)
	handler := syslog.NewChannelHandler(inchan)

	server := syslog.NewServer()
	server.SetFormat(syslog.Automatic)
	server.SetHandler(handler)
	server.ListenUDP(addr)
	server.ListenTCP(addr)
	return server, inchan
}

type RsyslogCfg struct {
	Name, Addr, Env, TagKey, MsgKey    string
	NewTimeFormat, TimeKey, NewTimeKey string
}

// RsyslogRecv
type RsyslogRecv struct {
	*BaseRecv
	*RsyslogCfg
}

func NewRsyslogRecv(cfg *RsyslogCfg) *RsyslogRecv {
	return &RsyslogRecv{
		BaseRecv:   &BaseRecv{},
		RsyslogCfg: cfg,
	}
}

func (r *RsyslogRecv) GetName() string {
	return r.Name
}

func (r *RsyslogRecv) Run(ctx context.Context) {
	utils.Logger.Info("Run RsyslogRecv")

	go func() {
		defer utils.Logger.Info("rsyslog reciver exit", zap.String("name", r.GetName()))
		var (
			err     error
			msg     *libs.FluentMsg
			tag     = "emqtt." + r.Env
			logPart format.LogParts
			ctx2Srv context.Context
			cancel  func()
		)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			ctx2Srv, cancel = context.WithCancel(ctx)
			srv, inchan := NewRsyslogSrv(r.Addr)
			utils.Logger.Info("listening rsyslog", zap.String("addr", r.Addr))
			if err = srv.Boot(&syslog.BLBCfg{
				ACK: []byte{},
				SYN: "hello",
			}); err != nil {
				utils.Logger.Error("try to start rsyslog server got error", zap.Error(err))
				cancel()
				continue
			}
			go func(srv *syslog.Server, cancel func()) {
				srv.Wait()
				cancel()
			}(srv, cancel)

		LOG_LOOP:
			for {
				select {
				case <-ctx2Srv.Done():
					utils.Logger.Info("rsyslog server exit")
					break LOG_LOOP
				case logPart = <-inchan:
					if logPart == nil {
						utils.Logger.Info("rsyslog channel closed")
						break LOG_LOOP
					}
				}

				switch logPart[r.TimeKey].(type) {
				case time.Time:
					logPart[r.NewTimeKey] = logPart[r.TimeKey].(time.Time).UTC().Format(r.NewTimeFormat)
					delete(logPart, r.TimeKey)
				default:
					utils.Logger.Error("unknown timestamp format")
				}

				// rename to message because of the elasticsearch default query field is `message`
				logPart["message"] = logPart[r.MsgKey]
				delete(logPart, r.MsgKey)

				msg = r.msgPool.Get().(*libs.FluentMsg)
				// utils.Logger.Info(fmt.Sprintf("got %p", msg))
				msg.Id = r.counter.Count()
				msg.Tag = tag
				msg.Message = logPart
				msg.Message[r.TagKey] = msg.Tag

				utils.Logger.Debug("receive new msg", zap.String("tag", msg.Tag), zap.Int64("id", msg.Id))
				r.asyncOutChan <- msg
			}

			if err = srv.Kill(); err != nil {
				utils.Logger.Error("stop rsyslog got error", zap.Error(err))
			}
		}
	}()
}
