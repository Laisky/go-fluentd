package monitor

import (
	"encoding/json"
	"time"

	"github.com/Laisky/go-utils"
	"github.com/kataras/iris"
	"go.uber.org/zap"
)

var metricGetter = map[string]func() map[string]interface{}{}

func AddMetric(name string, metric func() map[string]interface{}) {
	metricGetter[name] = metric
}

func BindHTTP(srv *iris.Application) {
	var (
		b   []byte
		err error
	)
	srv.Get("/monitor", func(ctx iris.Context) {
		metrics := map[string]interface{}{
			"ts": time.Now().Format(time.RFC3339),
		}
		for k, getter := range metricGetter {
			metrics[k] = getter()
		}
		if b, err = json.Marshal(&metrics); err != nil {
			utils.Logger.Error("try to marshal metrics to json got error", zap.Error(err))
			return
		}

		ctx.Write(b)
	})
}
