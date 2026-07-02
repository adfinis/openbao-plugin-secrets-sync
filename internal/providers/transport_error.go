package providers

import (
	"context"
	"errors"
	"net"
	"net/url"
	"syscall"
)

type transportTimeoutError interface {
	error
	Timeout() bool
}

// ClassifyTransportError maps provider-boundary transport failures to a stable retryable class.
func ClassifyTransportError(err error) (ErrorClass, bool) {
	if err == nil {
		return "", false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorClassUnavailable, true
	}
	if errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.ETIMEDOUT) {
		return ErrorClassUnavailable, true
	}
	if timeoutErr, ok := errors.AsType[transportTimeoutError](err); ok && timeoutErr.Timeout() {
		return ErrorClassUnavailable, true
	}
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return ErrorClassUnavailable, true
	}
	if _, ok := errors.AsType[*url.Error](err); ok {
		return ErrorClassUnavailable, true
	}
	return "", false
}
