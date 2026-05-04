package examples

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo/sip"
)

func SetupLogger() {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(os.Getenv("LOG_LEVEL"))); err != nil {
		lvl = slog.LevelInfo
	}

	h := NewConsoleHandler(os.Stdout, lvl)
	slog.SetDefault(slog.New(h))

	slog.SetLogLoggerLevel(lvl)
	media.RTPDebug = os.Getenv("RTP_DEBUG") == "true"
	media.RTCPDebug = os.Getenv("RTCP_DEBUG") == "true"
	sip.SIPDebug = os.Getenv("SIP_DEBUG") == "true"
	sip.TransactionFSMDebug = os.Getenv("SIP_TRANSACTION_DEBUG") == "true"
	media.DTLSDebug = os.Getenv("DTLS_DEBUG") == "true"
}

// ConsoleHandler is slog handler that formats logs as:
//
//	caller > msg key=val key=val
//
// If no caller attribute present, just outputs msg with attrs.
type ConsoleHandler struct {
	out   io.Writer
	level slog.Level
	mu    sync.Mutex

	// preformatted attrs from WithAttrs/WithGroup
	prefix string // caller value
	attrs  []slog.Attr
	groups []string
}

func NewConsoleHandler(out io.Writer, level slog.Level) *ConsoleHandler {
	return &ConsoleHandler{
		out:   out,
		level: level,
	}
}

func (h *ConsoleHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *ConsoleHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder

	// Timestamp
	if !r.Time.IsZero() {
		b.WriteString(r.Time.Format("15:04:05.000000"))
		b.WriteByte(' ')
	}

	// Level
	b.WriteString(r.Level.String())
	b.WriteByte(' ')

	// Prefix from caller
	if h.prefix != "" {
		b.WriteString(h.prefix)
		b.WriteString(" > ")
	}

	// Message
	b.WriteString(r.Message)

	// Pre-formatted attrs (from WithAttrs)
	for _, a := range h.attrs {
		writeAttr(&b, h.groups, a)
	}

	// Record attrs, skip "caller" as it becomes prefix
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "caller" {
			return true // skip, already used as prefix
		}
		writeAttr(&b, h.groups, a)
		return true
	})

	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, b.String())
	return err
}

func (h *ConsoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newH := h.clone()
	for _, a := range attrs {
		if a.Key == "caller" {
			newH.prefix = a.Value.String()
			continue
		}
		newH.attrs = append(newH.attrs, a)
	}
	return newH
}

func (h *ConsoleHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	newH := h.clone()
	newH.groups = append(newH.groups, name)
	return newH
}

func (h *ConsoleHandler) clone() *ConsoleHandler {
	return &ConsoleHandler{
		out:    h.out,
		level:  h.level,
		prefix: h.prefix,
		attrs:  append([]slog.Attr{}, h.attrs...),
		groups: append([]string{}, h.groups...),
	}
}

func writeAttr(b *strings.Builder, groups []string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}

	b.WriteByte(' ')
	for _, g := range groups {
		b.WriteString(g)
		b.WriteByte('.')
	}

	if a.Value.Kind() == slog.KindGroup {
		for _, ga := range a.Value.Group() {
			writeAttr(b, append(groups, a.Key), ga)
		}
		return
	}

	fmt.Fprintf(b, "%s=%s", a.Key, a.Value.String())
}
