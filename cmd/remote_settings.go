package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gofluentd/library/log"

	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

// The legacy helper logs both the raw URL and complete configuration response.
// Keep this startup boundary independent of its logging and bounded in memory.
const maxRemoteSettingsBytes = 4 << 20

func loadRemoteSettings(ctx context.Context, server, app, profile, label string) error {
	endpoint, err := url.Parse(server)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Opaque != "" {
		return log.SafeHTTPError("load configuration", server, fmt.Errorf("invalid endpoint"))
	}
	// Preserve base path, basic authentication and query on the actual request,
	// while each selector remains an escaped path component, not a URL fragment.
	path := strings.TrimRight(endpoint.EscapedPath(), "/")
	for _, part := range []string{app, profile, label} {
		path += "/" + url.PathEscape(part)
	}
	endpoint.Path, err = url.PathUnescape(path)
	if err != nil {
		return log.SafeHTTPError("load configuration", server, err)
	}
	endpoint.RawPath, endpoint.Fragment, endpoint.RawFragment = path, "", ""
	raw := endpoint.String()
	log.Logger.Info("load settings from remote", zap.String("endpoint", log.SafeURL(server)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return log.SafeHTTPError("load configuration", server, err)
	}
	transport := &http.Transport{MaxIdleConnsPerHost: 1}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		// Configuration credentials must not be forwarded to redirect destinations.
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		return log.SafeHTTPError("load configuration", server, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("configuration endpoint %s returned status %d", log.SafeURL(server), resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteSettingsBytes+1))
	if err != nil {
		return log.SafeHTTPError("read configuration", server, err)
	}
	if len(body) > maxRemoteSettingsBytes {
		return fmt.Errorf("configuration response exceeds byte limit")
	}
	var config gutils.Config
	if err = json.Unmarshal(body, &config); err != nil {
		return fmt.Errorf("invalid configuration response")
	}
	// Validate the complete envelope before mutating shared settings.
	for _, source := range config.Sources {
		if source == nil {
			return fmt.Errorf("invalid configuration source")
		}
	}
	for _, source := range config.Sources {
		for key, value := range source.Source {
			gutils.Settings.Set(key, value)
		}
	}
	return nil
}
