package concator

import (
	"sync"

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
	producerTagChanMap map[string]map[string]chan<- *libs.FluentMsg
	senders            []senders.SenderItf
	discardChan        chan *libs.FluentMsg
	discardMsgCountMap map[int64]int
}

// NewProducer create new producer
func NewProducer(cfg *ProducerCfg, senders ...senders.SenderItf) *Producer {
	utils.Logger.Info("create Producer")

	p := &Producer{
		ProducerCfg:        cfg,
		senders:            senders,
		producerTagChanMap: map[string]map[string]chan<- *libs.FluentMsg{},
		discardChan:        make(chan *libs.FluentMsg, cfg.DiscardChanSize),
		discardMsgCountMap: map[int64]int{},
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
	monitor.AddMetric("producer", func() map[string]interface{} {
		metrics := map[string]interface{}{}
		for tag, cm := range p.producerTagChanMap {
			for name, c := range cm {
				metrics[tag+"."+name+".ChanLen"] = len(c)
				metrics[tag+"."+name+".ChanCap"] = cap(c)
			}
		}
		metrics["discardChanLen"] = len(p.discardChan)
		metrics["discardChanCap"] = cap(p.discardChan)
		metrics["waitToDiscardMsgNum"] = len(p.discardMsgCountMap)
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

func (p *Producer) RunDiscard(nSenderForTagMap map[string]int, discardChan chan *libs.FluentMsg) {
	var (
		ok bool
	)
	for msg := range discardChan {
		if _, ok = p.discardMsgCountMap[msg.Id]; !ok {
			p.discardMsgCountMap[msg.Id] = 0
		}

		p.discardMsgCountMap[msg.Id]++
		if p.discardMsgCountMap[msg.Id] == nSenderForTagMap[msg.Tag] {
			delete(p.discardMsgCountMap, msg.Id)
			p.DiscardMsg(msg)
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
		c                chan<- *libs.FluentMsg
		unSupportedTags  = map[string]struct{}{}
		nSenderForTagMap = map[string]int{} // map[tag]nSender
		isSkip           = true
		name             string
	)

	go p.RunDiscard(nSenderForTagMap, p.discardChan)

	for msg = range p.InChan {
		if _, ok = unSupportedTags[msg.Tag]; ok {
			utils.Logger.Warn("do not produce since of unsupported tag", zap.String("tag", msg.Tag))
			p.DiscardMsg(msg)
			continue
		}

		msg.Message["tag"] = msg.Tag // set tag
		if _, ok = p.producerTagChanMap[msg.Tag]; !ok {
			isSkip = true
			p.producerTagChanMap[msg.Tag] = map[string]chan<- *libs.FluentMsg{}
			nSenderForTagMap[msg.Tag] = 0
			for _, s = range p.senders {
				if s.IsTagSupported(msg.Tag) {
					isSkip = false
					nSenderForTagMap[msg.Tag]++
					utils.Logger.Info("spawn new producer sender",
						zap.String("name", s.GetName()),
						zap.String("tag", msg.Tag))
					p.producerTagChanMap[msg.Tag][s.GetName()] = s.Spawn(msg.Tag)
				}
			}

			if isSkip {
				utils.Logger.Warn("do not produce since of unsupported tag", zap.String("tag", msg.Tag))
				unSupportedTags[msg.Tag] = struct{}{}
				p.DiscardMsg(msg)
				continue
			}
		}

		for name, c = range p.producerTagChanMap[msg.Tag] {
			select {
			case c <- msg:
			default:
				utils.Logger.Warn("skip sender",
					zap.String("name", name),
					zap.String("tag", msg.Tag))
			}
		}
	}
}
