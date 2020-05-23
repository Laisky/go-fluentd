package senders

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
)

type HTTPSenderCfg struct {
	Name, Addr                                  string
	Tags                                        []string
	BatchSize, InChanSize, RetryChanSize, NFork int
	MaxWait                                     time.Duration
	IsDiscardWhenBlocked                        bool
}

type HTTPSender struct {
	*BaseSender
	*HTTPSenderCfg
	retryMsgChan chan *libs.FluentMsg
	httpClient   *http.Client
}

func NewHTTPSender(cfg *HTTPSenderCfg) *HTTPSender {
	libs.Logger.Info("new http sender",
		zap.String("addr", cfg.Addr),
		zap.Strings("tags", cfg.Tags))

	if cfg.Addr == "" {
		panic(fmt.Errorf("addr should not be empty: %v", cfg.Addr))
	}

	s := &HTTPSender{
		BaseSender: &BaseSender{
			IsDiscardWhenBlocked: cfg.IsDiscardWhenBlocked,
		},
		HTTPSenderCfg: cfg,
		retryMsgChan:  make(chan *libs.FluentMsg, cfg.RetryChanSize),
		httpClient: &http.Client{ // default http client
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 30,
			},
			Timeout: 3 * time.Second,
		},
	}
	s.SetSupportedTags(cfg.Tags)
	return s
}

func (s *HTTPSender) GetName() string {
	return s.Name
}

func (s *HTTPSender) Spawn(ctx context.Context) chan<- *libs.FluentMsg {
	libs.Logger.Info("SpawnForTag")
	inChan := make(chan *libs.FluentMsg, s.InChanSize) // for each tag

	for i := 0; i < s.NFork; i++ { // parallel to each tag
		go func(i int) {
			defer libs.Logger.Info("producer exits",
				zap.Int("i", i),
				zap.String("name", s.GetName()))

			var (
				ok               bool
				nRetry           int
				maxRetry         = 3
				msg              *libs.FluentMsg
				msgBatch         = make([]*libs.FluentMsg, s.BatchSize)
				msgBatchDelivery []*libs.FluentMsg
				iBatch           = 0
				lastT            = time.Unix(0, 0)
				bulkCtx          = &bulkOpCtx{}
				err              error
				ticker           = time.NewTicker(s.MaxWait)
			)
			defer ticker.Stop()

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
						zap.Int("batch", len(msgBatchDelivery)),
						zap.String("log", fmt.Sprint(msgBatch[0].Message)))
					for _, msg = range msgBatchDelivery {
						s.successedChan <- msg
					}
					continue
				}

			SEND_MSG:
				if err = s.SendBulkMsgs(bulkCtx, msgBatchDelivery); err != nil {
					nRetry++
					if nRetry > maxRetry {
						libs.Logger.Error("discard msg since of sender err",
							zap.Error(err),
							zap.String("tag", msg.Tag),
							zap.Int("num", len(msgBatchDelivery)))
						for _, msg = range msgBatchDelivery {
							s.failedChan <- msg
						}

						continue
					}
					goto SEND_MSG
				}

				libs.Logger.Debug("success sent message to backend",
					zap.String("backend", s.Addr),
					zap.Int("batch", len(msgBatchDelivery)),
					zap.String("tag", msg.Tag))
				for _, msg = range msgBatchDelivery {
					s.successedChan <- msg
				}
			}
		}(i)
	}

	return inChan
}

func (s *HTTPSender) SendBulkMsgs(bulkCtx *bulkOpCtx, msgs []*libs.FluentMsg) (err error) {
	msgCnts := make([]map[string]interface{}, len(msgs))
	for i, m := range msgs {
		msgCnts[i] = m.Message
	}

	bulkCtx.buf.Reset()
	bulkCtx.gzWriter.Reset(bulkCtx.buf)
	var jb []byte
	if jb, err = json.Marshal(msgCnts); err != nil {
		return errors.Wrap(err, "try to marshal messages got error")
	}

	if _, err = bulkCtx.gzWriter.Write(jb); err != nil {
		return errors.Wrap(err, "try to compress messages got error")
	}

	bulkCtx.gzWriter.Flush()
	req, err := http.NewRequest("POST", s.Addr, bulkCtx.buf)
	if err != nil {
		return errors.Wrap(err, "try to init es request got error")
	}
	req.Header.Set("Content-encoding", "gzip")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "try to request es got error")
	}
	if err = utils.CheckResp(resp); err != nil {
		return errors.Wrap(err, "request es got error")
	}

	libs.Logger.Debug("httpforward bulk all done", zap.Int("batch", len(msgs)))
	return nil
}
