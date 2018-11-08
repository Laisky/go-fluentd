package tagFilters

import (
	"sync"

	"github.com/Laisky/go-concator/libs"
)

type TagFilterFactoryItf interface {
	IsTagSupported(string) bool
	Spawn(string, chan<- *libs.FluentMsg) chan<- *libs.FluentMsg // Spawn(tag, outChan) inChan
	GetName() string
}

type BaseTagFilterFactory struct {
	MsgPool *sync.Pool
}
