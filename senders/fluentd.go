package senders

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

type FluentSenderCfg struct {
	Name, Addr                   string
	Tags                         []string
	BatchSize, InChanSize, NFork int
	MaxWait                      time.Duration
	IsDiscardWhenBlocked         bool
	ConcatCfg                    map[string]interface{}
}

type FluentSender struct {
	*BaseSender
	*FluentSenderCfg
}

func NewFluentSender(cfg *FluentSenderCfg) *FluentSender {
	libs.Logger.Info("new fluent sender",
		zap.String("addr", cfg.Addr),
		zap.Strings("tags", cfg.Tags))

	if cfg.Addr == "" {
		libs.Logger.Panic("addr should not be empty")
	}

	s := &FluentSender{
		BaseSender: &BaseSender{
			IsDiscardWhenBlocked: cfg.IsDiscardWhenBlocked,
		},
		FluentSenderCfg: cfg,
	}
	s.SetSupportedTags(cfg.Tags)
	return s
}

func (s *FluentSender) GetName() string {
	return s.Name
}

func (s *FluentSender) Spawn(ctx context.Context) chan<- *libs.FluentMsg {
	libs.Logger.Info("spawn fluentd sender")
	var (
		inChan       = make(chan *libs.FluentMsg, s.InChanSize)
		childInChan  chan *libs.FluentMsg // for each tag
		ok           bool
		childInChani interface{}

		tag2childInChan = &sync.Map{}
		mutex           = &sync.Mutex{}
	)

	for i := 0; i < s.NFork; i++ { // parallel to each tag
		go func() {
			for msg := range inChan {
				if childInChani, ok = tag2childInChan.Load(msg.Tag); !ok {
					mutex.Lock()
					// double check
					if childInChani, ok = tag2childInChan.Load(msg.Tag); !ok {
						// create child sender
						childInChan = make(chan *libs.FluentMsg, s.InChanSize)
						go s.spawnChildSenderForTag(ctx, msg.Tag, childInChan)
						tag2childInChan.Store(msg.Tag, childInChan)
					} else {
						childInChan = childInChani.(chan *libs.FluentMsg)
					}
					mutex.Unlock()
				} else {
					childInChan = childInChani.(chan *libs.FluentMsg)
				}

				select {
				case childInChan <- msg:
				default:
					if s.DiscardWhenBlocked() {
						s.successedChan <- msg
						libs.Logger.Warn("skip sender and discard msg since of its inchan is full",
							zap.String("name", s.GetName()),
							zap.String("tag", msg.Tag))
					} else {
						s.failedChan <- msg
					}
				}
			}
		}()
	}

	return inChan
}

func (s *FluentSender) spawnChildSenderForTag(ctx context.Context, tag string, inChan chan *libs.FluentMsg) {
	libs.Logger.Info("spawn fluentd child sender", zap.String("tag", tag))
	var (
		nRetry           int
		maxRetry         = 3
		msg              *libs.FluentMsg
		msgBatch         = make([]*libs.FluentMsg, s.BatchSize)
		msgBatchDelivery []*libs.FluentMsg
		iBatch           = 0
		lastT            = time.Unix(0, 0)
		encoder          *libs.FluentEncoder
		conn             net.Conn
		err              error
		ok               bool
		ticker           = time.NewTicker(s.MaxWait)
	)
	defer ticker.Stop()

RECONNECT: // reconnect to downstream
	conn, err = net.DialTimeout("tcp", s.Addr, 10*time.Second)
	if err != nil {
		libs.Logger.Error("try to connect to backend got error", zap.Error(err), zap.String("tag", tag))
		goto RECONNECT
	}
	libs.Logger.Info("connected to backend",
		zap.String("backend", conn.RemoteAddr().String()),
		zap.String("tag", tag))

	encoder = libs.NewFluentEncoder(conn) // one encoder for each connection
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok = <-inChan:
			if !ok {
				libs.Logger.Info("inChan closed")
				return
			}
			msgBatch[iBatch] = msg
			iBatch++
		case <-ticker.C:
			if iBatch == 0 {
				continue
			}
			msg = msgBatch[iBatch-1]
		}

		if iBatch < s.BatchSize &&
			utils.Clock.GetUTCNow().Sub(lastT) < s.MaxWait {
			continue
		}

		lastT = utils.Clock.GetUTCNow()
		msgBatchDelivery = msgBatch[:iBatch]
		iBatch = 0
		nRetry = 0
		if utils.Settings.GetBool("dry") {
			libs.Logger.Info("send message to backend",
				zap.String("tag", tag),
				zap.String("log", fmt.Sprint(msgBatch[0].Message)))
			for _, msg = range msgBatchDelivery {
				s.successedChan <- msg
			}
			continue
		}

		for nRetry < maxRetry {
			nRetry++
			if err = encoder.EncodeBatch(tag, msgBatchDelivery); err != nil {
				libs.Logger.Debug("encode msg batch", zap.Error(err))
				continue
			}
			break
		}

		if nRetry >= maxRetry {
			libs.Logger.Error("try send message got error", zap.Error(err), zap.String("tag", tag))
			libs.Logger.Error("discard msg since of sender err", zap.String("tag", msg.Tag), zap.Int("num", len(msgBatchDelivery)))
			for _, msg = range msgBatchDelivery {
				s.failedChan <- msg
			}

			if err = conn.Close(); err != nil {
				libs.Logger.Error("try to close connection got error", zap.Error(err))
			}
			libs.Logger.Info("connection closed, try to reconnect...")
			goto RECONNECT
		}

		encoder.Flush()
		libs.Logger.Debug("success sent message to backend",
			zap.Int("batch", len(msgBatchDelivery)),
			zap.String("backend", s.Addr),
			zap.String("tag", tag))
		for _, msg = range msgBatchDelivery {
			s.successedChan <- msg
		}
	}
}
