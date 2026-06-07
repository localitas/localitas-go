package client

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultCoreURL_ReturnsLocalhost(t *testing.T) {
	u := DefaultCoreURL()
	if runtime.GOOS != "linux" {
		if u != "http://localhost:"+DefaultCorePort {
			t.Fatalf("expected localhost URL on non-linux, got %s", u)
		}
	}
}

func TestIsContainer_NonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("skipping on linux where detection may vary")
	}
	if isContainer() {
		t.Fatal("expected false on non-linux host")
	}
}

func TestDefaultToken_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	tokenDir := filepath.Join(dir, ".localitas")
	os.MkdirAll(tokenDir, 0755)
	os.WriteFile(filepath.Join(tokenDir, "api-token"), []byte("lt_test123\n"), 0600)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	token := DefaultToken()
	if token != "lt_test123" {
		t.Fatalf("expected lt_test123, got %q", token)
	}
}

func TestDefaultToken_MissingFile(t *testing.T) {
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	token := DefaultToken()
	if token != "" {
		t.Fatalf("expected empty token, got %q", token)
	}
}
