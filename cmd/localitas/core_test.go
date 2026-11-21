package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// core-env prints what core gives a process of its machine: as a container
// reaches it (start-docker's --env-file) or as a native process does.
func TestCoreEnv_PrintsTheEnvironmentForEachVantage(t *testing.T) {
	home := t.TempDir()
	if err := client.WriteCoreEnv(home, "http://127.0.0.1:8190", "lt_node"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORE_URL", "")
	t.Setenv("LOCALITAS_API_TOKEN", "")
	t.Setenv("LOCALITAS_HOME", home)
	for args, want := range map[string]string{
		"--for container": "CORE_URL=http://host.docker.internal:8190\nLOCALITAS_API_TOKEN=lt_node\n",
		"--for=native":    "CORE_URL=http://127.0.0.1:8190\nLOCALITAS_API_TOKEN=lt_node\n",
		"":                "CORE_URL=http://127.0.0.1:8190\nLOCALITAS_API_TOKEN=lt_node\n",
	} {
		var out strings.Builder
		if err := coreEnvCommand(strings.Fields(args), &out); err != nil {
			t.Fatalf("core-env %s: %v", args, err)
		}
		if out.String() != want {
			t.Errorf("core-env %s = %q, want %q", args, out.String(), want)
		}
	}
	t.Setenv("LOCALITAS_HOME", t.TempDir())
	if err := coreEnvCommand([]string{"--for", "container"}, &strings.Builder{}); err == nil {
		t.Fatal("core-env with no core found printed an environment")
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
