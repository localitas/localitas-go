package httputil

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// natRelay forwards TCP to target the way a NAT does, and can forget the
// connections it holds: their bytes then vanish in both directions while
// neither end is told, as when Docker Desktop drops an idle flow. A new
// connection is forwarded as before.
type natRelay struct {
	ln     net.Listener
	target string
	mu     sync.Mutex
	alive  []*atomic.Bool
}

func newNATRelay(t *testing.T, target string) *natRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &natRelay{ln: ln, target: target}
	t.Cleanup(func() { ln.Close() })
	go r.serve()
	return r
}

func (r *natRelay) serve() {
	for {
		c, err := r.ln.Accept()
		if err != nil {
			return
		}
		s, err := net.Dial("tcp", r.target)
		if err != nil {
			c.Close()
			continue
		}
		alive := &atomic.Bool{}
		alive.Store(true)
		r.mu.Lock()
		r.alive = append(r.alive, alive)
		r.mu.Unlock()
		go forward(c, s, alive)
		go forward(s, c, alive)
	}
}

// forward copies src to dst while alive, and after that reads and drops.
func forward(src, dst net.Conn, alive *atomic.Bool) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 && alive.Load() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err == io.EOF || err != nil {
			return
		}
	}
}

// forget drops every connection the relay holds now, silently.
func (r *natRelay) forget() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.alive {
		a.Store(false)
	}
}

// An HTTP/2 connection the network dropped without a word stays in the
// client's pool: a try on it stalls, and every try after it would stall
// too on the same connection. The upstream client pings a connection gone
// silent and closes it unanswered, so the stalled try does not doom the
// others: the next is asked on a fresh connection and answered. The
// timings are the default client's (5 s, 3 s, 10 s), scaled down.
func TestUpstream_ATryOnASilentlyDeadConnectionIsAskedAgainOnAFreshOne(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("forecast " + r.Proto))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	relay := newNATRelay(t, srv.Listener.Addr().String())
	u := "https://" + relay.ln.Addr().String() + "/forecast"

	client := newUpstreamClient(srv.Client().Transport.(*http.Transport), 50*time.Millisecond, 30*time.Millisecond)
	up := Upstream{Client: client, Delay: time.Millisecond, AttemptTimeout: 200 * time.Millisecond}
	ctx := context.Background()

	resp, err := up.Get(ctx, "forecast", u)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "forecast HTTP/2.0" {
		t.Fatalf("first answer %q, want it over HTTP/2", body)
	}

	relay.forget()

	start := time.Now()
	resp, err = up.Get(ctx, "forecast", u)
	if err != nil {
		t.Fatalf("after the connection was dropped: %v (%s)", err, time.Since(start))
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "forecast HTTP/2.0" {
		t.Fatalf("answer after the drop %q", body)
	}
}
