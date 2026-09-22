package controller

import (
	"context"
	"errors"
	"net/http"
	"time"

	"gofluentd/library/log"

	middlewares "github.com/Laisky/gin-middlewares"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
)

var (
	server                   = gin.New()
	defaultGraceShutdownWait = 3 * time.Second
)

// RunServer starting http server
func RunServer(ctx context.Context, addr string) {
	if !utils.Settings.GetBool("debug") {
		gin.SetMode(gin.ReleaseMode)
	}

	httpSrv := http.Server{
		Addr:    addr,
		Handler: server,
	}

	server.Use(gin.Recovery())
	server.Any("/health", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "hello, world")
	})

	// supported action:
	// cmdline, profile, symbol, goroutine, heap, threadcreate, block
	pprof.Register(server, "pprof")
	middlewares.BindPrometheus(server)

	log.Logger.Info("listening on http", zap.String("addr", addr))
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.ListenAndServe() }()
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Logger.Error("HTTP server stopped", zap.Error(err))
		}
		return
	case <-ctx.Done():
	}
	// The parent has already been canceled. Draining active requests requires
	// an independent bounded context, not a child of the canceled parent.
	srvCtx, cancel := context.WithTimeout(context.Background(), defaultGraceShutdownWait)
	defer cancel()
	if err := httpSrv.Shutdown(srvCtx); err != nil {
		log.Logger.Error("shutdown monitor server", zap.Error(err))
		httpSrv.Close()
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Logger.Error("HTTP server stopped", zap.Error(err))
	}
}
