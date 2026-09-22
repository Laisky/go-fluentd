package recvs

import (
	"context"
	"sort"
	"time"

	"gofluentd/library"
	"gofluentd/library/log"

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
		log.Logger.Error("listen udp", zap.Error(err), zap.String("addr", addr))
		return nil, nil, err
	}
	if err = server.ListenTCP(addr); err != nil {
		server.Kill() // release an already-bound UDP socket on partial startup failure
		log.Logger.Error("listen tcp", zap.Error(err), zap.String("addr", addr))
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
type syslogServer interface {
	Boot(*syslog.BLBCfg) error
	Wait()
	Kill() error
}

type RsyslogRecv struct {
	newServer func(string) (syslogServer, syslog.LogPartsChannel, error)
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
	log.Logger.Info("run RsyslogRecv", zap.String("tag", r.Tag))

	go r.run(ctx)
}

func (r *RsyslogRecv) run(ctx context.Context) {
	defer log.Logger.Info("rsyslog receiver exit", zap.String("name", r.GetName()))
	factory := r.newServer
	if factory == nil {
		factory = func(addr string) (syslogServer, syslog.LogPartsChannel, error) { return NewRsyslogSrv(addr) }
	}
	retry := func() bool {
		timer := time.NewTimer(defaultRetryWait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	}
	for {
		if ctx.Err() != nil {
			return
		}
		srv, input, err := factory(r.Addr)
		if err != nil {
			log.Logger.Error("new syslog server", zap.Error(err))
			if !retry() {
				return
			}
			continue
		}
		if err := srv.Boot(&syslog.BLBCfg{ACK: []byte{}, SYN: "hello"}); err != nil {
			log.Logger.Error("start syslog server", zap.Error(err))
			// A failed Boot has no live child context to cancel, but may own sockets.
			if err := srv.Kill(); err != nil {
				log.Logger.Error("close failed syslog server", zap.Error(err))
			}
			if !retry() {
				return
			}
			continue
		}
		serverCtx, stop := context.WithCancel(ctx)
		go func() { srv.Wait(); stop() }()
		func() {
			defer stop()
			defer func() {
				if err := srv.Kill(); err != nil {
					log.Logger.Error("close syslog server", zap.Error(err))
				}
			}()
			for {
				select {
				case <-serverCtx.Done():
					return
				case part, ok := <-input:
					if !ok {
						return
					}
					msg := r.parseLogPart(part)
					if msg == nil {
						continue
					}
					select {
					case r.asyncOutChan <- msg:
					case <-serverCtx.Done():
						r.msgPool.Put(msg)
						return
					}
				}
			}
		}()
		if ctx.Err() != nil {
			return
		}
		if !retry() {
			return
		}
	}
}

func (r *RsyslogRecv) parseLogPart(logPart format.LogParts) *library.FluentMsg {
	timestamp, ok := logPart[r.TimeKey].(time.Time)
	if !ok {
		log.Logger.Warn("discard syslog with invalid timestamp", zap.String("tag", r.Tag))
		return nil
	}
	content := logPart[r.MsgKey]
	// Read the original values before removing source fields, including when
	// a source key is identical to its destination.
	delete(logPart, r.TimeKey)
	delete(logPart, r.MsgKey)
	logPart[r.NewTimeKey] = timestamp.Add(r.TimeShift).UTC().Format(r.NewTimeFormat)
	logPart["message"] = content
	replacements := map[string]interface{}{}
	keys := make([]string, 0, len(r.RewriteTags))
	for key := range r.RewriteTags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if value, exists := logPart[key]; exists {
			replacements[r.RewriteTags[key]] = value
		}
	}
	for _, key := range keys {
		if _, exists := logPart[key]; exists {
			delete(logPart, key)
		}
	}
	for key, value := range replacements {
		logPart[key] = value
	}
	msg := r.getMsg()
	msg.ID = r.counter.Count()
	msg.Tag = r.Tag
	msg.Message = logPart
	if r.TagKey != "" {
		msg.Message[r.TagKey] = r.Tag
	}
	return msg
}
