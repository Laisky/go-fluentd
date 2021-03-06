package controller

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"gofluentd/internal/monitor"
	"gofluentd/internal/senders"
	"gofluentd/library"
	"gofluentd/library/log"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
)

// senderCache cache all sender instance in `p.sender2senderCache`
type senderCache struct {
	inchan chan<- *library.FluentMsg
	sender senders.SenderItf
}

type ProducerCfg struct {
	DistributeKey          string
	InChan                 chan *library.FluentMsg
	MsgPool                *sync.Pool
	CommitChan             chan<- *library.FluentMsg
	NFork, DiscardChanSize int
}

// Producer send messages to downstream
type Producer struct {
	*ProducerCfg
	sync.Mutex
	senders                   []senders.SenderItf
	successedChan, failedChan chan *library.FluentMsg
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

	tag2SenderCaches   *sync.Map // map[tag][]*senderCache
	sender2senderCache *sync.Map // map[senderItf]*senderCache
	unSupportedTags    *sync.Map
	tag2NSender        *sync.Map // map[tag]number of sender
	tag2Cancel         *sync.Map // map[tag]senderCancel
}

type pendingDiscardMsg struct {
	count          int
	isAllSuccessed bool
	msg            *library.FluentMsg
}

// NewProducer create new producer
func NewProducer(cfg *ProducerCfg, senders ...senders.SenderItf) (*Producer, error) {
	p := &Producer{
		ProducerCfg: cfg,
		senders:     senders,
		counter:     utils.NewCounter(),
		pMsgPool: &sync.Pool{
			New: func() interface{} {
				return &pendingDiscardMsg{}
			},
		},

		tag2SenderCaches:   &sync.Map{},
		sender2senderCache: &sync.Map{},
		discardMsgCountMap: &sync.Map{},
		unSupportedTags:    &sync.Map{},
		tag2NSender:        &sync.Map{}, // map[tag]nSender
		tag2Cancel:         &sync.Map{}, // map[tag]nSender
	}
	if err := p.valid(); err != nil {
		return nil, errors.Wrap(err, "producer config invalid")
	}

	p.successedChan = make(chan *library.FluentMsg, cfg.DiscardChanSize)
	p.failedChan = make(chan *library.FluentMsg, cfg.DiscardChanSize)
	p.registerMonitor()

	for _, s := range senders {
		log.Logger.Info("enable sender", zap.String("name", s.GetName()))
		s.SetCommitChan(cfg.CommitChan)
		s.SetMsgPool(cfg.MsgPool)
		s.SetSuccessedChan(p.successedChan)
		s.SetFailedChan(p.failedChan)
	}

	log.Logger.Info("new producer",
		zap.Int("nfork", p.NFork),
		zap.Int("discard_chan_size", p.DiscardChanSize),
	)
	return p, nil
}

func (p *Producer) valid() error {
	if p.NFork <= 0 {
		p.NFork = 4
		log.Logger.Info("reset nfork", zap.Int("nfork", 1))
	}

	if p.DiscardChanSize <= 0 {
		p.DiscardChanSize = 10000
		log.Logger.Info("reset discard_chan_size", zap.Int("discard_chan_size", 10000))
	}

	return nil
}

