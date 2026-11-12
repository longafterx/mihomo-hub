package storage_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/longafterx/mihomo-hub/internal/auth"
	"github.com/longafterx/mihomo-hub/internal/storage"
)

func TestPrepareKeyProtectsExistingData(t *testing.T) {
	for _, state := range []string{"empty", "administrator", "instances"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			db, err := storage.Open(t.Context(), filepath.Join(dir, "hub.db"), 5000)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			switch state {
			case "administrator":
				err = (storage.Admins{DB: db}).Initialize(t.Context(), auth.Admin{
					Username: "legacy", PasswordHash: "existing-hash",
				})
			case "instances":
				_, err = db.Exec(`INSERT INTO instances
					(id,name,controller_url,secret_ciphertext,created_at_ms,updated_at_ms,generation)
					VALUES ('retained','retained','',x'010203',1,1,1)`)
			}
			if err != nil {
				t.Fatal(err)
			}
			keyPath := filepath.Join(dir, "secrets", "master.key")
			_, err = storage.PrepareKey(t.Context(), db, keyPath)
			if state != "empty" {
				if err == nil {
					t.Fatal("missing key was replaced despite existing data")
				}
				if _, err := os.Lstat(keyPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("generated a replacement key: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(keyPath)
			if err != nil || len(original) != 32 {
				t.Fatalf("invalid generated key: %v", err)
			}
			info, err := os.Stat(keyPath)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("unsafe key permissions: %v", err)
			}
			if _, err := storage.PrepareKey(t.Context(), db, keyPath); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(keyPath)
			if err != nil || !bytes.Equal(original, after) {
				t.Fatal("restart replaced the original key")
			}
			if err := os.WriteFile(keyPath, []byte("invalid"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := storage.PrepareKey(t.Context(), db, keyPath); err == nil {
				t.Fatal("corrupt key was replaced or accepted")
			}
		})
	}
}

func TestAdministratorSetupIsAtomic(t *testing.T) {
	db, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "hub.db"), 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := storage.Admins{DB: db}
	if _, err := repo.Admin(t.Context()); !errors.Is(err, auth.ErrSetupRequired) {
		t.Fatalf("empty database setup state: %v", err)
	}
	const attempts = 8
	start := make(chan struct{})
	results := make(chan error, attempts)
	var workers sync.WaitGroup
	for range attempts {
		workers.Go(func() {
			<-start
			results <- repo.Initialize(t.Context(), auth.Admin{Username: "admin", PasswordHash: "first-hash"})
		})
	}
	close(start)
	workers.Wait()
	close(results)
	var succeeded int
	for err := range results {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, auth.ErrAlreadyInitialized) {
			t.Fatal(err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful concurrent setups=%d, want 1", succeeded)
	}
	err = repo.Initialize(t.Context(), auth.Admin{Username: "other", PasswordHash: "replacement"})
	if !errors.Is(err, auth.ErrAlreadyInitialized) {
		t.Fatalf("setup overwrote existing administrator: %v", err)
	}
	admin, err := repo.Admin(t.Context())
	if err != nil || admin.Username != "admin" || admin.PasswordHash != "first-hash" {
		t.Fatalf("administrator changed: %+v %v", admin, err)
	}
	var audits int
	err = db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='auth.setup' AND result='success'`).Scan(&audits)
	if err != nil || audits != 1 {
		t.Fatalf("setup audits=%d: %v", audits, err)
	}
}

func TestAdministratorSetupRollsBackWhenAuditFails(t *testing.T) {
	db, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "hub.db"), 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TRIGGER fail_setup_audit BEFORE INSERT ON audit_events
		BEGIN SELECT RAISE(ABORT, 'fixture audit failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	repo := storage.Admins{DB: db}
	if err := repo.Initialize(t.Context(), auth.Admin{Username: "admin", PasswordHash: "fixture"}); err == nil {
		t.Fatal("setup succeeded without audit")
	}
	if _, err := repo.Admin(t.Context()); !errors.Is(err, auth.ErrSetupRequired) {
		t.Fatalf("failed setup left a password behind: %v", err)
	}
}
