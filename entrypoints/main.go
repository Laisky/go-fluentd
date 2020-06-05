package main

import (
	"context"
	"fmt"
	"time"

	concator "github.com/Laisky/go-fluentd"
	"github.com/Laisky/go-fluentd/libs"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/spf13/pflag"
)

func main() {
	ctx := context.Background()
	setupArgs()
	setupGC(ctx)
	setupSettings()
	setupLogger(ctx)
	defer libs.Logger.Info("All done")

	// run
	controllor := concator.NewControllor()
	controllor.Run(ctx)
}

func setupGC(ctx context.Context) {
	if !utils.Settings.GetBool("enable-auto-gc") {
		return
	}
	opts := []utils.GcOptFunc{}
	ratio := utils.Settings.GetInt("gc-mem-ratio")
	if ratio > 0 {
		opts = append(opts, utils.WithGCMemRatio(ratio))
	}

	if err := utils.AutoGC(ctx, opts...); err != nil {
		libs.Logger.Panic("enable auto gc", zap.Error(err))
	}
}

// setupSettings setup arguments restored in viper
func setupSettings() {
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
	// switch utils.Settings.GetString("env") {
	// case "sit":
	// case "perf":
	// case "uat":
	// case "prod":
	// default:
	// 	panic(fmt.Sprintf("unknown value `--env=%v`", utils.Settings.GetString("env")))
	// }

	// mode
	if utils.Settings.GetBool("debug") {
		fmt.Println("run in debug mode")
		utils.Settings.Set("log-level", "debug")
	} else { // prod mode
		fmt.Println("run in prod mode")
	}

	// log
	if err := libs.Logger.ChangeLevel(utils.Settings.GetString("log-level")); err != nil {
		libs.Logger.Panic("change log level", zap.Error(err))
	}
	libs.Logger.Info("set log level", zap.String("level", utils.Settings.GetString("log-level")))

	// clock
	utils.SetupClock(100 * time.Millisecond)

	// load configuration
	isCfgLoaded := false
	cfgFilePath := utils.Settings.GetString("config")
	if err := utils.Settings.SetupFromFile(cfgFilePath); err != nil {
		libs.Logger.Info("can not load config from disk",
			zap.String("config", cfgFilePath))
	} else {
		libs.Logger.Info("success load configuration from dir",
			zap.String("config", cfgFilePath))
		isCfgLoaded = true
	}

	if utils.Settings.GetString("config-server") != "" &&
		utils.Settings.GetString("config-server-appname") != "" &&
		utils.Settings.GetString("config-server-profile") != "" &&
		utils.Settings.GetString("config-server-label") != "" &&
		utils.Settings.GetString("config-server-key") != "" {
		if err := utils.Settings.SetupFromConfigServerWithRawYaml(
			utils.Settings.GetString("config-server"),
			utils.Settings.GetString("config-server-appname"),
			utils.Settings.GetString("config-server-profile"),
			utils.Settings.GetString("config-server-label"),
			utils.Settings.GetString("config-server-key"),
		); err != nil {
			libs.Logger.Panic("try to load configuration from config-server got error", zap.Error(err))
		} else {
			libs.Logger.Info("success load configuration from config-server")
			isCfgLoaded = true
		}
	}

	if !isCfgLoaded {
		libs.Logger.Panic("can not load any configuration")
	}
}

func setupArgs() {
	pflag.Bool("debug", false, "run in debug mode")
	pflag.Bool("dry", false, "run in dry mode")
	pflag.Bool("enable-auto-gc", false, "enable auto gc")
	pflag.Int("gc-mem-ratio", 85, "trigger gc when memory usage")
	pflag.String("host", "unknown", "hostname")
	pflag.StringP("config", "c", "/etc/go-fluentd/settings/settings.yml", "config file path")
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
		libs.Logger.Panic("parse command arguments", zap.Error(err))
	}
}

func setupLogger(ctx context.Context) {
	if !utils.Settings.GetBool("log-alert") {
		return
	}
	libs.Logger.Info("enable alert pusher")
	libs.Logger = libs.Logger.Named("go-fluentd-" + utils.Settings.GetString("env") + "-" + utils.Settings.GetString("host"))

	if utils.Settings.GetString("settings.logger.push_api") != "" {
		// telegram alert
		alertPusher, err := utils.NewAlertPusherWithAlertType(
			ctx,
			utils.Settings.GetString("settings.logger.push_api"),
			utils.Settings.GetString("settings.logger.alert_type"),
			utils.Settings.GetString("settings.logger.push_token"),
		)
		if err != nil {
			libs.Logger.Panic("create AlertPusher", zap.Error(err))
		}
		libs.Logger = libs.Logger.
			WithOptions(zap.HooksWithFields(alertPusher.GetZapHook()))
	}

	if utils.Settings.GetString("settings.pateo_alert.push_api") != "" {
		// pateo wechat alert pusher
		pateoAlertPusher, err := utils.NewPateoAlertPusher(
			ctx,
			utils.Settings.GetString("settings.pateo_alert.push_api"),
			utils.Settings.GetString("settings.pateo_alert.token"),
		)
		if err != nil {
			libs.Logger.Panic("create PateoAlertPusher", zap.Error(err))
		}
		libs.Logger = libs.Logger.WithOptions(zap.HooksWithFields(pateoAlertPusher.GetZapHook()))
	}
}
