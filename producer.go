package concator

import (
	"fmt"
	"net"
	"sync"
	"time"

	"go.uber.org/zap"

	utils "github.com/Laisky/go-utils"
)

// Producer send messages to downstream
type Producer struct {
	addr    string
	msgChan <-chan *FluentMsg
	msgPool *sync.Pool
}

// NewProducer create new producer
func NewProducer(addr string, msgChan <-chan *FluentMsg, msgPool *sync.Pool) *Producer {
	utils.Logger.Info("create Producer")
	return &Producer{
		addr:    addr,
		msgChan: msgChan,
		msgPool: msgPool,
	}
}

// Run starting <n> Producer to send messages
func (p *Producer) Run(fork int, commitChan chan<- int64) {
	utils.Logger.Info("start producer", zap.String("addr", p.addr))

	var (
		msg                *FluentMsg
		retryMsgChan       = make(chan *FluentMsg, 500)
		producerTagChanMap = map[string]chan<- *FluentMsg{}
		ok                 bool
	)

	for {
		select {
		case msg = <-retryMsgChan:
		case msg = <-p.msgChan:
		}

		if _, ok = producerTagChanMap[msg.Tag]; !ok {
			producerTagChanMap[msg.Tag] = p.SpawnForTag(fork, msg.Tag, commitChan)
		}
		producerTagChanMap[msg.Tag] <- msg
	}

}

func (p *Producer) SpawnForTag(fork int, tag string, commitChan chan<- int64) chan<- *FluentMsg {
	utils.Logger.Info("SpawnForTag", zap.Int("fork", fork), zap.String("tag", tag))
	var (
		inChan = make(chan *FluentMsg, 1000)
	)

	for i := 0; i < fork; i++ { // parallel to each tag
		go func() {
			defer utils.Logger.Error("producer exits", zap.String("tag", tag))

			var (
				nRetry           = 0
				maxRetry         = 3
				id               int64
				retryMsgChan     = make(chan *FluentMsg, 50)
				msg              *FluentMsg
				maxNBatch        = 100
				msgBatch         = make([]*FluentMsg, maxNBatch)
				msgBatchDelivery []*FluentMsg
				iBatch           = 0
				lastT            = time.Unix(0, 0)
				maxWait          = 10 * time.Second
				encoder          *Encoder
			)

		RECONNECT:
			conn, err := net.DialTimeout("tcp", p.addr, 10*time.Second)
			if err != nil {
				utils.Logger.Error("try to connect to backend got error", zap.Error(err), zap.String("tag", tag))
				time.Sleep(1 * time.Second)
				goto RECONNECT
			}
			defer conn.Close()
			utils.Logger.Info("connected to backend",
				zap.String("backend", conn.RemoteAddr().String()),
				zap.String("tag", tag))

			encoder = NewEncoder(conn)
			for msg = range inChan {
				msgBatch[iBatch] = msg
				iBatch++
				if iBatch < maxNBatch &&
					time.Now().Sub(lastT) < maxWait {
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
							zap.String("log", fmt.Sprintf("%v", msgBatch[0].Message)))
						goto FINISHED
					}

					if err = encoder.EncodeBatch(tag, msgBatchDelivery); err != nil {
						nRetry++
						if nRetry > maxRetry {
							utils.Logger.Error("try send message got error", zap.Error(err), zap.String("tag", tag))

							for _, msg = range msgBatchDelivery {
								retryMsgChan <- msg
							}

							if err = conn.Close(); err != nil {
								utils.Logger.Error("try to close connection got error", zap.Error(err))
							}
							utils.Logger.Info("connection closed, try to reconnect...")
							goto RECONNECT
						}

						time.Sleep(100 * time.Microsecond)
						continue
					}

					utils.Logger.Debug("success sent message to backend", zap.String("backend", p.addr), zap.String("tag", tag))
					goto FINISHED
				}

			FINISHED:
				for _, msg = range msgBatchDelivery {
					commitChan <- msg.Id
					if msg.extIds != nil {
						for _, id = range msg.extIds {
							commitChan <- id
						}
					}

					msg.extIds = nil
					p.msgPool.Put(msg)
				}
			}
		}()
	}

	return inChan
}
