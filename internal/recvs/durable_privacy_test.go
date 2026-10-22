package recvs

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/IBM/sarama"
	utils "github.com/Laisky/go-utils"
	"gofluentd/library"
)

// TestDurableReceiverRejectedInputDiagnostics ensures rejected source bytes never enter diagnostics.
func TestDurableReceiverRejectedInputDiagnostics(t *testing.T) {
	if os.Getenv("DURABLE_RECEIVER_PRIVACY_CHILD") == "1" {
		kafka := upgradeKafkaRecv()
		kafka.IsJSONFormat = true
		kafka.SetDurableAdmission(true)
		kafka.SetSyncOutChan(make(chan *library.FluentMsg, 1))
		if kafka.forwardKafkaRecord(context.Background(), &sarama.ConsumerMessage{Value: []byte(`{"x":secret7}`)}) {
			t.Fatal("invalid Kafka input accepted")
		}
		_, engine, _ := componentHTTP(t)
		for _, tc := range []struct {
			path string
			body map[string]interface{}
		}{
			{"/logs/env-secret7", signedHTTP(utils.Clock.GetUTCNow().Format(time.RFC3339))},
			{"/logs/sit", map[string]interface{}{"ts": "timestamp-secret7", "sig": "irrelevant"}},
			{"/logs/sit", map[string]interface{}{"ts": utils.Clock.GetUTCNow().Format(time.RFC3339), "sig": "signature-secret7"}},
		} {
			body, err := json.Marshal(tc.body)
			durableCheck.NoError(t, err)
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest("POST", tc.path, bytes.NewReader(body)))
			durableCheck.Equal(t, http.StatusBadRequest, w.Code)
		}
		t.Log("PRIVACY_GATE_EXERCISED")
		return
	}
	exe, err := os.Executable()
	durableCheck.NoError(t, err)
	child := exec.Command(exe, "-test.run=^TestDurableReceiverRejectedInputDiagnostics$", "-test.v", "-test.timeout=10s")
	child.Env = append(os.Environ(), "DURABLE_RECEIVER_PRIVACY_CHILD=1")
	output, err := child.CombinedOutput()
	durableCheck.NoError(t, err)
	for _, canary := range []string{"secret7", "env-secret7", "signature-secret7", "timestamp-secret7"} {
		if strings.Contains(string(output), canary) {
			t.Fatalf("rejected source appeared in diagnostics: %q", canary)
		}
	}
	if !strings.Contains(string(output), "PRIVACY_GATE_EXERCISED") {
		t.Fatal("child did not exercise rejection paths")
	}
}
