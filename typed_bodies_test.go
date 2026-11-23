package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/localitas/localitas-go/maptest"
)

// The SDK's own Go holds no untyped map.
func TestNoUntypedMaps(t *testing.T) {
	maptest.NoUntypedMaps(t, ".", nil)
}

// bodyOf serves one call and returns the body the client sent.
func bodyOf(t *testing.T, reply string, call func(c *Client) error) string {
	t.Helper()
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		got = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, reply)
	}))
	defer ts.Close()
	if err := call(New(ts.URL)); err != nil {
		t.Fatal(err)
	}
	return got
}

// A credential's create and update bodies keep the wire shape the vault
// app decodes, byte for byte.
func TestVaultCredentialBodies_WireShape(t *testing.T) {
	const want = `{"data":{"nt_hash":"ab12"},"keychain_sync":true,"name":"SMB","url":"https://nas.local"}`
	secret := map[string]string{"nt_hash": "ab12"}
	created := bodyOf(t, `{"public_id":"p1"}`, func(c *Client) error {
		_, err := c.VaultCreateCredential(context.Background(), "SMB", "https://nas.local", true, secret)
		return err
	})
	if created != want {
		t.Errorf("create body = %s\nwant %s", created, want)
	}
	updated := bodyOf(t, `{}`, func(c *Client) error {
		return c.VaultUpdateCredential(context.Background(), "p1", "SMB", "https://nas.local", true, secret)
	})
	if updated != want {
		t.Errorf("update body = %s\nwant %s", updated, want)
	}
}

// A structured ingest's body keeps its wire shape, byte for byte.
func TestIngestMetricsBody_WireShape(t *testing.T) {
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	got := bodyOf(t, `{"accepted":1}`, func(c *Client) error {
		_, err := c.IngestMetrics(context.Background(), []MetricPoint{{Name: "cpu", Value: 0.5, Type: "gauge", Tags: map[string]string{"host": "a"}, Timestamp: &at}})
		return err
	})
	const want = `{"metrics":[{"name":"cpu","value":0.5,"type":"gauge","tags":{"host":"a"},"timestamp":"2026-11-23T10:00:00Z"}]}`
	if got != want {
		t.Fatalf("ingest body = %s\nwant %s", got, want)
	}
}

// A "reconnecting" listener receives the attempt and its delay as a
// PubSubReconnect.
func TestPubSubWS_ReconnectingEvent(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := "ws" + ts.URL[len("http"):]
	ts.Close()

	got := make(chan PubSubReconnect, 1)
	ps := NewPubSubWS(url, "", WithReconnectInterval(time.Hour))
	defer ps.Close()
	ps.On("reconnecting", func(data interface{}) {
		if ev, ok := data.(PubSubReconnect); ok {
			select {
			case got <- ev:
			default:
			}
			return
		}
		t.Errorf("reconnecting event is a %T, want a PubSubReconnect", data)
	})
	ps.scheduleReconnect()
	select {
	case ev := <-got:
		if ev.Attempt < 1 || ev.Delay <= 0 {
			t.Fatalf("reconnecting event = %+v, want an attempt and its delay", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no reconnecting event")
	}
}
