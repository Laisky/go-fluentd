package senders

import (
	"bytes"
	"compress/gzip"
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

func (s *ElasticSearchSender) SendBulkMsgs(ctx *bulkOpCtx, msgs []*libs.FluentMsg) (err error) {
	ctx.cnt = ctx.cnt[:0]
	for _, ctx.msg = range msgs {
		if ctx.starting, err = s.getMsgStarting(ctx.msg); err != nil {
			utils.Logger.Warn("try to generate bulk index got error", zap.Error(err))
			continue
		}

		b, err := json.Marshal(ctx.msg.Message)
		if err != nil {
			return errors.Wrap(err, "try to marshal messages got error")
		}

		utils.Logger.Debug("prepare bulk content send to es",
			zap.ByteString("starting", ctx.starting),
			zap.ByteString("body", b))
		ctx.cnt = append(ctx.cnt, ctx.starting...)
		ctx.cnt = append(ctx.cnt, b...)
		ctx.cnt = append(ctx.cnt, '\n')
	}

	ctx.buf.Reset()
	ctx.gzWriter.Reset(ctx.buf)
	if _, err = ctx.gzWriter.Write(ctx.cnt); err != nil {
		return errors.Wrap(err, "try to compress messages got error")
	}

	ctx.gzWriter.Flush()
	req, err := http.NewRequest("POST", s.Addr, ctx.buf)
	if err != nil {
		return errors.Wrap(err, "try to init es request got error")
	}
	req.Close = true
	req.Header.Set("Content-encoding", "gzip")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "try to request es got error")
	}
	defer resp.Body.Close()

	if err = s.checkResp(resp); err != nil {
		return errors.Wrap(err, "request es got error")
	}

	utils.Logger.Debug("elasticsearch bulk all done", zap.Int("batch", len(msgs)))
	return nil
}

type ESResp struct {
	Errors bool        `json:"errors"`
	Items  []*ESOpResp `json:"items"`
}

type ESOpResp struct {
	Index *ESIndexResp `json:"index"`
}

type ESIndexResp struct {
	Id     string `json:"_id"`
	Index  string `json:"_index"`
	Status int    `json:"status"`
}

func (s *ElasticSearchSender) checkResp(resp *http.Response) (err error) {
	if utils.FloorDivision(resp.StatusCode, 100) != 2 {
		return fmt.Errorf("server return error code %v", resp.StatusCode)
	}

	ret := &ESResp{}
	bb, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		utils.Logger.Error("try to read es resp body got error", zap.Error(err))
		return nil
	}

	if err = json.Unmarshal(bb, ret); err != nil {
		utils.Logger.Error("try to unmarshal body got error, body",
			zap.Error(err),
			zap.ByteString("body", bb))
		return nil
	}

	for _, v := range ret.Items {
		if utils.FloorDivision(v.Index.Status, 100) != 2 {
			// do not retry if there is part of msgs got error
			utils.Logger.Warn("bulk got error for idx",
				zap.ByteString("body", bb),
				zap.String("idx", v.Index.Index),
				zap.Int("status", v.Index.Status))
			return nil
		}
	}

	return nil
}

func (s *ElasticSearchSender) Spawn(tag string) chan<- *libs.FluentMsg {
	utils.Logger.Info("SpawnForTag", zap.String("tag", tag))
	inChan := make(chan *libs.FluentMsg, s.InChanSize) // for each tag
	go s.runFlusher(inChan)

	for i := 0; i < s.NFork; i++ { // parallel to each tag
		go func() {
			defer utils.Logger.Error("producer exits", zap.String("tag", tag), zap.String("name", s.GetName()))

			var (
				maxRetry         = 3
				msg              *libs.FluentMsg
				msgBatch         = make([]*libs.FluentMsg, s.BatchSize)
				msgBatchDelivery []*libs.FluentMsg
				iBatch           = 0
				lastT            = time.Unix(0, 0)
				err              error
				ctx              = &bulkOpCtx{
					cnt: []byte{},
				}
				nRetry, j int
			)

			ctx.buf = &bytes.Buffer{}
			ctx.gzWriter = gzip.NewWriter(ctx.buf)

			for msg = range inChan {
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
						utils.Logger.Info("send message to backend",
							zap.String("tag", tag),
							zap.String("log", fmt.Sprint(msgBatch[j].Message)))
						s.discardChan <- msg
					}
					continue
				}

			SEND_MSG:
				if err = s.SendBulkMsgs(ctx, msgBatchDelivery); err != nil {
					nRetry++
					if nRetry > maxRetry {
						utils.Logger.Error("try send message got error",
							zap.Error(err),
							zap.String("tag", tag),
							zap.Int("num", len(msgBatchDelivery)))
						for _, msg = range msgBatchDelivery {
							s.discardWithoutCommitChan <- msg
						}
						continue
					}
					goto SEND_MSG
				}

				utils.Logger.Debug("success sent message to backend",
					zap.String("backend", s.Addr),
					zap.Int("batch", len(msgBatchDelivery)),
					zap.String("tag", msg.Tag))
				for _, msg = range msgBatchDelivery {
					s.discardChan <- msg
				}
			}
		}()
	}

	return inChan
}
