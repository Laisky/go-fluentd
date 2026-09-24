package controller

import (
	"fmt"
	"time"

	utils "github.com/Laisky/go-utils"
	"gofluentd/internal/recvs"
	"gofluentd/library"
)

// Keep protocol-specific configuration separate from legacy signed-log HTTP.
func (c *Controllor) initHTTPEventsRecv(env, name string) (*recvs.HTTPEventsRecv, error) {
	if utils.Settings.GetBool("dry") {
		return nil, fmt.Errorf("HTTP event receivers require a non-dry durable pipeline")
	}
	prefix := "settings.acceptor.recvs.plugins." + name + "."
	return recvs.NewHTTPEventsRecv(recvs.HTTPEventsRecvCfg{
		HTTPSrv: server, Name: name,
		Path:        utils.Settings.GetString(prefix + "path"),
		Tag:         library.LoadTagReplaceEnv(env, utils.Settings.GetString(prefix+"tag")),
		Format:      utils.Settings.GetString(prefix + "format"),
		BearerToken: utils.Settings.GetString(prefix + "bearer_token"),
		MaxBodySize: utils.Settings.GetInt64(prefix + "max_body_byte"),
		MaxRecords:  utils.Settings.GetInt(prefix + "max_records"),
		AckTimeout:  utils.Settings.GetDuration(prefix+"ack_timeout_sec") * time.Second,
	})
}
