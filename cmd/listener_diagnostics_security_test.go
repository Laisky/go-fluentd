package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"gofluentd/library/log"
)

// TestRegressionStartupKeepsUsefulListenerDetails runs the ordinary command and
// real listeners. No fixture field is logged merely to satisfy the assertions.
func TestRegressionStartupKeepsUsefulListenerDetails(t *testing.T) {
	const childKey = "GOFLUENTD_LISTENER_PRIVACY_CHILD"
	if mode := os.Getenv(childKey); mode != "" {
		runListenerPrivacyChild(t, mode)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"listen", "error"} {
		for _, level := range []string{"info", "debug"} {
			t.Run(scenario+"/"+level, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				child := exec.CommandContext(ctx, exe, "-test.run=^TestRegressionStartupKeepsUsefulListenerDetails$")
				child.Env = append(os.Environ(), childKey+"="+scenario+":"+level)
				out, err := child.CombinedOutput()
				if err != nil {
					t.Fatalf("startup control failed: %v\n%s", err, out)
				}
				if strings.Contains(string(out), "private-startup-") {
					t.Error("startup exposed a synthetic configuration value")
				}
				messages := map[string]map[string]any{}
				for _, line := range strings.Split(string(out), "\n") {
					var event map[string]any
					if json.Unmarshal([]byte(line), &event) != nil {
						// The ordinary logger uses console fields; read its structured JSON tail.
						index := strings.LastIndex(line, "\t{")
						if index < 0 || json.Unmarshal([]byte(line[index+1:]), &event) != nil {
							continue
						}
						columns := strings.Split(line[:index], "\t")
						event["msg"] = columns[len(columns)-1]
					}
					if event != nil {
						if msg, ok := event["msg"].(string); ok {
							messages[msg] = event
						}
					}
				}
				if messages["configuration loaded"]["receiver_plugins"] != float64(1) {
					t.Error("missing explicit configuration summary")
				}
				if scenario == "error" {
					if messages["listening on http"] != nil || messages["OTLP HTTP listener started"] != nil {
						t.Error("failed startup claimed listeners were active")
					}
					if !strings.Contains(string(out), "invalid settings.otlp") || !strings.Contains(string(out), "STARTUP_ERROR_CONTROL_PASSED") {
						t.Error("missing useful fixed failure diagnostic or decode control")
					}
					return
				}
				if !strings.Contains(string(out), "BOTH_LISTENERS_REACHED") {
					t.Error("actual listener requests did not pass")
				}
				for _, msg := range []string{"listening on http", "OTLP HTTP listener started"} {
					addr, ok := messages[msg]["addr"].(string)
					host, port, err := net.SplitHostPort(addr)
					expectedField := "monitor_addr"
					if msg == "OTLP HTTP listener started" {
						expectedField = "otlp_addr"
					}
					if !ok || err != nil || host != "127.0.0.1" || port == "" || port == "0" || messages["LISTENER_BINDINGS_VERIFIED"][expectedField] != addr {
						t.Errorf("missing useful bound address in %s", msg)
					}
				}
			})
		}
	}
}

