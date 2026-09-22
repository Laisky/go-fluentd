package senders

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"gofluentd/library"
	"gofluentd/library/log"

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
	retryMsgChan chan *library.FluentMsg
	httpClient   *http.Client
}

func NewHTTPSender(cfg *HTTPSenderCfg) *HTTPSender {
	log.Logger.Info("new http sender",
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
		retryMsgChan:  make(chan *library.FluentMsg, cfg.RetryChanSize),
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

func (s *HTTPSender) Spawn(ctx context.Context) chan<- *library.FluentMsg {
	log.Logger.Info("SpawnForTag")
	inChan := make(chan *library.FluentMsg, s.InChanSize) // for each tag

	for i := 0; i < s.NFork; i++ { // parallel to each tag
		go func(i int) {
			defer log.Logger.Info("producer exits",
				zap.Int("i", i),
				zap.String("name", s.GetName()))

			var (
				ok               bool
				nRetry           int
				maxRetry         = 3
				msg              *library.FluentMsg
				msgBatch         = make([]*library.FluentMsg, s.BatchSize)
				msgBatchDelivery []*library.FluentMsg
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
						log.Logger.Info("inChan closed")
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
					log.Logger.Info("send message to backend",
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
						log.Logger.Error("discard msg since of sender err",
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

				log.Logger.Debug("success sent message to backend",
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

func (s *HTTPSender) SendBulkMsgs(bulkCtx *bulkOpCtx, msgs []*library.FluentMsg) error {
	if len(msgs) == 0 {
		return nil
	}
	if bulkCtx == nil {
		return fmt.Errorf("nil bulk context")
	}
	records := make([]map[string]interface{}, 0, len(msgs))
	for _, msg := range msgs {
		if msg == nil {
			return fmt.Errorf("nil message in HTTP batch")
		}
		records = append(records, msg.Message)
	}
	data, err := utils.JSON.Marshal(records)
	if err != nil {
		return errors.Wrap(err, "marshal HTTP batch")
	}
	bulkCtx.reset()
	if _, err = bulkCtx.gzWriter.Write(data); err != nil {
		return errors.Wrap(err, "compress HTTP batch")
	}
	// Flush alone does not write the gzip trailer.
	if err = bulkCtx.gzWriter.Close(); err != nil {
		return errors.Wrap(err, "finish gzip HTTP batch")
	}
	req, err := http.NewRequest("POST", s.Addr, bulkCtx.buf)
	if err != nil {
		return errors.Wrap(err, "create HTTP request")
	}
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "send HTTP batch")
	}
	defer resp.Body.Close()
	// Bound diagnostic bodies; do not let a failing peer exhaust memory.
	if resp.StatusCode/100 != 2 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if readErr != nil {
			return errors.Wrap(readErr, "read HTTP error response")
		}
		return fmt.Errorf("HTTP batch returned status %d: %s", resp.StatusCode, body)
	}
	// Drain small successful responses for connection reuse, but always close.
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	return errors.Wrap(err, "read HTTP response")
}
