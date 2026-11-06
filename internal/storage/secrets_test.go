package storage_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/longafterx/mihomo-hub/internal/storage"
)

func TestSecretEncryption(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "master.key")
	if err := storage.InitKey(path); err != nil {
		t.Fatal(err)
	}
	if err := storage.InitKey(path); err == nil {
		t.Fatal("existing key must never be overwritten")
	}
	aead, err := storage.LoadKey(path)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := storage.Encrypt(aead, "instance-a", "private-controller-secret")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("private-controller-secret")) {
		t.Fatal("secret stored in plaintext")
	}
	plain, err := storage.Decrypt(aead, "instance-a", encrypted)
	if err != nil || plain != "private-controller-secret" {
		t.Fatalf("round trip: %q %v", plain, err)
	}
	if _, err := storage.Decrypt(aead, "instance-b", encrypted); err == nil {
		t.Fatal("ciphertext must be bound to its instance")
	}
	encrypted[len(encrypted)-1] ^= 1
	if _, err := storage.Decrypt(aead, "instance-a", encrypted); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.LoadKey(path); err == nil {
		t.Fatal("insecure key permissions accepted")
	}
}
