package monitor

import (
	"net/http"

	"gofluentd/library"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/gin-gonic/gin"
	jsoniter "github.com/json-iterator/go"
)

var (
	json         = jsoniter.ConfigCompatibleWithStandardLibrary
	metricGetter = map[string]func() map[string]interface{}{}
)

func AddMetric(name string, metric func() map[string]interface{}) {
	metricGetter[name] = metric
}

func BindHTTP(srv *gin.Engine) {
	var (
		b   []byte
		err error
	)
	srv.GET("/monitor", func(ctx *gin.Context) {
		metrics := map[string]interface{}{
			"ts": utils.Clock.GetTimeInRFC3339Nano(),
		}
		for k, getter := range metricGetter {
			metrics[k] = getter()
		}
		if b, err = json.Marshal(&metrics); err != nil {
			library.Logger.Error("try to marshal metrics to json got error", zap.Error(err))
			return
		}

		ctx.String(http.StatusOK, string(b))
	})
}
