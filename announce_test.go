package client

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// fakeCore stands in for core's readiness long poll, its door and its app
// registry. A readiness question with a timeout is held until the node is
// ready (or the timeout passes); one without is answered at once. Before
// Ready, POST /apps/ext is answered 503 by the door.
type fakeCore struct {
	*httptest.Server
	mu         sync.Mutex
	ready      chan struct{}
	asks       []url.Values
	posts      int
	postStatus []int
	events     chan string
	removed    chan string
}

type fakeReadyAnswer struct {
	Phase string `json:"phase"`
	Want  string `json:"want"`
	Ready bool   `json:"ready"`
}

func newFakeCore(t *testing.T) *fakeCore {
	return newFakeCoreOn(t, nil)
}

// newFakeCoreOn serves on ln, or on a new port when ln is nil.
func newFakeCoreOn(t *testing.T, ln net.Listener) *fakeCore {
	f := &fakeCore{ready: make(chan struct{}), events: make(chan string, 64), removed: make(chan string, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/system/ready", f.handleReady)
	mux.HandleFunc("POST /apps/ext", f.handleRegister)
	mux.HandleFunc("DELETE /apps/ext/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.removed <- r.PathValue("name")
		w.WriteHeader(http.StatusNoContent)
	})
	f.Server = httptest.NewUnstartedServer(mux)
	if ln != nil {
		f.Listener.Close()
		f.Listener = ln
	}
	f.Start()
	t.Cleanup(f.Close)
	return f
}

func (f *fakeCore) becomeReady() {
	f.mu.Lock()
	defer f.mu.Unlock()
	close(f.ready)
}

func (f *fakeCore) handleReady(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	f.asks = append(f.asks, q)
	ready := f.ready
	f.mu.Unlock()
	f.events <- "ready"
	if d, err := time.ParseDuration(q.Get("timeout")); err == nil && d > 0 {
		select {
		case <-ready:
		case <-time.After(d):
		case <-r.Context().Done():
			return
		}
	}
	ans := fakeReadyAnswer{Phase: "serving", Want: q.Get("phase")}
	select {
	case <-ready:
		ans.Phase, ans.Ready = "ready", true
	default:
		w.Header().Set("Retry-After", "1")
	}
	w.Header().Set("Content-Type", "application/json")
	if !ans.Ready {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(ans)
}

func (f *fakeCore) handleRegister(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.posts++
	status := http.StatusCreated
	if f.posts <= len(f.postStatus) {
		status = f.postStatus[f.posts-1]
	}
	select {
	case <-f.ready:
	default:
		status = http.StatusServiceUnavailable
	}
	if status == http.StatusServiceUnavailable {
		select {
		case <-f.ready:
			f.ready = make(chan struct{})
		default:
		}
	}
	f.mu.Unlock()
	f.events <- "post"
	switch status {
	case http.StatusCreated:
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(struct {
			Name string `json:"name"`
		}{"app"})
	case http.StatusServiceUnavailable:
		w.Header().Set("Retry-After", "1")
		w.Header().Set("X-Localitas-Not-Ready", "serving")
		http.Error(w, `{"error":"node not ready: serving"}`, status)
	default:
		http.Error(w, `{"error":"health.json: connection refused"}`, status)
	}
}

func (f *fakeCore) next(t *testing.T) string {
	t.Helper()
	select {
	case e := <-f.events:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("core heard nothing from the app")
		return ""
	}
}

func (f *fakeCore) readyAsks() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.asks...)
}

func (f *fakeCore) postCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts
}

func announceTo(t *testing.T, coreURL string) *Announcement {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	a := New(coreURL).WithToken("token").Announce(ln)
	t.Cleanup(func() { a.Withdraw() })
	return a
}

func waitDone(t *testing.T, a *Announcement) {
	t.Helper()
	select {
	case <-a.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the announcement did not end")
	}
}

// The app asks core's readiness first, in one long poll core holds, and
// registers only once core answers it is ready.
func TestAnnounce_WaitsOnCoresReadinessLongPollBeforeRegistering(t *testing.T) {
	core := newFakeCore(t)
	a := announceTo(t, core.URL)

	if e := core.next(t); e != "ready" {
		t.Fatalf("first call was %s: POST /apps/ext reached core before its readiness answered", e)
	}
	q := core.readyAsks()[0]
	if q.Get("phase") != "ready" {
		t.Fatalf("asked for phase %q, want ready", q.Get("phase"))
	}
	if d, err := time.ParseDuration(q.Get("timeout")); err != nil || d <= 0 {
		t.Fatalf("readiness asked with timeout %q: not a long poll", q.Get("timeout"))
	}

	core.becomeReady()
	if e := core.next(t); e != "post" {
		t.Fatalf("after core became ready the app made a %s call, want its registration", e)
	}
	if n := len(core.readyAsks()); n != 1 {
		t.Fatalf("the app asked readiness %d times, want one long poll answered at the transition", n)
	}
	waitDone(t, a)
	if a.Name() != "app" {
		t.Fatalf("registered as %q, want app", a.Name())
	}
	if n := core.postCount(); n != 1 {
		t.Fatalf("registered %d times, want once", n)
	}
}

