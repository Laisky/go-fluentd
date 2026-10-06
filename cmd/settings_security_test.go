package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gofluentd/internal/global"
)

func TestRegressionStartupDoesNotPrintSecrets(t *testing.T) {
	if mode := os.Getenv("GOFLUENTD_SECRET_TEST_CHILD"); mode != "" {
		global.Config.CMDArgs.ConfigPath = os.Getenv("GOFLUENTD_SECRET_TEST_CONFIG")
		global.Config.CMDArgs.LogLevel = "info"
		global.Config.CMDArgs.Debug = mode == "debug"
		setupSettings()
		// Prove that the configuration was loaded, not silently ignored to pass.
		encoded, err := json.Marshal(global.Config)
		if err != nil {
			t.Fatal("cannot inspect loaded configuration")
		}
		for _, secret := range []string{"sentinel-push-token", "sentinel-bearer-token", "sentinel-nested-secret", "sentinel-array-secret"} {
			if !strings.Contains(string(encoded), secret) {
				t.Fatal("secret fixture was not loaded")
			}
		}
		return
	}
	config := `settings:
  logger:
    push_token: sentinel-push-token
  acceptor:
    recvs:
      plugins:
        custom:
          bearer_token: sentinel-bearer-token
          arbitrary_unknown_key:
            nested: sentinel-nested-secret
            list: [sentinel-array-secret]
`
	file := filepath.Join(t.TempDir(), "settings.yml")
	if err := os.WriteFile(file, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"info", "debug"} {
		t.Run(mode, func(t *testing.T) {
			child := exec.Command(exe, "-test.run=^TestRegressionStartupDoesNotPrintSecrets$")
			child.Env = append(os.Environ(), "GOFLUENTD_SECRET_TEST_CHILD="+mode, "GOFLUENTD_SECRET_TEST_CONFIG="+file)
			output, err := child.CombinedOutput()
			if err != nil {
				t.Fatalf("startup subprocess failed: %v\n%s", err, output)
			}
			for _, secret := range []string{"sentinel-push-token", "sentinel-bearer-token", "sentinel-nested-secret", "sentinel-array-secret"} {
				if strings.Contains(string(output), secret) {
					t.Errorf("%s startup leaked %s", mode, secret)
				}
			}
			if !strings.Contains(string(output), "success load configuration") {
				t.Fatal("missing non-sensitive startup diagnostic")
			}
		})
	}
}
