package senders

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	utils "github.com/Laisky/go-utils"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	"github.com/Laisky/go-fluentd/libs"
)

var (
	httpClient = &http.Client{ // default http client
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 20,
		},
		Timeout: time.Duration(30) * time.Second,
	}
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
}

func NewElasticSearchSender(cfg *ElasticSearchSenderCfg) *ElasticSearchSender {
	utils.Logger.Info("new ElasticSearch sender",
		zap.String("addr", cfg.Addr),
		zap.Int("batch_size", cfg.BatchSize),
		zap.Strings("tags", cfg.Tags))

	if cfg.Addr == "" {
		panic(fmt.Errorf("addr should not be empty: %v", cfg.Addr))
	}

	f := &ElasticSearchSender{
		BaseSender: &BaseSender{
			IsDiscardWhenBlocked: cfg.IsDiscardWhenBlocked,
		},
		ElasticSearchSenderCfg: cfg,
		retryMsgChan:           make(chan *libs.FluentMsg, cfg.RetryChanSize),
	}
	f.SetSupportedTags(cfg.Tags)
	return f
}

func (s *ElasticSearchSender) GetName() string {
	return s.Name
}

type ESBulkCtx struct {
	body     []byte
	buf      *bytes.Buffer
	gzWriter *gzip.Writer
}

func (s *ElasticSearchSender) getMsgStarting(msg *libs.FluentMsg) ([]byte, error) {
	// load message origin tag
	tag := msg.Message[s.TagKey].(string)

	// load elasitcsearch index name by msg tag
	index, ok := s.TagIndexMap[tag]
	if !ok {
		return nil, fmt.Errorf("tag `%v` not exists in indices", msg.Tag)
	}

	return []byte("{\"index\": {\"_index\": \"" + index + "\", \"_type\": \"logs\"}}\n"), nil
}

func (s *ElasticSearchSender) SendBulkMsgs(ctx *ESBulkCtx, msgs []*libs.FluentMsg) (err error) {
	cnt := []byte{}
	var starting []byte
	for _, m := range msgs {
		if starting, err = s.getMsgStarting(m); err != nil {
			utils.Logger.Warn("try to generate bulk index got error", zap.Error(err))
			continue
		}

		b, err := json.Marshal(m.Message)
		if err != nil {
			return errors.Wrap(err, "try to marshal messages got error")
		}

		utils.Logger.Debug("prepare bulk content send to es",
			zap.ByteString("starting", starting),
			zap.ByteString("body", b))
		cnt = append(cnt, starting...)
		cnt = append(cnt, b...)
		cnt = append(cnt, '\n')
	}

	ctx.buf.Reset()
	ctx.gzWriter.Reset(ctx.buf)
	// ctx.gzWriter = gzip.NewWriter(ctx.buf)
	if _, err = ctx.gzWriter.Write(cnt); err != nil {
		return errors.Wrap(err, "try to compress messages got error")
	}

	ctx.gzWriter.Flush()
	req, err := http.NewRequest("POST", s.Addr, ctx.buf)
	if err != nil {
		return errors.Wrap(err, "try to init es request got error")
	}
	req.Close = true
	req.Header.Set("Content-encoding", "gzip")
	resp, err := httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "try to request es got error")
	}
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
		return errors.Wrap(err, "try to read es resp body got error")
	}
	defer resp.Body.Close()

	if err = json.Unmarshal(bb, ret); err != nil {
		return errors.Wrapf(err, "try to unmarshal body got error, body: %v", string(bb))
	}

	for _, v := range ret.Items {
		if utils.FloorDivision(v.Index.Status, 100) != 2 {
			return fmt.Errorf("bulk got error for idx: %v", v.Index.Status)
		}
	}

	return nil
}

func (s *ElasticSearchSender) Spawn(tag string) chan<- *libs.FluentMsg {
	utils.Logger.Info("SpawnForTag", zap.String("tag", tag))
	inChan := make(chan *libs.FluentMsg, s.InChanSize) // for each tag

	for i := 0; i < s.NFork; i++ { // parallel to each tag
		go func() {
			defer utils.Logger.Error("producer exits", zap.String("tag", tag))

			var (
				nRetry           = 0
				maxRetry         = 3
				msg              *libs.FluentMsg
				msgBatch         = make([]*libs.FluentMsg, s.BatchSize)
				msgBatchDelivery []*libs.FluentMsg
				iBatch           = 0
				lastT            = time.Unix(0, 0)
				err              error
				ctx              = &ESBulkCtx{}
				j                int
			)

			ctx.buf = &bytes.Buffer{}
			ctx.gzWriter = gzip.NewWriter(ctx.buf)

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
					for j, msg = range msgBatchDelivery {
						utils.Logger.Info("send message to backend",
							zap.String("tag", tag),
							zap.String("log", fmt.Sprintf("%+v", msgBatch[j].Message)))
						s.discardChan <- msg
					}
					continue
				}

			SEND_MSG:
				if err = s.SendBulkMsgs(ctx, msgBatchDelivery); err != nil {
					nRetry++
					if nRetry > maxRetry {
						utils.Logger.Error("try send message got error", zap.Error(err), zap.String("tag", tag))
						utils.Logger.Error("discard msg since of sender err", zap.String("tag", msg.Tag), zap.Int("num", len(msgBatchDelivery)))
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
