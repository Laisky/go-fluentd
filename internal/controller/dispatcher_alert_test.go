package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"gofluentd/internal/alerts"
	"gofluentd/library"
)

// distinctFailure differs in words, so each record is its own error kind.
func distinctFailure(id int64) string {
	return fmt.Sprintf("failure %c%c", 'a'+id/26, 'a'+id%26)
}

func TestDispatcherAlertSaturationDoesNotChangeRecords(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	o, err := alerts.New(ctx, alerts.Config{Token: "123:synthetic", ChatID: "synthetic", IncludeMessage: true, Tags: []string{"app"}, MaxGroupsPerSource: 1024}, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); o.Wait() }()
	in := make(chan *library.FluentMsg, 128)
	pipe := &componentPipeline{counts: map[string]int{}}
	d := NewDispatcher(&DispatcherCfg{InChan: in, TagPipeline: pipe, NFork: 4, OutChanSize: 128, Observe: func(m *library.FluentMsg) { o.ObserveFluent(m.Tag, m.Message) }})
	d.Run(ctx)
	for i := 0; i < 100; i++ {
		in <- &library.FluentMsg{Tag: "app", ID: int64(i), Message: map[string]interface{}{"level": "error", "msg": distinctFailure(int64(i))}}
	}
	seen := map[int64]bool{}
	for i := 0; i < 100; i++ {
		m := componentRecv(t, d.GetOutChan())
		if seen[m.ID] || m.Message["msg"] != distinctFailure(m.ID) {
			t.Fatal("record changed or duplicated")
		}
		seen[m.ID] = true
	}
	if o.Stats().Dropped == 0 {
		t.Fatalf("queue saturation was not exercised: %+v", o.Stats())
	}
	close(in)
}
