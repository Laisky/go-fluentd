package controller

import (
	"errors"
	"fmt"
	"reflect"
)

type admissionSettings interface {
	Get(string) interface{}
	GetString(string) string
	GetStringSlice(string) []string
}

// Validate before opening storage or starting listeners. A budgeted deployment
// must not advertise retained originals while a prejournal stage can replace
// them. Keep legacy behavior only when the overall budget is omitted.
func validateDurableAdmissionPolicy(v admissionSettings, env string) error {
	if v.Get("settings.disk_budget") == nil {
		return nil
	}
	if dry, _ := v.Get("dry").(bool); dry {
		return errors.New("overall disk budget requires durable admission; dry mode is incompatible")
	}
	plugins, ok := v.Get("settings.acceptor.recvs.plugins").(map[string]interface{})
	if !ok {
		return nil
	} // OTLP does not traverse the legacy acceptor filters.
	active := false
	for name := range plugins {
		if StringListContains(v.GetStringSlice("settings.acceptor.recvs.plugins."+name+".active_env"), env) {
			active = true
		}
	}
	if !active {
		return nil
	}
	if plugins, ok := v.Get("settings.acceptor_filters.plugins").(map[string]interface{}); ok {
		for name := range plugins {
			if name != "default" {
				return fmt.Errorf("overall disk budget cannot preserve originals through prejournal filter %q; remove it or move its transformation to a qualified postjournal stage", name)
			}
		}
	}
	if admissionSettingPresent(v.Get("settings.acceptor_filters.plugins.default.add")) {
		return errors.New("overall disk budget cannot preserve originals through prejournal default.add; move the transformation to a qualified postjournal stage")
	}
	for name := range plugins {
		path := "settings.acceptor.recvs.plugins." + name
		if !StringListContains(v.GetStringSlice(path+".active_env"), env) {
			continue
		}
		typ := v.GetString(path + ".type")
		bestEffort := false
		if raw := v.Get(path + ".best_effort"); raw != nil {
			var valid bool
			bestEffort, valid = raw.(bool)
			if !valid {
				return fmt.Errorf("receiver %q best_effort must be an explicit boolean", name)
			}
		}
		if bestEffort && typ != "rsyslog" && typ != "fluentd" {
			return fmt.Errorf("receiver %q best_effort applies only to transports without durable acknowledgements", name)
		}
		switch typ {
		case "rsyslog":
			if !bestEffort {
				return fmt.Errorf("receiver %q syslog TCP and UDP have no durable application ACK; migrate to OTLP/HTTP, acknowledged Forward or Kafka, or explicitly accept best_effort: true", name)
			}
		case "fluentd":
			if admissionSettingPresent(v.Get(path + ".concat")) {
				return fmt.Errorf("receiver %q prejournal concatenation cannot preserve each original; move concatenation after durable storage", name)
			}
			if rewrite, _ := v.Get(path + ".is_rewrite_tag_from_tag_key").(bool); rewrite {
				return fmt.Errorf("receiver %q prejournal tag-key mutation is incompatible with original-preserving durable Forward input; move routing changes to a qualified postjournal stage", name)
			}
		case "http":
			if raw := v.Get(path + ".require_durable_ack"); raw != nil {
				if required, ok := raw.(bool); !ok || !required {
					return fmt.Errorf("receiver %q requires durable acknowledgement under the overall disk budget; omit require_durable_ack or set true", name)
				}
			}
		}
	}
	return nil
}

func admissionSettingPresent(raw interface{}) bool {
	if raw == nil {
		return false
	}
	v := reflect.ValueOf(raw)
	switch v.Kind() {
	case reflect.Map, reflect.Slice, reflect.Array:
		return v.Len() != 0
	}
	return true
}
