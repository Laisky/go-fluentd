package controller

import (
	"fmt"
	"time"

	utils "github.com/Laisky/go-utils"
	"gofluentd/internal/recvs"
	"gofluentd/internal/senders"
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

func (c *Controllor) initHTTPEventsSender(env, name string) (*senders.HTTPEventsSender, error) {
	prefix := "settings.producer.plugins." + name + "."
	if utils.Settings.GetBool("dry") || utils.Settings.GetBool(prefix+"is_discard_when_blocked") {
		return nil, fmt.Errorf("HTTP event senders do not support dry or discard-when-blocked acknowledgement")
	}
	return senders.NewHTTPEventsSender(senders.HTTPEventsSenderCfg{
		Name: name, Addr: utils.Settings.GetString(prefix + "addr"),
		Format: utils.Settings.GetString(prefix + "format"), Mode: utils.Settings.GetString(prefix + "mode"),
		Tags:             library.LoadTagsReplaceEnv(env, utils.Settings.GetStringSlice(prefix+"tags")),
		BearerToken:      utils.Settings.GetString(prefix + "bearer_token"),
		BatchSize:        utils.Settings.GetInt(prefix + "msg_batch_size"),
		InChanSize:       utils.Settings.GetInt("settings.producer.sender_inchan_size"),
		NFork:            utils.Settings.GetInt(prefix + "forks"),
		MaxAttempts:      utils.Settings.GetInt(prefix + "max_attempts"),
		MaxBodySize:      utils.Settings.GetInt64(prefix + "max_body_byte"),
		MaxResponseBytes: utils.Settings.GetInt64(prefix + "max_response_byte"),
		MaxWait:          utils.Settings.GetDuration(prefix+"max_wait_msec") * time.Millisecond,
		Timeout:          utils.Settings.GetDuration(prefix+"request_timeout_sec") * time.Second,
		RetryBackoff:     utils.Settings.GetDuration(prefix+"retry_backoff_msec") * time.Millisecond,
		MaxRetryDelay:    utils.Settings.GetDuration(prefix+"max_retry_delay_sec") * time.Second,
	})
}
