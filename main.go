package main

import (
	"gofluentd/cmd"
	"gofluentd/library/log"
	"os"

	"github.com/Laisky/zap"
)

func main() {
	if err := cmd.Execute(); err != nil {
		log.Logger.Error("start", zap.Error(err))
		os.Exit(1)
	}
}
