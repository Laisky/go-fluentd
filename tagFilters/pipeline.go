package tagFilters

import (
	"context"
	"sync"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-fluentd/monitor"
	"github.com/Laisky/zap"
)

const (
	defaultInternalFilterChanSize = 100000
)

type TagPipelineItf interface {
	Spawn(context.Context, string, chan<- *libs.FluentMsg) (chan<- *libs.FluentMsg, error)
}

type TagPipelineCfg struct {
	InternalChanSize int
	MsgPool          *sync.Pool
	WaitCommitChan   chan<- *libs.FluentMsg
}

type TagPipeline struct {
	*TagPipelineCfg
	TagFilterFactoryItfs []TagFilterFactoryItf
	monitorChans         map[string]chan<- *libs.FluentMsg
}

// NewTagPipeline create new TagPipeline
func NewTagPipeline(ctx context.Context, cfg *TagPipelineCfg, itfs ...TagFilterFactoryItf) *TagPipeline {
	libs.Logger.Info("create tag pipeline")
	if cfg.InternalChanSize <= 0 {
		cfg.InternalChanSize = defaultInternalFilterChanSize
	}

	for _, itf := range itfs {
		itf.SetMsgPool(cfg.MsgPool)
		itf.SetWaitCommitChan(cfg.WaitCommitChan)
		itf.SetDefaultIntervalChanSize(cfg.InternalChanSize)
	}

	tp := &TagPipeline{
		TagPipelineCfg:       cfg,
		TagFilterFactoryItfs: itfs,
		monitorChans:         map[string]chan<- *libs.FluentMsg{},
	}
	tp.registryMonitor()
	return tp
}

// Spawn create and run new Concator for new tag, return inchan
func (p *TagPipeline) Spawn(ctx context.Context, tag string, outChan chan<- *libs.FluentMsg) (chan<- *libs.FluentMsg, error) {
	libs.Logger.Info("spawn tagpipeline", zap.String("tag", tag))
	var (
		f              TagFilterFactoryItf
		i              int
		isTagSupported = false
		downstreamChan = outChan
	)
	for i = len(p.TagFilterFactoryItfs) - 1; i >= 0; i-- {
		f = p.TagFilterFactoryItfs[i]
		if f.IsTagSupported(tag) {
			libs.Logger.Info("enable tagfilter",
				zap.String("name", f.GetName()),
				zap.String("tag", tag))
			isTagSupported = true
			downstreamChan = f.Spawn(ctx, tag, downstreamChan)   // downstream's inChan is upstream's outChan
			p.monitorChans[tag+"."+f.GetName()] = downstreamChan // instream
		}
	}

	if !isTagSupported {
		libs.Logger.Info("skip tagPipeline", zap.String("tag", tag))
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
