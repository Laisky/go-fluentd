//go:build linux

package otlpstate_test

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
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"gofluentd/internal/otlpstate"
)

// This exercises the disposition guard as an independent consumer process, not
// the still-unimplemented OTLP plugin. The peer's raw response is retained. Its
// classification is supplied explicitly; otlpwire separately tests that parser.
func TestProcessHelper(t *testing.T) {
	mode := os.Getenv("OTLP_STATE_CHILD")
	if mode == "" {
		return
	}
	s, err := otlpstate.Open(os.Getenv("OTLP_STATE_DIR"), otlpstate.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := envelope()
	e.Signal = os.Getenv("OTLP_STATE_SIGNAL")
	e.Payload = []byte("{\"unknownFutureSignalField\":\"18446744073709551615\"}")
	kind := otlpstate.Kind(os.Getenv("OTLP_STATE_KIND"))
	var limit syscall.Rlimit
	if mode == "file-fault" {
		if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
			t.Fatal(err)
		}
		low := limit
		low.Cur = 96
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &low); err != nil {
			t.Fatal(err)
		}
	}
	send := func(ctx context.Context, copy otlpstate.Envelope) (otlpstate.Outcome, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, os.Getenv("OTLP_STATE_PEER"), bytes.NewReader(copy.Payload))
		if err != nil {
			return otlpstate.Outcome{}, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return otlpstate.Outcome{}, err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if err != nil {
			return otlpstate.Outcome{}, err
		}
		o := terminal(kind)
		o.Response = raw
		o.HTTPStatus = resp.StatusCode
		o.ResponseContentType = resp.Header.Get("Content-Type")
		return o, nil
	}
	r, err := s.Do(context.Background(), key(), e, send)
	switch mode {
	case "record":
		if err != nil || !r.Quarantined || r.Replayed {
			t.Fatalf("record: %+v %v", r, err)
		}
		fmt.Println("DURABLE")
	case "replay":
		if err != nil || !r.Quarantined || !r.Replayed || r.Outcome.Kind != kind || !bytes.Equal(r.Outcome.Response, terminal(kind).Response) {
			t.Fatalf("replay: %+v %v", r, err)
		}
		return
	case "file-fault":
		if restore := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit); restore != nil {
			t.Fatal(restore)
		}
		if !errors.Is(err, syscall.EFBIG) || !errors.Is(err, otlpstate.ErrUncertain) || r.Quarantined {
			t.Fatalf("no actual kernel write failure: %+v %v", r, err)
		}
		if _, err = s.Do(context.Background(), key(), e, send); !errors.Is(err, otlpstate.ErrUncertain) {
			t.Fatalf("repair caused resend: %v", err)
		}
		fmt.Println("UNCERTAIN")
	case "uncertain-reopen":
		if !errors.Is(err, otlpstate.ErrUncertain) || r.Quarantined {
			t.Fatalf("orphan evidence was replayed: %+v %v", r, err)
		}
		return
	default:
		t.Fatal("bad child mode")
	}
	for {
		time.Sleep(time.Hour)
	} // parent must SIGKILL, never graceful cleanup
}

func childCommand(t *testing.T, dir, peer, signal string, kind otlpstate.Kind, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessHelper$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "OTLP_STATE_CHILD="+mode, "OTLP_STATE_DIR="+dir, "OTLP_STATE_PEER="+peer, "OTLP_STATE_SIGNAL="+signal, "OTLP_STATE_KIND="+string(kind), "GORACE=halt_on_error=1 exitcode=66")
	return cmd
}
func killAfter(t *testing.T, cmd *exec.Cmd, marker string) {
	t.Helper()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	ready := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if sc.Text() == marker {
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
			t.Fatalf("child missing %s: %v %s", marker, err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("child timeout: %s", stderr.String())
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected killed child: %v", err)
	}
	status, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || status.Signal() != syscall.SIGKILL {
		t.Fatalf("not SIGKILL: %v", err)
	}
	if strings.Contains(stderr.String(), "DATA RACE") {
		t.Fatal(stderr.String())
	}
}

func TestProcessTerminalResponseSurvivesSIGKILL(t *testing.T) {
	for _, signal := range []string{"logs", "metrics", "traces"} {
		for _, kind := range []otlpstate.Kind{otlpstate.Partial, otlpstate.Permanent, otlpstate.Invalid} {
			t.Run(signal+"/"+string(kind), func(t *testing.T) {
				dir := t.TempDir()
				var calls atomic.Int64
				peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					payload, err := io.ReadAll(r.Body)
					if err != nil || !bytes.Equal(payload, []byte(`{"unknownFutureSignalField":"18446744073709551615"}`)) {
						t.Errorf("request changed: %q %v", payload, err)
					}
					o := terminal(kind)
					w.Header().Set("Content-Type", o.ResponseContentType)
					w.WriteHeader(o.HTTPStatus)
					w.Write(o.Response)
				}))
				defer peer.Close()
				killAfter(t, childCommand(t, dir, peer.URL, signal, kind, "record"), "DURABLE")
				if out, err := childCommand(t, dir, peer.URL, signal, kind, "replay").CombinedOutput(); err != nil {
					t.Fatalf("reopen: %s %v", out, err)
				}
				if calls.Load() != 1 {
					t.Fatalf("terminal peer called again after SIGKILL: %d", calls.Load())
				}
				onlyReceipt(t, dir)
			})
		}
	}
}

func TestProcessPartialDiskWriteBlocksReplayAfterSIGKILL(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int64
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		o := terminal(otlpstate.Partial)
		w.Header().Set("Content-Type", o.ResponseContentType)
		w.Write(o.Response)
	}))
	defer peer.Close()
	killAfter(t, childCommand(t, dir, peer.URL, "metrics", otlpstate.Partial, "file-fault"), "UNCERTAIN")
	pending, err := filepath.Glob(filepath.Join(dir, ".pending-*"))
	if err != nil || len(pending) != 1 {
		t.Fatalf("missing partial evidence: %v %v", pending, err)
	}
	st, err := os.Stat(pending[0])
	if err != nil || st.Size() != 96 {
		t.Fatalf("fault did not write the expected prefix: %v %v", st, err)
	}
	if out, err := childCommand(t, dir, peer.URL, "metrics", otlpstate.Partial, "uncertain-reopen").CombinedOutput(); err != nil {
		t.Fatalf("reopen: %s %v", out, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("uncertain receipt retried: %d", calls.Load())
	}
}

func TestProcessDirectoryOwnership(t *testing.T) {
	dir := t.TempDir()
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write(terminal(otlpstate.Permanent).Response)
	}))
	defer peer.Close()
	cmd := childCommand(t, dir, peer.URL, "logs", otlpstate.Permanent, "record")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	sc := bufio.NewScanner(stdout)
	if !sc.Scan() || sc.Text() != "DURABLE" {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("owner did not become ready")
	}
	if second, err := otlpstate.Open(dir, otlpstate.DefaultLimits()); err == nil {
		second.Close()
		t.Fatal("second process owner admitted")
	}
	cmd.Process.Kill()
	cmd.Wait()
	s := open(t, dir)
	s.Close()
}
