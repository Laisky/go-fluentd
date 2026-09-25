package recvs

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gofluentd/library/streamformat"

	"github.com/gin-gonic/gin"
)

// HTTPEventsRecvCfg configures a dedicated CloudEvents or NDJSON endpoint.
// Unlike the legacy signed-log receiver, it preserves the complete envelope and
// always waits for local durable acceptance. HTTPS/authentication may also be
// supplied by the shared server's middleware or a trusted reverse proxy.
type HTTPEventsRecvCfg struct {
	HTTPSrv     *gin.Engine
	Name        string
	Path        string
	Tag         string
	Format      string
	BearerToken string
	MaxBodySize int64
	MaxRecords  int
	AckTimeout  time.Duration
}

// HTTPEventsRecv publishes validated events to the existing durable pipeline.
// A successful HTTP response is not a downstream delivery acknowledgement.
type HTTPEventsRecv struct {
	BaseRecv
	cfg HTTPEventsRecvCfg
}

// NewHTTPEventsRecv validates configuration before registering a POST route.
// Zero limits select defaults; negative limits are configuration errors. A
// receiver owns a literal route (no parameters/wildcards) and a fixed routing tag.
func NewHTTPEventsRecv(cfg HTTPEventsRecvCfg) (*HTTPEventsRecv, error) {
	if cfg.HTTPSrv == nil || strings.TrimSpace(cfg.Name) == "" || strings.TrimSpace(cfg.Tag) == "" {
		return nil, fmt.Errorf("HTTP events requires a server, name and tag")
	}
	if !strings.HasPrefix(cfg.Path, "/") || strings.ContainsAny(cfg.Path, ":*?#\r\n") {
		return nil, fmt.Errorf("HTTP events requires a literal absolute route")
	}
	if !streamformat.ValidFormat(cfg.Format) {
		return nil, fmt.Errorf("unsupported HTTP events format %q", cfg.Format)
	}
	if cfg.MaxBodySize < 0 || cfg.MaxRecords < 0 || cfg.AckTimeout < 0 {
		return nil, fmt.Errorf("HTTP events limits must not be negative")
	}
	if cfg.BearerToken != "" && strings.ContainsAny(cfg.BearerToken, " \t\r\n") {
		return nil, fmt.Errorf("HTTP events bearer token must not contain whitespace")
	}
	if cfg.MaxBodySize == 0 {
		cfg.MaxBodySize = 4 << 20
	}
	if cfg.MaxRecords == 0 {
		cfg.MaxRecords = 1024
	}
	if cfg.AckTimeout == 0 {
		cfg.AckTimeout = 30 * time.Second
	}
	for _, route := range cfg.HTTPSrv.Routes() {
		if route.Method == http.MethodPost && route.Path == cfg.Path {
			return nil, fmt.Errorf("HTTP events POST route already registered")
		}
	}
	r := &HTTPEventsRecv{cfg: cfg}
	cfg.HTTPSrv.POST(cfg.Path, r.handle)
	return r, nil
}

func (r *HTTPEventsRecv) GetName() string { return r.cfg.Name }

// Run satisfies AcceptorRecvItf; the shared HTTP server owns serving/shutdown.
func (r *HTTPEventsRecv) Run(context.Context) {}

func (r *HTTPEventsRecv) handle(c *gin.Context) {
	if r.cfg.BearerToken != "" {
		values := c.Request.Header.Values("Authorization")
		valid := false
		if len(values) == 1 {
			scheme, token, ok := strings.Cut(values[0], " ")
			valid = ok && strings.EqualFold(scheme, "Bearer") &&
				subtle.ConstantTimeCompare([]byte(token), []byte(r.cfg.BearerToken)) == 1
		}
		if !valid {
			c.Header("WWW-Authenticate", "Bearer")
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
	}
	// Deliberately do not decompress in this increment: otherwise a wire-size
	// limit alone would not bound decoded memory. Reject, never misinterpret.
	encodings := c.Request.Header.Values("Content-Encoding")
	if len(encodings) > 1 || len(encodings) == 1 && !strings.EqualFold(strings.TrimSpace(encodings[0]), "identity") {
		c.AbortWithStatus(http.StatusUnsupportedMediaType)
		return
	}
	contentTypes := c.Request.Header.Values("Content-Type")
	if len(contentTypes) > 1 {
		c.AbortWithStatus(http.StatusUnsupportedMediaType)
		return
	}
	ct := c.GetHeader("Content-Type")
	media, err := streamformat.MediaType(ct)
	if err != nil && !(r.cfg.Format == streamformat.CloudEvents && ct == "") ||
		r.cfg.Format == streamformat.NDJSON && media != "application/x-ndjson" {
		c.AbortWithStatus(http.StatusUnsupportedMediaType)
		return
	}
	if c.Request.ContentLength > r.cfg.MaxBodySize {
		c.AbortWithStatus(http.StatusRequestEntityTooLarge)
		return
	}
	bodyReader := http.MaxBytesReader(c.Writer, c.Request.Body, r.cfg.MaxBodySize)
	defer bodyReader.Close()
	body, err := readEventBody(bodyReader, c.Request.ContentLength)
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
		} else {
			c.AbortWithStatus(http.StatusBadRequest)
		}
		return
	}
	// No allocations from the message pool, IDs or partial publication before
	// the entire request passes wire-format and record-count validation.
	records, err := streamformat.Decode(r.cfg.Format, ct, c.Request.Header, body, r.cfg.MaxRecords)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), r.cfg.AckTimeout)
	defer cancel()
	if ctx.Err() != nil || r.msgPool == nil || r.counter == nil || r.syncOutChan == nil {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	receipts := make([]chan error, 0, len(records))
	for _, record := range records {
		if ctx.Err() != nil {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		msg := r.newMsg()
		msg.ID, msg.Tag, msg.Message = r.counter.Count(), r.cfg.Tag, record
		msg.SourceFormat = r.cfg.Format
		if msg.ID < 0 {
			r.msgPool.Put(msg)
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		receipt := make(chan error, 1)
		msg.DurableAck = receipt
		select {
		case r.syncOutChan <- msg:
			// The pipeline now owns msg and may immediately recycle it. Keep
			// only the independent receipt, never read or recycle msg again.
			receipts = append(receipts, receipt)
		case <-ctx.Done():
			r.msgPool.Put(msg)
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
	}
	for _, receipt := range receipts {
		select {
		case err, ok := <-receipt:
			if !ok || err != nil {
				c.AbortWithStatus(http.StatusServiceUnavailable)
				return
			}
		case <-ctx.Done():
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
	}
	c.Status(http.StatusNoContent)
}