// registerMonitor bind monitor for producer
func (p *Producer) registerMonitor() {
	monitor.AddMetric("producer", func() map[string]interface{} {
		metrics := map[string]interface{}{
			"config": map[string]interface{}{
				"nfork":             p.NFork,
				"discard_chan_size": p.DiscardChanSize,
			},
			"msgPerSec": p.counter.GetSpeed(),
			"msgTotal":  p.counter.Get(),
		}

		p.sender2senderCache.Range(func(si, sci interface{}) bool {
			metrics[si.(senders.SenderItf).GetName()+".ChanLen"] = len(sci.(*senderCache).inchan)
			metrics[si.(senders.SenderItf).GetName()+".ChanCap"] = cap(sci.(*senderCache).inchan)
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

func (p *Producer) runMsgCollector(ctx context.Context, tag2NSender *sync.Map, successedChan chan *library.FluentMsg) {
	var (
		cntToDiscard    int
		ok, isSuccessed bool
		itf             interface{}
		msg             *library.FluentMsg
		pmsg            *pendingDiscardMsg
	)
	defer log.Logger.Info("msg collector exit", zap.String("msg", fmt.Sprint(msg)))

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok = <-p.successedChan:
			if !ok {
				log.Logger.Info("successedChan closed")
				return
			}
			isSuccessed = true
		case msg, ok = <-p.failedChan:
			if !ok {
				log.Logger.Info("failedChan closed")
				return
			}
			isSuccessed = false
		}

		// ⚠️Warning: Do not change tag in any sender
		if itf, ok = tag2NSender.Load(msg.Tag); !ok {
			log.Logger.Panic("[panic] tag2NSender should contains tag",
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
	log.Logger.Info("start producer")

	go p.runMsgCollector(ctx, p.tag2NSender, p.successedChan)
	for i := 0; i < p.NFork; i++ {
		go func(i int) {
			var (
				ok                 bool
				s                  senders.SenderItf
				itf                interface{}
				acceptSenderCaches []*senderCache
				sc                 *senderCache
				msg                *library.FluentMsg
			)
			defer log.Logger.Info("producer exit", zap.Int("i", i), zap.String("msg", fmt.Sprint(msg)))
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok = <-p.InChan:
					if !ok {
						log.Logger.Info("InChan closed")
						return
					}
				}

				// log.Logger.Info(fmt.Sprintf("send msg %p", msg))
				p.counter.Count()
				if _, ok = p.unSupportedTags.Load(msg.Tag); ok {
					log.Logger.Warn("do not produce since of unsupported tag", zap.String("tag", msg.Tag))
					p.successedChan <- msg
					continue
				}

				msg.Message["msgid"] = p.DistributeKey + "-" + strconv.FormatInt(msg.ID, 10) // set id
				if itf, ok = p.tag2SenderCaches.Load(msg.Tag); !ok {
					p.Lock()
					if itf, ok = p.tag2SenderCaches.Load(msg.Tag); !ok { // double check
						acceptSenderCaches = []*senderCache{}
						// create sender chans for new tag
						ctx2Tag, cancel := context.WithCancel(ctx)
						for _, s = range p.senders {
							if s.IsTagSupported(msg.Tag) {
								if itf, ok = p.sender2senderCache.Load(s); !ok {
									log.Logger.Info("spawn new producer sender",
										zap.String("name", s.GetName()),
										zap.String("tag", msg.Tag))
									sc = &senderCache{
										inchan: s.Spawn(ctx2Tag),
										sender: s,
									}
									p.sender2senderCache.Store(s, sc)
								} else {
									sc = itf.(*senderCache)
								}
								acceptSenderCaches = append(acceptSenderCaches, sc)
							}
						}

						if len(acceptSenderCaches) == 0 {
							// no sender support this tag
							log.Logger.Warn("do not produce since of unsupported tag", zap.String("tag", msg.Tag))
							p.tag2NSender.Store(msg.Tag, 1)
							p.unSupportedTags.Store(msg.Tag, struct{}{}) // mark as unsupported
							p.successedChan <- msg
							cancel()
							p.Unlock()
							continue
						}

						log.Logger.Info("register the number of senders for tag",
							zap.String("tag", msg.Tag),
							zap.Int("n", len(acceptSenderCaches)))
						p.tag2Cancel.Store(msg.Tag, cancel)
						p.tag2NSender.Store(msg.Tag, len(acceptSenderCaches))
						// tag2SenderCaches must put at last
						p.tag2SenderCaches.Store(msg.Tag, acceptSenderCaches)
					} else {
						acceptSenderCaches = itf.([]*senderCache)
					}
					p.Unlock()
				} else {
					acceptSenderCaches = itf.([]*senderCache)
				}

				// put msg into every sender's chan
				for _, sc = range acceptSenderCaches {
					select {
					case sc.inchan <- msg:
					default:
						if sc.sender.DiscardWhenBlocked() {
							p.successedChan <- msg
							log.Logger.Warn("skip sender and discard msg since of its inchan is full",
								zap.String("name", s.GetName()),
								zap.String("tag", msg.Tag))
						} else {
							p.failedChan <- msg
							// p.Debug("skip sender and not discard msg since of its inchan is full",
							// 	zap.String("name", s.GetName()),
							// 	zap.String("tag", msg.Tag))
						}
					}
				}
			}
		}(i)
	}
}
