package recvs

import (
	"sync"

	"gofluentd/library"
	"gofluentd/library/log"

	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

var (
	// httpClient = &http.Client{ // default http client
	// 	Transport: &http.Transport{
	// 		MaxIdleConnsPerHost: 20,
	// 	},
	// 	Timeout: 30 * time.Second,
	// }
	counter = gutils.NewCounter()
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
	if err := log.Logger.ChangeLevel("debug"); err != nil {
		log.Logger.Panic("change log level", zap.Error(err))
	}
}
