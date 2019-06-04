package concator

import (
	"net/http"

	"github.com/Depado/ginprom"

	"github.com/gin-contrib/pprof"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/gin-gonic/gin"
)

var (
	server       = gin.New()
	closeEvtChan = make(chan struct{})
)

// RunServer starting http server
func RunServer(addr string) {
	if !utils.Settings.GetBool("debug") {
		gin.SetMode(gin.ReleaseMode)
	}

	server.Any("/health", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "hello, world")
	})

	// supported action:
	// cmdline, profile, symbol, goroutine, heap, threadcreate, block
	pprof.Register(server, "pprof")
	p := ginprom.New(
		ginprom.Engine(server),
		ginprom.Subsystem("gin"),
		ginprom.Path("/metrics"),
	)
	server.Use(p.Instrument())

	utils.Logger.Info("listening on http", zap.String("addr", addr))
	utils.Logger.Panic("server exit", zap.Error(server.Run(addr)))
}
