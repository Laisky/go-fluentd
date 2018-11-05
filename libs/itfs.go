package libs

import (
	"sync"
)

type CounterIft interface {
	Count() int64
	CountN(int64) int64
}

type AcceptorRecvItf interface {
	Setup(*sync.Pool, chan *FluentMsg, CounterIft)
	Run()
	GetName() string
}
