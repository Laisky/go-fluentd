package log_test

import (
	"bytes"
	"encoding/json"
	"testing"

	collectorlog "gofluentd/library/log"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
)

// Keep the exported concrete type available to existing callers.
var _ *utils.LoggerType = collectorlog.Logger

func TestDefaultLoggerConcreteContract(t *testing.T) {
	logger := collectorlog.Logger
	if logger == nil || logger.Logger == nil {
		t.Fatal("default concrete logger was not initialized")
	}
	var embedded *zap.Logger = logger.Logger
	if embedded.Name() != "gofluentd" || logger.Level() != zapcore.DebugLevel {
		t.Fatal("default logger identity or level changed")
	}
	var output bytes.Buffer
	child := logger.Named("compatibility").WithOptions(zap.WrapCore(func(zapcore.Core) zapcore.Core {
		return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zapcore.DebugLevel)
	}))
	child.Debug("synthetic diagnostic", zap.String("context", "kept"), zap.Int64("sequence", 9007199254740993))
	if err := child.Sync(); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	decoder.UseNumber()
	var record map[string]interface{}
	if err := decoder.Decode(&record); err != nil {
		t.Fatal(err)
	}
	if record["logger"] != "gofluentd.compatibility" || record["level"] != "debug" || record["msg"] != "synthetic diagnostic" || record["context"] != "kept" || record["sequence"] != json.Number("9007199254740993") {
		t.Fatal("concrete child logger lost its chosen diagnostic", record)
	}
	if collectorlog.Logger != logger || logger.Name() != "gofluentd" {
		t.Fatal("child logger mutated the default logger")
	}
}
