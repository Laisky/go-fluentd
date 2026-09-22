package monitor

import (
	"net/http"
	"sync"

	"gofluentd/library/log"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/gin-gonic/gin"
	jsoniter "github.com/json-iterator/go"
)

var (
	json         = jsoniter.ConfigCompatibleWithStandardLibrary
	metricMu     sync.RWMutex
	metricGetter = map[string]func() map[string]interface{}{}
)

func AddMetric(name string, metric func() map[string]interface{}) {
	metricMu.Lock()
	defer metricMu.Unlock()
	metricGetter[name] = metric
}

func BindHTTP(srv *gin.Engine) {
	srv.GET("/monitor", func(ctx *gin.Context) {
		// Snapshot callbacks under the lock, but execute them outside it. A getter
		// may itself register a metric, or take locks owned by another component.
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
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": "cannot encode metrics"})
			return
		}
		ctx.Data(http.StatusOK, "application/json; charset=utf-8", b)
	})
}
