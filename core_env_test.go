package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// standInCore stands in for core: it records each request's path and
// Authorization header.
type standInCore struct {
	srv   *httptest.Server
	calls chan string
}

func newStandInCore(t *testing.T) *standInCore {
	t.Helper()
	f := &standInCore{calls: make(chan string, 16)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls <- r.URL.Path + " " + r.Header.Get("Authorization")
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// writeHomeCoreEnv writes the core.env a node writes in its home: the
// environment core gives a process of its machine, as KEY=VALUE lines.
func writeHomeCoreEnv(t *testing.T, home, body string) {
	t.Helper()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "core.env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// nativeRun is the environment of an app launchd starts on core's machine:
// nothing tells it where core is but the home of that core.
func nativeRun(t *testing.T, home string) {
	t.Helper()
	t.Setenv("CORE_URL", "")
	t.Setenv("LOCALITAS_API_TOKEN", "")
	t.Setenv("LOCALITAS_HOME", home)
}

// A natively run app reaches the core whose home it names, at the URL and
// with the token that core wrote in core.env.
func TestDefault_NativeRunReachesCoreThroughItsHome(t *testing.T) {
	core := newStandInCore(t)
	home := t.TempDir()
	writeHomeCoreEnv(t, home, "# written by localitas-core\nCORE_URL="+core.srv.URL+"\nLOCALITAS_API_TOKEN=lt_node\n")
	nativeRun(t, home)

	c, err := Default()
	if err != nil {
		t.Fatalf("Default() in a native run = %v, want the client of the core in %s", err, home)
	}
	if _, err := c.ListDatabases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := <-core.calls; got != "/apps/data/api/databases Bearer lt_node" {
		t.Fatalf("core got %q, want the call with the token core wrote", got)
	}
}

// The flag defaults the apps build their client from (DefaultCoreURL,
// DefaultToken) resolve as Default does.
func TestDefaultCoreURLAndToken_NativeRunReadTheHome(t *testing.T) {
	core := newStandInCore(t)
	home := t.TempDir()
	writeHomeCoreEnv(t, home, "CORE_URL="+core.srv.URL+"\nLOCALITAS_API_TOKEN=lt_node\n")
	nativeRun(t, home)

	if u := DefaultCoreURL(); u != core.srv.URL {
		t.Fatalf("DefaultCoreURL = %q, want %q from core.env", u, core.srv.URL)
	}
	if tok := DefaultToken(); tok != "lt_node" {
		t.Fatalf("DefaultToken = %q, want lt_node from core.env", tok)
	}
	if _, err := New(DefaultCoreURL()).WithToken(DefaultToken()).ListDatabases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := <-core.calls; got != "/apps/data/api/databases Bearer lt_node" {
		t.Fatalf("core got %q", got)
	}
}

// Without LOCALITAS_HOME the home is ~/.localitas, where core keeps its
// config by default.
func TestDefault_HomeDefaultsToDotLocalitas(t *testing.T) {
	core := newStandInCore(t)
	user := t.TempDir()
	writeHomeCoreEnv(t, filepath.Join(user, ".localitas"), "CORE_URL="+core.srv.URL+"\nLOCALITAS_API_TOKEN=lt_node\n")
	nativeRun(t, "")
	t.Setenv("HOME", user)

	c, err := Default()
	if err != nil {
		t.Fatalf("Default() = %v, want the core in ~/.localitas", err)
	}
	if c.baseURL != core.srv.URL {
		t.Fatalf("base URL %q, want %q", c.baseURL, core.srv.URL)
	}
}

// What core put in the environment wins over the home's file: a container
// (and a FaaS function) is told where core is by the core that started it.
func TestDefault_EnvironmentWinsOverTheHome(t *testing.T) {
	core := newStandInCore(t)
	home := t.TempDir()
	writeHomeCoreEnv(t, home, "CORE_URL=http://127.0.0.1:1\nLOCALITAS_API_TOKEN=lt_node\n")
	t.Setenv("LOCALITAS_HOME", home)
	t.Setenv("CORE_URL", core.srv.URL)
	t.Setenv("LOCALITAS_API_TOKEN", "lt_container")

	c, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListDatabases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := <-core.calls; got != "/apps/data/api/databases Bearer lt_container" {
		t.Fatalf("core got %q, want the environment's URL and token", got)
	}
}

// With nothing configured, Default fails loudly, naming the variable and
// the file it looked in; it never guesses an address.
func TestDefault_NothingConfiguredNamesVariableAndFile(t *testing.T) {
	home := t.TempDir()
	nativeRun(t, home)
	_, err := Default()
	if !errors.Is(err, ErrNoCoreURL) {
		t.Fatalf("Default() = %v, want ErrNoCoreURL", err)
	}
	for _, want := range []string{"CORE_URL", filepath.Join(home, "core.env")} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if u := DefaultCoreURL(); u != "" {
		t.Fatalf("DefaultCoreURL = %q, want none rather than a guess", u)
	}
}

// A core.env naming something that is not core's http(s) URL is refused,
// naming the file.
func TestDefault_RefusesABadURLInTheHome(t *testing.T) {
	home := t.TempDir()
	writeHomeCoreEnv(t, home, "CORE_URL=localhost:8090\nLOCALITAS_API_TOKEN=lt_node\n")
	nativeRun(t, home)
	_, err := Default()
	if err == nil || !strings.Contains(err.Error(), filepath.Join(home, "core.env")) {
		t.Fatalf("Default() = %v, want an error naming %s", err, filepath.Join(home, "core.env"))
	}
}

// What a node writes is what a process of its machine reads: the file
// WriteCoreEnv writes (private, whole) leads Default to that core.
func TestWriteCoreEnv_IsWhatDefaultReads(t *testing.T) {
	core := newStandInCore(t)
	home := t.TempDir()
	if err := WriteCoreEnv(home, core.srv.URL, "lt_node"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(home, CoreEnvFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%s is not private to its owner: %v %v", CoreEnvFile, info, err)
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 1 {
		t.Fatalf("home holds %d entries, want only %s", len(entries), CoreEnvFile)
	}
	nativeRun(t, home)
	c, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListDatabases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := <-core.calls; got != "/apps/data/api/databases Bearer lt_node" {
		t.Fatalf("core got %q", got)
	}
	u, tok, err := CoreAt(home)
	if err != nil || u != core.srv.URL || tok != "lt_node" {
		t.Fatalf("CoreAt = %q %q %v", u, tok, err)
	}
}

// CoreEnv is the environment core gives an app it starts, with the same
// names the file holds.
func TestCoreEnv_NamesWhatDefaultReads(t *testing.T) {
	env := CoreEnv("http://host.docker.internal:9090", "lt_app")
	if strings.Join(env, " ") != "CORE_URL=http://host.docker.internal:9090 LOCALITAS_API_TOKEN=lt_app" {
		t.Fatalf("CoreEnv = %v", env)
	}
	if env := CoreEnv("http://core.test", ""); len(env) != 1 {
		t.Fatalf("CoreEnv with no token = %v, want the URL only", env)
	}
}

// A container on core's machine reaches core's loopback URL through
// Docker's name for the host, on the same port; other URLs stay.
func TestContainerURL_LoopbackBecomesTheContainersHost(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:8090":         "http://host.docker.internal:8090",
		"http://localhost:9090/":        "http://host.docker.internal:9090/",
		"http://[::1]:8190":             "http://host.docker.internal:8190",
		"https://family.localitas.test": "https://family.localitas.test",
		"http://192.168.1.20:8090":      "http://192.168.1.20:8090",
		"":                              "",
	} {
		if got := ContainerURL(in); got != want {
			t.Errorf("ContainerURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// A tool told a core's home that has no core.env fails naming the file.
func TestCoreAt_MissingFileNamesIt(t *testing.T) {
	home := t.TempDir()
	_, _, err := CoreAt(home)
	if !errors.Is(err, ErrNoCoreURL) || !strings.Contains(err.Error(), filepath.Join(home, CoreEnvFile)) {
		t.Fatalf("CoreAt = %v, want ErrNoCoreURL naming the file", err)
	}
}
