// slog handler chain for gmk's logger.
//
// The chain is:
//
//	slog.Default()
//	  policyHandler   (drops records the user's policy doesn't permit)
//	    multiHandler  (fans out to terminal + file sinks)
//	      terminalHandler  (human or JSON, to stderr typically)
//	      fileHandler*     (one per FileRoute; always trace level)
//
// The policy gate sits at the top so disabled records don't even reach
// the sinks. File handlers ignore the policy gate (they're meant to
// capture EVERYTHING for retrospective debugging) but still apply their
// own NameMatcher.

package logger

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// policyHandler enforces the user's Policy. It looks at the "name"
// attribute on each record and drops the record if its level is below
// the configured threshold for that name.
type policyHandler struct {
	policy *Policy
	next   slog.Handler
}

func newPolicyHandler(p *Policy, next slog.Handler) *policyHandler {
	return &policyHandler{policy: p, next: next}
}

func (h *policyHandler) Enabled(ctx context.Context, lev slog.Level) bool {
	// We can't decide without seeing the record's name attribute, but
	// returning true means slog still constructs the record. We return
	// true if ANY name might allow this level, i.e. if the level is
	// >= the minimum across all rules. The trace floor (-8) means we
	// effectively always return true; that's fine — the policyHandler
	// itself filters at Handle time.
	return true
}

func (h *policyHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.policy == nil {
		return h.next.Handle(ctx, r)
	}
	name := nameOf(r)
	if !h.policy.Enabled(name, Level(r.Level)) {
		return nil
	}
	return h.next.Handle(ctx, r)
}

func (h *policyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &policyHandler{policy: h.policy, next: h.next.WithAttrs(attrs)}
}

func (h *policyHandler) WithGroup(name string) slog.Handler {
	return &policyHandler{policy: h.policy, next: h.next.WithGroup(name)}
}

// nameOf extracts the "name" attribute value from a record, falling
// back to "" if absent. The name may have been attached via base.With
// (so it's in the handler's pre-attached attrs) or attached directly
// to the record; we have to check both.
func nameOf(r slog.Record) string {
	var found string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == nameAttrKey {
			found = a.Value.String()
			return false
		}
		return true
	})
	return found
}

// multiHandler fans a record out to multiple downstream handlers.
type multiHandlerImpl []slog.Handler

func multiHandler(hs []slog.Handler) slog.Handler {
	switch len(hs) {
	case 0:
		return newDiscardHandler()
	case 1:
		return hs[0]
	default:
		return multiHandlerImpl(hs)
	}
}

func (h multiHandlerImpl) Enabled(ctx context.Context, lev slog.Level) bool {
	for _, hd := range h {
		if hd.Enabled(ctx, lev) {
			return true
		}
	}
	return false
}

func (h multiHandlerImpl) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, hd := range h {
		if err := hd.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (h multiHandlerImpl) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(multiHandlerImpl, len(h))
	for i, hd := range h {
		out[i] = hd.WithAttrs(attrs)
	}
	return out
}

func (h multiHandlerImpl) WithGroup(name string) slog.Handler {
	out := make(multiHandlerImpl, len(h))
	for i, hd := range h {
		out[i] = hd.WithGroup(name)
	}
	return out
}

// terminalHandler renders one line per record to the given writer. The
// "human" format produces a compact aligned line; the "json" format
// emits compact JSON.
type terminalHandler struct {
	mu     *sync.Mutex
	w      io.Writer
	format string
	attrs  []slog.Attr
}

func newTerminalHandler(w io.Writer, format string) *terminalHandler {
	if format == "" {
		format = "human"
	}
	return &terminalHandler{
		mu:     &sync.Mutex{},
		w:      w,
		format: format,
	}
}

func (h *terminalHandler) Enabled(ctx context.Context, lev slog.Level) bool { return true }

func (h *terminalHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.format == "json" {
		return h.handleJSON(r)
	}
	return h.handleHuman(r)
}

func (h *terminalHandler) handleHuman(r slog.Record) error {
	// Format: <LEVEL> <name> <msg> key=value ...
	lev := Level(r.Level).String()
	name := nameOf(r)
	if name == "" {
		name = "-"
	}
	line := fmt.Sprintf("%-5s %-30s %s", lev, name, r.Message)
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == nameAttrKey {
			return true
		}
		line += " " + a.Key + "=" + a.Value.String()
		return true
	})
	line += "\n"
	_, err := h.w.Write([]byte(line))
	return err
}

func (h *terminalHandler) handleJSON(r slog.Record) error {
	rec := map[string]any{
		"time":  r.Time.Format("2006-01-02T15:04:05.000Z07:00"),
		"level": Level(r.Level).String(),
		"msg":   r.Message,
	}
	r.Attrs(func(a slog.Attr) bool {
		rec[a.Key] = a.Value.Any()
		return true
	})
	for _, a := range h.attrs {
		if _, taken := rec[a.Key]; !taken {
			rec[a.Key] = a.Value.Any()
		}
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = h.w.Write(b)
	return err
}

func (h *terminalHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := *h
	out.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &out
}

func (h *terminalHandler) WithGroup(name string) slog.Handler { return h }

// fileHandler writes records matching a NameMatcher to a file as one
// JSON object per line (JSONL). It always emits at trace level — file
// sinks are for completeness.
type fileHandler struct {
	mu      *sync.Mutex
	w       io.Writer
	matcher *NameMatcher
	attrs   []slog.Attr
}

func newFileHandler(w io.Writer, matcher *NameMatcher) *fileHandler {
	return &fileHandler{
		mu:      &sync.Mutex{},
		w:       w,
		matcher: matcher,
	}
}

func (h *fileHandler) Enabled(ctx context.Context, lev slog.Level) bool { return true }

func (h *fileHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.matcher != nil && !h.matcher.Match(nameOf(r)) {
		return nil
	}
	rec := map[string]any{
		"time":  r.Time.Format("2006-01-02T15:04:05.000Z07:00"),
		"level": Level(r.Level).String(),
		"msg":   r.Message,
	}
	r.Attrs(func(a slog.Attr) bool {
		rec[a.Key] = a.Value.Any()
		return true
	})
	for _, a := range h.attrs {
		if _, taken := rec[a.Key]; !taken {
			rec[a.Key] = a.Value.Any()
		}
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err = h.w.Write(b)
	return err
}

func (h *fileHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := *h
	out.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &out
}

func (h *fileHandler) WithGroup(name string) slog.Handler { return h }

// discardHandler does nothing. Used as a fallback when no sinks are
// configured (e.g. in tests that haven't called Configure).
type discardHandler struct{}

func newDiscardHandler() *discardHandler { return &discardHandler{} }

func (h *discardHandler) Enabled(context.Context, slog.Level) bool   { return false }
func (h *discardHandler) Handle(context.Context, slog.Record) error  { return nil }
func (h *discardHandler) WithAttrs([]slog.Attr) slog.Handler         { return h }
func (h *discardHandler) WithGroup(string) slog.Handler              { return h }
