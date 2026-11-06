package auth_test

import (
	"strings"
	"testing"

	"github.com/longafterx/mihomo-hub/internal/auth"
)

func TestHashPasswordLengthPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, password string
		valid          bool
	}{
		{name: "empty", password: "", valid: false},
		{name: "below minimum", password: "1234567", valid: false},
		{name: "eight digits without complexity", password: "12345678", valid: true},
		{name: "ten lowercase letters", password: "abcdefghij", valid: true},
		{name: "UTF8 bytes", password: "你好ab", valid: true},
		{name: "maximum", password: strings.Repeat("a", 1024), valid: true},
		{name: "above maximum", password: strings.Repeat("a", 1025), valid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := auth.HashPassword(tc.password)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t, error=%v", tc.valid, err)
			}
			if !tc.valid {
				if encoded != "" {
					t.Fatal("invalid password returned a hash")
				}
				return
			}
			if !auth.VerifyPassword(encoded, tc.password) {
				t.Fatal("new password cannot log in")
			}
			if auth.VerifyPassword(encoded, "wrong-password") {
				t.Fatal("wrong password accepted")
			}
		})
	}
}

func TestVerifyPasswordExistingHash(t *testing.T) {
	// 长度策略变化不改变 Argon2 格式，旧账号无需重新初始化或更换密码。
	encoded, err := auth.HashPassword("existing-long-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	if !auth.VerifyPassword(encoded, "existing-long-admin-password") {
		t.Fatal("existing password no longer accepted")
	}
	if auth.VerifyPassword(encoded, strings.Repeat("a", 1025)) {
		t.Fatal("oversized password accepted")
	}
	if auth.VerifyPassword("invalid-hash", "12345678") {
		t.Fatal("malformed hash accepted")
	}
}
