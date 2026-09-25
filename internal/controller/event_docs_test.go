package controller

import (
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"gofluentd/internal/recvs"
	"gofluentd/internal/senders"
)

// Parse the operator's actual YAML, not a separately maintained config fixture.
func TestComponentEventDocumentedConfiguration(t *testing.T) {
	if !componentSubprocess(t) {
		return
	}
	data, err := os.ReadFile("../../docs/settings/http-events.yml")
	if err != nil {
		t.Fatal(err)
	}
	viper.Reset()
	viper.SetConfigType("yaml")
	if err := viper.ReadConfig(strings.NewReader(string(data))); err != nil {
		t.Fatal(err)
	}
	server = gin.New()
	c := NewControllor()
	rs, ss := c.initRecvs("prod"), c.initSenders("prod")
	if len(rs) != 1 || len(ss) != 1 {
		t.Fatal("documented plugins were not registered")
	}
	if _, ok := rs[0].(*recvs.HTTPEventsRecv); !ok {
		t.Fatal("wrong receiver")
	}
	if _, ok := ss[0].(*senders.HTTPEventsSender); !ok {
		t.Fatal("wrong sender")
	}
	if !ss[0].IsTagSupported("events.prod") || ss[0].DiscardWhenBlocked() {
		t.Fatal("documented event routing is not reliable")
	}
}
