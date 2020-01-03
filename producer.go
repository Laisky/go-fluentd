package concator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-fluentd/monitor"
	"github.com/Laisky/go-fluentd/senders"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

type ProducerCfg struct {
	InChan                 chan *libs.FluentMsg
	MsgPool                *sync.Pool
	CommitChan             chan<- *libs.FluentMsg
	NFork, DiscardChanSize int
}

// Producer send messages to downstream
type Producer struct {
	*ProducerCfg
	sync.Mutex
	tag2SenderChan                        *sync.Map
	senders                               []senders.SenderItf
	discardChan, discardWithoutCommitChan chan *libs.FluentMsg
	// discardMsgCountMap = map[&msg]discardedCount
	// ⚠️Notify: cannot use `msgid` as key,
	// since of there could be two msg with same msgid arrive to producer,
	// the later one come from journal.
	//
	// stores the count of each msg, if msg's count equals to the number of sender,
	// will put the msg into discardChan.
	discardMsgCountMap *sync.Map
	counter            *utils.Counter
	pMsgPool           *sync.Pool // pending msg pool

	unSupportedTags *sync.Map
	tag2NSender     *sync.Map // map[tag]nSender
	tag2Cancel      *sync.Map // map[tag]nSender
}

type ProducerPendingDiscardMsg struct {
	Count    int
	IsCommit bool
	Msg      *libs.FluentMsg
}

