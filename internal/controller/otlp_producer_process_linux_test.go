//go:build linux

package controller_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"gofluentd/internal/controller"
	"gofluentd/internal/otlpstate"
)

// TestOTLPProducerProcessHelper is an independent consumer of the real producer,
// journal and disposition store. Its HTTP callback uses explicit fixture response
// classifications. It is not the eventual configured OTLP plugin/Collector E2E.
func TestOTLPProducerProcessHelper(t *testing.T) {
	mode := os.Getenv("OTLP_PRODUCER_CHILD")
	if mode == "" {
		return
	}
	s := producerStore(t, os.Getenv("OTLP_PRODUCER_STATE"))
	j := producerJournal(t, os.Getenv("OTLP_PRODUCER_WAL"), os.Getenv("OTLP_PRODUCER_GZIP") == "true")
	var peers []controller.OTLPDestination
	for _, id := range []string{"a", "b", "c"} {
		peers = append(peers, controller.OTLPDestination{ID: id, Send: func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
			req, err := http.NewRequestWithContext(ctx, "POST", os.Getenv("OTLP_PRODUCER_PEER")+"/"+id, bytes.NewReader(e.Payload))
			if err != nil {
				return otlpstate.Outcome{}, err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return otlpstate.Outcome{}, err
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
			if err != nil {
				return otlpstate.Outcome{}, err
			}
			o := producerAccepted()
			o.HTTPStatus, o.Response = resp.StatusCode, b
			if resp.StatusCode == 503 {
				o.Kind = otlpstate.Retryable
			} else if id == "c" {
				o.Kind, o.RejectedItems = otlpstate.Partial, 1
			}
			return o, nil
		}})
	}
	p := producerNew(t, s, peers...)
	var record controller.OTLPRecord
	if mode == "first" {
		var err error
		record, err = p.Plan("dedicated-wal/generation1", 42, producerEnvelope())
		if err != nil {
			t.Fatal(err)
		}
		d, err := record.JournalData()
		if err != nil {
			t.Fatal(err)
		}
		if err = j.WriteData(d); err != nil {
			t.Fatal(err)
		}
		if err = j.Sync(); err != nil {
			t.Fatal(err)
		}
	} else {
		record = replayPlan(t, j)
	}
	ackErr := errors.New("before final journal ACK")
	report, err := p.Process(context.Background(), record, func(_ context.Context, id int64) error {
		if mode == "first" {
			t.Error("released while b is pending")
			return nil
		}
		if mode == "second" {
			return ackErr
		}
		if err := j.WriteId(id); err != nil {
			return err
		}
		return j.Sync()
	})
	switch mode {
	case "first":
		if !errors.Is(err, controller.ErrOTLPDeliveryPending) || report.Resolved || report.JournalReleased {
			t.Fatalf("first: %+v %v", report, err)
		}
	case "second":
		if !errors.Is(err, ackErr) || !report.Resolved || report.FullyDelivered || report.JournalReleased {
			t.Fatalf("second: %+v %v", report, err)
		}
	case "third":
		if err != nil || !report.Resolved || report.FullyDelivered || !report.JournalReleased {
			t.Fatalf("third: %+v %v", report, err)
		}
		if got := p.Counters(); got.Accepted != 0 || got.Quarantined != 0 || got.ReplayHits != 3 {
			t.Fatalf("restart counted saved outcomes again: %+v", got)
		}
		return
	default:
		t.Fatal("invalid child mode")
	}
	fmt.Println("CHECKPOINT")
	for {
		time.Sleep(time.Hour)
	} // only SIGKILL can terminate the first two phases
}

func producerChild(t *testing.T, state, wal, peer, mode string, gzip bool) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOTLPProducerProcessHelper$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "OTLP_PRODUCER_CHILD="+mode, "OTLP_PRODUCER_STATE="+state, "OTLP_PRODUCER_WAL="+wal, "OTLP_PRODUCER_PEER="+peer, fmt.Sprint("OTLP_PRODUCER_GZIP=", gzip), "GORACE=halt_on_error=1 exitcode=66")
	return cmd
}
func producerKillCheckpoint(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	cmd.Stderr = &logs
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if sc.Text() == "CHECKPOINT" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			err = cmd.Wait()
			t.Fatalf("child missing checkpoint: %v %s", err, logs.String())
		}
	case <-time.After(15 * time.Second):
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("child timeout: %s", logs.String())
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("not killed: %v", err)
	}
	if st, ok := exit.Sys().(syscall.WaitStatus); !ok || st.Signal() != syscall.SIGKILL {
		t.Fatalf("not SIGKILL: %v", err)
	}
	if strings.Contains(logs.String(), "DATA RACE") {
		t.Fatal(logs.String())
	}
}

func TestOTLPProducerMixedDestinationsSurviveSIGKILL(t *testing.T) {
	for _, gzip := range []bool{false, true} {
		t.Run(fmt.Sprint(gzip), func(t *testing.T) {
			var counts [3]atomic.Int64
			var acceptB atomic.Bool
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, err := io.ReadAll(r.Body)
				if err != nil || !bytes.Equal(b, producerEnvelope().Payload) {
					t.Errorf("changed request: %q %v", b, err)
					w.WriteHeader(400)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/a":
					counts[0].Add(1)
					w.Write([]byte(`{}`))
				case "/b":
					counts[1].Add(1)
					if !acceptB.Load() {
						w.WriteHeader(503)
					}
					w.Write([]byte(`{}`))
				case "/c":
					counts[2].Add(1)
					w.Write([]byte(`{"partialSuccess":{"rejectedDataPoints":"1"}}`))
				default:
					t.Errorf("unexpected destination %q", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer peer.Close()
			state, wal := t.TempDir(), t.TempDir()
			producerKillCheckpoint(t, producerChild(t, state, wal, peer.URL, "first", gzip))
			acceptB.Store(true)
			producerKillCheckpoint(t, producerChild(t, state, wal, peer.URL, "second", gzip))
			if out, err := producerChild(t, state, wal, peer.URL, "third", gzip).CombinedOutput(); err != nil {
				t.Fatalf("third process: %v %s", err, out)
			}
			if counts[0].Load() != 1 || counts[1].Load() != 2 || counts[2].Load() != 1 {
				t.Fatalf("resolved peer resent across crashes: %d/%d/%d", counts[0].Load(), counts[1].Load(), counts[2].Load())
			}
		})
	}
}
