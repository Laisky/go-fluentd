package main

import (
	"context"
	"fmt"
	"time"

	concator "github.com/Laisky/go-fluentd"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/spf13/pflag"
)

// SetupSettings setup arguments restored in viper
func SetupSettings() {
	// check `--log-level`
	switch utils.Settings.GetString("log-level") {
	case "debug":
	case "info":
	case "warn":
	case "error":
	default:
		panic(fmt.Sprintf("unknown value `--log-level=%v`", utils.Settings.GetString("log-level")))
	}

	// check `--env`
	switch utils.Settings.GetString("env") {
	case "sit":
	case "perf":
	case "uat":
	case "prod":
	default:
		panic(fmt.Sprintf("unknown value `--env=%v`", utils.Settings.GetString("env")))
	}

	// mode
	if utils.Settings.GetBool("debug") {
		fmt.Println("run in debug mode")
		utils.Settings.Set("log-level", "debug")
	} else { // prod mode
		fmt.Println("run in prod mode")
	}

	// log
	if err := utils.Logger.ChangeLevel(utils.Settings.GetString("log-level")); err != nil {
		utils.Logger.Panic("change log level", zap.Error(err))
	}

	// clock
	utils.SetupClock(100 * time.Millisecond)

	// load configuration
	isCfgLoaded := false
	cfgDirPath := utils.Settings.GetString("config")
	if err := utils.Settings.Setup(cfgDirPath); err != nil {
		utils.Logger.Info("can not load config from disk",
			zap.String("dirpath", cfgDirPath))
	} else {
		utils.Logger.Info("success load configuration from dir",
			zap.String("dirpath", cfgDirPath))
		isCfgLoaded = true
	}

	if utils.Settings.GetString("config-server") != "" &&
		utils.Settings.GetString("config-server-appname") != "" &&
		utils.Settings.GetString("config-server-profile") != "" &&
		utils.Settings.GetString("config-server-label") != "" &&
		utils.Settings.GetString("config-server-key") != "" {
		cfgSrvCfg := &utils.ConfigServerCfg{
			URL:     utils.Settings.GetString("config-server"),
			Profile: utils.Settings.GetString("config-server-profile"),
			Label:   utils.Settings.GetString("config-server-label"),
			App:     utils.Settings.GetString("config-server-appname"),
		}
		if err := utils.Settings.SetupFromConfigServerWithRawYaml(cfgSrvCfg, utils.Settings.GetString("config-server-key")); err != nil {
			utils.Logger.Panic("try to load configuration from config-server got error", zap.Error(err))
		} else {
			utils.Logger.Info("success load configuration from config-server",
				zap.String("cfg", fmt.Sprint(cfgSrvCfg)))
			isCfgLoaded = true
		}
	}

	if !isCfgLoaded {
		utils.Logger.Panic("can not load any configuration")
	}
}

func SetupArgs() {
	pflag.Bool("debug", false, "run in debug mode")
	pflag.Bool("dry", false, "run in dry mode")
	pflag.String("config", "/etc/go-fluentd/settings", "config file directory path")
	pflag.String("config-server", "", "config server url")
	pflag.String("config-server-appname", "", "config server app name")
	pflag.String("config-server-profile", "", "config server profile name")
	pflag.String("config-server-label", "", "config server branch name")
	pflag.String("config-server-key", "", "raw content key ")
	pflag.String("addr", "localhost:8080", "like `localhost:8080`")
	pflag.String("env", "", "environment `sit/perf/uat/prod`")
	pflag.String("log-level", "info", "`debug/info/error`")
	pflag.Bool("log-alert", false, "is enable log AlertPusher")
	pflag.Int("heartbeat", 60, "heartbeat seconds")
	pflag.Parse()
	if err := utils.Settings.BindPFlags(pflag.CommandLine); err != nil {
		utils.Logger.Panic("parse command arguments", zap.Error(err))
	}
}

func setupLogger(ctx context.Context) {
	if !utils.Settings.GetBool("log-alert") {
		return
	}
	utils.Logger.Info("enable alert pusher")
	// log
	alertPusher, err := utils.NewAlertPusherWithAlertType(
		ctx,
		utils.Settings.GetString("settings.logger.push_api"),
		utils.Settings.GetString("settings.logger.alert_type"),
		utils.Settings.GetString("settings.logger.push_token"),
	)
	if err != nil {
		utils.Logger.Panic("create AlertPusher", zap.Error(err))
	}

	hook := utils.NewAlertHook(alertPusher)
	if _, err := utils.SetDefaultLogger(
		"go-fluentd:"+utils.Settings.GetString("env"),
		utils.Settings.GetString("log-level"),
		zap.HooksWithFields(hook.GetZapHook())); err != nil {
		utils.Logger.Panic("setup logger", zap.Error(err))
	}
}

func main() {
	ctx := context.Background()
	SetupArgs()
	SetupSettings()
	setupLogger(ctx)
	defer utils.Logger.Info("All done")

	// run
	controllor := concator.NewControllor()
	controllor.Run(ctx)
}
