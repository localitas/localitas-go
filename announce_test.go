package client

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An app withdrawn while core is still answering its announcement is taken
// out once core answers: core registered it before answering, so the answer
// naming it is waited for, not abandoned with the app left registered.
func TestWithdraw_DuringAnAnnouncementCoreTookRemovesTheApp(t *testing.T) {
	taken := make(chan struct{})
	answer := make(chan struct{})
	removed := make(chan string, 1)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
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
