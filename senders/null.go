package senders

import (
	"fmt"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

// NullSenderCfg configuration of NullSender
type NullSenderCfg struct {
	Name, LogLevel                 string
	Tags                           []string
	NFork, InChanSize              int
	IsCommit, IsDiscardWhenBlocked bool
}

// NullSender /dev/null, will discard all msgs
type NullSender struct {
	*BaseSender
	*NullSenderCfg
}

// NewNullSender create new null sender
func NewNullSender(cfg *NullSenderCfg) *NullSender {
	utils.Logger.Info("new null sender",
		zap.Strings("tags", cfg.Tags),
		zap.String("name", cfg.Name),
	)
	switch cfg.LogLevel {
	case "info":
	case "debug":
	default:
		utils.Logger.Panic("null sender's LogLevel should be info/debug", zap.String("level", cfg.LogLevel))
	}
	if cfg.InChanSize < 1000 {
		utils.Logger.Warn("small inchan size could reduce performance")
	}

	s := &NullSender{
		BaseSender: &BaseSender{
			IsDiscardWhenBlocked: cfg.IsDiscardWhenBlocked,
		},
		NullSenderCfg: cfg,
	}
	s.SetSupportedTags(cfg.Tags)
	return s
}

// GetName get the name of null sender
func (s *NullSender) GetName() string {
	return s.Name
}

// Spawn fork
func (s *NullSender) Spawn(tag string) chan<- *libs.FluentMsg {
	utils.Logger.Info("spawn for tag", zap.String("tag", tag))
	inChan := make(chan *libs.FluentMsg, s.InChanSize) // for each tag

	for i := 0; i < s.NFork; i++ {
		go func() {
			defer func() {
				if err := recover(); err != nil {
					utils.Logger.Panic("null sender exit", zap.Error(err.(error)))
				}
			}()

			for {
				for msg := range inChan {
					if s.LogLevel == "info" {
						utils.Logger.Info("consume msg",
							zap.String("tag", msg.Tag),
							zap.String("msg", fmt.Sprint(msg.Message)))
					} else {
						utils.Logger.Debug("consume msg",
							zap.String("tag", msg.Tag),
							zap.String("msg", fmt.Sprint(msg.Message)))
					}

					if s.IsCommit {
						s.discardChan <- msg
					} else {
						s.discardWithoutCommitChan <- msg
					}
				}
			}
		}()
	}
	return inChan
}
