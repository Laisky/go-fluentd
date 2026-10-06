package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	gutils "github.com/Laisky/go-utils"
	"gofluentd/internal/global"
)

func TestRegressionRemoteSettingsDoNotLogSecrets(t *testing.T) {
	if os.Getenv("GOFLUENTD_REMOTE_SECRET_CHILD") != "" {
		if err := gutils.Logger.ChangeLevel("debug"); err != nil {
			t.Fatal(err)
		}
		requests := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			user, pass, ok := r.BasicAuth()
			if !ok || user != "sentinel-user" || pass != "sentinel-pass" || r.URL.Query().Get("token") != "sentinel-query" || r.URL.Path != "/base/app/test/main" {
				t.Error("request credentials or selectors were modified")
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"propertySources":[{"name":"test","source":{"settings.logger.push_token":"sentinel-remote-config","settings.acceptor.recvs.plugins.custom.nested":["sentinel-nested-config"]}}]}`)
		}))
		defer srv.Close()
		u, _ := url.Parse(srv.URL + "/base?token=sentinel-query")
		u.User = url.UserPassword("sentinel-user", "sentinel-pass")
		global.Config.CMDArgs.ConfigPath = "/nonexistent-config-test/settings.yml"
		global.Config.CMDArgs.ConfigServer = u.String()
		global.Config.CMDArgs.ConfigServerAppname = "app"
		global.Config.CMDArgs.ConfigServerProfile = "test"
		global.Config.CMDArgs.ConfigServerLabel = "main"
		global.Config.CMDArgs.ConfigServerKey = "sentinel-config-key"
		global.Config.CMDArgs.LogLevel = "debug"
		setupSettings()
		if requests != 1 || global.Config.Settings.Logger.PushToken != "sentinel-remote-config" {
			t.Fatal("remote settings not loaded")
		}
		encoded, _ := json.Marshal(global.Config)
		if !strings.Contains(string(encoded), "sentinel-nested-config") {
			t.Fatal("remote plugin fixture not loaded")
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(exe, "-test.run=^TestRegressionRemoteSettingsDoNotLogSecrets$")
	child.Env = append(os.Environ(), "GOFLUENTD_REMOTE_SECRET_CHILD=1")
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("remote startup failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "sentinel-") {
		t.Fatal("remote startup exposed a secret")
	}
	if !strings.Contains(string(output), "load settings from remote") {
		t.Fatal("missing useful remote diagnostic")
	}
}

func TestRegressionRemoteSettingsFailureAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"denied", "sentinel-response", 403},
		{"invalid-json", `{"sentinel-response":`, 200},
		{"null-source", `{"propertySources":[null]}`, 200},
		{"oversized", strings.Repeat(" ", maxRemoteSettingsBytes) + `"sentinel-response"`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			err := loadRemoteSettings(context.Background(), srv.URL+"?token=sentinel-query", "app", "test", "main")
			if err == nil || strings.Contains(fmt.Sprintf("%+v", err), "sentinel-") {
				t.Fatalf("missing or unsafe error: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := loadRemoteSettings(ctx, "http://user:secret@127.0.0.1:1?token=secret", "app", "test", "main")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation")
	}
	if err := loadRemoteSettings(ctx, "http://secret@host/%zz", "app", "test", "main"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("invalid URL leaked")
	}
}

func TestRegressionRemoteSettingsDoesNotFollowRedirects(t *testing.T) {
	destinationRequests := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationRequests++ }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer source.Close()
	if err := loadRemoteSettings(context.Background(), source.URL, "app", "test", "main"); err == nil {
		t.Fatal("redirect accepted")
	}
	if destinationRequests != 0 {
		t.Fatal("followed config redirect")
	}
}
