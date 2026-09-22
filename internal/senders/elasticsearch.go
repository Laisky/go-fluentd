package senders

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"gofluentd/library"
	"gofluentd/library/log"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
)

func LoadESTagIndexMap(env string, mapi interface{}) map[string]string {
	tagIndexMap := map[string]string{}
	for tag, indexi := range mapi.(map[string]interface{}) {
		tagIndexMap[strings.Replace(tag, "{env}", env, -1)] = strings.Replace(indexi.(string), "{env}", env, -1)
	}

	return tagIndexMap
}

type ElasticSearchSenderCfg struct {
	Name, Addr, TagKey           string
	Tags                         []string
	BatchSize, InChanSize, NFork int
	MaxWait                      time.Duration
	TagIndexMap                  map[string]string
	IsDiscardWhenBlocked         bool
}

type ElasticSearchSender struct {
	*BaseSender
	*ElasticSearchSenderCfg
	logger     *utils.LoggerType
	httpClient *http.Client
}

func NewElasticSearchSender(cfg *ElasticSearchSenderCfg) *ElasticSearchSender {
	s := &ElasticSearchSender{
		logger: log.Logger.Named(cfg.Name),
		BaseSender: &BaseSender{
			IsDiscardWhenBlocked: cfg.IsDiscardWhenBlocked,
		},
		ElasticSearchSenderCfg: cfg,
		httpClient: &http.Client{ // default http client
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 20,
			},
			Timeout: 30 * time.Second,
		},
	}
	if err := s.valid(); err != nil {
		s.logger.Panic("invalid", zap.Error(err))
	}

	s.SetSupportedTags(cfg.Tags)
	s.logger.Info("new elasticsearch sender",
		zap.String("addr", s.Addr),
		zap.Int("batch_size", s.BatchSize),
		zap.Int("n_fork", s.NFork),
		zap.Duration("max_wait_sec", s.MaxWait),
		zap.Strings("tags", s.Tags),
		zap.String("tag_key", s.TagKey),
	)
	return s
}

func (s *ElasticSearchSender) valid() error {
	if s.Addr == "" {
		s.logger.Panic("`addr` not set")
	}

	if s.NFork <= 0 {
		s.NFork = 3
		s.logger.Info("reset n_fork", zap.Int("n_fork", s.NFork))
	}

	if s.BatchSize <= 0 {
		s.BatchSize = 500
		s.logger.Info("reset msg_batch_size", zap.Int("msg_batch_size", s.BatchSize))
	}

	if s.MaxWait <= 0 {
		s.MaxWait = 5 * time.Second
		s.logger.Info("reset max_wait_sec", zap.Duration("max_wait_sec", s.MaxWait))
	}

	return nil
}

func (s *ElasticSearchSender) GetName() string {
	return s.Name
}

type bulkOpCtx struct {
	buf      *bytes.Buffer
	gzWriter *gzip.Writer
	cnt      []byte
	starting []byte
	msg      *library.FluentMsg
}

func (s *ElasticSearchSender) getMsgStarting(msg *library.FluentMsg) ([]byte, error) {
	// load origin tag from messages, because msg.Tag could modified by kafka
	var (
		tag string
		ok  bool
	)
	if s.TagKey != "" {
		if tag, ok = msg.Message[s.TagKey].(string); !ok {
			return nil, fmt.Errorf("empty tag load by key `%s`", s.TagKey)
		}
	} else {
		tag = msg.Tag
	}

	// load elasitcsearch index name by msg tag
	index, ok := s.TagIndexMap[tag]
	if !ok {
		return nil, fmt.Errorf("tag `%v` not exists in indices", tag)
	}

	metadata, err := utils.JSON.Marshal(map[string]interface{}{
		"index": map[string]string{"_index": index, "_type": "logs"},
	})
	if err != nil {
		return nil, errors.Wrap(err, "encode bulk metadata")
	}
	return append(metadata, '\n'), nil
}

func (s *ElasticSearchSender) SendBulkMsgs(bulkCtx *bulkOpCtx, msgs []*library.FluentMsg) error {
	return s.sendBulkMsgs(context.Background(), bulkCtx, msgs)
}

