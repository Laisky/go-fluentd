package alerts

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

const (
	// maxSources bounds the allowlists, and therefore the source budget table.
	maxSources = 128
	// maxValueBytes bounds any single record value before per-event processing.
	maxValueBytes = 8192
	// maxOTLPRequestBytes bounds alert-side memory and decoding work per request.
	maxOTLPRequestBytes = 1 << 20
)

var (
	errInvalidConfiguration = errors.New("invalid Telegram alert configuration")
	errInvalidAllowlist     = errors.New("invalid Telegram alert source allowlist")
	errInvalidPolicy        = errors.New("invalid Telegram alert policy limits")
	tokenPattern            = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
)

// Config selects sources and bounds the notification policy. Zero policy values
// take the documented defaults; other out-of-range values fail validation.
type Config struct {
	Token  string
	ChatID string

	IncludeMessage bool
	Tags           []string
	Services       []string

	EventQueueSize int
	OTLPQueueSize  int
	DedupEntries   int
	MaxAttempts    int
	// MaxGroupsPerSource bounds how many error kinds one selected source can notify
	// per DedupWindow. One restores a single notification per source and window.
	MaxGroupsPerSource int

	DedupWindow  time.Duration
	SendInterval time.Duration
}

// normalized is the single place that applies defaults and validates bounds, so
// the rest of the package can rely on every limit being set.
func (c Config) normalized() (Config, error) {
	if len(c.Token) > 256 || !tokenPattern.MatchString(c.Token) || strings.TrimSpace(c.ChatID) == "" || len(c.ChatID) > 256 || strings.ContainsAny(c.ChatID, "\r\n") {
		return c, errInvalidConfiguration
	}
	if len(c.Tags)+len(c.Services) > maxSources {
		return c, errInvalidAllowlist
	}
	for _, values := range [][]string{c.Tags, c.Services} {
		for _, v := range values {
			if strings.TrimSpace(v) == "" || len(v) > 256 {
				return c, errInvalidAllowlist
			}
		}
	}
	defaultInt(&c.EventQueueSize, 64)
	defaultInt(&c.OTLPQueueSize, 4)
	defaultInt(&c.DedupEntries, 1024)
	defaultInt(&c.MaxAttempts, 3)
	defaultInt(&c.MaxGroupsPerSource, 8)
	if c.DedupWindow == 0 {
		c.DedupWindow = time.Minute
	}
	if c.SendInterval == 0 {
		c.SendInterval = time.Second
	}
	if !inRange(c.EventQueueSize, 1, 4096) || !inRange(c.OTLPQueueSize, 1, 32) || !inRange(c.DedupEntries, 1, 16384) || !inRange(c.MaxAttempts, 1, 5) || !inRange(c.MaxGroupsPerSource, 1, 1024) ||
		!inRange(c.DedupWindow, time.Second, time.Hour) || !inRange(c.SendInterval, 100*time.Millisecond, time.Minute) {
		return c, errInvalidPolicy
	}
	c.Tags = append([]string(nil), c.Tags...)
	c.Services = append([]string(nil), c.Services...)
	return c, nil
}

func defaultInt(value *int, fallback int) {
	if *value == 0 {
		*value = fallback
	}
}

func inRange[T int | time.Duration](value, low, high T) bool {
	return value >= low && value <= high
}
