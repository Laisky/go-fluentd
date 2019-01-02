package senders

import (
	"fmt"
	"net"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-utils"
	"go.uber.org/zap"
)

type FluentSenderCfg struct {
	Name, Addr                                  string
	Tags                                        []string
	BatchSize, InChanSize, RetryChanSize, NFork int
	MaxWait                                     time.Duration
	IsDiscardWhenBlocked                        bool
}

type FluentSender struct {
	*BaseSender
	*FluentSenderCfg
	retryMsgChan chan *libs.FluentMsg
}

func NewFluentSender(cfg *FluentSenderCfg) *FluentSender {
	utils.Logger.Info("new fluent sender",
		zap.String("addr", cfg.Addr),
		zap.Strings("tags", cfg.Tags))

	if cfg.Addr == "" {
		panic(fmt.Errorf("addr should not be empty: %v", cfg.Addr))
	}

	f := &FluentSender{
		BaseSender: &BaseSender{
			IsDiscardWhenBlocked: cfg.IsDiscardWhenBlocked,
		},
		FluentSenderCfg: cfg,
		retryMsgChan:    make(chan *libs.FluentMsg, cfg.RetryChanSize),
	}
	f.SetSupportedTags(cfg.Tags)
	return f
}

func (s *FluentSender) GetName() string {
	return s.Name
}

func (s *FluentSender) Spawn(tag string) chan<- *libs.FluentMsg {
	utils.Logger.Info("SpawnForTag", zap.String("tag", tag))
	inChan := make(chan *libs.FluentMsg, s.InChanSize) // for each tag

	for i := 0; i < s.NFork; i++ { // parallel to each tag
		go func() {
			defer utils.Logger.Error("producer exits", zap.String("tag", tag), zap.String("name", s.GetName()))

			var (
				nRetry           = 0
				maxRetry         = 3
				msg              *libs.FluentMsg
				msgBatch         = make([]*libs.FluentMsg, s.BatchSize)
				msgBatchDelivery []*libs.FluentMsg
				iBatch           = 0
				lastT            = time.Unix(0, 0)
				encoder          *libs.FluentEncoder
				conn             net.Conn
				err              error
			)

		RECONNECT: // reconnect to downstream
			conn, err = net.DialTimeout("tcp", s.Addr, 10*time.Second)
			if err != nil {
				utils.Logger.Error("try to connect to backend got error", zap.Error(err), zap.String("tag", tag))
				goto RECONNECT
			}
			utils.Logger.Info("connected to backend",
				zap.String("backend", conn.RemoteAddr().String()),
				zap.String("tag", tag))

			encoder = libs.NewFluentEncoder(conn) // one encoder for each connection
			for msg = range inChan {
				msgBatch[iBatch] = msg
				iBatch++
				if iBatch < s.BatchSize &&
					time.Now().Sub(lastT) < s.MaxWait {
					continue
				}
				lastT = time.Now()
				msgBatchDelivery = msgBatch[:iBatch]
				iBatch = 0

				nRetry = 0
				if utils.Settings.GetBool("dry") {
					utils.Logger.Info("send message to backend",
						zap.String("tag", tag),
						zap.String("log", fmt.Sprint(msgBatch[0].Message)))
					for _, msg = range msgBatchDelivery {
						s.discardChan <- msg
					}
					continue
				}

			SEND_MSG:
				if err = encoder.EncodeBatch(tag, msgBatchDelivery); err != nil {
					nRetry++
					if nRetry > maxRetry {
						utils.Logger.Error("try send message got error", zap.Error(err), zap.String("tag", tag))
						utils.Logger.Error("discard msg since of sender err", zap.String("tag", msg.Tag), zap.Int("num", len(msgBatchDelivery)))
						for _, msg = range msgBatchDelivery {
							s.discardWithoutCommitChan <- msg
						}

						if err = conn.Close(); err != nil {
							utils.Logger.Error("try to close connection got error", zap.Error(err))
						}
						utils.Logger.Info("connection closed, try to reconnect...")
						goto RECONNECT
					}
					goto SEND_MSG
				}

				utils.Logger.Debug("success sent message to backend",
					zap.String("backend", s.Addr),
					zap.String("tag", tag))
				for _, msg = range msgBatchDelivery {
					s.discardChan <- msg
				}
			}
		}()
	}

	return inChan
}
