package client

import (
	"encoding/base64"
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

// TestEnvKeyStableAcrossInstances proves that a provided LOCALITAS_SECRET_KEY
// lets a "second instance" (fresh key state, no key file) decrypt ciphertext
// produced by the first — the cross-restart / cross-container guarantee that
// app containers rely on (they persist encrypted data in shared Raft state).
func TestEnvKeyStableAcrossInstances(t *testing.T) {
	// A fixed 32-byte key, base64-encoded, shared by both instances.
	keyB64 := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 32 bytes
	origHome := os.Getenv("HOME")
	origKey := os.Getenv("LOCALITAS_SECRET_KEY")
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		if origKey == "" {
			os.Unsetenv("LOCALITAS_SECRET_KEY")
		} else {
			os.Setenv("LOCALITAS_SECRET_KEY", origKey)
		}
		encryptionKey = nil
		encryptionKeyOnce = sync.Once{}
	})

	// Instance 1: HOME with no key file, but env key set → encrypt.
	os.Setenv("HOME", t.TempDir())
	os.Setenv("LOCALITAS_SECRET_KEY", keyB64)
	encryptionKey = nil
	encryptionKeyOnce = sync.Once{}
	ciphertext, err := Encrypt("GOCSPX-secret-value")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Instance 2: DIFFERENT HOME (so the file path can't help), same env key.
	os.Setenv("HOME", t.TempDir())
	encryptionKey = nil
	encryptionKeyOnce = sync.Once{}
	got, err := Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("decrypt on second instance: %v", err)
	}
	if got != "GOCSPX-secret-value" {
		t.Errorf("cross-instance decrypt = %q, want the original secret", got)
	}
}

func TestRequireSecretKey_AcceptsEnvKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOCALITAS_SECRET_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err := RequireSecretKey(); err != nil {
		t.Fatalf("RequireSecretKey with a valid env key: %v", err)
	}
}

func TestRequireSecretKey_AcceptsExistingKeyFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOCALITAS_SECRET_KEY", "")
	if err := os.MkdirAll(filepath.Join(home, ".localitas"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".localitas", "secret.key"), make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RequireSecretKey(); err != nil {
		t.Fatalf("RequireSecretKey with an existing key file: %v", err)
	}
}

func TestDecryptWithKey_RoundTripsEncrypt(t *testing.T) {
	keyB64 := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOCALITAS_SECRET_KEY", keyB64)
	encryptionKey = nil
	encryptionKeyOnce = sync.Once{}
	t.Cleanup(func() {
		encryptionKey = nil
		encryptionKeyOnce = sync.Once{}
	})

	ciphertext, err := Encrypt("refresh-token-value")
	if err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecryptWithKey(ciphertext, key)
	if err != nil {
		t.Fatalf("DecryptWithKey: %v", err)
	}
	if got != "refresh-token-value" {
		t.Errorf("DecryptWithKey = %q, want the plaintext", got)
	}
}
