package alerts

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// maxMessageRunes and maxMessageLines keep a rendered notification below
	// Telegram's 4096 UTF-16 unit limit, including line prefixes and metadata.
	maxMessageRunes = 1400
	maxMessageLines = 64
	// messageLinePrefix marks every line of untrusted text. Metadata comes first
	// and no message line can start without it, so a body cannot pose as a field.
	messageLinePrefix = "| "
)

var (
	secretPattern       = regexp.MustCompile(`(?i)((?:password|passwd|token|secret|api[_-]?key|authorization)["']?\s*[:=]\s*["']?)(?:bearer\s+)?[^\s,;"'}]+`)
	quotedSecretPattern = regexp.MustCompile(`(?i)((?:password|passwd|token|secret|api[_-]?key|authorization)["']?\s*[:=]\s*")[^"\r\n]*"`)
	bearerPattern       = regexp.MustCompile(`(?i)\bbearer\s+[^\s,;"'}]+`)
	botTokenPattern     = regexp.MustCompile(`\b[0-9]{4,}:[A-Za-z0-9_-]{15,}\b`)
	urlPattern          = regexp.MustCompile(`(?i)https?://[^\s]+`)
)

func clean(s string, limit int) string {
	if s == "" {
		return ""
	}
	// All supported key/value, URL and bot-token patterns contain ':' or '=';
	// bearer tokens are checked separately. Avoid regex replacement and allocation
	// for the common bounded metadata and message with no such marker.
	if len(s) <= limit && utf8.ValidString(s) && !strings.ContainsAny(s, ":=") && !bearerPattern.MatchString(s) {
		return s
	}
	s = strings.ToValidUTF8(s, "�")
	s = urlPattern.ReplaceAllString(s, "[url redacted]")
	s = bearerPattern.ReplaceAllString(s, "Bearer [redacted]")
	s = quotedSecretPattern.ReplaceAllString(s, `${1}[redacted]"`)
	s = secretPattern.ReplaceAllString(s, "$1[redacted]")
	s = botTokenPattern.ReplaceAllString(s, "[redacted]")
	r := []rune(s)
	if len(r) > limit {
		s = string(r[:limit]) + "…"
	}
	return s
}

// cleanMetadata prevents producer-controlled metadata from creating lines or
// terminal formatting controls in the notification. Bound before processing.
func cleanMetadata(s string, limit int) string {
	if len(s) > 512 {
		s = "[oversized metadata omitted]"
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, s)
	return clean(s, limit)
}

// notificationText renders one queued event as plain text.
func notificationText(e Event, includeMessage bool) string {
	var b strings.Builder
	if e.summary {
		b.WriteString("Error log summary\nsource: " + e.Source)
	} else {
		b.WriteString("Error log\nsource: " + e.Source + "\nlevel: " + e.Severity)
	}
	if !e.Timestamp.IsZero() {
		b.WriteString("\nevent_time: " + e.Timestamp.UTC().Format(time.RFC3339Nano))
	} else if !e.ObservedAt.IsZero() {
		b.WriteString("\nobserved_at: " + e.ObservedAt.UTC().Format(time.RFC3339Nano))
	}
	if e.OriginalSeverity != "" {
		b.WriteString("\noriginal severity: " + e.OriginalSeverity)
	}
	if e.Fingerprint != "" {
		b.WriteString("\nfingerprint: " + e.Fingerprint)
	}
	if e.Suppressed != 0 {
		b.WriteString("\nsuppressed_since_last: " + strconv.FormatUint(e.Suppressed, 10))
	}
	if includeMessage && e.Message != "" {
		writeMessageBlock(&b, e.Message)
	}
	return b.String()
}

// writeMessageBlock keeps legitimate multiline stack traces while making every
// line visibly subordinate to the trusted metadata above it.
func writeMessageBlock(b *strings.Builder, message string) {
	lines := strings.Split(strings.TrimRight(flattenMessage(message), "\n"), "\n")
	b.WriteString("\nmessage:")
	for i, line := range lines {
		if i == maxMessageLines {
			b.WriteString("\n" + messageLinePrefix + "… " + strconv.Itoa(len(lines)-i) + " more lines omitted")
			return
		}
		b.WriteString("\n" + messageLinePrefix + line)
	}
}

// flattenMessage folds every Unicode line break to '\n', so each one receives a
// prefix, and blanks formatting controls such as bidirectional overrides.
func flattenMessage(message string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n', r == '\t':
			return r
		case r == '\r', r == '\v', r == '\f', r == '\u0085', r == '\u2028', r == '\u2029':
			return '\n'
		case unicode.IsControl(r), unicode.In(r, unicode.Cf):
			return ' '
		}
		return r
	}, strings.ReplaceAll(message, "\r\n", "\n"))
}
