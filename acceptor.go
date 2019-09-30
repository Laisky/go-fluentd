package concator

import (
	"context"
	"fmt"
	"sync"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-fluentd/recvs"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

// AcceptorCfg is the configuation of Acceptor
type AcceptorCfg struct {
	MsgPool                           *sync.Pool
	Journal                           *Journal
	AsyncOutChanSize, SyncOutChanSize int
	MaxRotateID                       int64
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

	if cfg.MaxRotateID < 100 {
		utils.Logger.Error("MaxRotateID should not too small", zap.Int64("rotate", cfg.MaxRotateID))
	} else if cfg.MaxRotateID < 1000000 {
		utils.Logger.Warn("MaxRotateID should not too small", zap.Int64("rotate", cfg.MaxRotateID))
	}

	return &Acceptor{
		AcceptorCfg:  cfg,
		syncOutChan:  make(chan *libs.FluentMsg, cfg.SyncOutChanSize),
		asyncOutChan: make(chan *libs.FluentMsg, cfg.AsyncOutChanSize),
		recvs:        recvs,
	}
}

// Run starting acceptor to listening and receive messages,
// you can use `acceptor.MessageChan()` to load messages`
func (a *Acceptor) Run(ctx context.Context) {
	// got exists max id from legacy
	utils.Logger.Info("process legacy data...")
	maxID, err := a.Journal.LoadMaxID()
	if err != nil {
		utils.Logger.Panic("try to process legacy messages got error", zap.Error(err))
	}

	couter, err := utils.NewParallelCounterFromN((maxID+1)%a.MaxRotateID, 10000, a.MaxRotateID)
	if err != nil {
		panic(fmt.Errorf("try to create counter got error: %+v", err))
	}

	for _, recv := range a.recvs {
		utils.Logger.Info("enable recv", zap.String("name", recv.GetName()))
		recv.SetAsyncOutChan(a.asyncOutChan)
		recv.SetSyncOutChan(a.syncOutChan)
		recv.SetMsgPool(a.MsgPool)
		recv.SetCounter(couter.GetChild())
		go recv.Run(ctx)
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
