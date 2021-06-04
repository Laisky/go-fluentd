package tagfilters

import (
	"regexp"
	"testing"
)

// func BenchmarkConcator(b *testing.B) {
// 	// utils.SetupLogger("debug")
// 	cf := &Factory{
// 		BaseTagFilterFactory: &tagfilters.BaseTagFilterFactory{},
// 	}
// 	pMsgPool := &sync.Pool{
// 		New: func() interface{} {
// 			return &tagfilters.PendingMsg{}
// 		},
// 	}
// 	msgPool := &sync.Pool{
// 		New: func() interface{} {
// 			return &libs.FluentMsg{}
// 		},
// 	}
// 	inChan := make(chan *libs.FluentMsg, 2000)
// 	outChan := make(chan *libs.FluentMsg, 10000)

// 	go func() {
// 		i := 0.0
// 		now := time.Now()
// 		for msg := range outChan {
// 			i++
// 			if time.Now().Sub(now) > 1*time.Second {
// 				now = time.Now()
// 				b.Logf(">> msg: %v\n", string(msg.Message["log"].([]byte)))
// 				b.Logf("%v/s\n", i/time.Now().Sub(now).Seconds())
// 				i = 0
// 			}
// 		}
// 	}()

// 	c := tagfilters.NewConcator(&tagfilters.ConcatorCfg{
// 		Cf:         cf,
// 		MaxLen:     100000,
// 		Tag:        "spring.sit",
// 		MsgKey:     "log",
// 		Identifier: "container_id",
// 		MsgPool:    msgPool,
// 		PMsgPool:   pMsgPool,
// 		OutChan:    outChan,
// 		Regexp:     regexp.MustCompile(`^\d{4}-\d{2}-\d{2} +\d{2}:\d{2}:\d{2}\.\d{3} {0,}\|`),
// 	})
// 	go c.Run(inChan)

// 	var (
// 		msg1, msg2 *libs.FluentMsg
// 	)

// 	b.Run("concator", func(b *testing.B) {
// 		for i := 0; i < b.N; i++ {
// 			if rand.Float64() <= 0.5 {
// 				msg1 = msgPool.Get().(*libs.FluentMsg)
// 				msg1.Tag = "app.spring.sit"
// 				msg1.Id = 1
// 				msg1.Message = map[string]interface{}{
// 					"log":          "2018-11-21 17:05:22.514 | test | INFO  | http-nio-8080-exec-1 | com.laisky.cloud.cp.core.service.impl.CPBusiness.reflectAdapterRequest | 84: 123454321",
// 					"container_id": "docker",
// 				}
// 				inChan <- msg1
// 			} else {
// 				msg2 = msgPool.Get().(*libs.FluentMsg)
// 				msg2.Tag = "app.spring.sit"
// 				msg2.Id = 2
// 				msg2.Message = map[string]interface{}{
// 					"log":          "12345",
// 					"container_id": "docker",
// 				}
// 				inChan <- msg2
// 			}
// 		}
// 	})

// 	b.Error("done")
// }

func BenchmarkRegexp(b *testing.B) {
	log := "2018-11-21 17:05:22.514 | test | INFO  | http-nio-8080-exec-1 | com.laisky.cloud.cp.core.service.impl.CPBusiness.reflectAdapterRequest | 84: 123454321"
	re := regexp.MustCompile(`(?ms)^(?P<time>.{23}) {0,}\| {0,}(?P<app>[^ ]+) {0,}\| {0,}(?P<level>[^ ]+) {0,}\| {0,}(?P<thread>[^ ]+) {0,}\| {0,}(?P<class>[^ ]+) {0,}\| {0,}(?P<line>\d+) {0,}([\|:] {0,}(?P<args>\{.*\}))?([\|:] {0,}(?P<message>.*))?`)

	b.Run("regexp", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if !re.MatchString(log) {
				b.Fatal("should match")
			}
		}
	})

	log = "2018-02-01 16:15:43.518 - ms:cp|type:platform|uuid:4f99962d-c272-43bb-85d9-20ab030180b7|dateTime:2018-02-01 16:15:43.518|customerSid:27|customerCode:DT00000000|customerName:默认"
	reAcceptor := regexp.MustCompile(`ms:cp`)
	b.Run("regexpAcceptor", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if !reAcceptor.MatchString(log) {
				b.Fatal("should match")
			}
		}
	})
}
