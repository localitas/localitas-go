package httputil

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// traced is quick to give up a try, so a stall is seen in milliseconds.
var traced = Upstream{Delay: time.Millisecond, AttemptTimeout: 200 * time.Millisecond}

func upstreamErr(t *testing.T, err error) *UpstreamError {
	t.Helper()
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want an UpstreamError", err)
	}
	return ue
}

// wantTries checks ue's message holds every part and that it recorded n
// tries.
func wantTries(t *testing.T, ue *UpstreamError, n int, parts ...string) {
	t.Helper()
	msg := ue.Error()
	t.Log(msg)
	for _, p := range parts {
		if !strings.Contains(msg, p) {
			t.Errorf("message lacks %q:\n%s", p, msg)
		}
	}
	if len(ue.Tries) != n {
		t.Fatalf("%d tries recorded, want %d", len(ue.Tries), n)
	}
}

// stallingServer is an HTTPS server that answers its first answered
// requests and then holds every later one without a word.
func stallingServer(t *testing.T, answered int32) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= answered {
			w.Write([]byte("forecast"))
			return
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A peer that takes the connection but never answers the TLS handshake is
// named as such, on every try.
func TestUpstreamTrace_ATLSHandshakeThatNeverEndsIsNamed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		var held []net.Conn
		defer func() {
			for _, c := range held {
				c.Close()
			}
		}()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
		}
	}()

	_, err = traced.Get(context.Background(), "Open-Meteo", "https://"+ln.Addr().String()+"/v1/forecast")
	ue := upstreamErr(t, err)
	wantTries(t, ue, 3, "Open-Meteo: 3 tries failed: #1 new conn: connect ", " to "+ln.Addr().String()+", tls stalled after ", "ms; #2 new conn:", "; #3 new conn:")
	if !ue.Tries[2].TLS.Began || ue.Tries[2].TLS.Ended || ue.Tries[2].Remote != ln.Addr().String() {
		t.Errorf("tries = %+v", ue.Tries)
	}
}

// A service that reads the request and never answers is told apart from
// one that could not be reached.
func TestUpstreamTrace_ARequestWrittenButNeverAnsweredIsNamed(t *testing.T) {
	srv := stallingServer(t, 0)
	up := traced
	up.Client = srv.Client()

	_, err := up.Get(context.Background(), "Open-Meteo", srv.URL+"/v1/forecast")
	ue := upstreamErr(t, err)
	wantTries(t, ue, 3, "#1 new conn: connect ", ", tls ", ", request written, no response in ", "#3 new conn:")
	if !ue.Tries[0].Written || ue.Tries[0].FirstByte || ue.Tries[0].Reused {
		t.Errorf("first try = %+v", ue.Tries[0])
	}
}

// A name that does not resolve is named with the resolver's error.
func TestUpstreamTrace_ANameThatDoesNotResolveIsNamed(t *testing.T) {
	_, err := traced.Get(context.Background(), "geocoding", "https://nosuch.invalid/search")
	ue := upstreamErr(t, err)
	wantTries(t, ue, 3, "geocoding: 3 tries failed: #1 new conn: dns failed after ", "nosuch.invalid")
	if ue.Tries[0].DNS.Err == nil || ue.Tries[0].Connect.Began {
		t.Errorf("first try = %+v", ue.Tries[0])
	}
}

// A port nothing listens on is named as refused, with the address.
func TestUpstreamTrace_ARefusedConnectionIsNamed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	_, err = traced.Get(context.Background(), "geocoding", "https://"+addr+"/search")
	ue := upstreamErr(t, err)
	wantTries(t, ue, 3, "#1 new conn: connect to "+addr+" failed after ", "refused")
	if ue.Tries[0].Connect.Err == nil || ue.Tries[0].TLS.Began {
		t.Errorf("first try = %+v", ue.Tries[0])
	}
}

// A try on a pooled connection says it reused it and how long it lay idle;
// the next try, on a fresh connection, says so too.
func TestUpstreamTrace_AReusedConnectionIsNamed(t *testing.T) {
	srv := stallingServer(t, 1)
	up := traced
	up.Client = srv.Client()
	ctx := context.Background()

	resp, err := up.Get(ctx, "Open-Meteo", srv.URL+"/v1/forecast")
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	time.Sleep(50 * time.Millisecond)

	_, err = up.Get(ctx, "Open-Meteo", srv.URL+"/v1/forecast")
	ue := upstreamErr(t, err)
	wantTries(t, ue, 3, "#1 reused idle conn (idle ", ") to "+srv.Listener.Addr().String()+", request written, no response in ", "; #2 new conn: connect ")
	first := ue.Tries[0]
	if !first.Reused || !first.WasIdle || first.IdleTime < 50*time.Millisecond || first.Connect.Began {
		t.Errorf("first try = %+v", first)
	}
	if ue.Tries[1].Reused || !ue.Tries[1].TLS.Ended {
		t.Errorf("second try = %+v", ue.Tries[1])
	}
}

// A service that answers 5xx every time is named with its status.
func TestUpstreamTrace_AServerErrorIsNamedWithItsStatus(t *testing.T) {
	srv, _ := flaky(t, 100, http.StatusServiceUnavailable)

	_, err := traced.Get(context.Background(), "forecast", srv.URL)
	ue := upstreamErr(t, err)
	wantTries(t, ue, 3, "forecast: 3 tries failed: #1 new conn: connect ", ", request written, answered 503 in ")
	if ue.Tries[0].Status != http.StatusServiceUnavailable {
		t.Errorf("first try = %+v", ue.Tries[0])
	}
}

// A caller that gives up during a try is told how far that try got, and the
// error is still its own deadline.
func TestUpstreamTrace_ACallerDeadlineSaysHowFarTheTryGot(t *testing.T) {
	srv := stallingServer(t, 0)
	up := Upstream{Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	_, err := up.Get(ctx, "geocoding", srv.URL+"/search")
	ue := upstreamErr(t, err)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the caller's deadline", err)
	}
	wantTries(t, ue, 1, "geocoding: context deadline exceeded after 1 try: #1 new conn: connect ", ", request written, no response in ")
}
