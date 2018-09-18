package main

import (
	"fmt"
	"runtime"
	"time"

	utils "github.com/Laisky/go-utils"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	concator "pateo.com/go-concator"
)

// SetupSettings setup arguments restored in viper
func SetupSettings() {
	utils.Settings.Setup(utils.Settings.GetString("config"))

	if utils.Settings.GetBool("debug") { // debug mode
		fmt.Println("run in debug mode")
		utils.SetupLogger("debug")
	} else { // prod mode
		fmt.Println("run in prod mode")
		utils.SetupLogger("info")
	}
}

func SetupArgs() {
	pflag.Bool("debug", false, "run in debug mode")
	pflag.Bool("dry", false, "run in dry mode")
	pflag.String("config", "/etc/go-ramjet/settings", "config file directory path")
	pflag.Parse()
	viper.BindPFlags(pflag.CommandLine)
}

func HeartBeat() {
	go func() {
		for {
			utils.Logger.Info("heartbeat", zap.Int("goroutine", runtime.NumGoroutine()))
			time.Sleep(60 * time.Second)
		}
	}()
}

func main() {
	defer utils.Logger.Sync()
	SetupArgs()
	SetupSettings()
	HeartBeat()
	defer utils.Logger.Info("All done")

	controllor := concator.NewControllor()
	controllor.Run()
}
