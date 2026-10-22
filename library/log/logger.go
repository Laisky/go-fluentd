package log

import (
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

var Logger *utils.LoggerType

func init() {
	created, err := utils.NewConsoleLoggerWithName("gofluentd", "debug")
	if err != nil {
		utils.Logger.Panic("new logger", zap.Error(err))
	}
	concrete, ok := any(created).(*utils.LoggerType)
	if !ok || concrete == nil {
		panic("go-utils logger factory did not return a concrete logger")
	}
	Logger = concrete
}
