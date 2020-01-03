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
	Name, Addr, TagKey                          string
	Tags                                        []string
	BatchSize, InChanSize, RetryChanSize, NFork int
	MaxWait                                     time.Duration
	TagIndexMap                                 map[string]string
	IsDiscardWhenBlocked                        bool
}

type ElasticSearchSender struct {
	*BaseSender
	*ElasticSearchSenderCfg
	logger       *utils.LoggerType
	retryMsgChan chan *libs.FluentMsg
	httpClient   *http.Client
}

func NewElasticSearchSender(cfg *ElasticSearchSenderCfg) *ElasticSearchSender {
	utils.Logger.Info("new ElasticSearch sender",
		zap.String("addr", cfg.Addr),
		zap.Int("batch_size", cfg.BatchSize),
		zap.Strings("tags", cfg.Tags))

	if cfg.Addr == "" {
		panic(fmt.Errorf("addr should not be empty: %v", cfg.Addr))
	}

	s := &ElasticSearchSender{
		logger: utils.Logger.With(zap.String("name", cfg.Name)),
		BaseSender: &BaseSender{
			IsDiscardWhenBlocked: cfg.IsDiscardWhenBlocked,
		},
		ElasticSearchSenderCfg: cfg,
		retryMsgChan:           make(chan *libs.FluentMsg, cfg.RetryChanSize),
		httpClient: &http.Client{ // default http client
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 20,
			},
			Timeout: 30 * time.Second,
		},
	}
	s.SetSupportedTags(cfg.Tags)
	return s
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
	tag := msg.Message[s.TagKey].(string)

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
			s.logger.Warn("try to generate bulk index got error", zap.Error(err))
			continue
		}

		if b, err = json.Marshal(bulkCtx.msg.Message); err != nil {
			s.logger.Warn("try to marshal messages got error", zap.Error(err))
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
		return errors.Wrap(err, "try to compress messages got error")
	}

	bulkCtx.gzWriter.Close()
	req, err := http.NewRequest("POST", s.Addr, bulkCtx.buf)
	if err != nil {
		return errors.Wrap(err, "try to init es request got error")
	}
	req.Close = true
	req.Header.Set("Content-encoding", "gzip")
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "try to request es got error")
	}
	defer resp.Body.Close()

	if err = s.checkResp(resp); err != nil {
		// TODO: should ignore mapping & field type conlifct
		return errors.Wrap(err, "request es got error")
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
		s.logger.Error("try to read es resp body got error",
			zap.Error(err2),
			zap.Error(err))
		return errors.Wrap(err2, "try to read es resp body got error")
	}
	if err != nil {
		return errors.Wrap(err, string(bb))
	}
	s.logger.Debug("got es response", zap.ByteString("resp", bb))

	if err = json.Unmarshal(bb, ret); err != nil {
		s.logger.Error("try to unmarshal body got error, body",
			zap.Error(err),
			zap.ByteString("body", bb))
		return nil
	}

	if ret.Errors {
		return fmt.Errorf("es return error: %v", string(bb))
	}

	return nil
}

func (s *ElasticSearchSender) Spawn(ctx context.Context, tag string) chan<- *libs.FluentMsg {
	s.logger.Info("SpawnForTag", zap.String("tag", tag))
	inChan := make(chan *libs.FluentMsg, s.InChanSize) // for each tag
	go s.runFlusher(ctx, inChan)

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
			)
			defer s.logger.Info("producer exits",
				zap.String("tag", tag),
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
				}

				if msg != nil {
					msgBatch[iBatch] = msg
					iBatch++
				} else if iBatch == 0 {
					continue
				} else {
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
							zap.String("tag", tag),
							zap.String("log", fmt.Sprint(msgBatch[j].Message)))
						s.discardChan <- msg
					}
					continue
				}

				for {
					if err = s.SendBulkMsgs(bulkCtx, msgBatchDelivery); err != nil {
						nRetry++
						if nRetry > maxRetry {
							s.logger.Error("try send message got error",
								zap.Error(err),
								zap.String("tag", tag),
								zap.ByteString("content", bulkCtx.cnt),
								zap.Int("num", len(msgBatchDelivery)))
							for _, msg = range msgBatchDelivery {
								s.discardWithoutCommitChan <- msg
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
					s.discardChan <- msg
				}
			}
		}(i)
	}

	return inChan
}