func runListenerPrivacyChild(t *testing.T, mode string) {
	parts := strings.Split(mode, ":")
	if len(parts) != 2 {
		t.Fatal("invalid fixture mode")
	}
	scenario, level := parts[0], parts[1]
	reserve := func() string {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := listener.Addr().String()
		listener.Close()
		return addr
	}
	monitorAddr, otlpAddr := reserve(), reserve()
	for otlpAddr == monitorAddr {
		otlpAddr = reserve()
	}
	const token = "private-startup-token"
	const password = "private-startup-password"
	const marker = "private-startup-arbitrary-config-marker"
	const endpoint = "https://private-startup-user:private-startup-url-password@example.invalid:9443/ingest?token=private-startup-query#private-startup-fragment"
	t.Setenv("FIXTURE_OTLP_STARTUP_TOKEN", token)
	timeout := "30s"
	if scenario == "error" {
		timeout = "private-startup-invalid-duration"
	}
	privateDir := func() string {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	fixture := map[string]any{"settings": map[string]any{
		"logger":           map[string]any{"push_token": token},
		"journal":          map[string]any{"buf_dir_path": privateDir(), "buf_file_bytes": 1048576, "committed_id_sec": 1, "journal_out_chan_len": 16, "child_data_chan_len": 16, "child_id_chan_len": 16, "commit_id_chan_len": 16, "gc_inteval_sec": 3600},
		"otlp":             map[string]any{"enabled": true, "listen_addr": otlpAddr, "idle_timeout": timeout, "storage_dir": privateDir(), "storage_scan_timeout": "1s", "bearer_token_env": "FIXTURE_OTLP_STARTUP_TOKEN", "destinations": []any{map[string]any{"id": "fixture", "logs_endpoint": "http://127.0.0.1:1/v1/logs", "metrics_endpoint": "http://127.0.0.1:1/v1/metrics", "traces_endpoint": "http://127.0.0.1:1/v1/traces"}}},
		"acceptor":         map[string]any{"async_out_chan_size": 16, "sync_out_chan_size": 16, "recvs": map[string]any{"plugins": map[string]any{"fixture": map[string]any{"active_env": []string{"inactive"}, "password": password, "url": endpoint, "arbitrary": marker}}}},
		"acceptor_filters": map[string]any{"fork": 1, "out_buf_len": 16},
		"tag_filters":      map[string]any{"internal_chan_size": 16},
		"dispatcher":       map[string]any{"nfork": 1, "out_chan_size": 16},
		"post_filters":     map[string]any{"fork": 1, "out_chan_size": 16},
		"producer":         map[string]any{"forks": 1, "discard_chan_size": 16, "sender_inchan_size": 16},
	}}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "startup.json")
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reached := make(chan bool, 1)
	if scenario == "listen" {
		go func() {
			client := &http.Client{Timeout: 100 * time.Millisecond}
			check := func(addr, path string, want int) bool {
				method := http.MethodGet
				if want == 401 {
					method = http.MethodPost
				}
				req, _ := http.NewRequest(method, "http://"+addr+path, nil)
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					return false
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				return resp.StatusCode == want
			}
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					reached <- false
					return
				case <-ticker.C:
					if check(monitorAddr, "/health", 200) && check(otlpAddr, "/v1/logs", 401) {
						reached <- true
						cancel()
						return
					}
				}
			}
		}()
	}
	flags := []string{"--config=" + file, "--addr=" + monitorAddr, "--env=prod", "--log-level=" + level, "--heartbeat=3600"}
	if level == "debug" {
		flags = append(flags, "--debug")
	}
	rootCmd.SetArgs(flags)
	rootCmd.SetContext(ctx)
	err = rootCmd.Execute()
	// Prove secret-bearing values were loaded and left intact, rather than omitted
	// from the fixture or changed to make the no-leak assertions pass.
	for key, want := range map[string]string{"settings.logger.push_token": token, "settings.acceptor.recvs.plugins.fixture.password": password, "settings.acceptor.recvs.plugins.fixture.url": endpoint, "settings.acceptor.recvs.plugins.fixture.arbitrary": marker} {
		if gutils.Settings.GetString(key) != want {
			t.Fatal("fixture configuration was ignored or changed")
		}
	}
	if scenario == "error" {
		if err == nil {
			t.Fatal("invalid duration was accepted")
		}
		log.Logger.Error("start", zap.Error(err)) // Same boundary as main.go.
		fmt.Println("STARTUP_ERROR_CONTROL_PASSED")
		return
	}
	if err != nil {
		t.Fatal("ordinary startup failed")
	}
	if !<-reached {
		t.Fatal("real listeners did not respond")
	}
	proof, _ := json.Marshal(map[string]string{"msg": "LISTENER_BINDINGS_VERIFIED", "monitor_addr": monitorAddr, "otlp_addr": otlpAddr})
	fmt.Println(string(proof))
	fmt.Println("BOTH_LISTENERS_REACHED")
}
