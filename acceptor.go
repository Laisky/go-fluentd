package concator

import (
	"fmt"
	"sync"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-fluentd/recvs"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

type AcceptorCfg struct {
	MsgPool                           *sync.Pool
	Journal                           *Journal
	AsyncOutChanSize, SyncOutChanSize int
	MaxRotateId                       int64
}

// Acceptor listening tcp connection, and decode messages
type Acceptor struct {
	*AcceptorCfg
	syncOutChan, asyncOutChan chan *libs.FluentMsg
	recvs                     []recvs.AcceptorRecvItf
}

// NewAcceptor create new Acceptor
func NewAcceptor(cfg *AcceptorCfg, recvs ...recvs.AcceptorRecvItf) *Acceptor {
	utils.Logger.Info("create Acceptor")
	return &Acceptor{
		AcceptorCfg:  cfg,
		syncOutChan:  make(chan *libs.FluentMsg, cfg.SyncOutChanSize),
		asyncOutChan: make(chan *libs.FluentMsg, cfg.AsyncOutChanSize),
		recvs:        recvs,
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
		recv.SetAsyncOutChan(a.asyncOutChan)
		recv.SetSyncOutChan(a.syncOutChan)
		recv.SetMsgPool(a.MsgPool)
		recv.SetCounter(couter)
		go recv.Run()
	}
}

// GetSyncOutChan return the message chan that received by acceptor
func (a *Acceptor) GetSyncOutChan() chan *libs.FluentMsg {
	return a.syncOutChan
}

// GetAsyncOutChan return the message chan that received by blockable acceptor
func (a *Acceptor) GetAsyncOutChan() chan *libs.FluentMsg {
	return a.asyncOutChan
}
