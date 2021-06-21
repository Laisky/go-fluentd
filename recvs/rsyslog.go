package recvs

import (
	"context"
	"time"

	"gofluentd/library"

	"github.com/Laisky/go-syslog"
	"github.com/Laisky/go-syslog/format"
	"github.com/Laisky/zap"
)

var (
	defaultRetryWait = 3 * time.Second
)

func NewRsyslogSrv(addr string) (*syslog.Server, syslog.LogPartsChannel, error) {
	var (
		inchan  = make(syslog.LogPartsChannel, 1000)
		handler = syslog.NewChannelHandler(inchan)
		server  = syslog.NewServer()
		err     error
	)

	server.SetFormat(syslog.Automatic)
	server.SetHandler(handler)
	if err = server.ListenUDP(addr); err != nil {
		library.Logger.Error("listen udp", zap.Error(err), zap.String("addr", addr))
		return nil, nil, err
	}
	if err = server.ListenTCP(addr); err != nil {
		library.Logger.Error("listen tcp", zap.Error(err), zap.String("addr", addr))
		return nil, nil, err
	}
	return server, inchan, nil
}

type RsyslogCfg struct {
	RewriteTags map[string]string
	TimeShift   time.Duration
	Name, Addr, TagKey, MsgKey,
	Tag,
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
	library.Logger.Info("run RsyslogRecv", zap.String("tag", r.Tag))

	go func() {
		defer library.Logger.Info("rsyslog reciver exit", zap.String("name", r.GetName()))
		var (
			ok                        bool
			msg                       *library.FluentMsg
			logPart                   format.LogParts
			ctx2Srv                   context.Context
			cancel                    func()
			rewriteKey, rewriteNewKey string
		)
	SERVER_LOOP:
		for {
			select {
			case <-ctx.Done():
				break SERVER_LOOP
			default:
			}

			srv, inchan, err := NewRsyslogSrv(r.Addr)
			if err != nil {
				library.Logger.Error("new rsyslog server", zap.String("addr", r.Addr), zap.Error(err))
				time.Sleep(defaultRetryWait)
				continue SERVER_LOOP
			}
			library.Logger.Info("listening rsyslog", zap.String("addr", r.Addr))
			if err = srv.Boot(&syslog.BLBCfg{
				ACK: []byte{},
				SYN: "hello",
			}); err != nil {
				library.Logger.Error("try to start rsyslog server got error", zap.Error(err))
				cancel()
				continue
			}

			ctx2Srv, cancel = context.WithCancel(ctx)
			go func(srv *syslog.Server, cancel func()) {
				srv.Wait()
				cancel()
			}(srv, cancel)

		LOG_LOOP:
			for {
				select {
				case <-ctx2Srv.Done():
					library.Logger.Info("try to reconnect rsyslog server")
					break LOG_LOOP
				case logPart, ok = <-inchan:
					if !ok {
						library.Logger.Info("rsyslog channel closed")
						cancel()
						break LOG_LOOP
					}
				}

				switch t := logPart[r.TimeKey].(type) {
				case time.Time:
					logPart[r.NewTimeKey] = t.Add(r.TimeShift).UTC().Format(r.NewTimeFormat)
					delete(logPart, r.TimeKey)
				default:
					library.Logger.Error("discard log since unknown timestamp format")
				}

				// rename to message because of the elasticsearch default query field is `message`
				logPart["message"] = logPart[r.MsgKey]
				delete(logPart, r.MsgKey)

				msg = r.msgPool.Get().(*library.FluentMsg)
				// library.Logger.Info(fmt.Sprintf("got %p", msg))
				msg.ID = r.counter.Count()
				msg.Tag = r.Tag
				msg.Message = logPart
				for rewriteKey, rewriteNewKey = range r.RewriteTags { // rewrite key
					msg.Message[rewriteNewKey] = msg.Message[rewriteKey]
					delete(msg.Message, rewriteKey)
				}
				if r.TagKey != "" { // reset tag
					msg.Message[r.TagKey] = r.Tag
				}

				library.Logger.Debug("receive new msg", zap.String("tag", r.Tag), zap.Int64("id", msg.ID))
				r.asyncOutChan <- msg
			}

			if err = srv.Kill(); err != nil {
				library.Logger.Error("stop rsyslog got error", zap.Error(err))
			}
		}
	}()
}
