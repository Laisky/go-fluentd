package recvs_test

import (
	"net/http"
	"sync"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	utils "github.com/Laisky/go-utils"
)

var (
	httpClient = &http.Client{ // default http client
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 20,
		},
		Timeout: 30 * time.Second,
	}
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
	utils.SetupLogger("debug")
}
