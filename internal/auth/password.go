package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// 管理员密码只用于平台网页登录，与各实例的 Controller Secret 独立。
// 只限制长度，不强制字符组合；CLI 提示与校验共用同一组边界。
const (
	MinPasswordBytes = 8
	MaxPasswordBytes = 1024
)

func HashPassword(password string) (string, error) {
	if len(password) < MinPasswordBytes || len(password) > MaxPasswordBytes {
		return "", fmt.Errorf("password must contain %d to %d bytes", MinPasswordBytes, MaxPasswordBytes)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	enc := base64.RawStdEncoding
	return "argon2id-v1$" + enc.EncodeToString(salt) + "$" + enc.EncodeToString(hash), nil
}

func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 3 || parts[0] != "argon2id-v1" || len(password) > MaxPasswordBytes {
		return false
	}
	salt, saltErr := base64.RawStdEncoding.DecodeString(parts[1])
	want, hashErr := base64.RawStdEncoding.DecodeString(parts[2])
	if saltErr != nil || hashErr != nil || len(salt) != 16 || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}
