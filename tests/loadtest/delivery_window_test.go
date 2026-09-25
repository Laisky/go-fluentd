package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeliveryWindowDoesNotReleaseOnAdmissionOrOneDestination(t *testing.T) {
	entered := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; w.Write([]byte("{}")) }))
	defer srv.Close()
	tr := &trial{opts: options{Concurrency: 1, DeliveryWindow: true, Destinations: 2, Timeout: time.Second}, epoch: time.Now(), ingest: srv.URL}
	for id := 0; id < 2; id++ {
		tr.fixtures = append(tr.fixtures, makeFixture("logs", id, 1))
		tr.records = append(tr.records, record{Sink: make([]int64, 2), done: make(chan struct{})})
	}
	done := make(chan struct{})
	go func() { tr.load(srv.Client(), 0, 2, 0); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first request absent")
	}
	// Wait for the HTTP ACK to make this assertion distinguish ACK and delivery.
	deadline := time.Now().Add(time.Second)
	for {
		tr.mu.Lock()
		acked := tr.records[0].Status == 200
		tr.mu.Unlock()
		if acked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ACK absent")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-entered:
		t.Fatal("window released by admission ACK")
	case <-time.After(20 * time.Millisecond):
	}
	tr.mu.Lock()
	tr.records[0].Sink[0] = 1
	tr.mu.Unlock()
	select {
	case <-entered:
		t.Fatal("window released before every required destination")
	case <-time.After(20 * time.Millisecond):
	}
	close(tr.records[0].done)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("window did not reopen")
	}
	close(tr.records[1].done)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("load did not complete")
	}
}