// NewProducer create new producer
func NewProducer(cfg *ProducerCfg, senders ...senders.SenderItf) *Producer {
	utils.Logger.Info("create Producer")

	if cfg.NFork < 1 {
		utils.Logger.Panic("nfork must > 1", zap.Int("nfork", cfg.NFork))
	}

	p := &Producer{
		ProducerCfg:              cfg,
		senders:                  senders,
		tag2SenderChan:           &sync.Map{},
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

		unSupportedTags: &sync.Map{},
		tag2NSender:     &sync.Map{}, // map[tag]nSender
		tag2Cancel:      &sync.Map{}, // map[tag]nSender
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
			"msgPerSec": utils.Round(float64(p.counter.Get())/(time.Since(lastT).Seconds()), .5, 1),
		}
		p.counter.Set(0)
		lastT = time.Now()

		p.tag2SenderChan.Range(func(tagi, smi interface{}) bool {
			smi.(*sync.Map).Range(func(si, ci interface{}) bool {
				metrics[tagi.(string)+"."+si.(senders.SenderItf).GetName()+".ChanLen"] = len(ci.(chan<- *libs.FluentMsg))
				metrics[tagi.(string)+"."+si.(senders.SenderItf).GetName()+".ChanCap"] = cap(ci.(chan<- *libs.FluentMsg))
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
	if pmsg.IsCommit {
		p.CommitChan <- pmsg.Msg
	} else {
		// committed msg will recycled in journal
		p.MsgPool.Put(pmsg.Msg)
	}

	p.pMsgPool.Put(pmsg)
}

func (p *Producer) RunMsgCollector(ctx context.Context, tag2NSender *sync.Map, discardChan chan *libs.FluentMsg) {
	var (
		cntToDiscard int
		ok, isCommit bool
		itf          interface{}
		msg          *libs.FluentMsg
		pmsg         *ProducerPendingDiscardMsg
	)
	defer utils.Logger.Info("msg collector exit", zap.String("msg", fmt.Sprint(msg)))

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok = <-p.discardChan:
			if !ok {
				utils.Logger.Info("discardChan closed")
				return
			}
			isCommit = true
		case msg, ok = <-p.discardWithoutCommitChan:
			if !ok {
				utils.Logger.Info("discardWithoutCommitChan closed")
				return
			}
			isCommit = false
		}

		// ⚠️Warning: Do not change tag in any sender
		if itf, ok = tag2NSender.Load(msg.Tag); !ok {
			utils.Logger.Panic("[panic] tag2NSender should contains tag",
				zap.String("tag", msg.Tag),
				zap.String("msg", fmt.Sprint(msg)))
			cntToDiscard = 1
		} else {
			cntToDiscard = itf.(int)
		}

		if itf, ok = p.discardMsgCountMap.Load(msg); !ok {
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

		if pmsg.Count == cntToDiscard {
			// msg already sent by all sender
			p.discardMsgCountMap.Delete(pmsg.Msg)
			p.DiscardMsg(pmsg)
		} else {
			p.discardMsgCountMap.Store(pmsg.Msg, pmsg)
		}
	}
}

// Run starting <n> Producer to send messages
func (p *Producer) Run(ctx context.Context) {
	utils.Logger.Info("start producer")

	go p.RunMsgCollector(ctx, p.tag2NSender, p.discardChan)
	for i := 0; i < p.NFork; i++ {
		go func(i int) {
			var (
				ok, isSkip    bool
				s             senders.SenderItf
				itf           interface{}
				sender2Inchan *sync.Map // sender: chan
				nSender       int
				msg           *libs.FluentMsg
			)
			defer utils.Logger.Info("producer exit", zap.Int("i", i), zap.String("msg", fmt.Sprint(msg)))
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok = <-p.InChan:
					if !ok {
						utils.Logger.Info("InChan closed")
						return
					}
				}

				// utils.Logger.Info(fmt.Sprintf("send msg %p", msg))
				p.counter.Count()
				if _, ok = p.unSupportedTags.Load(msg.Tag); ok {
					utils.Logger.Warn("do not produce since of unsupported tag", zap.String("tag", msg.Tag))
					p.discardChan <- msg
					continue
				}

				msg.Message["msgid"] = msg.Id // set id
				if _, ok = p.tag2SenderChan.Load(msg.Tag); !ok {
					p.Lock()
					if _, ok = p.tag2SenderChan.Load(msg.Tag); !ok { // double check
						// create sender chans for new tag
						isSkip = true
						nSender = 0
						sender2Inchan = &sync.Map{}
						ctx2Tag, cancel := context.WithCancel(ctx)
						for _, s = range p.senders {
							if s.IsTagSupported(msg.Tag) {
								isSkip = false
								nSender++
								utils.Logger.Info("spawn new producer sender",
									zap.String("name", s.GetName()),
									zap.String("tag", msg.Tag))
								sender2Inchan.Store(s, s.Spawn(ctx2Tag, msg.Tag))
							}
						}

						if isSkip {
							// no sender support this tag
							utils.Logger.Warn("do not produce since of unsupported tag", zap.String("tag", msg.Tag))
							p.tag2NSender.Store(msg.Tag, 1)
							p.unSupportedTags.Store(msg.Tag, struct{}{}) // mark as unsupported
							p.discardChan <- msg
							cancel()
							p.Unlock()
							continue
						}

						utils.Logger.Info("register the number of senders for tag",
							zap.String("tag", msg.Tag),
							zap.Int("n", nSender))
						p.tag2Cancel.Store(msg.Tag, cancel)
						p.tag2NSender.Store(msg.Tag, nSender)
						p.tag2SenderChan.Store(msg.Tag, sender2Inchan)
					}
					p.Unlock()
				}

				if itf, ok = p.tag2SenderChan.Load(msg.Tag); !ok {
					utils.Logger.Panic("[panic] tag2SenderChan should contains tag", zap.String("tag", msg.Tag))
					continue
				}

				// put msg into every sender's chan
				itf.(*sync.Map).Range(func(si, val interface{}) bool {
					s = si.(senders.SenderItf)
					select {
					case val.(chan<- *libs.FluentMsg) <- msg:
					default:
						if s.DiscardWhenBlocked() {
							p.discardChan <- msg
							utils.Logger.Warn("skip sender and discard msg since of its inchan is full",
								zap.String("name", s.GetName()),
								zap.String("tag", msg.Tag))
						} else {
							p.discardWithoutCommitChan <- msg
							// utils.Logger.Debug("skip sender and not discard msg since of its inchan is full",
							// 	zap.String("name", s.GetName()),
							// 	zap.String("tag", msg.Tag))
						}
					}
					return true
				})
			}
		}(i)
	}
}
