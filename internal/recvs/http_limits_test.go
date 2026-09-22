package recvs

import (
	"github.com/gin-gonic/gin"
	"gofluentd/library"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type regressionCountingBody struct {
	io.Reader
	n int
}

func (b *regressionCountingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.n += n
	return n, err
}
func (*regressionCountingBody) Close() error { return nil }
func TestRegressionHTTPBodyLimits(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown_length", true: "known_length"}[known], func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = gin.Params{{Key: "env", Value: "sit"}}
			body := &regressionCountingBody{Reader: strings.NewReader(strings.Repeat("!", 1024))}
			c.Request = httptest.NewRequest(http.MethodPost, "/logs/sit", nil)
			c.Request.Body = body
			c.Request.ContentLength = -1
			if known {
				c.Request.ContentLength = 1024
			}
			r := &HTTPRecv{BaseRecv: &BaseRecv{}, HTTPRecvCfg: &HTTPRecvCfg{MaxBodySize: 32}}
			r.SetMsgPool(&sync.Pool{New: func() interface{} { return &library.FluentMsg{} }})
			r.HTTPLogHandler(c)
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("status=%d, want 413", w.Code)
			}
			if body.n > 33 {
				t.Errorf("read %d bytes, limit must stop after at most 33", body.n)
			}
		})
	}
}
func TestRegressionBadRequestKeepsLiteralPercent(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	r := &HTTPRecv{}
	want := "100% failed: %s"
	r.BadRequest(c, want)
	if got := c.Errors.Last().Error(); got != want {
		t.Errorf("error=%q, want %q", got, want)
	}
}
