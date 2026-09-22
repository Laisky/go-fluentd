package controller

import (
	"context"
	"errors"
	"net"
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

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Logger.Error("listen on HTTP", zap.Error(err))
		return
	}
	log.Logger.Info("listening on http", zap.String("addr", ln.Addr().String()))
	if err := serveHTTP(ctx, &httpSrv, ln); err != nil {
		log.Logger.Error("HTTP server stopped", zap.Error(err))
	}
}

func serveHTTP(ctx context.Context, srv *http.Server, ln net.Listener) error {
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		// The parent is already cancelled. Deriving from it would cancel the
		// grace period immediately and interrupt otherwise healthy requests.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultGraceShutdownWait)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			srv.Close()
			<-done
			return err
		}
		err := <-done
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
