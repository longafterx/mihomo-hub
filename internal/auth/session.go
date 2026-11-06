package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"sync"
	"time"
)

type Admin struct {
	Username     string
	PasswordHash string
}

type Repository interface {
	Admin(context.Context) (Admin, error)
}

type Session struct {
	Username  string
	CSRF      string
	ExpiresAt time.Time
	Context   context.Context
	cancel    context.CancelFunc
}

type Service struct {
	repo     Repository
	root     context.Context
	lifetime time.Duration
	mu       sync.Mutex
	sessions map[[32]byte]*Session
	// Bound simultaneous Argon2 memory use; overload fails without queueing.
	logins chan struct{}
}

func New(ctx context.Context, repo Repository, lifetime time.Duration) *Service {
	return &Service{
		repo: repo, root: ctx, lifetime: lifetime,
		sessions: map[[32]byte]*Session{}, logins: make(chan struct{}, 2),
	}
}

var (
	ErrCredentials = errors.New("invalid credentials")
	ErrBusy        = errors.New("authentication capacity exceeded")
)

func (s *Service) Login(ctx context.Context, username, password string) (string, *Session, error) {
	select {
	case s.logins <- struct{}{}:
		defer func() { <-s.logins }()
	default:
		return "", nil, ErrBusy
	}
	admin, err := s.repo.Admin(ctx)
	if err != nil {
		return "", nil, err
	}
	validPassword := VerifyPassword(admin.PasswordHash, password)
	validUsername := subtle.ConstantTimeCompare([]byte(username), []byte(admin.Username)) == 1
	if !validPassword || !validUsername {
		return "", nil, ErrCredentials
	}
	token := rand.Text()
	expires := time.Now().Add(s.lifetime)
	sessionCtx, cancel := context.WithDeadline(s.root, expires)
	session := &Session{
		Username: admin.Username, CSRF: rand.Text(), ExpiresAt: expires, Context: sessionCtx, cancel: cancel,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, old := range s.sessions {
		if old.Context.Err() != nil {
			old.cancel()
			delete(s.sessions, key)
		}
	}
	if len(s.sessions) >= 1024 {
		cancel()
		return "", nil, ErrBusy
	}
	s.sessions[sha256.Sum256([]byte(token))] = session
	return token, session, nil
}

func (s *Service) Lookup(token string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sha256.Sum256([]byte(token))]
	if !ok || session.Context.Err() != nil {
		return nil, false
	}
	return session, true
}

func (s *Service) Logout(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sha256.Sum256([]byte(token))
	if session, ok := s.sessions[key]; ok {
		session.cancel()
		delete(s.sessions, key)
	}
}
