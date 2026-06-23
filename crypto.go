package client

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

var (
	encryptionKey     []byte
	encryptionKeyOnce sync.Once
)

func getOrCreateKey() ([]byte, error) {
	var err error
	encryptionKeyOnce.Do(func() {
		homeDir, _ := os.UserHomeDir()
		keyPath := filepath.Join(homeDir, ".localitas", "secret.key")

		data, readErr := os.ReadFile(keyPath)
		if readErr == nil && len(data) == 32 {
			encryptionKey = data
			return
		}

		key := make([]byte, 32)
		if _, err = io.ReadFull(rand.Reader, key); err != nil {
			return
		}

		os.MkdirAll(filepath.Dir(keyPath), 0700)
		if writeErr := os.WriteFile(keyPath, key, 0600); writeErr != nil {
			err = writeErr
			return
		}

		encryptionKey = key
	})
	if err != nil {
		return nil, err
	}
	if encryptionKey == nil {
		return nil, fmt.Errorf("encryption key not available")
	}
	return encryptionKey, nil
}

// Encrypt encrypts a plaintext string using AES-256-GCM with a key stored
// at ~/.localitas/secret.key. The key is auto-generated on first use.
// Returns a string prefixed with "enc:" followed by base64-encoded ciphertext.
// Returns empty string for empty input.
func Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}

	key, err := getOrCreateKey()
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return "enc:" + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a string previously encrypted with Encrypt.
// If the input doesn't start with "enc:", it is returned as-is (passthrough
// for plaintext values). Returns empty string for empty input.
func Decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}

	if len(encoded) < 4 || encoded[:4] != "enc:" {
		return encoded, nil
	}

	key, err := getOrCreateKey()
	if err != nil {
		return "", err
	}

	ciphertext, err := base64.StdEncoding.DecodeString(encoded[4:])
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decryption failed: %w", err)
	}

	return string(plaintext), nil
}
