package log

import (
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
)

// Logger is the collector's concrete structured logger.
var Logger *zap.Logger
var loggerLevel = zap.NewAtomicLevelAt(zapcore.DebugLevel)

func init() {
	config := zap.NewProductionConfig()
	config.Encoding = "console"
	config.Level = loggerLevel
	logger, err := config.Build()
	if err != nil {
		panic("initialize collector logger: " + err.Error())
	}
	Logger = logger.Named("gofluentd")
}

// SetLevel changes the level shared by the collector and its child loggers.
func SetLevel(level string) error {
	var parsed zapcore.Level
	if err := parsed.UnmarshalText([]byte(level)); err != nil {
		return err
	}
	loggerLevel.SetLevel(parsed)
	return nil
}
