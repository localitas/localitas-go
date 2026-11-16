package httputil

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"
)

// TryStep is one step of a try (resolving the name, connecting, the TLS
// handshake): when it began, counted from the try's start, and how it ended.
type TryStep struct {
	Began bool
	At    time.Duration
	Ended bool
	Took  time.Duration
	Err   error
}

// UpstreamTry is how far one failed try got, as net/http/httptrace saw it.
// Durations named At are counted from the try's start.
type UpstreamTry struct {
	Elapsed time.Duration
	Err     error
	// Status is the 5xx the service answered; 0 when it answered nothing.
	Status int
	DNS    TryStep
	// Addrs are the addresses the name resolved to.
	Addrs   []string
	Connect TryStep
	// Remote is the address connected to, or being connected to.
	Remote string
	TLS    TryStep
	// GotConn is set once the try had a connection to send on; Reused,
	// WasIdle and IdleTime say whether it came from the pool.
	GotConn  bool
	Reused   bool
	WasIdle  bool
	IdleTime time.Duration
	// Written is set once the whole request was sent.
	Written   bool
	WrittenAt time.Duration
	WriteErr  error
	// FirstByte is set once the first byte of the answer came.
	FirstByte   bool
	FirstByteAt time.Duration
}

// tryTracer records a try's httptrace events. A dial can outlive the try
// that started it (the transport finishes it for its pool), so events can
// come after finish, from other goroutines: every write holds mu, and finish
// hands back a copy.
type tryTracer struct {
	start time.Time
	mu    sync.Mutex
	try   UpstreamTry
}

func newTryTracer() *tryTracer { return &tryTracer{start: time.Now()} }

func (tr *tryTracer) record(f func(t *UpstreamTry, now time.Duration)) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	f(&tr.try, time.Since(tr.start))
}

func begin(s *TryStep, now time.Duration) {
	if !s.Began {
		s.Began, s.At = true, now
	}
}

func end(s *TryStep, now time.Duration, err error) {
	s.Ended, s.Took, s.Err = true, now-s.At, err
}

func (tr *tryTracer) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			tr.record(func(t *UpstreamTry, now time.Duration) { begin(&t.DNS, now) })
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			tr.record(func(t *UpstreamTry, now time.Duration) {
				end(&t.DNS, now, info.Err)
				for _, a := range info.Addrs {
					t.Addrs = append(t.Addrs, a.String())
				}
			})
		},
		// With several addresses the dialer may try more than one; the
		// step is the first start to the connection that held, or to the
		// last failure when none did.
		ConnectStart: func(_, addr string) {
			tr.record(func(t *UpstreamTry, now time.Duration) {
				if t.Connect.Ended && t.Connect.Err == nil {
					return
				}
				begin(&t.Connect, now)
				t.Connect.Ended, t.Remote = false, addr
			})
		},
		ConnectDone: func(_, addr string, err error) {
			tr.record(func(t *UpstreamTry, now time.Duration) {
				if t.Connect.Ended && t.Connect.Err == nil {
					return
				}
				end(&t.Connect, now, err)
				t.Remote = addr
			})
		},
		TLSHandshakeStart: func() {
			tr.record(func(t *UpstreamTry, now time.Duration) { begin(&t.TLS, now) })
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			tr.record(func(t *UpstreamTry, now time.Duration) { end(&t.TLS, now, err) })
		},
		GotConn: func(info httptrace.GotConnInfo) {
			tr.record(func(t *UpstreamTry, now time.Duration) {
				t.GotConn, t.Reused, t.WasIdle, t.IdleTime = true, info.Reused, info.WasIdle, info.IdleTime
				if info.Conn != nil {
					t.Remote = info.Conn.RemoteAddr().String()
				}
			})
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			tr.record(func(t *UpstreamTry, now time.Duration) {
				t.Written, t.WrittenAt, t.WriteErr = info.Err == nil, now, info.Err
			})
		},
		GotFirstResponseByte: func() {
			tr.record(func(t *UpstreamTry, now time.Duration) { t.FirstByte, t.FirstByteAt = true, now })
		},
	}
}

