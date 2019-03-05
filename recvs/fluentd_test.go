package recvs_test

import (
	"net"
	"testing"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-fluentd/recvs"
)

func TestFluentdRecv(t *testing.T) {
	var (
		err          error
		syncOutChan  = make(chan *libs.FluentMsg, 1000)
		asyncOutChan = make(chan *libs.FluentMsg, 1000)

		// cfg
		tag = "test.sit"
	)

	cfg := &recvs.FluentdRecvCfg{
		Name:   "fluentd-test",
		Addr:   "127.0.0.1:24228",
		TagKey: "tag",
	}
	recv := recvs.NewFluentdRecv(cfg)

	recv.SetCounter(counter)
	recv.SetMsgPool(msgPool)
	recv.SetAsyncOutChan(asyncOutChan)
	recv.SetSyncOutChan(syncOutChan)

	go func() {
		recv.Run()
	}()
	time.Sleep(100 * time.Millisecond)
	cnt := 0

	// send signle msg
	cnt++
	conn, err := net.DialTimeout("tcp", cfg.Addr, 1*time.Second)
	if err != nil {
		t.Fatalf("got error: %+v", err)
	}
	defer conn.Close()

	msg := &libs.FluentMsg{
		Tag:     tag,
		Message: map[string]interface{}{"a": "b"},
		Id:      123,
	}
	encoder := libs.NewFluentEncoder(conn)
	if err = encoder.Encode(msg); err != nil {
		t.Fatalf("got error: %+v", err)
	}
	encoder.Flush()
	time.Sleep(100 * time.Millisecond)

	// send msg batch
	cnt += 3
	msgBatch := []*libs.FluentMsg{
		&libs.FluentMsg{
			Tag:     tag,
			Message: map[string]interface{}{"a": "b"},
			Id:      123,
		},
		&libs.FluentMsg{
			Tag:     tag,
			Message: map[string]interface{}{"a": "b"},
			Id:      123,
		},
		&libs.FluentMsg{
			Tag:     tag,
			Message: map[string]interface{}{"a": "b"},
			Id:      123,
		},
	}
	if err = encoder.EncodeBatch(tag, msgBatch); err != nil {
		t.Fatalf("got error: %+v", err)
	}
	encoder.Flush()
	time.Sleep(100 * time.Millisecond)

	// check msg
	for {
		if cnt == 0 {
			break
		}
		cnt--

		select {
		case msg = <-asyncOutChan:
		default:
			t.Fatalf("can not load msg")
		}
		t.Log("load 1 msg")

		if msg.Tag != tag {
			t.Fatalf("tag not correct, got %v", msg.Tag)
		}
		if msg.Message["tag"].(string) != tag {
			t.Fatalf("orig tag not correct, got %v", msg.Message["tag"].(string))
		}
		if msg.Message["a"].(string) != "b" {
			t.Fatalf("msg not correct, got %v", msg.Message["a"].(string))
		}
	}

}
