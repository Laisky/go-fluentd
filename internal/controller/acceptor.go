package controller

import (
	"context"
	"fmt"
	"sync"

	"gofluentd/internal/recvs"
	"gofluentd/library"
	"gofluentd/library/log"

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
	syncOutChan, asyncOutChan chan *library.FluentMsg
	recvs                     []recvs.AcceptorRecvItf
}

// NewAcceptor create new Acceptor
func NewAcceptor(cfg *AcceptorCfg, recvs ...recvs.AcceptorRecvItf) *Acceptor {
	a := &Acceptor{
		AcceptorCfg:  cfg,
		syncOutChan:  make(chan *library.FluentMsg, cfg.SyncOutChanSize),
		asyncOutChan: make(chan *library.FluentMsg, cfg.AsyncOutChanSize),
		recvs:        recvs,
	}
	if err := a.valid(); err != nil {
		log.Logger.Panic("new acceptor", zap.Error(err))
	}

	log.Logger.Info("create acceptor",
		zap.Int64("max_rotate_id", a.MaxRotateID),
		zap.Int("sync_out_chan_size", a.SyncOutChanSize),
		zap.Int("async_out_chan_size", a.AsyncOutChanSize),
	)
	return a
}

func (a *Acceptor) valid() error {
	if a.MaxRotateID == 0 {
		a.MaxRotateID = 372036854775807
		log.Logger.Info("reset max_rotate_id", zap.Int64("max_rotate_id", a.MaxRotateID))
	} else if a.MaxRotateID < 1000000 {
		log.Logger.Warn("max_rotate_id should not too small", zap.Int64("max_rotate_id", a.MaxRotateID))
	}

	if a.SyncOutChanSize == 0 {
		a.SyncOutChanSize = 10000
		log.Logger.Info("reset sync_out_chan_size", zap.Int("sync_out_chan_size", a.SyncOutChanSize))
	}

	if a.AsyncOutChanSize == 0 {
		a.AsyncOutChanSize = 10000
		log.Logger.Info("reset async_out_chan_size", zap.Int("async_out_chan_size", a.AsyncOutChanSize))
	}

	return nil
}

// Run starting acceptor to listening and receive messages,
// you can use `acceptor.MessageChan()` to load messages`
func (a *Acceptor) Run(ctx context.Context) {
	// got exists max id from legacy
	log.Logger.Info("process legacy data...")
	maxID, err := a.Journal.LoadMaxID()
	if err != nil {
		log.Logger.Panic("try to process legacy messages got error", zap.Error(err))
	}

	couter, err := utils.NewParallelCounterFromN((maxID+1)%a.MaxRotateID, 10000, a.MaxRotateID)
	if err != nil {
		panic(fmt.Errorf("try to create counter got error: %+v", err))
	}

	for _, recv := range a.recvs {
		log.Logger.Info("enable recv", zap.String("name", recv.GetName()))
		recv.SetAsyncOutChan(a.asyncOutChan)
		recv.SetSyncOutChan(a.syncOutChan)
		recv.SetMsgPool(a.MsgPool)
		recv.SetCounter(couter.GetChild())
		go recv.Run(ctx)
	}
}

// GetSyncOutChan return the message chan that received by acceptor
func (a *Acceptor) GetSyncOutChan() chan *library.FluentMsg {
	return a.syncOutChan
}

// GetAsyncOutChan return the message chan that received by blockable acceptor
func (a *Acceptor) GetAsyncOutChan() chan *library.FluentMsg {
	return a.asyncOutChan
}
