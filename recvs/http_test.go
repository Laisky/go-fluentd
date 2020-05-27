package recvs_test

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-fluentd/recvs"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/gin-gonic/gin"
)

var (
	httpsrv = gin.New()
	salt    = []byte("2ji3r32r932r32j932jf92")
)

func TestHTTPRecv(t *testing.T) {
	var (
		err          error
		syncOutChan  = make(chan *libs.FluentMsg, 1000)
		asyncOutChan = make(chan *libs.FluentMsg, 1000)
	)

	cfg := &recvs.HTTPRecvCfg{
		Name:               "test-http-srv",
		HTTPSrv:            httpsrv,
		Env:                "sit",
		MsgKey:             "log",
		TagKey:             "tag",
		OrigTag:            "wechat",
		Tag:                "forward-wechat",
		Path:               "/api/v1/log/wechat/:env",
		SigKey:             "sig",
		SigSalt:            salt,
		MaxBodySize:        1000,
		TSRegexp:           regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}.\d{3}Z$`),
		TimeKey:            "@timestamp",
		TimeFormat:         "2006-01-02T15:04:05.000Z",
		MaxAllowedDelaySec: 300 * time.Second,
		MaxAllowedAheadSec: 60 * time.Second,
	}
	httprecv := recvs.NewHTTPRecv(cfg)

	httprecv.SetCounter(counter)
	httprecv.SetMsgPool(msgPool)
	httprecv.SetAsyncOutChan(asyncOutChan)
	httprecv.SetSyncOutChan(syncOutChan)

	port := 24888
	addr := fmt.Sprintf("localhost:%v", port)
	go func() {
		for {
			if err := httpsrv.Run(addr); err != nil {
				libs.Logger.Error("try to run server got error", zap.Error(err))
				port++
				addr = fmt.Sprintf("localhost:%v", port)
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	resp := map[string]interface{}{}
	if err = utils.RequestJSON("post", "http://"+addr+"/api/v1/log/wechat/sit", &utils.RequestData{Data: fakeReq()}, &resp); err != nil {
		t.Fatalf("got error: %+v", err)
	}

	// check resp
	t.Logf("got resp: %+v", resp)
	if vi, ok := resp["msgid"]; !ok {
		t.Fatalf("should contains msgid")
	} else {
		switch vi := vi.(type) {
		case int:
			t.Logf("got id: %v", vi)
		case float64:
			t.Logf("got id: %v", vi)
		default:
			t.Fatalf("msgid should be int")
		}
	}

	// check msg
	var msg *libs.FluentMsg
	select {
	case msg = <-asyncOutChan:
	default:
		t.Fatalf("can not load msg")
	}

	if msg.Tag != "forward-wechat.sit" {
		t.Fatalf("tag not correct, got %v", msg.Tag)
	}
	if msg.Message["tag"].(string) != "wechat.sit" {
		t.Fatalf("orig tag not correct, got %v", msg.Message["tag"].(string))
	}
	if msg.Message["url"].(string) != "abc" {
		t.Fatalf("msg not correct, got %v", msg.Message["url"].(string))
	}

}

func fakeReq() map[string]string {
	ts := utils.UTCNow().Format("2006-01-02T15:04:05.000Z")
	hash := md5.Sum(append([]byte(ts), salt...))
	sig := hex.EncodeToString(hash[:])

	return map[string]string{
		"@timestamp": ts,
		"url":        "abc",
		"sig":        sig,
	}

}