func (s *ElasticSearchSender) sendBulkMsgs(ctx context.Context, bulkCtx *bulkOpCtx, msgs []*library.FluentMsg) (err error) {
	if len(msgs) == 0 {
		return nil
	}

	bulkCtx.cnt = bulkCtx.cnt[:0]
	var b []byte
	for _, bulkCtx.msg = range msgs {
		if bulkCtx.starting, err = s.getMsgStarting(bulkCtx.msg); err != nil {
			return errors.Wrap(err, "prepare bulk index")
		}

		if b, err = utils.JSON.Marshal(bulkCtx.msg.Message); err != nil {
			return errors.Wrap(err, "marshal bulk message")
		}

		s.logger.Debug("prepare bulk content send to es",
			zap.ByteString("starting", bulkCtx.starting),
			zap.ByteString("body", b))
		bulkCtx.cnt = append(bulkCtx.cnt, bulkCtx.starting...)
		bulkCtx.cnt = append(bulkCtx.cnt, b...)
		bulkCtx.cnt = append(bulkCtx.cnt, '\n')
	}

	if len(bulkCtx.cnt) == 0 {
		return nil
	}

	bulkCtx.reset()
	if _, err = bulkCtx.gzWriter.Write(bulkCtx.cnt); err != nil {
		return errors.Wrap(err, "try to compress messages")
	}

	if err = bulkCtx.gzWriter.Close(); err != nil {
		return errors.Wrap(err, "finish gzip bulk batch")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.Addr, bulkCtx.buf)
	if err != nil {
		return errors.Wrap(err, "try to init es request")
	}
	req.Close = true
	req.Header.Set("Content-encoding", "gzip")
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "try to request es")
	}
	defer resp.Body.Close()

	if err = s.checkResp(resp); err != nil {
		return errors.Wrap(err, "request es")
	}

	s.logger.Debug("elasticsearch bulk all done", zap.Int("batch", len(msgs)))
	return nil
}

type ESResp struct {
	Errors bool `json:"errors"`
	// Items  []*ESOpResp `json:"items"`
}

type ESOpResp struct {
	Index *ESIndexResp `json:"index"`
}

type ESIndexResp struct {
	ID     string `json:"_id"`
	Index  string `json:"_index"`
	Status int    `json:"status"`
}

func isStatusCodeOk(s int) bool {
	return s/100 == 2
}

func (s *ElasticSearchSender) checkResp(resp *http.Response) error {
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return errors.Wrap(err, "read Elasticsearch response")
	}
	if !isStatusCodeOk(resp.StatusCode) {
		return fmt.Errorf("Elasticsearch returned status %d: %s", resp.StatusCode, body)
	}
	// A missing result is not evidence of successful delivery. Keep accepting
	// filter_path=errors responses, but require an explicit boolean result.
	var result struct {
		Errors *bool                     `json:"errors"`
		Items  []map[string]*ESIndexResp `json:"items"`
	}
	if err = utils.JSON.Unmarshal(body, &result); err != nil {
		return errors.Wrap(err, "decode Elasticsearch response")
	}
	if result.Errors == nil {
		return fmt.Errorf("Elasticsearch response is missing the errors result")
	}
	if *result.Errors {
		return fmt.Errorf("Elasticsearch rejected one or more bulk items: %s", body)
	}
	// filter_path=errors is valid. When item results are supplied, however,
	// a contradictory or malformed result cannot establish delivery.
	for i, item := range result.Items {
		if len(item) != 1 {
			return fmt.Errorf("invalid Elasticsearch bulk item %d", i)
		}
		for _, operation := range item {
			if operation == nil || !isStatusCodeOk(operation.Status) {
				return fmt.Errorf("unsuccessful Elasticsearch bulk item %d", i)
			}
		}
	}
	return nil
}

func (s *ElasticSearchSender) Spawn(ctx context.Context) chan<- *library.FluentMsg {
	in := make(chan *library.FluentMsg, s.InChanSize)
	for i := 0; i < s.NFork; i++ {
		go func() {
			bulk := &bulkOpCtx{}
			s.runBatches(ctx, in, s.BatchSize, s.MaxWait, func(ctx context.Context, msgs []*library.FluentMsg) error { return s.sendBulkMsgs(ctx, bulk, msgs) })
		}()
	}
	return in
}
