package main

import (
	"fmt"

	concator "github.com/Laisky/go-concator"
	utils "github.com/Laisky/go-utils"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// SetupSettings setup arguments restored in viper
func SetupSettings() {
	if err := utils.Settings.Setup(utils.Settings.GetString("config")); err != nil {
		panic(err)
	}

	// mode
	if utils.Settings.GetBool("debug") {
		fmt.Println("run in debug mode")
		utils.SetupLogger("debug")
	} else { // prod mode
		fmt.Println("run in prod mode")
		utils.SetupLogger("info")
	}

	// env
	if utils.Settings.GetString("env") == "nil" {
		panic(fmt.Errorf("must set `--env`"))
	}
}

func SetupArgs() {
	pflag.Bool("debug", false, "run in debug mode")
	pflag.Bool("dry", false, "run in dry mode")
	pflag.String("config", "/etc/go-ramjet/settings", "config file directory path")
	pflag.String("addr", "0.0.0.0:8080", "like `0.0.0.0:8080`")
	pflag.String("env", "nil", "environment `sit/perf/uat/prod`")
	pflag.Int("heartbeat", 60, "heartbeat seconds")
	pflag.Parse()
	viper.BindPFlags(pflag.CommandLine)
}

func main() {
	defer utils.Logger.Sync()
	SetupArgs()
	SetupSettings()
	defer utils.Logger.Info("All done")

	controllor := concator.NewControllor()
	controllor.Run()
	concator.RunServer(utils.Settings.GetString("addr"))
}
