package main

import (
	"context"
	"fmt"
	"time"

	"gofluentd/internal/controller"
	"gofluentd/internal/global"
	"gofluentd/library/log"

	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/spf13/pflag"
)

func main() {
	ctx := context.Background()
	parseCMD()
	setupGC(ctx)
	setupSettings()
	setupLogger(ctx)
	defer log.Logger.Info("All done")

	// run
	controllor := controller.NewControllor()
	controllor.Run(ctx)
}

func setupGC(ctx context.Context) {
	if !global.Config.CMDArgs.EnableAutoGC {
		return
	}

	opts := []gutils.GcOptFunc{}
	ratio := global.Config.CMDArgs.GCMemRatio
	if ratio > 0 {
		opts = append(opts, gutils.WithGCMemRatio(int(ratio)))
	}

	if err := gutils.AutoGC(ctx, opts...); err != nil {
		log.Logger.Panic("enable auto gc", zap.Error(err))
	}
}

// setupSettings setup arguments restored in viper
func setupSettings() {
	// check `--log-level`
	switch global.Config.CMDArgs.LogLevel {
	case "debug":
	case "info":
	case "warn":
	case "error":
	default:
		gutils.Logger.Panic(
			"unknown value",
			zap.String("--log-level", global.Config.CMDArgs.LogLevel),
		)
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
	if global.Config.CMDArgs.Debug {
		fmt.Println("run in debug mode")
		global.Config.CMDArgs.LogLevel = gutils.LoggerLevelDebug
		gutils.Settings.Set("log-level", gutils.LoggerLevelDebug)
	} else { // prod mode
		fmt.Println("run in prod mode")
	}

	// log
	if err := log.Logger.ChangeLevel(global.Config.CMDArgs.LogLevel); err != nil {
		log.Logger.Panic("change log level", zap.Error(err))
	}
	log.Logger.Info("set log level",
		zap.String("level", global.Config.CMDArgs.LogLevel))

	// clock
	gutils.SetupClock(100 * time.Millisecond)

	// load configuration
	isCfgLoaded := false
	cfgFilePath := global.Config.CMDArgs.ConfigPath
	if err := gutils.Settings.LoadFromFile(cfgFilePath); err != nil {
		log.Logger.Info("can not load config from disk",
			zap.String("config", cfgFilePath))
	} else {
		log.Logger.Info("success load configuration from dir",
			zap.String("config", cfgFilePath))
		isCfgLoaded = true
	}

	if global.Config.CMDArgs.ConfigServer != "" &&
		global.Config.CMDArgs.ConfigServerAppname != "" &&
		global.Config.CMDArgs.ConfigServerProfile != "" &&
		global.Config.CMDArgs.ConfigServerLabel != "" &&
		global.Config.CMDArgs.ConfigServerKey != "" {
		if err := gutils.Settings.LoadFromConfigServer(
			global.Config.CMDArgs.ConfigServer,
			global.Config.CMDArgs.ConfigServerAppname,
			global.Config.CMDArgs.ConfigServerProfile,
			global.Config.CMDArgs.ConfigServerLabel,
		); err != nil {
			log.Logger.Panic("try to load configuration from config-server got error", zap.Error(err))
		} else {
			log.Logger.Info("success load configuration from config-server")
			isCfgLoaded = true
		}
	}

	if !isCfgLoaded {
		log.Logger.Panic("can not load any configuration")
	}
	if err := gutils.Settings.Unmarshal(global.Config); err != nil {
		log.Logger.Panic("unmarshal settings", zap.Error(err))
	}
}

func parseCMD() {
	pflag.BoolVar(&global.Config.CMDArgs.Debug, "debug", false, "run in debug mode")
	pflag.BoolVar(&global.Config.CMDArgs.Dry, "dry", false, "run in dry mode")
	pflag.BoolVar(&global.Config.CMDArgs.EnableAutoGC, "enable-auto-gc", false, "enable auto gc")
	pflag.UintVar(&global.Config.CMDArgs.GCMemRatio, "gc-mem-ratio", 85, "trigger gc when memory usage")
	pflag.StringVar(&global.Config.CMDArgs.HostName, "host", "unknown", "hostname")
	pflag.StringVarP(&global.Config.CMDArgs.ConfigPath, "config", "c", "/etc/go-fluentd/settings/settings.yml", "config file path")
	pflag.StringVar(&global.Config.CMDArgs.ConfigServer, "config-server", "", "config server url")
	pflag.StringVar(&global.Config.CMDArgs.ConfigServerAppname, "config-server-appname", "", "config server app name")
	pflag.StringVar(&global.Config.CMDArgs.ConfigServerProfile, "config-server-profile", "", "config server profile name")
	pflag.StringVar(&global.Config.CMDArgs.ConfigServerLabel, "config-server-label", "", "config server branch name")
	pflag.StringVar(&global.Config.CMDArgs.ConfigServerKey, "config-server-key", "", "raw content key ")
	pflag.StringVar(&global.Config.CMDArgs.ListenAddr, "addr", "localhost:8080", "like `localhost:8080`")
	pflag.StringVar(&global.Config.CMDArgs.Env, "env", "", "environment `sit/perf/uat/prod`")
	pflag.StringVar(&global.Config.CMDArgs.LogLevel, "log-level", "info", "`debug/info/error`")
	pflag.BoolVar(&global.Config.CMDArgs.EnableLogAlert, "log-alert", false, "is enable log AlertPusher")
	pflag.UintVar(&global.Config.CMDArgs.HeartBeat, "heartbeat", 60, "heartbeat seconds")
	pflag.Parse()
	if err := gutils.Settings.BindPFlags(pflag.CommandLine); err != nil {
		log.Logger.Panic("parse command arguments", zap.Error(err))
	}
}

func setupLogger(ctx context.Context) {
	if !global.Config.CMDArgs.EnableLogAlert {
		return
	}

	log.Logger.Info("enable alert pusher")
	log.Logger = log.Logger.Named("go-fluentd-" + global.Config.CMDArgs.Env + "-" + global.Config.CMDArgs.HostName)

	if gutils.Settings.GetString("settings.logger.push_api") != "" {
		// telegram alert
		alertPusher, err := gutils.NewAlertPusherWithAlertType(
			ctx,
			gutils.Settings.GetString("settings.logger.push_api"),
			gutils.Settings.GetString("settings.logger.alert_type"),
			gutils.Settings.GetString("settings.logger.push_token"),
		)
		if err != nil {
			log.Logger.Panic("create AlertPusher", zap.Error(err))
		}
		log.Logger = log.Logger.
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
			log.Logger.Panic("create PateoAlertPusher", zap.Error(err))
		}
		log.Logger = log.Logger.WithOptions(zap.HooksWithFields(pateoAlertPusher.GetZapHook()))
	}
}