// finish ends the try with err (and the 5xx status, if one came) and returns
// what it got through.
func (tr *tryTracer) finish(err error, status int) UpstreamTry {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	t := tr.try
	t.Addrs = append([]string(nil), t.Addrs...)
	t.Elapsed, t.Err, t.Status = time.Since(tr.start), err, status
	return t
}

// String says how far the try got, step by step, ending at the step that
// stalled or failed, e.g. "new conn: dns 12ms, connect 40ms to
// 51.81.167.44:443, tls stalled after 10s".
func (t UpstreamTry) String() string {
	var parts []string
	if t.GotConn && t.Reused {
		conn := "reused conn"
		if t.WasIdle {
			conn = "reused idle conn (idle " + short(t.IdleTime) + ")"
		}
		parts = append(parts, conn+" to "+t.Remote)
	} else {
		var steps []string
		stopped := false
		for _, s := range []struct {
			step       TryStep
			name, addr string
		}{
			{t.DNS, "dns", ""},
			{t.Connect, "connect", t.Remote},
			{t.TLS, "tls", ""},
		} {
			desc, stop := s.step.describe(s.name, s.addr, t.Elapsed)
			if desc != "" {
				steps = append(steps, desc)
			}
			if stop {
				stopped = true
				break
			}
		}
		conn := "new conn"
		if len(steps) > 0 {
			conn += ": " + strings.Join(steps, ", ")
		}
		parts = append(parts, conn)
		if stopped {
			return strings.Join(parts, ", ")
		}
		if !t.GotConn {
			if len(steps) == 0 {
				parts[0] = "no conn after " + short(t.Elapsed)
			}
			return strings.Join(append(parts, cause(t.Err)), ", ")
		}
	}
	switch {
	case t.WriteErr != nil:
		parts = append(parts, "request write failed ("+t.WriteErr.Error()+")")
	case !t.Written:
		parts = append(parts, "request not written: "+cause(t.Err))
	case t.Status != 0:
		parts = append(parts, "request written", fmt.Sprintf("answered %d in %s", t.Status, short(t.FirstByteAt-t.WrittenAt)))
	case t.FirstByte:
		parts = append(parts, "request written", "first byte after "+short(t.FirstByteAt-t.WrittenAt)+", then "+cause(t.Err))
	case stalled(t.Err):
		parts = append(parts, "request written", "no response in "+short(t.Elapsed-t.WrittenAt))
	default:
		parts = append(parts, "request written", "no response after "+short(t.Elapsed-t.WrittenAt)+": "+cause(t.Err))
	}
	return strings.Join(parts, ", ")
}

// describe says how a step went: "connect 40ms to addr", "tls stalled after
// 10s", "dns failed after 3ms (lookup ...: no such host)". stop is set when
// the try ended at this step.
func (s TryStep) describe(name, addr string, elapsed time.Duration) (desc string, stop bool) {
	subject := name
	if addr != "" {
		subject += " to " + addr
	}
	switch {
	case !s.Began:
		return "", false
	case !s.Ended:
		return subject + " stalled after " + short(elapsed-s.At), true
	case s.Err != nil:
		return subject + " failed after " + short(s.Took) + " (" + s.Err.Error() + ")", true
	case addr != "":
		return name + " " + short(s.Took) + " to " + addr, false
	default:
		return name + " " + short(s.Took), false
	}
}

// stalled reports whether err is a try running out of time (or being
// called off) rather than failing outright.
func stalled(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// cause is err without the url.Error wrapping that repeats the URL.
func cause(err error) string {
	if err == nil {
		return "no error"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return err.Error()
}

// short is d rounded for reading: 350µs, 12ms, 10.4s.
func short(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return d.Round(time.Microsecond).String()
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	default:
		return d.Round(100 * time.Millisecond).String()
	}
}
