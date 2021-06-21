package senders

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"gofluentd/library"

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
	library.Logger.Info("new fluent sender",
		zap.String("addr", cfg.Addr),
		zap.Strings("tags", cfg.Tags))

	if cfg.Addr == "" {
		library.Logger.Panic("addr should not be empty")
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

func (s *FluentSender) Spawn(ctx context.Context) chan<- *library.FluentMsg {
	library.Logger.Info("spawn fluentd sender")
	var (
		inChan       = make(chan *library.FluentMsg, s.InChanSize)
		childInChan  chan *library.FluentMsg // for each tag
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
						childInChan = make(chan *library.FluentMsg, s.InChanSize)
						go s.spawnChildSenderForTag(ctx, msg.Tag, childInChan)
						tag2childInChan.Store(msg.Tag, childInChan)
					} else {
						childInChan = childInChani.(chan *library.FluentMsg)
					}
					mutex.Unlock()
				} else {
					childInChan = childInChani.(chan *library.FluentMsg)
				}

				select {
				case childInChan <- msg:
				default:
					if s.DiscardWhenBlocked() {
						s.successedChan <- msg
						library.Logger.Warn("skip sender and discard msg since of its inchan is full",
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

func (s *FluentSender) spawnChildSenderForTag(ctx context.Context, tag string, inChan chan *library.FluentMsg) {
	logger := library.Logger.With(zap.String("tag", tag), zap.String("addr", s.Addr))
	logger.Info("spawn fluentd child sender")
	var (
		nRetry           int
		maxRetry         = 3
		msg              *library.FluentMsg
		msgBatch         = make([]*library.FluentMsg, s.BatchSize)
		msgBatchDelivery []*library.FluentMsg
		iBatch           = 0
		lastT            = time.Unix(0, 0)
		encoder          *library.FluentEncoder
		conn             net.Conn
		err              error
		ok               bool
	)
	ticker := time.NewTicker(s.MaxWait)
	defer ticker.Stop()

RECONNECT: // reconnect to downstream
	for {
		if conn, err = net.DialTimeout("tcp", s.Addr, 10*time.Second); err != nil {
			logger.Error("connect to fluentd server",
				zap.Error(err), zap.String("tag", tag))
			time.Sleep(time.Second)
			continue RECONNECT
		}

		logger.Info("connected to backend",
			zap.String("backend", conn.RemoteAddr().String()),
			zap.String("tag", tag))

		encoder = library.NewFluentEncoder(conn) // one encoder for each connection
	NEW_MSG:
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok = <-inChan:
				if !ok {
					logger.Info("inchan closed")
					return
				}

				msgBatch[iBatch] = msg
				iBatch++
			case <-ticker.C:
				if iBatch == 0 {
					continue NEW_MSG
				}
			}

			if iBatch < s.BatchSize &&
				utils.Clock.GetUTCNow().Sub(lastT) < s.MaxWait {
				continue NEW_MSG
			}

			lastT = utils.Clock.GetUTCNow()
			msgBatchDelivery = msgBatch[:iBatch]
			iBatch = 0
			if utils.Settings.GetBool("dry") {
				logger.Info("send message to backend",
					zap.String("log", fmt.Sprint(msgBatch[0].Message)))
				for _, msg = range msgBatchDelivery {
					s.successedChan <- msg
				}

				continue NEW_MSG
			}

			nRetry = 0
			for {
				if err = encoder.EncodeBatch(tag, msgBatchDelivery); err != nil {
					logger.Error("encode msg batch", zap.Error(err))
					nRetry++
					// resend too many times, try reconnect
					if nRetry >= maxRetry {
						logger.Error("discard msg since of sender err",
							zap.Int("num", len(msgBatchDelivery)))
						for _, msg = range msgBatchDelivery {
							s.failedChan <- msg
						}

						if err = conn.Close(); err != nil {
							logger.Error("try to close connection got error", zap.Error(err))
						}

						logger.Info("connection closed, try to reconnect...")
						continue RECONNECT
					}

					continue
				}

				break
			}

			encoder.Flush()
			logger.Debug("successed send message to backend",
				zap.Int("batch", len(msgBatchDelivery)))
			for _, msg = range msgBatchDelivery {
				s.successedChan <- msg
			}
		}
	}
}
