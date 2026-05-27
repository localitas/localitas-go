package client

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	resetKey(t)

	plaintext := "my-secret-password-123!"
	encrypted, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if encrypted == plaintext {
		t.Error("encrypted should differ from plaintext")
	}
	if len(encrypted) < 4 || encrypted[:4] != "enc:" {
		t.Errorf("expected enc: prefix, got %s", encrypted[:10])
	}

	decrypted, err := Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

func TestDecrypt_PlaintextPassthrough(t *testing.T) {
	result, err := Decrypt("not-encrypted")
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if result != "not-encrypted" {
		t.Errorf("expected passthrough, got %q", result)
	}
}

func TestEncryptDecrypt_Empty(t *testing.T) {
	encrypted, _ := Encrypt("")
	if encrypted != "" {
		t.Error("empty input should return empty output")
	}
	decrypted, _ := Decrypt("")
	if decrypted != "" {
		t.Error("empty input should return empty output")
	}
}

func TestEncrypt_DifferentCiphertexts(t *testing.T) {
	resetKey(t)

	e1, _ := Encrypt("same-password")
	e2, _ := Encrypt("same-password")
	if e1 == e2 {
		t.Error("two encryptions of same plaintext should produce different ciphertexts (random nonce)")
	}

	d1, _ := Decrypt(e1)
	d2, _ := Decrypt(e2)
	if d1 != d2 {
		t.Error("both should decrypt to same value")
	}
}

func resetKey(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "secret.key")

	encryptionKey = nil
	encryptionKeyOnce = sync.Once{}

	origHome := os.Getenv("HOME")
	localitasDir := filepath.Join(tmpDir, ".localitas")
	os.MkdirAll(localitasDir, 0700)
	os.Setenv("HOME", tmpDir)
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		encryptionKey = nil
		encryptionKeyOnce = sync.Once{}
	})
	_ = keyPath
}
