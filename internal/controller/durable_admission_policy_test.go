package controller

import (
	"testing"

	"github.com/spf13/viper"
)

func TestOverallDurableAdmissionCompatibilityPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]interface{}
		fail   bool
	}{
		{"plain-http-forced-durable", map[string]interface{}{"settings.acceptor.recvs.plugins.in.type": "http"}, false},
		{"http-cannot-disable", map[string]interface{}{"settings.acceptor.recvs.plugins.in.type": "http", "settings.acceptor.recvs.plugins.in.require_durable_ack": false}, true},
		{"http-durable-explicit", map[string]interface{}{"settings.acceptor.recvs.plugins.in.type": "http", "settings.acceptor.recvs.plugins.in.require_durable_ack": true}, false},
		{"fluent-chunk-required-at-runtime", map[string]interface{}{"settings.acceptor.recvs.plugins.in.type": "fluentd"}, false},
		{"fluent-no-concat", map[string]interface{}{"settings.acceptor.recvs.plugins.in.type": "fluentd", "settings.acceptor.recvs.plugins.in.concat": map[string]interface{}{"tag": map[string]interface{}{"msg_key": "message"}}}, true},
		{"syslog-no-durable-app-ack", map[string]interface{}{"settings.acceptor.recvs.plugins.in.type": "rsyslog"}, true},
		{"syslog-explicit-best-effort", map[string]interface{}{"settings.acceptor.recvs.plugins.in.type": "rsyslog", "settings.acceptor.recvs.plugins.in.best_effort": true}, false},
		{"no-string-coercion", map[string]interface{}{"settings.acceptor.recvs.plugins.in.type": "rsyslog", "settings.acceptor.recvs.plugins.in.best_effort": "true"}, true},
		{"kafka-cannot-waive", map[string]interface{}{"settings.acceptor.recvs.plugins.in.type": "kafka", "settings.acceptor.recvs.plugins.in.best_effort": true}, true},
		{"events", map[string]interface{}{"settings.acceptor.recvs.plugins.in.type": "http_events"}, false},
		{"no-prejournal-mutation", map[string]interface{}{"settings.acceptor_filters.plugins.default.add": map[string]interface{}{"tag": map[string]interface{}{"field": "value"}}}, true},
		{"no-prejournal-reentry", map[string]interface{}{"settings.acceptor_filters.plugins.spring.type": "spring"}, true},
		{"validation-only-default", map[string]interface{}{"settings.acceptor_filters.plugins.default.remove_empty_tag": true}, false},
		{"no-dry-success", map[string]interface{}{"dry": true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := viper.New()
			v.Set("settings.acceptor.recvs.plugins.in.active_env", []string{"test"})
			for k, value := range tc.fields {
				v.Set(k, value)
			}
			// False-positive control: legacy deployment remains compatible.
			if err := validateDurableAdmissionPolicy(v, "test"); err != nil {
				t.Fatalf("legacy policy changed: %v", err)
			}
			v.Set("settings.disk_budget", map[string]interface{}{"max_bytes": 3000000000})
			err := validateDurableAdmissionPolicy(v, "test")
			if (err != nil) != tc.fail {
				t.Fatalf("policy error=%v, want failure=%v", err, tc.fail)
			}
		})
	}
}

func TestOverallDurableAdmissionIgnoresInactiveReceivers(t *testing.T) {
	v := viper.New()
	v.Set("settings.disk_budget", map[string]interface{}{"max_bytes": 3000000000})
	v.Set("settings.acceptor.recvs.plugins.legacy.type", "rsyslog")
	v.Set("settings.acceptor.recvs.plugins.legacy.active_env", []string{"other"})
	if err := validateDurableAdmissionPolicy(v, "test"); err != nil {
		t.Fatal(err)
	}
}
