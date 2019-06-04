module github.com/Laisky/go-fluentd

go 1.12

require (
	github.com/Depado/ginprom v1.1.1
	github.com/Laisky/go-syslog v2.3.3+incompatible
	github.com/Laisky/go-utils v1.4.0
	github.com/Laisky/zap v1.9.2
	github.com/Shopify/sarama v1.22.0
	github.com/cespare/xxhash v1.1.0
	github.com/gin-contrib/pprof v1.2.0
	github.com/gin-gonic/gin v1.4.0
	github.com/json-iterator/go v1.1.6
	github.com/pkg/errors v0.8.1
	github.com/spf13/pflag v1.0.3
	github.com/spf13/viper v1.3.2
	github.com/tinylib/msgp v1.1.0
)

replace github.com/ugorji/go v1.1.4 => github.com/ugorji/go/codec v0.0.0-20190204201341-e444a5086c43
