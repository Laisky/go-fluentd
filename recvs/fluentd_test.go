package recvs_test

import (
	"context"
	"github.com/Laisky/go-utils"
	"github.com/cespare/xxhash"
	"math/rand"
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
		NFork:           3,
		ConcatorBufSize: 1000,
		Name:            "fluentd-test",
		Addr:            "127.0.0.1:24228",
		TagKey:          "tag",
	}
	recv := recvs.NewFluentdRecv(cfg)

	recv.SetCounter(counter)
	recv.SetMsgPool(msgPool)
	recv.SetAsyncOutChan(asyncOutChan)
	recv.SetSyncOutChan(syncOutChan)

	go func() {
		recv.Run(context.Background())
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
		{
			Tag:     tag,
			Message: map[string]interface{}{"a": "b"},
			Id:      123,
		},
		{
			Tag:     tag,
			Message: map[string]interface{}{"a": "b"},
			Id:      123,
		},
		{
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

func choice(s []string) string {
	return s[rand.Intn(len(s))]
}

type hashCacheItem struct {
	v uint64
	t time.Time
}

func BenchmarkLB(b *testing.B) {
	lbkeys := []string{}
	for i := 0; i < 100; i++ {
		lbkeys = append(lbkeys, utils.RandomStringWithLength(25))
	}

	var (
		lbKey string
		hashV uint64
		// hashItem *hashCacheItem
	)
	b.Run("hash base lb", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			lbKey = choice(lbkeys)
			hashV = xxhash.Sum64String(lbKey)
		}
	})

	// hashCache := map[string]*hashCacheItem{}
	hashCache := map[string]uint64{}
	var ok bool
	// expires := 1 * time.Second
	b.Run("hash and cache", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			lbKey = choice(lbkeys)
			if _, ok = hashCache[lbKey]; ok {
				continue
			}

			hashV = xxhash.Sum64String(lbKey)
			hashCache[lbKey] = hashV
			// hashCache[lbKey] = &hashCacheItem{
			// 	t: utils.Clock.GetUTCNow(),
			// 	v: hashV,
			// }
		}

		// for lbKey, hashItem = range hashCache {
		// 	if hashItem.t.Add(expires).After(utils.Clock.GetUTCNow()) {
		// 		delete(hashCache, lbKey)
		// 	}
		// }
	})
}
