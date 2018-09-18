package concator

import (
	"fmt"
	"net"
	"sync"
	"time"

	"go.uber.org/zap"

	utils "github.com/Laisky/go-utils"
	"github.com/ugorji/go/codec"
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
	wg := &sync.WaitGroup{}
	for i := 0; i < fork; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			utils.Logger.Info("start producer", zap.Int("n", n), zap.String("addr", p.addr))

			var (
				nRetry       = 0
				maxRetry     = 3
				id           int64
				msgWrap      = []interface{}{nil, nil, nil}
				retryMsgChan = make(chan *FluentMsg, 50)
				msg          *FluentMsg
				encoder      *codec.Encoder
			)

		RECONNECT:
			conn, err := net.DialTimeout("tcp", p.addr, 10*time.Second)
			if err != nil {
				utils.Logger.Error("try to connect to backend got error", zap.Error(err), zap.Int("n", n))
				time.Sleep(5 * time.Second)
				goto RECONNECT
			}
			defer conn.Close()
			utils.Logger.Info("connected to backend", zap.String("backend", conn.RemoteAddr().String()))

			encoder = codec.NewEncoder(conn, NewCodec())
			for {
				select {
				case msg = <-retryMsgChan:
				case msg = <-p.msgChan:
				}

				nRetry = 0
				for {
					msgWrap[0] = msg.Tag
					msgWrap[2] = msg.Message
					if utils.Settings.GetBool("dry") {
						utils.Logger.Info("send message to backend",
							zap.String("tag", msg.Tag),
							zap.String("log", fmt.Sprintf("%v", msg.Message)),
							zap.Int("n", n))
						goto FINISHED
					}

					err = encoder.Encode(msgWrap)
					if err != nil {
						nRetry++
						if nRetry > maxRetry {
							utils.Logger.Error("try send message got error",
								zap.Error(err),
								zap.Int("n", n),
								zap.String("tag", msg.Tag))

							retryMsgChan <- msg
							conn.Close()
							utils.Logger.Warn("connection closed, try to reconnect...")
							goto RECONNECT
						}

						time.Sleep(1 * time.Second)
						continue
					}

					utils.Logger.Debug("success sent message to backend", zap.String("backend", p.addr))
					goto FINISHED
				}

			FINISHED:
				commitChan <- msg.Id
				if msg.extIds != nil {
					for _, id = range msg.extIds {
						commitChan <- id
					}
				}

				msg.extIds = nil
				p.msgPool.Put(msg)
			}
		}(i)
	}

	wg.Wait()
}
