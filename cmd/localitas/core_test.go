package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	client "github.com/localitas/localitas-go"
)

// The CLI reaches the core it is pointed at the way every process does:
// --config names a core, whose core.env beside it gives the URL and token;
// without it, the environment, else the core in $LOCALITAS_HOME.
func TestCoreClient_ReachesTheCoreThroughItsCoreEnv(t *testing.T) {
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	home := t.TempDir()
	if err := client.WriteCoreEnv(home, srv.URL, "lt_node"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORE_URL", "")
	t.Setenv("LOCALITAS_API_TOKEN", "")

	t.Setenv("LOCALITAS_HOME", t.TempDir())
	byConfig, err := coreClient(filepath.Join(home, "config-core.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALITAS_HOME", home)
	byHome, err := coreClient("")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*client.Client{byConfig, byHome} {
		if _, err := c.ListDatabases(context.Background()); err != nil {
			t.Fatal(err)
		}
		if auth := <-got; auth != "Bearer lt_node" {
			t.Fatalf("core got %q, want the node's token", auth)
		}
	}
}

// Pointed at a core that has not written its core.env, the CLI fails
// naming the file instead of guessing an address.
func TestCoreClient_NoCoreEnvFailsLoudly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORE_URL", "")
	t.Setenv("LOCALITAS_HOME", home)
	if _, err := coreClient(""); err == nil {
		t.Fatal("coreClient with no core configured succeeded")
	}
	if _, err := coreClient(filepath.Join(home, "config-core.yaml")); err == nil {
		t.Fatal("coreClient for a core without core.env succeeded")
	}
}
