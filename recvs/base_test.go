package recvs_test

import (
	"sync"

	"gofluentd/library"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

var (
	// httpClient = &http.Client{ // default http client
	// 	Transport: &http.Transport{
	// 		MaxIdleConnsPerHost: 20,
	// 	},
	// 	Timeout: 30 * time.Second,
	// }
	counter = utils.NewCounter()
	msgPool = &sync.Pool{
		New: func() interface{} {
			return &library.FluentMsg{
				// Message: map[string]interface{}{},
				ID: -1,
			}
		},
	}
)

func init() {
	if err := library.Logger.ChangeLevel("debug"); err != nil {
		library.Logger.Panic("change log level", zap.Error(err))
	}
}
