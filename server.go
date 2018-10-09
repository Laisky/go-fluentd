package concator

import (
	utils "github.com/Laisky/go-utils"
	"github.com/kataras/iris"
	"github.com/kataras/iris/middleware/pprof"
	"go.uber.org/zap"
)

var (
	Server = iris.New()
)

func RunServer(addr string) {
	Server.Get("/", func(ctx iris.Context) {
		ctx.Write([]byte("Hello, World"))
	})

	if utils.Settings.GetBool("pprof") {
		Server.Any("/debug/pprof/{action:path}", pprof.New())
	}

	utils.Logger.Info("listening on http", zap.String("addr", addr))
	Server.Run(iris.Addr(addr))
}
