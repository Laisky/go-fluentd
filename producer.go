package concator

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/Laisky/go-concator/libs"
	utils "github.com/Laisky/go-utils"
	"github.com/kataras/iris"
	"go.uber.org/zap"
)

type ProducerCfg struct {
	Addr      string
	InChan    chan *libs.FluentMsg
	MsgPool   *sync.Pool
	BatchSize int
	MaxWait   time.Duration
}

// Producer send messages to downstream
type Producer struct {
	*ProducerCfg
	producerTagChanMap map[string]chan<- *libs.FluentMsg
	retryMsgChan       chan *libs.FluentMsg
}

// NewProducer create new producer
func NewProducer(cfg *ProducerCfg) *Producer {
	utils.Logger.Info("create Producer",
		zap.String("backend", cfg.Addr),
		zap.Duration("maxWait", cfg.MaxWait))
	p := &Producer{
		ProducerCfg:        cfg,
		producerTagChanMap: map[string]chan<- *libs.FluentMsg{},
		retryMsgChan:       make(chan *libs.FluentMsg, 500),
	}
	p.BindMonitor()
	return p
}

// BindMonitor bind monitor for producer
func (p *Producer) BindMonitor() {
	utils.Logger.Info("bind `/monitor/producer`")
	Server.Get("/monitor/producer", func(ctx iris.Context) {
		cnt := "producerTagChanMap tag:chan\n"
		for tag, c := range p.producerTagChanMap {
			cnt += fmt.Sprintf("> %v: %v\n", tag, len(c))
		}
		cnt += fmt.Sprintf("> retryMsgChan: %v\n", len(p.retryMsgChan))
		ctx.Writef(cnt)
	})
}

// Run starting <n> Producer to send messages
func (p *Producer) Run(fork int, commitChan chan<- int64) {
	utils.Logger.Info("start producer", zap.String("addr", p.Addr))

	var (
		msg *libs.FluentMsg
		ok  bool
	)

	for {
		select {
		case msg = <-p.retryMsgChan:
		case msg = <-p.InChan:
		}

		if _, ok = p.producerTagChanMap[msg.Tag]; !ok {
			p.producerTagChanMap[msg.Tag] = p.SpawnForTag(fork, msg.Tag, commitChan)
		}

		select {
		case p.producerTagChanMap[msg.Tag] <- msg:
		default:
		}
	}

}

// SpawnForTag spawn `fork` numbers connections to downstream for each tag
func (p *Producer) SpawnForTag(fork int, tag string, commitChan chan<- int64) chan<- *libs.FluentMsg {
	utils.Logger.Info("SpawnForTag", zap.Int("fork", fork), zap.String("tag", tag))
	var (
		inChan = make(chan *libs.FluentMsg, 1000) // for each tag
	)

	for i := 0; i < fork; i++ { // parallel to each tag
		go func() {
			defer utils.Logger.Error("producer exits", zap.String("tag", tag))

			var (
				nRetry           = 0
				maxRetry         = 3
				id               int64
				msg              *libs.FluentMsg
				msgBatch         = make([]*libs.FluentMsg, p.BatchSize)
				msgBatchDelivery []*libs.FluentMsg
				iBatch           = 0
				lastT            = time.Unix(0, 0)
				encoder          *Encoder
				conn             net.Conn
				err              error
			)

		RECONNECT: // reconnect to downstream
			conn, err = net.DialTimeout("tcp", p.Addr, 10*time.Second)
			if err != nil {
				utils.Logger.Error("try to connect to backend got error", zap.Error(err), zap.String("tag", tag))
				time.Sleep(1 * time.Second)
				goto RECONNECT
			}
			utils.Logger.Info("connected to backend",
				zap.String("backend", conn.RemoteAddr().String()),
				zap.String("tag", tag))

			encoder = NewEncoder(conn) // one encoder for each connection
			for msg = range inChan {
				msgBatch[iBatch] = msg
				iBatch++
				if iBatch < p.BatchSize &&
					time.Now().Sub(lastT) < p.MaxWait {
					continue
				}
				lastT = time.Now()
				msgBatchDelivery = msgBatch[:iBatch]
				iBatch = 0

				nRetry = 0
				for {
					if utils.Settings.GetBool("dry") {
						utils.Logger.Info("send message to backend",
							zap.String("tag", tag),
							zap.String("log", fmt.Sprint(msgBatch[0].Message)))
						goto FINISHED
					}

					if err = encoder.EncodeBatch(tag, msgBatchDelivery); err != nil {
						nRetry++
						if nRetry > maxRetry {
							utils.Logger.Error("try send message got error", zap.Error(err), zap.String("tag", tag))

							for _, msg = range msgBatchDelivery { // put back
								select {
								case p.retryMsgChan <- msg:
								default:
								}
							}

							if err = conn.Close(); err != nil {
								utils.Logger.Error("try to close connection got error", zap.Error(err))
							}
							utils.Logger.Info("connection closed, try to reconnect...")
							goto RECONNECT
						}

						time.Sleep(100 * time.Millisecond)
						continue
					}

					utils.Logger.Debug("success sent message to backend",
						zap.String("backend", p.Addr),
						zap.String("tag", tag))
					goto FINISHED
				}

			FINISHED:
				for _, msg = range msgBatchDelivery {
					commitChan <- msg.Id
					if msg.ExtIds != nil {
						for _, id = range msg.ExtIds {
							commitChan <- id
						}
					}

					msg.ExtIds = nil
					p.MsgPool.Put(msg)
				}
			}
		}()
	}

	return inChan
}
