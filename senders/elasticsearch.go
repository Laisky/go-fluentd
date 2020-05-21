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

	"github.com/Laisky/go-fluentd/libs"
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
		logger: libs.Logger.Named(cfg.Name),
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
		s.NFork = 1
		s.logger.Info("reset n_fork", zap.Int("n_fork", s.NFork))
	}
	if s.BatchSize <= 0 {
		s.BatchSize = 500
		s.logger.Info("reset msg_batch_size", zap.Int("msg_batch_size", s.BatchSize))
	}
	if s.MaxWait <= 0 {
		s.MaxWait = 5
		s.logger.Info("reset max_wait_sec", zap.Duration("max_wait_sec", s.MaxWait))
	}
	if s.TagKey == "" {
		s.logger.Info("`tag_key` is missing, will load msg tag from `msg.Tag`, this tag could be modified by upstream")
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
	msg      *libs.FluentMsg
}

func (s *ElasticSearchSender) getMsgStarting(msg *libs.FluentMsg) ([]byte, error) {
	// load origin tag from messages, because msg.Tag could modified by kafka
	var tag string
	if s.TagKey != "" {
		tag = msg.Message[s.TagKey].(string)
	} else {
		tag = msg.Tag
	}

	// load elasitcsearch index name by msg tag
	index, ok := s.TagIndexMap[tag]
	if !ok {
		return nil, fmt.Errorf("tag `%v` not exists in indices", tag)
	}

	return []byte("{\"index\": {\"_index\": \"" + index + "\", \"_type\": \"logs\"}}\n"), nil
}

func (s *ElasticSearchSender) SendBulkMsgs(bulkCtx *bulkOpCtx, msgs []*libs.FluentMsg) (err error) {
	if len(msgs) == 0 {
		return nil
	}

	bulkCtx.cnt = bulkCtx.cnt[:0]
	var b []byte
	for _, bulkCtx.msg = range msgs {
		if bulkCtx.starting, err = s.getMsgStarting(bulkCtx.msg); err != nil {
			s.logger.Warn("try to generate bulk index", zap.Error(err))
			continue
		}

		if b, err = json.Marshal(bulkCtx.msg.Message); err != nil {
			s.logger.Warn("try to marshal messages", zap.Error(err))
			continue
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

	bulkCtx.buf.Reset()
	bulkCtx.gzWriter.Reset(bulkCtx.buf)
	if _, err = bulkCtx.gzWriter.Write(bulkCtx.cnt); err != nil {
		return errors.Wrap(err, "try to compress messages")
	}

	bulkCtx.gzWriter.Close()
	req, err := http.NewRequest("POST", s.Addr, bulkCtx.buf)
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
	Id     string `json:"_id"`
	Index  string `json:"_index"`
	Status int    `json:"status"`
}

func isStatusCodeOk(s int) bool {
	return s/100 == 2
}

func (s *ElasticSearchSender) checkResp(resp *http.Response) (err error) {
	if !isStatusCodeOk(resp.StatusCode) {
		err = fmt.Errorf("server return error code %v", resp.StatusCode)
	}

	ret := &ESResp{}
	bb, err2 := ioutil.ReadAll(resp.Body)
	if err2 != nil {
		s.logger.Error("try to read es resp body",
			zap.Error(err2),
			zap.Error(err))
		return errors.Wrap(err2, "try to read es resp body")
	}
	if err != nil {
		return errors.Wrap(err, string(bb))
	}
	s.logger.Debug("got es response", zap.ByteString("resp", bb))

	if err = json.Unmarshal(bb, ret); err != nil {
		s.logger.Error("try to unmarshal body, body",
			zap.Error(err),
			zap.ByteString("body", bb))
		return nil
	}

	// if ret.Errors {
	// 	// // ignore mapping type conflict & fileds number exceeds
	// 	// if bytes.Contains(bb, []byte("mapper_parsing_exception")) ||
	// 	// 	bytes.Contains(bb, []byte("Limit of total fields")) {
	// 	// 	s.logger.Warn("rejected by es", zap.ByteString("error", bb))
	// 	// 	return nil
	// 	// }

	// 	// thread pool exceeds
	// 	if bytes.Contains(bb, []byte("EsThreadPool")) {
	// 		return fmt.Errorf("es return error: %v", string(bb))
	// 	}
	// }

	return nil
}

func (s *ElasticSearchSender) Spawn(ctx context.Context) chan<- *libs.FluentMsg {
	s.logger.Info("spawn elasticsearch sender")
	inChan := make(chan *libs.FluentMsg, s.InChanSize) // for each tag

	for i := 0; i < s.NFork; i++ { // parallel to each tag
		go func(i int) {
			var (
				maxRetry         = 3
				msg              *libs.FluentMsg
				msgBatch         = make([]*libs.FluentMsg, s.BatchSize)
				msgBatchDelivery []*libs.FluentMsg
				iBatch           = 0
				lastT            = time.Unix(0, 0)
				err              error
				bulkCtx          = &bulkOpCtx{
					cnt: []byte{},
				}
				nRetry, j int
				ok        bool
				ticker    = time.NewTicker(s.MaxWait)
			)
			defer ticker.Stop()
			defer s.logger.Info("producer exits",
				zap.Int("i", i),
				zap.String("name", s.GetName()))

			bulkCtx.buf = &bytes.Buffer{}
			bulkCtx.gzWriter = gzip.NewWriter(bulkCtx.buf)

		NEW_MSG_LOOP:
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok = <-inChan:
					if !ok {
						s.logger.Info("inChan closed")
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
					for j, msg = range msgBatchDelivery {
						s.logger.Info("send message to backend",
							zap.String("log", fmt.Sprint(msgBatch[j].Message)))
						s.successedChan <- msg
					}
					continue
				}

				for {
					if err = s.SendBulkMsgs(bulkCtx, msgBatchDelivery); err != nil {
						nRetry++
						if nRetry > maxRetry {
							s.logger.Error("try send message",
								zap.Error(err),
								zap.ByteString("content", bulkCtx.cnt),
								zap.Int("num", len(msgBatchDelivery)))
							for _, msg = range msgBatchDelivery {
								s.failedChan <- msg
							}
							continue NEW_MSG_LOOP
						}
						continue
					}

					break
				}

				s.logger.Debug("success sent message to backend",
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
