package config

type Config struct {
	// CMDArgs command line args
	CMDArgs  cmdArgs  `mapstructure:"-"`
	Settings settings `mapstructure:"settings"`
}

type cmdArgs struct {
	Debug          bool
	Dry            bool
	EnableAutoGC   bool
	GCMemRatio     uint
	HostName       string
	ListenAddr     string
	Env            string
	LogLevel       string
	EnableLogAlert bool
	HeartBeat      uint

	ConfigPath          string
	ConfigServer        string
	ConfigServerAppname string
	ConfigServerProfile string
	ConfigServerLabel   string
	ConfigServerKey     string
}

type settings struct {
	Logger   settingsLogger   `mapstructure:"logger"`
	Journal  settingsJournal  `mapstructure:"journal"`
	Acceptor settingsAcceptor `mapstructure:"acceptor"`
}

type settingsLogger struct {
	PushAPI   string `mapstructure:"push_api"`
	AlertType string `mapstructure:"alert_type"`
	PushToken string `mapstructure:"push_token"`
}

type settingsJournal struct {
	BufDirPath string `mapstructure:"buf_dir_path"`
	IsCompress bool   `mapstructure:"is_compress"`
}

type settingsAcceptor struct {
	Recvs settingsAcceptorRecvs `mapstructure:"recvs"`
}

type settingsAcceptorRecvs struct {
	Plugins map[string]interface{} `mapstructure:"plugins"`
}

// type settingsAcceptorRecvsPlugins struct {
// 	Fluentd *settingsAcceptorRecvsPluginsFluentd `mapstructure:"fluentd"`
// }

// type settingsAcceptorRecvsPluginsFluentd struct {
// 	Type      string   `mapstructure:"type"`
// 	ActiveEnv []string `mapstructure:"active_env"`
// 	Concat    map[string]interface{} `mapstructure:"concat"`
// 	// Concat map[string]struct {
// 	// 	MsgKey     string `"mapstructure:msg_key"`
// 	// 	Identifier string `"mapstructure:identifier"`
// 	// 	HeadRegexp string `"mapstructure:head_regexp"`
// 	// } `"mapstructure:concat"`
// }
