package recvs

import (
	"sync"

	"github.com/Laisky/go-concator/libs"
)

type BaseRecv struct {
	outChan chan *libs.FluentMsg
	msgPool *sync.Pool
	counter libs.CounterIft
}

func (r *BaseRecv) Setup(msgPool *sync.Pool, outChan chan *libs.FluentMsg, counter libs.CounterIft) {
	r.msgPool = msgPool
	r.outChan = outChan
	r.counter = counter
}
