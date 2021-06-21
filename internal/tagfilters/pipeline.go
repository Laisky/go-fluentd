package tagfilters

import (
	"context"
	"sync"

	"gofluentd/internal/monitor"
	"gofluentd/library"

	"github.com/Laisky/zap"
)

const (
	defaultInternalFilterChanSize = 10000
)

type TagPipelineItf interface {
	Spawn(context.Context, string, chan<- *library.FluentMsg) (chan<- *library.FluentMsg, error)
}

type TagPipelineCfg struct {
	InternalChanSize int
	MsgPool          *sync.Pool
	WaitCommitChan   chan<- *library.FluentMsg
}

type TagPipeline struct {
	*TagPipelineCfg
	TagFilterFactoryItfs []TagFilterFactoryItf
	monitorChans         map[string]chan<- *library.FluentMsg
}

// NewTagPipeline create new TagPipeline
func NewTagPipeline(ctx context.Context, cfg *TagPipelineCfg, itfs ...TagFilterFactoryItf) *TagPipeline {
	p := &TagPipeline{
		TagPipelineCfg:       cfg,
		TagFilterFactoryItfs: itfs,
		monitorChans:         map[string]chan<- *library.FluentMsg{},
	}
	if err := p.valid(); err != nil {
		library.Logger.Panic("config invalid", zap.Error(err))
	}

	for _, itf := range itfs {
		itf.SetMsgPool(p.MsgPool)
		itf.SetWaitCommitChan(p.WaitCommitChan)
		itf.SetDefaultIntervalChanSize(p.InternalChanSize)
	}

	p.registryMonitor()
	library.Logger.Info("create tag pipeline",
		zap.Int("internal_chan_size", p.InternalChanSize),
	)
	return p
}

func (p *TagPipeline) valid() error {
	if p.InternalChanSize <= 0 {
		p.InternalChanSize = defaultInternalFilterChanSize
		library.Logger.Info("reset internal_chan_size", zap.Int("internal_chan_size", p.InternalChanSize))
	}

	return nil
}

// Spawn create and run new Concator for new tag, return inchan
func (p *TagPipeline) Spawn(ctx context.Context, tag string, outChan chan<- *library.FluentMsg) (chan<- *library.FluentMsg, error) {
	library.Logger.Info("spawn tagpipeline", zap.String("tag", tag))
	var (
		f              TagFilterFactoryItf
		i              int
		isTagSupported = false
		downstreamChan = outChan
	)
	for i = len(p.TagFilterFactoryItfs) - 1; i >= 0; i-- {
		f = p.TagFilterFactoryItfs[i]
		if f.IsTagSupported(tag) {
			library.Logger.Info("enable tagfilter",
				zap.String("name", f.GetName()),
				zap.String("tag", tag))
			isTagSupported = true
			downstreamChan = f.Spawn(ctx, tag, downstreamChan)   // downstream's inChan is upstream's outChan
			p.monitorChans[tag+"."+f.GetName()] = downstreamChan // instream
		}
	}

	if !isTagSupported {
		library.Logger.Info("skip tagPipeline", zap.String("tag", tag))
		return outChan, nil
	}

	return downstreamChan, nil
}

func (p *TagPipeline) registryMonitor() {
	monitor.AddMetric("tagpipeline", func() map[string]interface{} {
		metrics := map[string]interface{}{}
		for k, c := range p.monitorChans {
			metrics[k+".ChanLen"] = len(c)
			metrics[k+".ChanCap"] = cap(c)
		}
		return metrics
	})
}
