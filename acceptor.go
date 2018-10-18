package concator

import (
	"fmt"
	"sync"

	"github.com/Laisky/go-concator/libs"
	utils "github.com/Laisky/go-utils"
	"go.uber.org/zap"
)

// Acceptor listening tcp connection, and decode messages
type Acceptor struct {
	addr    string
	msgChan chan *libs.FluentMsg
	msgPool *sync.Pool
	journal *Journal
	recvs   []libs.AcceptorRecvItf
}

// NewAcceptor create new Acceptor
func NewAcceptor(msgpool *sync.Pool, journal *Journal, recvs ...libs.AcceptorRecvItf) *Acceptor {
	utils.Logger.Info("create Acceptor")
	return &Acceptor{
		msgChan: make(chan *libs.FluentMsg, 5000),
		msgPool: msgpool,
		journal: journal,
		recvs:   recvs,
	}
}

// Run starting acceptor to listening and receive messages,
// you can use `acceptor.MessageChan()` to load messages`
func (a *Acceptor) Run() (err error) {
	var (
		maxIDToRotate int64 = 100000000
	)

	// got exists max id from legacy
	utils.Logger.Info("process legacy data...")
	maxId, err := a.journal.LoadMaxId()
	if err != nil {
		panic(fmt.Errorf("try to process legacy messages got error: %+v", err))
	}
	couter, err := utils.NewRotateCounterFromN(maxId+1, maxIDToRotate)
	if err != nil {
		panic(fmt.Errorf("try to create counter got error: %+v", err))
	}

	for _, recv := range a.recvs {
		utils.Logger.Info("enable recv", zap.String("name", utils.GetFuncName(recv.Setup)))
		recv.Setup(a.msgPool, a.msgChan, couter)
		go recv.Run()
	}

	return nil
}

// MessageChan return the message chan that received by acceptor
func (a *Acceptor) MessageChan() <-chan *libs.FluentMsg {
	return a.msgChan
}
