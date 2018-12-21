package concator

import (
	"os"
	"time"

	utils "github.com/Laisky/go-utils"
	"github.com/kataras/iris"
	"github.com/kataras/iris/middleware/pprof"
	"go.uber.org/zap"
)

var (
	Server       = iris.New()
	closeEvtChan = make(chan struct{})
)

func RunServer(addr string) {
	Server.Any("/health", func(ctx iris.Context) {
		ctx.Write([]byte("Hello, World"))
	})

	go func() {
		defer time.Sleep(1 * time.Second)
		<-closeEvtChan
		os.Exit(0)
	}()

	Server.Any("/admin/pprof/{action:path}", pprof.New())
	if utils.Settings.GetBool("pprof") {
		Server.Post("/admin/shutdown", func(ctx iris.Context) {
			go func() {
				time.Sleep(1 * time.Second)
				closeEvtChan <- struct{}{}
			}()
			ctx.WriteString("shutdown now...")
		})
	}

	utils.Logger.Info("listening on http", zap.String("addr", addr))
	Server.Run(iris.Addr(addr))
}
