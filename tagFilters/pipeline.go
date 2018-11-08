package tagFilters

import (
	"errors"

	"github.com/Laisky/go-concator/libs"
	utils "github.com/Laisky/go-utils"
	"go.uber.org/zap"
)

type TagPipeline struct {
	*TagPipelineCfg
	TagFilterFactoryItfs []TagFilterFactoryItf
}

type TagPipelineCfg struct {
	DefaultInternalChanSize int
}

// NewTagPipeline create new TagPipeline
func NewTagPipeline(cfg *TagPipelineCfg, itfs ...TagFilterFactoryItf) *TagPipeline {
	utils.Logger.Info("create tag pipeline")
	if cfg.DefaultInternalChanSize <= 0 {
		cfg.DefaultInternalChanSize = 1000
	}

	return &TagPipeline{
		TagPipelineCfg:       cfg,
		TagFilterFactoryItfs: itfs,
	}
}

// Spawn create and run new Concator for new tag
func (p *TagPipeline) Spawn(tag string, outChan chan<- *libs.FluentMsg) (chan<- *libs.FluentMsg, error) {
	var (
		lastI          = len(p.TagFilterFactoryItfs) - 1
		f              TagFilterFactoryItf
		i              int
		isTagSupported = false
		downstreamChan = outChan
	)
	for i = lastI; i >= 0; i-- {
		f = p.TagFilterFactoryItfs[i]
		if f.IsTagSupported(tag) {
			utils.Logger.Info("enable tagfilter",
				zap.String("name", f.GetName()),
				zap.String("tag", tag))
			isTagSupported = true
			downstreamChan = f.Spawn(tag, downstreamChan) // downstream outChan is upstream's inChan
		}
	}

	if !isTagSupported {
		return nil, errors.New("tag do not has any tagfilter")
	}

	return downstreamChan, nil
}
