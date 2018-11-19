package concator

import (
	"fmt"
	"sync"

	"github.com/Laisky/go-concator/libs"
	utils "github.com/Laisky/go-utils"
	"go.uber.org/zap"
)

type AcceptorCfg struct {
	MsgPool     *sync.Pool
	Journal     *Journal
	OutChanSize int
	MaxRotateId int64
}

// Acceptor listening tcp connection, and decode messages
type Acceptor struct {
	*AcceptorCfg
	msgChan chan *libs.FluentMsg
	recvs   []libs.AcceptorRecvItf
}

// NewAcceptor create new Acceptor
func NewAcceptor(cfg *AcceptorCfg, recvs ...libs.AcceptorRecvItf) *Acceptor {
	utils.Logger.Info("create Acceptor")
	return &Acceptor{
		AcceptorCfg: cfg,
		msgChan:     make(chan *libs.FluentMsg, cfg.OutChanSize),
		recvs:       recvs,
	}
}

// Run starting acceptor to listening and receive messages,
// you can use `acceptor.MessageChan()` to load messages`
func (a *Acceptor) Run() {
	// got exists max id from legacy
	utils.Logger.Info("process legacy data...")
	maxId, err := a.Journal.LoadMaxId()
	if err != nil {
		panic(fmt.Errorf("try to process legacy messages got error: %+v", err))
	}
	couter, err := utils.NewRotateCounterFromN(maxId+1, a.MaxRotateId)
	if err != nil {
		panic(fmt.Errorf("try to create counter got error: %+v", err))
	}

	for _, recv := range a.recvs {
		utils.Logger.Info("enable recv", zap.String("name", recv.GetName()))
		recv.Setup(a.MsgPool, a.msgChan, couter)
		go recv.Run()
	}
}

// MessageChan return the message chan that received by acceptor
func (a *Acceptor) MessageChan() chan *libs.FluentMsg {
	return a.msgChan
}
