package senders

import (
	"context"
	"fmt"
	"time"

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
	utils.Counter
	logger *utils.LoggerType

	*BaseSender
	*NullSenderCfg
}

// NewNullSender create new null sender
func NewNullSender(cfg *NullSenderCfg) *NullSender {
	logger := libs.Logger.Named(cfg.Name)
	logger.Info("new null sender",
		zap.Strings("tags", cfg.Tags),
		zap.String("name", cfg.Name),
	)
	switch cfg.LogLevel {
	case "info":
	case "debug":
	default:
		logger.Info("null sender will discard msg without any log",
			zap.String("level", cfg.LogLevel))
	}
	if cfg.InChanSize < 1000 {
		logger.Warn("small inchan size could reduce performance")
	}

	s := &NullSender{
		logger: logger,
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

func (s *NullSender) startStats(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.logger.Info("send msgs ", zap.Float64("n/s", s.GetSpeed()))
		}
	}
}

// Spawn fork
func (s *NullSender) Spawn(ctx context.Context) chan<- *libs.FluentMsg {
	s.logger.Info("spawn for tag")
	go s.startStats(ctx)
	inChan := make(chan *libs.FluentMsg, s.InChanSize) // for each tag
	for i := 0; i < s.NFork; i++ {
		go func() {
			var (
				msg *libs.FluentMsg
				ok  bool
			)
			defer s.logger.Info("null sender exit", zap.String("msg", fmt.Sprint("msg", msg)))
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok = <-inChan:
					if !ok {
						s.logger.Info("inChan closed")
						return
					}
				}

				s.Count()
				switch s.LogLevel {
				case "info":
					s.logger.Info("consume msg",
						zap.String("tag", msg.Tag),
						zap.String("msg", fmt.Sprint(msg.Message)))
				case "debug":
					s.logger.Debug("consume msg",
						zap.String("tag", msg.Tag),
						zap.String("msg", fmt.Sprint(msg.Message)))
				}

				if s.IsCommit {
					s.successedChan <- msg
				} else {
					s.failedChan <- msg
				}
			}
		}()
	}
	return inChan
}
