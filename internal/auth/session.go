package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	Initialize(context.Context, Admin) error
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
	ErrCredentials        = errors.New("invalid credentials")
	ErrBusy               = errors.New("authentication capacity exceeded")
	ErrSetupRequired      = errors.New("administrator password must be set in the browser")
	ErrAlreadyInitialized = errors.New("administrator already initialized")
	ErrInvalidPassword    = errors.New("invalid password length or confirmation")
)

func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	_, err := s.repo.Admin(ctx)
	if errors.Is(err, ErrSetupRequired) {
		return true, nil
	}
	return false, err
}

// Setup 与登录共用哈希并发上限；数据库原子写入确保不能再次设置或覆盖密码。
func (s *Service) Setup(ctx context.Context, password, confirmation string) (string, *Session, error) {
	select {
	case s.logins <- struct{}{}:
		defer func() { <-s.logins }()
	default:
		return "", nil, ErrBusy
	}
	required, err := s.SetupRequired(ctx)
	if err != nil {
		return "", nil, err
	}
	if !required {
		return "", nil, ErrAlreadyInitialized
	}
	if len(password) < MinPasswordBytes || len(password) > MaxPasswordBytes || password != confirmation {
		return "", nil, ErrInvalidPassword
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", nil, err
	}
	admin := Admin{Username: "admin", PasswordHash: hash}
	if err := s.repo.Initialize(ctx, admin); err != nil {
		return "", nil, err
	}
	return s.newSession(admin)
}

func (s *Service) Login(ctx context.Context, password string) (string, *Session, error) {
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
	// 单管理员只验证密码；历史用户名仅留作内部会话和审计标识。
	if !VerifyPassword(admin.PasswordHash, password) {
		return "", nil, ErrCredentials
	}
	return s.newSession(admin)
}

func (s *Service) newSession(admin Admin) (string, *Session, error) {
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
