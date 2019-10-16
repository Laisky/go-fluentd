package recvs_test

import (
	"sync"

	"github.com/Laisky/zap"

	"github.com/Laisky/go-fluentd/libs"
	utils "github.com/Laisky/go-utils"
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
	if err := utils.Logger.ChangeLevel("debug"); err != nil {
		utils.Logger.Panic("change log level", zap.Error(err))
	}
}
