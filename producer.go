package concator

import (
	"fmt"
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
	producerTagChanMap                    *sync.Map
	senders                               []senders.SenderItf
	discardChan, discardWithoutCommitChan chan *libs.FluentMsg
	// discardMsgCountMap = map[msgId]discardedCount
	// stores the count of each msg, if msg's count equals to the number of sender,
	// then put the msg into discardChan.
	discardMsgCountMap *sync.Map
	counter            *utils.Counter
	pMsgPool           *sync.Pool // pending msg pool
}

type ProducerPendingDiscardMsg struct {
	Count    int
	IsCommit bool
	Msg      *libs.FluentMsg
}

// NewProducer create new producer
func NewProducer(cfg *ProducerCfg, senders ...senders.SenderItf) *Producer {
	utils.Logger.Info("create Producer")

	p := &Producer{
		ProducerCfg:              cfg,
		senders:                  senders,
		producerTagChanMap:       &sync.Map{},
		discardChan:              make(chan *libs.FluentMsg, cfg.DiscardChanSize),
		discardWithoutCommitChan: make(chan *libs.FluentMsg, cfg.DiscardChanSize),
		discardMsgCountMap:       &sync.Map{},
		counter:                  utils.NewCounter(),
		pMsgPool: &sync.Pool{
			New: func() interface{} {
				return &ProducerPendingDiscardMsg{
					IsCommit: true,
				}
			},
		},
	}
	p.registerMonitor()

	for _, s := range senders {
		utils.Logger.Info("enable sender", zap.String("name", s.GetName()))
		s.SetCommitChan(cfg.CommitChan)
		s.SetMsgPool(cfg.MsgPool)
		s.SetDiscardChan(p.discardChan)
		s.SetDiscardWithoutCommitChan(p.discardWithoutCommitChan)
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

func (p *Producer) DiscardMsg(pmsg *ProducerPendingDiscardMsg) {
	utils.Logger.Debug("recycle pmsg", zap.Int64("id", pmsg.Msg.Id), zap.String("tag", pmsg.Msg.Tag))
	if pmsg.IsCommit {
		p.CommitChan <- pmsg.Msg.Id
		if pmsg.Msg.ExtIds != nil {
			for _, id := range pmsg.Msg.ExtIds {
				p.CommitChan <- id
			}
		}
	}

	pmsg.Msg.ExtIds = nil
	p.MsgPool.Put(pmsg.Msg)
	p.pMsgPool.Put(pmsg)
}

func (p *Producer) RunMsgCollector(nSenderForTagMap *sync.Map, discardChan chan *libs.FluentMsg) {
	var (
		targetCnt    int
		ok, isCommit bool
		itf          interface{}
		msg          *libs.FluentMsg
		pmsg         *ProducerPendingDiscardMsg
	)

	for {
		select {
		case msg = <-p.discardChan:
			isCommit = true
		case msg = <-p.discardWithoutCommitChan:
			isCommit = false
		}

		if itf, ok = nSenderForTagMap.Load(msg.Tag); !ok {
			utils.Logger.Error("[panic] nSenderForTagMap should contains tag",
				zap.String("tag", msg.Tag),
				zap.String("msg", fmt.Sprintf("%+v", msg)))
			continue
		}
		targetCnt = itf.(int)

		if itf, ok = p.discardMsgCountMap.Load(msg.Id); !ok {
			// create new pmsg
			pmsg = p.pMsgPool.Get().(*ProducerPendingDiscardMsg)
			pmsg.IsCommit = isCommit
			pmsg.Count = 1
			pmsg.Msg = msg
		} else {
			// update existsing pmsg
			pmsg = itf.(*ProducerPendingDiscardMsg)
			pmsg.IsCommit = pmsg.IsCommit && isCommit
			pmsg.Count++
		}

		if pmsg.Count == targetCnt {
			// msg already sent by all sender
			p.discardMsgCountMap.Delete(msg.Id)
			p.DiscardMsg(pmsg)
		} else {
			p.discardMsgCountMap.Store(msg.Id, pmsg)
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

	go p.RunMsgCollector(nSenderForTagMap, p.discardChan)

	for msg = range p.InChan {
		p.counter.Count()
		if _, ok = unSupportedTags[msg.Tag]; ok {
			utils.Logger.Warn("do not produce since of unsupported tag", zap.String("tag", msg.Tag))
			p.discardChan <- msg
			continue
		}

		msg.Message["tag"] = msg.Tag  // set tag
		msg.Message["msgid"] = msg.Id // set id

		if _, ok = p.producerTagChanMap.Load(msg.Tag); !ok {
			// create sender chans for new tag
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
				// no sender support this tag
				utils.Logger.Warn("do not produce since of unsupported tag", zap.String("tag", msg.Tag))
				unSupportedTags[msg.Tag] = struct{}{} // mark as unsupported
				p.discardChan <- msg
				continue
			}

			nSenderForTagMap.Store(msg.Tag, nSender)
			p.producerTagChanMap.Store(msg.Tag, senderChanMap)
		}

		if itf, ok = p.producerTagChanMap.Load(msg.Tag); !ok {
			utils.Logger.Error("[panic] producerTagChanMap should contains tag", zap.String("tag", msg.Tag))
			continue
		}

		// put msg into every sender's chan
		senderChanMap = itf.(*sync.Map)
		senderChanMap.Range(func(key, val interface{}) bool {
			select {
			case val.(chan<- *libs.FluentMsg) <- msg:
			default:
				utils.Logger.Warn("skip sender",
					zap.String("name", key.(string)),
					zap.String("tag", msg.Tag))
				p.discardWithoutCommitChan <- msg // at least once
			}

			return true
		})
	}
}
