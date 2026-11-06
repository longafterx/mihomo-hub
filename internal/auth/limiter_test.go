package auth_test

import (
	"testing"
	"time"

	"github.com/longafterx/mihomo-hub/internal/auth"
)

func TestLimiter(t *testing.T) {
	t.Parallel()
	l := auth.NewLimiter(2, time.Minute)
	if !l.Allow("a") || !l.Allow("a") || l.Allow("a") || !l.Allow("b") {
		t.Fatal("rate limit or client isolation failed")
	}
}
