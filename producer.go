package concator

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-fluentd/monitor"
	"github.com/Laisky/go-fluentd/senders"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

type ProducerCfg struct {
	DistributeKey          string
	InChan                 chan *libs.FluentMsg
	MsgPool                *sync.Pool
	CommitChan             chan<- *libs.FluentMsg
	NFork, DiscardChanSize int
}

// Producer send messages to downstream
type Producer struct {
	*ProducerCfg
	sync.Mutex
	tag2SenderChan            *sync.Map
	senders                   []senders.SenderItf
	successedChan, failedChan chan *libs.FluentMsg
	// discardMsgCountMap = map[&msg]discardedCount
	// ⚠️Notify: cannot use `msgid` as key,
	// since of there could be two msg with same msgid arrive to producer,
	// the later one come from journal.
	//
	// stores the count of each msg, if msg's count equals to the number of sender,
	// will put the msg into successedChan.
	discardMsgCountMap *sync.Map
	counter            *utils.Counter
	pMsgPool           *sync.Pool // pending msg pool

	unSupportedTags *sync.Map
	tag2NSender     *sync.Map // map[tag]Sender
	tag2Cancel      *sync.Map // map[tag]senderCancel
}

type pendingDiscardMsg struct {
	count          int
	isAllSuccessed bool
	msg            *libs.FluentMsg
}

// NewProducer create new producer
func NewProducer(cfg *ProducerCfg, senders ...senders.SenderItf) *Producer {
	utils.Logger.Info("create Producer")

	if cfg.NFork < 1 {
		utils.Logger.Panic("nfork must > 1", zap.Int("nfork", cfg.NFork))
	}

	p := &Producer{
		ProducerCfg:        cfg,
		senders:            senders,
		tag2SenderChan:     &sync.Map{},
		successedChan:      make(chan *libs.FluentMsg, cfg.DiscardChanSize),
		failedChan:         make(chan *libs.FluentMsg, cfg.DiscardChanSize),
		discardMsgCountMap: &sync.Map{},
		counter:            utils.NewCounter(),
		pMsgPool: &sync.Pool{
			New: func() interface{} {
				return &pendingDiscardMsg{}
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
		s.SetSuccessedChan(p.successedChan)
		s.SetFailedChan(p.failedChan)
	}

	return p
}

// registerMonitor bind monitor for producer
func (p *Producer) registerMonitor() {
	monitor.AddMetric("producer", func() map[string]interface{} {
		metrics := map[string]interface{}{
			"msgPerSec": p.counter.GetSpeed(),
			"msgTotal":  p.counter.Get(),
		}

		p.tag2SenderChan.Range(func(tagi, smi interface{}) bool {
			smi.(*sync.Map).Range(func(si, ci interface{}) bool {
				metrics[tagi.(string)+"."+si.(senders.SenderItf).GetName()+".ChanLen"] = len(ci.(chan<- *libs.FluentMsg))
				metrics[tagi.(string)+"."+si.(senders.SenderItf).GetName()+".ChanCap"] = cap(ci.(chan<- *libs.FluentMsg))
				return true
			})
			return true
		})
		metrics["discardChanLen"] = len(p.successedChan)
		metrics["discardChanCap"] = cap(p.successedChan)

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

func (p *Producer) discardMsg(pmsg *pendingDiscardMsg) {
	if pmsg.isAllSuccessed {
		p.CommitChan <- pmsg.msg
	} else {
		// committed msg will recycled in journal
		p.MsgPool.Put(pmsg.msg)
	}

	p.pMsgPool.Put(pmsg)
}

func (p *Producer) runMsgCollector(ctx context.Context, tag2NSender *sync.Map, successedChan chan *libs.FluentMsg) {
	var (
		cntToDiscard    int
		ok, isSuccessed bool
		itf             interface{}
		msg             *libs.FluentMsg
		pmsg            *pendingDiscardMsg
	)
	defer utils.Logger.Info("msg collector exit", zap.String("msg", fmt.Sprint(msg)))

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok = <-p.successedChan:
			if !ok {
				utils.Logger.Info("successedChan closed")
				return
			}
			isSuccessed = true
		case msg, ok = <-p.failedChan:
			if !ok {
				utils.Logger.Info("failedChan closed")
				return
			}
			isSuccessed = false
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
			pmsg = p.pMsgPool.Get().(*pendingDiscardMsg)
			pmsg.isAllSuccessed = isSuccessed
			pmsg.count = 1
			pmsg.msg = msg
		} else {
			// update existsing pmsg
			pmsg = itf.(*pendingDiscardMsg)
			pmsg.isAllSuccessed = pmsg.isAllSuccessed && isSuccessed
			pmsg.count++
		}

		if pmsg.count == cntToDiscard {
			// msg already sent by all sender
			p.discardMsgCountMap.Delete(pmsg.msg)
			p.discardMsg(pmsg)
		} else {
			p.discardMsgCountMap.Store(pmsg.msg, pmsg)
		}
	}
}

// Run starting <n> Producer to send messages
func (p *Producer) Run(ctx context.Context) {
	utils.Logger.Info("start producer")

	go p.runMsgCollector(ctx, p.tag2NSender, p.successedChan)
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
					p.successedChan <- msg
					continue
				}

				msg.Message["msgid"] = p.DistributeKey + "-" + strconv.FormatInt(msg.Id, 10) // set id
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
							p.successedChan <- msg
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
							p.successedChan <- msg
							utils.Logger.Warn("skip sender and discard msg since of its inchan is full",
								zap.String("name", s.GetName()),
								zap.String("tag", msg.Tag))
						} else {
							p.failedChan <- msg
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
