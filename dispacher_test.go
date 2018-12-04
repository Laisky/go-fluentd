package concator_test

import (
	"testing"

	utils "github.com/Laisky/go-utils"
	concator "github.com/Laisky/go-fluentd"
)

func TestRefreshConfig(t *testing.T) {
	configs := concator.LoadDispatcherConfig()
	if configs["test"].MsgKey != "log" {
		t.Errorf("expect `settings.tagConfigs.test.key: log`")
	}
}

func init() {
	utils.Settings.Setup("/Users/laisky/repo/pateo/configs/go-fluentd")
}
