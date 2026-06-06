package client

import (
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
