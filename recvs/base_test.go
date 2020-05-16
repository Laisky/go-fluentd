package recvs_test

import (
	"sync"

	"github.com/Laisky/go-fluentd/libs"
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
			return &libs.FluentMsg{
				// Message: map[string]interface{}{},
				Id: -1,
			}
		},
	}
)

func init() {
	if err := libs.Logger.ChangeLevel("debug"); err != nil {
		libs.Logger.Panic("change log level", zap.Error(err))
	}
}
