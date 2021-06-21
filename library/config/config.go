package config

type Config struct {
	CMDArgs cmdArgs
}

type cmdArgs struct {
	Debug               bool
	Dry                 bool
	EnableAutoGC        bool
	GCMemRatio          uint
	HostName            string
	ConfigPath          string
	ConfigServer        string
	ConfigServerAppname string
	ConfigServerProfile string
	ConfigServerLabel   string
	ConfigServerKey     string
	ListenAddr          string
	Env                 string
	LogLevel            string
	EnableLogAlert      bool
	HeartBeat           uint
}
