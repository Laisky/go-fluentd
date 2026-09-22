package monitor

import (
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/gin-gonic/gin"
	jsoniter "github.com/json-iterator/go"
	"gofluentd/library/log"
	"net/http"
	"sync"
)

var (
	json         = jsoniter.ConfigCompatibleWithStandardLibrary
	metricGetter = map[string]func() map[string]interface{}{}
	metricMu     sync.RWMutex
)

func AddMetric(name string, metric func() map[string]interface{}) {
	metricMu.Lock()
	defer metricMu.Unlock()
	metricGetter[name] = metric
}
func BindHTTP(srv *gin.Engine) {
	srv.GET("/monitor", func(ctx *gin.Context) {
		// Call user-supplied getters outside the registry lock: a getter can
		// register another metric or acquire a component's own locks.
		metricMu.RLock()
		getters := make(map[string]func() map[string]interface{}, len(metricGetter))
		for name, getter := range metricGetter {
			getters[name] = getter
		}
		metricMu.RUnlock()
		metrics := map[string]interface{}{"ts": utils.Clock.GetTimeInRFC3339Nano()}
		for name, getter := range getters {
			if getter != nil {
				metrics[name] = getter()
			}
		}
		b, err := json.Marshal(metrics)
		if err != nil {
			log.Logger.Error("marshal metrics", zap.Error(err))
			ctx.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		ctx.Data(http.StatusOK, "application/json; charset=utf-8", b)
	})
}
