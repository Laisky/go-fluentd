package concator

import (
	"sync"
	"time"

	"github.com/Laisky/go-concator/libs"
	"github.com/Laisky/go-concator/monitor"
	"github.com/Laisky/go-concator/senders"
	utils "github.com/Laisky/go-utils"
	"go.uber.org/zap"
)

type ProducerCfg struct {
	InChan          chan *libs.FluentMsg
	MsgPool         *sync.Pool
	CommitChan      chan<- int64
	DiscardChanSize int
}

// Producer send messages to downstream
type Producer struct {
	*ProducerCfg
	producerTagChanMap *sync.Map
	senders            []senders.SenderItf
	discardChan        chan *libs.FluentMsg
	discardMsgCountMap *sync.Map
	counter            *utils.Counter
}

// NewProducer create new producer
func NewProducer(cfg *ProducerCfg, senders ...senders.SenderItf) *Producer {
	utils.Logger.Info("create Producer")

	p := &Producer{
		ProducerCfg:        cfg,
		senders:            senders,
		producerTagChanMap: &sync.Map{},
		discardChan:        make(chan *libs.FluentMsg, cfg.DiscardChanSize),
		discardMsgCountMap: &sync.Map{},
		counter:            utils.NewCounter(),
	}
	p.registerMonitor()

	for _, s := range senders {
		utils.Logger.Info("enable sender", zap.String("name", s.GetName()))
		s.SetCommitChan(cfg.CommitChan)
		s.SetMsgPool(cfg.MsgPool)
		s.SetDiscardChan(p.discardChan)
	}

	return p
}

// registerMonitor bind monitor for producer
func (p *Producer) registerMonitor() {
	lastT := time.Now()
	monitor.AddMetric("producer", func() map[string]interface{} {
		metrics := map[string]interface{}{
			"msgPerSec": utils.Round(float64(p.counter.Get())/(time.Now().Sub(lastT).Seconds()), .5, 1),
		}
		p.counter.Set(0)
		lastT = time.Now()

		p.producerTagChanMap.Range(func(tagi, smi interface{}) bool {
			smi.(*sync.Map).Range(func(namei, ci interface{}) bool {
				metrics[tagi.(string)+"."+namei.(string)+".ChanLen"] = len(ci.(chan<- *libs.FluentMsg))
				metrics[tagi.(string)+"."+namei.(string)+".ChanCap"] = cap(ci.(chan<- *libs.FluentMsg))
				return true
			})
			return true
		})
		metrics["discardChanLen"] = len(p.discardChan)
		metrics["discardChanCap"] = cap(p.discardChan)

		// get discardMsgCountMap length
		nMsg := 0
		p.discardMsgCountMap.Range(func(k, v interface{}) bool {
			nMsg++
			return true
		})
		metrics["waitToDiscardMsgNum"] = nMsg
		return metrics
	})
}

func (p *Producer) DiscardMsg(msg *libs.FluentMsg) {
	utils.Logger.Debug("recycle msg", zap.Int64("id", msg.Id), zap.String("tag", msg.Tag))
	p.CommitChan <- msg.Id
	if msg.ExtIds != nil {
		for _, id := range msg.ExtIds {
			p.CommitChan <- id
		}
		msg.ExtIds = nil
	}
	p.MsgPool.Put(msg)
}

func (p *Producer) RunDiscard(nSenderForTagMap *sync.Map, discardChan chan *libs.FluentMsg) {
	var (
		cnt int
		ok  bool
		itf interface{}
	)
	for msg := range discardChan {
		itf, _ = p.discardMsgCountMap.LoadOrStore(msg.Id, 0)
		cnt = itf.(int)
		cnt++
		if itf, ok = nSenderForTagMap.Load(msg.Tag); !ok {
			utils.Logger.Error("nSenderForTagMap should contains tag", zap.String("tag", msg.Tag))
			continue
		}

		if cnt == itf.(int) {
			// msg already sent by all sender
			p.discardMsgCountMap.Delete(msg.Id)
			p.DiscardMsg(msg)
		} else {
			p.discardMsgCountMap.Store(msg.Id, cnt)
		}
	}
}

// Run starting <n> Producer to send messages
func (p *Producer) Run() {
	utils.Logger.Info("start producer")

	var (
		msg              *libs.FluentMsg
		ok               bool
		s                senders.SenderItf
		unSupportedTags  = map[string]struct{}{}
		nSenderForTagMap = &sync.Map{} // map[tag]nSender
		isSkip           = true
		itf              interface{}
		senderChanMap    *sync.Map
		nSender          int
	)

	go p.RunDiscard(nSenderForTagMap, p.discardChan)

	for msg = range p.InChan {
		p.counter.Count()
		if _, ok = unSupportedTags[msg.Tag]; ok {
			utils.Logger.Warn("do not produce since of unsupported tag", zap.String("tag", msg.Tag))
			p.DiscardMsg(msg)
			continue
		}

		msg.Message["tag"] = msg.Tag // set tag

		if _, ok = p.producerTagChanMap.Load(msg.Tag); !ok {
			isSkip = true
			nSender = 0
			senderChanMap = &sync.Map{}
			for _, s = range p.senders {
				if s.IsTagSupported(msg.Tag) {
					isSkip = false
					nSender++
					utils.Logger.Info("spawn new producer sender",
						zap.String("name", s.GetName()),
						zap.String("tag", msg.Tag))
					senderChanMap.Store(s.GetName(), s.Spawn(msg.Tag))
				}
			}

			if isSkip {
				utils.Logger.Warn("do not produce since of unsupported tag", zap.String("tag", msg.Tag))
				unSupportedTags[msg.Tag] = struct{}{}
				p.DiscardMsg(msg)
				continue
			}

			nSenderForTagMap.Store(msg.Tag, nSender)
			p.producerTagChanMap.Store(msg.Tag, senderChanMap)
		}

		if itf, ok = p.producerTagChanMap.Load(msg.Tag); !ok {
			utils.Logger.Error("producerTagChanMap should contains tag", zap.String("tag", msg.Tag))
			continue
		}

		senderChanMap = itf.(*sync.Map)
		senderChanMap.Range(func(key, val interface{}) bool {
			select {
			case val.(chan<- *libs.FluentMsg) <- msg:
			default:
				utils.Logger.Warn("skip sender",
					zap.String("name", key.(string)),
					zap.String("tag", msg.Tag))
			}

			return true
		})
	}
}
