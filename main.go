package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Laisky/go-fluentd/controller"
	"github.com/Laisky/go-fluentd/libs"
	gutils "github.com/Laisky/go-utils"
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
	controllor := controller.NewControllor()
	controllor.Run(ctx)
}

func setupGC(ctx context.Context) {
	if !gutils.Settings.GetBool("enable-auto-gc") {
		return
	}
	opts := []gutils.GcOptFunc{}
	ratio := gutils.Settings.GetInt("gc-mem-ratio")
	if ratio > 0 {
		opts = append(opts, gutils.WithGCMemRatio(ratio))
	}

	if err := gutils.AutoGC(ctx, opts...); err != nil {
		libs.Logger.Panic("enable auto gc", zap.Error(err))
	}
}

// setupSettings setup arguments restored in viper
func setupSettings() {
	// check `--log-level`
	switch gutils.Settings.GetString("log-level") {
	case "debug":
	case "info":
	case "warn":
	case "error":
	default:
		panic(fmt.Sprintf("unknown value `--log-level=%v`", gutils.Settings.GetString("log-level")))
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
	if gutils.Settings.GetBool("debug") {
		fmt.Println("run in debug mode")
		gutils.Settings.Set("log-level", "debug")
	} else { // prod mode
		fmt.Println("run in prod mode")
	}

	// log
	if err := libs.Logger.ChangeLevel(gutils.Settings.GetString("log-level")); err != nil {
		libs.Logger.Panic("change log level", zap.Error(err))
	}
	libs.Logger.Info("set log level", zap.String("level", gutils.Settings.GetString("log-level")))

	// clock
	gutils.SetupClock(100 * time.Millisecond)

	// load configuration
	isCfgLoaded := false
	cfgFilePath := gutils.Settings.GetString("config")
	if err := gutils.Settings.LoadFromFile(cfgFilePath); err != nil {
		libs.Logger.Info("can not load config from disk",
			zap.String("config", cfgFilePath))
	} else {
		libs.Logger.Info("success load configuration from dir",
			zap.String("config", cfgFilePath))
		isCfgLoaded = true
	}

	if gutils.Settings.GetString("config-server") != "" &&
		gutils.Settings.GetString("config-server-appname") != "" &&
		gutils.Settings.GetString("config-server-profile") != "" &&
		gutils.Settings.GetString("config-server-label") != "" &&
		gutils.Settings.GetString("config-server-key") != "" {
		if err := gutils.Settings.LoadFromConfigServer(
			gutils.Settings.GetString("config-server"),
			gutils.Settings.GetString("config-server-appname"),
			gutils.Settings.GetString("config-server-profile"),
			gutils.Settings.GetString("config-server-label"),
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
	if err := gutils.Settings.BindPFlags(pflag.CommandLine); err != nil {
		libs.Logger.Panic("parse command arguments", zap.Error(err))
	}
}

func setupLogger(ctx context.Context) {
	if !gutils.Settings.GetBool("log-alert") {
		return
	}
	libs.Logger.Info("enable alert pusher")
	libs.Logger = libs.Logger.Named("go-fluentd-" + gutils.Settings.GetString("env") + "-" + gutils.Settings.GetString("host"))

	if gutils.Settings.GetString("settings.logger.push_api") != "" {
		// telegram alert
		alertPusher, err := gutils.NewAlertPusherWithAlertType(
			ctx,
			gutils.Settings.GetString("settings.logger.push_api"),
			gutils.Settings.GetString("settings.logger.alert_type"),
			gutils.Settings.GetString("settings.logger.push_token"),
		)
		if err != nil {
			libs.Logger.Panic("create AlertPusher", zap.Error(err))
		}
		libs.Logger = libs.Logger.
			WithOptions(zap.HooksWithFields(alertPusher.GetZapHook()))
	}

	if gutils.Settings.GetString("settings.pateo_alert.push_api") != "" {
		// pateo wechat alert pusher
		pateoAlertPusher, err := gutils.NewPateoAlertPusher(
			ctx,
			gutils.Settings.GetString("settings.pateo_alert.push_api"),
			gutils.Settings.GetString("settings.pateo_alert.token"),
		)
		if err != nil {
			libs.Logger.Panic("create PateoAlertPusher", zap.Error(err))
		}
		libs.Logger = libs.Logger.WithOptions(zap.HooksWithFields(pateoAlertPusher.GetZapHook()))
	}
}
