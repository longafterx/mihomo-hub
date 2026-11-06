package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// InitKey is explicit initialization only: serving never replaces a missing key.
func InitKey(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("generate master key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create master key: %w", err)
	}
	_, writeErr := f.Write(key)
	return errors.Join(writeErr, f.Close())
}

func LoadKey(path string) (cipher.AEAD, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect master key (run init first): %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("master key must be a regular file with mode 0600")
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	if len(key) != 32 {
		return nil, errors.New("master key must contain exactly 32 raw bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize secret cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

func Encrypt(aead cipher.AEAD, id, secret string) ([]byte, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate encryption nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, []byte(secret), []byte(id)), nil
}

func Decrypt(aead cipher.AEAD, id string, ciphertext []byte) (string, error) {
	if len(ciphertext) < aead.NonceSize() {
		return "", errors.New("invalid encrypted secret")
	}
	n := aead.NonceSize()
	plain, err := aead.Open(nil, ciphertext[:n], ciphertext[n:], []byte(id))
	if err != nil {
		return "", errors.New("cannot decrypt instance secret; check the master key")
	}
	return string(plain), nil
}
