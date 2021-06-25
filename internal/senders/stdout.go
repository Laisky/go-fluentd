package senders

import (
	"context"
	"fmt"
	"time"

	"gofluentd/library"
	"gofluentd/library/log"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

// StdoutSenderCfg configuration of StdoutSender
type StdoutSenderCfg struct {
	Name, LogLevel                 string
	Tags                           []string
	NFork, InChanSize              int
	IsCommit, IsDiscardWhenBlocked bool
}

// StdoutSender print or discard
type StdoutSender struct {
	utils.Counter
	logger *utils.LoggerType

	*BaseSender
	*StdoutSenderCfg
}

// NewStdoutSender create new null sender
func NewStdoutSender(cfg *StdoutSenderCfg) *StdoutSender {
	s := &StdoutSender{
		logger: log.Logger.Named(cfg.Name),
		BaseSender: &BaseSender{
			IsDiscardWhenBlocked: cfg.IsDiscardWhenBlocked,
		},
		StdoutSenderCfg: cfg,
	}
	if err := s.valid(); err != nil {
		s.logger.Panic("stdout sender invalid", zap.Error(err))
	}

	s.logger.Info("new stdout sender",
		zap.String("LogLevel", s.LogLevel),
		zap.Int("InChanSize", s.InChanSize),
		zap.Int("NFork", s.NFork),
	)
	s.SetSupportedTags(cfg.Tags)
	return s
}

func (s *StdoutSender) valid() error {
	switch s.LogLevel {
	case "info":
	case "debug":
	default:
		s.logger.Info("will discard msg without any log",
			zap.String("level", s.LogLevel))
	}
	if s.InChanSize < 1000 {
		s.logger.Warn("small inchan size could reduce performance")
	}

	if s.NFork <= 0 {
		s.NFork = 1
		s.logger.Info("reset n_fork", zap.Int("n_fork", s.NFork))
	}

	return nil
}

// GetName get the name of null sender
func (s *StdoutSender) GetName() string {
	return s.Name
}

func (s *StdoutSender) startStats(ctx context.Context) {
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
func (s *StdoutSender) Spawn(ctx context.Context) chan<- *library.FluentMsg {
	s.logger.Info("spawn for tag")
	go s.startStats(ctx)
	inChan := make(chan *library.FluentMsg, s.InChanSize) // for each tag
	for i := 0; i < s.NFork; i++ {
		go func() {
			var (
				msg *library.FluentMsg
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
