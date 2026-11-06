package mihomo

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"syscall"
)

// ProbeError 仅携带固定诊断代码和安全提示，不包含地址、密钥或上游响应体。
type ProbeError struct {
	Code    string
	message string
}

func (e *ProbeError) Error() string { return e.message }

func probeFailure(code, message string) *ProbeError {
	return &ProbeError{Code: "controller_" + code, message: message}
}

func probeConnectionError(err error) *ProbeError {
	var networkError net.Error
	var dnsError *net.DNSError
	var certificateError *tls.CertificateVerificationError
	var recordError tls.RecordHeaderError
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &networkError) && networkError.Timeout():
		return probeFailure("timeout", "controller request timed out")
	case errors.Is(err, syscall.ECONNREFUSED):
		return probeFailure("connection_refused", "controller connection refused")
	case errors.As(err, &dnsError), errors.Is(err, ErrControllerDNS):
		return probeFailure("dns_failed", "controller DNS lookup failed")
	case errors.As(err, &certificateError), errors.As(err, &recordError), errors.As(err, &unknownAuthority), errors.As(err, &hostnameError):
		return probeFailure("tls_failed", "controller TLS verification failed")
	default:
		return probeFailure("unavailable", "controller unavailable")
	}
}
