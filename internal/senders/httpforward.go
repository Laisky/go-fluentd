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

	if cfg.NFork <= 0 {
		cfg.NFork = 1
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 500
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = 5 * time.Second
	}
	if cfg.InChanSize < 0 {
		cfg.InChanSize = 0
	}
	if cfg.RetryChanSize < 0 {
		cfg.RetryChanSize = 0
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
	in := make(chan *library.FluentMsg, s.InChanSize)
	for i := 0; i < s.NFork; i++ {
		go func() {
			bulk := &bulkOpCtx{}
			s.runBatches(ctx, in, s.BatchSize, s.MaxWait, func(ctx context.Context, msgs []*library.FluentMsg) error { return s.sendBulkMsgs(ctx, bulk, msgs) })
		}()
	}
	return in
}

func (s *HTTPSender) SendBulkMsgs(bulkCtx *bulkOpCtx, msgs []*library.FluentMsg) error {
	return s.sendBulkMsgs(context.Background(), bulkCtx, msgs)
}

func (s *HTTPSender) sendBulkMsgs(ctx context.Context, bulkCtx *bulkOpCtx, msgs []*library.FluentMsg) error {
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
	req, err := http.NewRequestWithContext(ctx, "POST", s.Addr, bulkCtx.buf)
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
