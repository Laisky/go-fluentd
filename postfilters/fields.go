package postfilters

import (
	"fmt"

	"gofluentd/library"

	"github.com/Laisky/zap"
)

type FieldsFilterCfg struct {
	Tags,
	IncludeFields, ExcludeFields []string // filter fields
	NewFieldTemplates map[string]string
}

type FieldsFilter struct {
	BaseFilter
	*FieldsFilterCfg
	supportedTags, includeMap map[string]struct{}
}

func NewFieldsFilter(cfg *FieldsFilterCfg) *FieldsFilter {
	f := &FieldsFilter{
		FieldsFilterCfg: cfg,
	}
	f.includeMap = getIncludeMap(cfg.IncludeFields)
	f.supportedTags = map[string]struct{}{}
	for _, t := range f.Tags {
		f.supportedTags[t] = struct{}{}
	}

	library.Logger.Info("create new FieldsFilter",
		zap.Strings("tags", cfg.Tags),
		zap.String("includes", fmt.Sprint(f.includeMap)),
		zap.String("new_fields", fmt.Sprint(cfg.NewFieldTemplates)),
	)
	return f
}

func getIncludeMap(include []string) map[string]struct{} {
	im := map[string]struct{}{}
	if len(include) == 0 {
		return im
	}

	for _, k := range append(include, library.MustIncludeFileds...) {
		im[k] = struct{}{}
	}
	return im
}

func (f *FieldsFilter) Filter(msg *library.FluentMsg) *library.FluentMsg {
	var ok bool
	if _, ok = f.supportedTags[msg.Tag]; !ok {
		return msg
	}

	// combine template
	for newFieldName, tpl := range f.NewFieldTemplates {
		msg.Message[newFieldName] = library.TemplateWithMap(tpl, msg.Message)
	}
	// only remain include fields
	if len(f.includeMap) != 0 {
		for k := range msg.Message {
			if _, ok = f.includeMap[k]; !ok {
				delete(msg.Message, k)
			}
		}
	} else {
		// remove exclude fields
		if len(f.ExcludeFields) != 0 {
			for _, f := range f.ExcludeFields {
				delete(msg.Message, f)
			}
		}
	}

	return msg
}
