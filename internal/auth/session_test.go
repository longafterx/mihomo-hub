package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/longafterx/mihomo-hub/internal/auth"
)

type accounts struct{ admin auth.Admin }

func (a accounts) Admin(context.Context) (auth.Admin, error) { return a.admin, nil }

func TestSessionLifecycle(t *testing.T) {
	hash, err := auth.HashPassword("test-password-with-length")
	if err != nil {
		t.Fatal(err)
	}
	root, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := auth.New(root, accounts{admin: auth.Admin{Username: "admin", PasswordHash: hash}}, time.Hour)
	if _, _, err := s.Login(t.Context(), "admin", "wrong"); !errors.Is(err, auth.ErrCredentials) {
		t.Fatalf("bad password: %v", err)
	}
	if _, _, err := s.Login(t.Context(), "wrong", "test-password-with-length"); !errors.Is(err, auth.ErrCredentials) {
		t.Fatalf("bad username: %v", err)
	}
	token, session, err := s.Login(t.Context(), "admin", "test-password-with-length")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Lookup(token); !ok {
		t.Fatal("valid token rejected")
	}
	if _, ok := s.Lookup("forged"); ok {
		t.Fatal("forged token accepted")
	}
	if !auth.ValidCSRF(session, session.CSRF) || auth.ValidCSRF(session, "forged") {
		t.Fatal("CSRF validation failed")
	}
	s.Logout(token)
	if _, ok := s.Lookup(token); ok || session.Context.Err() == nil {
		t.Fatal("logout did not revoke session and cancel active streams")
	}
	expired := auth.New(root, accounts{admin: auth.Admin{Username: "admin", PasswordHash: hash}}, -time.Second)
	token, _, err = expired.Login(t.Context(), "admin", "test-password-with-length")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := expired.Lookup(token); ok {
		t.Fatal("expired session accepted")
	}
}