// A registration core answers 503 (it is not ready: it restarted between its
// readiness answer and the registration) sends the app back to waiting, and
// it registers once core is ready again.
func TestAnnounce_NotReadyRegistrationWaitsAgainThenRegistersOnce(t *testing.T) {
	core := newFakeCore(t)
	core.postStatus = []int{http.StatusServiceUnavailable}
	core.becomeReady()
	a := announceTo(t, core.URL)

	for i, want := range []string{"ready", "post", "ready"} {
		if e := core.next(t); e != want {
			t.Fatalf("call %d was %s, want %s", i+1, e, want)
		}
	}
	core.becomeReady()
	if e := core.next(t); e != "post" {
		t.Fatalf("after core became ready again the app made a %s call, want its registration", e)
	}
	waitDone(t, a)
	if a.Name() != "app" {
		t.Fatalf("registered as %q, want app", a.Name())
	}
	if n, asks := core.postCount(), len(core.readyAsks()); n != 2 || asks != 2 {
		t.Fatalf("%d registrations and %d readiness polls, want 2 and 2", n, asks)
	}
}

// An app announces before it serves, so core may not reach it yet (502): it
// is asked again, and registers once it answers.
func TestAnnounce_AnAppCoreCannotReachYetIsAskedAgainThenRegistersOnce(t *testing.T) {
	core := newFakeCore(t)
	core.postStatus = []int{http.StatusBadGateway}
	core.becomeReady()
	a := announceTo(t, core.URL)

	for i, want := range []string{"ready", "post", "ready", "post"} {
		if e := core.next(t); e != want {
			t.Fatalf("call %d was %s, want %s: core could not reach the app yet (502) and it did not register once it answered", i+1, e, want)
		}
	}
	waitDone(t, a)
	if a.Name() != "app" {
		t.Fatalf("registered as %q, want app", a.Name())
	}
	if n := core.postCount(); n != 2 {
		t.Fatalf("registered %d times, want 2 asks", n)
	}
}

// A registration core refuses for any other reason is a fault: logged, not
// tried again.
func TestAnnounce_RefusedRegistrationIsNotRetried(t *testing.T) {
	core := newFakeCore(t)
	core.postStatus = []int{http.StatusUnprocessableEntity}
	core.becomeReady()
	a := announceTo(t, core.URL)

	for i, want := range []string{"ready", "post"} {
		if e := core.next(t); e != want {
			t.Fatalf("call %d was %s, want %s", i+1, e, want)
		}
	}
	waitDone(t, a)
	if a.Name() != "" {
		t.Fatalf("a refused app is named %q", a.Name())
	}
	if n := core.postCount(); n != 1 {
		t.Fatalf("registered %d times, want once", n)
	}
}

// Withdrawn while core is still booting, the app ends its long poll at once
// and never registers.
func TestWithdraw_WhileWaitingOnReadinessEndsAtOnce(t *testing.T) {
	core := newFakeCore(t)
	a := announceTo(t, core.URL)

	if e := core.next(t); e != "ready" {
		t.Fatalf("first call was %s, want the readiness long poll", e)
	}
	withdrawn := make(chan error, 1)
	go func() { withdrawn <- a.Withdraw() }()
	select {
	case err := <-withdrawn:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Withdraw waited on core's readiness")
	}
	if n := core.postCount(); n != 0 {
		t.Fatalf("withdrawn before core was ready, the app registered %d times", n)
	}
}

// Core not listening yet (its process is starting): the app dials again
// until it listens, then waits on its readiness.
func TestAnnounce_DialsAgainUntilCoreListens(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	a := announceTo(t, "http://"+addr)
	time.Sleep(300 * time.Millisecond)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	core := newFakeCoreOn(t, ln)

	if e := core.next(t); e != "ready" {
		t.Fatalf("first call was %s, want the readiness long poll", e)
	}
	core.becomeReady()
	if e := core.next(t); e != "post" {
		t.Fatalf("after core became ready the app made a %s call, want its registration", e)
	}
	waitDone(t, a)
	if a.Name() != "app" {
		t.Fatalf("registered as %q, want app", a.Name())
	}
}

// An app withdrawn while core is still answering its announcement is taken
// out once core answers: core registered it before answering, so the answer
// naming it is waited for, not abandoned with the app left registered.
func TestWithdraw_DuringAnAnnouncementCoreTookRemovesTheApp(t *testing.T) {
	taken := make(chan struct{})
	answer := make(chan struct{})
	removed := make(chan string, 1)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(fakeReadyAnswer{Phase: "ready", Want: "ready", Ready: true})
		case http.MethodPost:
			close(taken)
			<-answer
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(struct {
				Name string `json:"name"`
			}{"late"})
		case http.MethodDelete:
			removed <- r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(core.Close)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	a := New(core.URL).WithToken("token").Announce(ln)
	<-taken
	a.stop()
	close(answer)
	if err := a.Withdraw(); err != nil {
		t.Fatal(err)
	}
	select {
	case path := <-removed:
		if path != "/apps/ext/late" {
			t.Fatalf("withdrew %s, want /apps/ext/late", path)
		}
	default:
		t.Fatal("withdrawn while core was taking it, the app was left registered")
	}
}
