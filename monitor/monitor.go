package monitor

import (
	"encoding/json"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/kataras/iris"
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
			"ts": utils.Clock.GetTimeInRFC3339Nano(),
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
